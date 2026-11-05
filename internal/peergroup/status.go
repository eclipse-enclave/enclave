// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// Reader names in StatusFile.Reader.
const (
	ReaderSupervisor = "supervisor"
	ReaderProxy      = "proxy"
)

// StatusFile is status/supervisor.json and status/proxy.json (proposal 6).
type StatusFile struct {
	SchemaVersion   string     `json:"schemaVersion"`
	Reader          string     `json:"reader"`
	GroupGeneration Generation `json:"groupGeneration"`
	Epoch           string     `json:"epoch"`
	// AppliedRevision is the last revision this reader verified and applied; 0 before the first.
	AppliedRevision Revision `json:"appliedRevision"`
	// RejectedRevision is the last revision refused, 0 when none since the applied one.
	RejectedRevision Revision `json:"rejectedRevision"`
	Error            string   `json:"error"`
	UpdatedAt        string   `json:"updatedAt"`
}

// WriteStatus atomically replaces path (temp file and rename in the same directory).
func WriteStatus(path string, s StatusFile) error {
	if s.Reader != ReaderSupervisor && s.Reader != ReaderProxy {
		return fmt.Errorf("unknown status reader %q", s.Reader)
	}
	if err := checkSchema(&s.SchemaVersion); err != nil {
		return err
	}
	if s.UpdatedAt == "" {
		s.UpdatedAt = FormatTimestamp(time.Now())
	}
	return writeJSONAtomic(path, s, publicFileMode)
}

// statusPollInterval is how often WaitCommitted re-reads the status files.
const statusPollInterval = 50 * time.Millisecond

// CommitResult reports both readers at the end of a wait.
type CommitResult struct {
	Revision   Revision
	Supervisor *StatusFile
	Proxy      *StatusFile
	Committed  bool
}

// StatusWaiter decides commit: both status files show the revision applied.
type StatusWaiter interface {
	// WaitCommitted polls until both readers applied rev (Committed), either
	// rejected it (ErrRevisionRefused), or timeout elapses (ErrNotCommitted).
	WaitCommitted(ctx context.Context, group string, groupGeneration Generation, rev Revision, timeout time.Duration) (CommitResult, error)
	// Read returns the current status files; missing files are nil.
	Read(group string) (supervisor *StatusFile, proxy *StatusFile, err error)
}

// NewStatusWaiter returns the filesystem waiter.
func NewStatusWaiter(stateRoot string) StatusWaiter {
	return &fsStatusWaiter{stateRoot: stateRoot}
}

type fsStatusWaiter struct {
	stateRoot string
}

func (w *fsStatusWaiter) Read(group string) (*StatusFile, *StatusFile, error) {
	if err := ValidateGroupName(group); err != nil {
		return nil, nil, err
	}
	dir := StatusDir(w.stateRoot, group)
	sup, err := readStatus(filepath.Join(dir, SupervisorStatus), ReaderSupervisor)
	if err != nil {
		return nil, nil, err
	}
	proxy, err := readStatus(filepath.Join(dir, ProxyStatus), ReaderProxy)
	if err != nil {
		return nil, nil, err
	}
	return sup, proxy, nil
}

func readStatus(path, reader string) (*StatusFile, error) {
	var s StatusFile
	if err := readJSON(path, &s); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if s.Reader != reader {
		return nil, fmt.Errorf("%s names reader %q", path, s.Reader)
	}
	return &s, nil
}

func (w *fsStatusWaiter) WaitCommitted(ctx context.Context, group string, groupGeneration Generation, rev Revision, timeout time.Duration) (CommitResult, error) {
	res := CommitResult{Revision: rev}
	if err := ValidateGroupName(group); err != nil {
		return res, err
	}
	if rev == 0 {
		return res, errors.New("wait for revision 0")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(statusPollInterval)
	defer tick.Stop()
	var readErr error
	for {
		sup, proxy, err := w.Read(group)
		// Keep polling on read errors; the last one is reported on timeout.
		readErr = err
		if err == nil {
			res.Supervisor, res.Proxy = sup, proxy
			if applied(sup, groupGeneration, rev) && applied(proxy, groupGeneration, rev) {
				res.Committed = true
				return res, nil
			}
			for _, s := range []*StatusFile{sup, proxy} {
				if rejected(s, groupGeneration, rev) {
					return res, fmt.Errorf("%w: %s rejected revision %d: %s", ErrRevisionRefused, s.Reader, rev, s.Error)
				}
			}
		}
		select {
		case <-ctx.Done():
			return res, fmt.Errorf("%w: revision %d: %w", ErrNotCommitted, rev, ctx.Err())
		case <-deadline.C:
			if readErr != nil {
				return res, fmt.Errorf("%w: revision %d within %s: %w", ErrNotCommitted, rev, timeout, readErr)
			}
			return res, fmt.Errorf("%w: revision %d within %s (%s)", ErrNotCommitted, rev, timeout, describeAcks(res))
		case <-tick.C:
		}
	}
}

func applied(s *StatusFile, gen Generation, rev Revision) bool {
	return s != nil && s.GroupGeneration == gen && s.AppliedRevision >= rev
}

func rejected(s *StatusFile, gen Generation, rev Revision) bool {
	return s != nil && s.GroupGeneration == gen && s.RejectedRevision == rev
}

func describeAcks(res CommitResult) string {
	parts := make([]string, 0, 2)
	for _, r := range []struct {
		name string
		s    *StatusFile
	}{{ReaderSupervisor, res.Supervisor}, {ReaderProxy, res.Proxy}} {
		if r.s == nil {
			parts = append(parts, r.name+" has no status")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s applied %d", r.name, r.s.AppliedRevision))
	}
	return strings.Join(parts, ", ")
}
