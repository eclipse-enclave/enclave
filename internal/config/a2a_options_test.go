// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"enclave/internal/model"
	"enclave/internal/peergroup"
)

func TestA2AOptionDefaults(t *testing.T) {
	opts := DefaultOptions().A2AOptions
	if err := ValidateA2AOptions(opts); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
	durations := map[string]time.Duration{
		opts.A2AContextTTL:          24 * time.Hour,
		opts.A2ATaskDeadline:        30 * time.Minute,
		opts.A2ASendWait:            90 * time.Second,
		opts.A2AHostSendWait:        10 * time.Minute,
		opts.A2ARecordMaxAge:        7 * 24 * time.Hour,
		opts.A2AReconnectWindow:     60 * time.Second,
		opts.A2ACommitTimeout:       5 * time.Second,
		opts.A2ADepartureAckTimeout: 30 * time.Second,
		opts.A2ACredentialLifetime:  15 * time.Minute,
		opts.A2AStatusStaleness:     5 * time.Minute,
		opts.A2ATerminationTimeout:  10 * time.Second,
	}
	for value, want := range durations {
		if got, err := ParseA2ADuration(value); err != nil || got != want {
			t.Errorf("ParseA2ADuration(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	counts := map[string]int{opts.A2AQueueDepth: 8, opts.A2AHubRetainedResults: 100, opts.A2ARecordLimit: 1000}
	for value, want := range counts {
		if got, err := ParseA2ACount(value); err != nil || got != want {
			t.Errorf("ParseA2ACount(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	if got, _ := ParseA2ASize(opts.A2AMaxRequestSize); got != 1<<20 {
		t.Errorf("request size = %d", got)
	}
	if got, _ := ParseA2ASize(opts.A2AMaxResultSize); got != 256<<10 {
		t.Errorf("result size = %d", got)
	}
	if opts.PeerGroup != "" || opts.A2ARole != A2ARoleWorker || opts.A2ALog != A2ALogCoarse {
		t.Errorf("group %q role %q log %q", opts.PeerGroup, opts.A2ARole, opts.A2ALog)
	}
}

func TestA2ASessionEffects(t *testing.T) {
	unaffected := map[string]bool{
		"a2a_log":                   true,
		"a2a_host_send_wait":        true,
		"a2a_hub_retained_results":  true,
		"a2a_commit_timeout":        true,
		"a2a_departure_ack_timeout": true,
		"a2a_credential_lifetime":   true,
		"a2a_status_staleness":      true,
	}
	seen := 0
	for _, def := range OptionDefs() {
		if def.Name != "peer_group" && !strings.HasPrefix(def.Name, "a2a_") {
			continue
		}
		seen++
		want := SessionAffected
		if unaffected[def.Name] {
			want = SessionUnaffected
		}
		if def.AffectsSession != want {
			t.Errorf("%s AffectsSession = %q, want %q", def.Name, def.AffectsSession, want)
		}
	}
	if seen != len(a2aOptions)+1 {
		t.Errorf("found %d A2A options, want %d", seen, len(a2aOptions)+1)
	}
}

func TestParseA2ASize(t *testing.T) {
	valid := map[string]int64{"1": 1, "4096": 4096, "64KiB": 64 << 10, " 2MiB ": 2 << 20}
	for in, want := range valid {
		if got, err := ParseA2ASize(in); err != nil || got != want {
			t.Errorf("ParseA2ASize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-1", "1MB", "1.5MiB", "KiB", "1 MiB", "9223372036854775807MiB"} {
		if _, err := ParseA2ASize(in); err == nil {
			t.Errorf("ParseA2ASize(%q) succeeded", in)
		}
	}
}

func TestParseA2ADurationAndCount(t *testing.T) {
	for _, in := range []string{"", "0s", "-5s", "7d", "90"} {
		if _, err := ParseA2ADuration(in); err == nil {
			t.Errorf("ParseA2ADuration(%q) succeeded", in)
		}
	}
	for _, in := range []string{"", "0", "-1", "1.5", "8k"} {
		if _, err := ParseA2ACount(in); err == nil {
			t.Errorf("ParseA2ACount(%q) succeeded", in)
		}
	}
}

func TestA2AFlags(t *testing.T) {
	flags := CLIFlagIndex()
	valid := []struct {
		flag, value string
		get         func(model.Options) string
		want        string
	}{
		{"--peer-group", "review-1", func(o model.Options) string { return o.PeerGroup }, "review-1"},
		{"--role", "Orchestrator", func(o model.Options) string { return o.A2ARole }, A2ARoleOrchestrator},
		{"--a2a-description", "Reviews Go code", func(o model.Options) string { return o.A2ADescription }, "Reviews Go code"},
		{"--a2a-log", "FULL", func(o model.Options) string { return o.A2ALog }, A2ALogFull},
		{"--a2a-context-ttl", "2h", func(o model.Options) string { return o.A2AContextTTL }, "2h"},
	}
	for _, tt := range valid {
		flag, ok := flags[tt.flag]
		if !ok {
			t.Fatalf("flag %s not registered", tt.flag)
		}
		opts, sources := DefaultOptions(), model.DefaultOptionSources()
		if err := flag.Apply(&opts, &sources, tt.value); err != nil {
			t.Fatalf("%s %q: %v", tt.flag, tt.value, err)
		}
		if got := tt.get(opts); got != tt.want {
			t.Errorf("%s %q set %q, want %q", tt.flag, tt.value, got, tt.want)
		}
	}
	if sources := model.DefaultOptionSources(); sources.A2ARole != model.SourceDefault {
		t.Fatalf("default a2a_role source = %v", sources.A2ARole)
	}
	opts, sources := DefaultOptions(), model.DefaultOptionSources()
	if err := flags["--role"].Apply(&opts, &sources, "worker"); err != nil || sources.A2ARole != model.SourceCLI {
		t.Fatalf("--role source = %v, err %v", sources.A2ARole, err)
	}

	invalid := map[string][]string{
		"--peer-group":      {"", "Review", "-x", "a_b", strings.Repeat("a", 33), "_host"},
		"--role":            {"", "host", "lead"},
		"--a2a-log":         {"", "requests"},
		"--a2a-context-ttl": {"", "1d", "0s"},
	}
	for name, values := range invalid {
		for _, value := range values {
			opts, sources := DefaultOptions(), model.DefaultOptionSources()
			if err := flags[name].Apply(&opts, &sources, value); err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s %q: err = %v, want an error naming the flag", name, value, err)
			}
		}
	}
	for _, name := range []string{"--a2a-queue-depth", "--a2a-send-wait", "--a2a-host-send-wait", "--a2a-credential-lifetime"} {
		if _, ok := flags[name]; ok {
			t.Errorf("bound option %s has a flag; bounds are config-only in v1", name)
		}
	}
}

func TestValidateA2AOptions(t *testing.T) {
	opts := DefaultOptions().A2AOptions
	opts.PeerGroup = "Bad"
	opts.A2AQueueDepth = "0"
	opts.A2AMaxResultSize = "1MB"
	err := ValidateA2AOptions(opts)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"peer_group", "a2a_queue_depth", "a2a_max_result_size"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}

	opts = DefaultOptions().A2AOptions
	opts.A2ASendWait = ""
	if err := ValidateA2AOptions(opts); err == nil || !strings.Contains(err.Error(), "a2a_send_wait") {
		t.Errorf("empty bound: err = %v", err)
	}

	opts = DefaultOptions().A2AOptions
	opts.A2AHostSendWait = "14m1s"
	if err := ValidateA2AOptions(opts); err == nil || !strings.Contains(err.Error(), "a2a_credential_lifetime") {
		t.Errorf("host wait above the credential cap: err = %v", err)
	}
	opts.A2AHostSendWait = "14m"
	if err := ValidateA2AOptions(opts); err != nil {
		t.Errorf("host wait at the cap: %v", err)
	}
}

func TestA2ADurationMinimums(t *testing.T) {
	want := map[string]time.Duration{
		"a2a_context_ttl":           time.Minute,
		"a2a_task_deadline":         10 * time.Second,
		"a2a_send_wait":             time.Second,
		"a2a_host_send_wait":        time.Second,
		"a2a_record_max_age":        time.Minute,
		"a2a_reconnect_window":      5 * time.Second,
		"a2a_commit_timeout":        time.Second,
		"a2a_departure_ack_timeout": time.Second,
		"a2a_credential_lifetime":   2 * time.Minute,
		"a2a_status_staleness":      10 * time.Second,
		"a2a_termination_timeout":   time.Second,
	}
	for _, option := range a2aOptions {
		if option.kind != a2aDuration {
			continue
		}
		minimum, ok := want[option.name]
		if !ok || option.min != minimum {
			t.Errorf("%s minimum = %s, want %s", option.name, option.min, minimum)
			continue
		}
		set := func(value string) error {
			opts := DefaultOptions().A2AOptions
			opts.A2AHostSendWait = "1s" // keeps the credential lifetime cross-check out of the way
			reflect.ValueOf(&opts).Elem().FieldByName(option.field).SetString(value)
			return ValidateA2AOptions(opts)
		}
		if err := set((minimum - time.Microsecond).String()); err == nil || !strings.Contains(err.Error(), option.name) || !strings.Contains(err.Error(), "minimum") {
			t.Errorf("%s below the minimum: err = %v", option.name, err)
		}
		if err := set(minimum.String()); err != nil {
			t.Errorf("%s at the minimum: %v", option.name, err)
		}
	}
	opts := DefaultOptions().A2AOptions
	opts.A2ATerminationTimeout = "1us"
	if err := ValidateA2AOptions(opts); err == nil {
		t.Error("a2a_termination_timeout 1us accepted")
	}
	full, sources := DefaultOptions(), model.DefaultOptionSources()
	if err := CLIFlagIndex()["--a2a-context-ttl"].Apply(&full, &sources, "30s"); err == nil {
		t.Error("--a2a-context-ttl 30s accepted")
	}
}

func TestA2AConfigValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"peer_group":"review","a2a_role":"Orchestrator","a2a_queue_depth":"4","a2a_send_wait":"soon","a2a_max_request_size":"2MiB",` +
		`"tool_overrides":{"codex":{"a2a_log":"verbose","a2a_task_deadline":"1h"}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	defaults, warnings, err := readDefaults(path)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.PeerGroup != "review" || defaults.A2ARole != A2ARoleOrchestrator || defaults.A2AQueueDepth != "4" || defaults.A2AMaxRequestSize != "2MiB" {
		t.Fatalf("defaults = %+v", defaults)
	}
	if defaults.A2ASendWait != "" || defaults.ToolOverrides["codex"].A2ALog != "" || defaults.ToolOverrides["codex"].A2ATaskDeadline != "1h" {
		t.Fatalf("invalid values kept: send wait %q, codex log %q", defaults.A2ASendWait, defaults.ToolOverrides["codex"].A2ALog)
	}
	if len(warnings) != 2 || warnings[0].Option != "a2a_send_wait" || warnings[1].Option != "a2a_log" || warnings[1].Layer != "tool_override:codex" {
		t.Fatalf("warnings = %+v", warnings)
	}

	opts, _, _ := ResolveOptionsForTool(DefaultOptions(), model.DefaultOptionSources(), Defaults{}, defaults, "codex")
	if opts.A2AQueueDepth != "4" || opts.Sources.A2AQueueDepth != model.SourceProject {
		t.Errorf("queue depth = %q from %v", opts.A2AQueueDepth, opts.Sources.A2AQueueDepth)
	}
	if opts.A2ASendWait != "90s" || opts.Sources.A2ASendWait != model.SourceDefault {
		t.Errorf("send wait = %q from %v, want the default", opts.A2ASendWait, opts.Sources.A2ASendWait)
	}
	if opts.A2ATaskDeadline != "1h" || opts.Sources.A2ATaskDeadline != model.SourceToolOverride {
		t.Errorf("task deadline = %q from %v", opts.A2ATaskDeadline, opts.Sources.A2ATaskDeadline)
	}
}

func TestA2AGroupNamePatternMatchesPeergroup(t *testing.T) {
	if a2aGroupNamePattern.String() != peergroup.GroupNamePattern.String() {
		t.Fatalf("pattern %q differs from peergroup's %q", a2aGroupNamePattern, peergroup.GroupNamePattern)
	}
}
