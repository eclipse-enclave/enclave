// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Journal file names inside the journal directory.
const (
	JournalFile     = "journal.json"
	journalLockFile = "journal.lock"
	journalSchema   = "1"
	// journalLimit bounds the entries kept; the oldest are dropped first.
	journalLimit = 100
)

// JournalHeader identifies the principal whose sends the journal records.
type JournalHeader struct {
	SchemaVersion    string `json:"schemaVersion"`
	Group            string `json:"group"`
	GroupGeneration  string `json:"groupGeneration"`
	Member           string `json:"member"`
	MemberGeneration string `json:"memberGeneration"`
	UpdatedAt        string `json:"updatedAt"`
}

// JournalEntry is one send, written before the request leaves the client.
type JournalEntry struct {
	MessageID string `json:"messageId"`
	Peer      string `json:"peer"`
	// Context is the context argument as sent, part of the retry match key.
	Context string `json:"context,omitempty"`
	// ContextHandle is the context the task ran in, as issued by the worker.
	ContextHandle string `json:"contextHandle,omitempty"`
	ContentHash   string `json:"contentHash"`
	Handle        string `json:"handle,omitempty"`
	State         string `json:"state,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Termination   string `json:"termination,omitempty"`
	Error         string `json:"error,omitempty"`
	Attempts      int    `json:"attempts"`
	SentAt        string `json:"sentAt"`
	UpdatedAt     string `json:"updatedAt"`
}

type journalFile struct {
	JournalHeader
	Entries []JournalEntry `json:"entries"`
}

// Journal is the durable send journal of one orchestrator principal (CR-P3-2).
type Journal struct {
	dir    string
	header JournalHeader
	now    func() time.Time
	mu     sync.Mutex
}

// OpenJournal prepares dir; entries recorded under another principal generation are discarded on the next write.
func OpenJournal(dir string, header JournalHeader, now func() time.Time) (*Journal, error) {
	if dir == "" {
		return nil, errors.New("a2a journal: directory not set")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("a2a journal: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	header.SchemaVersion = journalSchema
	return &Journal{dir: dir, header: header, now: now}, nil
}

// ContentHash is the journal's match key for a send's text.
func ContentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Update runs fn on the entries under the journal lock and persists them unless fn fails.
func (j *Journal) Update(fn func(entries []JournalEntry) ([]JournalEntry, error)) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	unlock, err := lockPath(filepath.Join(j.dir, journalLockFile))
	if err != nil {
		return fmt.Errorf("a2a journal: lock: %w", err)
	}
	defer unlock()
	entries, err := j.load()
	if err != nil {
		return err
	}
	entries, err = fn(entries)
	if err != nil {
		return err
	}
	if len(entries) > journalLimit {
		entries = entries[len(entries)-journalLimit:]
	}
	return j.save(entries)
}

// Entries returns a snapshot of the entries, oldest first.
func (j *Journal) Entries() ([]JournalEntry, error) {
	var out []JournalEntry
	err := j.Update(func(entries []JournalEntry) ([]JournalEntry, error) {
		out = append([]JournalEntry(nil), entries...)
		return nil, errReadOnly
	})
	if errors.Is(err, errReadOnly) {
		err = nil
	}
	return out, err
}

var errReadOnly = errors.New("read only")

// Timestamp formats t as the journal's RFC 3339 UTC second-precision time.
func (j *Journal) Timestamp() string {
	return j.now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func (j *Journal) path() string { return filepath.Join(j.dir, JournalFile) }

func (j *Journal) load() ([]JournalEntry, error) {
	data, err := os.ReadFile(j.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("a2a journal: %w", err)
	}
	var f journalFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("a2a journal: %s: %w", j.path(), err)
	}
	if f.SchemaVersion != journalSchema {
		return nil, fmt.Errorf("a2a journal: %s: unknown schemaVersion %q", j.path(), f.SchemaVersion)
	}
	// Message IDs and handles of another principal generation are unusable.
	if f.Group != j.header.Group || f.GroupGeneration != j.header.GroupGeneration ||
		f.Member != j.header.Member || f.MemberGeneration != j.header.MemberGeneration {
		return nil, nil
	}
	return f.Entries, nil
}

func (j *Journal) save(entries []JournalEntry) error {
	if entries == nil {
		entries = []JournalEntry{}
	}
	f := journalFile{JournalHeader: j.header, Entries: entries}
	f.UpdatedAt = j.Timestamp()
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("a2a journal: %w", err)
	}
	return writeAtomic(j.path(), append(data, '\n'))
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".journal-*")
	if err != nil {
		return fmt.Errorf("a2a journal: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // #nosec G703 -- name comes from os.CreateTemp.
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("a2a journal: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("a2a journal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("a2a journal: %w", err)
	}
	if err := os.Rename(name, path); err != nil { // #nosec G703 -- path is the journal file in the host-chosen directory.
		return fmt.Errorf("a2a journal: %w", err)
	}
	return nil
}
