// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const lockHelperEnv = "PEERGROUP_TEST_LOCK_HELPER"

// TestMain doubles as the lock-holding child process of TestGroupLockAcrossProcesses.
func TestMain(m *testing.M) {
	if path := os.Getenv(lockHelperEnv); path != "" {
		os.Exit(runLockHelper(path))
	}
	os.Exit(m.Run())
}

func runLockHelper(path string) int {
	release, err := acquireLock(context.Background(), path)
	if err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	release()
	return 0
}

func newTestStore(t *testing.T) (GroupStore, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	return NewGroupStore(state, filepath.Join(state, "locks")), state
}

func TestNewGenerationAndNames(t *testing.T) {
	seen := map[Generation]bool{}
	for range 100 {
		g, err := NewGeneration()
		if err != nil {
			t.Fatal(err)
		}
		if !g.Valid() {
			t.Fatalf("generation %q is not 16 lowercase hex", g)
		}
		if seen[g] {
			t.Fatalf("duplicate generation %q", g)
		}
		seen[g] = true
	}
	for _, name := range []string{"review", "a", "0-team", strings.Repeat("a", 32)} {
		if err := ValidateGroupName(name); err != nil {
			t.Errorf("ValidateGroupName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "-x", "Review", "a_b", "a/b", "..", strings.Repeat("a", 33), "_host"} {
		if err := ValidateGroupName(name); !errors.Is(err, ErrInvalidGroup) {
			t.Errorf("ValidateGroupName(%q) = %v, want ErrInvalidGroup", name, err)
		}
	}
	ts := FormatTimestamp(time.Date(2026, 11, 5, 13, 0, 1, 999, time.FixedZone("x", 3600)))
	if ts != "2026-11-05T12:00:01Z" {
		t.Fatalf("FormatTimestamp = %s", ts)
	}
}

func TestGroupStoreLifecycle(t *testing.T) {
	store, state := newTestStore(t)

	if _, err := store.Load("review"); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("Load before Create = %v, want ErrGroupNotFound", err)
	}
	rec, err := store.Create("review")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.GroupGeneration.Valid() || !rec.HostPrincipal.Generation.Valid() || rec.HostPrincipal.Name != HostMemberName {
		t.Fatalf("Create record = %+v", rec)
	}
	if rec.GroupGeneration == rec.HostPrincipal.Generation {
		t.Fatal("host principal reuses the group generation")
	}
	if rec.HubContainer != "enclave-a2a-hub-review" {
		t.Fatalf("HubContainer = %q", rec.HubContainer)
	}
	if _, err := store.Create("review"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second Create = %v, want ErrExist", err)
	}

	data, err := os.ReadFile(GroupRecordPath(state, "review"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"schemaVersion": "1"`, `"pendingRevision": null`, `"members": []`, `"credentials": []`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("group.json lacks %s:\n%s", want, data)
		}
	}

	rec.Committed = 3
	rec.Pending = &PendingRevision{Revision: 4, Purpose: "join", Member: "w1", StartedAt: FormatTimestamp(time.Now())}
	rec.Members = append(rec.Members, GroupMember{Name: "w1", Role: "worker", State: MemberJoining})
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("review")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Committed != 3 || loaded.Pending == nil || loaded.Pending.Revision != 4 || len(loaded.Members) != 1 {
		t.Fatalf("Load after Save = %+v", loaded)
	}

	stale := *loaded
	stale.GroupGeneration = "0000000000000000"
	if err := store.Save(&stale); err == nil {
		t.Fatal("Save with a different group generation succeeded")
	}

	groups, err := store.List()
	if err != nil || len(groups) != 1 || groups[0] != "review" {
		t.Fatalf("List = %v, %v", groups, err)
	}

	if err := store.Delete("review", "0000000000000000"); err == nil {
		t.Fatal("Delete with a stale generation succeeded")
	}
	if err := store.Delete("review", rec.GroupGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(GroupDir(state, "review")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("group dir after Delete without inventory: %v", err)
	}
	if groups, _ := store.List(); len(groups) != 0 {
		t.Fatalf("List after Delete = %v", groups)
	}
}

func TestGroupStoreRejectsUnknownSchema(t *testing.T) {
	store, state := newTestStore(t)
	if _, err := store.Create("review"); err != nil {
		t.Fatal(err)
	}
	path := GroupRecordPath(state, "review")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), `"schemaVersion": "1"`, `"schemaVersion": "2"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("review"); err == nil || !strings.Contains(err.Error(), "schemaVersion") {
		t.Fatalf("Load with schemaVersion 2 = %v", err)
	}
}

func TestGroupStoreCreateResetsPreviousGeneration(t *testing.T) {
	store, state := newTestStore(t)
	rec, err := store.Create("review")
	if err != nil {
		t.Fatal(err)
	}
	pub := NewSnapshotPublisher(state, nil)
	if _, err := pub.Publish(context.Background(), "review", testMembership(rec.GroupGeneration), testSecrets(rec.GroupGeneration)); err != nil {
		t.Fatal(err)
	}
	if err := WriteStatus(filepath.Join(StatusDir(state, "review"), ProxyStatus), StatusFile{Reader: ReaderProxy, GroupGeneration: rec.GroupGeneration, AppliedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	inv := NewInventory(state)
	if err := inv.Add("review", rec.GroupGeneration, testEntry(t, "w1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("review", rec.GroupGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(InventoryPath(state, "review", rec.GroupGeneration)); err != nil {
		t.Fatalf("inventory removed by Delete: %v", err)
	}
	if groups, _ := store.List(); len(groups) != 1 {
		t.Fatalf("List with pending inventory = %v", groups)
	}

	next, err := store.Create("review")
	if err != nil {
		t.Fatal(err)
	}
	if next.GroupGeneration == rec.GroupGeneration || next.HostPrincipal.Generation == rec.HostPrincipal.Generation {
		t.Fatal("recreated group reused a generation")
	}
	if cur, _ := pub.Current("review"); cur != 0 {
		t.Fatalf("Current after recreate = %d, want 0", cur)
	}
	if entries, _ := os.ReadDir(StatusDir(state, "review")); len(entries) != 0 {
		t.Fatalf("status files survived recreate: %v", entries)
	}
	if rev, err := pub.Publish(context.Background(), "review", testMembership(next.GroupGeneration), testSecrets(next.GroupGeneration)); err != nil || rev != 1 {
		t.Fatalf("first revision of new generation = %d, %v", rev, err)
	}
	if gens, _ := inv.Generations("review"); len(gens) != 1 || gens[0] != rec.GroupGeneration {
		t.Fatalf("old generation inventory = %v", gens)
	}
}

func TestGroupDirModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	store, state := newTestStore(t)
	if _, err := store.Create("review"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		GroupDir(state, "review"):                         0o700,
		SnapshotsDir(state, "review"):                     0o755,
		StatusDir(state, "review"):                        os.ModeSticky | 0o777,
		AuditDir(state, "review"):                         os.ModeSticky | 0o777,
		filepath.Dir(OutboundTokensPath(state, "review")): 0o700,
		GroupRecordPath(state, "review"):                  0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() & (os.ModePerm | os.ModeSticky); got != want {
			t.Errorf("%s mode = %v, want %v", path, got, want)
		}
	}
}

func TestGroupLockInProcess(t *testing.T) {
	store, _ := newTestStore(t)
	unlock, err := store.Lock(context.Background(), "review")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := store.Lock(ctx, "review"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Lock = %v, want deadline exceeded", err)
	}
	other, err := store.Lock(context.Background(), "other")
	if err != nil {
		t.Fatalf("lock of another group blocked: %v", err)
	}
	other()

	acquired := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		u, err := store.Lock(ctx, "review")
		if err == nil {
			u()
		}
		acquired <- err
	}()
	time.Sleep(50 * time.Millisecond)
	unlock()
	unlock()
	if err := <-acquired; err != nil {
		t.Fatalf("Lock after release = %v", err)
	}
	if _, err := store.Lock(context.Background(), "../x"); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("Lock with invalid name = %v", err)
	}
}

func TestGroupLockAcrossProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper uses a pipe protocol tested on unix")
	}
	store, state := newTestStore(t)
	path := filepath.Join(state, "locks", GroupLockName("review"))
	// #nosec G204 -- re-executes the test binary itself.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper said %q, %v", line, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := store.Lock(ctx, "review"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock held by another process = %v, want deadline exceeded", err)
	}

	_ = stdin.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	unlock, err := store.Lock(ctx2, "review")
	if err != nil {
		t.Fatalf("Lock after helper released = %v", err)
	}
	unlock()
}

func TestGroupStoreRecordsEngine(t *testing.T) {
	store, state := newTestStore(t)
	if _, err := store.CreateOn("review", Engine{Name: "podman", Root: "/home/u/.local/share/containers/storage"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(GroupRecordPath(state, "review"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"engine": {`) || !strings.Contains(string(data), `"name": "podman"`) {
		t.Fatalf("group.json lacks the engine:\n%s", data)
	}
	rec, err := store.Load("review")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Engine == nil || rec.Engine.Name != "podman" || rec.Engine.Root == "" {
		t.Fatalf("loaded engine = %+v", rec.Engine)
	}

	// Records written before the field existed load with no engine.
	legacy, err := store.Create("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Engine != nil {
		t.Fatalf("Create engine = %+v, want nil", legacy.Engine)
	}
	data, err = os.ReadFile(GroupRecordPath(state, "legacy"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"engine"`) {
		t.Fatalf("legacy group.json carries an engine:\n%s", data)
	}
}

func TestEngineSameDaemon(t *testing.T) {
	docker := Engine{Name: "docker", Root: "/var/lib/docker"}
	for _, tc := range []struct {
		other Engine
		want  bool
	}{
		{Engine{Name: "docker", Root: "/var/lib/docker"}, true},
		{Engine{Name: "docker"}, true},
		{Engine{Name: "docker", Root: "/home/u/.local/share/docker"}, false},
		{Engine{Name: "podman", Root: "/var/lib/docker"}, false},
	} {
		if got := docker.SameDaemon(tc.other); got != tc.want {
			t.Errorf("SameDaemon(%+v) = %v, want %v", tc.other, got, tc.want)
		}
	}
}
