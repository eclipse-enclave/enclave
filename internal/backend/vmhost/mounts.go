// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vmhost

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/backend"
)

// DirMount is one host directory a guest mounts at Target.
type DirMount struct {
	ID        string
	Tag       string
	Source    string
	Target    string
	ReadOnly  bool
	CacheMmap bool
}

// FileMount is one host file exposed at Target. A directory share cannot
// carry a single file, so the host stages a copy for the guest to install
// (GuestSource) and a second one the guest writes its final content to
// (GuestResult, ResultPath on the host).
type FileMount struct {
	Source      string
	Target      string
	GuestSource string
	GuestResult string
	ResultPath  string
	ReadOnly    bool
}

// MountPlanner turns a request's mounts and stores into guest mounts. Its
// errors name the backend.
type MountPlanner struct {
	Backend string
	Stores  *StoreManager
	// Reserved are guest paths no mount target may overlap.
	Reserved []string
}

// CreateDisposableSources honors the backend.Mount.CreateSourceDir
// contract: a disposable source that no longer exists is recreated as an empty
// directory instead of failing the session. Non-bind mounts have no host
// directory to create.
func (p MountPlanner) CreateDisposableSources(mounts []backend.Mount) error {
	for _, mount := range mounts {
		if !mount.CreateSourceDir {
			continue
		}
		if mount.Type != "" && mount.Type != backend.MountTypeBind {
			continue
		}
		if err := os.MkdirAll(mount.Source, 0o700); err != nil {
			return fmt.Errorf("%s backend: create disposable mount source %q: %w", p.Backend, mount.Source, err)
		}
	}
	return nil
}

// DirMounts plans the directory mounts and stores of a request, in request
// order. File sources are skipped: FileMounts stages those.
func (p MountPlanner) DirMounts(req backend.Request) ([]DirMount, error) {
	mounts := make([]DirMount, 0, len(req.Mounts)+len(req.Stores)+2)
	idx := 0
	for _, mount := range req.Mounts {
		if mount.Type != "" && mount.Type != backend.MountTypeBind {
			return nil, fmt.Errorf("%s backend: unsupported mount type %q", p.Backend, mount.Type)
		}
		info, err := os.Stat(mount.Source)
		if err != nil {
			return nil, fmt.Errorf("%s backend: inspect mount source %q: %w", p.Backend, mount.Source, err)
		}
		if !info.IsDir() {
			continue
		}
		if err := p.ValidateGuestPath(mount.ContainerPath, "mount target"); err != nil {
			return nil, err
		}
		mounts = append(mounts, DirMount{
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
		if err := p.ValidateGuestPath(store.ContainerPath, "store target"); err != nil {
			return nil, err
		}
		source, err := p.Stores.MountSource(store.Key, store.Kind)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, DirMount{
			ID:        fmt.Sprintf("store-%d", idx),
			Tag:       fmt.Sprintf("enclave-store-%d", idx),
			Source:    source,
			Target:    filepath.Clean(store.ContainerPath),
			ReadOnly:  store.ReadOnly,
			CacheMmap: store.CacheMmap,
		})
		idx++
	}
	return mounts, nil
}

// SortParentsFirst orders mounts so that a parent target precedes its
// children, keeping the given order among equals.
func SortParentsFirst(mounts []DirMount) {
	sort.SliceStable(mounts, func(i, j int) bool {
		return strings.Count(mounts[i].Target, "/") < strings.Count(mounts[j].Target, "/")
	})
}

// FileMounts stages the file sources of mounts under stageRoot, which the
// guest sees at guestFilesPath. Nothing is created when there are none.
func (p MountPlanner) FileMounts(stageRoot string, guestFilesPath string, mounts []backend.Mount) ([]FileMount, error) {
	var prepared []FileMount
	for _, mount := range mounts {
		if mount.Type != "" && mount.Type != backend.MountTypeBind {
			continue
		}
		info, err := os.Stat(mount.Source)
		if err != nil {
			return nil, fmt.Errorf("%s backend: inspect file mount source %q: %w", p.Backend, mount.Source, err)
		}
		if info.IsDir() {
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s backend: mount source %q must be a directory or regular file", p.Backend, mount.Source)
		}
		if err := p.ValidateGuestPath(mount.ContainerPath, "file mount target"); err != nil {
			return nil, err
		}
		entryRoot := filepath.Join(stageRoot, fmt.Sprintf("%02d", len(prepared)))
		if err := os.MkdirAll(entryRoot, 0o700); err != nil {
			return nil, fmt.Errorf("%s backend: create file mount staging entry: %w", p.Backend, err)
		}
		sourcePath := filepath.Join(entryRoot, "source")
		resultPath := filepath.Join(entryRoot, "result")
		if err := copyFile(mount.Source, sourcePath, info.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("%s backend: stage file mount source: %w", p.Backend, err)
		}
		if err := copyFile(mount.Source, resultPath, info.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("%s backend: stage file mount result: %w", p.Backend, err)
		}
		guestEntry := filepath.Join(guestFilesPath, fmt.Sprintf("%02d", len(prepared)))
		prepared = append(prepared, FileMount{
			Source:      mount.Source,
			Target:      filepath.Clean(mount.ContainerPath),
			GuestSource: filepath.Join(guestEntry, "source"),
			GuestResult: filepath.Join(guestEntry, "result"),
			ResultPath:  resultPath,
			ReadOnly:    mount.ReadOnly,
		})
	}
	return prepared, nil
}

func (p MountPlanner) ValidateGuestPath(path string, label string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s backend: %s is empty", p.Backend, label)
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) || cleaned == string(filepath.Separator) {
		return fmt.Errorf("%s backend: %s %q must be an absolute non-root path", p.Backend, label, path)
	}
	for _, reserved := range p.Reserved {
		if cleaned == reserved || strings.HasPrefix(cleaned, reserved+string(filepath.Separator)) {
			return fmt.Errorf("%s backend: %s %q overlaps reserved guest path %s", p.Backend, label, path, reserved)
		}
	}
	return nil
}

// PersistFileMounts writes the guest's final content of each writable file
// mount back to its host source.
func PersistFileMounts(files []FileMount) []error {
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

func removeCopyDestination(dst string) error {
	if _, err := os.Lstat(dst); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.RemoveAll(dst)
}

func copyFile(src string, dst string, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	in, err := os.Open(src) // #nosec G304 -- caller validates source.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := removeCopyDestination(dst); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode.Perm()) // #nosec G304 -- dst is constrained by caller.
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode.Perm())
}
