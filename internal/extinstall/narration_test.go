// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSectionSeparatesEveryExtension is the reason sections exist: a run that
// touches several extensions has to show where one ends and the next begins.
func TestSectionSeparatesEveryExtension(t *testing.T) {
	var out bytes.Buffer
	env := Env{Narration: &out}

	env.section(env.Style, "dsh", "tool @ 8f0bbc7")
	env.outcome(env.Style, markOK, "", "installed at /somewhere/dsh")
	env.section(env.Style, "openclaw", "tool @ 8f0bbc7")
	env.outcome(env.Style, markOK, "", "installed at /somewhere/openclaw")

	lines := strings.Split(out.String(), "\n")
	rules := 0
	for i, line := range lines {
		if !strings.HasPrefix(line, ruleGlyph) {
			continue
		}
		rules++
		if i == 0 || lines[i-1] != "" {
			t.Errorf("rule %q is not preceded by a blank line", line)
		}
	}
	if rules != 2 {
		t.Fatalf("rendered %d rules, want one per extension:\n%s", rules, out.String())
	}
	for _, want := range []string{"dsh", "openclaw", "tool @ 8f0bbc7"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("section output is missing %q:\n%s", want, out.String())
		}
	}
}

// TestAddSeparatesTheBlocksOfEveryExtension is the end-to-end guard: whatever
// each extension has to say, an --all run keeps one extension's output from
// running into the next one's.
func TestAddSeparatesTheBlocksOfEveryExtension(t *testing.T) {
	files := fooRepoFiles()
	files["extensions/features/bar/spec.yaml"] = "schemaVersion: \"1\"\nkind: mixin\nname: bar\n"
	fetcher := newFakeFetcher(t, "a1b2c3d4", files)
	env, out := testEnv(t, fetcher, "")

	req := addRequest()
	req.All = true
	results, err := Add(context.Background(), env, req)
	if err != nil {
		t.Fatalf("Add --all: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %s, want two", fmtResults(results))
	}

	rendered := out.String()
	for _, name := range []string{"foo", "bar"} {
		if !strings.Contains(rendered, ruleGlyph+" "+name+" ") {
			t.Errorf("no section rule for %q:\n%s", name, rendered)
		}
	}
	if got := strings.Count(rendered, "\n"+ruleGlyph); got != 2 {
		t.Errorf("counted %d section rules at the start of a line, want 2:\n%s", got, rendered)
	}
	if want := "2 features: 2 installed"; !strings.Contains(rendered, want) {
		t.Errorf("run summary missing %q:\n%s", want, rendered)
	}
}

// TestSectionMovesCaptionBelowWhenItCannotFit pins the narrow-terminal
// fallback: the caption drops to its own line rather than being cut off.
func TestSectionMovesCaptionBelowWhenItCannotFit(t *testing.T) {
	var out bytes.Buffer
	env := Env{Narration: &out, Style: Style{Width: minWidth}}
	name := strings.Repeat("x", minWidth)

	env.section(env.Style, name, "tool @ 8f0bbc7")

	if !strings.Contains(out.String(), name) {
		t.Errorf("the name was dropped:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "tool @ 8f0bbc7") {
		t.Errorf("the caption was dropped:\n%s", out.String())
	}
}

// TestStyleZeroValueRendersPlainText pins that narration into a pipe or a test
// buffer carries no escape sequences: color is opt-in, never the default.
func TestStyleZeroValueRendersPlainText(t *testing.T) {
	var out bytes.Buffer
	env := Env{Narration: &out}

	env.section(env.Style, "dsh", "tool")
	env.outcome(env.Style, markOK, "\x1b[32m", "installed")
	env.note(env.Style, "the next run rebuilds the image")
	env.unchanged("openclaw", "up to date")
	env.summarize(env.Style, "tool", []ActionResult{{Action: ActionInstalled}, {Action: ActionFailed}}, false)

	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("the zero-value style emitted ANSI escapes:\n%q", out.String())
	}
}

func TestRowSetAlignsToTheWidestLabelPresent(t *testing.T) {
	var out bytes.Buffer
	rows := &rowSet{}
	rows.add("ports", "3080")
	rows.add("allowlist directives", "no-poll")
	rows.add("skipped", "")
	rows.render(&out, Style{})

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("rendered %d lines, want the two non-empty rows:\n%s", len(lines), out.String())
	}
	first := strings.Index(lines[0], "3080")
	second := strings.Index(lines[1], "no-poll")
	if first != second {
		t.Errorf("values start at columns %d and %d, want one column:\n%s", first, second, out.String())
	}
	// The column follows the widest label rather than a fixed constant.
	if want := len(bodyIndent) + len("allowlist directives") + labelGap; first != want {
		t.Errorf("value column = %d, want %d", first, want)
	}
}

func TestWrapValueKeepsTokensWhole(t *testing.T) {
	long := "conf-file=/etc/dnsmasq.allowlists/fragments/anthropic.conf"
	lines := wrapValue(long+", no-poll, no-resolv", 40)

	if len(lines) < 2 {
		t.Fatalf("a value past the column was not wrapped: %q", lines)
	}
	if lines[0] != long+"," {
		t.Errorf("first line = %q, want the long token whole", lines[0])
	}
	for _, line := range lines {
		if strings.HasPrefix(line, " ") || strings.HasSuffix(line, " ") {
			t.Errorf("line %q carries padding; the caller indents", line)
		}
	}
	if joined := strings.Join(lines, " "); joined != long+", no-poll, no-resolv" {
		t.Errorf("wrapping changed the value: %q", joined)
	}
}

func TestSummarizeCountsOutcomes(t *testing.T) {
	var out bytes.Buffer
	env := Env{Narration: &out}
	results := []ActionResult{
		{Action: ActionInstalled}, {Action: ActionInstalled}, {Action: ActionFailed},
	}

	env.summarize(env.Style, "tool", results, false)

	if want := "3 tools: 2 installed, 1 failed"; !strings.Contains(out.String(), want) {
		t.Errorf("summary = %q, want it to contain %q", out.String(), want)
	}
}

// TestSummarizeStaysQuietWhenItWouldMislead covers the two cases where a count
// line is noise or a lie: a single extension, and a dry run whose results are
// all ActionSkipped because nothing was meant to be written.
func TestSummarizeStaysQuietWhenItWouldMislead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results []ActionResult
		dryRun  bool
	}{
		{"single extension", []ActionResult{{Action: ActionInstalled}}, false},
		{"dry run", []ActionResult{{Action: ActionSkipped}, {Action: ActionSkipped}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			env := Env{Narration: &out}
			env.summarize(env.Style, "tool", tc.results, tc.dryRun)
			if out.Len() != 0 {
				t.Errorf("summary rendered anyway: %q", out.String())
			}
		})
	}
}

// TestNarrateWithoutStreamDiscards pins that this package never picks a process
// stream of its own: with no narration configured there is nothing to render
// to, and nothing is rendered.
func TestNarrateWithoutStreamDiscards(t *testing.T) {
	if got := (Env{}).narrate(); got != io.Discard {
		t.Fatalf("narrate() = %v, want io.Discard", got)
	}
	if got := (Env{Narration: os.Stderr}).narrate(); got != os.Stderr {
		t.Fatalf("narrate() = %v, want the configured stream", got)
	}
}

// TestAddWithoutNarrationInstallsSilently covers the --json path: the installer
// still does the work and still reports it through the returned results, but
// renders no human-facing text anywhere.
func TestAddWithoutNarrationInstallsSilently(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, out := testEnv(t, fetcher, "")
	env.Narration = nil

	results, err := Add(context.Background(), env, addRequest())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionInstalled {
		t.Fatalf("results = %s", fmtResults(results))
	}
	if out.Len() != 0 {
		t.Fatalf("narration was rendered despite a nil stream:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo")); statErr != nil {
		t.Fatalf("extension was not installed: %v", statErr)
	}
}

// TestConfirmWithoutNarrationFails pins that a run which may prompt but has
// nowhere to put the question fails instead of assuming an answer.
func TestConfirmWithoutNarrationFails(t *testing.T) {
	fetcher := newFakeFetcher(t, "a1b2c3d4", fooRepoFiles())
	env, _ := testEnv(t, fetcher, "y\n")
	env.Narration = nil

	req := addRequest()
	req.Yes = false
	req.Interactive = true

	results, err := Add(context.Background(), env, req)
	if err == nil && !HasFailure(results) {
		t.Fatalf("a prompt with nowhere to render succeeded: %s", fmtResults(results))
	}
	message := results[0].Error
	if err != nil {
		message = err.Error()
	}
	if !strings.Contains(message, "--yes") {
		t.Fatalf("error = %q, want it to point at --yes", message)
	}
	if _, statErr := os.Stat(filepath.Join(env.Paths.UserFeaturesDir, "foo")); !os.IsNotExist(statErr) {
		t.Fatal("an unconfirmed install wrote files")
	}
}
