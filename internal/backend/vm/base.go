// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/backend/vmhost"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

// The guest is a pinned Ubuntu 26.04 (resolute) server cloud image. The
// release and its digest change together; SHA256SUMS in the release directory
// lists the digest.
const (
	cloudImageRelease = "20261108"
	cloudImageURL     = "https://cloud-images.ubuntu.com/releases/resolute/release-" + cloudImageRelease + "/ubuntu-26.04-server-cloudimg-amd64.img"
	cloudImageSHA256  = "7b90593f5532b49b7e3fe3d2743f0d1f0d822e5b07cebcd8e00c0df8069ecab9"
)

const (
	baseDiskFile = "base.qcow2"
	// BaseStampFile marks a complete base and records what it was built from.
	BaseStampFile = "enclave-vm-base.json"

	baseDiskSizeEnv     = "ENCLAVE_VM_DISK_SIZE"
	defaultBaseDiskSize = "40G"
	provisionTimeout    = 45 * time.Minute
	provisionLogLimit   = 4 << 20

	provisionContextTag = "enclave-ctx"
	provisionResultFile = "result"
	provisionLogFile    = "provision.log"
)

// provisionContext lists what the guest's provisioning script reads from the
// build context.
var provisionContext = []string{"Dockerfile", "entrypoint.sh", "runtime-assets", "extensions", "docs"}

// BaseStamp is the metadata of a prewarmed base image.
type BaseStamp struct {
	Key   string `json:"key"`
	Tool  string `json:"tool"`
	Image string `json:"image"`
}

// BaseSpec describes a base image to provision.
type BaseSpec struct {
	// Dir is where the finished base is published.
	Dir  string
	Key  string
	Tool string
	// ContextDir is the image build context: Dockerfile, runtime assets,
	// extensions and entrypoint.
	ContextDir string
}

// base is a prewarmed base image project disks are created on.
type base struct {
	Disk string
	Key  string
}

// BaseKey identifies a base by everything that shapes its content: the image
// build inputs, the guest image, the provisioning recipe, and the host user
// the guest tool user mirrors.
func BaseKey(combinedHash string, recipeHash string, host model.Host) string {
	// The user-data is rendered with placeholders: its template is recipe,
	// its tool is already in combinedHash.
	return util.HashString(strings.Join([]string{
		combinedHash, cloudImageSHA256, recipeHash, provisionUserData("tool", "0", "0"), baseDiskSize(), host.UID, host.GID,
	}, "\n"))
}

// BaseDir is the managed location of a tool's base with the given key. Bases
// back project disks, so they are state: a cache cleanup must not take them.
func BaseDir(home string, tool string, key string) string {
	return filepath.Join(config.HostVMDir(home), "bases", tool, model.ShortHash(key))
}

// ReadBaseStamp reads the stamp of the base in dir. It reports os.ErrNotExist
// when dir holds no complete base.
func ReadBaseStamp(dir string) (BaseStamp, error) {
	var stamp BaseStamp
	data, err := os.ReadFile(filepath.Join(dir, BaseStampFile)) // #nosec G304 -- dir is a base directory chosen by the caller.
	if err != nil {
		return stamp, err
	}
	if err := json.Unmarshal(data, &stamp); err != nil {
		return stamp, fmt.Errorf("vm backend: parse %s: %w", filepath.Join(dir, BaseStampFile), err)
	}
	if _, err := os.Stat(filepath.Join(dir, baseDiskFile)); err != nil {
		return stamp, err
	}
	return stamp, nil
}

func resolveBase(image string) (base, error) {
	if strings.TrimSpace(image) == "" {
		return base{}, fmt.Errorf("vm backend: missing base image directory")
	}
	dir, err := filepath.Abs(image)
	if err != nil {
		return base{}, fmt.Errorf("vm backend: resolve base image %q: %w", image, err)
	}
	stamp, err := ReadBaseStamp(dir)
	if err != nil {
		return base{}, fmt.Errorf("vm backend: %s is not a prewarmed base image: %w", dir, err)
	}
	return base{Disk: filepath.Join(dir, baseDiskFile), Key: stamp.Key}, nil
}

func baseDiskSize() string {
	if size := strings.TrimSpace(os.Getenv(baseDiskSizeEnv)); size != "" {
		return size
	}
	return defaultBaseDiskSize
}

// BuildBase provisions a base image: it boots a copy of the cloud image once,
// lets the guest install the tool from the build context, and publishes the
// powered-off disk. A published base is never rebuilt in place, because the
// project disks created on it would silently become garbage.
func (b *Backend) BuildBase(ctx context.Context, spec BaseSpec) error {
	if err := b.Check(ctx); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(spec.Dir, baseDiskFile)); err == nil {
		return fmt.Errorf("vm backend: base image %s already exists and is immutable", spec.Dir)
	}
	seedTool, err := b.seedTool()
	if err != nil {
		return err
	}
	virtiofsd, err := b.findVirtiofsd()
	if err != nil {
		return err
	}
	caps, err := b.probeVirtiofsd(ctx, virtiofsd)
	if err != nil {
		return err
	}
	image, err := b.ensureCloudImage(ctx)
	if err != nil {
		return err
	}
	work := spec.Dir + ".tmp"
	if err := os.RemoveAll(work); err != nil {
		return fmt.Errorf("vm backend: clear base staging directory: %w", err)
	}
	if err := os.MkdirAll(work, 0o700); err != nil {
		return fmt.Errorf("vm backend: create base staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	disk := filepath.Join(work, baseDiskFile)
	for _, args := range [][]string{
		{"convert", "-O", "qcow2", image, disk},
		{"resize", "-q", disk, baseDiskSize()},
	} {
		if out, err := b.host.Output(ctx, qemuImgBinary, args...); err != nil {
			return fmt.Errorf("vm backend: qemu-img %s: %s: %w", args[0], strings.TrimSpace(string(out)), err)
		}
	}

	rt, err := newRuntimeDir("")
	if err != nil {
		return err
	}
	keepLogs := false
	defer func() {
		if !keepLogs {
			rt.remove()
		}
	}()
	files, err := rt.open()
	if err != nil {
		return err
	}
	shares, seed, err := b.prepareProvisioning(ctx, files, seedTool, caps, spec)
	if err != nil {
		return err
	}
	size := machineSize()
	args, err := qemuArgs(machine{
		Name:      "enclave-vm-base-" + spec.Tool,
		MemoryMiB: size.MemoryMiB,
		CPUs:      size.CPUs,
		Disk:      disk,
		Seed:      seed,
		Shares:    shares,
		Console:   files.console(),
		socket:    files.socket,
	})
	if err != nil {
		return err
	}
	stopExports, err := b.startExports(ctx, virtiofsd, caps, files, shares)
	if err != nil {
		return err
	}
	logx.Infof("Provisioning the vm base image for %s. This takes several minutes on first run.", spec.Tool)
	logx.Debugf("vm backend: provisioning args: %s %s", qemuBinary, strings.Join(args, " "))
	provisionCtx, cancel := context.WithTimeout(ctx, provisionTimeout)
	out, runErr := b.host.Output(provisionCtx, qemuBinary, args...)
	cancel()
	stopExports()
	if err := provisionResult(rt, out, runErr); err != nil {
		keepLogs = true
		return fmt.Errorf("%w\nlogs are kept in %s", err, rt.name())
	}

	if err := os.Chmod(disk, 0o400); err != nil {
		return fmt.Errorf("vm backend: protect base image: %w", err)
	}
	stamp, err := json.MarshalIndent(BaseStamp{Key: spec.Key, Tool: spec.Tool, Image: cloudImageSHA256}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(work, BaseStampFile), append(stamp, '\n'), 0o600); err != nil {
		return fmt.Errorf("vm backend: write base stamp: %w", err)
	}
	if err := os.Rename(work, spec.Dir); err != nil {
		return fmt.Errorf("vm backend: publish base image: %w", err)
	}
	return nil
}

// prepareProvisioning stages what the provisioning boot needs: a private copy
// of the build context, an export for the result, and the NoCloud seed.
func (b *Backend) prepareProvisioning(ctx context.Context, rt runtimeFiles, seedTool []string, caps virtiofsdCaps, spec BaseSpec) ([]share, string, error) {
	contextDir := filepath.Join(rt.dir, "ctx")
	for _, entry := range provisionContext {
		source := filepath.Join(spec.ContextDir, entry)
		if _, err := os.Stat(source); os.IsNotExist(err) && entry == "docs" {
			continue
		}
		if err := util.CopyTree(source, filepath.Join(contextDir, entry), vmhost.CopyFile); err != nil {
			return nil, "", fmt.Errorf("vm backend: stage build context: %w", err)
		}
	}
	// The guest runs as root before the tool user exists and virtiofsd cannot
	// create files for another user, so the files it writes are created here.
	for _, name := range []string{provisionResultFile, provisionLogFile} {
		if err := os.WriteFile(filepath.Join(rt.out(), name), nil, 0o600); err != nil {
			return nil, "", err
		}
	}
	seedDir := filepath.Join(rt.dir, "seed")
	if err := os.Mkdir(seedDir, 0o700); err != nil {
		return nil, "", err
	}
	userData := filepath.Join(seedDir, "user-data")
	metaData := filepath.Join(seedDir, "meta-data")
	if err := os.WriteFile(userData, []byte(provisionUserData(spec.Tool, b.opts.Host.UID, b.opts.Host.GID)), 0o600); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(metaData, []byte("instance-id: enclave-vm-base\nlocal-hostname: enclave-vm\n"), 0o600); err != nil {
		return nil, "", err
	}
	seed := filepath.Join(rt.dir, "seed.iso")
	args := seedArgs(seedTool, seed, userData, metaData)
	if out, err := b.host.Output(ctx, args[0], args[1:]...); err != nil {
		return nil, "", fmt.Errorf("vm backend: build cloud-init seed with %s: %s: %w", args[0], strings.TrimSpace(string(out)), err)
	}
	// The context is a private copy, so it stays on virtiofs, which the
	// pristine cloud image can mount, even where read-only is not enforced.
	shares := []share{
		{Tag: provisionContextTag, Source: contextDir, ReadOnly: caps.ReadOnly, Transport: transportVirtiofs},
		{Tag: controlOutTag, Source: rt.out(), Transport: transportVirtiofs},
	}
	return shares, seed, nil
}

// seedTool returns the command prefix of the NoCloud seed builder this host
// has.
func (b *Backend) seedTool() ([]string, error) {
	for _, tool := range [][]string{{"cloud-localds"}, {"genisoimage"}, {"xorriso", "-as", "genisoimage"}} {
		if _, err := b.host.LookPath(tool[0]); err == nil {
			return tool, nil
		}
	}
	return nil, fmt.Errorf("vm backend: building a base image needs cloud-localds, genisoimage or xorriso on the host")
}

// seedArgs completes the seed builder's command line. cloud-init finds the
// seed by its volume label and reads user-data and meta-data from it.
func seedArgs(tool []string, seed string, userData string, metaData string) []string {
	if tool[0] == "cloud-localds" {
		return append(append([]string(nil), tool...), seed, userData, metaData)
	}
	return append(append([]string(nil), tool...), "-quiet", "-output", seed, "-volid", "cidata", "-joliet", "-rock", userData, metaData)
}

// provisionUserData is the cloud-init configuration of the provisioning boot.
// It creates no login: the guest mounts the build context, runs the
// provisioning script, reports the result and powers off.
func provisionUserData(tool string, uid string, gid string) string {
	script := []string{
		"mkdir -p /run/enclave-provision/ctx /run/enclave-provision/out",
		"modprobe virtiofs 2>/dev/null || true",
		"mount -t virtiofs -o ro " + provisionContextTag + " /run/enclave-provision/ctx",
		"mount -t virtiofs " + controlOutTag + " /run/enclave-provision/out",
		"status=0",
		"bash /run/enclave-provision/ctx/runtime-assets/vm/base/provision.sh /run/enclave-provision/ctx " +
			util.ShellQuote(tool) + " " + util.ShellQuote(uid) + " " + util.ShellQuote(gid) +
			" >/run/enclave-provision/out/" + provisionLogFile + " 2>&1 || status=$?",
		`echo "$status" >/run/enclave-provision/out/` + provisionResultFile,
		"sync",
		"poweroff",
	}
	var out strings.Builder
	out.WriteString("#cloud-config\nusers: []\nssh_pwauth: false\nruncmd:\n  - - bash\n    - -c\n    - |\n")
	for _, line := range script {
		out.WriteString("      " + line + "\n")
	}
	return out.String()
}

// provisionResult accepts a base only when the guest reported success and
// QEMU then exited by itself. The guest reports before it syncs and powers
// off, so a QEMU that was interrupted or timed out after the report leaves a
// disk that may lack the last writes.
func provisionResult(rt runtimeDir, qemuOutput []byte, runErr error) error {
	result, _, err := rt.readOut(provisionResultFile, guestStatusLimit)
	if err != nil {
		return err
	}
	result = strings.TrimSpace(result)
	qemu := strings.TrimSpace(string(qemuOutput))
	if runErr != nil {
		qemu = strings.TrimSpace(qemu + " " + runErr.Error())
	}
	switch {
	case result == "0" && runErr == nil:
		return nil
	case result == "0":
		return fmt.Errorf("vm backend: the guest finished provisioning, but the VM did not shut down cleanly (%s)", qemu)
	case result == "":
		return fmt.Errorf("vm backend: the guest did not report a provisioning result (%s)%s", qemu, rt.consoleTail(20))
	}
	log, _, _ := rt.readOut(provisionLogFile, provisionLogLimit)
	return fmt.Errorf("vm backend: provisioning failed with status %s%s", sanitize(result), tailText(log, 30))
}

// ensureCloudImage returns the verified cloud image, downloading it once.
func (b *Backend) ensureCloudImage(ctx context.Context) (string, error) {
	dir := filepath.Join(config.HostVMDir(b.opts.Host.Home), "images")
	path := filepath.Join(dir, cloudImageSHA256+".img")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("vm backend: create image directory: %w", err)
	}
	logx.Infof("Downloading the Ubuntu cloud image (%s).", cloudImageURL)
	staging, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(staging.Name()) }()
	digest := sha256.New()
	err = b.host.Download(ctx, cloudImageURL, io.MultiWriter(staging, digest))
	if closeErr := staging.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("vm backend: download cloud image: %w", err)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != cloudImageSHA256 {
		return "", fmt.Errorf("vm backend: cloud image digest is %s, want %s", got, cloudImageSHA256)
	}
	if err := os.Rename(staging.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
