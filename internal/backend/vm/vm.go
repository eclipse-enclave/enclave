// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package vm runs a session in a full QEMU/KVM virtual machine: an Ubuntu
// cloud guest on a persistent per-project disk, with the project and the host
// stores shared into it and the tool started over SSH.
package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"enclave/internal/backend"
	"enclave/internal/backend/vmhost"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

const (
	memoryEnv        = "ENCLAVE_VM_MEMORY_MIB"
	cpusEnv          = "ENCLAVE_VM_CPUS"
	bootTimeoutEnv   = "ENCLAVE_VM_BOOT_TIMEOUT"
	defaultMemoryMiB = 4096
	defaultMaxCPUs   = 4

	supportedPlatform = "linux/amd64"
	// sshd starts right after the guest reports ready, so the first
	// connections can still be refused.
	sshAttempts = 20
)

type Options struct {
	Host  model.Host
	Paths model.Paths
}

type Backend struct {
	opts    Options
	storage *vmhost.StoreManager
	host    host

	// reservation is this process's claim on the project disk, made by
	// AdmitSession and handed to QEMU.
	reservation *os.File
	reservedDir string

	platform string
	kvmPath  string
	// vmProcess reports the PID of a session's QEMU if it still runs, and
	// killProcess ends it. Tests replace both.
	vmProcess   func(rt runtimeFiles) (int, bool)
	killProcess func(pid int)
	// beforeForget, set by tests, runs between the last check and the
	// removal of a session's record.
	beforeForget func()
	// Waits, shortened by tests.
	pollInterval    time.Duration
	exportTimeout   time.Duration
	bootTimeout     time.Duration
	shutdownTimeout time.Duration
}

func New(opts Options) *Backend {
	return &Backend{
		opts:            opts,
		storage:         vmhost.NewStoreManager(opts.Host, backend.NameVM),
		host:            execHost{},
		platform:        runtime.GOOS + "/" + runtime.GOARCH,
		kvmPath:         "/dev/kvm",
		vmProcess:       vmProcess,
		killProcess:     killProcess,
		pollInterval:    250 * time.Millisecond,
		exportTimeout:   10 * time.Second,
		bootTimeout:     bootTimeout(),
		shutdownTimeout: 30 * time.Second,
	}
}

func (b *Backend) Name() string { return backend.NameVM }

// Check verifies what a session needs from the host. There is no emulation
// fallback: a full guest without KVM takes minutes to boot.
func (b *Backend) Check(context.Context) error {
	if b.platform != supportedPlatform {
		return fmt.Errorf("vm backend: only x86-64 Linux hosts are supported, this is %s", b.platform)
	}
	for _, tool := range []struct{ binary, hint string }{
		{qemuBinary, "install qemu-system-x86"},
		{qemuImgBinary, "install qemu-utils"},
		{sshBinary, "install the OpenSSH client"},
		{sshKeygenBinary, "install the OpenSSH client"},
	} {
		if _, err := b.host.LookPath(tool.binary); err != nil {
			return fmt.Errorf("vm backend: %s not found; %s on the host", tool.binary, tool.hint)
		}
	}
	if _, err := b.findVirtiofsd(); err != nil {
		return err
	}
	kvm, err := os.OpenFile(b.kvmPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("vm backend: %s is not accessible (%w); enable KVM and add this user to the kvm group", b.kvmPath, err)
	}
	_ = kvm.Close()
	return nil
}

func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{RestrictedNetwork: false, SecretHTTPRelease: false}
}

func (b *Backend) Storage() backend.StoreManager { return b.storage }

func (b *Backend) PrepareStores(ctx context.Context, prep backend.StorePrep) (backend.StoreState, error) {
	return b.storage.PrepareStores(ctx, prep)
}

// home is the host home directory. Listing commands construct the backend
// without one.
func (b *Backend) home() string {
	if b.opts.Host.Home == "" {
		if home, err := config.ResolveHostHome(); err == nil {
			b.opts.Host.Home = home
			b.storage = vmhost.NewStoreManager(b.opts.Host, backend.NameVM)
		}
	}
	return b.opts.Host.Home
}

// liveVM is a VM whose guest reported that the session can start.
type liveVM struct {
	rt          runtimeDir
	qmp         *qmpClient
	stopExports func()
	record      sessionRecord
}

// prepare validates a request and brings its VM up to the point where the
// tool can be started: the disk claimed and present, the guest booted with
// every export mounted, and the session recorded.
func (b *Backend) prepare(ctx context.Context, req backend.Request) (*liveVM, error) {
	if err := backend.Validate(req, b.Capabilities()); err != nil {
		return nil, err
	}
	if err := b.Check(ctx); err != nil {
		return nil, err
	}
	user, err := guestUser(req)
	if err != nil {
		return nil, err
	}
	bs, err := resolveBase(req.Image)
	if err != nil {
		return nil, err
	}
	// The runtime has admitted the session already; a direct caller has not.
	if err := b.AdmitSession(ctx, req.Session); err != nil {
		return nil, err
	}
	disk, err := b.ensureDisk(ctx, b.reservedDir, bs)
	if err != nil {
		return nil, err
	}
	return b.boot(ctx, req, disk, user)
}

// boot starts the exports and the VM and waits for the guest. On failure
// nothing is left running.
func (b *Backend) boot(ctx context.Context, req backend.Request, disk string, user string) (*liveVM, error) {
	// A foreground session ends with its terminal, so its runtime directory
	// can live in the login session's. A detached one has to survive the
	// logout that removes that directory, and with it the only way to reach
	// or stop the VM.
	parent := ""
	if req.Detached {
		parent = filepath.Join(config.HostVMDir(b.home()), "run")
	}
	rt, err := newRuntimeDir(parent)
	if err != nil {
		return nil, err
	}
	vm := &liveVM{rt: rt, stopExports: func() {}}
	args, err := b.plan(ctx, req, vm, disk, user)
	if err != nil {
		vm.stopExports()
		rt.remove()
		return nil, err
	}
	// Recorded before QEMU exists: from here on a VM can hold the disk, and
	// if this process dies now, the record is how `ps` and `stop` find it.
	if err := b.publish(vm.record); err != nil {
		return nil, errors.Join(fmt.Errorf("vm backend: record the session: %w", err), b.discard(vm))
	}
	logx.Debugf("vm backend: args: %s %s", qemuBinary, strings.Join(args, " "))
	if out, err := b.host.Daemonize(ctx, qemuBinary, args, b.reservation); err != nil {
		return nil, errors.Join(fmt.Errorf("vm backend: start %s: %s: %w", qemuBinary, strings.TrimSpace(string(out)), err), b.discard(vm))
	}
	vm.qmp, err = rt.dialMonitor(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("vm backend: connect to the VM's control socket: %w", err), b.discard(vm))
	}
	if err := b.waitBoot(ctx, rt, vm.qmp); err != nil {
		if ctx.Err() != nil {
			logx.Warnf("Interrupted before the session started; cleaning up.")
			err = backend.ErrInterrupted
		}
		return nil, errors.Join(err, b.discard(vm))
	}
	return vm, nil
}

// publish writes a session's record.
func (b *Backend) publish(record sessionRecord) error {
	return b.locked(record.ProjectHash, record.Tool, func() error { return b.writeRecord(record) })
}

// markStarted records that the tool has been launched.
func (b *Backend) markStarted(vm *liveVM) {
	vm.record.Started = true
	if err := b.publish(vm.record); err != nil {
		logx.Warnf("vm session %s: record its start: %v", vm.record.Name, err)
	}
}

// plan writes everything the VM is started from into the runtime directory,
// starts the exports, fills in the session record, and returns QEMU's
// arguments.
func (b *Backend) plan(ctx context.Context, req backend.Request, vm *liveVM, disk string, user string) ([]string, error) {
	rt, err := vm.rt.open()
	if err != nil {
		return nil, err
	}
	planner := vmhost.MountPlanner{Backend: backend.NameVM, Stores: b.storage, Reserved: []string{guestRunPath}}
	if err := planner.CreateDisposableSources(req.Mounts); err != nil {
		return nil, err
	}
	files, err := planner.FileMounts(filepath.Join(rt.out(), filepath.Base(guestFilesPath)), guestFilesPath, req.Mounts)
	if err != nil {
		return nil, err
	}
	writable, err := recordFileMounts(files)
	if err != nil {
		return nil, err
	}
	mounts, err := planner.DirMounts(req)
	if err != nil {
		return nil, err
	}
	if err := rejectControlCover(mounts); err != nil {
		return nil, err
	}
	virtiofsd, err := b.findVirtiofsd()
	if err != nil {
		return nil, err
	}
	caps, err := b.probeVirtiofsd(ctx, virtiofsd)
	if err != nil {
		return nil, err
	}
	shares := sessionShares(rt, mounts, caps)
	for _, key := range []string{rt.clientKey(), rt.hostKey()} {
		if err := b.generateKey(ctx, key); err != nil {
			return nil, err
		}
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return nil, err
	}
	if err := writeKnownHosts(rt, port); err != nil {
		return nil, err
	}
	assets := filepath.Join(b.opts.Paths.AppRoot, "runtime-assets", "vm", "session")
	if err := writeControlIn(rt, assets, shares, files, req, user); err != nil {
		return nil, err
	}
	hostfwd := []string{fmt.Sprintf("tcp:127.0.0.1:%d-:22", port)}
	for _, published := range req.Ports {
		forward, err := vmhost.FormatHostfwd(backend.NameVM, published)
		if err != nil {
			return nil, err
		}
		hostfwd = append(hostfwd, forward)
	}
	size := machineSize()
	args, err := qemuArgs(machine{
		Name:      sessionName(req),
		MemoryMiB: size.MemoryMiB,
		CPUs:      size.CPUs,
		Disk:      disk,
		Shares:    shares,
		Hostfwd:   hostfwd,
		Console:   rt.console(),
		QMP:       rt.qmp(),
		Control:   rt.control(),
		PIDFile:   rt.pidFile(),
		socket:    rt.socket,
	})
	if err != nil {
		return nil, err
	}
	vm.record = newSessionRecord(req, vm.rt, port, writable)
	vm.stopExports, err = b.startExports(ctx, virtiofsd, caps, rt, shares)
	if err != nil {
		vm.stopExports = func() {}
		return nil, err
	}
	return args, nil
}

// discard tears down a VM whose session did not get to run. Nothing is
// written back: the guest never had the tool. A VM that cannot be stopped
// keeps its record, which is the only way left to find it.
func (b *Backend) discard(vm *liveVM) error {
	if err := b.stopConfirmed(context.Background(), vm.record, vm.qmp, 0); err != nil {
		return err
	}
	vm.stopExports()
	return b.locked(vm.record.ProjectHash, vm.record.Tool, func() error {
		b.removeRecord(vm.record)
		return b.returnDisk(vm.record)
	})
}

// returnDisk gives up this process's reservation and waits until the disk
// can be claimed again. The VM that inherited the reservation has stopped,
// but a process that is no longer seen running can hold its files a moment
// longer, and a start that came in that moment would be told the disk is in
// use. It runs under the sessions lock, so nothing else takes the disk
// meanwhile. If the disk does not come free in time, the caller hears of it:
// the session is over, and no command of its own is left to wait with.
func (b *Backend) returnDisk(record sessionRecord) error {
	dir := b.reservedDir
	b.releaseReservation()
	if dir == "" {
		return nil
	}
	deadline := time.Now().Add(b.shutdownTimeout)
	for {
		lock, err := b.claimDisk(dir, record.Tool)
		if err == nil {
			return lock.Close()
		}
		if !errors.Is(err, errDiskInUse) {
			return fmt.Errorf("vm backend: session %s has ended, but its disk could not be checked: %w", record.Name, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vm backend: session %s has ended, but its disk is still held after %s, most likely by its %s process on its way out; the project cannot start a VM session until that process is gone", record.Name, b.shutdownTimeout, qemuBinary)
		}
		time.Sleep(b.pollInterval)
	}
}

// stopConfirmed stops a session's VM and returns once its process is gone,
// or an error if it is not gone in time. Every teardown goes through it: a
// session is finalized or forgotten only after this has returned nil. own is
// the monitor connection of the process that started the VM; other processes
// use the control monitor.
func (b *Backend) stopConfirmed(ctx context.Context, record sessionRecord, own *qmpClient, timeout time.Duration) error {
	// A guest takes longer to shut down than a container to exit; the
	// callers' timeouts are a container engine's.
	timeout = max(timeout, b.shutdownTimeout)
	if own != nil {
		defer own.close()
	}
	// The open monitor connection of the process that started the VM says
	// more than the session's files: a runtime directory can be lost while
	// its VM lives, and that is no sign that the VM has stopped.
	running := func() bool {
		return (own != nil && !own.exited()) || b.status(record) == statusRunning
	}
	if !running() {
		return nil
	}
	qmp := own
	if qmp == nil {
		if control, ok := b.connect(ctx, record); ok {
			qmp = control
			defer control.close()
		}
	}
	if qmp != nil {
		b.shutDown(qmp, record.runtime(), timeout)
	} else {
		logx.Warnf("vm session %s: its VM does not answer on the control socket; stopping the VM.", record.Name)
		b.killVM(record.runtime())
	}
	// A kill is a request; the process is gone when it is gone.
	deadline := time.Now().Add(b.shutdownTimeout)
	for running() {
		if time.Now().After(deadline) {
			return fmt.Errorf("vm backend: the VM of session %s could not be stopped; the session is kept, try `%s stop --backend %s %s`", record.Name, model.AppName, backend.NameVM, record.Name)
		}
		time.Sleep(b.pollInterval)
	}
	return nil
}

// Run runs the tool on the caller's terminal and returns when it has ended
// and the session is finalized.
func (b *Backend) Run(ctx context.Context, req backend.Request, attach backend.AttachIO) (status backend.ExitStatus, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	defer b.releaseReservation()
	vm, err := b.prepare(ctx, req)
	if err != nil {
		return backend.ExitStatus{}, err
	}
	// In this order: the VM stops, its exports with it, then what the guest
	// left is written back and its credentials reconciled, all while the disk
	// is still reserved for this session. The disk goes back last.
	defer func() {
		if stopErr := b.stopConfirmed(context.Background(), vm.record, vm.qmp, 0); stopErr != nil {
			// The VM lives on with the disk. Its record stays, and nothing
			// of the session is finalized under a guest that still writes.
			status, err = backend.ExitStatus{Code: 1}, stopErr
			return
		}
		vm.stopExports()
		var heldErr error
		finalizeErr := b.locked(vm.record.ProjectHash, vm.record.Tool, func() error {
			finalizeErr := b.finalize(&vm.record)
			if vm.record.Finalized {
				b.removeRecord(vm.record)
			}
			heldErr = b.returnDisk(vm.record)
			return finalizeErr
		})
		if finalizeErr != nil {
			logx.Warnf("%v", finalizeErr)
		}
		switch {
		case heldErr == nil:
		case err == nil:
			// The tool succeeded, the session did not end cleanly.
			status, err = backend.ExitStatus{Code: 1}, heldErr
		default:
			logx.Warnf("%v", heldErr)
		}
	}()
	b.markStarted(vm)
	if attach.OnStarted != nil {
		attach.OnStarted()
	}
	rt := vm.rt
	stopWatch := b.removePayloadOnceStarted(rt, vm.qmp)
	sshErr := b.runTool(ctx, vm, attach)
	stopWatch()
	if guestStarted(rt) {
		// The launcher powers the guest off when the tool ends; give it the
		// time before anything asks QEMU to.
		vm.qmp.waitClosed(b.shutdownTimeout)
	}

	code, reported, err := readGuestExitCode(rt)
	switch {
	case err != nil:
		return backend.ExitStatus{Code: 1}, err
	case reported && code != 0:
		return backend.ExitStatus{Code: code}, &backend.ExitError{Code: code}
	case reported:
		return backend.ExitStatus{}, nil
	case ctx.Err() != nil:
		return backend.ExitStatus{Code: backend.ExitCodeInterrupted}, &backend.ExitError{Code: backend.ExitCodeInterrupted}
	case sshErr != nil:
		return backend.ExitStatus{Code: 1}, sshErr
	default:
		return backend.ExitStatus{Code: 1}, fmt.Errorf("vm backend: the guest ended without reporting an exit code%s", rt.consoleTail(20))
	}
}

// Start runs the tool detached: in the guest it gets a terminal of its own
// that `attach` joins. The VM, its exports and the disk reservation outlive
// this process; the session record is how later commands find them.
func (b *Backend) Start(ctx context.Context, req backend.Request) (backend.SessionRef, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// QEMU has inherited the reservation by the time this returns.
	defer b.releaseReservation()
	vm, err := b.prepare(ctx, req)
	if err != nil {
		return backend.SessionRef{}, err
	}
	if err := b.startDetached(ctx, vm); err != nil {
		if ctx.Err() != nil {
			logx.Warnf("Interrupted before the session started; cleaning up.")
			err = backend.ErrInterrupted
		}
		return backend.SessionRef{}, errors.Join(err, b.discard(vm))
	}
	b.markStarted(vm)
	vm.qmp.close()
	return vm.record.session(statusRunning).Ref, nil
}

// startDetached starts the guest launcher under a terminal holder and waits
// until it has taken the launch payload, which is then removed.
func (b *Backend) startDetached(ctx context.Context, vm *liveVM) error {
	var out []byte
	var err error
	for attempt := 1; ; attempt++ {
		// ssh reads the session's identity files each time it runs.
		files, lost := vm.rt.open()
		if lost != nil {
			return lost
		}
		out, err = b.host.Output(ctx, sshBinary, sshArgs(files, vm.record.SSHPort, false, sessionModeDetached)...)
		if err == nil || attempt == sshAttempts || ctx.Err() != nil || vm.qmp.exited() {
			break
		}
		time.Sleep(b.pollInterval)
	}
	if err != nil {
		return fmt.Errorf("vm backend: start the tool in the guest: %s: %w", sanitize(strings.TrimSpace(string(out))), err)
	}
	deadline := time.Now().Add(b.bootTimeout)
	for !guestStarted(vm.rt) {
		switch {
		case vm.qmp.exited():
			return fmt.Errorf("vm backend: the VM exited before the tool started%s", vm.rt.consoleTail(20))
		case ctx.Err() != nil:
			return ctx.Err()
		case time.Now().After(deadline):
			return fmt.Errorf("vm backend: the tool did not start in the guest within %s", b.bootTimeout)
		}
		time.Sleep(b.pollInterval)
	}
	return vm.rt.removePayload()
}

func (b *Backend) List(_ context.Context, filter backend.SessionFilter) ([]backend.Session, error) {
	records, err := b.namedRecords()
	if err != nil {
		return nil, err
	}
	var sessions []backend.Session
	for _, record := range records {
		session := record.session(b.status(record))
		if matchesFilter(session, filter) {
			sessions = append(sessions, session)
		}
	}
	return sessions, nil
}

func (b *Backend) Inspect(_ context.Context, ref backend.SessionRef) (*backend.Session, error) {
	record, err := b.sessionRecord(ref)
	if err != nil {
		return nil, err
	}
	session := record.session(b.status(record))
	return &session, nil
}

// sessionRecord resolves a session reference. A reference from a listing
// carries the instance, and then it means that instance and no other: the
// name may have passed to a new session since the listing. A reference by
// name alone means whatever the name stands for now.
func (b *Backend) sessionRecord(ref backend.SessionRef) (sessionRecord, error) {
	name := strings.TrimSpace(ref.Name)
	if instance := strings.TrimSpace(ref.ID); instance != "" {
		record, ok, err := b.readInstance(instance)
		if err != nil {
			return record, err
		}
		if !ok || (name != "" && record.Name != name) {
			return record, fmt.Errorf("vm backend: session %s is gone", ref.Name)
		}
		return record, nil
	}
	record, ok, err := b.readRecord(name)
	if err != nil {
		return record, err
	}
	if !ok {
		return record, fmt.Errorf("vm backend: no session named %q", name)
	}
	return record, nil
}

// Attach joins the terminal of a detached session's tool. The detach key
// leaves the tool running.
func (b *Backend) Attach(ctx context.Context, ref backend.SessionRef, attach backend.AttachIO) error {
	record, err := b.sessionRecord(ref)
	if err != nil {
		return err
	}
	if !record.Background {
		return fmt.Errorf("vm backend: session %s runs in the foreground of another terminal and cannot be attached", record.Name)
	}
	keys := attach.DetachKeys
	if strings.TrimSpace(keys) == "" {
		keys = model.DetachKeysDefault
	}
	detachKey, err := dtachDetachKey(keys)
	if err != nil {
		return err
	}
	if b.status(record) != statusRunning {
		return fmt.Errorf("vm backend: session %s is not running", record.Name)
	}
	if !record.Started {
		return fmt.Errorf("vm backend: session %s has not started its tool yet", record.Name)
	}
	files, err := record.runtime().open()
	if err != nil {
		return err
	}
	args := sshArgs(files, record.SSHPort, attachIsTerminal(attach), sessionModeAttach, detachKey)
	code, err := b.host.Interactive(ctx, sshBinary, args, attach)
	if err != nil {
		return fmt.Errorf("vm backend: run ssh: %w", err)
	}
	// 255 is ssh's own failure, which is also how a session that ended while
	// attached looks: the guest powers off under the connection.
	if code != 0 && (code != 255 || b.status(record) == statusRunning) {
		return fmt.Errorf("vm backend: attaching to %s ended with status %d", record.Name, code)
	}
	return nil
}

// Stop shuts the session's guest down and, on request, finalizes the
// session. It is safe to repeat. A VM that cannot be stopped is an error, and
// nothing of the session is finalized then.
func (b *Backend) Stop(ctx context.Context, ref backend.SessionRef, opts backend.StopOptions) error {
	record, err := b.sessionRecord(ref)
	if err != nil {
		return err
	}
	if err := b.stopRecorded(ctx, record, opts.Timeout); err != nil {
		return err
	}
	if opts.Finalize {
		return b.finalizeStopped(record)
	}
	return nil
}

// EndSession is `enclave stop`: stop, finalize and forget one session. The
// three steps act on the session instance that was found at the start, so a
// new session that takes the name meanwhile is not touched, and the session
// is forgotten only once its VM is gone and its finalization is done.
func (b *Backend) EndSession(ctx context.Context, ref backend.SessionRef, opts backend.StopOptions) error {
	record, err := b.sessionRecord(ref)
	if err != nil {
		return err
	}
	if err := b.stopRecorded(ctx, record, opts.Timeout); err != nil {
		return err
	}
	var finalizeErr error
	if opts.Finalize {
		finalizeErr = b.finalizeStopped(record)
	}
	return errors.Join(finalizeErr, b.forget(record))
}

// stopRecorded stops the VM of a session another process started, and the
// exports that VM leaves behind.
func (b *Backend) stopRecorded(ctx context.Context, record sessionRecord, timeout time.Duration) error {
	if err := b.stopConfirmed(ctx, record, nil, timeout); err != nil {
		return err
	}
	reapExports(record.runtime())
	return nil
}

// finalizeStopped finalizes a session whose VM is gone. That needs the disk,
// and whoever takes the disk finalizes what earlier sessions of the project
// owe. The disk can still be held for three reasons, and the call waits for
// each of them: the QEMU process that just stopped has not closed its files
// yet, the session's own enclave process is on its way out, or a newer
// session of the project runs. The first frees the disk for this call; the
// other two finalize the session themselves.
func (b *Backend) finalizeStopped(record sessionRecord) error {
	dir, err := b.diskDir(backend.SessionMeta{Tool: record.Tool, ProjectHash: record.ProjectHash})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(b.shutdownTimeout)
	for {
		err = b.locked(record.ProjectHash, record.Tool, func() error {
			if b.reservation == nil || b.reservedDir != dir {
				lock, err := b.claimDisk(dir, record.Tool)
				if err != nil {
					return err
				}
				defer func() { _ = lock.Close() }()
			}
			return b.finalizeLeftovers(record.ProjectHash, record.Tool)
		})
		if !errors.Is(err, errDiskInUse) {
			return err
		}
		current, ok, err := b.readInstance(record.Instance)
		if err != nil || !ok || current.Finalized {
			return err
		}
		if time.Now().After(deadline) {
			if _, lost := record.runtime().open(); record.RuntimeDir != "" && lost != nil {
				// Without its runtime directory the session's VM cannot be
				// found, and a held disk is all that is known of it.
				return fmt.Errorf("vm backend: the disk of session %s is still held, and %w, so its VM cannot be found; if a %s process of the session still runs, end it, then run `%s stop --backend %s %s` again", record.Name, lost, qemuBinary, model.AppName, backend.NameVM, record.Name)
			}
			return fmt.Errorf("vm backend: session %s is still being finalized by the process that holds its disk; run `%s stop --backend %s %s` again", record.Name, model.AppName, backend.NameVM, record.Name)
		}
		time.Sleep(b.pollInterval)
	}
}

// Remove stops a session if it still runs, finalizes it and forgets it.
func (b *Backend) Remove(ctx context.Context, ref backend.SessionRef) error {
	record, err := b.sessionRecord(ref)
	if err != nil {
		return err
	}
	if err := b.stopRecorded(ctx, record, 0); err != nil {
		return err
	}
	if err := b.finalizeStopped(record); err != nil {
		return err
	}
	return b.forget(record)
}

// RemoveWithoutFinalize forgets a session that has ended and is finalized.
// It never stops a VM and never discards a finalization that is still owed:
// the runtime directory it would delete holds the session's only copy of
// what the guest wrote to its file mounts.
func (b *Backend) RemoveWithoutFinalize(_ context.Context, ref backend.SessionRef) error {
	if strings.TrimSpace(ref.ID) != "" {
		record, err := b.sessionRecord(ref)
		if err != nil {
			return err
		}
		return b.forget(record)
	}
	record, ok, err := b.readRecord(strings.TrimSpace(ref.Name))
	if err != nil || !ok {
		// A foreground session's record goes when it is finalized.
		return err
	}
	return b.forget(record)
}

// forget removes the record and the runtime directory of a session instance,
// if that loses nothing: its VM is gone and it is finalized. Otherwise the
// instance stays, for its owner or the next stop to settle. The check and the
// removal are one step under the sessions lock, on a record addressed by its
// instance: a session that takes the name meanwhile is neither seen nor
// touched.
func (b *Backend) forget(record sessionRecord) error {
	return b.locked(record.ProjectHash, record.Tool, func() error {
		current, ok, err := b.readInstance(record.Instance)
		if err != nil || !ok {
			return err
		}
		if b.status(current) == statusRunning {
			return fmt.Errorf("vm backend: session %s is still running", current.Name)
		}
		if !current.Finalized {
			return fmt.Errorf("vm backend: session %s is not finalized and is kept; run `%s stop --backend %s %s` again", current.Name, model.AppName, backend.NameVM, current.Name)
		}
		if b.beforeForget != nil {
			b.beforeForget()
		}
		b.removeRecord(current)
		return nil
	})
}

// waitBoot waits until the guest's boot script reports that every export is
// mounted. A failed mount ends the session here instead of leaving the tool
// an empty, writable directory.
func (b *Backend) waitBoot(ctx context.Context, rt runtimeDir, qmp *qmpClient) error {
	deadline := time.Now().Add(b.bootTimeout)
	for {
		status, ok, err := rt.readOut(guestBootFile, guestStatusLimit)
		if err != nil {
			return err
		}
		status = strings.TrimSpace(status)
		switch {
		case ok && status == "ready":
			return nil
		case ok && status != "":
			return fmt.Errorf("vm backend: the guest could not start the session: %s", sanitize(status))
		case qmp.exited():
			return fmt.Errorf("vm backend: the VM exited while booting%s", rt.consoleTail(20))
		case ctx.Err() != nil:
			return ctx.Err()
		case time.Now().After(deadline):
			return fmt.Errorf("vm backend: the guest did not come up within %s (%s extends it)%s", b.bootTimeout, bootTimeoutEnv, rt.consoleTail(20))
		}
		time.Sleep(b.pollInterval)
	}
}

// runTool runs the guest launcher on the session's terminal. ssh reports its
// own failures as 255; until the launcher has started, that means sshd was
// not accepting yet.
func (b *Backend) runTool(ctx context.Context, vm *liveVM, attach backend.AttachIO) error {
	for attempt := 1; ; attempt++ {
		// ssh reads the session's identity files each time it runs.
		files, err := vm.rt.open()
		if err != nil {
			return err
		}
		code, err := b.host.Interactive(ctx, sshBinary, sshArgs(files, vm.record.SSHPort, attachIsTerminal(attach)), attach)
		if err != nil {
			return fmt.Errorf("vm backend: run ssh: %w", err)
		}
		if code != 255 || attempt == sshAttempts || ctx.Err() != nil || vm.qmp.exited() || guestStarted(vm.rt) {
			return nil
		}
		time.Sleep(b.pollInterval)
	}
}

func guestStarted(rt runtimeDir) bool {
	_, ok, err := rt.readOut(guestStartedFile, guestStatusLimit)
	return ok && err == nil
}

// removePayloadOnceStarted deletes the launch payload as soon as the guest
// launcher has read it. The returned function ends the watch and waits for
// it.
func (b *Backend) removePayloadOnceStarted(rt runtimeDir, qmp *qmpClient) (stop func()) {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !guestStarted(rt) {
			select {
			case <-quit:
				return
			case <-qmp.closed:
				return
			case <-time.After(b.pollInterval):
			}
		}
		_ = rt.removePayload()
	}()
	return func() {
		close(quit)
		<-done
	}
}

// shutDown asks QEMU to go: by the guest's own shutdown if it gets there
// within timeout, else by quitting or killing QEMU. stopConfirmed, its only
// caller, checks that the process is gone.
func (b *Backend) shutDown(qmp *qmpClient, rt runtimeDir, timeout time.Duration) {
	if qmp.exited() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if _, err := qmp.execute(ctx, "system_powerdown"); err == nil && qmp.waitClosed(timeout) {
		return
	}
	logx.Warnf("vm backend: the guest did not shut down within %s; stopping the VM.", timeout)
	_, _ = qmp.execute(context.Background(), "quit")
	if !qmp.waitClosed(min(timeout, 5*time.Second)) {
		b.killVM(rt)
	}
}

// vmProcess finds the QEMU process of a runtime directory. The PID file
// alone is not trusted: the process must still carry the session's monitor
// socket, whose path is unique to the session, on its command line.
func vmProcess(rt runtimeFiles) (int, bool) {
	return ownedProcess(rt.pidFile(), rt.qmp())
}

func killProcess(pid int) {
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
	}
}

// process finds the QEMU process of a runtime directory. Every lookup goes
// through here: a PID file is read only in a runtime directory that is still
// the session's, so what someone else left under the path names no process
// to report as running or to kill.
func (b *Backend) process(rt runtimeDir) (int, bool) {
	files, err := rt.open()
	if err != nil {
		return 0, false
	}
	return b.vmProcess(files)
}

func (b *Backend) killVM(rt runtimeDir) {
	if pid, ok := b.process(rt); ok {
		b.killProcess(pid)
	}
}

type machineResources struct {
	MemoryMiB int
	CPUs      int
}

func machineSize() machineResources {
	return machineResources{
		MemoryMiB: positiveEnv(memoryEnv, defaultMemoryMiB),
		CPUs:      positiveEnv(cpusEnv, min(runtime.NumCPU(), defaultMaxCPUs)),
	}
}

func bootTimeout() time.Duration {
	return time.Duration(positiveEnv(bootTimeoutEnv, 180)) * time.Second
}

func positiveEnv(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		logx.Warnf("Ignoring %s=%q: not a positive number.", name, value)
		return fallback
	}
	return parsed
}

func sessionName(req backend.Request) string {
	if name := strings.TrimSpace(req.Session.Name); name != "" {
		return name
	}
	return strings.Join([]string{model.AppName, req.Session.Tool, req.Session.ProjectHash}, "-")
}

func attachIsTerminal(attach backend.AttachIO) bool {
	in, out := attach.In, attach.Out
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	for _, stream := range []any{in, out} {
		file, ok := stream.(*os.File)
		if !ok || !term.IsTerminal(int(file.Fd())) { // #nosec G115 -- file descriptors fit in int on all supported platforms.
			return false
		}
	}
	return true
}

// tailFile returns the last lines of a log for an error message.
func tailFile(path string, lines int) string {
	data, err := os.ReadFile(path) // #nosec G304 -- path is a log inside the session runtime directory.
	if err != nil {
		return ""
	}
	return tailText(string(data), lines)
}

// tailText formats the last lines of guest output as an error suffix.
func tailText(text string, lines int) string {
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	tail := sanitize(strings.Join(all, "\n"))
	if strings.TrimSpace(tail) == "" {
		return ""
	}
	return "\n" + tail
}

// sanitize drops control characters from text the guest produced, so it
// cannot drive the terminal it is printed on.
func sanitize(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f) {
			return r
		}
		return -1
	}, text)
}
