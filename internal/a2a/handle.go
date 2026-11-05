// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

import (
	"errors"
	"fmt"
	"strings"
)

// Handle prefixes. Handles are locators, never authorization (proposal 7).
const (
	TaskHandlePrefix    = "task"
	ContextHandlePrefix = "ctx"
)

// TaskHandle locates a task: "task:<worker>:<generation>:<taskID>".
type TaskHandle struct {
	Worker     string
	Generation string
	TaskID     string
}

// ContextHandle locates a context: "ctx:<worker>:<generation>:<contextID>".
// The adapter-issued context ID carries the random part.
type ContextHandle struct {
	Worker     string
	Generation string
	ContextID  string
}

func (h TaskHandle) String() string {
	return formatHandle(TaskHandlePrefix, h.Worker, h.Generation, h.TaskID)
}

func (h ContextHandle) String() string {
	return formatHandle(ContextHandlePrefix, h.Worker, h.Generation, h.ContextID)
}

// ParseTaskHandle parses the String form of a TaskHandle.
func ParseTaskHandle(s string) (TaskHandle, error) {
	worker, generation, id, err := parseHandle(TaskHandlePrefix, s)
	if err != nil {
		return TaskHandle{}, err
	}
	return TaskHandle{Worker: worker, Generation: generation, TaskID: id}, nil
}

// ParseContextHandle parses the String form of a ContextHandle.
func ParseContextHandle(s string) (ContextHandle, error) {
	worker, generation, id, err := parseHandle(ContextHandlePrefix, s)
	if err != nil {
		return ContextHandle{}, err
	}
	return ContextHandle{Worker: worker, Generation: generation, ContextID: id}, nil
}

// MarshalText implements encoding.TextMarshaler with the String form.
func (h TaskHandle) MarshalText() ([]byte, error) {
	if err := validateHandleParts(h.Worker, h.Generation, h.TaskID); err != nil {
		return nil, err
	}
	return []byte(h.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler with ParseTaskHandle.
func (h *TaskHandle) UnmarshalText(text []byte) error {
	parsed, err := ParseTaskHandle(string(text))
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

// MarshalText implements encoding.TextMarshaler with the String form.
func (h ContextHandle) MarshalText() ([]byte, error) {
	if err := validateHandleParts(h.Worker, h.Generation, h.ContextID); err != nil {
		return nil, err
	}
	return []byte(h.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler with ParseContextHandle.
func (h *ContextHandle) UnmarshalText(text []byte) error {
	parsed, err := ParseContextHandle(string(text))
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

const maxWorkerNameLength = 32

const generationLength = 16

func formatHandle(prefix, worker, generation, id string) string {
	return prefix + ":" + worker + ":" + generation + ":" + id
}

func parseHandle(prefix, s string) (worker, generation, id string, err error) {
	// The ID is last and may itself contain colons.
	fields := strings.SplitN(s, ":", 4)
	if len(fields) != 4 {
		return "", "", "", fmt.Errorf("a2a: invalid %s handle: want %s:<worker>:<generation>:<id>", prefix, prefix)
	}
	if fields[0] != prefix {
		return "", "", "", fmt.Errorf("a2a: invalid %s handle: prefix %q", prefix, fields[0])
	}
	if err := validateHandleParts(fields[1], fields[2], fields[3]); err != nil {
		return "", "", "", err
	}
	return fields[1], fields[2], fields[3], nil
}

func validateHandleParts(worker, generation, id string) error {
	if !validWorkerName(worker) {
		return fmt.Errorf("a2a: invalid handle worker %q", worker)
	}
	if !validGeneration(generation) {
		return fmt.Errorf("a2a: invalid handle generation %q", generation)
	}
	if id == "" {
		return errors.New("a2a: invalid handle: empty id")
	}
	return nil
}

// Approximates model.SanitizeSessionName output without importing model.
func validWorkerName(name string) bool {
	if name == "" || len(name) > maxWorkerNameLength || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func validGeneration(generation string) bool {
	if len(generation) != generationLength {
		return false
	}
	for i := 0; i < len(generation); i++ {
		c := generation[i]
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
