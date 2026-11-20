// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"enclave/internal/backend"
	backendvm "enclave/internal/backend/vm"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

// endingBackend ends sessions in one operation and records how it is asked.
type endingBackend struct {
	*stopTestBackend
	ended  []string
	refs   []backend.SessionRef
	endErr map[string]error
}

func (e *endingBackend) EndSession(_ context.Context, ref backend.SessionRef, opts backend.StopOptions) error {
	if !opts.Finalize {
		return errors.New("EndSession without finalization")
	}
	e.ended = append(e.ended, ref.Name)
	e.refs = append(e.refs, ref)
	return e.endErr[ref.Name]
}

// A backend that ends a session in one operation is asked for exactly that:
// a stop and a removal by name could act on two different sessions.
func TestStopContainerEndsTheSessionInOneOperation(t *testing.T) {
	be := &endingBackend{stopTestBackend: &stopTestBackend{}}
	if !stopContainer(be, backend.SessionRef{Name: "enclave-codex-abc123def456-1", ID: "instance-1"}) {
		t.Fatal("stopContainer reported a failure")
	}
	// The reference arrives whole: the ID is what tells the selected session
	// from a later one of its name.
	if len(be.refs) != 1 || be.refs[0].ID != "instance-1" {
		t.Fatalf("EndSession got %+v, want the selected session's ID", be.refs)
	}
	if len(be.ended) != 1 || be.stopCalled || be.forceRemoveCalled || be.strictRemoveCalled {
		t.Fatalf("EndSession calls = %v, Stop=%v RemoveWithoutFinalize=%v Remove=%v; want one EndSession and nothing else",
			be.ended, be.stopCalled, be.forceRemoveCalled, be.strictRemoveCalled)
	}
}

// A session that could not be stopped or finalized is still there, and
// `enclave stop` must not exit as if it were done. For the container engines
// a failed stop stays a warning, as before.
func TestStopExitCodeFollowsAFailedSessionEnd(t *testing.T) {
	projectDir := t.TempDir()
	project, err := config.ResolveProjectFromDir(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	first := session("enclave-codex-"+project.Hash+"-1", "1", project.Hash, projectDir)
	second := session("enclave-codex-"+project.Hash+"-2", "2", project.Hash, projectDir)
	failure := errors.New("the VM of the session could not be stopped")
	all := model.Options{Sources: model.DefaultOptionSources()}

	ender := func(failing ...string) *endingBackend {
		be := &endingBackend{stopTestBackend: &stopTestBackend{sessions: []backend.Session{first, second}}, endErr: map[string]error{}}
		for _, name := range failing {
			be.endErr[name] = failure
		}
		return be
	}
	if code := stopSessions(context.Background(), ender(), all, projectDir); code != 0 {
		t.Errorf("stop of two sessions that end = %d", code)
	}
	// One failure fails the command, and the other session is still ended.
	be := ender(first.Ref.Name)
	if code := stopSessions(context.Background(), be, all, projectDir); code != 1 || len(be.ended) != 2 {
		t.Errorf("stop with one failing session = %d after ending %v; want 1 after both", code, be.ended)
	}
	if code := stopSessions(context.Background(), ender(first.Ref.Name), stopOptionsWithArg(first.Ref.Name), projectDir); code != 1 {
		t.Errorf("stop of a named session that fails = %d", code)
	}
	if code := stopSessions(context.Background(), ender(first.Ref.Name), stopOptionsWithArg(second.Ref.Name), projectDir); code != 0 {
		t.Errorf("stop of the other named session = %d", code)
	}

	// Every stop carries the listed reference, ID included, so that it ends
	// the listed session and not a later one of its name.
	first.Ref.ID, second.Ref.ID = "instance-a", "instance-b"
	listed := ender()
	if code := stopSessions(context.Background(), listed, all, projectDir); code != 0 {
		t.Fatalf("stop = %d", code)
	}
	if len(listed.refs) != 2 || listed.refs[0].ID != "instance-a" || listed.refs[1].ID != "instance-b" {
		t.Errorf("EndSession got %+v, want the listed IDs", listed.refs)
	}
	named := ender()
	if code := stopSessions(context.Background(), named, stopOptionsWithArg(second.Ref.Name), projectDir); code != 0 || len(named.refs) != 1 || named.refs[0].ID != "instance-b" {
		t.Errorf("named stop = %d with %+v, want the selected session's ID", code, named.refs)
	}

	engine := &stopTestBackend{sessions: []backend.Session{first}, stopErr: failure}
	if code := stopSessions(context.Background(), engine, all, projectDir); code != 0 || !engine.forceRemoveCalled {
		t.Errorf("a container engine's failed stop = %d, removed=%v; its behaviour must not change", code, engine.forceRemoveCalled)
	}
}

// vmSessionOnDisk writes what a vm session whose VM is gone leaves behind: a
// record, and a runtime directory with the guest's version of one writable
// file mount. It returns the host file, the record path and the runtime
// directory.
func vmSessionOnDisk(t *testing.T, home string, name string, background bool) (hostFile string, recordPath string, runtimeDir string) {
	t.Helper()
	hostFile = filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(hostFile, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(hostFile)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	runtimeDir = filepath.Join(home, "run", name)
	staged := filepath.Join(runtimeDir, "out", "files", "00", "result")
	if err := os.MkdirAll(filepath.Dir(staged), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("guest edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("original"))
	record := map[string]any{
		"instance": "instance-1", "name": name, "tool": "codex", "projectHash": "abc123def456",
		"background": background, "started": true, "runtimeDir": runtimeDir,
		"files": []map[string]any{{
			"target": "/home/agent/settings.json", "resultPath": staged, "path": hostFile,
			"id": fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), "sum": hex.EncodeToString(sum[:]), "mode": 0o600,
		}},
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	recordPath = filepath.Join(config.HostVMDir(home), "sessions", "instance-1.json")
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return hostFile, recordPath, runtimeDir
}

// `enclave stop` through the real vm backend, on a detached session whose
// tool ended by itself: its file mount is written back and it is forgotten.
func TestStopContainerFinalizesAnEndedVMSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	name := "enclave-codex-abc123def456-1"
	hostFile, recordPath, runtimeDir := vmSessionOnDisk(t, home, name, true)
	be := backendvm.New(backendvm.Options{Host: model.Host{Home: home}})

	stopContainer(be, backend.SessionRef{Name: name})

	if got, err := os.ReadFile(hostFile); err != nil || string(got) != "guest edit" {
		t.Errorf("host file after stop = %q, %v; want the guest's edit", got, err)
	}
	for _, path := range []string{recordPath, runtimeDir} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived the stop", path)
		}
	}
}

// The same stop while the session's own enclave process still holds the disk:
// that process owes the write-back, and the stop must leave it the record and
// the staged file.
func TestStopContainerLeavesASessionItsOwnerIsFinalizing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	name := "enclave-codex-abc123def456"
	hostFile, recordPath, runtimeDir := vmSessionOnDisk(t, home, name, false)
	// The owner: a foreground run between its VM's exit and its finalization.
	lock, ok, err := util.TryLockFile(filepath.Join(config.HostProjectVMDir(home, "abc123def456", "codex"), "disk.lock"))
	if err != nil || !ok {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		stopContainer(backendvm.New(backendvm.Options{Host: model.Host{Home: home}}), backend.SessionRef{Name: name})
	}()
	// Long enough for the stop to have done whatever it does to a session it
	// cannot finalize itself.
	time.Sleep(300 * time.Millisecond)
	// The owner finishes: it marks the session finalized, as its own
	// finalization does, and the stop then has nothing left to wait for.
	staged := filepath.Join(runtimeDir, "out", "files", "00", "result")
	if got, err := os.ReadFile(staged); err != nil || string(got) != "guest edit" {
		t.Fatalf("the owner's staged file before it finalized: %q, %v", got, err)
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("the record was removed under its owner: %v", err)
	}
	finalized := strings.Replace(string(data), `"started":true`, `"started":true,"finalized":true`, 1)
	if err := util.WriteFileAtomic(recordPath, []byte(finalized), 0o600); err != nil {
		t.Fatal(err)
	}
	<-finished
	_ = lock.Close()
	if got, err := os.ReadFile(hostFile); err != nil || string(got) != "original" {
		t.Errorf("host file = %q, %v; the stop wrote back a file its owner owed", got, err)
	}
}

// The exit code through the real vm backend: a write-back that can neither be
// done nor kept fails the stop and leaves the session; once the guest's
// version can be kept, the stop still reports the refused write-back, and the
// session is gone.
func TestStopExitCodeForAnUnsettledVMSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	name := "enclave-codex-abc123def456-1"
	hostFile, recordPath, runtimeDir := vmSessionOnDisk(t, home, name, true)
	if err := os.WriteFile(hostFile, []byte("host edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	keptRoot := filepath.Join(config.HostVMDir(home), "kept")
	if err := os.WriteFile(keptRoot, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := backendvm.New(backendvm.Options{Host: model.Host{Home: home}})
	opts := stopOptionsWithArg(name)

	if code := stopSessions(context.Background(), be, opts, t.TempDir()); code != 1 {
		t.Fatalf("stop of a session that cannot be settled = %d, want 1", code)
	}
	for _, path := range []string{recordPath, filepath.Join(runtimeDir, "out", "files", "00", "result")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the unsettled session lost %s: %v", path, err)
		}
	}
	if err := os.Remove(keptRoot); err != nil {
		t.Fatal(err)
	}
	if code := stopSessions(context.Background(), be, opts, t.TempDir()); code != 1 {
		t.Fatalf("stop that kept the guest's version instead of writing it back = %d, want 1", code)
	}
	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		t.Error("the settled session is still recorded")
	}
	kept, _ := filepath.Glob(filepath.Join(keptRoot, "*", "*"))
	if len(kept) != 1 {
		t.Fatalf("kept files = %v", kept)
	}
	if got, _ := os.ReadFile(kept[0]); string(got) != "guest edit" {
		t.Errorf("kept guest version = %q", got)
	}
	if got, _ := os.ReadFile(hostFile); string(got) != "host edit" {
		t.Errorf("host file = %q", got)
	}
}
