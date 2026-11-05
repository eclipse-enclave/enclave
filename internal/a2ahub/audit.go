// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"enclave/internal/peergroup"
)

// AuditEntry is one line of audit.jsonl (interfaces.md section 11).
type AuditEntry struct {
	TS          string `json:"ts"`
	Group       string `json:"group"`
	Epoch       string `json:"epoch"`
	Revision    uint64 `json:"revision"`
	Principal   string `json:"principal"`
	Role        string `json:"role"`
	Generation  string `json:"generation"`
	Target      string `json:"target"`
	Method      string `json:"method"`
	TaskID      string `json:"taskId"`
	ContextID   string `json:"contextId"`
	Outcome     int    `json:"outcome"`
	Reason      string `json:"reason"`
	Termination string `json:"termination"`
	// Content is set only with ENCLAVE_A2A_LOG=full.
	Content *AuditContent `json:"content,omitempty"`
}

// AuditContent carries request and response text in full mode.
type AuditContent struct {
	Request  string `json:"request"`
	Response string `json:"response"`
}

type auditLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

func newAuditLog(dir string) *auditLog {
	return &auditLog{path: filepath.Join(dir, peergroup.AuditLogFile)}
}

// write appends one line; failures are reported but never fail a request.
func (a *auditLog) write(e AuditEntry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644) // #nosec G302 G304 -- fixed audit path, read by the host user.
		if err != nil {
			return fmt.Errorf("open audit log: %w", err)
		}
		a.f = f
	}
	if _, err := a.f.Write(line); err != nil {
		_ = a.f.Close()
		a.f = nil
		return fmt.Errorf("write audit log: %w", err)
	}
	return nil
}

func (a *auditLog) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f != nil {
		_ = a.f.Close()
		a.f = nil
	}
}
