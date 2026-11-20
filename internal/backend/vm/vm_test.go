// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/util"
)

// sessionRequest is a foreground request with one directory mount, one
// read-only directory, one writable and one read-only file mount, and a store.
func sessionRequest(t *testing.T, baseDir string) (backend.Request, map[string]string) {
	t.Helper()
	project := t.TempDir()
	skills := t.TempDir()
	files := t.TempDir()
	gitconfig := filepath.Join(files, "gitconfig")
	envrc := filepath.Join(files, "allow")
	for path, content := range map[string]string{gitconfig: "[user]\n", envrc: "host\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	req := backend.Request{
		Session:    backend.SessionMeta{Tool: "codex", ProjectHash: "abc123def456", Name: "enclave-codex-abc123def456"},
		Image:      baseDir,
		Argv:       []string{"codex", "--flag", "two words"},
		Env:        []backend.EnvVar{{Name: "TOKEN", Value: "s3cret value"}, {Name: "TERM", Value: "xterm"}},
		WorkingDir: project,
		Hostname:   "enclave-project",
		Mounts: []backend.Mount{
			{Type: backend.MountTypeBind, Source: project, ContainerPath: project},
			{Type: backend.MountTypeBind, Source: skills, ContainerPath: "/home/agent/.codex/skills", ReadOnly: true},
			{Type: backend.MountTypeBind, Source: gitconfig, ContainerPath: "/tmp/host_gitconfig", ReadOnly: true},
			{Type: backend.MountTypeBind, Source: envrc, ContainerPath: "/home/agent/.allow"},
		},
		Stores: []backend.PersistentStore{
			{Kind: backend.StoreKindConfig, Key: backend.StoreKey{Owner: "codex", ProjectHash: "abc123def456"}, ContainerPath: "/home/agent/.codex"},
		},
		Network: backend.NetworkPolicy{Mode: backend.NetworkModeUnrestricted, LoopbackPorts: []string{"1455"}},
		Ports:   []backend.PortMapping{{HostPort: "8080", ContainerPort: "3000"}},
	}
	return req, map[string]string{"project": project, "skills": skills, "gitconfig": gitconfig, "allow": envrc}
}

func TestRunForegroundSession(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, paths := sessionRequest(t, baseDir)
	var runtimeDirSeen string
	var manifest, fileManifest string
	host.duringSession = func(rt runtimeFiles) {
		runtimeDirSeen = rt.dir
		manifest = readTestFile(t, filepath.Join(rt.in(), "mounts.tsv"))
		fileManifest = readTestFile(t, filepath.Join(rt.in(), "files.tsv"))
		// The tool writes through the bound file, which is the staged result.
		if err := os.WriteFile(filepath.Join(rt.out(), "files", "01", "result"), []byte("guest\n"), 0o600); err != nil {
			t.Error(err)
		}
	}
	started := 0
	status, err := b.Run(context.Background(), req, backend.AttachIO{OnStarted: func() { started++ }})
	if err != nil || status.Code != 0 {
		t.Fatalf("Run = %+v, %v", status, err)
	}
	if started != 1 {
		t.Fatalf("OnStarted ran %d times, want 1", started)
	}

	t.Run("launch payload", func(t *testing.T) {
		if got, want := host.launch["argv"], defaultEntrypoint+"\x00codex\x00--flag\x00two words\x00"; got != want {
			t.Errorf("argv = %q, want %q", got, want)
		}
		if got, want := host.launch["env"], "TOKEN=s3cret value\x00TERM=xterm\x00"; got != want {
			t.Errorf("env = %q, want %q", got, want)
		}
		if got := host.launch["user"]; got != "agent\n" {
			t.Errorf("user = %q", got)
		}
		if got := host.launch["workdir"]; got != paths["project"]+"\n" {
			t.Errorf("workdir = %q", got)
		}
		if got := host.launch["loopback-ports"]; got != "1455\n" {
			t.Errorf("loopback ports = %q", got)
		}
	})
	t.Run("no secret on a command line", func(t *testing.T) {
		host.mu.Lock()
		defer host.mu.Unlock()
		for _, command := range host.commands {
			if strings.Contains(strings.Join(command, " "), "s3cret") {
				t.Errorf("environment value on a command line: %v", command)
			}
		}
	})
	t.Run("mount manifests", func(t *testing.T) {
		wantMounts := "virtiofs\tenclave-mount-0\trw\tdefault\t" + paths["project"] + "\n" +
			"virtiofs\tenclave-store-2\trw\tdefault\t/home/agent/.codex\n" +
			"virtiofs\tenclave-mount-1\tro\tdefault\t/home/agent/.codex/skills\n"
		// Where the project sorts depends on the depth of its temporary
		// path, so compare as sets and check that a parent precedes its child.
		if !sameLines(manifest, wantMounts) {
			t.Errorf("mounts.tsv =\n%swant\n%s", manifest, wantMounts)
		}
		if strings.Index(manifest, "\t/home/agent/.codex\n") > strings.Index(manifest, "\t/home/agent/.codex/skills\n") {
			t.Errorf("store is mounted after its child:\n%s", manifest)
		}
		// The guest binds the staged file itself: the read-only source, or
		// the result the host reads back.
		wantFiles := "ro\t/run/enclave/out/files/00/source\t/tmp/host_gitconfig\n" +
			"rw\t/run/enclave/out/files/01/result\t/home/agent/.allow\n"
		if fileManifest != wantFiles {
			t.Errorf("files.tsv =\n%swant\n%s", fileManifest, wantFiles)
		}
	})
	t.Run("file mounts written back", func(t *testing.T) {
		if got := readTestFile(t, paths["allow"]); got != "guest\n" {
			t.Errorf("writable file mount = %q, want the guest's content", got)
		}
		if got := readTestFile(t, paths["gitconfig"]); got != "[user]\n" {
			t.Errorf("read-only file mount changed to %q", got)
		}
	})
	t.Run("disk is an overlay on the base", func(t *testing.T) {
		diskDir := config.HostProjectVMDir(b.opts.Host.Home, "abc123def456", "codex")
		creates := host.commandsOf(qemuImgBinary)
		want := []string{"create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", filepath.Join(baseDir, baseDiskFile), filepath.Join(diskDir, diskFile+".tmp")}
		if len(creates) != 1 || !slices.Equal(creates[0], want) {
			t.Errorf("qemu-img calls = %v, want %v", creates, want)
		}
		if meta := readTestFile(t, filepath.Join(diskDir, diskMetaFile)); !strings.Contains(meta, `"baseKey": "basekey"`) {
			t.Errorf("disk metadata = %s", meta)
		}
		// The reservation ends with the session.
		release, ok, err := util.TryAcquireFileLock(filepath.Join(diskDir, diskLockFile))
		if err != nil || !ok {
			t.Fatalf("disk still reserved after the session: ok=%v err=%v", ok, err)
		}
		release()
	})
	t.Run("vm lifecycle", func(t *testing.T) {
		qemu := host.commandsOf(qemuBinary)
		if len(qemu) != 1 {
			t.Fatalf("qemu started %d times", len(qemu))
		}
		line := strings.Join(qemu[0], " ")
		for _, want := range []string{
			"-machine q35,accel=kvm", "-daemonize",
			"hostfwd=tcp:127.0.0.1:8080-:3000",
			"file=" + filepath.Join(config.HostProjectVMDir(b.opts.Host.Home, "abc123def456", "codex"), diskFile),
			"tag=" + controlInTag, "tag=" + controlOutTag, "tag=enclave-mount-0",
		} {
			if !strings.Contains(line, want) {
				t.Errorf("qemu args lack %q:\n%s", want, line)
			}
		}
		if host.inherited == nil || filepath.Base(host.inherited.Name()) != diskLockFile {
			t.Errorf("QEMU was not given the disk reservation: %v", host.inherited)
		}
		if got := host.guest.received(); !slices.Equal(got, []string{"qmp_capabilities"}) {
			t.Errorf("QMP commands = %v; a guest that powers itself off needs none", got)
		}
		if _, err := os.Stat(runtimeDirSeen); !os.IsNotExist(err) {
			t.Errorf("runtime directory %s survived the session", runtimeDirSeen)
		}
		// One virtiofsd per export: two control exports, project, store, skills.
		if got := len(host.commandsOf("virtiofsd")) - 1; got != 5 {
			t.Errorf("virtiofsd started %d times, want 5", got)
		}
	})
}

func TestRunReportsToolExitCode(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, _ := sessionRequest(t, baseDir)
	host.toolExit = 7
	// ssh's own status is not the tool's: the connection drops when the guest
	// powers off.
	host.sshExit = 255
	status, err := b.Run(context.Background(), req, backend.AttachIO{})
	var exitErr *backend.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 || status.Code != 7 {
		t.Fatalf("Run = %+v, %v; want exit code 7", status, err)
	}
}

// A start right after a foreground session must find the disk free, also
// when the stopped VM's process holds its files a moment longer.
func TestRunReturnsOnceTheDiskIsFree(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, _ := sessionRequest(t, baseDir)
	host.duringSession = func(runtimeFiles) { host.guest.lingers = 50 * time.Millisecond }
	if _, err := b.Run(context.Background(), req, backend.AttachIO{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	next := otherProcess(b, host)
	if err := next.AdmitSession(context.Background(), req.Session); err != nil {
		t.Fatalf("start right after the session: %v", err)
	}
	next.releaseReservation()
}

// The wait for the disk is bounded. A session that ends with its disk still
// held says so instead of reporting success: the start that follows would
// be refused, and no session is left to stop.
func TestRunReportsADiskThatStaysHeld(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, _ := sessionRequest(t, baseDir)
	b.shutdownTimeout = 40 * time.Millisecond
	host.duringSession = func(runtimeFiles) { host.guest.lingers = 400 * time.Millisecond }
	status, err := b.Run(context.Background(), req, backend.AttachIO{})
	if err == nil || status.Code != 1 || !strings.Contains(err.Error(), "its disk is still held") {
		t.Fatalf("Run = %+v, %v; want the held disk reported", status, err)
	}

	// The tool's own failure is what the caller gets when there is one.
	b, host, baseDir = newTestBackend(t)
	req, _ = sessionRequest(t, baseDir)
	b.shutdownTimeout = 40 * time.Millisecond
	host.toolExit = 7
	host.duringSession = func(runtimeFiles) { host.guest.lingers = 400 * time.Millisecond }
	status, err = b.Run(context.Background(), req, backend.AttachIO{})
	var exitErr *backend.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 || status.Code != 7 {
		t.Fatalf("Run = %+v, %v; want exit code 7", status, err)
	}
}

// A runtime directory that is lost while the session runs is no sign that
// the VM has stopped. The session's own monitor connection still reaches the
// VM, and nothing in the directory is taken for the session's any more, the
// exit code included.
func TestRunStopsItsVMWhenTheRuntimeDirectoryIsLost(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, paths := sessionRequest(t, baseDir)
	before := readTestFile(t, paths["allow"])
	host.staysUp = true
	host.duringSession = func(dir runtimeFiles) {
		writeTestFile(t, filepath.Join(dir.out(), "files", "01", "result"), "planted\n")
		if err := os.Chmod(dir.dir, 0o755); err != nil { // #nosec G302 -- the test plays a directory that is not private.
			t.Error(err)
		}
	}
	status, err := b.Run(context.Background(), req, backend.AttachIO{})
	if err == nil || status.Code != 1 || !strings.Contains(err.Error(), "no longer this session's") {
		t.Fatalf("Run = %+v, %v; want the lost runtime directory reported, not the exit code found in it", status, err)
	}
	if !host.guest.isOff() || !slices.Contains(host.guest.received(), "system_powerdown") {
		t.Errorf("off = %v, commands = %v; want the VM shut down over the session's own connection", host.guest.isOff(), host.guest.received())
	}
	if got := readTestFile(t, paths["allow"]); got != before {
		t.Errorf("host file = %q, want it unchanged (%q)", got, before)
	}
	next := otherProcess(b, host)
	if err := next.AdmitSession(context.Background(), req.Session); err != nil {
		t.Fatalf("start after the session: %v", err)
	}
	next.releaseReservation()
}

func TestRunFailsWhenGuestCannotMount(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, _ := sessionRequest(t, baseDir)
	host.bootStatus = "failed: cannot mount /home/agent/.codex (virtiofs)\x1b[2J"
	started := false
	_, err := b.Run(context.Background(), req, backend.AttachIO{OnStarted: func() { started = true }})
	if err == nil || !strings.Contains(err.Error(), "failed: cannot mount /home/agent/.codex (virtiofs)[2J") {
		t.Fatalf("Run error = %v, want the guest's reason without its escape sequence", err)
	}
	if started {
		t.Error("OnStarted ran although the session never started")
	}
	if len(host.commandsOf(sshBinary)) != 0 {
		t.Error("the tool was started after a failed mount")
	}
	if got := host.guest.received(); !slices.Contains(got, "system_powerdown") {
		t.Errorf("QMP commands = %v; the VM was left running", got)
	}
}

func TestRunWithoutExitCodeIsAnError(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, _ := sessionRequest(t, baseDir)
	host.toolExit = -1
	host.sshExit = 255
	status, err := b.Run(context.Background(), req, backend.AttachIO{})
	if err == nil || status.Code != 1 || !strings.Contains(err.Error(), "without reporting an exit code") {
		t.Fatalf("Run = %+v, %v", status, err)
	}
	if got := len(host.commandsOf(sshBinary)); got != sshAttempts {
		t.Errorf("ssh ran %d times, want %d attempts while sshd refuses", got, sshAttempts)
	}
}

func TestRunRefusesDiskInUse(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, _ := sessionRequest(t, baseDir)
	req.Session.Name = "enclave-codex-abc123def456-other"
	diskDir := config.HostProjectVMDir(b.opts.Host.Home, "abc123def456", "codex")
	release, ok, err := util.TryAcquireFileLock(filepath.Join(diskDir, diskLockFile))
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer release()
	_, err = b.Run(context.Background(), req, backend.AttachIO{})
	if err == nil || !strings.Contains(err.Error(), "in use by another session") {
		t.Fatalf("Run error = %v", err)
	}
	if len(host.commandsOf(qemuBinary)) != 0 {
		t.Error("a second VM was started on a reserved disk")
	}
}

func claimDisk(t *testing.T, b *Backend, session backend.SessionMeta, bs base) (string, error) {
	t.Helper()
	if err := b.AdmitSession(context.Background(), session); err != nil {
		return "", err
	}
	return b.ensureDisk(context.Background(), b.reservedDir, bs)
}

func TestDiskOfEarlierBaseIsKept(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	session := backend.SessionMeta{Tool: "codex", ProjectHash: "abc123def456"}
	first := base{Disk: filepath.Join(baseDir, baseDiskFile), Key: "basekey"}
	disk, err := claimDisk(t, b, session, first)
	if err != nil {
		t.Fatal(err)
	}
	b.releaseReservation()
	// A new base key must not recreate or touch the disk.
	again, err := claimDisk(t, b, session, base{Disk: "/elsewhere/base.qcow2", Key: "newer"})
	if err != nil || again != disk {
		t.Fatalf("disk = %q, %v; want the existing %q", again, err, disk)
	}
	b.releaseReservation()
	if got := len(host.commandsOf(qemuImgBinary)); got != 1 {
		t.Errorf("qemu-img ran %d times, want only the first create", got)
	}
	// Without its backing file the disk is unusable; say so instead of booting.
	if err := os.Remove(first.Disk); err != nil {
		t.Fatal(err)
	}
	if _, err := claimDisk(t, b, session, first); err == nil || !strings.Contains(err.Error(), "which is gone") {
		t.Fatalf("disk without its base = %v", err)
	}
}

// A second enclave process is refused before it prepares anything, and the
// admitted one keeps its claim for Run.
func TestAdmitSessionClaimsTheDiskOnce(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, _ := sessionRequest(t, baseDir)
	other := New(b.opts)
	if err := b.AdmitSession(context.Background(), req.Session); err != nil {
		t.Fatal(err)
	}
	if err := b.AdmitSession(context.Background(), req.Session); err != nil {
		t.Fatalf("admitting the same session twice: %v", err)
	}
	if err := other.AdmitSession(context.Background(), req.Session); err == nil || !strings.Contains(err.Error(), "in use by another session") {
		t.Fatalf("a second process was admitted: %v", err)
	}
	if status, err := b.Run(context.Background(), req, backend.AttachIO{}); err != nil || status.Code != 0 {
		t.Fatalf("Run after admission = %+v, %v", status, err)
	}
	if filepath.Base(host.inherited.Name()) != diskLockFile {
		t.Errorf("QEMU inherited %s, not the admitted reservation", host.inherited.Name())
	}
	if err := other.AdmitSession(context.Background(), req.Session); err != nil {
		t.Fatalf("the disk stayed reserved after the session: %v", err)
	}
	other.releaseReservation()
}

func TestRemoveUnlessDiskInUse(t *testing.T) {
	b, _, _ := newTestBackend(t)
	home := b.opts.Host.Home
	session := backend.SessionMeta{Tool: "codex", ProjectHash: "abc123def456"}
	diskDir := config.HostProjectVMDir(home, session.ProjectHash, session.Tool)
	projects := config.HostProjectsDir(home)

	if err := b.AdmitSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	removed := false
	err := RemoveUnlessDiskInUse(home, []string{projects}, func() { removed = true })
	if err == nil || !strings.Contains(err.Error(), diskDir) || removed {
		t.Fatalf("removal under a running VM: err=%v removed=%v; want a refusal naming %s", err, removed, diskDir)
	}
	// What the removal does not touch is not its concern.
	if err := RemoveUnlessDiskInUse(home, []string{config.HostCacheDir(home)}, func() { removed = true }); err != nil || !removed {
		t.Fatalf("removal elsewhere: err=%v removed=%v", err, removed)
	}
	b.releaseReservation()

	// While an allowed removal runs, no VM can claim the disk it found idle.
	inRemoval := make(chan struct{})
	finishRemoval := make(chan struct{})
	removalDone := make(chan error, 1)
	go func() {
		removalDone <- RemoveUnlessDiskInUse(home, []string{projects}, func() {
			close(inRemoval)
			<-finishRemoval
		})
	}()
	<-inRemoval
	claimed := make(chan error, 1)
	go func() { claimed <- b.AdmitSession(context.Background(), session) }()
	select {
	case err := <-claimed:
		t.Fatalf("a VM claimed the disk during its removal (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(finishRemoval)
	if err := <-removalDone; err != nil {
		t.Fatal(err)
	}
	if err := <-claimed; err != nil {
		t.Fatalf("claim after the removal: %v", err)
	}
	b.releaseReservation()
}

func TestCheck(t *testing.T) {
	b, host, _ := newTestBackend(t)
	if err := b.Check(context.Background()); err != nil {
		t.Fatalf("Check on a complete host: %v", err)
	}
	for _, binary := range []string{qemuBinary, qemuImgBinary, sshBinary, sshKeygenBinary, "virtiofsd"} {
		host.missing[binary] = true
		if err := b.Check(context.Background()); err == nil || !strings.Contains(err.Error(), binary) {
			t.Errorf("Check without %s = %v", binary, err)
		}
		delete(host.missing, binary)
	}
	b.kvmPath = filepath.Join(t.TempDir(), "missing")
	if err := b.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "kvm group") {
		t.Errorf("Check without KVM = %v", err)
	}
	b.platform = "darwin/arm64"
	if err := b.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "darwin/arm64") {
		t.Errorf("Check on another platform = %v", err)
	}
}

func TestGuestUser(t *testing.T) {
	for _, tc := range []struct {
		user  string
		admin bool
		want  string
	}{{"", false, "agent"}, {"agent", false, "agent"}, {"", true, "root"}, {"root", false, "root"}} {
		got, err := guestUser(backend.Request{User: tc.user, Security: backend.SecurityPosture{Admin: tc.admin}})
		if err != nil || got != tc.want {
			t.Errorf("guestUser(%q, admin=%v) = %q, %v; want %q", tc.user, tc.admin, got, err, tc.want)
		}
	}
	if _, err := guestUser(backend.Request{User: "vscode"}); err == nil {
		t.Error("guestUser accepted an unknown user")
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func sameLines(a string, b string) bool {
	left := strings.Split(strings.TrimSpace(a), "\n")
	right := strings.Split(strings.TrimSpace(b), "\n")
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func TestRunRefusesMountOverControlDirectory(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, paths := sessionRequest(t, baseDir)
	for _, target := range []string{"/run", "/run/enclave", "/run/enclave/in/x"} {
		req.Mounts = []backend.Mount{{Type: backend.MountTypeBind, Source: paths["skills"], ContainerPath: target}}
		if _, err := b.Run(context.Background(), req, backend.AttachIO{}); err == nil || !strings.Contains(err.Error(), "/run/enclave") {
			t.Errorf("mount at %s = %v; it would hide the control exports", target, err)
		}
	}
	if len(host.commandsOf(qemuBinary)) != 0 {
		t.Error("a VM was started for a refused mount")
	}
}

// The reservation is a lock QEMU inherits, so it holds while the VM runs even
// when the process that started it is gone.
func TestReservationOutlivesTheStartingProcess(t *testing.T) {
	b, _, _ := newTestBackend(t)
	session := backend.SessionMeta{Tool: "codex", ProjectHash: "abc123def456"}
	if err := b.AdmitSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	// A daemon that outlives its launcher, as QEMU does.
	if out, err := (execHost{}).Daemonize(context.Background(), "sh", []string{"-c", "sleep 1 &"}, b.reservation); err != nil {
		t.Fatalf("Daemonize: %v: %s", err, out)
	}
	b.releaseReservation()
	if err := b.AdmitSession(context.Background(), session); err == nil || !strings.Contains(err.Error(), "in use by another session") {
		t.Fatalf("claim while the daemon runs = %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := b.AdmitSession(context.Background(), session)
		if err == nil {
			b.releaseReservation()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the disk stayed reserved after the daemon exited: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
