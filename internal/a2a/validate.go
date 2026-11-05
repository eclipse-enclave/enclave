// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

var knownTaskStates = map[TaskState]bool{
	TaskStateSubmitted:     true,
	TaskStateWorking:       true,
	TaskStateCompleted:     true,
	TaskStateCanceled:      true,
	TaskStateFailed:        true,
	TaskStateRejected:      true,
	TaskStateInputRequired: true,
	TaskStateAuthRequired:  true,
}

// Valid reports whether s is a 1.0 task state other than TaskStateUnspecified.
func (s TaskState) Valid() bool { return knownTaskStates[s] }

// Terminal reports whether s ends the task (spec 9.4.6 list).
func (s TaskState) Terminal() bool {
	switch s {
	case TaskStateCompleted, TaskStateCanceled, TaskStateFailed, TaskStateRejected:
		return true
	}
	return false
}

// MarshalJSON refuses to emit an empty, unspecified or unknown state.
func (s TaskState) MarshalJSON() ([]byte, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("a2a: invalid task state %q", string(s))
	}
	return json.Marshal(string(s))
}

// UnmarshalJSON rejects TASK_STATE_UNSPECIFIED and values unknown to 1.0.
func (s *TaskState) UnmarshalJSON(data []byte) error {
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("a2a: task state: %w", err)
	}
	if !TaskState(v).Valid() {
		return fmt.Errorf("a2a: invalid task state %q", v)
	}
	*s = TaskState(v)
	return nil
}

// TextPart builds a text part.
func TextPart(text string) Part { return Part{Text: &text} }

func (p Part) contentMembers() int {
	n := 0
	if p.Text != nil {
		n++
	}
	if p.Raw != "" {
		n++
	}
	if p.URL != "" {
		n++
	}
	if len(p.Data) > 0 {
		n++
	}
	return n
}

// IsText reports whether p is a well-formed text part.
func (p Part) IsText() bool { return p.Text != nil && p.contentMembers() == 1 }

// CheckTextParts accepts a non-empty list of text parts only (proposal 4, v1).
func CheckTextParts(parts []Part) *Error {
	if len(parts) == 0 {
		return invalidParams("at least one part is required")
	}
	for i, p := range parts {
		switch {
		case p.contentMembers() != 1:
			return invalidParams(fmt.Sprintf("part %d must set exactly one of text, raw, url, data", i))
		case p.Text == nil:
			return &Error{
				Code:    CodeContentTypeNotSupported,
				Message: fmt.Sprintf("Content type not supported: part %d is not a text part", i),
			}
		}
	}
	return nil
}

// Validate checks the required members of a message (spec 5.7) and that every part is text.
func (m *Message) Validate() *Error {
	if m.MessageID == "" {
		return invalidParams("message.messageId is required")
	}
	if m.Role != RoleUser && m.Role != RoleAgent {
		return invalidParams(fmt.Sprintf("message.role %q is not %s or %s", m.Role, RoleUser, RoleAgent))
	}
	return CheckTextParts(m.Parts)
}

func invalidParams(detail string) *Error {
	return &Error{Code: CodeInvalidParams, Message: "Invalid parameters: " + detail}
}

// CheckVersion answers a missing or other A2A-Version than ProtocolVersion with -32009.
func CheckVersion(h http.Header) *Error {
	v := strings.TrimSpace(h.Get(HeaderVersion))
	if v == ProtocolVersion {
		return nil
	}
	if v == "" {
		// Spec 3.6.2: an empty version means 0.3.
		v = "0.3"
	}
	return &Error{
		Code:    CodeVersionNotSupported,
		Message: fmt.Sprintf("Protocol version %s not supported; this agent speaks %s", v, ProtocolVersion),
	}
}

// CheckExtensions answers a request that does not opt into ExtensionURI with -32008.
func CheckExtensions(h http.Header) *Error {
	for _, value := range h.Values(HeaderExtensions) {
		for _, uri := range strings.Split(value, ",") {
			if strings.TrimSpace(uri) == ExtensionURI {
				return nil
			}
		}
	}
	return &Error{Code: CodeExtensionSupportRequired, Message: "Extension support required: " + ExtensionURI}
}

// SetProtocolHeaders sets the A2A-Version and A2A-Extensions headers every enclave request carries.
func SetProtocolHeaders(h http.Header) {
	h.Set(HeaderVersion, ProtocolVersion)
	h.Set(HeaderExtensions, ExtensionURI)
}
