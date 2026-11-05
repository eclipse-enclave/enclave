// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

// Enclave-owned wire contract between client, hub and adapter. Nothing here
// is spec-derived; see interfaces.md sections 6 and 11.

// Role is a member's exclusive, host-declared role (proposal 6).
type Role string

const (
	RoleOrchestrator Role = "orchestrator"
	RoleWorker       Role = "worker"
	// RoleHost is the group's host principal (proposal 7); never a session.
	RoleHost Role = "host"
)

// Principal is the host-provisioned identity the hub forwards on every request.
type Principal struct {
	Group           string `json:"group"`
	GroupGeneration string `json:"groupGeneration"`
	Member          string `json:"member"`
	Role            Role   `json:"role"`
	Generation      string `json:"generation"`
}

// Request headers. The hub strips every inbound header with HeaderPrefix and
// the Authorization header before forwarding.
const (
	HeaderAuthorization   = "Authorization"
	BearerPrefix          = "Bearer "
	HeaderPrefix          = "X-Enclave-A2A-"
	HeaderGroup           = "X-Enclave-A2A-Group"
	HeaderGroupGeneration = "X-Enclave-A2A-Group-Generation"
	HeaderMember          = "X-Enclave-A2A-Member"
	HeaderRole            = "X-Enclave-A2A-Role"
	HeaderGeneration      = "X-Enclave-A2A-Generation"
	HeaderEpoch           = "X-Enclave-A2A-Epoch"
	HeaderRevision        = "X-Enclave-A2A-Revision"
)

// Hub HTTP paths. Per-peer A2A traffic goes to PeerPathPrefix+<name>; the card
// is at PeerPathPrefix+<name>+CardPath.
const (
	HubDNSName      = "a2a.enclave"
	PeerPathPrefix  = "/peers/"
	HubPeersPath    = "/_enclave/peers"
	HubControlPath  = "/_enclave/control"
	HubLivenessPath = "/_enclave/liveness"
	AdapterRPCPath  = "/"
)

// Control and liveness stream event names.
const (
	ControlEventHello        = "hello"
	ControlEventOrchestrator = "orchestrator"
	ControlEventMembership   = "membership"
)

// ControlHello is the first event on the control and liveness streams.
type ControlHello struct {
	Epoch           string `json:"epoch"`
	Revision        uint64 `json:"revision"`
	GroupGeneration string `json:"groupGeneration"`
	// Principals is every current principal of Revision, the host principal included (CR-A10-4).
	Principals []MemberPrincipal `json:"principals"`
}

// ControlMembership is sent on the control stream for every revision the hub applies after hello.
type ControlMembership struct {
	Revision   uint64            `json:"revision"`
	Principals []MemberPrincipal `json:"principals"`
}

// MemberPrincipal is one current principal of a revision.
type MemberPrincipal struct {
	Member     string `json:"member"`
	Role       Role   `json:"role"`
	Generation string `json:"generation"`
}

// OrchestratorState reports the orchestrator liveness connection to adapters.
type OrchestratorState struct {
	Member     string `json:"member"`
	Generation string `json:"generation"`
	Connected  bool   `json:"connected"`
	Since      string `json:"since"`
}

// PeerListing is the body of HubPeersPath.
type PeerListing struct {
	Group           string      `json:"group"`
	GroupGeneration string      `json:"groupGeneration"`
	Revision        uint64      `json:"revision"`
	Epoch           string      `json:"epoch"`
	Peers           []PeerEntry `json:"peers"`
}

// PeerEntry is one worker in a PeerListing; orchestrator and host are not listed.
type PeerEntry struct {
	Name        string `json:"name"`
	Generation  string `json:"generation"`
	Description string `json:"description"`
	URL         string `json:"url"`
	CardURL     string `json:"cardUrl"`
}

// Termination is the adapter-set termination field (proposal 12).
type Termination string

const (
	TerminationConfirmed     Termination = "confirmed"
	TerminationUnconfirmed   Termination = "unconfirmed"
	TerminationNotApplicable Termination = "not-applicable"
)

// EndingReason is the structured ending reason (proposal 12).
type EndingReason string

const (
	ReasonCancel           EndingReason = "cancel"
	ReasonDeadline         EndingReason = "deadline"
	ReasonHubLost          EndingReason = "hub-lost"
	ReasonEpochChanged     EndingReason = "epoch-changed"
	ReasonOrchestratorLost EndingReason = "orchestrator-lost"
	ReasonOutcomeUnknown   EndingReason = "outcome-unknown"
)

// Delivery error reasons carried in ErrorInfo.Reason by the hub; the adapter
// also raises the last three.
const (
	DeliveryAdapterUnreachable = "ADAPTER_UNREACHABLE"
	DeliveryUnknownMember      = "UNKNOWN_MEMBER"
	DeliveryRevoked            = "REVOKED"
	DeliveryUnauthenticated    = "UNAUTHENTICATED"
	DeliveryRoleForbidden      = "ROLE_FORBIDDEN"
	DeliveryRequestTooLarge    = "REQUEST_TOO_LARGE"
)

// Adapter refusal reasons carried in ErrorInfo.Reason.
const (
	RefusalTaskNotFound      = "TASK_NOT_FOUND"
	RefusalContextUnknown    = "CONTEXT_UNKNOWN"
	RefusalContextExpired    = "CONTEXT_EXPIRED"
	RefusalMessageIDConflict = "MESSAGE_ID_CONFLICT"
	RefusalMessageIDReused   = "MESSAGE_ID_REUSED"
	RefusalRetryAmbiguous    = "RETRY_AMBIGUOUS"
	RefusalAdmissionStopped  = "ADMISSION_STOPPED"
	RefusalQuarantined       = "QUARANTINED"
	RefusalQueueFull         = "QUEUE_FULL"
	RefusalInvalidRequest    = "INVALID_REQUEST"
	RefusalNotCancelable     = "TASK_NOT_CANCELABLE"
	RefusalGenerationUnknown = "GENERATION_UNKNOWN"
	// RefusalEpochUnconfirmed admitted nothing, so the client may resend unchanged.
	RefusalEpochUnconfirmed = "EPOCH_UNCONFIRMED"
)

// TruncatedMarker ends a result artifact cut to the result bound (proposal 17);
// the marker counts toward the bound.
const TruncatedMarker = "\n\n[enclave: result truncated]"

// ProvenanceFormat is the provisional prompt prefix line, followed by a blank
// line; fields in order: group, member, role, generation, task, context.
const ProvenanceFormat = "[a2a group=%s from=%s role=%s generation=%s task=%s context=%s]"

// Adapter and client environment (interfaces.md section 6).
const (
	EnvRole               = "ENCLAVE_A2A_ROLE"
	EnvGroup              = "ENCLAVE_A2A_GROUP"
	EnvGroupGeneration    = "ENCLAVE_A2A_GROUP_GENERATION"
	EnvMember             = "ENCLAVE_A2A_MEMBER"
	EnvMemberGeneration   = "ENCLAVE_A2A_MEMBER_GENERATION"
	EnvHubHost            = "ENCLAVE_A2A_HUB_HOST"
	EnvHubPort            = "ENCLAVE_A2A_HUB_PORT"
	EnvAdapterPort        = "ENCLAVE_A2A_ADAPTER_PORT"
	EnvInboundToken       = "ENCLAVE_A2A_INBOUND_TOKEN"  // #nosec G101 -- variable name, not a credential.
	EnvOutboundToken      = "ENCLAVE_A2A_OUTBOUND_TOKEN" // #nosec G101 -- variable name, not a credential.
	EnvTableDir           = "ENCLAVE_A2A_TABLE_DIR"
	EnvDescription        = "ENCLAVE_A2A_DESCRIPTION"
	EnvQueueDepth         = "ENCLAVE_A2A_QUEUE_DEPTH"
	EnvTaskDeadline       = "ENCLAVE_A2A_TASK_DEADLINE"
	EnvSendWait           = "ENCLAVE_A2A_SEND_WAIT"
	EnvMaxRequestBytes    = "ENCLAVE_A2A_MAX_REQUEST_BYTES"
	EnvMaxResultBytes     = "ENCLAVE_A2A_MAX_RESULT_BYTES"
	EnvRecordLimit        = "ENCLAVE_A2A_RECORD_LIMIT"
	EnvRecordMaxAge       = "ENCLAVE_A2A_RECORD_MAX_AGE"
	EnvReconnectWindow    = "ENCLAVE_A2A_RECONNECT_WINDOW"
	EnvContextTTL         = "ENCLAVE_A2A_CONTEXT_TTL"
	EnvTerminationTimeout = "ENCLAVE_A2A_TERMINATION_TIMEOUT"
	EnvHeartbeatInterval  = "ENCLAVE_A2A_HEARTBEAT_INTERVAL"
	EnvHubLogMode         = "ENCLAVE_A2A_LOG"
	EnvHubRetainedResults = "ENCLAVE_A2A_HUB_RETAINED_RESULTS"
	EnvHubProxyUID        = "ENCLAVE_A2A_PROXY_UID"
	EnvHubSnapshotDir     = "ENCLAVE_A2A_HUB_SNAPSHOT_DIR"
	EnvHubStatusDir       = "ENCLAVE_A2A_HUB_STATUS_DIR"
	EnvHubAuditDir        = "ENCLAVE_A2A_HUB_AUDIT_DIR"
	// EnvHubEpoch is set by the supervisor so proxy and supervisor report one epoch (CR-A4-2).
	EnvHubEpoch = "ENCLAVE_A2A_HUB_EPOCH"
	// EnvHarness is the tool's sandbox.a2a block as JSON plus "yoloFlag", the profile's yolo flag (CR-A10-5).
	EnvHarness = "ENCLAVE_A2A_HARNESS"
	// EnvYolo is the session's existing yolo switch ("1"); it mirrors model.EnvYolo, which the agent does not import.
	EnvYolo = "ENCLAVE_YOLO"
)

// Worker control operations run by the host through engine exec
// ("enclave-a2a-agent ctl <op>"), proposal 13, 15, 19.
const (
	CtlDepart            = "depart"
	CtlReleaseAdmission  = "release-admission"
	CtlReleaseQuarantine = "release-quarantine"
	CtlStatus            = "status"
)

// CtlRequest is the JSON a ctl operation reads from stdin.
type CtlRequest struct {
	Op        string    `json:"op"`
	Principal Principal `json:"principal"`
	// Authoritative applies all three facts; a suspected departure stops
	// admission and discards queued work while running work continues (CR-A8-3).
	Authoritative bool `json:"authoritative,omitempty"`
}

// DepartureAck is the three-fact worker acknowledgment (proposal 13).
type DepartureAck struct {
	Worker                 string      `json:"worker"`
	WorkerGeneration       string      `json:"workerGeneration"`
	Principal              Principal   `json:"principal"`
	AdmissionStopPersisted bool        `json:"admissionStopPersisted"`
	QueuedDiscarded        []string    `json:"queuedDiscarded"`
	RunningTerminated      []string    `json:"runningTerminated"`
	Termination            Termination `json:"termination"`
	At                     string      `json:"at"`
}

// CtlResponse is the JSON a ctl operation writes to stdout.
type CtlResponse struct {
	Op          string        `json:"op"`
	OK          bool          `json:"ok"`
	Error       string        `json:"error,omitempty"`
	Departure   *DepartureAck `json:"departure,omitempty"`
	Quarantined *bool         `json:"quarantined,omitempty"`
	// DescendantsAlive answers release-quarantine: true keeps the quarantine.
	DescendantsAlive *bool `json:"descendantsAlive,omitempty"`
}
