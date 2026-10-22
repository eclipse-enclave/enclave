// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"encoding/json"
	"strings"
	"testing"
)

// capture_id is additive: events without a capture serialize exactly as
// before, and lines written before the field existed still parse.
func TestEventCaptureIDIsAdditive(t *testing.T) {
	event := Event{Timestamp: "2026-08-13T12:00:00Z", Type: TypeHTTP, Method: "GET", Domain: "api.example.com", Verdict: VerdictPass}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(payload), "capture_id") {
		t.Fatalf("event without a capture serialized %s", payload)
	}

	event.CaptureID = "0123456789abcdef"
	payload, err = json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	parsed, ok := ParseLine(payload)
	if !ok || parsed.CaptureID != event.CaptureID {
		t.Fatalf("ParseLine(%s) = %+v, %v", payload, parsed, ok)
	}

	old, ok := ParseLine([]byte(`{"ts":"2026-08-13T12:00:00Z","type":"http","verdict":"pass","domain":"api.example.com"}`))
	if !ok || old.CaptureID != "" {
		t.Fatalf("pre-capture event = %+v, %v", old, ok)
	}
}
