// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hosthooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func scriptProvider(t *testing.T, id, phase, body string) Provider {
	t.Helper()
	dir := t.TempDir()
	data, _ := json.Marshal(Manifest{SchemaVersion: Version, Phases: map[string]string{phase: phase}})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, phase), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := Load(id, dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func responseScript(value string) string { return "cat >/dev/null\nprintf '%s' '" + value + "'" }
func TestPrepareProtocolAndAtomicity(t *testing.T) {
	cases := []struct {
		name, output string
		accept       bool
	}{
		{"valid", `{"schemaVersion":"1","hostEnvironment":{"set":{"NEW":"value"}}}`, true},
		{"unknown additive", `{"schemaVersion":"1","future":1,"hostEnvironment":{"set":{"NEW":"value"}}}`, true},
		{"trailing", `{"schemaVersion":"1","hostEnvironment":{"set":{"NEW":"value"}}}{}`, false},
		{"version", `{"schemaVersion":"2","hostEnvironment":{"set":{"NEW":"value"}}}`, false},
		{"illegal phase", `{"schemaVersion":"1","hostEnvironment":{"set":{"NEW":"value"}},"terminalEffects":[]}`, false},
		{"overlap", `{"schemaVersion":"1","hostEnvironment":{"set":{"NEW":"value"},"unset":["NEW"]}}`, false},
		{"reserved atomic", `{"schemaVersion":"1","hostEnvironment":{"set":{"NEW":"value","ENCLAVE_HOME":"bad"}}}`, false},
		{"null", `{"schemaVersion":"1","hostEnvironment":null}`, false},
		{"null env value", `{"schemaVersion":"1","hostEnvironment":{"set":{"NEW":null}}}`, false},
		{"invalid name", `{"schemaVersion":"1","hostEnvironment":{"set":{"NEW":"value","BAD-NAME":"bad"}}}`, false},
		{"NUL value", `{"schemaVersion":"1","hostEnvironment":{"set":{"NEW":"value","NUL":"bad\u0000value"}}}`, false},
		{"wrong type", `{"schemaVersion":"1","restartHostProcess":"yes"}`, false},
		{"restart noop", `{"schemaVersion":"1","restartHostProcess":true}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := scriptProvider(t, "user:test", Prepare, responseScript(tc.output))
			var warnings []string
			env, _ := (Runner{Warn: func(w string) { warnings = append(warnings, w) }}).Prepare(context.Background(), []Provider{p}, Request{}, []string{"OLD=old"})
			values := EnvironmentMap(env)
			if (values["NEW"] == "value") != tc.accept {
				t.Fatalf("env=%v warnings=%v", env, warnings)
			}
			if !tc.accept && len(warnings) != 1 {
				t.Fatalf("expected one warning: %v", warnings)
			}
		})
	}
}
func TestOrderedEnvironmentConflictAndEmptyOptOut(t *testing.T) {
	p1 := scriptProvider(t, "user:first", Prepare, responseScript(`{"schemaVersion":"1","hostEnvironment":{"set":{"NEW":"one"},"setIfAbsent":{"EMPTY":"filled"}},"restartHostProcess":true}`))
	p2 := scriptProvider(t, "user:second", Prepare, responseScript(`{"schemaVersion":"1","hostEnvironment":{"set":{"NEW":"two","OTHER":"partial"}}}`))
	p3 := scriptProvider(t, "user:third", Prepare, `cat >/dev/null
[ "$NEW" = one ] || exit 9
printf '%s' '{"schemaVersion":"1","hostEnvironment":{"setIfAbsent":{"NEW":"ignored"},"set":{"SAW":"one"}}}'`)
	var warnings []string
	env, restart := (Runner{Warn: func(w string) { warnings = append(warnings, w) }}).Prepare(context.Background(), []Provider{p1, p2, p3}, Request{}, []string{"EMPTY=", "DUP=first", "DUP=last"})
	m := EnvironmentMap(env)
	if !restart || m["NEW"] != "one" || m["EMPTY"] != "" || m["SAW"] != "one" || m["DUP"] != "last" {
		t.Fatalf("unexpected %v, restart=%v", env, restart)
	}
	if _, ok := m["OTHER"]; ok || len(warnings) != 1 {
		t.Fatalf("partial response accepted: %v %v", env, warnings)
	}
	if len(env) != len(m) {
		t.Fatalf("duplicate env keys: %v", env)
	}
}
func TestRunnerLimitsAndTimeout(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"output", `cat >/dev/null; head -c 70000 /dev/zero`},
		{"timeout", `cat >/dev/null; sleep 20`},
		{"retained pipe", `cat >/dev/null; sleep 20 &`},
		{"missing interpreter", `exit 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := scriptProvider(t, "user:test", Enter, tc.body)
			if tc.name == "missing interpreter" {
				if err := os.WriteFile(filepath.Join(p.Dir, Enter), []byte("#!/missing-interpreter\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			var warnings []string
			scope := (Runner{Warn: func(w string) { warnings = append(warnings, w) }}).Begin(context.Background(), []Provider{p}, Request{Terminal: Terminal{StdoutIsTTY: true}}, os.Environ(), &bytes.Buffer{})
			scope.Close(Outcome{Status: "failed"})
			if time.Since(start) > 2*time.Second || len(warnings) == 0 {
				t.Fatalf("not bounded: %v %v", time.Since(start), warnings)
			}
		})
	}
	p := scriptProvider(t, "user:test", Prepare, `exit 99`)
	_, err := (Runner{}).invoke(context.Background(), p, Request{Command: Command{Argv: []string{strings.Repeat("x", MaxMessage)}}}, Prepare, os.Environ())
	if err == nil {
		t.Fatal("oversized request accepted")
	}
}
func TestTerminalReverseCleanupDuplicateAndLeaveFailure(t *testing.T) {
	p1 := scriptProvider(t, "user:a", Enter, responseScript(`{"schemaVersion":"1","terminalEffects":[{"resource":"a","enter":"A","leave":"a"}],"state":{"token":1}}`))
	if err := os.WriteFile(filepath.Join(p1.Dir, Leave), []byte("#!/bin/sh\ncat >/dev/null; exit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p1.Phases[Leave] = Leave
	data, _ := json.Marshal(Manifest{SchemaVersion: Version, Phases: p1.Phases})
	if err := os.WriteFile(filepath.Join(p1.Dir, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	p2 := scriptProvider(t, "user:b", Enter, responseScript(`{"schemaVersion":"1","terminalEffects":[{"resource":"a","enter":"BAD","leave":"BAD"},{"resource":"b","enter":"B","leave":"b"}]}`))
	p3 := scriptProvider(t, "user:bad", Enter, "exit 4")
	var out bytes.Buffer
	var warnings []string
	scope := (Runner{Warn: func(w string) { warnings = append(warnings, w) }}).Begin(context.Background(), []Provider{p1, p2, p3}, Request{Terminal: Terminal{StdoutIsTTY: true}}, os.Environ(), &out)
	if out.String() != "AB" {
		t.Fatalf("enter %q", out.String())
	}
	scope.Close(Outcome{Status: "failed"})
	scope.Close(Outcome{})
	if out.String() != "ABba" || len(warnings) != 3 {
		t.Fatalf("cleanup %q warnings=%v", out.String(), warnings)
	}
}
func TestNonTTYNeverInvokesAndChildEnvironment(t *testing.T) {
	p := scriptProvider(t, "user:a", Enter, "touch sentinel")
	scope := (Runner{}).Begin(context.Background(), []Provider{p}, Request{}, os.Environ(), &bytes.Buffer{})
	scope.Close(Outcome{})
	if _, err := os.Stat(filepath.Join(p.Dir, "sentinel")); !os.IsNotExist(err) {
		t.Fatal("non-TTY invoked hook")
	}
	env := EnvironmentMap(ChildEnvironment([]string{ReplayEnv + "=stale", "NORMAL=yes", ChildEnv + "=old"}))
	if env[ChildEnv] != "1" || env["NORMAL"] != "yes" || env[ReplayEnv] != "" {
		t.Fatal(env)
	}
}
func TestDiscoveryValidationAndActivation(t *testing.T) {
	p := scriptProvider(t, "user:sample", Prepare, "exit 0")
	root := t.TempDir()
	dir := filepath.Join(root, "sample")
	if err := os.Rename(p.Dir, dir); err != nil {
		t.Fatal(err)
	}
	providers, warnings := Discover(Roots{User: root}, "claude")
	if len(providers) != 1 || len(warnings) != 0 {
		t.Fatalf("discovery %v %v", providers, warnings)
	}
	if got := Activate(providers, nil, func(string) {}); len(got) != 0 {
		t.Fatal("installation activated hook")
	}
	enabled := true
	order := 0
	selected := Activate(providers, ConfigMap{"user:sample": {Enabled: &enabled, Order: &order}}, func(string) {})
	if len(selected) != 1 {
		t.Fatal(selected)
	}
	for _, target := range []string{"/bin/sh", "../../../bin/sh", "missing", "loop"} {
		t.Run(target, func(t *testing.T) {
			handler := filepath.Join(dir, Prepare)
			_ = os.Remove(handler)
			if err := os.Symlink(target, handler); err != nil {
				t.Fatal(err)
			}
			if target == "loop" {
				_ = os.Symlink(Prepare, filepath.Join(dir, "loop"))
			}
			if _, err := Load("user:sample", dir); err == nil {
				t.Fatalf("accepted %s", target)
			}
		})
	}
}
func TestActivationOrdering(t *testing.T) {
	enabled := true
	order := 1
	ps := []Provider{{ID: "user:z"}, {ID: "user:b"}, {ID: "user:a"}}
	configs := ConfigMap{"user:z": {Enabled: &enabled, Order: &order}, "user:a": {Enabled: &enabled}, "user:b": {Enabled: &enabled}}
	got := Activate(ps, configs, func(string) {})
	ids := []string{}
	for _, p := range got {
		ids = append(ids, p.ID)
	}
	if !reflect.DeepEqual(ids, []string{"user:z", "user:a", "user:b"}) {
		t.Fatal(fmt.Sprint(ids))
	}
}

func TestLeaveReceivesStateAndOutcomeAfterCancelledRuntime(t *testing.T) {
	p := scriptProvider(t, "user:state", Enter, responseScript(`{"schemaVersion":"1","state":{"token":"opaque"},"terminalEffects":[{"resource":"a","enter":"A","leave":"a"}]}`))
	write := filepath.Join(p.Dir, Leave)
	if err := os.WriteFile(write, []byte("#!/bin/sh\ncat > leave-request.json\nprintf '%s' '{\"schemaVersion\":\"1\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p.Phases[Leave] = Leave
	data, _ := json.Marshal(Manifest{SchemaVersion: Version, Phases: p.Phases})
	if err := os.WriteFile(filepath.Join(p.Dir, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	scope := (Runner{}).Begin(ctx, []Provider{p}, Request{AttachmentID: "attachment", Terminal: Terminal{StdoutIsTTY: true}}, os.Environ(), &out)
	cancel()
	code := 17
	scope.Close(Outcome{Status: "failed", ExitCode: &code})
	requestData, err := os.ReadFile(filepath.Join(p.Dir, "leave-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request Request
	if err := json.Unmarshal(requestData, &request); err != nil {
		t.Fatal(err)
	}
	if request.Phase != Leave || request.AttachmentID != "attachment" || string(request.State) != `{"token":"opaque"}` || request.Outcome == nil || *request.Outcome.ExitCode != 17 || out.String() != "Aa" {
		t.Fatalf("request=%+v output=%q", request, out.String())
	}
}
