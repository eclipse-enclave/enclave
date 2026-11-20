// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/model"
)

const virtiofsdHelpFull = "virtiofsd\n  --readonly\n  --inode-file-handles <mode>\n"

// fakeHost stands in for the host's processes: it records every command and
// plays QEMU and the guest behind it, so a session runs without KVM.
type fakeHost struct {
	t *testing.T

	mu       sync.Mutex
	commands [][]string
	missing  map[string]bool

	virtiofsdHelp string
	// bootStatus is what the guest's boot script reports.
	bootStatus string
	// toolExit is the exit code the guest launcher reports; negative means it
	// never starts.
	toolExit int
	// sshExit is what ssh itself returns.
	sshExit int
	// provisionResult is what a provisioning boot writes as its result.
	provisionResult string
	// provisionError is how the provisioning QEMU ends, after the guest wrote
	// its result.
	provisionError error
	// inherited is the file the daemonized QEMU was given.
	inherited *os.File
	// recordedAtStart reports whether the session's record existed when QEMU
	// was started.
	recordedAtStart bool
	sessions        func() []sessionRecord
	// killed lists the PIDs the backend killed; unkillable makes the fake
	// QEMU survive that.
	killed     []int
	unkillable bool
	// mute makes the monitor take commands after the handshake and never
	// answer them.
	mute bool
	// staysUp makes the guest keep running after the tool has ended.
	staysUp bool
	// launch records what the launcher found in its payload.
	launch map[string]string
	// duringSession runs while the tool is running in the guest.
	duringSession func(runtimeFiles)
	// beforeLaunch runs when the guest launcher is about to take its payload.
	beforeLaunch func(runtimeFiles)
	// afterDaemonize runs when QEMU has started and before anything dials it.
	afterDaemonize func(runtimeFiles)
	// duringSSH runs in every ssh that is to run the tool on a terminal.
	duringSSH func(runtimeFiles)

	guest *fakeGuest
}

func newFakeHost(t *testing.T) *fakeHost {
	return &fakeHost{t: t, missing: map[string]bool{}, virtiofsdHelp: virtiofsdHelpFull, bootStatus: "ready", provisionResult: "0"}
}

func (f *fakeHost) record(name string, args []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, append([]string{filepath.Base(name)}, args...))
}

// commandsOf returns the argument lists of every recorded call of a binary.
func (f *fakeHost) commandsOf(binary string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var calls [][]string
	for _, command := range f.commands {
		if command[0] == binary {
			calls = append(calls, command[1:])
		}
	}
	return calls
}

func (f *fakeHost) LookPath(file string) (string, error) {
	if f.missing[filepath.Base(file)] || filepath.IsAbs(file) {
		return "", errors.New("not found")
	}
	return "/usr/bin/" + file, nil
}

func (f *fakeHost) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.record(name, args)
	switch filepath.Base(name) {
	case "virtiofsd":
		return []byte(f.virtiofsdHelp), nil
	case sshKeygenBinary:
		path := args[len(args)-1]
		if err := os.WriteFile(path, []byte("private key of "+filepath.Base(path)+"\n"), 0o600); err != nil {
			return nil, err
		}
		return nil, os.WriteFile(path+".pub", []byte("ssh-ed25519 AAAA"+filepath.Base(path)+" enclave-vm\n"), 0o600)
	case qemuImgBinary:
		if args[0] == "resize" {
			return nil, nil
		}
		return nil, os.WriteFile(args[len(args)-1], []byte("qcow2"), 0o600)
	case "cloud-localds":
		return nil, os.WriteFile(args[0], []byte("seed"), 0o600)
	case sshBinary:
		// A detached start: the launcher takes the payload and keeps running.
		return nil, f.launcherStarts(runtimeFiles{dir: filepath.Dir(argAfter(args, "-i"))})
	case qemuBinary:
		if err := f.provision(args); err != nil {
			return nil, err
		}
		if f.provisionError != nil {
			return []byte("qemu: terminated"), f.provisionError
		}
		return nil, nil
	}
	return nil, fmt.Errorf("fakeHost: unexpected command %s", name)
}

func (f *fakeHost) Daemonize(_ context.Context, name string, args []string, keep *os.File) ([]byte, error) {
	f.record(name, args)
	if !slices.Contains(args, "-daemonize") {
		return nil, fmt.Errorf("fakeHost: %s was not asked to daemonize", name)
	}
	f.inherited = keep
	if f.sessions != nil {
		f.recordedAtStart = len(f.sessions()) > 0
	}
	if err := f.startGuest(args); err != nil {
		return nil, err
	}
	if f.afterDaemonize != nil {
		f.afterDaemonize(f.guest.rt)
	}
	return nil, nil
}

func argAfter(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func (f *fakeHost) provision(args []string) error {
	out := filepath.Join(filepath.Dir(strings.TrimPrefix(argAfter(args, "-serial"), "file:")), "out")
	if err := os.WriteFile(filepath.Join(out, provisionLogFile), []byte("installing\nlast line of the log\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, provisionResultFile), []byte(f.provisionResult+"\n"), 0o600)
}

func (f *fakeHost) startGuest(args []string) error {
	var sockets []string
	for i, arg := range args {
		if arg == "-qmp" {
			sockets = append(sockets, strings.TrimSuffix(strings.TrimPrefix(args[i+1], "unix:"), ",server=on,wait=off"))
		}
	}
	rt := runtimeFiles{dir: filepath.Dir(argAfter(args, "-pidfile")), sockets: filepath.Dir(sockets[0])}
	guest, err := startFakeGuest(argAfter(args, "-name"), sockets...)
	if err != nil {
		return err
	}
	// A real QEMU holds the reservation it inherited until it exits.
	guest.inherited = dupFile(f.inherited)
	guest.rt = rt
	guest.mute = f.mute
	f.mu.Lock()
	f.guest = guest
	f.mu.Unlock()
	f.t.Cleanup(guest.powerOff)
	if f.bootStatus == "" {
		return nil
	}
	return os.WriteFile(filepath.Join(rt.out(), guestBootFile), []byte(f.bootStatus+"\n"), 0o600)
}

// running is the backend's view of the fake QEMU process.
func (f *fakeHost) running(rt runtimeFiles) (int, bool) {
	f.mu.Lock()
	guest := f.guest
	f.mu.Unlock()
	if guest == nil || guest.rt.dir != rt.dir || guest.isOff() {
		return 0, false
	}
	return 4243, true
}

// kill is the backend's last resort on the fake QEMU process.
func (f *fakeHost) kill(pid int) {
	f.mu.Lock()
	f.killed = append(f.killed, pid)
	guest, unkillable := f.guest, f.unkillable
	f.mu.Unlock()
	if guest != nil && !unkillable {
		guest.powerOff()
	}
}

// launcherStarts is the guest launcher reading its payload and reporting
// that the host may remove it.
func (f *fakeHost) launcherStarts(rt runtimeFiles) error {
	if f.beforeLaunch != nil {
		f.beforeLaunch(rt)
	}
	f.launch = map[string]string{}
	for _, entry := range []string{"argv", "env", "workdir", "user", "loopback-ports"} {
		data, err := os.ReadFile(filepath.Join(rt.payload(), entry))
		if err != nil {
			return err
		}
		f.launch[entry] = string(data)
	}
	return os.WriteFile(filepath.Join(rt.out(), guestStartedFile), nil, 0o600)
}

// toolExits is the tool of a detached session ending by itself: the launcher
// reports the exit code and the guest powers off.
func (f *fakeHost) toolExits(code int) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.guest.rt.out(), guestExitCodeFile), []byte(fmt.Sprintf("%d\n", code)), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if !f.staysUp {
		f.guest.powerOff()
	}
}

func (f *fakeHost) Start(name string, args []string, _ string) (int, func(), error) {
	f.record(name, args)
	socket := strings.TrimPrefix(args[0], "--socket-path=")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return 0, nil, err
	}
	return 4242, func() { _ = listener.Close() }, nil
}

// Interactive plays the guest launcher behind ssh: it reads the payload,
// reports that it started, waits for the host to remove the payload, reports
// the tool's exit code and powers the guest off.
func (f *fakeHost) Interactive(_ context.Context, name string, args []string, _ backend.AttachIO) (int, error) {
	f.record(name, args)
	if strings.Contains(args[len(args)-1], "'"+sessionModeAttach+"'") {
		return f.sshExit, nil
	}
	rt := runtimeFiles{dir: filepath.Dir(argAfter(args, "-i"))}
	if f.duringSSH != nil {
		f.duringSSH(rt)
	}
	if f.toolExit < 0 {
		return f.sshExit, nil
	}
	if err := f.launcherStarts(rt); err != nil {
		return 0, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(rt.payload()); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			return 0, errors.New("fakeHost: the host never removed the launch payload")
		}
		time.Sleep(time.Millisecond)
	}
	if f.duringSession != nil {
		f.duringSession(rt)
	}
	f.toolExits(f.toolExit)
	return f.sshExit, nil
}

func (f *fakeHost) Download(_ context.Context, _ string, w io.Writer) error {
	_, err := io.WriteString(w, "not the pinned image")
	return err
}

// fakeGuest is a QEMU process as the host sees it: its two monitor sockets
// and the reservation it inherited. Powering it off closes all of them.
type fakeGuest struct {
	name      string
	rt        runtimeFiles
	listeners []net.Listener
	inherited *os.File
	// lingers is how long the fake QEMU keeps the file it inherited after
	// its process is no longer seen running.
	lingers  time.Duration
	mute     bool
	mu       sync.Mutex
	conns    []net.Conn
	commands []string
	off      bool
}

func startFakeGuest(name string, sockets ...string) (*fakeGuest, error) {
	guest := &fakeGuest{name: name}
	for _, socket := range sockets {
		listener, err := net.Listen("unix", socket)
		if err != nil {
			guest.powerOff()
			return nil, err
		}
		guest.listeners = append(guest.listeners, listener)
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				guest.mu.Lock()
				guest.conns = append(guest.conns, conn)
				guest.mu.Unlock()
				go guest.serve(conn)
			}
		}()
	}
	return guest, nil
}

func (g *fakeGuest) serve(conn net.Conn) {
	_, _ = io.WriteString(conn, `{"QMP": {"version": {}, "capabilities": []}}`+"\n")
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var request struct {
			Execute string `json:"execute"`
		}
		_ = json.Unmarshal(line, &request)
		g.mu.Lock()
		g.commands = append(g.commands, request.Execute)
		mute := g.mute
		g.mu.Unlock()
		if mute && request.Execute != "qmp_capabilities" {
			continue
		}
		reply := `{"return": {}}`
		if request.Execute == "query-name" {
			reply = fmt.Sprintf(`{"return": {"name": %q}}`, g.name)
		}
		// An event before the reply, as QEMU interleaves them.
		_, _ = io.WriteString(conn, `{"event": "NOISE"}`+"\n"+reply+"\n")
		if request.Execute == "system_powerdown" || request.Execute == "quit" {
			g.powerOff()
			return
		}
	}
}

func (g *fakeGuest) powerOff() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.off {
		return
	}
	g.off = true
	for _, listener := range g.listeners {
		_ = listener.Close()
	}
	for _, conn := range g.conns {
		_ = conn.Close()
	}
	if g.inherited != nil {
		if g.lingers > 0 {
			time.AfterFunc(g.lingers, func() { _ = g.inherited.Close() })
			return
		}
		_ = g.inherited.Close()
	}
}

func (g *fakeGuest) isOff() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.off
}

func (g *fakeGuest) received() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.commands...)
}

// filesOf gives a test the paths of a runtime directory in whatever state it
// is: tests plant files in directories the backend must not use, and look
// into ones it should have left alone.
func filesOf(rt runtimeDir) runtimeFiles {
	return runtimeFiles{dir: rt.name(), sockets: rt.socketsName()}
}

// newTestBackend returns a backend on a fake host with a temporary home, the
// repository's guest scripts, and a prewarmed base to create disks on.
func newTestBackend(t *testing.T) (*Backend, *fakeHost, string) {
	t.Helper()
	// Unix socket paths are short; the default temp dir of a test can be deep.
	home := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t))
	kvm := filepath.Join(home, "kvm")
	if err := os.WriteFile(kvm, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	appRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	host := newFakeHost(t)
	b := New(Options{Host: model.Host{Home: home, UID: "1000", GID: "1000"}, Paths: model.Paths{AppRoot: appRoot}})
	b.host = host
	b.vmProcess = host.running
	b.killProcess = host.kill
	host.sessions = func() []sessionRecord {
		records, _ := b.listRecords()
		return records
	}
	b.platform = supportedPlatform
	b.kvmPath = kvm
	b.pollInterval = time.Millisecond
	b.exportTimeout = 2 * time.Second
	b.bootTimeout = 2 * time.Second
	b.shutdownTimeout = 2 * time.Second

	baseDir := BaseDir(home, "codex", "basekey")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, baseDiskFile), []byte("base"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, BaseStampFile), []byte(`{"key":"basekey","tool":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return b, host, baseDir
}
