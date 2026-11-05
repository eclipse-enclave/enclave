// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestErrorImplementsError(t *testing.T) {
	var err error = &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	if got := err.Error(); !strings.Contains(got, "-32001") || !strings.Contains(got, "Task not found") {
		t.Fatalf("Error() = %q", got)
	}
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != CodeTaskNotFound {
		t.Fatal("errors.As must find *Error")
	}
}

func TestNewErrorData(t *testing.T) {
	e := NewError(CodeQueueFull, "queue full", "QUEUE_FULL", nil)
	var data []map[string]any
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 || data[0]["@type"] != ErrorInfoType || data[0]["domain"] != ErrorDomain || data[0]["reason"] != "QUEUE_FULL" {
		t.Fatalf("data %s", e.Data)
	}
	if _, ok := data[0]["metadata"]; ok {
		t.Fatal("nil metadata must be omitted")
	}
	info, ok := e.Info()
	if !ok || info.Reason != "QUEUE_FULL" {
		t.Fatalf("Info() = %+v, %v", info, ok)
	}
}

func TestErrorInfoSkipsForeignDetails(t *testing.T) {
	e := &Error{Code: CodeDeliveryRevoked, Message: "revoked", Data: json.RawMessage(`[
		{"@type":"type.googleapis.com/google.rpc.BadRequest"},
		{"@type":"type.googleapis.com/google.rpc.ErrorInfo","domain":"a2a-protocol.org","reason":"X"},
		{"@type":"type.googleapis.com/google.rpc.ErrorInfo","domain":"enclave.eclipse.dev","reason":"REVOKED"}
	]`)}
	info, ok := e.Info()
	if !ok || info.Reason != DeliveryRevoked {
		t.Fatalf("Info() = %+v, %v", info, ok)
	}
	for _, data := range []string{"", `{}`, `"x"`, `[1]`} {
		e := &Error{Data: json.RawMessage(data)}
		if _, ok := e.Info(); ok {
			t.Fatalf("Info() on %q must be false", data)
		}
	}
	var nilErr *Error
	if _, ok := nilErr.Info(); ok {
		t.Fatal("nil Info must be false")
	}
}

func TestDecodeRequestErrors(t *testing.T) {
	cases := map[string]struct {
		body string
		code int
	}{
		"invalid JSON":    {`{"jsonrpc":`, CodeParseError},
		"empty":           {``, CodeParseError},
		"batch":           {`[{"jsonrpc":"2.0","id":1,"method":"GetTask"}]`, CodeInvalidRequest},
		"wrong version":   {`{"jsonrpc":"1.0","id":1,"method":"GetTask"}`, CodeInvalidRequest},
		"missing version": {`{"id":1,"method":"GetTask"}`, CodeInvalidRequest},
		"missing method":  {`{"jsonrpc":"2.0","id":1}`, CodeInvalidRequest},
		"method type":     {`{"jsonrpc":"2.0","id":1,"method":7}`, CodeInvalidRequest},
		"notification":    {`{"jsonrpc":"2.0","method":"GetTask"}`, CodeInvalidRequest},
		"null id":         {`{"jsonrpc":"2.0","id":null,"method":"GetTask"}`, CodeInvalidRequest},
		"object id":       {`{"jsonrpc":"2.0","id":{},"method":"GetTask"}`, CodeInvalidRequest},
		"bool id":         {`{"jsonrpc":"2.0","id":true,"method":"GetTask"}`, CodeInvalidRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeRequest([]byte(tc.body))
			if err == nil || err.Code != tc.code {
				t.Fatalf("want %d, got %v", tc.code, err)
			}
		})
	}
	for _, id := range []string{`1`, `-3`, `"abc"`, `1.5`} {
		req, err := DecodeRequest([]byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"GetTask"}`))
		if err != nil || string(req.ID) != id {
			t.Fatalf("id %s: %v %s", id, err, req.ID)
		}
	}
}

func TestDecodeParams(t *testing.T) {
	var p GetTaskRequest
	if err := DecodeParams(Request{}, &p); err != nil {
		t.Fatalf("absent params: %v", err)
	}
	if err := DecodeParams(Request{Params: json.RawMessage("null")}, &p); err != nil {
		t.Fatalf("null params: %v", err)
	}
	for _, params := range []string{`[]`, `"x"`, `{"id":5}`} {
		if err := DecodeParams(Request{Params: json.RawMessage(params)}, &p); err == nil || err.Code != CodeInvalidParams {
			t.Fatalf("params %s: want -32602, got %v", params, err)
		}
	}
}
