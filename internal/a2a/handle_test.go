// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

import (
	"encoding/json"
	"strings"
	"testing"
)

const testGeneration = "0123456789abcdef"

func TestTaskHandleRoundTrip(t *testing.T) {
	h := TaskHandle{Worker: "worker-1", Generation: testGeneration, TaskID: "4f1c:9a/b c"}
	s := h.String()
	if s != "task:worker-1:0123456789abcdef:4f1c:9a/b c" {
		t.Fatalf("String() = %q", s)
	}
	parsed, err := ParseTaskHandle(s)
	if err != nil || parsed != h {
		t.Fatalf("ParseTaskHandle = %+v, %v", parsed, err)
	}
}

func TestContextHandleRoundTrip(t *testing.T) {
	h := ContextHandle{Worker: "w", Generation: testGeneration, ContextID: "Zm9vYmFy_-"}
	parsed, err := ParseContextHandle(h.String())
	if err != nil || parsed != h {
		t.Fatalf("ParseContextHandle = %+v, %v", parsed, err)
	}
	if _, err := ParseTaskHandle(h.String()); err == nil {
		t.Fatal("a context handle must not parse as a task handle")
	}
}

func TestParseHandleRejects(t *testing.T) {
	long := strings.Repeat("a", 33)
	for _, s := range []string{
		"",
		"task",
		"task:",
		"task:w:0123456789abcdef",
		"task:w:0123456789abcdef:",
		"ctx:w:0123456789abcdef:id",
		"TASK:w:0123456789abcdef:id",
		" task:w:0123456789abcdef:id",
		"task::0123456789abcdef:id",
		"task:W:0123456789abcdef:id",
		"task:-w:0123456789abcdef:id",
		"task:w-:0123456789abcdef:id",
		"task:_host:0123456789abcdef:id",
		"task:" + long + ":0123456789abcdef:id",
		"task:w:0123456789ABCDEF:id",
		"task:w:0123456789abcde:id",
		"task:w:0123456789abcdef0:id",
		"task:w:0123456789abcdeg:id",
		"task:w::id",
	} {
		if h, err := ParseTaskHandle(s); err == nil {
			t.Errorf("ParseTaskHandle(%q) = %+v, want error", s, h)
		}
	}
}

func TestHandleText(t *testing.T) {
	type payload struct {
		Task    TaskHandle    `json:"task"`
		Context ContextHandle `json:"context"`
	}
	in := payload{
		Task:    TaskHandle{Worker: "w", Generation: testGeneration, TaskID: "t1"},
		Context: ContextHandle{Worker: "w", Generation: testGeneration, ContextID: "c1"},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"task":"task:w:0123456789abcdef:t1","context":"ctx:w:0123456789abcdef:c1"}`
	if string(data) != want {
		t.Fatalf("marshal = %s", data)
	}
	var out payload
	if err := json.Unmarshal(data, &out); err != nil || out != in {
		t.Fatalf("unmarshal = %+v, %v", out, err)
	}
	if _, err := json.Marshal(TaskHandle{Worker: "w", TaskID: "t"}); err == nil {
		t.Fatal("marshal of an invalid handle must fail")
	}
	if err := json.Unmarshal([]byte(`{"task":"ctx:w:0123456789abcdef:c"}`), &out); err == nil {
		t.Fatal("unmarshal of a wrong prefix must fail")
	}
}

var handleSeeds = []string{
	"task:worker-1:0123456789abcdef:7b2e1c",
	"ctx:worker-1:0123456789abcdef:Zm9vYmFy",
	"task:w:ffffffffffffffff:a:b:c",
	"task:w:0123456789abcdef:",
	"task::0123456789abcdef:x",
	"task:w:0123456789ABCDEF:x",
	"task:w:0123456789abcdef:\x00\n",
	"ctx:a-b-c:0000000000000000:ctxé",
	"",
	":::",
}

// Accepted handles must round-trip exactly through String.
func FuzzParseTaskHandle(f *testing.F) {
	for _, s := range handleSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		h, err := ParseTaskHandle(s)
		if err != nil {
			return
		}
		if h.String() != s {
			t.Fatalf("String() = %q, input %q", h.String(), s)
		}
		again, err := ParseTaskHandle(h.String())
		if err != nil || again != h {
			t.Fatalf("reparse = %+v, %v", again, err)
		}
		if _, err := h.MarshalText(); err != nil {
			t.Fatalf("MarshalText of a parsed handle: %v", err)
		}
	})
}

// Accepted handles must round-trip exactly through String.
func FuzzParseContextHandle(f *testing.F) {
	for _, s := range handleSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		h, err := ParseContextHandle(s)
		if err != nil {
			return
		}
		if h.String() != s {
			t.Fatalf("String() = %q, input %q", h.String(), s)
		}
		again, err := ParseContextHandle(h.String())
		if err != nil || again != h {
			t.Fatalf("reparse = %+v, %v", again, err)
		}
		if _, err := h.MarshalText(); err != nil {
			t.Fatalf("MarshalText of a parsed handle: %v", err)
		}
	})
}
