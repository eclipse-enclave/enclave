// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netcapture

import (
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"
)

func redactStream(r *redactor, chunks ...string) string {
	stream := r.stream()
	var out strings.Builder
	for _, chunk := range chunks {
		out.Write(stream.feed([]byte(chunk)))
	}
	out.Write(stream.flush())
	return out.String()
}

func TestStreamRedactorFindsValuesSplitAcrossWrites(t *testing.T) {
	r := newRedactor([]Secret{{Value: "sk-real-secret", Placeholder: "PLACEHOLDER"}})
	cases := map[string][]string{
		"whole":        {`{"key":"sk-real-secret"}`},
		"split":        {`{"key":"sk-re`, `al-secret"}`},
		"byte by byte": strings.Split(`{"key":"sk-real-secret"}`, ""),
		"at the end":   {`{"key":"`, `sk-real-secret`},
	}
	for name, chunks := range cases {
		t.Run(name, func(t *testing.T) {
			got := redactStream(r, chunks...)
			want := strings.ReplaceAll(strings.Join(chunks, ""), "sk-real-secret", "PLACEHOLDER")
			if got != want {
				t.Fatalf("redacted = %q, want %q", got, want)
			}
		})
	}
}

func TestStreamRedactorKeepsUnrelatedBytes(t *testing.T) {
	r := newRedactor([]Secret{{Value: "secret-value", Placeholder: "P"}})
	input := []string{"secret-", "valu", "e is not secret-val", "ue-ish secret-v"}
	got := redactStream(r, input...)
	if want := "P is not P-ish secret-v"; got != want {
		t.Fatalf("redacted = %q, want %q", got, want)
	}
}

func TestRedactorPrefersTheLongerValue(t *testing.T) {
	r := newRedactor([]Secret{
		{Value: "token", Placeholder: "SHORT"},
		{Value: "token-with-suffix", Placeholder: "LONG"},
	})
	if got := r.string("a token-with-suffix and a token"); got != "a LONG and a SHORT" {
		t.Fatalf("redacted = %q", got)
	}
	if got := redactStream(r, "a token-with", "-suffix and a token"); got != "a LONG and a SHORT" {
		t.Fatalf("stream redacted = %q", got)
	}
}

func TestRedactorHeaderRedactsValuesAndCopies(t *testing.T) {
	r := newRedactor([]Secret{{Value: "real", Placeholder: "PH"}})
	original := http.Header{"Authorization": {"Bearer real"}, "X-Other": {"plain"}}
	redacted := r.header(original)
	if got := redacted.Get("Authorization"); got != "Bearer PH" {
		t.Fatalf("Authorization = %q", got)
	}
	if original.Get("Authorization") != "Bearer real" {
		t.Fatal("redaction modified the request's own headers")
	}

	// A name carrying the value is redacted too, keeping its values.
	renamed := r.header(http.Header{"X-real": {"one"}, "X-PH": {"two"}})
	for name, values := range renamed {
		if name != "X-PH" || len(values) != 2 || len(renamed) != 1 {
			t.Fatalf("headers = %v, want both values under the redacted name", renamed)
		}
	}
}

func TestNilRedactorPassesThrough(t *testing.T) {
	var r *redactor
	if r = newRedactor(nil); r != nil {
		t.Fatal("expected no redactor without secrets")
	}
	if got := r.string("value"); got != "value" {
		t.Fatalf("string = %q", got)
	}
	if r.stream() != nil {
		t.Fatal("expected no stream redactor")
	}
}

// However a body is split into writes, the stream form must redact exactly as
// redacting the whole body at once does. Overlapping values over a small
// alphabet make every tie and partial match likely.
func TestStreamRedactorMatchesWholeBodyRedaction(t *testing.T) {
	r := newRedactor([]Secret{
		{Value: "abca", Placeholder: "<1>"},
		{Value: "bc", Placeholder: "<2>"},
		{Value: "cab", Placeholder: "<3>"},
		{Value: "aaaa", Placeholder: "<4>"},
	})
	random := rand.New(rand.NewSource(1)) // #nosec G404 -- deterministic test input.
	for round := 0; round < 2000; round++ {
		body := make([]byte, random.Intn(40))
		for i := range body {
			body[i] = "abc"[random.Intn(3)]
		}
		var chunks []string
		for rest := string(body); rest != ""; {
			size := 1 + random.Intn(len(rest))
			chunks = append(chunks, rest[:size])
			rest = rest[size:]
		}
		want := string(r.replace(body))
		if got := redactStream(r, chunks...); got != want {
			t.Fatalf("chunks %q redacted to %q, want %q", chunks, got, want)
		}
	}
}

func TestRedactorScansManyMatchesInLinearTime(t *testing.T) {
	r := newRedactor([]Secret{
		{Value: "sk-live-0123456789abcdef", Placeholder: "P"},
		{Value: "never-present-value", Placeholder: "Q"},
	})
	body := []byte(strings.Repeat("sk-live-0123456789abcdef", 1<<16))
	started := time.Now()
	out := r.replace(body)
	if len(out) != 1<<16 {
		t.Fatalf("redacted %d bytes, want one placeholder per match", len(out))
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("redacting 1.5 MB of matches took %s", elapsed)
	}
}
