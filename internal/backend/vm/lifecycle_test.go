// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
)

// startDetached starts a detached session of sessionRequest and returns the
// backend, its fake host, the request's host paths and the session's record.
func startDetached(t *testing.T) (*Backend, *fakeHost, map[string]string, sessionRecord) {
	t.Helper()
	b, host, baseDir := newTestBackend(t)
	paths, record := startDetachedOn(t, b, baseDir)
	return b, host, paths, record
}

func startDetachedOn(t *testing.T, b *Backend, baseDir string) (map[string]string, sessionRecord) {
	t.Helper()
	req, paths := sessionRequest(t, baseDir)
	req.Detached = true
	req.Session.Background = true
	req.Session.DisplayName = "1"
	req.Session.Name += "-1"
	req.Session.Worktree = paths["project"]
	req.AuthSync = &backend.AuthSyncSpec{AuthFiles: []string{"auth.json"}}
	req.Stores = append(req.Stores, backend.PersistentStore{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "codex"}, ContainerPath: "/home/agent/.codex-auth"})
	ref, err := b.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ref.Name != req.Session.Name {
		t.Fatalf("Start returned %q, want %q", ref.Name, req.Session.Name)
	}
	record, ok, err := b.readRecord(ref.Name)
	if err != nil || !ok {
		t.Fatalf("no record of the started session: ok=%v err=%v", ok, err)
	}
	return paths, record
}

// otherProcess is a second enclave process on the same host: a new backend
// that knows the session only from its record.
func otherProcess(b *Backend, host *fakeHost) *Backend {
	other := New(b.opts)
	other.host = host
	other.vmProcess = host.running
	other.killProcess = host.kill
	other.platform, other.kvmPath = b.platform, b.kvmPath
	other.pollInterval, other.exportTimeout = b.pollInterval, b.exportTimeout
	other.bootTimeout, other.shutdownTimeout = b.bootTimeout, b.shutdownTimeout
	return other
}

func TestStartLeavesADetachedSessionRunning(t *testing.T) {
	b, host, paths, record := startDetached(t)

	if host.guest.isOff() {
		t.Fatal("the VM stopped when Start returned")
	}
	if got := host.launch["argv"]; !strings.HasPrefix(got, defaultEntrypoint+"\x00codex\x00") {
		t.Errorf("the tool was not launched: argv %q", got)
	}
	detached := host.commandsOf(sshBinary)
	if len(detached) != 1 || !strings.HasSuffix(detached[0][len(detached[0])-1], "/session 'detached'") || !slices.Contains(detached[0], "-T") {
		t.Errorf("ssh calls = %v, want one detached launch without a terminal", detached)
	}
	if _, err := os.Stat(filesOf(record.runtime()).payload()); !os.IsNotExist(err) {
		t.Error("the launch payload outlived the start")
	}
	// Not in the login session's runtime directory, which a logout removes
	// while the VM keeps running.
	if want := filepath.Join(config.HostVMDir(b.home()), "run"); filepath.Dir(record.RuntimeDir) != want {
		t.Errorf("runtime directory %s, want one under %s", record.RuntimeDir, want)
	}
	raw := readTestFile(t, filepath.Join(config.HostVMDir(b.home()), "sessions", record.Instance+".json"))
	for _, secret := range []string{"s3cret", "TOKEN"} {
		if strings.Contains(raw, secret) {
			t.Errorf("the session record contains %q:\n%s", secret, raw)
		}
	}

	// Another enclave process finds the session and cannot take its disk.
	other := otherProcess(b, host)
	sessions, err := other.List(context.Background(), backend.SessionFilter{})
	if err != nil || len(sessions) != 1 {
		t.Fatalf("List = %+v, %v", sessions, err)
	}
	got := sessions[0]
	if got.Ref.Name != record.Name || got.Status != statusRunning || !got.Background || got.Name != "1" ||
		got.Tool != "codex" || got.ProjectHash != "abc123def456" || got.Worktree != paths["project"] || got.ProjectDir != paths["project"] {
		t.Errorf("listed session = %+v", got)
	}
	if len(got.Ports) != 1 || got.Ports[0].HostPort != "8080" || got.Ports[0].ContainerPort != "3000" {
		t.Errorf("listed ports = %+v, want the published port and not the ssh forward", got.Ports)
	}
	inspected, err := other.Inspect(context.Background(), backend.SessionRef{Name: record.Name})
	if err != nil || inspected.Status != statusRunning {
		t.Fatalf("Inspect = %+v, %v", inspected, err)
	}
	meta := backend.SessionMeta{Tool: "codex", ProjectHash: "abc123def456"}
	if err := other.AdmitSession(context.Background(), meta); err == nil || !strings.Contains(err.Error(), "in use by another session") {
		t.Fatalf("a second session was admitted while the detached VM runs: %v", err)
	}
}

func TestAttachJoinsTheToolsTerminal(t *testing.T) {
	b, host, _, record := startDetached(t)
	other := otherProcess(b, host)
	ref := backend.SessionRef{Name: record.Name}
	if err := other.Attach(context.Background(), ref, backend.AttachIO{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	calls := host.commandsOf(sshBinary)
	attach := calls[len(calls)-1]
	if want := `/bin/bash /run/enclave/in/session 'attach' '^\'`; attach[len(attach)-1] != want {
		t.Errorf("attach command = %q, want %q", attach[len(attach)-1], want)
	}
	if !slices.Contains(attach, filesOf(record.runtime()).clientKey()) {
		t.Errorf("attach does not use the session's key: %v", attach)
	}
	if err := other.Attach(context.Background(), ref, backend.AttachIO{DetachKeys: "ctrl-p,ctrl-q"}); err == nil || !strings.Contains(err.Error(), "detach keys") {
		t.Errorf("a key sequence dtach cannot detach on = %v", err)
	}

	// A session that ends while attached takes the connection with it.
	host.sshExit = 255
	host.guest.powerOff()
	if err := other.Attach(context.Background(), ref, backend.AttachIO{}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("Attach to a stopped session = %v", err)
	}
}

func TestDetachKeys(t *testing.T) {
	for keys, want := range map[string]string{`ctrl-\`: `^\`, "ctrl-p": "^P", "CTRL-A": "^A", "ctrl-]": "^]"} {
		if got, err := dtachDetachKey(keys); err != nil || got != want {
			t.Errorf("dtachDetachKey(%q) = %q, %v; want %q", keys, got, err, want)
		}
	}
	for _, keys := range []string{"", "ctrl-p,ctrl-q", "a", "ctrl-", "ctrl-1", "alt-x"} {
		if got, err := dtachDetachKey(keys); err == nil {
			t.Errorf("dtachDetachKey(%q) = %q, want an error", keys, got)
		}
	}
}

func TestStopShutsDownAndFinalizes(t *testing.T) {
	b, host, paths, record := startDetached(t)
	// The tool edited a writable file mount and logged in.
	writeTestFile(t, record.Files[0].ResultPath, "guest\n")
	configKey := backend.StoreKey{Owner: "codex", ProjectHash: "abc123def456"}
	if err := b.Storage().WriteFile(context.Background(), configKey, backend.StoreKindConfig, "auth.json", []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}

	other := otherProcess(b, host)
	ref := backend.SessionRef{Name: record.Name}
	if err := other.Stop(context.Background(), ref, backend.StopOptions{Finalize: true, Timeout: time.Second}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !host.guest.isOff() {
		t.Fatal("the VM still runs after Stop")
	}
	if got := host.guest.received(); !slices.Contains(got, "query-name") || !slices.Contains(got, "system_powerdown") {
		t.Errorf("monitor commands = %v, want the name check and a powerdown", got)
	}
	if got := readTestFile(t, paths["allow"]); got != "guest\n" {
		t.Errorf("writable file mount after Stop = %q", got)
	}
	shared, err := b.Storage().ReadFile(context.Background(), backend.StoreKey{Owner: "codex"}, backend.StoreKindAuth, "auth.json")
	if err != nil || string(shared) != "credential" {
		t.Errorf("shared auth store after Stop = %q, %v; the session's login was not synced", shared, err)
	}
	stopped, ok, err := other.readRecord(record.Name)
	if err != nil || !ok || !stopped.Finalized {
		t.Fatalf("record after Stop: %+v ok=%v err=%v", stopped, ok, err)
	}

	if sessions, _ := other.List(context.Background(), backend.SessionFilter{}); len(sessions) != 0 {
		t.Errorf("a stopped session is listed as running: %+v", sessions)
	}
	all, _ := other.List(context.Background(), backend.SessionFilter{All: true})
	if len(all) != 1 || all[0].Status != statusExited {
		t.Errorf("List --all = %+v, want the exited session", all)
	}
	// Stopping again is harmless and finalizes nothing twice.
	writeTestFile(t, paths["allow"], "host edit after the session\n")
	if err := other.Stop(context.Background(), ref, backend.StopOptions{Finalize: true}); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if got := readTestFile(t, paths["allow"]); got != "host edit after the session\n" {
		t.Errorf("a second Stop wrote the file mount back again: %q", got)
	}

	if err := other.RemoveWithoutFinalize(context.Background(), ref); err != nil {
		t.Fatalf("RemoveWithoutFinalize: %v", err)
	}
	if _, ok, _ := other.readRecord(record.Name); ok {
		t.Error("the record survived its removal")
	}
	if _, err := os.Stat(record.RuntimeDir); !os.IsNotExist(err) {
		t.Error("the runtime directory survived the session's removal")
	}
	if _, err := other.Inspect(context.Background(), ref); err == nil {
		t.Error("a removed session can still be inspected")
	}
}

// A detached tool that exits by itself powers its guest off with nobody
// there to finalize. The next session of the project does it before it
// prepares anything.
func TestNextSessionFinalizesOneThatEndedOnItsOwn(t *testing.T) {
	b, host, paths, record := startDetached(t)
	writeTestFile(t, record.Files[0].ResultPath, "guest\n")
	host.toolExits(0)

	other := otherProcess(b, host)
	if sessions, _ := other.List(context.Background(), backend.SessionFilter{All: true}); len(sessions) != 1 || sessions[0].Status != statusExited {
		t.Fatalf("List --all = %+v", sessions)
	}
	if got := readTestFile(t, paths["allow"]); got != "host\n" {
		t.Fatalf("file mount written back before anyone finalized: %q", got)
	}
	meta := backend.SessionMeta{Tool: "codex", ProjectHash: "abc123def456"}
	if err := other.AdmitSession(context.Background(), meta); err != nil {
		t.Fatalf("AdmitSession after the earlier session ended: %v", err)
	}
	defer other.releaseReservation()
	if got := readTestFile(t, paths["allow"]); got != "guest\n" {
		t.Errorf("file mount after admission = %q, want the earlier session's edit", got)
	}
	finalized, ok, _ := other.readRecord(record.Name)
	if !ok || !finalized.Finalized {
		t.Errorf("record of the earlier detached session: %+v ok=%v; it stays listed until removed", finalized, ok)
	}
}

// A foreground run whose enclave process was killed leaves a record nobody
// will remove. Admission finalizes it and drops it.
func TestAdmissionClearsAKilledForegroundSession(t *testing.T) {
	b, _, _ := newTestBackend(t)
	rt, err := newRuntimeDir("")
	if err != nil {
		t.Fatal(err)
	}
	file, files := stageFileMount(t, rt, "original")
	writeTestFile(t, file.ResultPath, "guest")
	record := sessionRecord{Instance: filepath.Base(rt.name()), Name: "enclave-codex-abc123def456", Tool: "codex", ProjectHash: "abc123def456", RuntimeDir: rt.name(), Files: files}
	if err := b.writeRecord(record); err != nil {
		t.Fatal(err)
	}
	// A session of another project is none of this admission's business.
	foreign := sessionRecord{Instance: "foreign-instance", Name: "enclave-codex-fff000fff000", Tool: "codex", ProjectHash: "fff000fff000"}
	if err := b.writeRecord(foreign); err != nil {
		t.Fatal(err)
	}
	if err := b.AdmitSession(context.Background(), backend.SessionMeta{Tool: "codex", ProjectHash: "abc123def456"}); err != nil {
		t.Fatal(err)
	}
	defer b.releaseReservation()
	if got := readTestFile(t, file.Source); got != "guest" {
		t.Errorf("file mount of the killed session = %q", got)
	}
	if _, ok, _ := b.readRecord(record.Name); ok {
		t.Error("the killed foreground session is still recorded")
	}
	if _, err := os.Stat(rt.name()); !os.IsNotExist(err) {
		t.Error("its runtime directory was left behind")
	}
	if left, ok, _ := b.readRecord(foreign.Name); !ok || left.Finalized {
		t.Errorf("another project's session was touched: %+v ok=%v", left, ok)
	}
}

// `enclave stop` on a foreground session of another terminal runs while that
// session's own process still holds the disk and the only copy of what the
// guest wrote.
func TestEndSessionLeavesAForegroundSessionToItsOwner(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, paths := sessionRequest(t, baseDir)
	other := otherProcess(b, host)
	ref := backend.SessionRef{Name: req.Session.Name}
	ended := make(chan error, 1)
	host.duringSession = func(rt runtimeFiles) {
		writeTestFile(t, filepath.Join(rt.out(), "files", "01", "result"), "guest\n")
		go func() {
			ended <- other.EndSession(context.Background(), ref, backend.StopOptions{Finalize: true, Timeout: time.Second})
		}()
		// The tool ends when the guest goes down under it.
		for !host.guest.isOff() {
			time.Sleep(time.Millisecond)
		}
	}
	if _, err := b.Run(context.Background(), req, backend.AttachIO{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := <-ended; err != nil {
		t.Fatalf("stop of a foreground session: %v", err)
	}
	if got := readTestFile(t, paths["allow"]); got != "guest\n" {
		t.Errorf("file mount = %q; the stop removed what the owner had to write back", got)
	}
	if _, ok, _ := b.readRecord(req.Session.Name); ok {
		t.Error("a finished foreground session is still recorded")
	}
}

// A new session of the same name must not publish its record between a
// stop's last check and its removal of the old record.
// Publishing waits for the sessions lock, and what is removed is the old
// instance's own file.
func TestForgetAndPublishOfTheSameNameDoNotInterleave(t *testing.T) {
	b, host, _, record := startDetached(t)
	stopper := otherProcess(b, host)
	if err := stopper.Stop(context.Background(), backend.SessionRef{Name: record.Name}, backend.StopOptions{Finalize: true}); err != nil {
		t.Fatal(err)
	}
	starter := otherProcess(b, host)
	successorDir := shortTempDir(t)
	successor := record
	successor.Instance = filepath.Base(successorDir)
	successor.RuntimeDir = successorDir
	successor.CreatedAt = record.CreatedAt.Add(time.Minute)
	successor.Finalized = false
	published := make(chan error, 1)
	early := false
	stopper.beforeForget = func() {
		go func() { published <- starter.publish(successor) }()
		select {
		case err := <-published:
			early = true
			published <- err
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err := stopper.forget(record); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if early {
		t.Error("a record was published inside another process's check-and-remove")
	}
	if err := <-published; err != nil {
		t.Fatalf("publish after the removal: %v", err)
	}
	if _, ok, _ := stopper.readInstance(record.Instance); ok {
		t.Error("the stopped instance is still recorded")
	}
	left, ok, _ := stopper.readRecord(record.Name)
	if !ok || left.Instance != successor.Instance {
		t.Fatalf("the name after the old session's removal: %+v ok=%v, want the successor", left, ok)
	}
	if _, err := os.Stat(successorDir); err != nil {
		t.Errorf("the successor's runtime directory was removed: %v", err)
	}
}

// A session whose write-back can neither be done nor kept blocks the next
// start of the project, of whatever name: that start would work on host files
// with changes still pending. The error names the command that retries.
func TestStartIsRefusedWhileAnEarlierSessionIsUnsettled(t *testing.T) {
	b, host, paths, record := startDetached(t)
	writeTestFile(t, record.Files[0].ResultPath, "guest edit\n")
	writeTestFile(t, paths["allow"], "host edit\n")
	keptRoot := filepath.Join(config.HostVMDir(b.home()), "kept")
	writeTestFile(t, keptRoot, "in the way")
	host.toolExits(0)
	vmsBefore := len(host.commandsOf(qemuBinary))

	// The same name again, through the whole start path.
	next := otherProcess(b, host)
	req, _ := sessionRequest(t, BaseDir(b.home(), "codex", "basekey"))
	req.Detached = true
	req.Session.Background = true
	req.Session.Name = record.Name
	_, err := next.Start(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "is not finalized") || !strings.Contains(err.Error(), "enclave stop --backend vm "+record.Name) {
		t.Fatalf("Start over an unsettled session = %v, want a refusal that names the stop command", err)
	}
	if !strings.Contains(err.Error(), "could not be kept") {
		t.Errorf("the refusal does not say what is unsettled: %v", err)
	}
	if got := len(host.commandsOf(qemuBinary)); got != vmsBefore {
		t.Error("a VM was started over an unsettled session")
	}
	if next.reservation != nil {
		t.Error("the refused start kept the disk")
	}
	unsettled, ok, _ := next.readInstance(record.Instance)
	if !ok || unsettled.Finalized || len(unsettled.Files) != 1 {
		t.Fatalf("the unsettled session after the refused start: %+v ok=%v", unsettled, ok)
	}
	if got := readTestFile(t, record.Files[0].ResultPath); got != "guest edit\n" {
		t.Fatalf("its staged guest version: %q", got)
	}
	if records, _ := next.listRecords(); len(records) != 1 {
		t.Errorf("records after the refused start: %+v", records)
	}

	// The named command retries; once it can keep the file, starts are free.
	ref := backend.SessionRef{Name: record.Name}
	if err := next.EndSession(context.Background(), ref, backend.StopOptions{Finalize: true}); err == nil || !strings.Contains(err.Error(), "could not be kept") {
		t.Fatalf("stop while nothing can be kept = %v", err)
	}
	if err := os.Remove(keptRoot); err != nil {
		t.Fatal(err)
	}
	if err := next.EndSession(context.Background(), ref, backend.StopOptions{Finalize: true}); err == nil || !strings.Contains(err.Error(), "is kept in ") {
		t.Fatalf("stop once the file can be kept = %v, want the report of where it is", err)
	}
	if err := next.AdmitSession(context.Background(), backend.SessionMeta{Tool: "codex", ProjectHash: "abc123def456"}); err != nil {
		t.Fatalf("start after the session was settled: %v", err)
	}
	next.releaseReservation()
}

func TestEndSessionStopsFinalizesAndForgets(t *testing.T) {
	b, host, paths, record := startDetached(t)
	writeTestFile(t, record.Files[0].ResultPath, "guest\n")
	other := otherProcess(b, host)
	if err := other.EndSession(context.Background(), backend.SessionRef{Name: record.Name}, backend.StopOptions{Finalize: true}); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if !host.guest.isOff() {
		t.Error("the VM still runs")
	}
	if got := readTestFile(t, paths["allow"]); got != "guest\n" {
		t.Errorf("file mount = %q", got)
	}
	if _, ok, _ := other.readRecord(record.Name); ok {
		t.Error("the session is still recorded")
	}
	if _, err := os.Stat(record.RuntimeDir); !os.IsNotExist(err) {
		t.Error("the runtime directory was left behind")
	}
}

// A process that is no longer seen running can hold its files a moment
// longer. The stop that just ended the VM waits for the disk instead of
// taking the hold for another session's.
func TestEndSessionWaitsForTheStoppedVMToLetGoOfTheDisk(t *testing.T) {
	b, host, paths, record := startDetached(t)
	host.guest.lingers = 50 * time.Millisecond
	writeTestFile(t, record.Files[0].ResultPath, "guest\n")
	other := otherProcess(b, host)
	if err := other.EndSession(context.Background(), backend.SessionRef{Name: record.Name}, backend.StopOptions{Finalize: true}); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if got := readTestFile(t, paths["allow"]); got != "guest\n" {
		t.Errorf("file mount = %q", got)
	}
	if _, ok, _ := other.readRecord(record.Name); ok {
		t.Error("the session is still recorded")
	}
}

// A write-back that is refused must not take the guest's edit with it: the
// runtime directory goes, the guest's version stays where the error says.
func TestRefusedWriteBackKeepsTheGuestVersion(t *testing.T) {
	b, host, paths, record := startDetached(t)
	writeTestFile(t, record.Files[0].ResultPath, "guest edit\n")
	writeTestFile(t, paths["allow"], "host edit\n")
	other := otherProcess(b, host)
	err := other.EndSession(context.Background(), backend.SessionRef{Name: record.Name}, backend.StopOptions{Finalize: true})
	if err == nil || !strings.Contains(err.Error(), "changed on the host") || !strings.Contains(err.Error(), "is kept in ") {
		t.Fatalf("EndSession = %v, want a report of the refused write-back and where the guest's version is", err)
	}
	if got := readTestFile(t, paths["allow"]); got != "host edit\n" {
		t.Errorf("host file = %q, want the host's edit", got)
	}
	kept := filepath.Join(config.HostVMDir(b.home()), "kept", record.Name+"-"+record.Instance, "01-"+filepath.Base(paths["allow"]))
	if !strings.Contains(err.Error(), kept) {
		t.Errorf("the error does not name %s: %v", kept, err)
	}
	if got := readTestFile(t, kept); got != "guest edit\n" {
		t.Errorf("kept guest version = %q", got)
	}
	if _, ok, _ := other.readRecord(record.Name); ok {
		t.Error("the settled session is still recorded")
	}
}

// If the guest's version cannot even be kept, the session stays as it is,
// staged files included, and a later stop tries again.
func TestUnsettledWriteBackKeepsTheSession(t *testing.T) {
	b, host, paths, record := startDetached(t)
	writeTestFile(t, record.Files[0].ResultPath, "guest edit\n")
	writeTestFile(t, paths["allow"], "host edit\n")
	// Nothing can be kept: a file sits where the directory would go.
	keptRoot := filepath.Join(config.HostVMDir(b.home()), "kept")
	writeTestFile(t, keptRoot, "in the way")
	other := otherProcess(b, host)
	ref := backend.SessionRef{Name: record.Name}
	err := other.EndSession(context.Background(), ref, backend.StopOptions{Finalize: true})
	if err == nil || !strings.Contains(err.Error(), "could not be kept") {
		t.Fatalf("EndSession = %v", err)
	}
	left, ok, _ := other.readRecord(record.Name)
	if !ok || left.Finalized || len(left.Files) != 1 {
		t.Fatalf("record after an unsettled write-back: %+v ok=%v", left, ok)
	}
	if got := readTestFile(t, record.Files[0].ResultPath); got != "guest edit\n" {
		t.Errorf("the staged guest version is gone: %q", got)
	}
	// With the obstacle gone, the next stop settles it.
	if err := os.Remove(keptRoot); err != nil {
		t.Fatal(err)
	}
	if err := other.EndSession(context.Background(), ref, backend.StopOptions{Finalize: true}); err == nil || !strings.Contains(err.Error(), "is kept in ") {
		t.Fatalf("second EndSession = %v", err)
	}
	if _, ok, _ := other.readRecord(record.Name); ok {
		t.Error("the session is still recorded after it was settled")
	}
}

func TestRemoveWithoutFinalizeKeepsWhatIsStillOwed(t *testing.T) {
	b, host, _, record := startDetached(t)
	other := otherProcess(b, host)
	ref := backend.SessionRef{Name: record.Name}
	// Running: removal is not a stop.
	if err := other.RemoveWithoutFinalize(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("removal of a running session = %v", err)
	}
	if host.guest.isOff() {
		t.Fatal("a removal stopped the VM")
	}
	// Ended by itself and not finalized: its runtime directory holds the
	// only copy of what the guest wrote.
	host.toolExits(0)
	if err := other.RemoveWithoutFinalize(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "not finalized") {
		t.Fatalf("removal of an unfinalized session = %v", err)
	}
	if _, ok, _ := other.readRecord(record.Name); !ok {
		t.Fatal("an unfinalized session was forgotten")
	}
	if _, err := os.Stat(filesOf(record.runtime()).out()); err != nil {
		t.Errorf("its staged file mounts are gone: %v", err)
	}
}

func TestEndSessionReportsAVMItCannotStop(t *testing.T) {
	b, host, _, record := startDetached(t)
	other := otherProcess(b, host)
	// The monitor takes commands and never answers, and the process ignores
	// the kill.
	host.guest.mu.Lock()
	host.guest.mute = true
	host.guest.mu.Unlock()
	host.unkillable = true
	previous := qmpIOTimeout
	qmpIOTimeout = 50 * time.Millisecond
	t.Cleanup(func() { qmpIOTimeout = previous })
	other.shutdownTimeout = 100 * time.Millisecond
	ref := backend.SessionRef{Name: record.Name}

	done := make(chan error, 1)
	go func() { done <- other.EndSession(context.Background(), ref, backend.StopOptions{Finalize: true}) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a monitor that does not answer hung the stop")
	}
	if err == nil || !strings.Contains(err.Error(), "could not be stopped") {
		t.Fatalf("EndSession = %v", err)
	}
	if len(host.killed) == 0 {
		t.Error("the stop never got to killing the VM")
	}
	if left, ok, _ := other.readRecord(record.Name); !ok || left.Finalized {
		t.Fatalf("a session whose VM still runs was finalized or forgotten: %+v ok=%v", left, ok)
	}
	if _, err := os.Stat(record.RuntimeDir); err != nil {
		t.Errorf("its runtime directory is gone: %v", err)
	}

	// A VM that dies on the kill is stopped, finalized and forgotten.
	host.unkillable = false
	if err := other.EndSession(context.Background(), ref, backend.StopOptions{Finalize: true}); err != nil {
		t.Fatalf("EndSession with a killable VM: %v", err)
	}
	if _, ok, _ := other.readRecord(record.Name); ok {
		t.Error("the session is still recorded")
	}
}

// The record is written before QEMU takes the disk: if the enclave process
// dies during the boot, the VM can still be listed and stopped.
func TestSessionIsRecordedBeforeItsVMStarts(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, _ := sessionRequest(t, baseDir)
	req.Detached = true
	req.Session.Background = true
	// The guest never reports ready, as far as this enclave process sees.
	host.bootStatus = ""
	b.bootTimeout = 50 * time.Millisecond
	type seen struct {
		sessions []backend.Session
		record   sessionRecord
	}
	observed := make(chan seen, 1)
	observer := otherProcess(b, host)
	go func() {
		for {
			sessions, _ := observer.List(context.Background(), backend.SessionFilter{})
			if len(sessions) == 1 {
				record, _, _ := observer.readRecord(sessions[0].Ref.Name)
				observed <- seen{sessions, record}
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	if _, err := b.Start(context.Background(), req); err == nil {
		t.Fatal("Start succeeded without a ready guest")
	}
	if !host.recordedAtStart {
		t.Error("QEMU was started before the session was recorded")
	}
	select {
	case during := <-observed:
		if during.sessions[0].Status != statusRunning || during.record.Started {
			t.Errorf("the booting session as another process saw it: %+v, started=%v", during.sessions, during.record.Started)
		}
	case <-time.After(5 * time.Second):
		t.Error("another process never saw the booting session")
	}
	// The failed start cleaned up after itself.
	if sessions, _ := b.List(context.Background(), backend.SessionFilter{All: true}); len(sessions) != 0 {
		t.Errorf("a failed start left sessions behind: %+v", sessions)
	}
	if !host.guest.isOff() {
		t.Error("a failed start left its VM running")
	}
}

// The owner's own teardown: the tool has ended, the guest neither shuts down
// nor answers, and the process survives the kill. The VM still holds the
// disk, so the session must stay findable and nothing of it may be finalized.
func TestForegroundTeardownKeepsASessionWhoseVMWillNotStop(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, paths := sessionRequest(t, baseDir)
	host.staysUp, host.mute, host.unkillable = true, true, true
	previous := qmpIOTimeout
	qmpIOTimeout = 20 * time.Millisecond
	t.Cleanup(func() { qmpIOTimeout = previous })
	b.shutdownTimeout = 50 * time.Millisecond
	host.duringSession = func(rt runtimeFiles) {
		writeTestFile(t, filepath.Join(rt.out(), "files", "01", "result"), "guest\n")
	}
	status, err := b.Run(context.Background(), req, backend.AttachIO{})
	if err == nil || !strings.Contains(err.Error(), "could not be stopped") || status.Code == 0 {
		t.Fatalf("Run = %+v, %v; want the unstoppable VM reported", status, err)
	}
	if len(host.killed) == 0 {
		t.Error("the teardown never tried to kill the VM")
	}
	record, ok, _ := b.readRecord(req.Session.Name)
	if !ok || record.Finalized {
		t.Fatalf("record after a teardown that could not stop the VM: %+v ok=%v", record, ok)
	}
	if _, err := os.Stat(record.RuntimeDir); err != nil {
		t.Errorf("its runtime directory is gone: %v", err)
	}
	if got := readTestFile(t, paths["allow"]); got != "host\n" {
		t.Errorf("file mount = %q; it was written back under a guest that still runs", got)
	}
	other := otherProcess(b, host)
	if sessions, _ := other.List(context.Background(), backend.SessionFilter{}); len(sessions) != 1 || sessions[0].Status != statusRunning {
		t.Errorf("ps after the failed teardown = %+v, want the session still running", sessions)
	}
	if err := other.AdmitSession(context.Background(), backend.SessionMeta{Tool: "codex", ProjectHash: "abc123def456"}); err == nil {
		t.Error("the disk of a VM that still runs was given to another session")
	}
	// Once the VM can be killed, `stop` ends the session and writes back.
	host.unkillable = false
	if err := other.EndSession(context.Background(), backend.SessionRef{Name: req.Session.Name}, backend.StopOptions{Finalize: true}); err != nil {
		t.Fatalf("EndSession afterwards: %v", err)
	}
	if got := readTestFile(t, paths["allow"]); got != "guest\n" {
		t.Errorf("file mount after the later stop = %q", got)
	}
}

// The owner's teardown with a runtime directory that is no longer the
// session's and a monitor that is open but silent: the PID file in that
// directory is not read, so nothing is killed, and since the open connection
// says the VM runs, the session is kept and reported.
func TestForegroundTeardownDoesNotKillByAPIDFromALostRuntimeDirectory(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, paths := sessionRequest(t, baseDir)
	host.staysUp, host.mute = true, true
	previous := qmpIOTimeout
	qmpIOTimeout = 20 * time.Millisecond
	t.Cleanup(func() { qmpIOTimeout = previous })
	b.shutdownTimeout = 50 * time.Millisecond
	host.duringSession = func(rt runtimeFiles) {
		writeTestFile(t, filepath.Join(rt.out(), "files", "01", "result"), "planted\n")
		if err := os.Chmod(rt.dir, 0o755); err != nil { // #nosec G302 -- the test plays a directory that is not private.
			t.Error(err)
		}
	}
	status, err := b.Run(context.Background(), req, backend.AttachIO{})
	if err == nil || !strings.Contains(err.Error(), "could not be stopped") || status.Code == 0 {
		t.Fatalf("Run = %+v, %v; want the VM that could not be stopped reported", status, err)
	}
	if len(host.killed) != 0 || host.guest.isOff() {
		t.Errorf("killed = %v, off = %v; a PID from a directory that is not the session's was used", host.killed, host.guest.isOff())
	}
	if record, ok, _ := b.readRecord(req.Session.Name); !ok || record.Finalized {
		t.Fatalf("record after a teardown that could not stop the VM: %+v ok=%v", record, ok)
	}
	if got := readTestFile(t, paths["allow"]); got != "host\n" {
		t.Errorf("file mount = %q; it was written back under a guest that still runs", got)
	}
}

// The same for a start that fails: its cleanup must not forget a VM it
// could not stop.
func TestFailedStartKeepsASessionWhoseVMWillNotStop(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	req, _ := sessionRequest(t, baseDir)
	req.Detached = true
	req.Session.Background = true
	host.bootStatus = "failed: cannot mount /home/agent/.codex (virtiofs)"
	host.mute, host.unkillable = true, true
	previous := qmpIOTimeout
	qmpIOTimeout = 20 * time.Millisecond
	t.Cleanup(func() { qmpIOTimeout = previous })
	b.shutdownTimeout = 50 * time.Millisecond
	_, err := b.Start(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "cannot mount") || !strings.Contains(err.Error(), "could not be stopped") {
		t.Fatalf("Start = %v; want the guest's reason and the VM that could not be stopped", err)
	}
	record, ok, _ := b.readRecord(req.Session.Name)
	if !ok || record.Started {
		t.Fatalf("record after the failed start: %+v ok=%v", record, ok)
	}
	if _, err := os.Stat(record.RuntimeDir); err != nil {
		t.Errorf("its runtime directory is gone: %v", err)
	}
	other := otherProcess(b, host)
	if sessions, _ := other.List(context.Background(), backend.SessionFilter{}); len(sessions) != 1 {
		t.Errorf("ps after the failed start = %+v, want the VM that still runs", sessions)
	}
	host.unkillable = false
	if err := other.EndSession(context.Background(), backend.SessionRef{Name: req.Session.Name}, backend.StopOptions{Finalize: true}); err != nil {
		t.Fatalf("EndSession afterwards: %v", err)
	}
	if records, _ := other.listRecords(); len(records) != 0 {
		t.Errorf("records after the later stop: %+v", records)
	}
}

// `enclave stop` selects sessions from a listing and stops them one by one.
// A selected session can end and its name pass to a new one before its turn;
// the reference from the listing still means the session that was listed.
func TestEndSessionOfAListedSessionSparesItsSuccessor(t *testing.T) {
	b, host, _, record := startDetached(t)
	lister := otherProcess(b, host)
	listed, err := lister.List(context.Background(), backend.SessionFilter{})
	if err != nil || len(listed) != 1 || listed[0].Ref.ID != record.Instance {
		t.Fatalf("List = %+v, %v; want the session with its instance as ID", listed, err)
	}
	stale := listed[0].Ref

	// The listed session ends by itself and is settled; a new one of the
	// same name starts.
	host.toolExits(0)
	settler := otherProcess(b, host)
	if err := settler.EndSession(context.Background(), backend.SessionRef{Name: record.Name}, backend.StopOptions{Finalize: true}); err != nil {
		t.Fatal(err)
	}
	successorDir := shortTempDir(t)
	successor := record
	successor.Instance = filepath.Base(successorDir)
	successor.RuntimeDir = successorDir
	successor.Finalized = false
	if err := settler.publish(successor); err != nil {
		t.Fatal(err)
	}
	stopped := false
	lister.vmProcess = func(rt runtimeFiles) (int, bool) { return 4245, rt.dir == successorDir && !stopped }
	lister.killProcess = func(int) { stopped = true }

	if err := lister.EndSession(context.Background(), stale, backend.StopOptions{Finalize: true}); err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Fatalf("EndSession of the listed session = %v, want it reported gone", err)
	}
	if stopped {
		t.Fatal("the successor was stopped in place of the listed session")
	}
	if left, ok, _ := lister.readInstance(successor.Instance); !ok || left.Finalized {
		t.Fatalf("the successor's record: %+v ok=%v", left, ok)
	}
	// By name alone, the name means what it stands for now.
	byName, err := lister.Inspect(context.Background(), backend.SessionRef{Name: record.Name})
	if err != nil || byName.Ref.ID != successor.Instance {
		t.Fatalf("Inspect by name = %+v, %v; want the successor", byName, err)
	}
	// An ID that belongs to another name is not a match.
	if _, err := lister.Inspect(context.Background(), backend.SessionRef{Name: "enclave-codex-other", ID: successor.Instance}); err == nil {
		t.Error("an instance was accepted under another session's name")
	}
}

// A record that cannot be read may be the one that still owes a
// finalization. A start must not go ahead on a listing that skipped it; a
// listing for `ps` shows what it can.
func TestStartIsRefusedWhenARecordCannotBeRead(t *testing.T) {
	meta := backend.SessionMeta{Tool: "codex", ProjectHash: "abc123def456"}
	pending := sessionRecord{Instance: "pending-instance", Name: "enclave-codex-abc123def456-1", Tool: "codex", ProjectHash: "abc123def456", Background: true}
	cases := map[string]func(t *testing.T, path string){
		"unreadable": func(t *testing.T, path string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads any file")
			}
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		},
		"corrupt": func(t *testing.T, path string) {
			writeTestFile(t, path, `{"instance": "pending-instance", "finalized": fal`)
		},
		"under another instance's name": func(t *testing.T, path string) {
			if err := os.Rename(path, filepath.Join(filepath.Dir(path), "renamed.json")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			b, _, _ := newTestBackend(t)
			if err := b.writeRecord(pending); err != nil {
				t.Fatal(err)
			}
			readable := sessionRecord{Instance: "readable-instance", Name: "enclave-codex-fff000fff000-1", Tool: "codex", ProjectHash: "fff000fff000", Background: true, Finalized: true}
			if err := b.writeRecord(readable); err != nil {
				t.Fatal(err)
			}
			path, _ := b.recordPath(pending.Instance)
			damage(t, path)

			err := b.AdmitSession(context.Background(), meta)
			if err == nil || !strings.Contains(err.Error(), "cannot tell whether an earlier session") {
				t.Fatalf("AdmitSession with a record it cannot read = %v", err)
			}
			if b.reservation != nil {
				t.Error("the refused start kept the disk")
			}
			// A stop of this project reports the same instead of calling the
			// project settled.
			if err := b.finalizeStopped(readableAs(readable, meta)); err == nil {
				t.Error("finalization passed over a record it could not read")
			}
			sessions, err := b.List(context.Background(), backend.SessionFilter{All: true})
			if err != nil || len(sessions) != 1 || sessions[0].Ref.ID != readable.Instance {
				t.Errorf("ps = %+v, %v; want the readable session", sessions, err)
			}
		})
	}
}

// readableAs returns a record with the project and tool of meta.
func readableAs(record sessionRecord, meta backend.SessionMeta) sessionRecord {
	record.ProjectHash, record.Tool = meta.ProjectHash, meta.Tool
	return record
}

func TestListFilters(t *testing.T) {
	yes, no := true, false
	running := backend.Session{Ref: backend.SessionRef{Name: "enclave-codex-abc-1"}, Tool: "codex", ProjectHash: "abc", Status: statusRunning, Background: true}
	exited := running
	exited.Status = statusExited
	cases := []struct {
		name    string
		session backend.Session
		filter  backend.SessionFilter
		want    bool
	}{
		{"running by default", running, backend.SessionFilter{}, true},
		{"exited hidden by default", exited, backend.SessionFilter{}, false},
		{"exited with all", exited, backend.SessionFilter{All: true}, true},
		{"running only beats all", exited, backend.SessionFilter{All: true, RunningOnly: true}, false},
		{"tool", running, backend.SessionFilter{Tool: "claude"}, false},
		{"project", running, backend.SessionFilter{ProjectHash: "def"}, false},
		{"background", running, backend.SessionFilter{Background: &yes}, true},
		{"foreground only", running, backend.SessionFilter{Background: &no}, false},
		{"name prefix", running, backend.SessionFilter{NamePrefix: "enclave-codex-abc"}, true},
		{"other prefix", running, backend.SessionFilter{NamePrefix: "enclave-claude"}, false},
		{"exact name", running, backend.SessionFilter{ExactName: "enclave-codex-abc"}, false},
	}
	for _, tc := range cases {
		if got := matchesFilter(tc.session, tc.filter); got != tc.want {
			t.Errorf("%s: matchesFilter = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRecordNamesStayInTheSessionsDirectory(t *testing.T) {
	b, _, _ := newTestBackend(t)
	for _, name := range []string{"", "../escape", "a/b", ".hidden"} {
		if _, err := b.recordPath(name); err == nil {
			t.Errorf("record path for %q was accepted", name)
		}
	}
	// Listing commands construct the backend without a home.
	t.Setenv("HOME", b.opts.Host.Home)
	listing := New(Options{})
	if sessions, err := listing.List(context.Background(), backend.SessionFilter{All: true}); err != nil || len(sessions) != 0 {
		t.Fatalf("List without a configured home = %+v, %v", sessions, err)
	}
	if listing.home() != b.opts.Host.Home {
		t.Errorf("home resolved to %q, want %q", listing.home(), b.opts.Host.Home)
	}
}

// useSocketTempDirs replaces where socket directories are created.
func useSocketTempDirs(t *testing.T, dirs ...string) {
	t.Helper()
	saved := socketTempDirs
	socketTempDirs = func() []string { return dirs }
	t.Cleanup(func() { socketTempDirs = saved })
}

func TestRuntimeDirectoryKeepsItsSocketsWhereTheyFitAnAddress(t *testing.T) {
	deep := filepath.Join(shortTempDir(t), strings.Repeat("d", 90))
	short := shortTempDir(t)
	useSocketTempDirs(t, deep, short)
	rt, err := newRuntimeDir(deep)
	if err != nil {
		t.Fatalf("newRuntimeDir(%d bytes): %v", len(deep), err)
	}
	if filepath.Dir(rt.name()) != deep || filepath.Dir(filesOf(rt).pidFile()) != rt.name() || filepath.Dir(filesOf(rt).out()) != rt.name() {
		t.Errorf("the runtime directory moved: %+v", rt)
	}
	parent := filepath.Join(short, "enclave-vm-sockets-"+strconv.Itoa(os.Getuid()))
	for _, socket := range []string{filesOf(rt).qmp(), filesOf(rt).control(), filesOf(rt).socket(999)} {
		if filepath.Dir(filepath.Dir(socket)) != parent || len(socket) > socketPathLimit {
			t.Errorf("socket %s (%d bytes) is not in a directory under %s", socket, len(socket), parent)
		}
	}
	if filepath.Dir(filesOf(rt).exportPID(3)) != rt.name() {
		t.Errorf("export PID file %s is not in the runtime directory", filesOf(rt).exportPID(3))
	}
	for _, dir := range []string{parent, rt.socketsName()} {
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v; want it private", dir, info, err)
		}
	}
	if entries, _ := os.ReadDir(deep); len(entries) != 1 {
		t.Errorf("a refused socket directory was left behind: %v", entries)
	}
	rt.remove()
	for _, dir := range []string{rt.name(), rt.socketsName()} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", dir)
		}
	}

	plain, err := newRuntimeDir(short)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.remove()
	if plain.socketsName() != "" || filepath.Dir(filesOf(plain).qmp()) != plain.name() {
		t.Errorf("a runtime directory that fits got a socket directory: %+v", plain)
	}
}

func TestRuntimeDirectoryNeedsSomewhereForItsSockets(t *testing.T) {
	deep := filepath.Join(shortTempDir(t), strings.Repeat("d", 90))
	useSocketTempDirs(t, deep)
	if rt, err := newRuntimeDir(deep); err == nil || !strings.Contains(err.Error(), "too deep for a unix socket") {
		t.Fatalf("newRuntimeDir(%d bytes) = %v, %v", len(deep), rt, err)
	}
	if entries, _ := os.ReadDir(deep); len(entries) != 0 {
		t.Errorf("a refused runtime directory was left behind: %v", entries)
	}
}

// A home directory can be too deep for socket addresses. The detached
// session's files stay under it; only the sockets go elsewhere, and the
// session is stopped and forgotten like any other.
func TestDetachedSessionUnderADeepHome(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	b.opts.Host.Home = filepath.Join(b.opts.Host.Home, strings.Repeat("d", 90))
	useSocketTempDirs(t, shortTempDir(t))
	paths, record := startDetachedOn(t, b, baseDir)

	if !strings.HasPrefix(record.RuntimeDir, b.opts.Host.Home) {
		t.Errorf("runtime directory %s is not under the home", record.RuntimeDir)
	}
	if record.SocketDir == "" || !socketExists(filesOf(record.runtime()).control()) {
		t.Fatalf("no control socket in a socket directory: %+v", record.runtime())
	}
	writeTestFile(t, record.Files[0].ResultPath, "guest\n")
	other := otherProcess(b, host)
	if sessions, err := other.List(context.Background(), backend.SessionFilter{}); err != nil || len(sessions) != 1 {
		t.Fatalf("List = %+v, %v; want the running session", sessions, err)
	}
	if err := other.EndSession(context.Background(), backend.SessionRef{Name: record.Name}, backend.StopOptions{Finalize: true}); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if got := host.guest.received(); !slices.Contains(got, "system_powerdown") {
		t.Errorf("the VM was not asked to shut down over its control socket: %v", got)
	}
	if got := readTestFile(t, paths["allow"]); got != "guest\n" {
		t.Errorf("file mount = %q", got)
	}
	for _, dir := range []string{record.RuntimeDir, record.SocketDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", dir)
		}
	}
}

// A socket directory under the temp directory can be removed while the VM
// runs. The session is then stopped through its process, and still settled.
func TestEndSessionWithoutTheSocketDirectory(t *testing.T) {
	b, host, baseDir := newTestBackend(t)
	b.opts.Host.Home = filepath.Join(b.opts.Host.Home, strings.Repeat("d", 90))
	useSocketTempDirs(t, shortTempDir(t))
	paths, record := startDetachedOn(t, b, baseDir)
	writeTestFile(t, record.Files[0].ResultPath, "guest\n")
	if err := os.RemoveAll(record.SocketDir); err != nil {
		t.Fatal(err)
	}
	other := otherProcess(b, host)
	if err := other.EndSession(context.Background(), backend.SessionRef{Name: record.Name}, backend.StopOptions{Finalize: true}); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if len(host.killed) != 1 || !host.guest.isOff() || slices.Contains(host.guest.received(), "system_powerdown") {
		t.Errorf("killed = %v, off = %v, commands = %v; want the VM stopped through its process", host.killed, host.guest.isOff(), host.guest.received())
	}
	if got := readTestFile(t, paths["allow"]); got != "guest\n" {
		t.Errorf("file mount = %q", got)
	}
	if _, ok, _ := other.readRecord(record.Name); ok {
		t.Error("the session is still recorded")
	}
}

// The path of a lost socket directory can be taken by someone else. What is
// there is then neither trusted with the control socket nor removed: not the
// directory itself when others can enter it, and nothing below a parent that
// is no longer private.
func TestEndSessionLeavesASocketDirectoryThatIsNotItsOwn(t *testing.T) {
	for name, open := range map[string]func(sessionRecord) string{
		"the directory": func(record sessionRecord) string { return record.SocketDir },
		"its parent":    func(record sessionRecord) string { return filepath.Dir(record.SocketDir) },
	} {
		t.Run(name, func(t *testing.T) {
			b, host, baseDir := newTestBackend(t)
			b.opts.Host.Home = filepath.Join(b.opts.Host.Home, strings.Repeat("d", 90))
			useSocketTempDirs(t, shortTempDir(t))
			_, record := startDetachedOn(t, b, baseDir)
			foreign := filepath.Join(record.SocketDir, "not-the-session's")
			writeTestFile(t, foreign, "keep\n")
			if err := os.Chmod(open(record), 0o755); err != nil { // #nosec G302 -- the test opens the directory up on purpose.
				t.Fatal(err)
			}
			other := otherProcess(b, host)
			if err := other.EndSession(context.Background(), backend.SessionRef{Name: record.Name}, backend.StopOptions{Finalize: true}); err != nil {
				t.Fatalf("EndSession: %v", err)
			}
			if len(host.killed) != 1 || slices.Contains(host.guest.received(), "system_powerdown") {
				t.Errorf("killed = %v, commands = %v; want the VM stopped through its process, not the socket", host.killed, host.guest.received())
			}
			if got := readTestFile(t, foreign); got != "keep\n" {
				t.Errorf("a directory that is not the session's was emptied")
			}
			if _, err := os.Stat(record.RuntimeDir); !os.IsNotExist(err) {
				t.Error("the runtime directory was left behind")
			}
		})
	}
}

// A parent that someone else created under the name is not used.
func TestSocketDirectoryNeedsAParentOfItsOwn(t *testing.T) {
	taken, short := shortTempDir(t), shortTempDir(t)
	// "relative" below is relative to here, not to the source tree.
	t.Chdir(shortTempDir(t))
	squatted := filepath.Join(taken, "enclave-vm-sockets-"+strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(squatted, 0o777); err != nil { // #nosec G301 -- the test plays a directory others can write.
		t.Fatal(err)
	}
	if err := os.Chmod(squatted, 0o777); err != nil { // #nosec G302 -- as above, past the umask.
		t.Fatal(err)
	}
	useSocketTempDirs(t, "relative", taken, short)
	deep := filepath.Join(shortTempDir(t), strings.Repeat("d", 90))
	rt, err := newRuntimeDir(deep)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.remove()
	if !strings.HasPrefix(rt.socketsName(), short+string(filepath.Separator)) {
		t.Errorf("socket directory %s, want one under %s", rt.socketsName(), short)
	}
	if entries, _ := os.ReadDir(squatted); len(entries) != 0 {
		t.Errorf("a directory others can write was used: %v", entries)
	}
	if _, err := os.Stat("relative"); !os.IsNotExist(err) {
		t.Error("a relative temp directory was used")
	}

	// A leftover under the session's name in the trusted parent is replaced.
	stale := filepath.Join(rt.socketsName(), "stale")
	writeTestFile(t, stale, "old\n")
	again, err := newSocketDir(rt.name())
	if err != nil || again != rt.socketsName() {
		t.Fatalf("newSocketDir again = %q, %v; want %q", again, err, rt.socketsName())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a leftover socket directory was reused with its content")
	}
}

// The session record names the runtime directory for commands that run in
// other directories.
func TestRuntimeDirectoryIsAbsolute(t *testing.T) {
	t.Chdir(shortTempDir(t))
	if err := os.Mkdir("tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", "tmp")
	rt, err := newRuntimeDir("")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.remove()
	if !filepath.IsAbs(rt.name()) {
		t.Errorf("runtime directory %q is relative", rt.name())
	}
}

// After a reboot or a temp cleaner, the path a session record names for its
// runtime directory can hold someone else's directory. Nothing in it is
// taken for the session's: not a result to write back to a host file, not a
// process to signal, not a VM to attach to, and it is not removed.
func TestARuntimeDirectoryThatIsNotTheSessionsIsLeftAlone(t *testing.T) {
	b, host, paths, record := startDetached(t)
	host.guest.powerOff()
	before := readTestFile(t, paths["allow"])
	writeTestFile(t, record.Files[0].ResultPath, "planted\n")
	if err := os.Chmod(record.RuntimeDir, 0o755); err != nil { // #nosec G302 -- the test plays a directory that is not private.
		t.Fatal(err)
	}
	other := otherProcess(b, host)
	ref := backend.SessionRef{Name: record.Name}
	if session, err := other.Inspect(context.Background(), ref); err != nil || session.Status == statusRunning {
		t.Fatalf("Inspect = %+v, %v; want a session that does not run", session, err)
	}
	if err := other.Attach(context.Background(), ref, backend.AttachIO{}); err == nil {
		t.Error("Attach used the identity files of a directory that is not the session's")
	}
	err := other.EndSession(context.Background(), ref, backend.StopOptions{Finalize: true})
	if err == nil || !strings.Contains(err.Error(), "no longer this session's") {
		t.Fatalf("EndSession = %v, want the lost runtime directory reported", err)
	}
	if got := readTestFile(t, paths["allow"]); got != before {
		t.Errorf("host file = %q, want it unchanged (%q)", got, before)
	}
	if got := readTestFile(t, record.Files[0].ResultPath); got != "planted\n" {
		t.Errorf("the directory was changed: %q", got)
	}
	if _, ok, _ := other.readRecord(record.Name); ok {
		t.Error("the session is still recorded")
	}
}

// A stop from another process cannot find a VM whose runtime directory is
// lost. If the VM still holds the disk, the session stays and the error says
// what is known.
func TestEndSessionOfALiveVMWithoutItsRuntimeDirectory(t *testing.T) {
	b, host, _, record := startDetached(t)
	if err := os.Chmod(record.RuntimeDir, 0o755); err != nil { // #nosec G302 -- the test plays a directory that is not private.
		t.Fatal(err)
	}
	other := otherProcess(b, host)
	other.shutdownTimeout = 40 * time.Millisecond
	err := other.EndSession(context.Background(), backend.SessionRef{Name: record.Name}, backend.StopOptions{Finalize: true})
	if err == nil || !strings.Contains(err.Error(), "its VM cannot be found") {
		t.Fatalf("EndSession = %v, want the held disk and the lost runtime directory reported", err)
	}
	if _, ok, _ := other.readRecord(record.Name); !ok {
		t.Error("the session was forgotten while its VM holds the disk")
	}
	if host.guest.isOff() || len(host.killed) != 0 {
		t.Errorf("off = %v, killed = %v; nothing was to be stopped by a PID from that directory", host.guest.isOff(), host.killed)
	}
}
