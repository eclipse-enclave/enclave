// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

const (
	missingRetriesWarning = `time="2026-09-15T14:10:19+02:00" level=warning msg="missing \"ENCLAVE_NET_RETRIES\" build argument. Try adding \"--build-arg ENCLAVE_NET_RETRIES=<VALUE>\" to the command line"`
	missingProxyWarning   = `WARN[0000] missing "GOPROXY" build argument. Try adding "--build-arg GOPROXY=<VALUE>" to the command line`
	missingCustomWarning  = `time="2026-09-15T14:10:19+02:00" level=warning msg="missing \"CUSTOM\" build argument. Try adding \"--build-arg CUSTOM=<VALUE>\" to the command line"`
)

func TestBuildahWarningFilterDropsOnlyOptionalArgWarnings(t *testing.T) {
	var got bytes.Buffer
	f := newBuildahWarningFilter(&got, []string{"ENCLAVE_NET_RETRIES", "GOPROXY"})
	lines := []string{
		missingRetriesWarning,
		missingProxyWarning,
		missingCustomWarning,
		`time="2026-09-15T14:10:19+02:00" level=warning msg="SHELL is not supported for OCI image format"`,
		"[1/6] STEP 1/19: FROM debian:trixie-slim AS system",
		"Get:4 http://deb.debian.org/debian trixie/main amd64 Packages [9,678 kB]",
	}
	for _, line := range lines {
		if _, err := io.WriteString(f, line+"\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := f.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	want := strings.Join(lines[2:], "\n") + "\n"
	if got.String() != want {
		t.Fatalf("filtered output mismatch\n got: %q\nwant: %q", got.String(), want)
	}
}

func TestBuildahWarningFilterHandlesSplitWrites(t *testing.T) {
	var got bytes.Buffer
	f := newBuildahWarningFilter(&got, []string{"ENCLAVE_NET_RETRIES"})
	stream := missingRetriesWarning + "\n[1/6] STEP 1/19: FROM debian\n" + missingRetriesWarning + "\nSTEP 2\n"
	for i := 0; i < len(stream); i += 7 {
		end := i + 7
		if end > len(stream) {
			end = len(stream)
		}
		if _, err := f.Write([]byte(stream[i:end])); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := f.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if want := "[1/6] STEP 1/19: FROM debian\nSTEP 2\n"; got.String() != want {
		t.Fatalf("filtered output mismatch\n got: %q\nwant: %q", got.String(), want)
	}
}

// Step output without a trailing newline (a prompt, a progress line) must
// reach the terminal at once; only a possible warning is held back.
func TestBuildahWarningFilterDoesNotHoldBackStepOutput(t *testing.T) {
	var got bytes.Buffer
	f := newBuildahWarningFilter(&got, []string{"ENCLAVE_NET_RETRIES"})
	if _, err := io.WriteString(f, "Installing claude..."); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got.String() != "Installing claude..." {
		t.Fatalf("partial step output was held back, got %q", got.String())
	}
	if _, err := io.WriteString(f, " done\n"+`time="2026-09-15T14:10:19+02:00" level=warn`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got.String() != "Installing claude... done\n" {
		t.Fatalf("possible warning was not held back, got %q", got.String())
	}
	if _, err := io.WriteString(f, `ing msg="Failed to ..."`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if want := "Installing claude... done\n" + `time="2026-09-15T14:10:19+02:00" level=warning msg="Failed to ..."`; got.String() != want {
		t.Fatalf("held line was not flushed\n got: %q\nwant: %q", got.String(), want)
	}
}

func TestEngineOutputIsUnfilteredWithoutOptionalArgs(t *testing.T) {
	var got bytes.Buffer
	out, flush := engineOutput(&got, BuildRequest{})
	if _, err := io.WriteString(out, missingRetriesWarning+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	flush()
	if got.String() != missingRetriesWarning+"\n" {
		t.Fatalf("output must pass through unchanged, got %q", got.String())
	}
}
