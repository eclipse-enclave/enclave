// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestSignalContainerNormalizesSignalName(t *testing.T) {
	for input, want := range map[string]string{
		"HUP":        "HUP",
		"SIGHUP":     "HUP",
		" sighup ":   "HUP",
		"term":       "TERM",
		"1":          "1",
		"RTMIN+3":    "RTMIN+3",
		"SIGRTMAX-1": "RTMAX-1",
	} {
		logPath := installNetworkDockerStub(t, `exit 0`)
		if err := SignalContainer(context.Background(), "hub", input); err != nil {
			t.Fatalf("SignalContainer(%q) error = %v", input, err)
		}
		wantArgs := []string{"kill", "--signal", want, "hub"}
		if got := readNetworkDockerArgs(t, logPath); !reflect.DeepEqual(got, wantArgs) {
			t.Fatalf("SignalContainer(%q) args = %v, want %v", input, got, wantArgs)
		}
	}
}

func TestSignalContainerRejectsInvalidInput(t *testing.T) {
	logPath := installNetworkDockerStub(t, `exit 0`)
	for _, tc := range []struct{ container, signal string }{
		{"hub", ""},
		{"hub", "SIG"},
		{"hub", "--help"},
		{"hub", "HUP; rm"},
		{"hub", "+HUP"},
		{" ", "HUP"},
	} {
		if err := SignalContainer(context.Background(), tc.container, tc.signal); err == nil {
			t.Errorf("SignalContainer(%q, %q) succeeded, want an error", tc.container, tc.signal)
		}
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("invalid input reached the engine CLI")
	}
}

func TestIsContainerNotRunning(t *testing.T) {
	for stderr, want := range map[string]bool{
		"Error response from daemon: cannot kill container: hub: container 0123 is not running":      true,
		"Error: can only kill running containers. 0123 is in state exited: container state improper": true,
		"Error response from daemon: cannot kill container: hub: No such container: hub":             false,
		`Error: no container with name or ID "hub" found: no such container`:                         false,
		"Error response from daemon: cannot kill container: hub: invalid signal: BOGUS":              false,
	} {
		err := &cliError{args: []string{"kill"}, stderr: stderr}
		if got := IsContainerNotRunning(err); got != want {
			t.Errorf("IsContainerNotRunning(%q) = %v, want %v", stderr, got, want)
		}
	}
}
