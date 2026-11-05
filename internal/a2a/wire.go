// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package a2a is the hand-written subset of the A2A JSON-RPC binding that
// enclave speaks, plus the enclave-owned wire contract around it.
//
// Every spec-derived name lives in this file and follows the pin in
// docs/design/a2a/spec-pin.md (A2A v1.0.1, normative a2a.proto).
package a2a

import "encoding/json"

// ProtocolVersion is the Major.Minor wire version sent in HeaderVersion and the card.
const ProtocolVersion = "1.0"

// ProtocolBinding is the AgentInterface.protocolBinding value of the only binding we speak.
const ProtocolBinding = "JSONRPC"

// CardPath is the agent card path served by adapters and, under /peers/<name>, by the hub.
const CardPath = "/.well-known/agent-card.json"

// A2A service parameter headers (spec 3.2.6). An empty version means 0.3 and is rejected.
const (
	HeaderVersion    = "A2A-Version"
	HeaderExtensions = "A2A-Extensions"
)

// ExtensionURI identifies the enclave extension. It is declared required in
// every card and keys all enclave metadata: metadata[ExtensionURI] = EnclaveMeta.
const ExtensionURI = "https://enclave.eclipse.dev/a2a/ext/v1"

// JSON-RPC method names (spec 5.3).
const (
	MethodSendMessage          = "SendMessage"
	MethodSendStreamingMessage = "SendStreamingMessage"
	MethodGetTask              = "GetTask"
	MethodCancelTask           = "CancelTask"
	MethodSubscribeToTask      = "SubscribeToTask"
	// Answered with CodeUnsupportedOperation in v1 (interfaces.md section 11).
	MethodListTasks            = "ListTasks"
	MethodGetExtendedAgentCard = "GetExtendedAgentCard"
	// Answered with CodePushNotificationNotSupported in v1 (interfaces.md section 11).
	MethodCreateTaskPushNotificationConfig = "CreateTaskPushNotificationConfig"
	MethodGetTaskPushNotificationConfig    = "GetTaskPushNotificationConfig"
	MethodListTaskPushNotificationConfigs  = "ListTaskPushNotificationConfigs"
	MethodDeleteTaskPushNotificationConfig = "DeleteTaskPushNotificationConfig"
)

// TaskState is an A2A task state, serialized as the ProtoJSON enum name.
type TaskState string

const (
	// TaskStateUnspecified is the proto zero value; rejected on input, never emitted.
	TaskStateUnspecified TaskState = "TASK_STATE_UNSPECIFIED"
	TaskStateSubmitted   TaskState = "TASK_STATE_SUBMITTED"
	TaskStateWorking     TaskState = "TASK_STATE_WORKING"
	TaskStateCompleted   TaskState = "TASK_STATE_COMPLETED"
	TaskStateCanceled    TaskState = "TASK_STATE_CANCELED"
	TaskStateFailed      TaskState = "TASK_STATE_FAILED"
	// TaskStateRejected is never emitted in v1: refusals are JSON-RPC errors.
	TaskStateRejected TaskState = "TASK_STATE_REJECTED"
	// TaskStateInputRequired is never emitted in v1 (proposal 4).
	TaskStateInputRequired TaskState = "TASK_STATE_INPUT_REQUIRED"
	// TaskStateAuthRequired is never emitted in v1 (proposal 4).
	TaskStateAuthRequired TaskState = "TASK_STATE_AUTH_REQUIRED"
)

// Message roles.
const (
	RoleUser  = "ROLE_USER"
	RoleAgent = "ROLE_AGENT"
)

// A2A-defined JSON-RPC error codes (spec 9.5). A2A claims -32001 to -32099;
// enclave codes live outside it, in jsonrpc.go.
const (
	CodeTaskNotFound                 = -32001
	CodeTaskNotCancelable            = -32002
	CodePushNotificationNotSupported = -32003
	CodeUnsupportedOperation         = -32004
	CodeContentTypeNotSupported      = -32005
	CodeInvalidAgentResponse         = -32006
	CodeExtendedAgentCardNotConfig   = -32007
	CodeExtensionSupportRequired     = -32008
	CodeVersionNotSupported          = -32009
)

// EnclaveMeta is the value under metadata[ExtensionURI], carrying the custom
// representations of proposal section 4 (carriers in spec-pin.md).
type EnclaveMeta struct {
	// Termination and Reason ride Task.metadata and the terminal
	// TaskStatusUpdateEvent.metadata; TaskStatus has no metadata field.
	Termination Termination  `json:"termination,omitempty"`
	Reason      EndingReason `json:"reason,omitempty"`
	// QueuePosition rides task metadata while submitted; 0 is next.
	QueuePosition *int `json:"queuePosition,omitempty"`
	// Retry rides Message.metadata; the client message ID is Message.MessageID.
	Retry bool `json:"retry,omitempty"`
	// Principal is written by the adapter only; a client-supplied value is rejected.
	Principal *Principal `json:"principal,omitempty"`
	// Truncated rides with Termination: the result artifact was cut to the result bound and ends with TruncatedMarker.
	Truncated bool `json:"truncated,omitempty"`
}

// CardExtensionParams is AgentExtension.params of the enclave extension.
type CardExtensionParams struct {
	QueueDepth int `json:"queueDepth"`
}

// Part is a message or artifact part. v1 accepts text parts only; the other
// content members are decoded so that they can be refused.
type Part struct {
	Text      *string         `json:"text,omitempty"`
	Raw       string          `json:"raw,omitempty"`
	URL       string          `json:"url,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
}

type Message struct {
	MessageID        string         `json:"messageId"`
	ContextID        string         `json:"contextId,omitempty"`
	TaskID           string         `json:"taskId,omitempty"`
	Role             string         `json:"role"`
	Parts            []Part         `json:"parts"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	Extensions       []string       `json:"extensions,omitempty"`
	ReferenceTaskIDs []string       `json:"referenceTaskIds,omitempty"`
}

type TaskStatus struct {
	State     TaskState `json:"state"`
	Message   *Message  `json:"message,omitempty"`
	Timestamp string    `json:"timestamp,omitempty"`
}

type Artifact struct {
	ArtifactID  string         `json:"artifactId"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []Part         `json:"parts"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Extensions  []string       `json:"extensions,omitempty"`
}

type Task struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId,omitempty"`
	Status    TaskStatus     `json:"status"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	History   []Message      `json:"history,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// TaskStatusUpdateEvent has no final flag in 1.0; the stream closes at a terminal state.
type TaskStatusUpdateEvent struct {
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId"`
	Status    TaskStatus     `json:"status"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type TaskArtifactUpdateEvent struct {
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId"`
	Artifact  Artifact       `json:"artifact"`
	Append    bool           `json:"append,omitempty"`
	LastChunk bool           `json:"lastChunk,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// SendMessageConfiguration: send blocks by default; ReturnImmediately returns after task creation.
type SendMessageConfiguration struct {
	AcceptedOutputModes []string `json:"acceptedOutputModes,omitempty"`
	HistoryLength       *int     `json:"historyLength,omitempty"`
	ReturnImmediately   bool     `json:"returnImmediately,omitempty"`
}

// SendMessageRequest is the params of SendMessage and SendStreamingMessage.
type SendMessageRequest struct {
	Tenant        string                    `json:"tenant,omitempty"`
	Message       Message                   `json:"message"`
	Configuration *SendMessageConfiguration `json:"configuration,omitempty"`
	Metadata      map[string]any            `json:"metadata,omitempty"`
}

type GetTaskRequest struct {
	Tenant        string `json:"tenant,omitempty"`
	ID            string `json:"id"`
	HistoryLength *int   `json:"historyLength,omitempty"`
}

type CancelTaskRequest struct {
	Tenant   string         `json:"tenant,omitempty"`
	ID       string         `json:"id"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type SubscribeToTaskRequest struct {
	Tenant string `json:"tenant,omitempty"`
	ID     string `json:"id"`
}

type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	Tenant          string `json:"tenant,omitempty"`
	ProtocolVersion string `json:"protocolVersion"`
}

type AgentExtension struct {
	URI         string         `json:"uri"`
	Description string         `json:"description,omitempty"`
	Required    bool           `json:"required,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
}

type AgentCapabilities struct {
	Streaming         *bool            `json:"streaming,omitempty"`
	PushNotifications *bool            `json:"pushNotifications,omitempty"`
	Extensions        []AgentExtension `json:"extensions,omitempty"`
	ExtendedAgentCard *bool            `json:"extendedAgentCard,omitempty"`
}

type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// HTTPAuthSecurityScheme is the only scheme we declare (bearer).
type HTTPAuthSecurityScheme struct {
	Description  string `json:"description,omitempty"`
	Scheme       string `json:"scheme"`
	BearerFormat string `json:"bearerFormat,omitempty"`
}

// SecurityScheme is a oneof wrapper; v1 sets HTTPAuth only.
type SecurityScheme struct {
	HTTPAuth *HTTPAuthSecurityScheme `json:"httpAuthSecurityScheme,omitempty"`
}

// StringList is the proto wrapper used for scopes in SecurityRequirement.
type StringList struct {
	List []string `json:"list"`
}

type SecurityRequirement struct {
	Schemes map[string]StringList `json:"schemes"`
}

// AgentCard has no metadata field in 1.0; enclave data goes in the extension params.
type AgentCard struct {
	Name                 string                    `json:"name"`
	Description          string                    `json:"description"`
	SupportedInterfaces  []AgentInterface          `json:"supportedInterfaces"`
	Version              string                    `json:"version"`
	Capabilities         AgentCapabilities         `json:"capabilities"`
	SecuritySchemes      map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	SecurityRequirements []SecurityRequirement     `json:"securityRequirements,omitempty"`
	DefaultInputModes    []string                  `json:"defaultInputModes"`
	DefaultOutputModes   []string                  `json:"defaultOutputModes"`
	Skills               []AgentSkill              `json:"skills"`
}
