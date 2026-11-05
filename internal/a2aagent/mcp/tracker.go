// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"fmt"
	"strings"

	"enclave/internal/a2a"
)

// tracker folds task snapshots and stream events into the latest known task state.
type tracker struct {
	peer       string
	generation string
	messageID  string

	taskID        string
	contextID     string
	state         a2a.TaskState
	queuePosition *int
	reason        string
	termination   string
	statusText    string
	artifacts     []artifactText
	messageOnly   bool
}

type artifactText struct {
	id   string
	text string
}

func (t *tracker) apply(r a2a.Result) {
	switch {
	case r.Task != nil:
		t.taskID = r.Task.ID
		t.setContext(r.Task.ContextID)
		t.applyStatus(r.Task.Status)
		t.applyMeta(r.Task.Metadata)
		if len(r.Task.Artifacts) > 0 {
			t.artifacts = t.artifacts[:0]
			for _, a := range r.Task.Artifacts {
				t.artifacts = append(t.artifacts, artifactText{id: a.ArtifactID, text: partsText(a.Parts)})
			}
		}
	case r.StatusUpdate != nil:
		if t.taskID == "" {
			t.taskID = r.StatusUpdate.TaskID
		}
		t.setContext(r.StatusUpdate.ContextID)
		t.applyStatus(r.StatusUpdate.Status)
		t.applyMeta(r.StatusUpdate.Metadata)
	case r.ArtifactUpdate != nil:
		u := r.ArtifactUpdate
		if t.taskID == "" {
			t.taskID = u.TaskID
		}
		t.setContext(u.ContextID)
		text := partsText(u.Artifact.Parts)
		for i := range t.artifacts {
			if t.artifacts[i].id == u.Artifact.ArtifactID {
				if u.Append {
					t.artifacts[i].text += text
				} else {
					t.artifacts[i].text = text
				}
				return
			}
		}
		t.artifacts = append(t.artifacts, artifactText{id: u.Artifact.ArtifactID, text: text})
	case r.Message != nil:
		t.messageOnly = true
		t.setContext(r.Message.ContextID)
		t.statusText = partsText(r.Message.Parts)
	}
}

func (t *tracker) setContext(id string) {
	if id != "" {
		t.contextID = id
	}
}

func (t *tracker) applyStatus(s a2a.TaskStatus) {
	if s.State != "" {
		t.state = s.State
	}
	if s.Message != nil {
		t.statusText = partsText(s.Message.Parts)
	}
	if t.state != a2a.TaskStateSubmitted {
		t.queuePosition = nil
	}
}

func (t *tracker) applyMeta(metadata map[string]any) {
	meta, ok, err := a2a.EnclaveMetaFrom(metadata)
	if !ok || err != nil {
		return
	}
	if meta.QueuePosition != nil && t.state == a2a.TaskStateSubmitted {
		pos := *meta.QueuePosition
		t.queuePosition = &pos
	}
	if meta.Reason != "" {
		t.reason = string(meta.Reason)
	}
	if meta.Termination != "" {
		t.termination = string(meta.Termination)
	}
}

func partsText(parts []a2a.Part) string {
	var texts []string
	for _, p := range parts {
		if p.Text != nil {
			texts = append(texts, *p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func (t *tracker) terminal() bool { return t.state.Terminal() }

// finalText prefers artifacts, the A2A result carrier, over the terminal status message.
func (t *tracker) finalText() string {
	var texts []string
	for _, a := range t.artifacts {
		if a.text != "" {
			texts = append(texts, a.text)
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "\n")
	}
	return t.statusText
}

func (t *tracker) handle() string {
	if t.taskID == "" {
		return ""
	}
	return a2a.TaskHandle{Worker: t.peer, Generation: t.generation, TaskID: t.taskID}.String()
}

func (t *tracker) result(note string) Result {
	res := Result{
		Handle:      t.handle(),
		State:       string(t.state),
		MessageID:   t.messageID,
		Reason:      t.reason,
		Termination: t.termination,
		Note:        note,
	}
	if t.contextID != "" {
		res.Context = a2a.ContextHandle{Worker: t.peer, Generation: t.generation, ContextID: t.contextID}.String()
	}
	if t.state == a2a.TaskStateSubmitted && t.queuePosition != nil {
		pos := *t.queuePosition
		res.QueuePosition = &pos
	}
	if t.terminal() || t.messageOnly {
		res.Message = t.finalText()
	}
	return res
}

// resultWithError keeps the handle and last known state next to the error.
func (t *tracker) resultWithError(note string, err error) Result {
	res := t.result(note)
	if err == nil {
		return res
	}
	described := describeError(err)
	res.Error = described.Error
	if described.Note != "" {
		res.Note = described.Note
	}
	return res
}

func (t *tracker) progressLine() string {
	switch {
	case t.taskID == "":
		return fmt.Sprintf("%s: sending", t.peer)
	case t.state == a2a.TaskStateSubmitted && t.queuePosition != nil:
		return fmt.Sprintf("%s: queued, position %d", t.peer, *t.queuePosition)
	default:
		return fmt.Sprintf("%s: %s", t.peer, strings.ToLower(strings.TrimPrefix(string(t.state), "TASK_STATE_")))
	}
}
