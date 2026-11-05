// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package a2aagent is the in-container A2A library: the worker adapter
// (driver, context table, executor, server) and the orchestrator client.
// Implementations live in subpackages; this package holds the boundaries
// and the persisted context table format the host also reads.
package a2aagent

import (
	"context"
	"time"

	"enclave/internal/a2a"
)

// Context table files under <config store>/a2a/<memberGeneration>/
// (interfaces.md section 5). The adapter is the only writer while running.
const (
	SchemaVersion    = "1"
	ContextsFile     = "contexts.json"
	TasksDirName     = "tasks"
	DedupeFile       = "dedupe.json"
	RestrictionsFile = "restrictions.json"
	TranscriptsDir   = "transcripts"
)

// TableHeader identifies the table owner; every table file carries it.
type TableHeader struct {
	SchemaVersion    string `json:"schemaVersion"`
	Group            string `json:"group"`
	GroupGeneration  string `json:"groupGeneration"`
	Worker           string `json:"worker"`
	WorkerGeneration string `json:"workerGeneration"`
	UpdatedAt        string `json:"updatedAt"`
}

// PrincipalRef is the binding key: principal member and member generation.
type PrincipalRef struct {
	Member     string   `json:"member"`
	Role       a2a.Role `json:"role"`
	Generation string   `json:"generation"`
}

// ContextsFileData is contexts.json.
type ContextsFileData struct {
	TableHeader
	Contexts   []ContextRecord `json:"contexts"`
	Tombstones []Tombstone     `json:"tombstones"`
}

// ContextRecord binds a context to a principal and one harness session.
type ContextRecord struct {
	ContextID        string       `json:"contextId"`
	Principal        PrincipalRef `json:"principal"`
	HarnessSessionID string       `json:"harnessSessionId"`
	CreatedAt        string       `json:"createdAt"`
	// LastActivityAt is updated at enqueue; idle TTL runs from it.
	LastActivityAt string `json:"lastActivityAt"`
	// Transcripts are config-store-relative paths deleted on expiry and teardown.
	Transcripts []string `json:"transcripts"`
}

// Tombstone answers "expired" for a context ID after expiry.
type Tombstone struct {
	ContextID string       `json:"contextId"`
	Principal PrincipalRef `json:"principal"`
	ExpiredAt string       `json:"expiredAt"`
}

// TaskRecord is tasks/<taskID>.json.
type TaskRecord struct {
	TableHeader
	TaskID    string       `json:"taskId"`
	ContextID string       `json:"contextId"`
	Principal PrincipalRef `json:"principal"`
	MessageID string       `json:"messageId"`
	// ContentHash binds the message ID to the request content: "sha256:<hex>".
	ContentHash string `json:"contentHash"`
	// Seq orders admission; the queue is submitted tasks by Seq.
	Seq         uint64           `json:"seq"`
	Epoch       string           `json:"epoch"`
	State       a2a.TaskState    `json:"state"`
	Reason      a2a.EndingReason `json:"reason,omitempty"`
	Termination a2a.Termination  `json:"termination,omitempty"`
	PGID        int              `json:"pgid,omitempty"`
	AdmittedAt  string           `json:"admittedAt"`
	StartedAt   string           `json:"startedAt,omitempty"`
	EndedAt     string           `json:"endedAt,omitempty"`
	DeadlineAt  string           `json:"deadlineAt,omitempty"`
	Result      string           `json:"result,omitempty"`
	Truncated   bool             `json:"truncated,omitempty"`
}

// HasResultArtifact reports whether a task carries the result artifact; a completed task does even with an empty reply.
func HasResultArtifact(rec *TaskRecord) bool {
	return rec.State == a2a.TaskStateCompleted || rec.Result != ""
}

// TerminalMeta is the enclave metadata of an ended task, on Task.metadata and the terminal status event.
func TerminalMeta(rec *TaskRecord) a2a.EnclaveMeta {
	return a2a.EnclaveMeta{Termination: rec.Termination, Reason: rec.Reason, Truncated: rec.Truncated}
}

// DedupeFileData is dedupe.json.
type DedupeFileData struct {
	TableHeader
	Records []DedupeRecord `json:"records"`
}

// DedupeRecord is keyed by principal member, generation and message ID.
type DedupeRecord struct {
	Principal   PrincipalRef `json:"principal"`
	MessageID   string       `json:"messageId"`
	ContentHash string       `json:"contentHash"`
	TaskID      string       `json:"taskId"`
	AdmittedAt  string       `json:"admittedAt"`
}

// RestrictionsFileData is restrictions.json. UpdatedAt doubles as the
// heartbeat the host compares with the ps staleness bound.
type RestrictionsFileData struct {
	TableHeader
	AdmissionStops []AdmissionStop `json:"admissionStops"`
	Quarantine     *Quarantine     `json:"quarantine"`
}

// AdmissionStop is a persisted per-principal admission stop; only host reconcile clears it.
type AdmissionStop struct {
	Principal PrincipalRef `json:"principal"`
	// Reason is "suspected" or "authoritative".
	Reason string `json:"reason"`
	Since  string `json:"since"`
}

// Quarantine is set when termination cannot be confirmed.
type Quarantine struct {
	Since  string `json:"since"`
	TaskID string `json:"taskId,omitempty"`
	PGID   int    `json:"pgid,omitempty"`
	Reason string `json:"reason"`
}

// Admission is the outcome of ContextTable.Admit.
type Admission struct {
	Task *TaskRecord
	// Existing is set when a flagged retry matched a known original.
	Existing bool
	// QueuePosition is 0 when the task can start immediately.
	QueuePosition int
}

// AdmitRequest is one send after authentication.
type AdmitRequest struct {
	Principal   PrincipalRef
	Epoch       string
	MessageID   string
	ContentHash string
	Retry       bool
	// ContextID is empty to create a context.
	ContextID string
}

// ContextTable is the persisted table (proposal 9, 10, 16). Errors that
// reach the client are *a2a.Error with enclave codes.
type ContextTable interface {
	// Admit runs dedupe, binding and admission checks and persists the task
	// before returning; nothing executes before Admit returns.
	Admit(ctx context.Context, req AdmitRequest) (Admission, error)
	// Task returns a task bound to principal, else an a2a.CodeTaskNotFound error.
	Task(principal PrincipalRef, taskID string) (*TaskRecord, error)
	UpdateTask(rec *TaskRecord) error
	// Next returns the oldest submitted task not under an admission stop.
	Next() (*TaskRecord, error)
	BindHarnessSession(contextID, harnessSessionID string, transcripts []string) error
	Context(principal PrincipalRef, contextID string) (*ContextRecord, error)
	StopAdmission(principal PrincipalRef, reason string) error
	ReleaseAdmission(principal PrincipalRef) error
	SetQuarantine(q *Quarantine) error
	Restrictions() (RestrictionsFileData, error)
	// Expire applies idle TTL and record bounds; contexts with queued or running tasks never expire.
	Expire(now time.Time) error
	// Heartbeat refreshes restrictions.json UpdatedAt.
	Heartbeat(now time.Time) error
}
