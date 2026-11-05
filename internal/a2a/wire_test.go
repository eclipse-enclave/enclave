// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Fixtures named spec-<section>-* are copied from the A2A v1.0.1 specification
// examples of that section. Two were not valid JSON upstream and are repaired:
// 4.6.1 (trailing comma) and 6.7 (missing comma, trailing comma). Fixtures
// named enclave-* are modeled on interfaces.md section 11.

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// decodeRoundTrip decodes data into a new T and requires encode and decode to be stable.
func decodeRoundTrip[T any](t *testing.T, data []byte) T {
	t.Helper()
	var first T
	if err := json.Unmarshal(data, &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var second T
	if err := json.Unmarshal(encoded, &second); err != nil {
		t.Fatalf("decode re-encoded: %v\n%s", err, encoded)
	}
	// Compare encodings, not values: raw JSON members are compacted on encode.
	reencoded, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("encode again: %v", err)
	}
	if string(encoded) != string(reencoded) {
		t.Fatalf("round trip is not stable:\nfirst:  %s\nsecond: %s", encoded, reencoded)
	}
	return first
}

// requireSameJSON compares two JSON documents structurally.
func requireSameJSON(t *testing.T, want, got []byte) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("want: %v", err)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got: %v", err)
	}
	if !reflect.DeepEqual(w, g) {
		t.Fatalf("JSON differs:\nwant: %s\ngot:  %s", want, got)
	}
}

// requireExactRoundTrip requires that decoding and re-encoding reproduces the
// fixture, which holds when the fixture sets every non-omitempty field.
func requireExactRoundTrip[T any](t *testing.T, name string) T {
	t.Helper()
	data := readFixture(t, name)
	v := decodeRoundTrip[T](t, data)
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	requireSameJSON(t, data, encoded)
	return v
}

func TestSpecSendMessageRequest(t *testing.T) {
	req := requireExactRoundTrip[SendMessageRequest](t, "spec-6.1-send-request.json")
	if req.Message.Role != RoleUser || req.Message.MessageID != "msg-uuid" {
		t.Fatalf("unexpected message: %+v", req.Message)
	}
	if err := req.Message.Validate(); err != nil {
		t.Fatalf("spec 6.1 message should validate: %v", err)
	}

	follow := requireExactRoundTrip[SendMessageRequest](t, "spec-6.3-follow-up-request.json")
	if follow.Message.TaskID != "task-uuid" {
		t.Fatalf("taskId lost: %+v", follow.Message)
	}
}

func TestSpecResultsDecode(t *testing.T) {
	cases := []struct {
		fixture string
		check   func(t *testing.T, r Result)
	}{
		{"spec-6.1-send-response.json", func(t *testing.T, r Result) {
			if r.Task == nil || r.Task.Status.State != TaskStateCompleted {
				t.Fatalf("want completed task, got %+v", r)
			}
			if got := *r.Task.Artifacts[0].Parts[0].Text; got != "Today will be sunny with a high of 75°F" {
				t.Fatalf("artifact text %q", got)
			}
		}},
		{"spec-6.3-input-required-response.json", func(t *testing.T, r Result) {
			if r.Task == nil || r.Task.Status.State != TaskStateInputRequired {
				t.Fatalf("want input-required task, got %+v", r)
			}
			if r.Task.Status.Message == nil || r.Task.Status.Message.Role != RoleAgent {
				t.Fatalf("status message lost: %+v", r.Task.Status)
			}
		}},
		{"spec-6.7-file-response.json", func(t *testing.T, r Result) {
			p := r.Task.Artifacts[0].Parts[0]
			if p.URL == "" || p.Filename != "output.png" || p.MediaType != "image/png" || p.IsText() {
				t.Fatalf("url part decoded wrong: %+v", p)
			}
			if r.Task.Status.Timestamp != "2024-03-15T12:05:00Z" {
				t.Fatalf("timestamp %q", r.Task.Status.Timestamp)
			}
		}},
		{"spec-6.8-structured-response.json", func(t *testing.T, r Result) {
			if !r.Task.Artifacts[0].Parts[0].IsText() {
				t.Fatal("structured response is a text part")
			}
		}},
		{"enclave-task-terminal.json", func(t *testing.T, r Result) {
			meta, ok, err := EnclaveMetaFrom(r.Task.Metadata)
			if err != nil || !ok {
				t.Fatalf("task meta: ok=%v err=%v", ok, err)
			}
			if meta.Termination != TerminationUnconfirmed || meta.Reason != ReasonCancel {
				t.Fatalf("task meta %+v", meta)
			}
			hist, ok, err := EnclaveMetaFrom(r.Task.History[0].Metadata)
			if err != nil || !ok || hist.Principal == nil || hist.Principal.Role != RoleOrchestrator {
				t.Fatalf("history principal: %+v ok=%v err=%v", hist, ok, err)
			}
		}},
		{"enclave-status-update-queued.json", func(t *testing.T, r Result) {
			meta, ok, err := EnclaveMetaFrom(r.StatusUpdate.Metadata)
			if err != nil || !ok || meta.QueuePosition == nil || *meta.QueuePosition != 0 {
				t.Fatalf("queue position: %+v ok=%v err=%v", meta, ok, err)
			}
		}},
		{"enclave-artifact-update.json", func(t *testing.T, r Result) {
			if r.ArtifactUpdate == nil || !r.ArtifactUpdate.Append || !r.ArtifactUpdate.LastChunk {
				t.Fatalf("artifact update %+v", r.ArtifactUpdate)
			}
		}},
		{"enclave-message-result.json", func(t *testing.T, r Result) {
			if r.Message == nil || r.Message.Role != RoleAgent {
				t.Fatalf("message result %+v", r)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			data := readFixture(t, tc.fixture)
			r, err := DecodeResult(data)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, r)
			encoded, err := EncodeResult(r)
			if err != nil {
				t.Fatal(err)
			}
			again, err := DecodeResult(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r, again) {
				t.Fatalf("round trip changed the result:\n%s", encoded)
			}
		})
	}
}

// The enclave fixtures set every required field, so re-encoding must reproduce them exactly.
func TestEnclaveResultsExactRoundTrip(t *testing.T) {
	for _, name := range []string{
		"enclave-status-update-queued.json",
		"enclave-artifact-update.json",
		"enclave-message-result.json",
		"enclave-task-terminal.json",
	} {
		t.Run(name, func(t *testing.T) {
			data := readFixture(t, name)
			r, err := DecodeResult(data)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := EncodeResult(r)
			if err != nil {
				t.Fatal(err)
			}
			requireSameJSON(t, data, encoded)
		})
	}
}

func TestSpecArtifactWithExtension(t *testing.T) {
	a := requireExactRoundTrip[Artifact](t, "spec-4.6.2-artifact-extension.json")
	if len(a.Extensions) != 1 || a.Metadata[a.Extensions[0]] == nil {
		t.Fatalf("extension metadata lost: %+v", a)
	}
}

// Spec 4.6.1 omits the required messageId; decoding is lenient, validation is not.
func TestSpecExtensionRequestMissingMessageID(t *testing.T) {
	req := decodeRoundTrip[SendMessageRequest](t, readFixture(t, "spec-4.6.1-extension-request.json"))
	if _, ok := req.Message.Metadata["https://example.com/extensions/geolocation/v1"]; !ok {
		t.Fatal("extension metadata lost")
	}
	err := req.Message.Validate()
	if err == nil || err.Code != CodeInvalidParams {
		t.Fatalf("want -32602 for missing messageId, got %v", err)
	}
}

func TestSpecAgentCards(t *testing.T) {
	// Spec 8.5 carries fields we do not model (provider, iconUrl, signatures, the
	// prose-only "security"); unknown fields are ignored (spec 5.7).
	card := decodeRoundTrip[AgentCard](t, readFixture(t, "spec-8.5-agent-card.json"))
	if len(card.SupportedInterfaces) != 3 || card.SupportedInterfaces[0].ProtocolBinding != ProtocolBinding {
		t.Fatalf("interfaces %+v", card.SupportedInterfaces)
	}
	if card.Capabilities.Streaming == nil || !*card.Capabilities.Streaming {
		t.Fatal("streaming capability lost")
	}
	if s, ok := card.SecuritySchemes["google"]; !ok || s.HTTPAuth != nil {
		t.Fatalf("openIdConnect scheme should decode as an empty wrapper: %+v", card.SecuritySchemes)
	}
	if len(card.Skills) != 2 || len(card.Skills[0].Tags) != 5 {
		t.Fatalf("skills %+v", card.Skills)
	}

	ext := decodeRoundTrip[AgentCard](t, readFixture(t, "spec-4.6.1-agent-card.json"))
	if len(ext.Capabilities.Extensions) != 2 || ext.Capabilities.Extensions[0].Required {
		t.Fatalf("extensions %+v", ext.Capabilities.Extensions)
	}
}

func TestEnclaveAgentCardGolden(t *testing.T) {
	streaming := true
	card := AgentCard{
		Name:        "worker-1",
		Description: "Reviews Go code",
		SupportedInterfaces: []AgentInterface{{
			URL:             "http://" + HubDNSName + ":8080" + PeerPathPrefix + "worker-1",
			ProtocolBinding: ProtocolBinding,
			ProtocolVersion: ProtocolVersion,
		}},
		Version: "1",
		Capabilities: AgentCapabilities{
			Streaming: &streaming,
			Extensions: []AgentExtension{{
				URI:         ExtensionURI,
				Description: "enclave peer group",
				Required:    true,
				Params:      map[string]any{"queueDepth": 4},
			}},
		},
		SecuritySchemes: map[string]SecurityScheme{
			"bearer": {HTTPAuth: &HTTPAuthSecurityScheme{Scheme: "Bearer"}},
		},
		SecurityRequirements: []SecurityRequirement{{Schemes: map[string]StringList{"bearer": {List: []string{}}}}},
		DefaultInputModes:    []string{"text/plain"},
		DefaultOutputModes:   []string{"text/plain"},
		Skills: []AgentSkill{{
			ID: "delegate", Name: "Delegate", Description: "Run a task in this worker session", Tags: []string{"enclave"},
		}},
	}
	got, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	requireSameJSON(t, readFixture(t, "enclave-agent-card.json"), got)
	requireExactRoundTrip[AgentCard](t, "enclave-agent-card.json")
}

func TestSpecRequests(t *testing.T) {
	cases := []struct {
		fixture string
		method  string
		check   func(t *testing.T, req Request)
	}{
		{"spec-9.4.1-send-message-request.json", MethodSendMessage, func(t *testing.T, req Request) {
			var p SendMessageRequest
			if err := DecodeParams(req, &p); err != nil {
				t.Fatal(err)
			}
			if p.Message.MessageID != "msg-uuid" {
				t.Fatalf("params %+v", p)
			}
		}},
		{"spec-9.4.3-get-task-request.json", MethodGetTask, func(t *testing.T, req Request) {
			var p GetTaskRequest
			if err := DecodeParams(req, &p); err != nil {
				t.Fatal(err)
			}
			if p.ID != "task-uuid" || p.HistoryLength == nil || *p.HistoryLength != 10 {
				t.Fatalf("params %+v", p)
			}
		}},
		{"spec-9.4.5-cancel-task-request.json", MethodCancelTask, func(t *testing.T, req Request) {
			var p CancelTaskRequest
			if err := DecodeParams(req, &p); err != nil || p.ID != "task-uuid" {
				t.Fatalf("params %+v err %v", p, err)
			}
		}},
		{"spec-9.4.6-subscribe-request.json", MethodSubscribeToTask, func(t *testing.T, req Request) {
			var p SubscribeToTaskRequest
			if err := DecodeParams(req, &p); err != nil || p.ID != "task-uuid" {
				t.Fatalf("params %+v err %v", p, err)
			}
		}},
		{"spec-9.4.8-extended-card-request.json", MethodGetExtendedAgentCard, func(t *testing.T, req Request) {
			if req.Params != nil {
				t.Fatalf("params %s", req.Params)
			}
		}},
		{"enclave-send-streaming-request.json", MethodSendStreamingMessage, func(t *testing.T, req Request) {
			var p SendMessageRequest
			if err := DecodeParams(req, &p); err != nil {
				t.Fatal(err)
			}
			meta, ok, err := EnclaveMetaFrom(p.Message.Metadata)
			if err != nil || !ok || !meta.Retry {
				t.Fatalf("retry meta %+v ok=%v err=%v", meta, ok, err)
			}
			if string(req.ID) != `"req-1"` {
				t.Fatalf("id %s", req.ID)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			data := readFixture(t, tc.fixture)
			req, rpcErr := DecodeRequest(data)
			if rpcErr != nil {
				t.Fatal(rpcErr)
			}
			if req.Method != tc.method {
				t.Fatalf("method %q", req.Method)
			}
			tc.check(t, req)
			encoded, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			requireSameJSON(t, data, encoded)
		})
	}
}

func TestSpecErrorResponses(t *testing.T) {
	for _, name := range []string{
		"spec-9.5-invalid-params-error.json",
		"spec-9.5-task-not-found-error.json",
		"enclave-delivery-error.json",
	} {
		t.Run(name, func(t *testing.T) {
			resp := requireExactRoundTrip[Response](t, name)
			if resp.Error == nil || resp.Result != nil {
				t.Fatalf("want error response, got %+v", resp)
			}
		})
	}

	notFound := requireExactRoundTrip[Response](t, "spec-9.5-task-not-found-error.json")
	if _, ok := notFound.Error.Info(); ok {
		t.Fatal("a2a-protocol.org ErrorInfo must not be reported as an enclave ErrorInfo")
	}
}

func TestNewErrorGolden(t *testing.T) {
	resp := Response{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage("1"),
		Error: NewError(CodeDeliveryAdapterUnreachable, "adapter unreachable", DeliveryAdapterUnreachable,
			map[string]string{"member": "worker-1"}),
	}
	got, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	requireSameJSON(t, readFixture(t, "enclave-delivery-error.json"), got)

	info, ok := resp.Error.Info()
	if !ok || info.Reason != DeliveryAdapterUnreachable || info.Metadata["member"] != "worker-1" {
		t.Fatalf("info %+v ok=%v", info, ok)
	}
}

func TestTaskStateUnspecifiedRejected(t *testing.T) {
	inputs := map[string]string{
		"task":          `{"task":{"id":"t","status":{"state":"TASK_STATE_UNSPECIFIED"}}}`,
		"status update": `{"statusUpdate":{"taskId":"t","contextId":"c","status":{"state":"TASK_STATE_UNSPECIFIED"}}}`,
		"empty state":   `{"task":{"id":"t","status":{"state":""}}}`,
		"0.3 spelling":  `{"task":{"id":"t","status":{"state":"completed"}}}`,
		"number":        `{"task":{"id":"t","status":{"state":3}}}`,
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeResult(json.RawMessage(in)); err == nil {
				t.Fatalf("want error for %s", in)
			}
		})
	}

	var task Task
	if err := json.Unmarshal([]byte(`{"id":"t","status":{"state":"TASK_STATE_UNSPECIFIED"}}`), &task); err == nil {
		t.Fatal("Task decode must reject TASK_STATE_UNSPECIFIED")
	}
	for _, s := range []TaskState{"", TaskStateUnspecified, "bogus"} {
		if _, err := json.Marshal(Task{ID: "t", Status: TaskStatus{State: s}}); err == nil {
			t.Fatalf("encoding state %q must fail", s)
		}
	}
}

func TestTaskStateTerminal(t *testing.T) {
	terminal := map[TaskState]bool{
		TaskStateSubmitted: false, TaskStateWorking: false, TaskStateInputRequired: false,
		TaskStateAuthRequired: false, TaskStateCompleted: true, TaskStateCanceled: true,
		TaskStateFailed: true, TaskStateRejected: true, TaskStateUnspecified: false,
	}
	for s, want := range terminal {
		if s.Terminal() != want {
			t.Errorf("%s.Terminal() = %v", s, !want)
		}
	}
	if TaskStateUnspecified.Valid() {
		t.Error("TASK_STATE_UNSPECIFIED must not be valid")
	}
}

func TestResultOneofRejected(t *testing.T) {
	inputs := map[string]string{
		"zero members":     `{}`,
		"unknown only":     `{"kind":"task","foo":{}}`,
		"null member":      `{"task":null}`,
		"two members":      `{"task":{"id":"t","status":{"state":"TASK_STATE_WORKING"}},"message":{"messageId":"m","role":"ROLE_AGENT","parts":[{"text":"x"}]}}`,
		"status and art":   `{"statusUpdate":{"taskId":"t","contextId":"c","status":{"state":"TASK_STATE_WORKING"}},"artifactUpdate":{"taskId":"t","contextId":"c","artifact":{"artifactId":"a","parts":[]}}}`,
		"not an object":    `[]`,
		"null":             `null`,
		"string":           `"task"`,
		"invalid JSON":     `{"task":`,
		"member not obj":   `{"task":42}`,
		"bad member field": `{"message":{"parts":"x"}}`,
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			if r, err := DecodeResult(json.RawMessage(in)); err == nil {
				t.Fatalf("want error, got %+v", r)
			}
			var r Result
			if err := json.Unmarshal([]byte(in), &r); err == nil {
				t.Fatal("Result.UnmarshalJSON must reject too")
			}
		})
	}

	// Unknown members next to one known member are ignored (spec 5.7).
	r, err := DecodeResult(json.RawMessage(`{"message":{"messageId":"m","role":"ROLE_AGENT","parts":[{"text":"x"}]},"future":{}}`))
	if err != nil || r.Message == nil {
		t.Fatalf("unknown sibling member: %+v %v", r, err)
	}

	task := &Task{ID: "t", Status: TaskStatus{State: TaskStateWorking}}
	msg := &Message{MessageID: "m", Role: RoleAgent, Parts: []Part{TextPart("x")}}
	for name, r := range map[string]Result{
		"zero": {},
		"two":  {Task: task, Message: msg},
		"four": {Task: task, Message: msg, StatusUpdate: &TaskStatusUpdateEvent{}, ArtifactUpdate: &TaskArtifactUpdateEvent{}},
	} {
		if _, err := EncodeResult(r); err == nil {
			t.Errorf("EncodeResult(%s) must fail", name)
		}
		if _, err := json.Marshal(r); err == nil {
			t.Errorf("json.Marshal(%s) must fail", name)
		}
	}
}

func TestResultEmbeddedInResponse(t *testing.T) {
	result := Result{StatusUpdate: &TaskStatusUpdateEvent{
		TaskID: "t", ContextID: "c", Status: TaskStatus{State: TaskStateCompleted},
		Metadata: WithEnclaveMeta(nil, EnclaveMeta{Termination: TerminationNotApplicable}),
	}}
	raw, err := EncodeResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), `{"statusUpdate":`) {
		t.Fatalf("encoded %s", raw)
	}
	data, err := json.Marshal(Response{JSONRPC: JSONRPCVersion, ID: json.RawMessage("7"), Result: raw})
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResult(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	meta, ok, err := EnclaveMetaFrom(decoded.StatusUpdate.Metadata)
	if err != nil || !ok || meta.Termination != TerminationNotApplicable {
		t.Fatalf("meta %+v ok=%v err=%v", meta, ok, err)
	}
}
