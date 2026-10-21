// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/netcapture"
	"enclave/internal/netlog"
	"enclave/internal/netlogview"
)

func writeNetworkLogFixture(t *testing.T, path string, lines ...string) {
	t.Helper()
	payload := ""
	for _, line := range lines {
		payload += line + "\n"
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestReadNetworkLogEventsReadsTheRotatedGenerationFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.log")
	writeNetworkLogFixture(t, netlog.RotatedPath(path),
		`{"ts":"2026-08-13T11:00:00Z","type":"tcp","verdict":"pass","domain":"old.example"}`)
	writeNetworkLogFixture(t, path,
		`{"ts":"2026-08-13T12:00:00Z","type":"tcp","verdict":"pass","domain":"new.example"}`,
		"Aug 13 12:04:35 dnsmasq[12]: config telemetry.example.com is NXDOMAIN")

	read, err := readNetworkLogEvents([]string{path})
	if err != nil {
		t.Fatalf("readNetworkLogEvents() error = %v", err)
	}
	if read.skipped != 1 {
		t.Fatalf("skipped = %d, want 1", read.skipped)
	}
	if len(read.events) != 2 || read.events[0].Domain != "old.example" || read.events[1].Domain != "new.example" {
		t.Fatalf("events = %+v", read.events)
	}
}

func TestReadNetworkLogEventsReportsTheLiveFollowOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.log")
	writeNetworkLogFixture(t, netlog.RotatedPath(path),
		`{"ts":"2026-08-13T11:00:00Z","type":"tcp","verdict":"pass","domain":"old.example"}`)
	writeNetworkLogFixture(t, path,
		`{"ts":"2026-08-13T12:00:00Z","type":"tcp","verdict":"pass","domain":"new.example"}`)

	read, err := readNetworkLogEvents([]string{path})
	if err != nil {
		t.Fatalf("readNetworkLogEvents() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	// --follow has to resume exactly where the backlog read stopped, and only
	// the live log is followed.
	if read.offsets[path] != info.Size() {
		t.Fatalf("offset = %d, want %d", read.offsets[path], info.Size())
	}
	if _, ok := read.offsets[netlog.RotatedPath(path)]; ok {
		t.Fatal("the rotated generation should not be followed")
	}
}

// A writer caught mid-append leaves a line with no newline. --follow has to
// resume before it, so the completed line is read whole rather than as a tail.
func TestReadNetworkLogEventsRewindsOverAnUnfinishedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	complete := `{"ts":"2026-08-13T12:00:00Z","type":"tcp","verdict":"pass","domain":"done.example"}` + "\n"
	torn := `{"ts":"2026-08-13T12:00:01Z","type":"tcp",`
	if err := os.WriteFile(path, []byte(complete+torn), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	read, err := readNetworkLogEvents([]string{path})
	if err != nil {
		t.Fatalf("readNetworkLogEvents() error = %v", err)
	}
	if read.offsets[path] != int64(len(complete)) {
		t.Fatalf("offset = %d, want %d (the start of the unfinished line)", read.offsets[path], len(complete))
	}
}

func TestNetworkLogPathsCollapsesSharedLogs(t *testing.T) {
	paths := networkLogPaths(t.TempDir(), []backend.GatewayInfo{
		{Name: "enclave-demo-claude-gateway", SessionContainer: "enclave-demo-claude", ProjectHash: "abc", Tool: "claude"},
		{Name: "enclave-demo-claude-2-gateway", SessionContainer: "enclave-demo-claude-2", ProjectHash: "abc", Tool: "claude"},
		{Name: "enclave-demo-codex-gateway", SessionContainer: "enclave-demo-codex", ProjectHash: "abc", Tool: "codex"},
	})
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Fatalf("networkLogPaths() = %v, want one path per project/tool", paths)
	}
}

func TestReadNetworkLogEventsMergesTargetsInTimeOrder(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.log")
	second := filepath.Join(dir, "b.log")
	writeNetworkLogFixture(t, first,
		`{"ts":"2026-08-13T12:00:00Z","type":"tcp","verdict":"pass","domain":"a1.example"}`,
		`{"ts":"2026-08-13T12:00:04Z","type":"tcp","verdict":"pass","domain":"a2.example"}`)
	writeNetworkLogFixture(t, second,
		`{"ts":"2026-08-13T12:00:02Z","type":"tcp","verdict":"pass","domain":"b1.example"}`)

	read, err := readNetworkLogEvents([]string{first, second})
	if err != nil {
		t.Fatalf("readNetworkLogEvents() error = %v", err)
	}
	want := []string{"a1.example", "b1.example", "a2.example"}
	if len(read.events) != len(want) {
		t.Fatalf("events = %+v", read.events)
	}
	for i, event := range read.events {
		if event.Domain != want[i] {
			t.Fatalf("event %d = %q, want %q", i, event.Domain, want[i])
		}
	}
}

// An unparseable timestamp must not make the merge comparison intransitive,
// which would scramble the order of the events that do parse.
func TestReadNetworkLogEventsSortsUnparseableTimestampsLast(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.log")
	second := filepath.Join(dir, "b.log")
	writeNetworkLogFixture(t, first,
		`{"ts":"2026-08-13T12:00:04Z","type":"tcp","verdict":"pass","domain":"late.example"}`,
		`{"ts":"garbage","type":"tcp","verdict":"pass","domain":"broken.example"}`)
	writeNetworkLogFixture(t, second,
		`{"ts":"2026-08-13T12:00:00Z","type":"tcp","verdict":"pass","domain":"early.example"}`)

	read, err := readNetworkLogEvents([]string{first, second})
	if err != nil {
		t.Fatalf("readNetworkLogEvents() error = %v", err)
	}
	want := []string{"early.example", "late.example", "broken.example"}
	for i, event := range read.events {
		if i >= len(want) || event.Domain != want[i] {
			t.Fatalf("events = %+v, want %v", read.events, want)
		}
	}
}

func TestReadNetworkLogEventsMissingFile(t *testing.T) {
	read, err := readNetworkLogEvents([]string{filepath.Join(t.TempDir(), "network.log")})
	if err != nil || len(read.events) != 0 || read.skipped != 0 {
		t.Fatalf("got %+v, %d, %v; want an empty read", read.events, read.skipped, err)
	}
}

func TestApplyNetworkLogSince(t *testing.T) {
	events := []netlog.Event{
		{Timestamp: "2026-08-13T11:00:00Z", Type: netlog.TypeSession, Verdict: netlog.VerdictInfo, Rule: netlog.RuleSessionStart, Session: "old"},
		{Timestamp: "2026-08-13T11:30:00Z", Type: netlog.TypeTCP, Verdict: netlog.VerdictPass, Domain: "a.example"},
		{Timestamp: "2026-08-13T12:00:00Z", Type: netlog.TypeSession, Verdict: netlog.VerdictInfo, Rule: netlog.RuleSessionStart, Session: "current"},
		{Timestamp: "2026-08-13T12:30:00Z", Type: netlog.TypeTCP, Verdict: netlog.VerdictPass, Domain: "b.example"},
	}

	t.Run("empty", func(t *testing.T) {
		filter, err := applyNetworkLogSince(netlogview.Filter{}, "", events)
		if err != nil || !filter.Since.IsZero() {
			t.Fatalf("got %v, %v; want the zero time", filter.Since, err)
		}
	})

	t.Run("session resolves to the latest marker and scopes to it", func(t *testing.T) {
		filter, err := applyNetworkLogSince(netlogview.Filter{}, "session", events)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		want := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
		if !filter.Since.Equal(want) {
			t.Fatalf("since = %v, want %v", filter.Since, want)
		}
		// The file holds concurrent sessions, so anchoring on a boundary has to
		// scope to that boundary's session as well.
		if filter.Session != "current" {
			t.Fatalf("session = %q, want current", filter.Session)
		}
	})

	// An already scoped read anchors on its own session's marker, not on
	// whichever session started last in the shared log.
	t.Run("session anchors on the named session", func(t *testing.T) {
		filter, err := applyNetworkLogSince(netlogview.Filter{Session: "old"}, "session", events)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		want := time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC)
		if !filter.Since.Equal(want) || filter.Session != "old" {
			t.Fatalf("filter = %+v, want since %v and session old", filter, want)
		}
	})

	t.Run("session without a marker", func(t *testing.T) {
		if _, err := applyNetworkLogSince(netlogview.Filter{}, "session", events[1:2]); err == nil {
			t.Fatal("expected an error when the log predates session markers")
		}
	})

	t.Run("duration", func(t *testing.T) {
		before := time.Now().Add(-10 * time.Minute)
		filter, err := applyNetworkLogSince(netlogview.Filter{}, "10m", nil)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if filter.Since.Before(before.Add(-time.Minute)) || filter.Since.After(time.Now()) {
			t.Fatalf("since = %v, want roughly ten minutes ago", filter.Since)
		}
		if filter.Session != "" {
			t.Fatalf("session = %q, want a clock bound to leave the scope alone", filter.Session)
		}
	})

	t.Run("rfc3339", func(t *testing.T) {
		filter, err := applyNetworkLogSince(netlogview.Filter{}, "2026-08-13T12:00:00Z", nil)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if !filter.Since.Equal(time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)) {
			t.Fatalf("since = %v", filter.Since)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		for _, value := range []string{"yesterday", "-5m", "10x"} {
			if _, err := applyNetworkLogSince(netlogview.Filter{}, value, nil); err == nil {
				t.Fatalf("applyNetworkLogSince(%q) was accepted", value)
			}
		}
	})
}

// With no Docker to ask, --session falls back to this project's log plus the
// session stamp its events carry. Passing the gateway container name has to
// resolve to the same stamp.
func TestResolveNetworkLogScopeFallsBackWithoutDocker(t *testing.T) {
	stubDockerPing(t, fmt.Errorf("docker is not running"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home, err := config.ResolveHostHome()
	if err != nil {
		t.Fatalf("ResolveHostHome() error = %v", err)
	}

	for _, name := range []string{"enclave-demo-claude", "enclave-demo-claude-gateway"} {
		input := &CommandInput{
			Ctx:            &AppContext{project: model.Project{Hash: "abc"}, projectResolved: true},
			Options:        model.Options{RunOptions: model.RunOptions{Tool: "claude"}},
			NetworkLogView: model.NetworkLogView{Session: name},
		}
		scope, err := resolveNetworkLogScope(input)
		if err != nil {
			t.Fatalf("resolveNetworkLogScope(%q) error = %v", name, err)
		}
		want := config.HostProjectNetworkLogPath(home, "abc", "claude")
		if len(scope.paths) != 1 || scope.paths[0] != want {
			t.Fatalf("paths = %v, want [%s]", scope.paths, want)
		}
		wantRoot := config.HostProjectNetworkCaptureDir(home, "abc", "claude")
		if len(scope.captureRoots) != 1 || scope.captureRoots[0] != wantRoot {
			t.Fatalf("capture roots = %v, want [%s]", scope.captureRoots, wantRoot)
		}
		if scope.session != "enclave-demo-claude" {
			t.Fatalf("session = %q, want enclave-demo-claude", scope.session)
		}
		// The label names the log that was read, since a session of another tool
		// is not in this file.
		if !strings.Contains(scope.label, currentProjectToolLabel("abc", "claude")) {
			t.Fatalf("label = %q, want it to name the log the session was looked for in", scope.label)
		}
	}
}

// A name no event carries is not a session of this log, and must not read as a
// session that simply recorded nothing.
func TestSessionRecorded(t *testing.T) {
	events := []netlog.Event{
		{Timestamp: "2026-08-13T12:00:00Z", Type: netlog.TypeSession, Verdict: netlog.VerdictInfo, Rule: netlog.RuleSessionStart, Session: "enclave-demo-claude"},
	}
	if !sessionRecorded(events, "enclave-demo-claude") {
		t.Fatal("the session's own start marker was not recognised")
	}
	if sessionRecorded(events, "enclave-demo-codex") {
		t.Fatal("an unknown session name matched")
	}
}

func TestFilterGatewaysBySession(t *testing.T) {
	gateways := []backend.GatewayInfo{
		{Name: "enclave-demo-claude-gateway", SessionContainer: "enclave-demo-claude", Tool: "claude", ProjectHash: "abc"},
		{Name: "enclave-other-codex-gateway", SessionContainer: "enclave-other-codex", Tool: "codex", ProjectHash: "def"},
	}

	for _, name := range []string{"enclave-demo-claude", "enclave-demo-claude-gateway"} {
		matched := filterGatewaysBySession(gateways, name)
		if len(matched) != 1 || matched[0].Tool != "claude" {
			t.Fatalf("filterGatewaysBySession(%q) = %+v", name, matched)
		}
	}
	if matched := filterGatewaysBySession(gateways, "missing"); len(matched) != 0 {
		t.Fatalf("filterGatewaysBySession(missing) = %+v", matched)
	}
}

// writeTestCapture records one exchange in a session directory below root, the
// way the gateway does, and returns its ID.
func writeTestCapture(t *testing.T, root string, session string) string {
	t.Helper()
	dir, err := netcapture.PrepareSessionDir(root, session)
	if err != nil {
		t.Fatalf("PrepareSessionDir() error = %v", err)
	}
	store, err := netcapture.Open(netcapture.Options{Dir: dir, Session: session})
	if err != nil {
		t.Fatalf("netcapture.Open() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://api.example.com/v1/items?draft=1", strings.NewReader(`{"name":"report"}`))
	req.Header.Set("Content-Type", "application/json")
	capture := store.Begin(req, "https")
	_, _ = io.Copy(io.Discard, capture.RequestBody(req.Body))
	capture.Response(&http.Response{StatusCode: http.StatusCreated, Proto: "HTTP/1.1", Header: http.Header{"Content-Type": {"application/json"}}})
	_, _ = io.WriteString(capture.ResponseBody(), `{"id":7}`)
	id := capture.Finish(nil)
	if id == "" {
		t.Fatal("capture was not written")
	}
	return id
}

func TestShowNetworkCapturePrintsBothMessages(t *testing.T) {
	root := t.TempDir()
	id := writeTestCapture(t, root, "enclave-demo-claude-1")
	scope := networkLogScope{captureRoots: []string{root}, label: "test"}

	var code int
	out := captureStdout(t, func() { code = showNetworkCapture(scope, id, false) })
	if code != 0 {
		t.Fatalf("showNetworkCapture() = %d", code)
	}
	for _, want := range []string{
		"POST https://api.example.com/v1/items?draft=1",
		"Content-Type: application/json",
		`{"name":"report"}`,
		"HTTP/1.1 201 Created",
		`{"id":7}`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestShowNetworkCaptureJSONEmitsTheRecord(t *testing.T) {
	root := t.TempDir()
	id := writeTestCapture(t, root, "enclave-demo-claude-1")
	scope := networkLogScope{captureRoots: []string{root}, label: "test", session: "enclave-demo-claude-1"}

	var code int
	out := captureStdout(t, func() { code = showNetworkCapture(scope, id, true) })
	if code != 0 {
		t.Fatalf("showNetworkCapture() = %d", code)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("JSON output spans several lines:\n%s", out)
	}
	var record netcapture.Record
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if record.ID != id || record.Kind != netcapture.Kind || record.SchemaVersion != netcapture.SchemaVersion {
		t.Fatalf("record = %+v", record)
	}
}

func TestShowNetworkCaptureReportsUnknownIDs(t *testing.T) {
	root := t.TempDir()
	writeTestCapture(t, root, "enclave-demo-claude-1")
	scope := networkLogScope{captureRoots: []string{root}, label: "test"}
	for _, id := range []string{"ffffffffffffffff", "../escape"} {
		var code int
		_, stderr := captureOutput(t, func() { code = showNetworkCapture(scope, id, false) })
		if code == 0 {
			t.Fatalf("showNetworkCapture(%q) succeeded", id)
		}
		if strings.TrimSpace(stderr) == "" {
			t.Fatalf("showNetworkCapture(%q) failed silently", id)
		}
	}
}

// A compromised gateway could plant a link to a host file in its session
// directory; showing the capture must not print that file.
func TestShowNetworkCaptureDoesNotFollowPlantedLinks(t *testing.T) {
	root := t.TempDir()
	id := writeTestCapture(t, root, "enclave-demo-claude-1")
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY MATERIAL"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	body := filepath.Join(root, "enclave-demo-claude-1", id+".response.body")
	if err := os.Remove(body); err != nil {
		t.Fatalf("remove body: %v", err)
	}
	if err := os.Symlink(secret, body); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	scope := networkLogScope{captureRoots: []string{root}, label: "test"}

	var code int
	out := captureStdout(t, func() { code = showNetworkCapture(scope, id, false) })
	if code != 0 {
		t.Fatalf("showNetworkCapture() = %d", code)
	}
	if strings.Contains(out, "PRIVATE KEY MATERIAL") {
		t.Fatalf("output contains the linked file:\n%s", out)
	}
	if !strings.Contains(out, "response body not shown") {
		t.Fatalf("output does not say the body was refused:\n%s", out)
	}
}
