// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/backend/vmhost"
	"enclave/internal/model"
)

const (
	// BundleConfigFile is the metadata file next to vmlinuz/initramfs that
	// declares host-side QEMU launch settings for a bundle.
	BundleConfigFile = "enclave-vm-bundle.json"
	// InitramfsFile is the guest initramfs of a generated bundle, a
	// zstd-compressed newc cpio archive.
	InitramfsFile = "initramfs.cpio.zst"
	// LegacyInitramfsFile is the uncompressed initramfs name, still accepted for
	// hand-assembled bundles passed via --image-name.
	LegacyInitramfsFile = "initramfs.cpio"
	// InitramfsMetaFile records the built initramfs sizes. The host needs the
	// unpacked size to size guest memory and cannot derive it from the
	// compressed image.
	InitramfsMetaFile = "enclave-vm-initramfs.json"
	// DefaultMemoryMiB is the generated-bundle memory default and the fallback
	// for minimal/prebuilt bundles without BundleConfigFile.
	DefaultMemoryMiB = 4096

	// Guest memory has to cover the unpacked initramfs for the whole session:
	// it becomes the rootfs, a ramfs the guest can never reclaim. During boot
	// the still-compressed image is resident on top of it, and the boot slack
	// covers the kernel plus early allocations; once unpacking is done that
	// memory is freed and the workload budget is what the agent tool gets.
	initramfsBootSlackMiB = 256
	initramfsWorkloadMiB  = 2048
	memoryGranularityMiB  = 512

	bytesPerMiB = 1 << 20
	// initramfsSegmentAlignment is the boundary an appended plain cpio segment
	// must start on for the kernel's unpack_to_rootfs() to recognize it.
	initramfsSegmentAlignment = 4

	guestControlPath       = "/run/enclave/control"
	guestFilesPath         = "/run/enclave/files"
	guestExitCodePath      = guestControlPath + "/exit-code"
	guestRunScriptPath     = "/etc/enclave/run.sh"
	qemuNetdevID           = "enclave-net0"
	qemuGuestMessagePrefix = "enclave qemu backend: "
)

// BundleConfig is the host-side metadata stored next to a generated or
// prebuilt QEMU bundle. MemoryMiB is the declared memory floor; the launch
// size is raised to whatever the bundle's initramfs needs.
type BundleConfig struct {
	MemoryMiB int `json:"memoryMiB"`
}

// InitramfsMeta is the size metadata the bundle builder writes next to the
// compressed initramfs.
type InitramfsMeta struct {
	UncompressedBytes int64  `json:"uncompressedBytes"`
	CompressedBytes   int64  `json:"compressedBytes"`
	Compression       string `json:"compression"`
}

type bundle struct {
	Root      string
	Kernel    string
	Initramfs string
	MemoryMiB int
}

type guestRuntime struct {
	TempDir          string
	RuntimeInitramfs string
	ExitCodePath     string
	Mounts           []runtimeMount
	FileMounts       []runtimeFileMount
}

type (
	runtimeMount     = vmhost.DirMount
	runtimeFileMount = vmhost.FileMount
)

func resolveBundle(image string) (bundle, error) {
	if strings.TrimSpace(image) == "" {
		return bundle{}, fmt.Errorf("qemu backend: missing microvm bundle path")
	}
	resolved, err := filepath.Abs(image)
	if err != nil {
		return bundle{}, fmt.Errorf("qemu backend: resolve bundle path %q: %w", image, err)
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return bundle{}, fmt.Errorf("qemu backend: bundle path %q does not exist", resolved)
		}
		return bundle{}, fmt.Errorf("qemu backend: inspect bundle path %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return bundle{}, fmt.Errorf("qemu backend: bundle path %q must be a directory containing vmlinuz and %s", resolved, InitramfsFile)
	}
	kernel, err := requireRegularFile(filepath.Join(resolved, "vmlinuz"), "kernel")
	if err != nil {
		return bundle{}, err
	}
	initramfs, err := InitramfsPath(resolved)
	if err != nil {
		return bundle{}, err
	}
	memoryMiB, err := resolveBundleMemoryMiB(resolved)
	if err != nil {
		return bundle{}, err
	}
	return bundle{Root: resolved, Kernel: kernel, Initramfs: initramfs, MemoryMiB: memoryMiB}, nil
}

// InitramfsPath resolves a bundle's initramfs, preferring the compressed image
// over the uncompressed legacy name.
func InitramfsPath(root string) (string, error) {
	compressed := filepath.Join(root, InitramfsFile)
	if info, err := os.Stat(compressed); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("qemu backend: initramfs %s is not a regular file", compressed)
		}
		return compressed, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("qemu backend: inspect initramfs %s: %w", compressed, err)
	}
	legacy := filepath.Join(root, LegacyInitramfsFile)
	if _, err := os.Stat(legacy); err != nil && os.IsNotExist(err) {
		return "", fmt.Errorf("qemu backend: missing initramfs %s", compressed)
	}
	return requireRegularFile(legacy, "initramfs")
}

// RequiredMemoryMiB is the guest memory a bundle with the given initramfs sizes
// needs: enough to unpack the image at boot, and enough to keep the unpacked
// rootfs resident next to the running agent tool afterwards.
func RequiredMemoryMiB(uncompressedBytes int64, compressedBytes int64) int {
	unpacked := bytesToMiB(uncompressedBytes)
	boot := unpacked + bytesToMiB(compressedBytes) + initramfsBootSlackMiB
	workload := unpacked + initramfsWorkloadMiB
	required := boot
	if workload > required {
		required = workload
	}
	return roundUpMiB(required, memoryGranularityMiB)
}

func bytesToMiB(size int64) int {
	if size <= 0 {
		return 0
	}
	return int((size + bytesPerMiB - 1) / bytesPerMiB)
}

func roundUpMiB(value int, granularity int) int {
	if value <= 0 {
		return granularity
	}
	return ((value + granularity - 1) / granularity) * granularity
}

// ReadInitramfsMeta reads the size metadata the bundle builder wrote. It
// reports os.ErrNotExist for bundles built without it.
func ReadInitramfsMeta(root string) (InitramfsMeta, error) {
	path := filepath.Join(root, InitramfsMetaFile)
	data, err := os.ReadFile(path) // #nosec G304 -- path is inside a validated bundle directory.
	if err != nil {
		return InitramfsMeta{}, err
	}
	var meta InitramfsMeta
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return InitramfsMeta{}, fmt.Errorf("qemu backend: parse initramfs metadata %s: %w", path, err)
	}
	if meta.UncompressedBytes <= 0 {
		return InitramfsMeta{}, fmt.Errorf("qemu backend: initramfs metadata %s has invalid uncompressedBytes %d", path, meta.UncompressedBytes)
	}
	return meta, nil
}

func requireRegularFile(path string, label string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("qemu backend: missing %s %s", label, path)
		}
		return "", fmt.Errorf("qemu backend: inspect %s %s: %w", label, path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("qemu backend: %s %s is not a regular file", label, path)
	}
	return path, nil
}

// ReadBundleConfig reads a bundle's host-side launch metadata. It reports
// os.ErrNotExist for minimal or prebuilt bundles that ship without it.
func ReadBundleConfig(root string) (BundleConfig, error) {
	path := filepath.Join(root, BundleConfigFile)
	data, err := os.ReadFile(path) // #nosec G304 -- path is inside a validated bundle directory.
	if err != nil {
		if os.IsNotExist(err) {
			return BundleConfig{}, err
		}
		return BundleConfig{}, fmt.Errorf("qemu backend: read bundle config %s: %w", path, err)
	}
	var cfg BundleConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return BundleConfig{}, fmt.Errorf("qemu backend: parse bundle config %s: %w", path, err)
	}
	if cfg.MemoryMiB <= 0 {
		return BundleConfig{}, fmt.Errorf("qemu backend: bundle config %s has invalid memoryMiB %d", path, cfg.MemoryMiB)
	}
	return cfg, nil
}

// resolveBundleMemoryMiB raises the bundle's declared memory floor to what its
// initramfs needs. The kernel unpacks the image into a ramfs that stays
// resident as the guest rootfs, so a large bundle needs proportionally more
// memory; too little and the guest dies in the unpacker ("Initramfs unpacking
// failed: write error", then a no-init panic).
func resolveBundleMemoryMiB(root string) (int, error) {
	memoryMiB := DefaultMemoryMiB
	cfg, err := ReadBundleConfig(root)
	if err == nil {
		memoryMiB = cfg.MemoryMiB
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	meta, err := ReadInitramfsMeta(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return memoryMiB, nil
		}
		return 0, err
	}
	return max(memoryMiB, RequiredMemoryMiB(meta.UncompressedBytes, meta.CompressedBytes)), nil
}

func (b *Backend) prepareGuestRuntime(bundle bundle, req backend.Request, console consoleSize) (guestRuntime, error) {
	// Before anything inspects the sources: prepareFileMounts stats every bind
	// source and rejects a missing one, so a later creation pass would never run
	// for the case CreateSourceDir exists for.
	planner := b.mountPlanner()
	if err := planner.CreateDisposableSources(req.Mounts); err != nil {
		return guestRuntime{}, err
	}
	tempDir, err := os.MkdirTemp("", "enclave-qemu-*")
	if err != nil {
		return guestRuntime{}, fmt.Errorf("qemu backend: create runtime directory: %w", err)
	}
	cleanupOnErr := func() {
		_ = os.RemoveAll(tempDir)
	}
	controlDir := filepath.Join(tempDir, "control")
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		cleanupOnErr()
		return guestRuntime{}, fmt.Errorf("qemu backend: create control directory: %w", err)
	}
	fileStageSource := filepath.Join(tempDir, "files")
	fileMounts, err := planner.FileMounts(fileStageSource, guestFilesPath, req.Mounts)
	if err != nil {
		cleanupOnErr()
		return guestRuntime{}, err
	}
	if len(fileMounts) == 0 {
		fileStageSource = ""
	}
	mounts, err := b.buildRuntimeMounts(req, controlDir, fileStageSource)
	if err != nil {
		cleanupOnErr()
		return guestRuntime{}, err
	}
	overlayRoot := filepath.Join(tempDir, "overlay")
	runScriptPath := filepath.Join(overlayRoot, strings.TrimPrefix(guestRunScriptPath, "/"))
	if err := os.MkdirAll(filepath.Dir(runScriptPath), 0o755); err != nil { // #nosec G301 -- packed into initramfs and must be guest-traversable.
		cleanupOnErr()
		return guestRuntime{}, fmt.Errorf("qemu backend: create overlay directory: %w", err)
	}
	content, err := b.renderRunScript(req, mounts, fileMounts, console)
	if err != nil {
		cleanupOnErr()
		return guestRuntime{}, err
	}
	if err := os.WriteFile(runScriptPath, []byte(content), 0o755); err != nil { // #nosec G306 -- guest run script must be executable in initramfs.
		cleanupOnErr()
		return guestRuntime{}, fmt.Errorf("qemu backend: write run script: %w", err)
	}
	overlayInitramfs := filepath.Join(tempDir, "overlay.cpio")
	if err := createCPIOArchive(overlayRoot, overlayInitramfs); err != nil {
		cleanupOnErr()
		return guestRuntime{}, err
	}
	runtimeInitramfs := filepath.Join(tempDir, "initramfs.img")
	if err := concatenateFiles(runtimeInitramfs, bundle.Initramfs, overlayInitramfs); err != nil {
		cleanupOnErr()
		return guestRuntime{}, err
	}
	return guestRuntime{
		TempDir:          tempDir,
		RuntimeInitramfs: runtimeInitramfs,
		ExitCodePath:     filepath.Join(controlDir, "exit-code"),
		Mounts:           mounts,
		FileMounts:       fileMounts,
	}, nil
}

func (b *Backend) mountPlanner() vmhost.MountPlanner {
	return vmhost.MountPlanner{
		Backend:  backend.NameQEMU,
		Stores:   b.storage,
		Reserved: []string{guestControlPath, guestFilesPath},
	}
}

func (b *Backend) buildRuntimeMounts(req backend.Request, controlDir string, fileStageSource string) ([]runtimeMount, error) {
	mounts, err := b.mountPlanner().DirMounts(req)
	if err != nil {
		return nil, err
	}
	if fileStageSource != "" {
		mounts = append(mounts, runtimeMount{ID: "files", Tag: "enclave-files", Source: fileStageSource, Target: guestFilesPath})
	}
	mounts = append(mounts, runtimeMount{ID: "control", Tag: "enclave-control", Source: controlDir, Target: guestControlPath})
	// Parent mounts must precede children, including late-added config anchors.
	vmhost.SortParentsFirst(mounts)
	return mounts, nil
}

func createCPIOArchive(sourceDir string, output string) error {
	out, err := os.Create(output) // #nosec G304 -- output is under a generated runtime directory.
	if err != nil {
		return fmt.Errorf("qemu backend: create overlay archive: %w", err)
	}
	defer func() { _ = out.Close() }()
	cmd := exec.Command("sh", "-c", "find . -print | cpio -o -H newc --quiet")
	cmd.Dir = sourceDir
	cmd.Stdout = out
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(stderr.String())
		if text == "" {
			text = err.Error()
		}
		return fmt.Errorf("qemu backend: build overlay initramfs with cpio: %s", text)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("qemu backend: close overlay archive: %w", err)
	}
	return nil
}

// concatenateFiles builds the initrd the guest boots from: the bundle image
// followed by the per-run overlay archive. Every segment starts on a 4-byte
// boundary because the kernel's unpack_to_rootfs() only detects a plain cpio
// segment there; it skips the padding bytes in between.
func concatenateFiles(output string, inputs ...string) error {
	out, err := os.Create(output) // #nosec G304 -- output is under a generated runtime directory.
	if err != nil {
		return fmt.Errorf("qemu backend: create runtime initramfs: %w", err)
	}
	defer func() { _ = out.Close() }()
	written := int64(0)
	for _, input := range inputs {
		if pad := segmentPadding(written); pad > 0 {
			padded, err := out.Write(make([]byte, pad))
			if err != nil {
				return fmt.Errorf("qemu backend: pad runtime initramfs: %w", err)
			}
			written += int64(padded)
		}
		file, err := os.Open(input) // #nosec G304 -- inputs are validated bundle/generated files.
		if err != nil {
			return fmt.Errorf("qemu backend: open initramfs part %s: %w", input, err)
		}
		copied, err := io.Copy(out, file)
		written += copied
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("qemu backend: append initramfs part %s: %w", input, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("qemu backend: close initramfs part %s: %w", input, err)
		}
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("qemu backend: close runtime initramfs: %w", err)
	}
	return nil
}

func segmentPadding(offset int64) int {
	remainder := offset % initramfsSegmentAlignment
	if remainder == 0 {
		return 0
	}
	return initramfsSegmentAlignment - int(remainder)
}

func formatHostfwd(port backend.PortMapping) (string, error) {
	return vmhost.FormatHostfwd(backend.NameQEMU, port)
}

func qemuName(req backend.Request) string {
	if strings.TrimSpace(req.Session.Name) != "" {
		return req.Session.Name
	}
	parts := []string{model.AppName, req.Session.Tool, req.Session.ProjectHash}
	compact := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			compact = append(compact, strings.TrimSpace(part))
		}
	}
	return strings.Join(compact, "-")
}
