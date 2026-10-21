// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netcapture

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareSessionDirPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs", "capture")
	dir, err := PrepareSessionDir(root, "enclave-claude-abc-1")
	if err != nil {
		t.Fatalf("PrepareSessionDir() error = %v", err)
	}
	if dir != filepath.Join(root, "enclave-claude-abc-1") {
		t.Fatalf("dir = %s", dir)
	}
	for path, want := range map[string]os.FileMode{root: 0o700, dir: 0o777} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestPrepareSessionDirRejectsPathNames(t *testing.T) {
	for _, session := range []string{"", "../escape", "a/b", ".hidden"} {
		if _, err := PrepareSessionDir(t.TempDir(), session); err == nil {
			t.Fatalf("PrepareSessionDir(%q) accepted the name", session)
		}
	}
}

// writeCapture places a finished capture of the given body size on disk.
func writeCapture(t *testing.T, dir string, id string, bodyBytes int, modified time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	record := RecordPath(dir, id)
	if err := os.WriteFile(record, []byte(`{"id":"`+id+`"}`), 0o600); err != nil {
		t.Fatalf("write record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+responseBodySuffix), []byte(strings.Repeat("x", bodyBytes)), 0o600); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := os.Chtimes(record, modified, modified); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func TestPruneStoreRemovesOldestCapturesAcrossSessions(t *testing.T) {
	root := t.TempDir()
	base := time.Now().Add(-time.Hour)
	writeCapture(t, filepath.Join(root, "session-a"), "000000000000000a", 1000, base)
	writeCapture(t, filepath.Join(root, "session-b"), "000000000000000b", 1000, base.Add(time.Minute))
	writeCapture(t, filepath.Join(root, "session-a"), "000000000000000c", 1000, base.Add(2*time.Minute))
	// A recently written body without a record belongs to a capture in flight;
	// one untouched for a day was left by a gateway that stopped mid-capture.
	inFlight := filepath.Join(root, "session-b", "000000000000000d"+requestBodySuffix)
	if err := os.WriteFile(inFlight, []byte(strings.Repeat("x", 5000)), 0o600); err != nil {
		t.Fatalf("write in-flight body: %v", err)
	}
	abandonedBody := filepath.Join(root, "session-c", "000000000000000e"+responseBodySuffix)
	abandonedStaging := filepath.Join(root, "session-c", ".000000000000000e.json.123")
	for _, path := range []string{abandonedBody, abandonedStaging} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("x", 5000)), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		old := time.Now().Add(-AbandonedAfter - time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	removed, err := PruneStore(root, 2100)
	if err != nil {
		t.Fatalf("PruneStore() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(RecordPath(filepath.Join(root, "session-a"), "000000000000000a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oldest capture survived")
	}
	if _, err := os.Stat(filepath.Join(root, "session-a", "000000000000000a"+responseBodySuffix)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oldest capture's body survived")
	}
	for _, path := range []string{
		RecordPath(filepath.Join(root, "session-b"), "000000000000000b"),
		RecordPath(filepath.Join(root, "session-a"), "000000000000000c"),
		inFlight,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was removed: %v", path, err)
		}
	}
	for _, path := range []string{abandonedBody, abandonedStaging} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("abandoned %s survived: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "session-c")); err != nil {
		t.Fatalf("pruning removed a session directory: %v", err)
	}
}

func TestPruneStoreToleratesMissingRoot(t *testing.T) {
	if removed, err := PruneStore(filepath.Join(t.TempDir(), "missing"), 1); err != nil || removed != 0 {
		t.Fatalf("PruneStore(missing) = %d, %v", removed, err)
	}
}

func TestFindLocatesCaptureAcrossRootsAndSessions(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	writeCapture(t, filepath.Join(rootB, "session-x"), "00000000000000aa", 1, time.Now())

	path, err := Find([]string{rootA, rootB}, "", "00000000000000aa")
	if err != nil || path != RecordPath(filepath.Join(rootB, "session-x"), "00000000000000aa") {
		t.Fatalf("Find() = %q, %v", path, err)
	}
	if _, err := Find([]string{rootA, rootB}, "session-y", "00000000000000aa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Find(other session) error = %v, want ErrNotFound", err)
	}
	if _, err := Find([]string{rootA}, "", "../../etc/passwd"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Find(path) error = %v, want an invalid ID error", err)
	}

	for _, session := range []string{".", "..", "a/b"} {
		if _, err := Find([]string{rootB}, session, "00000000000000aa"); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("Find(session %q) error = %v, want an invalid session error", session, err)
		}
	}

	writeCapture(t, filepath.Join(rootA, "session-y"), "00000000000000aa", 1, time.Now())
	if _, err := Find([]string{rootA, rootB}, "", "00000000000000aa"); err == nil || !strings.Contains(err.Error(), "--session") {
		t.Fatalf("Find(ambiguous) error = %v", err)
	}
}

// The state directory is user-chosen, so pattern characters in it are plain
// characters.
func TestFindTreatsRootLiterally(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state [x]*?", "capture")
	writeCapture(t, filepath.Join(root, "session-x"), "00000000000000bb", 1, time.Now())
	if path, err := Find([]string{root}, "", "00000000000000bb"); err != nil || path != RecordPath(filepath.Join(root, "session-x"), "00000000000000bb") {
		t.Fatalf("Find() = %q, %v", path, err)
	}
}

func TestBodyPathRefusesNamesOutsideTheSessionDir(t *testing.T) {
	record := filepath.Join("root", "session", "0123456789abcdef.json")
	for _, name := range []string{"../escape", "/etc/passwd", ".hidden", "", "0123456789abcdef.json", "\x1b]0;x\x07.request.body"} {
		if _, ok := BodyPath(record, &Body{File: name}); ok {
			t.Fatalf("BodyPath accepted %q", name)
		}
	}
	if path, ok := BodyPath(record, &Body{File: "0123456789abcdef.request.body"}); !ok || path != filepath.Join("root", "session", "0123456789abcdef.request.body") {
		t.Fatalf("BodyPath = %q, %v", path, ok)
	}
}

// The gateway can write into a session directory, so a file there is not
// trusted to be what its name says: a link is refused rather than followed.
func TestCaptureReadsRefuseLinksOutOfTheSessionDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "session-x")
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	jsonSecret := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(jsonSecret, []byte(`{"auths":{"registry":"token"}}`), 0o600); err != nil {
		t.Fatalf("write json secret: %v", err)
	}

	const linkedBody = "00000000000000c1"
	writeCapture(t, dir, linkedBody, 1, time.Now())
	bodyPath := filepath.Join(dir, linkedBody+responseBodySuffix)
	if err := os.Remove(bodyPath); err != nil {
		t.Fatalf("remove body: %v", err)
	}
	if err := os.Symlink(secret, bodyPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path, data, err := ReadBody(RecordPath(dir, linkedBody), &Body{File: linkedBody + responseBodySuffix})
	if err == nil || len(data) != 0 || path != bodyPath {
		t.Fatalf("ReadBody(link) = %q, %q, %v; want the link refused", path, data, err)
	}

	const linkedRecord = "00000000000000c2"
	if err := os.Symlink(jsonSecret, RecordPath(dir, linkedRecord)); err != nil {
		t.Fatalf("symlink record: %v", err)
	}
	if _, err := Find([]string{root}, "", linkedRecord); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Find(linked record) error = %v, want ErrNotFound", err)
	}
	if raw, err := ReadRecord(RecordPath(dir, linkedRecord)); err == nil {
		t.Fatalf("ReadRecord(link) = %q, want the link refused", raw)
	}
}

func TestReadBodyRefusesFilesLargerThanTheStoreWrites(t *testing.T) {
	dir := t.TempDir()
	const id = "00000000000000c3"
	writeCapture(t, dir, id, 1, time.Now())
	if err := os.Truncate(filepath.Join(dir, id+responseBodySuffix), MaxBodyBytes+1); err != nil {
		t.Fatalf("grow body: %v", err)
	}
	if _, data, err := ReadBody(RecordPath(dir, id), &Body{File: id + responseBodySuffix}); err == nil || len(data) != 0 {
		t.Fatalf("ReadBody(oversized) = %d bytes, %v; want it refused", len(data), err)
	}
}
