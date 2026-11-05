// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testHeader = JournalHeader{Group: "g", GroupGeneration: "aaaaaaaaaaaaaaaa", Member: "lead", MemberGeneration: "bbbbbbbbbbbbbbbb"}

func appendEntries(t *testing.T, j *Journal, ids ...string) {
	t.Helper()
	err := j.Update(func(entries []JournalEntry) ([]JournalEntry, error) {
		for _, id := range ids {
			entries = append(entries, JournalEntry{MessageID: id, Peer: "p", ContentHash: ContentHash(id)})
		}
		return entries, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJournalPersistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir, testHeader, nil)
	if err != nil {
		t.Fatal(err)
	}
	appendEntries(t, j, "m1")
	again, _ := OpenJournal(dir, testHeader, nil)
	entries, err := again.Entries()
	if err != nil || len(entries) != 1 || entries[0].MessageID != "m1" {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	info, err := os.Stat(filepath.Join(dir, JournalFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal file mode = %v, %v", info.Mode(), err)
	}
}

func TestJournalDiscardsOtherGeneration(t *testing.T) {
	dir := t.TempDir()
	j, _ := OpenJournal(dir, testHeader, nil)
	appendEntries(t, j, "m1")
	rejoined := testHeader
	rejoined.MemberGeneration = "cccccccccccccccc"
	j2, _ := OpenJournal(dir, rejoined, nil)
	if entries, err := j2.Entries(); err != nil || len(entries) != 0 {
		t.Fatalf("entries of a new generation = %+v, %v", entries, err)
	}
}

func TestJournalRejectsUnknownSchema(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, JournalFile), []byte(`{"schemaVersion":"2","entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	j, _ := OpenJournal(dir, testHeader, nil)
	if _, err := j.Entries(); err == nil || !strings.Contains(err.Error(), "schemaVersion") {
		t.Fatalf("err = %v", err)
	}
}

func TestJournalBoundKeepsNewest(t *testing.T) {
	j, _ := OpenJournal(t.TempDir(), testHeader, nil)
	var ids []string
	for i := range journalLimit + 5 {
		ids = append(ids, fmt.Sprintf("m%d", i))
	}
	appendEntries(t, j, ids...)
	entries, _ := j.Entries()
	if len(entries) != journalLimit || entries[0].MessageID != "m5" || entries[len(entries)-1].MessageID != ids[len(ids)-1] {
		t.Fatalf("kept %d, first %s", len(entries), entries[0].MessageID)
	}
}
