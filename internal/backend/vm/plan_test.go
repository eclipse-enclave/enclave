// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend/vmhost"
)

func TestQemuArgsForSession(t *testing.T) {
	rt := runtimeFiles{dir: "/run/user/1000/enclave-vm-x"}
	got, err := qemuArgs(machine{
		Name:      "enclave-codex-abc",
		MemoryMiB: 4096,
		CPUs:      2,
		Disk:      "/state/vm/disk.qcow2",
		Shares: []share{
			{Tag: controlInTag, Source: rt.in(), ReadOnly: true, Transport: transportVirtiofs},
			{Tag: "enclave-mount-0", Source: "/home/u/project", Target: "/home/u/project", Transport: transportVirtiofs},
			{Tag: "enclave-mount-1", Source: "/home/u/skills", Target: "/skills", ReadOnly: true, Transport: transport9P},
		},
		Hostfwd: []string{"tcp:127.0.0.1:40022-:22"},
		Console: rt.console(),
		QMP:     rt.qmp(),
		Control: rt.control(),
		PIDFile: rt.pidFile(),
		socket:  rt.socket,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-nodefaults",
		"-no-user-config",
		"-name", "enclave-codex-abc",
		"-machine", "q35,accel=kvm",
		"-cpu", "host",
		"-smp", "2",
		"-m", "4096",
		"-object", "memory-backend-memfd,id=mem,size=4096M,share=on",
		"-numa", "node,memdev=mem",
		"-display", "none",
		"-serial", "file:/run/user/1000/enclave-vm-x/console.log",
		"-drive", "file=/state/vm/disk.qcow2,if=none,id=disk0,format=qcow2,discard=unmap",
		"-device", "virtio-blk-pci,drive=disk0,bootindex=1,addr=0x1.0x0,multifunction=on",
		"-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:40022-:22",
		"-device", "virtio-net-pci,netdev=net0,addr=0x1.0x1",
		"-chardev", "socket,id=fs0,path=/run/user/1000/enclave-vm-x/fs0.sock",
		"-device", "vhost-user-fs-pci,chardev=fs0,tag=enclave-in,addr=0x1.0x2",
		"-chardev", "socket,id=fs1,path=/run/user/1000/enclave-vm-x/fs1.sock",
		"-device", "vhost-user-fs-pci,chardev=fs1,tag=enclave-mount-0,addr=0x1.0x3",
		"-fsdev", "local,id=fs2,path=/home/u/skills,security_model=none,readonly=on",
		"-device", "virtio-9p-pci,fsdev=fs2,mount_tag=enclave-mount-1,addr=0x1.0x4",
		"-qmp", "unix:/run/user/1000/enclave-vm-x/qmp.sock,server=on,wait=off",
		"-qmp", "unix:/run/user/1000/enclave-vm-x/ctl.sock,server=on,wait=off",
		"-pidfile", "/run/user/1000/enclave-vm-x/qemu.pid",
		"-daemonize",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("qemuArgs =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestQemuArgsForProvisioningStayInForeground(t *testing.T) {
	got, err := qemuArgs(machine{Name: "base", MemoryMiB: 2048, CPUs: 1, Disk: "/b/base.qcow2", Seed: "/r/seed.iso", Console: "/r/console.log"})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(got, " ")
	if !strings.Contains(line, "-drive file=/r/seed.iso,if=none,id=seed0,format=raw,readonly=on -device virtio-blk-pci,drive=seed0,addr=0x1.0x2") {
		t.Errorf("no seed drive in %s", line)
	}
	for _, flag := range []string{"-daemonize", "-qmp", "-pidfile", "hostfwd"} {
		if strings.Contains(line, flag) {
			t.Errorf("provisioning args contain %s: %s", flag, line)
		}
	}
}

func TestQemuArgsRejectCommas(t *testing.T) {
	if _, err := qemuArgs(machine{Name: "a,b", Disk: "/d"}); err == nil || !strings.Contains(err.Error(), "vm backend: session name") {
		t.Errorf("comma in name: %v", err)
	}
	shares := []share{{Tag: "t", Source: "/with,comma", Transport: transport9P}}
	if _, err := qemuArgs(machine{Name: "a", Disk: "/d", Shares: shares}); err == nil || !strings.Contains(err.Error(), "mount source") {
		t.Errorf("comma in mount source: %v", err)
	}
}

func TestPCIAddrsUseEveryFunction(t *testing.T) {
	addrs, err := pciAddrs(10)
	if err != nil {
		t.Fatal(err)
	}
	if addrs[0] != ",addr=0x1.0x0,multifunction=on" || addrs[7] != ",addr=0x1.0x7" || addrs[8] != ",addr=0x2.0x0,multifunction=on" || addrs[9] != ",addr=0x2.0x1" {
		t.Errorf("addrs = %v", addrs)
	}
	// The last device alone in its slot needs no multifunction bit.
	if addrs, _ := pciAddrs(9); addrs[8] != ",addr=0x2.0x0" {
		t.Errorf("last addr = %q", addrs[8])
	}
	if addrs, err := pciAddrs(240); err != nil || addrs[239] != ",addr=0x1e.0x7" {
		t.Errorf("240 devices: %v, last %v", err, addrs)
	}
	if _, err := pciAddrs(241); err == nil {
		t.Error("241 devices fit a bus with 240 functions")
	}
}

func TestReadOnlyExportsAreEnforcedOnTheHost(t *testing.T) {
	withFlag := virtiofsdCaps{ReadOnly: true, InodeFileHandles: true}
	without := virtiofsdCaps{}
	if got := exportTransport(true, withFlag); got != transportVirtiofs {
		t.Errorf("read-only with --readonly goes through %s", got)
	}
	if got := exportTransport(true, without); got != transport9P {
		t.Errorf("read-only without --readonly goes through %s, where only the guest enforces it", got)
	}
	if got := exportTransport(false, without); got != transportVirtiofs {
		t.Errorf("writable export goes through %s", got)
	}

	ro := share{Source: "/src", ReadOnly: true}
	if got, want := virtiofsdArgs(ro, "/s.sock", withFlag), []string{"--socket-path=/s.sock", "--shared-dir=/src", "--sandbox", "none", "--readonly", "--inode-file-handles=prefer"}; !slices.Equal(got, want) {
		t.Errorf("virtiofsdArgs = %v, want %v", got, want)
	}
	if got, want := virtiofsdArgs(share{Source: "/src"}, "/s.sock", without), []string{"--socket-path=/s.sock", "--shared-dir=/src", "--sandbox", "none"}; !slices.Equal(got, want) {
		t.Errorf("virtiofsdArgs = %v, want %v", got, want)
	}
}

func TestSessionSharesOrderAndFallback(t *testing.T) {
	rt := runtimeFiles{dir: "/r"}
	mounts := []vmhost.DirMount{
		{Tag: "child", Source: "/s/child", Target: "/home/agent/.tool/skills", ReadOnly: true},
		{Tag: "parent", Source: "/s/parent", Target: "/home/agent/.tool", CacheMmap: true},
	}
	shares := sessionShares(rt, mounts, virtiofsdCaps{})
	var got []string
	for _, sh := range shares {
		got = append(got, sh.Tag+":"+string(sh.Transport))
	}
	// Without --readonly the read-only exports, the control input among
	// them, go through 9p.
	want := []string{"enclave-in:9p", "enclave-out:virtiofs", "parent:virtiofs", "child:9p"}
	if !slices.Equal(got, want) {
		t.Fatalf("shares = %v, want %v", got, want)
	}
	manifest, err := mountsManifest(shares)
	if err != nil {
		t.Fatal(err)
	}
	if want := "virtiofs\tparent\trw\tmmap\t/home/agent/.tool\n9p\tchild\tro\tdefault\t/home/agent/.tool/skills\n"; manifest != want {
		t.Errorf("manifest = %q, want %q", manifest, want)
	}
	if _, err := mountsManifest([]share{{Tag: "t", Target: "/a\tb"}}); err == nil {
		t.Error("a tab in a target would shift the manifest columns")
	}
}

func TestSSHArgsUseOnlyTheSessionIdentity(t *testing.T) {
	rt := runtimeFiles{dir: "/r"}
	want := []string{
		"-F", "/dev/null",
		"-i", "/r/client_key",
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "UserKnownHostsFile=/r/known_hosts",
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "BatchMode=yes",
		"-o", "ForwardAgent=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "ConnectTimeout=10",
		"-o", "LogLevel=ERROR",
		"-p", "40022",
		"-tt",
		"root@127.0.0.1",
		"/bin/bash /run/enclave/in/session",
	}
	if got := sshArgs(rt, 40022, true); !slices.Equal(got, want) {
		t.Errorf("sshArgs = %v, want %v", got, want)
	}
	if got := sshArgs(rt, 40022, false); !slices.Contains(got, "-T") || slices.Contains(got, "-tt") {
		t.Errorf("sshArgs without a terminal = %v", got)
	}
}

func TestWriteKnownHostsPinsTheGeneratedHostKey(t *testing.T) {
	rt := runtimeFiles{dir: privateTempDir(t)}
	if err := os.Mkdir(rt.in(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.hostKey()+".pub", []byte("ssh-ed25519 AAAAKEY enclave-vm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeKnownHosts(rt, 40022); err != nil {
		t.Fatal(err)
	}
	if got, want := readTestFile(t, rt.knownHosts()), "[127.0.0.1]:40022 ssh-ed25519 AAAAKEY\n"; got != want {
		t.Errorf("known_hosts = %q, want %q", got, want)
	}
}

func TestReadGuestFileStaysInsideTheExport(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readGuestFile(dir, "missing", 16); ok || err != nil {
		t.Errorf("missing file: ok=%v err=%v", ok, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := readGuestFile(dir, "status", 16); got != "ready\n" || !ok || err != nil {
		t.Errorf("regular file: %q ok=%v err=%v", got, ok, err)
	}
	if _, _, err := readGuestFile(dir, "status", 3); err == nil {
		t.Error("a file over the limit was read")
	}
	if err := os.Symlink(secret, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if got, _, err := readGuestFile(dir, "link", 64); err == nil {
		t.Errorf("a symlink out of the export was followed: %q", got)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readGuestFile(dir, "sub", 64); err == nil {
		t.Error("a directory was read as a file")
	}
}

// stageFileMount stages one writable file mount as a session start does and
// returns it with its record.
func stageFileMount(t *testing.T, rt runtimeDir, content string) (vmhost.FileMount, []fileRecord) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "mounted")
	stage := filepath.Join(filesOf(rt).out(), "files", "00")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(stage, "result")
	for _, path := range []string{source, result} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	file := vmhost.FileMount{Source: source, Target: "/t", ResultPath: result}
	records, err := recordFileMounts([]vmhost.FileMount{file})
	if err != nil {
		t.Fatal(err)
	}
	return file, records
}

func TestPersistFileMounts(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		// during changes the staged result (the guest) and the host source.
		during  func(t *testing.T, file vmhost.FileMount)
		want    string
		wantErr string
	}{
		{name: "guest edit is written back", want: "guest", during: func(t *testing.T, file vmhost.FileMount) {
			writeTestFile(t, file.ResultPath, "guest")
		}},
		{name: "untouched file is not rewritten", want: "host edit", during: func(t *testing.T, file vmhost.FileMount) {
			// A failed boot leaves the staged copy as it was; a host edit
			// made meanwhile must survive.
			writeTestFile(t, file.Source, "host edit")
		}},
		{name: "host edit is not overwritten", want: "host edit", wantErr: "changed on the host", during: func(t *testing.T, file vmhost.FileMount) {
			writeTestFile(t, file.ResultPath, "guest")
			writeTestFile(t, file.Source, "host edit")
		}},
		{name: "replaced host file is not written", want: "replacement", wantErr: "no longer the file that was mounted", during: func(t *testing.T, file vmhost.FileMount) {
			// As an editor saves: a new file renamed over the old one.
			writeTestFile(t, file.ResultPath, "guest")
			writeTestFile(t, file.Source+".new", "replacement")
			if err := os.Rename(file.Source+".new", file.Source); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "source swapped for a link to another file", want: "host secret", wantErr: "no longer the file that was mounted", during: func(t *testing.T, file vmhost.FileMount) {
			// The mount's directory can be a writable share: the guest points
			// the source path at a file it wants overwritten.
			writeTestFile(t, file.ResultPath, "guest")
			if err := os.Remove(file.Source); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(secret, file.Source); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "result swapped for a link to a host file", want: "original", wantErr: "read guest file", during: func(t *testing.T, file vmhost.FileMount) {
			if err := os.Remove(file.ResultPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(secret, file.ResultPath); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "failed write leaves the host file", want: "original", wantErr: "permission denied", during: func(t *testing.T, file vmhost.FileMount) {
			// The new content cannot be staged next to the file.
			writeTestFile(t, file.ResultPath, "guest")
			dir := filepath.Dir(file.Source)
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		}},
		{name: "result removed", want: "original", wantErr: "removed its copy", during: func(t *testing.T, file vmhost.FileMount) {
			if err := os.Remove(file.ResultPath); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := runtimeDirAt(privateTempDir(t), "")
			file, records := stageFileMount(t, rt, "original")
			tc.during(t, file)
			errs := persistFileMounts(rt, records)
			switch {
			case tc.wantErr == "" && len(errs) != 0:
				t.Fatalf("errs = %v", errs)
			case tc.wantErr != "" && (len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.wantErr)):
				t.Fatalf("errs = %v, want one containing %q", errs, tc.wantErr)
			}
			if got := readTestFile(t, file.Source); got != tc.want {
				t.Fatalf("host file = %q, want %q", got, tc.want)
			}
			if got := readTestFile(t, secret); got != "host secret" {
				t.Fatalf("another host file was overwritten with %q", got)
			}
		})
	}
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDialQMPGivesUpOnASilentMonitor(t *testing.T) {
	previous := qmpIOTimeout
	qmpIOTimeout = 100 * time.Millisecond
	t.Cleanup(func() { qmpIOTimeout = previous })
	socket := filepath.Join(shortTempDir(t), "qmp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		// Accept and never send the greeting.
		if conn, err := listener.Accept(); err == nil {
			defer func() { _ = conn.Close() }()
			time.Sleep(5 * time.Second)
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := dialQMP(context.Background(), socket)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a monitor that never greeted was accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dialQMP waited on a silent monitor past its timeout")
	}
}

func TestReadGuestExitCode(t *testing.T) {
	rt := runtimeDirAt(privateTempDir(t), "")
	if err := os.Mkdir(filesOf(rt).out(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readGuestExitCode(rt); ok || err != nil {
		t.Errorf("no report: ok=%v err=%v", ok, err)
	}
	for content, want := range map[string]int{"0\n": 0, "137\n": 137} {
		if err := os.WriteFile(filepath.Join(filesOf(rt).out(), guestExitCodeFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := readGuestExitCode(rt); got != want || !ok || err != nil {
			t.Errorf("%q: %d ok=%v err=%v", content, got, ok, err)
		}
	}
	for _, content := range []string{"", "256\n", "-1\n", "ok\n"} {
		if err := os.WriteFile(filepath.Join(filesOf(rt).out(), guestExitCodeFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readGuestExitCode(rt); err == nil {
			t.Errorf("%q was accepted as an exit code", content)
		}
	}
}

func TestQMPClientSeesQEMUExit(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "qmp.sock")
	guest, err := startFakeGuest("test", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.powerOff()
	client, err := dialQMP(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.close()
	if client.exited() || client.waitClosed(10*time.Millisecond) {
		t.Fatal("a running VM is reported as exited")
	}
	if _, err := client.execute(context.Background(), "query-status"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := client.execute(context.Background(), "quit"); err != nil {
		t.Fatalf("quit: %v", err)
	}
	if !client.waitClosed(2 * time.Second) {
		t.Fatal("QEMU's exit was not noticed")
	}
	if got := guest.received(); !slices.Equal(got, []string{"qmp_capabilities", "query-status", "quit"}) {
		t.Errorf("commands = %v", got)
	}
}

// The handshake succeeding says nothing about the next command.
func TestQMPCommandGivesUpOnASilentMonitor(t *testing.T) {
	previous := qmpIOTimeout
	qmpIOTimeout = 100 * time.Millisecond
	t.Cleanup(func() { qmpIOTimeout = previous })
	socket := filepath.Join(shortTempDir(t), "qmp.sock")
	guest, err := startFakeGuest("test", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.powerOff()
	guest.mu.Lock()
	guest.mute = true
	guest.mu.Unlock()
	client, err := dialQMP(context.Background(), socket)
	if err != nil {
		t.Fatalf("dialQMP: %v", err)
	}
	defer client.close()
	done := make(chan error, 1)
	go func() {
		_, err := client.execute(context.Background(), "query-name")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no reply") {
			t.Fatalf("execute = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a command without a reply waited past its timeout")
	}
}

func TestSanitizeDropsTerminalControl(t *testing.T) {
	if got := sanitize("ok\x1b[31m red\x07\tkept\nline\x7f"); got != "ok[31m red\tkept\nline" {
		t.Errorf("sanitize = %q", got)
	}
}

// shortTempDir is a temporary directory whose path fits a unix socket address.
// What the session's own process does in its runtime directory while the VM
// runs follows the same rule as later commands: nothing is read from or
// removed in a directory that is no longer the session's.
func TestRuntimeDirectoryIsNotUsedOnceItIsNotTheSessions(t *testing.T) {
	rt, err := newRuntimeDir(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filesOf(rt).payload(), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(filesOf(rt).out(), guestExitCodeFile), "0\n")
	writeTestFile(t, filepath.Join(filesOf(rt).out(), guestStartedFile), "")
	writeTestFile(t, filesOf(rt).console(), "console line\n")
	if code, ok, err := readGuestExitCode(rt); err != nil || !ok || code != 0 || !guestStarted(rt) || rt.consoleTail(5) == "" {
		t.Fatalf("a private runtime directory was not read: %d %v %v", code, ok, err)
	}

	if err := os.Chmod(rt.name(), 0o755); err != nil { // #nosec G302 -- the test plays a directory that is not private.
		t.Fatal(err)
	}
	if _, ok, err := readGuestExitCode(rt); err == nil || ok {
		t.Errorf("an exit code was accepted: ok=%v err=%v", ok, err)
	}
	if guestStarted(rt) {
		t.Error("the started mark was accepted")
	}
	if tail := rt.consoleTail(5); tail != "" {
		t.Errorf("the console log was read: %q", tail)
	}
	if err := rt.removePayload(); err == nil {
		t.Error("removePayload reported success")
	}
	rt.remove()
	if _, err := os.Stat(filesOf(rt).payload()); err != nil {
		t.Errorf("the directory's content was removed: %v", err)
	}
}

// privateTempDir is a temp directory with the mode of a runtime directory.
func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "evm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestExecHostOutputDoesNotWaitForADaemon(t *testing.T) {
	// The child keeps the output descriptors open far longer than the command
	// runs, as a daemonized QEMU does.
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = execHost{}.Output(context.Background(), "sh", "-c", "echo started; sleep 30 &")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Output waited for the background process holding its output")
	}
	if err != nil || string(out) != "started\n" {
		t.Fatalf("Output = %q, %v", out, err)
	}
}

// A source that is a link stays a link: the file it points to is replaced.
func TestPersistFileMountKeepsALinkedSourceALink(t *testing.T) {
	rt := runtimeDirAt(privateTempDir(t), "")
	file, _ := stageFileMount(t, rt, "original")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(file.Source, link); err != nil {
		t.Fatal(err)
	}
	target := file.Source
	file.Source = link
	records, err := recordFileMounts([]vmhost.FileMount{file})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, file.ResultPath, "guest")
	if errs := persistFileMounts(rt, records); len(errs) != 0 {
		t.Fatal(errs)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the linked source is no longer a link: %v, %v", info, err)
	}
	if got := readTestFile(t, target); got != "guest" {
		t.Fatalf("link target = %q", got)
	}
}
