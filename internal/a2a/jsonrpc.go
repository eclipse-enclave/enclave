// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// JSONRPCVersion is the only accepted "jsonrpc" member value.
const JSONRPCVersion = "2.0"

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Enclave-owned error codes, in the application-defined space below -32768
// that neither JSON-RPC 2.0 nor A2A 1.0 claims (spec-pin.md, Errors).
const (
	// Hub delivery errors (proposal 4, 6, 12). Never a task state.
	CodeDeliveryAdapterUnreachable = -31001
	CodeDeliveryUnknownMember      = -31002
	CodeDeliveryRevoked            = -31003
	CodeDeliveryUnauthenticated    = -31004
	CodeDeliveryRoleForbidden      = -31005

	// Adapter refusals (proposal 10, 13, 14).
	CodeContextUnknown    = -31010
	CodeContextExpired    = -31011
	CodeMessageIDConflict = -31012
	CodeRetryAmbiguous    = -31013
	CodeAdmissionStopped  = -31014
	CodeQuarantined       = -31015
	CodeQueueFull         = -31016
	CodeGenerationUnknown = -31017
	CodeRequestTooLarge   = -31018
	// CodeEpochUnconfirmed: the control stream has not confirmed the request's epoch; nothing was admitted.
	CodeEpochUnconfirmed = -31019
)

// Request is a JSON-RPC 2.0 request. ID is kept raw so it round-trips unchanged.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response; exactly one of Result and Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("a2a: json-rpc error %d: %s", e.Code, e.Message)
}

// ErrorInfoType and ErrorDomain fill the google.rpc.ErrorInfo entry that spec 9.5 asks for in "data".
const (
	ErrorInfoType = "type.googleapis.com/google.rpc.ErrorInfo"
	ErrorDomain   = "enclave.eclipse.dev"
)

// ErrorInfo is one element of the "data" array of enclave error codes.
type ErrorInfo struct {
	Type     string            `json:"@type"`
	Reason   string            `json:"reason"`
	Domain   string            `json:"domain"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// NewError builds an Error whose data is a single ErrorInfo with ErrorDomain and the given reason.
func NewError(code int, message, reason string, metadata map[string]string) *Error {
	data, err := json.Marshal([]ErrorInfo{{
		Type:     ErrorInfoType,
		Reason:   reason,
		Domain:   ErrorDomain,
		Metadata: metadata,
	}})
	if err != nil {
		// Unreachable: ErrorInfo holds only strings.
		panic(err)
	}
	return &Error{Code: code, Message: message, Data: data}
}

// Result is the oneof of SendMessageResponse and StreamResponse; the JSON
// member name discriminates (no kind field in 1.0). Exactly one is set.
type Result struct {
	Task           *Task
	Message        *Message
	StatusUpdate   *TaskStatusUpdateEvent
	ArtifactUpdate *TaskArtifactUpdateEvent
}

// DecodeResult decodes a result by its single member name; zero or several members is an error.
func DecodeResult(raw json.RawMessage) (Result, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return Result{}, fmt.Errorf("a2a: decode result: %w", err)
	}
	if members == nil {
		return Result{}, errors.New("a2a: decode result: not an object")
	}
	var r Result
	found := 0
	for name, value := range members {
		// ProtoJSON writes an unset message field as absent or null.
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		var target any
		switch name {
		case resultTask:
			r.Task = new(Task)
			target = r.Task
		case resultMessage:
			r.Message = new(Message)
			target = r.Message
		case resultStatusUpdate:
			r.StatusUpdate = new(TaskStatusUpdateEvent)
			target = r.StatusUpdate
		case resultArtifactUpdate:
			r.ArtifactUpdate = new(TaskArtifactUpdateEvent)
			target = r.ArtifactUpdate
		default:
			continue
		}
		found++
		if err := json.Unmarshal(value, target); err != nil {
			return Result{}, fmt.Errorf("a2a: decode result member %q: %w", name, err)
		}
	}
	if found != 1 {
		return Result{}, fmt.Errorf("a2a: decode result: want exactly one of task, message, statusUpdate, artifactUpdate, got %d", found)
	}
	return r, nil
}

// EncodeResult encodes the populated member of r.
func EncodeResult(r Result) (json.RawMessage, error) {
	name, value, err := r.member()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(map[string]any{name: value})
	if err != nil {
		return nil, fmt.Errorf("a2a: encode result member %q: %w", name, err)
	}
	return data, nil
}

// Result member names (spec 3.2.3, SendMessageResponse and StreamResponse).
const (
	resultTask           = "task"
	resultMessage        = "message"
	resultStatusUpdate   = "statusUpdate"
	resultArtifactUpdate = "artifactUpdate"
)

func (r Result) member() (string, any, error) {
	var name string
	var value any
	set := 0
	if r.Task != nil {
		name, value = resultTask, r.Task
		set++
	}
	if r.Message != nil {
		name, value = resultMessage, r.Message
		set++
	}
	if r.StatusUpdate != nil {
		name, value = resultStatusUpdate, r.StatusUpdate
		set++
	}
	if r.ArtifactUpdate != nil {
		name, value = resultArtifactUpdate, r.ArtifactUpdate
		set++
	}
	if set != 1 {
		return "", nil, fmt.Errorf("a2a: encode result: want exactly one member set, got %d", set)
	}
	return name, value, nil
}

// MarshalJSON encodes r with EncodeResult.
func (r Result) MarshalJSON() ([]byte, error) { return EncodeResult(r) }

// UnmarshalJSON decodes r with DecodeResult.
func (r *Result) UnmarshalJSON(data []byte) error {
	decoded, err := DecodeResult(data)
	if err != nil {
		return err
	}
	*r = decoded
	return nil
}

// Info returns the first ErrorInfo in e.Data with ErrorDomain, if any.
func (e *Error) Info() (ErrorInfo, bool) {
	if e == nil || len(e.Data) == 0 {
		return ErrorInfo{}, false
	}
	var details []json.RawMessage
	if err := json.Unmarshal(e.Data, &details); err != nil {
		return ErrorInfo{}, false
	}
	for _, d := range details {
		var info ErrorInfo
		if json.Unmarshal(d, &info) == nil && info.Type == ErrorInfoType && info.Domain == ErrorDomain {
			return info, true
		}
	}
	return ErrorInfo{}, false
}

// DecodeRequest parses one JSON-RPC request; the returned error is ready to send.
func DecodeRequest(body []byte) (Request, *Error) {
	if !json.Valid(body) {
		return Request{}, &Error{Code: CodeParseError, Message: "Invalid JSON payload"}
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return Request{}, &Error{Code: CodeInvalidRequest, Message: "Request payload validation error"}
	}
	if req.JSONRPC != JSONRPCVersion || req.Method == "" || !validRequestID(req.ID) {
		return req, &Error{Code: CodeInvalidRequest, Message: "Request payload validation error"}
	}
	return req, nil
}

// Notifications (no id) and null ids are refused: every A2A method has a response.
func validRequestID(id json.RawMessage) bool {
	id = bytes.TrimSpace(id)
	if len(id) == 0 {
		return false
	}
	switch id[0] {
	case '"':
		return true
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return true
	}
	return false
}

// DecodeParams decodes req.Params into v; absent params decode as an empty object.
func DecodeParams(req Request, v any) *Error {
	params := bytes.TrimSpace(req.Params)
	if len(params) == 0 || bytes.Equal(params, []byte("null")) {
		params = []byte("{}")
	}
	if params[0] != '{' {
		return &Error{Code: CodeInvalidParams, Message: "Invalid parameters"}
	}
	if err := json.Unmarshal(params, v); err != nil {
		return &Error{Code: CodeInvalidParams, Message: "Invalid parameters: " + err.Error()}
	}
	return nil
}
