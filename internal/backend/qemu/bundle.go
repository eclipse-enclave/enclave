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
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/model"
)

const (
	// BundleConfigFile is the metadata file next to vmlinuz/initramfs that
	// declares host-side QEMU launch settings for a bundle.
	BundleConfigFile = "enclave-vm-bundle.json"
	// DefaultMemoryMiB is the generated-bundle memory default and the fallback
	// for minimal/prebuilt bundles without BundleConfigFile.
	DefaultMemoryMiB = 4096

	guestControlPath       = "/run/enclave/control"
	guestFilesPath         = "/run/enclave/files"
	guestExitCodePath      = guestControlPath + "/exit-code"
	guestRunScriptPath     = "/etc/enclave/run.sh"
	qemuNetdevID           = "enclave-net0"
	qemuGuestMessagePrefix = "enclave qemu backend: "
)

// BundleConfig is the host-side metadata stored next to a generated or
// prebuilt QEMU bundle.
type BundleConfig struct {
	MemoryMiB int `json:"memoryMiB"`
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

type runtimeMount struct {
	ID        string
	Tag       string
	Source    string
	Target    string
	ReadOnly  bool
	CacheMmap bool
}

type runtimeFileMount struct {
	Source      string
	Target      string
	GuestSource string
	GuestResult string
	ResultPath  string
	ReadOnly    bool
}

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
		return bundle{}, fmt.Errorf("qemu backend: bundle path %q must be a directory containing vmlinuz and initramfs.cpio", resolved)
	}
	kernel, err := requireRegularFile(filepath.Join(resolved, "vmlinuz"), "kernel")
	if err != nil {
		return bundle{}, err
	}
	initramfs, err := requireRegularFile(filepath.Join(resolved, "initramfs.cpio"), "initramfs")
	if err != nil {
		return bundle{}, err
	}
	memoryMiB, err := resolveBundleMemoryMiB(resolved)
	if err != nil {
		return bundle{}, err
	}
	return bundle{Root: resolved, Kernel: kernel, Initramfs: initramfs, MemoryMiB: memoryMiB}, nil
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

func resolveBundleMemoryMiB(root string) (int, error) {
	path := filepath.Join(root, BundleConfigFile)
	data, err := os.ReadFile(path) // #nosec G304 -- path is inside a validated bundle directory.
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultMemoryMiB, nil
		}
		return 0, fmt.Errorf("qemu backend: read bundle config %s: %w", path, err)
	}
	var cfg BundleConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return 0, fmt.Errorf("qemu backend: parse bundle config %s: %w", path, err)
	}
	if cfg.MemoryMiB <= 0 {
		return 0, fmt.Errorf("qemu backend: bundle config %s has invalid memoryMiB %d", path, cfg.MemoryMiB)
	}
	return cfg.MemoryMiB, nil
}

func (b *Backend) prepareGuestRuntime(bundle bundle, req backend.Request) (guestRuntime, error) {
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
	fileMounts, fileStageSource, err := prepareFileMounts(tempDir, req.Mounts)
	if err != nil {
		cleanupOnErr()
		return guestRuntime{}, err
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
	content, err := b.renderRunScript(req, mounts, fileMounts)
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
	runtimeInitramfs := filepath.Join(tempDir, "initramfs.cpio")
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

func (b *Backend) buildRuntimeMounts(req backend.Request, controlDir string, fileStageSource string) ([]runtimeMount, error) {
	mounts := make([]runtimeMount, 0, len(req.Mounts)+len(req.Stores)+2)
	idx := 0
	for _, mount := range req.Mounts {
		if mount.Type != "" && mount.Type != backend.MountTypeBind {
			return nil, fmt.Errorf("qemu backend: unsupported mount type %q", mount.Type)
		}
		info, err := os.Stat(mount.Source)
		if err != nil {
			return nil, fmt.Errorf("qemu backend: inspect mount source %q: %w", mount.Source, err)
		}
		if !info.IsDir() {
			continue
		}
		if err := validateGuestPath(mount.ContainerPath, "mount target"); err != nil {
			return nil, err
		}
		mounts = append(mounts, runtimeMount{
			ID:       fmt.Sprintf("mount-%d", idx),
			Tag:      fmt.Sprintf("enclave-mount-%d", idx),
			Source:   filepath.Clean(mount.Source),
			Target:   filepath.Clean(mount.ContainerPath),
			ReadOnly: mount.ReadOnly,
		})
		idx++
	}
	for _, store := range req.Stores {
		if strings.TrimSpace(store.ContainerPath) == "" {
			continue
		}
		if err := validateGuestPath(store.ContainerPath, "store target"); err != nil {
			return nil, err
		}
		source, err := b.storage.MountSource(store.Key, store.Kind)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, runtimeMount{
			ID:        fmt.Sprintf("store-%d", idx),
			Tag:       fmt.Sprintf("enclave-store-%d", idx),
			Source:    source,
			Target:    filepath.Clean(store.ContainerPath),
			ReadOnly:  store.ReadOnly,
			CacheMmap: store.CacheMmap,
		})
		idx++
	}
	if fileStageSource != "" {
		mounts = append(mounts, runtimeMount{ID: "files", Tag: "enclave-files", Source: fileStageSource, Target: guestFilesPath})
	}
	mounts = append(mounts, runtimeMount{ID: "control", Tag: "enclave-control", Source: controlDir, Target: guestControlPath})
	return mounts, nil
}

func prepareFileMounts(tempDir string, mounts []backend.Mount) ([]runtimeFileMount, string, error) {
	root := filepath.Join(tempDir, "files")
	var prepared []runtimeFileMount
	for _, mount := range mounts {
		if mount.Type != "" && mount.Type != backend.MountTypeBind {
			continue
		}
		info, err := os.Stat(mount.Source)
		if err != nil {
			return nil, "", fmt.Errorf("qemu backend: inspect file mount source %q: %w", mount.Source, err)
		}
		if info.IsDir() {
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, "", fmt.Errorf("qemu backend: mount source %q must be a directory or regular file", mount.Source)
		}
		if err := validateGuestPath(mount.ContainerPath, "file mount target"); err != nil {
			return nil, "", err
		}
		entryRoot := filepath.Join(root, fmt.Sprintf("%02d", len(prepared)))
		if err := os.MkdirAll(entryRoot, 0o700); err != nil {
			return nil, "", fmt.Errorf("qemu backend: create file mount staging entry: %w", err)
		}
		sourcePath := filepath.Join(entryRoot, "source")
		resultPath := filepath.Join(entryRoot, "result")
		if err := copyFile(mount.Source, sourcePath, info.Mode().Perm()); err != nil {
			return nil, "", fmt.Errorf("qemu backend: stage file mount source: %w", err)
		}
		if err := copyFile(mount.Source, resultPath, info.Mode().Perm()); err != nil {
			return nil, "", fmt.Errorf("qemu backend: stage file mount result: %w", err)
		}
		guestEntry := filepath.Join(guestFilesPath, fmt.Sprintf("%02d", len(prepared)))
		prepared = append(prepared, runtimeFileMount{
			Source:      mount.Source,
			Target:      filepath.Clean(mount.ContainerPath),
			GuestSource: filepath.Join(guestEntry, "source"),
			GuestResult: filepath.Join(guestEntry, "result"),
			ResultPath:  resultPath,
			ReadOnly:    mount.ReadOnly,
		})
	}
	if len(prepared) == 0 {
		return nil, "", nil
	}
	return prepared, root, nil
}

func validateGuestPath(path string, label string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("qemu backend: %s is empty", label)
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) || cleaned == string(filepath.Separator) {
		return fmt.Errorf("qemu backend: %s %q must be an absolute non-root path", label, path)
	}
	for _, reserved := range []string{guestControlPath, guestFilesPath} {
		if cleaned == reserved || strings.HasPrefix(cleaned, reserved+string(filepath.Separator)) {
			return fmt.Errorf("qemu backend: %s %q overlaps reserved guest path %s", label, path, reserved)
		}
	}
	return nil
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

func concatenateFiles(output string, inputs ...string) error {
	out, err := os.Create(output) // #nosec G304 -- output is under a generated runtime directory.
	if err != nil {
		return fmt.Errorf("qemu backend: create runtime initramfs: %w", err)
	}
	defer func() { _ = out.Close() }()
	for _, input := range inputs {
		file, err := os.Open(input) // #nosec G304 -- inputs are validated bundle/generated files.
		if err != nil {
			return fmt.Errorf("qemu backend: open initramfs part %s: %w", input, err)
		}
		if _, err := io.Copy(out, file); err != nil {
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

func persistFileMounts(files []runtimeFileMount) []error {
	var errs []error
	for _, file := range files {
		if file.ReadOnly {
			continue
		}
		data, err := os.ReadFile(file.ResultPath) // #nosec G304 -- result path is generated under the runtime directory.
		if err != nil {
			errs = append(errs, fmt.Errorf("persist file mount %s: %w", file.Target, err))
			continue
		}
		info, statErr := os.Stat(file.Source)
		mode := fsMode(info, statErr)
		if err := os.WriteFile(file.Source, data, mode); err != nil { // #nosec G306 G304 G703 -- source was supplied as a validated mount source.
			errs = append(errs, fmt.Errorf("persist file mount %s: %w", file.Target, err))
		}
	}
	return errs
}

func fsMode(info os.FileInfo, err error) os.FileMode {
	if err == nil {
		return info.Mode().Perm()
	}
	return 0o600
}

func formatHostfwd(port backend.PortMapping) (string, error) {
	protocol := strings.ToLower(strings.TrimSpace(port.Protocol))
	if protocol != "" && protocol != "tcp" {
		return "", fmt.Errorf("qemu backend: only tcp port forwarding is supported")
	}
	hostPort, err := strconv.Atoi(strings.TrimSpace(port.HostPort))
	if err != nil || hostPort < 1 || hostPort > 65535 {
		return "", fmt.Errorf("qemu backend: invalid host port %q", port.HostPort)
	}
	containerPort, err := strconv.Atoi(strings.TrimSpace(port.ContainerPort))
	if err != nil || containerPort < 1 || containerPort > 65535 {
		return "", fmt.Errorf("qemu backend: invalid guest port %q", port.ContainerPort)
	}
	hostIP := strings.TrimSpace(port.HostIP)
	if hostIP == "" {
		hostIP = "127.0.0.1"
	}
	return fmt.Sprintf("tcp:%s:%d-:%d", hostIP, hostPort, containerPort), nil
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
