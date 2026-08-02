// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

const (
	EnvPrefix = "ENCLAVE_"

	EnvHome                     = EnvPrefix + "HOME"
	EnvLogLevel                 = EnvPrefix + "LOG_LEVEL"
	EnvColor                    = EnvPrefix + "COLOR"
	EnvAgentUpdateIntervalHours = EnvPrefix + "AGENT_UPDATE_INTERVAL_HOURS"
	EnvRuntimeUID               = EnvPrefix + "RUNTIME_UID"
	EnvRuntimeGID               = EnvPrefix + "RUNTIME_GID"
	// EnvRootlessSandbox activates the nested user-namespace launcher in the
	// entrypoint: the container starts as (rootless) root, forks a child user
	// namespace mapping outer UID/GID 0 to the sandbox UID/GID, and runs the
	// agent there without capabilities. EnvSandboxPhase tracks the launcher
	// stage across the entrypoint re-executions.
	EnvRootlessSandbox        = EnvPrefix + "ROOTLESS_SANDBOX"
	EnvSandboxUID             = EnvPrefix + "SANDBOX_UID"
	EnvSandboxGID             = EnvPrefix + "SANDBOX_GID"
	EnvSandboxPhase           = EnvPrefix + "SANDBOX_PHASE"
	EnvLoopbackPorts          = EnvPrefix + "LOOPBACK_PORTS"
	EnvIdeBridgePorts         = EnvPrefix + "IDE_BRIDGE_PORTS"
	EnvDNSGateway             = EnvPrefix + "DNS_GATEWAY"
	EnvDevcontainer           = EnvPrefix + "DEVCONTAINER"
	EnvDevcontainerRemoteUser = EnvPrefix + "DEVCONTAINER_REMOTE_USER"
	EnvDevcontainerPostCreate = EnvPrefix + "DEVCONTAINER_POST_CREATE"
	EnvDevcontainerPostStart  = EnvPrefix + "DEVCONTAINER_POST_START"
	EnvToolConfigDir          = EnvPrefix + "TOOL_CONFIG_DIR"
	EnvToolSkillsDir          = EnvPrefix + "TOOL_SKILLS_DIR"
	EnvToolSettingsTemplate   = EnvPrefix + "TOOL_SETTINGS_TEMPLATE"
	EnvToolSettingsTarget     = EnvPrefix + "TOOL_SETTINGS_TARGET"
	EnvAuthDir                = EnvPrefix + "AUTH_DIR"
	EnvAuthFiles              = EnvPrefix + "AUTH_FILES"
	EnvNetworkLogFile         = EnvPrefix + "NETWORK_LOG_FILE"
	EnvNetworkLogMode         = EnvPrefix + "NETWORK_LOG_MODE"
	EnvGatewayConfigDir       = EnvPrefix + "GATEWAY_CONFIG_DIR"
	EnvFeatureAuthMap         = EnvPrefix + "FEATURE_AUTH_MAP"
	EnvYolo                   = EnvPrefix + "YOLO"
	EnvPlaywrightMCP          = EnvPrefix + "PLAYWRIGHT_MCP"
	EnvSecretReleaseFile      = EnvPrefix + "SECRET_RELEASE_FILE"
	EnvGatewayTLSRoot         = EnvPrefix + "GATEWAY_TLS_ROOT"
	EnvGatewayProxyReadyFile  = EnvPrefix + "GATEWAY_PROXY_READY_FILE"
	EnvGatewayCACertPath      = EnvPrefix + "GATEWAY_CA_CERT_PATH"
	EnvImageInbox             = EnvPrefix + "IMAGE_INBOX"
	EnvBin                    = EnvPrefix + "BIN"
	EnvProjectRoot            = EnvPrefix + "PROJECT_ROOT"
	EnvProjectMount           = EnvPrefix + "PROJECT_MOUNT"
	EnvConfigDir              = EnvPrefix + "CONFIG_DIR"
	EnvSessionMonitor         = EnvPrefix + "SESSION_MONITOR"
	// EnvSessionMonitorUser records the user that owns the managed tmux server
	// so `status` can capture as that user (the agent runs remapped under
	// --runtime-uid-remap, so the container's default exec user is not the
	// tmux owner). Carried into a label, not consumed by the entrypoint.
	EnvSessionMonitorUser = EnvPrefix + "SESSION_MONITOR_USER"
)
