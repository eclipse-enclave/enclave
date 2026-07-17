// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/logx"
	"enclave/internal/model"
)

const qemuBinary = "qemu-system-x86_64"

type Options struct {
	Host  model.Host
	Paths model.Paths
}

type Backend struct {
	opts    Options
	storage *StoreManager
}

func New(opts Options) *Backend {
	return &Backend{opts: opts, storage: newStoreManager(opts.Host)}
}

func (b *Backend) Name() string { return backend.NameQEMU }

func (b *Backend) Check(context.Context) error {
	if _, err := exec.LookPath(qemuBinary); err != nil {
		return fmt.Errorf("qemu backend: %s not found; install qemu-system-x86 on the host", qemuBinary)
	}
	if _, err := exec.LookPath("cpio"); err != nil {
		return fmt.Errorf("qemu backend: cpio not found; install cpio on the host")
	}
	return nil
}

func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{RestrictedNetwork: false, SecretHTTPRelease: false}
}

func (b *Backend) Storage() backend.StoreManager { return b.storage }

func (b *Backend) Run(ctx context.Context, req backend.Request, attach backend.AttachIO) (backend.ExitStatus, error) {
	if req.AuthSync != nil {
		defer b.runRequestAuthSync(req)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := backend.Validate(req, b.Capabilities()); err != nil {
		return backend.ExitStatus{}, err
	}
	if err := b.Check(ctx); err != nil {
		return backend.ExitStatus{}, err
	}
	vmBundle, err := resolveBundle(req.Image)
	if err != nil {
		return backend.ExitStatus{}, err
	}
	runtime, err := b.prepareGuestRuntime(vmBundle, req)
	if err != nil {
		return backend.ExitStatus{}, err
	}
	defer func() { _ = os.RemoveAll(runtime.TempDir) }()
	defer func() {
		for _, err := range persistFileMounts(runtime.FileMounts) {
			_, _ = fmt.Fprintf(stderrOrDefault(attach.Err), "%v\n", err)
		}
	}()
	args, err := b.buildQEMUArgs(vmBundle, runtime, req)
	if err != nil {
		return backend.ExitStatus{}, err
	}
	logQEMURequest(vmBundle, runtime, req, args)
	cmd := exec.CommandContext(ctx, qemuBinary, args...) // #nosec G204 -- args are generated from validated request fields.
	cmd.Stdin = readerOrDefault(attach.In)
	cmd.Stdout = writerOrDefault(attach.Out)
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(stderrOrDefault(attach.Err), &stderr)
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return backend.ExitStatus{}, fmt.Errorf("qemu backend: %s not found; install qemu-system-x86 on the host", qemuBinary)
		}
		return backend.ExitStatus{}, fmt.Errorf("qemu backend: start %s: %w", qemuBinary, err)
	}
	if attach.OnStarted != nil {
		attach.OnStarted()
	}
	waitErr := cmd.Wait()
	guestCode, hasGuestCode, readErr := readGuestExitCode(runtime.ExitCodePath)
	if readErr != nil {
		return backend.ExitStatus{Code: 1}, readErr
	}
	if hasGuestCode {
		if guestCode != 0 {
			return backend.ExitStatus{Code: guestCode}, &backend.ExitError{Code: guestCode}
		}
		return backend.ExitStatus{Code: 0}, nil
	}
	if waitErr != nil {
		text := strings.TrimSpace(stderr.String())
		if text != "" {
			return backend.ExitStatus{Code: 1}, fmt.Errorf("qemu backend: qemu exited before guest completion: %s", text)
		}
		return backend.ExitStatus{Code: 1}, fmt.Errorf("qemu backend: qemu exited before guest completion: %w", waitErr)
	}
	return backend.ExitStatus{Code: 1}, fmt.Errorf("qemu backend: guest exited without reporting an exit code")
}

func (b *Backend) Start(context.Context, backend.Request) (backend.SessionRef, error) {
	return backend.SessionRef{}, fmt.Errorf("qemu backend: detached sessions are not supported")
}

func (b *Backend) List(context.Context, backend.SessionFilter) ([]backend.Session, error) {
	return nil, nil
}

func (b *Backend) Inspect(context.Context, backend.SessionRef) (*backend.Session, error) {
	return nil, fmt.Errorf("qemu backend: inspect is not supported")
}

func (b *Backend) Attach(context.Context, backend.SessionRef, backend.AttachIO) error {
	return fmt.Errorf("qemu backend: attach is not supported")
}

func (b *Backend) Stop(context.Context, backend.SessionRef, backend.StopOptions) error {
	return fmt.Errorf("qemu backend: stop is not supported")
}

func (b *Backend) Remove(context.Context, backend.SessionRef) error {
	return fmt.Errorf("qemu backend: remove is not supported")
}

func logQEMURequest(vmBundle bundle, runtime guestRuntime, req backend.Request, args []string) {
	logx.Debugf("qemu backend: bundle=%s memory=%dMiB runtime=%s session=%s", vmBundle.Root, vmBundle.MemoryMiB, runtime.TempDir, qemuName(req))
	for _, mount := range runtime.Mounts {
		cache := "default"
		if mount.CacheMmap {
			cache = "mmap"
		}
		mode := "rw"
		if mount.ReadOnly {
			mode = "ro"
		}
		logx.Debugf("qemu backend: mount tag=%s target=%s source=%s mode=%s cache=%s", mount.Tag, mount.Target, mount.Source, mode, cache)
	}
	logx.Debugf("qemu backend: args: %s %s", qemuBinary, strings.Join(args, " "))
}

func readerOrDefault(r io.Reader) io.Reader {
	if r != nil {
		return r
	}
	return os.Stdin
}

func writerOrDefault(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return os.Stdout
}

func stderrOrDefault(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return os.Stderr
}

func readGuestExitCode(path string) (int, bool, error) {
	if path == "" {
		return 0, false, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is generated under the qemu runtime control directory.
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("qemu backend: read guest exit code: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, false, fmt.Errorf("qemu backend: guest exit code file %s is empty", path)
	}
	code, err := strconv.Atoi(text)
	if err != nil {
		return 0, false, fmt.Errorf("qemu backend: parse guest exit code %q: %w", text, err)
	}
	if code < 0 || code > 255 {
		return 0, false, fmt.Errorf("qemu backend: guest exit code %d is out of range", code)
	}
	return code, true, nil
}
