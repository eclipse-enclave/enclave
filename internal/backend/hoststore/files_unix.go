// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package hoststore

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func (r *Files) ReadDir(name string) ([]os.DirEntry, error) {
	f, err := r.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
}

func (r *Files) ReadFile(name string) ([]byte, error) {
	f, err := r.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular store file: %s", name)
	}
	return io.ReadAll(f)
}

func (r *Files) withParent(name string, fn func(int, string) error) error {
	if !filepath.IsLocal(name) || name == "." {
		return fmt.Errorf("invalid store path %q", name)
	}
	f, err := r.OpenFile(filepath.Dir(name), os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return fn(int(f.Fd()), filepath.Base(name)) // #nosec G115 -- Unix file descriptors fit in int; f is open.
}

func (r *Files) WriteFile(name string, data []byte, mode fs.FileMode) error {
	if err := r.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	return r.withParent(name, func(fd int, base string) error {
		tmp := ".enclave-" + rand.Text()
		raw, err := unix.Openat(fd, tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW, uint32(mode.Perm()))
		if err != nil {
			return err
		}
		defer func() { _ = unix.Unlinkat(fd, tmp, 0) }()
		f := os.NewFile(uintptr(raw), tmp) // #nosec G115 -- successful Openat returns a nonnegative file descriptor.
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return err
		}
		// The create mode is subject to the umask; set the exact mode.
		if err := f.Chmod(mode.Perm()); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return unix.Renameat(fd, tmp, fd, base)
	})
}

func (r *Files) Readlink(name string) (string, error) {
	var target string
	err := r.withParent(name, func(fd int, base string) error {
		buf := make([]byte, 65536)
		n, err := unix.Readlinkat(fd, base, buf)
		if err == nil {
			target = string(buf[:n])
		}
		return err
	})
	return target, err
}

func (r *Files) Symlink(target, name string) error {
	if err := r.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	return r.withParent(name, func(fd int, base string) error {
		if err := unix.Unlinkat(fd, base, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
		return unix.Symlinkat(target, fd, base)
	})
}

func (r *Files) Chmod(name string, mode fs.FileMode) error {
	f, err := r.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Chmod(mode)
}

func (r *Files) Chtimes(name string, stamp time.Time) error {
	return r.withParent(name, func(fd int, base string) error {
		ts := unix.NsecToTimespec(stamp.UnixNano())
		return unix.UtimesNanoAt(fd, base, []unix.Timespec{ts, ts}, unix.AT_SYMLINK_NOFOLLOW)
	})
}
