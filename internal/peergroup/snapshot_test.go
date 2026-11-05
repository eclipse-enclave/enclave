// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"

	"enclave/internal/a2a"
)

const testGen Generation = "3f9c0a1b2c3d4e5f"

func testMembership(gen Generation) Membership {
	return Membership{
		Group:           "review",
		GroupGeneration: gen,
		Members: []MembershipMember{{
			Name: "worker-1", Role: a2a.RoleWorker, Generation: "a1b2c3d4e5f60718",
			ProjectPath: "/home/u/src/app", MountMode: MountReadWrite, AdapterPort: AdapterPort,
			InboundTokenHash: "sha256:00",
		}},
		HostPrincipal: HostPrincipalEntry{Name: HostMemberName, Generation: "0011223344556677"},
	}
}

func testSecrets(gen Generation) Secrets {
	return Secrets{
		Group:           "review",
		GroupGeneration: gen,
		Outbound:        []OutboundSecret{{Member: "worker-1", Generation: "a1b2c3d4e5f60718", Token: "secret-token"}},
	}
}

func publishN(t *testing.T, pub SnapshotPublisher, n int) {
	t.Helper()
	for range n {
		if _, err := pub.Publish(context.Background(), "review", testMembership(testGen), testSecrets(testGen)); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotEntries(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestPublishAndLoadCurrent(t *testing.T) {
	state := t.TempDir()
	var ownedPath string
	pub := NewSnapshotPublisher(state, func(_ context.Context, path string) error {
		ownedPath = path
		return nil
	})
	if cur, err := pub.Current("review"); err != nil || cur != 0 {
		t.Fatalf("Current before publish = %d, %v", cur, err)
	}
	rev, err := pub.Publish(context.Background(), "review", testMembership(testGen), testSecrets(testGen))
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 {
		t.Fatalf("first revision = %d", rev)
	}
	if ownedPath != filepath.Join(SnapshotsDir(state, "review"), StagingPrefix+"1", SecretsFile) {
		t.Fatalf("owner hook saw %q, want the staged secrets file", ownedPath)
	}
	target, err := os.Readlink(CurrentLinkPath(state, "review"))
	if err != nil || target != "1" {
		t.Fatalf("current -> %q, %v; want relative 1", target, err)
	}
	if cur, _ := pub.Current("review"); cur != 1 {
		t.Fatalf("Current = %d", cur)
	}

	root := SnapshotsDir(state, "review")
	snap, err := LoadCurrent(root, testGen, true)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Manifest.Revision != 1 || snap.Membership.Revision != 1 || snap.Secrets.Revision != 1 {
		t.Fatalf("revisions = %d %d %d", snap.Manifest.Revision, snap.Membership.Revision, snap.Secrets.Revision)
	}
	if snap.Membership.HubPort != HubPort || snap.Secrets.Outbound[0].Token != "secret-token" {
		t.Fatalf("snapshot content = %+v", snap)
	}
	if snap.Membership.HostPrincipal.Credentials == nil || snap.Membership.Members[0].Skills == nil {
		t.Fatal("empty lists not normalized")
	}
	for _, f := range []string{MembershipFile, SecretsFile} {
		if !strings.HasPrefix(snap.Manifest.Files[f], HashPrefix) || len(snap.Manifest.Files[f]) != len(HashPrefix)+64 {
			t.Fatalf("manifest hash of %s = %q", f, snap.Manifest.Files[f])
		}
	}
	noSecrets, err := LoadCurrent(root, testGen, false)
	if err != nil || noSecrets.Secrets != nil {
		t.Fatalf("LoadCurrent without secrets = %+v, %v", noSecrets, err)
	}
	if _, err := LoadCurrent(root, "ffffffffffffffff", true); err == nil {
		t.Fatal("LoadCurrent accepted another group generation")
	}

	publishN(t, pub, 2)
	if cur, _ := pub.Current("review"); cur != 3 {
		t.Fatalf("Current after three publishes = %d", cur)
	}
	if snap, err := LoadCurrent(root, testGen, true); err != nil || snap.Membership.Revision != 3 {
		t.Fatalf("LoadCurrent after swap = %v", err)
	}
}

func TestPublishModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	state := t.TempDir()
	pub := NewSnapshotPublisher(state, nil)
	publishN(t, pub, 1)
	dir := SnapshotDir(state, "review", 1)
	for name, want := range map[string]os.FileMode{
		"":             0o755,
		MembershipFile: 0o644,
		SecretsFile:    0o400,
		ManifestFile:   0o644,
	} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %v, want %v", name, got, want)
		}
	}
}

func TestPublishRejectsMismatchedInput(t *testing.T) {
	state := t.TempDir()
	pub := NewSnapshotPublisher(state, nil)
	m, s := testMembership(testGen), testSecrets(testGen)
	s.GroupGeneration = "ffffffffffffffff"
	if _, err := pub.Publish(context.Background(), "review", m, s); err == nil {
		t.Fatal("Publish accepted mismatched generations")
	}
	m, s = testMembership(testGen), testSecrets(testGen)
	m.Group = "other"
	if _, err := pub.Publish(context.Background(), "review", m, s); err == nil {
		t.Fatal("Publish accepted another group")
	}
	m = testMembership(testGen)
	m.SchemaVersion = "2"
	if _, err := pub.Publish(context.Background(), "review", m, testSecrets(testGen)); err == nil {
		t.Fatal("Publish accepted schemaVersion 2")
	}
}

func TestPublishOwnerFailureLeavesCurrent(t *testing.T) {
	state := t.TempDir()
	fail := false
	pub := NewSnapshotPublisher(state, func(context.Context, string) error {
		if fail {
			return errors.New("chown refused")
		}
		return nil
	})
	publishN(t, pub, 1)
	fail = true
	if _, err := pub.Publish(context.Background(), "review", testMembership(testGen), testSecrets(testGen)); err == nil {
		t.Fatal("Publish ignored the owner hook error")
	}
	if cur, _ := pub.Current("review"); cur != 1 {
		t.Fatalf("Current after failed publish = %d, want 1", cur)
	}
	fail = false
	rev, err := pub.Publish(context.Background(), "review", testMembership(testGen), testSecrets(testGen))
	if err != nil || rev != 3 {
		t.Fatalf("revision after a failed staging = %d, %v; want 3 (2 is never reused)", rev, err)
	}
}

func TestPruneRetention(t *testing.T) {
	state := t.TempDir()
	pub := NewSnapshotPublisher(state, nil)
	publishN(t, pub, 4)
	root := SnapshotsDir(state, "review")
	if err := os.Mkdir(filepath.Join(root, StagingPrefix+"9"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("4", filepath.Join(root, currentTempPrefix+"8")); err != nil {
		t.Fatal(err)
	}
	if err := pub.Prune("review", 4, 3); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(snapshotEntries(t, root), ",")
	if got != "3,4,current" {
		t.Fatalf("after Prune(4, 3): %s", got)
	}
	if rev, err := pub.Publish(context.Background(), "review", testMembership(testGen), testSecrets(testGen)); err != nil || rev != 5 {
		t.Fatalf("publish after prune = %d, %v", rev, err)
	}
	// Revision 5 is published but not committed; current must survive Prune(4, 3).
	if err := pub.Prune("review", 4, 3); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(snapshotEntries(t, root), ","); got != "3,4,5,current" {
		t.Fatalf("after Prune(4, 3) with pending 5: %s", got)
	}
	if err := pub.Prune("review", 6, 0); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(snapshotEntries(t, root), ","); got != "5,current" {
		t.Fatalf("after Prune(6, 0): %s", got)
	}
	if err := pub.Prune("review", 4, 4); err == nil {
		t.Fatal("Prune accepted previous not below committed")
	}
	if err := pub.Prune("missing", 1, 0); err != nil {
		t.Fatalf("Prune of an unpublished group = %v", err)
	}
}

func TestPruneKeepsCommittedNotFailedRevision(t *testing.T) {
	state := t.TempDir()
	pub := NewSnapshotPublisher(state, nil)
	// 7 commits, 8 fails, rollback 9 commits: 7 stays as the rollback target, 8 goes.
	publishN(t, pub, 9)
	if err := pub.Prune("review", 9, 7); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(snapshotEntries(t, SnapshotsDir(state, "review")), ","); got != "7,9,current" {
		t.Fatalf("after Prune(9, 7) with failed 8: %s", got)
	}
}

func TestLoadCurrentRefusesBadSnapshots(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"tampered membership": func(t *testing.T, dir string) {
			appendFile(t, filepath.Join(dir, MembershipFile), " ")
		},
		"tampered secrets": func(t *testing.T, dir string) {
			appendFile(t, filepath.Join(dir, SecretsFile), " ")
		},
		"membership revision differs": func(t *testing.T, dir string) {
			rewriteJSON(t, dir, MembershipFile, func(v map[string]any) { v["revision"] = 2 })
		},
		"secrets revision differs": func(t *testing.T, dir string) {
			rewriteJSON(t, dir, SecretsFile, func(v map[string]any) { v["revision"] = 2 })
		},
		"secrets generation differs": func(t *testing.T, dir string) {
			rewriteJSON(t, dir, SecretsFile, func(v map[string]any) { v["groupGeneration"] = "ffffffffffffffff" })
		},
		"manifest revision differs": func(t *testing.T, dir string) {
			rewriteManifest(t, dir, func(m *Manifest) { m.Revision = 2 })
		},
		"manifest lists extra file": func(t *testing.T, dir string) {
			rewriteManifest(t, dir, func(m *Manifest) { m.Files["x.json"] = HashPrefix })
		},
		"unknown schema": func(t *testing.T, dir string) {
			rewriteManifest(t, dir, func(m *Manifest) { m.SchemaVersion = "2" })
		},
		"missing secrets": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, SecretsFile)); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			state := t.TempDir()
			publishN(t, NewSnapshotPublisher(state, nil), 1)
			dir := SnapshotDir(state, "review", 1)
			makeWritable(t, dir)
			corrupt(t, dir)
			if _, err := LoadCurrent(SnapshotsDir(state, "review"), testGen, true); err == nil {
				t.Fatal("LoadCurrent accepted a corrupt snapshot")
			}
		})
	}
	t.Run("current outside the root", func(t *testing.T) {
		state := t.TempDir()
		root := SnapshotsDir(state, "review")
		publishN(t, NewSnapshotPublisher(state, nil), 1)
		link := CurrentLinkPath(state, "review")
		_ = os.Remove(link)
		if err := os.Symlink("../../1", link); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCurrent(root, testGen, true); err == nil {
			t.Fatal("LoadCurrent followed a non-revision link")
		}
	})
}

func TestLocalSecretsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX ownership")
	}
	path := filepath.Join(t.TempDir(), SecretsFile)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LocalSecretsOwner(os.Geteuid())(context.Background(), path); err != nil {
		t.Fatalf("own uid: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	if os.Geteuid() != 0 {
		err := LocalSecretsOwner(HubProxyUID)(context.Background(), path)
		if !errors.Is(err, syscall.EPERM) {
			t.Fatalf("chown to another uid as non-root = %v, want EPERM", err)
		}
	}
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(s)
	_ = f.Close()
}

func makeWritable(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(filepath.Join(dir, SecretsFile), 0o600); err != nil {
		t.Fatal(err)
	}
}

// rewriteJSON edits a snapshot file and fixes the manifest hash, so only the
// field check can catch it.
func rewriteJSON(t *testing.T, dir, name string, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	edit(v)
	out, _ := json.Marshal(v)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	rewriteManifest(t, dir, func(m *Manifest) { m.Files[name] = hashBytes(out) })
}

func rewriteManifest(t *testing.T, dir string, edit func(*Manifest)) {
	t.Helper()
	path := filepath.Join(dir, ManifestFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	edit(&m)
	out, _ := json.Marshal(m)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}
