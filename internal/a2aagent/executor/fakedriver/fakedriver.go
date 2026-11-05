// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package fakedriver is an a2aagent.Driver for tests. The harness is the
// test binary itself, re-executed with EnvMode set; call Main first in TestMain.
package fakedriver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"enclave/internal/a2aagent"
)

// EnvMode selects the fake harness behaviour; EnvResume carries the resumed session ID.
const (
	EnvMode   = "ENCLAVE_FAKE_HARNESS_MODE"
	EnvResume = "ENCLAVE_FAKE_HARNESS_RESUME"
	EnvSize   = "ENCLAVE_FAKE_HARNESS_SIZE"
	// EnvNew carries Invocation.NewHarnessSessionID.
	EnvNew = "ENCLAVE_FAKE_HARNESS_NEW"
)

// Harness modes.
const (
	// ModeComplete answers "echo:" + the prompt and exits 0.
	ModeComplete = "complete"
	// ModeEnv answers with the sorted ENCLAVE_A2A_* variable names it sees.
	ModeEnv = "env"
	// ModeBig answers EnvSize bytes of "x".
	ModeBig = "big"
	// ModeFail emits a session and exits 3 without a final message.
	ModeFail = "fail"
	// ModeCrash exits 3 before emitting anything.
	ModeCrash = "crash"
	// ModeEmpty answers an empty final message.
	ModeEmpty = "empty"
	// ModeUnknownEvent emits an event type the driver does not know.
	ModeUnknownEvent = "unknown-event"
	// ModeSlow completes after 300 ms.
	ModeSlow = "slow"
	// ModeHang emits a session and runs until killed.
	ModeHang = "hang"
	// ModeIgnoreTerm is ModeHang ignoring SIGTERM, so only SIGKILL ends it.
	ModeIgnoreTerm = "ignore-term"
	// ModeChildThenExit leaves a child in its process group and completes.
	ModeChildThenExit = "child-then-exit"
	// ModeEscape starts a child in a new session (escaping the group) and hangs.
	ModeEscape = "escape"
	// ModeEscapeThenExit starts an escaping child and completes.
	ModeEscapeThenExit = "escape-then-exit"
	// ModeDoubleFork leaves an orphaned grandchild in a new session and hangs.
	ModeDoubleFork = "double-fork"
	// ModeDoubleForkThenExit leaves an orphaned grandchild in a new session and completes.
	ModeDoubleForkThenExit = "double-fork-then-exit"
	// ModeSleep only sleeps; used for children.
	ModeSleep = "sleep"
	// modeForkAndExit starts a sleeping child in a new session and exits at once.
	modeForkAndExit = "fork-and-exit"
)

// SessionID is the session the fake harness reports for a new context.
const SessionID = "fake-session-1"

// Driver runs Exe (normally os.Args[0]) in Mode.
type Driver struct {
	Exe  string
	Mode string
	// Env is appended to the invocation environment, e.g. EnvSize.
	Env []string
	// NewSession is what NewSessionID returns; empty lets the harness report SessionID.
	NewSession string
}

var _ a2aagent.Driver = (*Driver)(nil)

func (d *Driver) Name() string { return "fake" }

func (d *Driver) CheckVersion(context.Context) error { return nil }

func (d *Driver) NewSessionID() (string, error) { return d.NewSession, nil }

func (d *Driver) Command(_ context.Context, inv a2aagent.Invocation) (*exec.Cmd, error) {
	cmd := exec.Command(d.Exe) // #nosec G204 -- test helper binary.
	cmd.Env = append(append(append([]string{}, inv.Env...), d.Env...), EnvMode+"="+d.Mode, EnvResume+"="+inv.HarnessSessionID, EnvNew+"="+inv.NewHarnessSessionID)
	cmd.Dir = inv.WorkDir
	return cmd, nil
}

type event struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId,omitempty"`
	Text      string `json:"text,omitempty"`
}

// Decode reads JSON lines; an unknown event type is an error, as for real drivers.
func (d *Driver) Decode(r io.Reader, emit func(a2aagent.HarnessEvent)) (a2aagent.Outcome, error) {
	var out a2aagent.Outcome
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var ev event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return out, err
		}
		switch ev.Type {
		case "session":
			out.HarnessSessionID = ev.SessionID
			out.Transcripts = []string{"projects/fake/" + ev.SessionID + ".jsonl"}
			emit(a2aagent.HarnessEvent{Kind: a2aagent.EventSession, HarnessSessionID: ev.SessionID})
		case "progress":
			emit(a2aagent.HarnessEvent{Kind: a2aagent.EventProgress, Text: ev.Text})
		case "final":
			out.FinalMessage = ev.Text
			emit(a2aagent.HarnessEvent{Kind: a2aagent.EventFinal, Text: ev.Text})
		default:
			return out, fmt.Errorf("fake harness: unknown event %q", ev.Type)
		}
	}
	return out, sc.Err()
}

// Main runs the fake harness and exits when EnvMode is set; otherwise it returns.
func Main() {
	mode := os.Getenv(EnvMode)
	if mode == "" {
		return
	}
	os.Exit(harness(mode))
}

func harness(mode string) int {
	switch mode {
	case ModeSleep:
		sleepForever()
	case modeForkAndExit:
		if err := startChild(ModeSleep, true); err != nil {
			return 4
		}
		return 0
	}
	if mode == ModeCrash {
		return 3
	}
	if mode == ModeIgnoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	prompt, _ := io.ReadAll(os.Stdin)
	session := os.Getenv(EnvResume)
	if session == "" {
		session = os.Getenv(EnvNew)
	}
	if session == "" {
		session = SessionID
	}
	out := json.NewEncoder(os.Stdout)
	_ = out.Encode(event{Type: "session", SessionID: session})
	_ = out.Encode(event{Type: "progress", Text: "working"})
	switch mode {
	case ModeComplete:
		_ = out.Encode(event{Type: "final", Text: "echo:" + string(prompt)})
	case ModeEmpty:
		_ = out.Encode(event{Type: "final"})
	case ModeEnv:
		var names []string
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "ENCLAVE_A2A_") {
				names = append(names, strings.SplitN(kv, "=", 2)[0])
			}
		}
		sort.Strings(names)
		_ = out.Encode(event{Type: "final", Text: "env:" + strings.Join(names, ",")})
	case ModeBig:
		var n int
		_, _ = fmt.Sscan(os.Getenv(EnvSize), &n)
		_ = out.Encode(event{Type: "final", Text: strings.Repeat("x", n)})
	case ModeFail:
		return 3
	case ModeUnknownEvent:
		_ = out.Encode(event{Type: "mystery"})
	case ModeSlow:
		time.Sleep(300 * time.Millisecond)
		_ = out.Encode(event{Type: "final", Text: "slow done"})
	case ModeHang, ModeIgnoreTerm:
		sleepForever()
	case ModeChildThenExit:
		if err := startChild(ModeSleep, false); err != nil {
			return 4
		}
		_ = out.Encode(event{Type: "final", Text: "left a child"})
	case ModeEscape, ModeDoubleFork:
		if err := startChild(childMode(mode), true); err != nil {
			return 4
		}
		sleepForever()
	case ModeEscapeThenExit, ModeDoubleForkThenExit:
		if err := startChild(childMode(mode), true); err != nil {
			return 4
		}
		_ = out.Encode(event{Type: "final", Text: "left an escapee"})
	default:
		return 2
	}
	return 0
}

func childMode(mode string) string {
	if mode == ModeDoubleFork || mode == ModeDoubleForkThenExit {
		return modeForkAndExit
	}
	return ModeSleep
}

// startChild starts a copy that inherits stdout, so its tree also holds the event pipe open.
func startChild(mode string, newSession bool) error {
	cmd := exec.Command(os.Args[0]) // #nosec G204 G702 -- re-executes the test binary.
	cmd.Env = append(os.Environ(), EnvMode+"="+mode)
	cmd.Stdout = os.Stdout
	if newSession {
		setsid(cmd)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if mode == modeForkAndExit {
		// Wait so the grandchild is orphaned before the harness reports progress.
		return cmd.Wait()
	}
	return nil
}

func sleepForever() {
	for {
		time.Sleep(time.Hour)
	}
}
