// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "strings"

const (
	EnvPrefix = "ENCLAVE_"

	EnvHome                     = EnvPrefix + "HOME"
	EnvAllowRoot                = EnvPrefix + "ALLOW_ROOT"
	EnvAllowSensitiveMounts     = EnvPrefix + "ALLOW_SENSITIVE_MOUNTS"
	EnvLogLevel                 = EnvPrefix + "LOG_LEVEL"
	EnvColor                    = EnvPrefix + "COLOR"
	EnvAgentUpdateIntervalHours = EnvPrefix + "AGENT_UPDATE_INTERVAL_HOURS"
	EnvNoRebuild                = EnvPrefix + "NO_REBUILD"
	EnvRuntimeUID               = EnvPrefix + "RUNTIME_UID"
	EnvRuntimeGID               = EnvPrefix + "RUNTIME_GID"
	EnvLoopbackPorts            = EnvPrefix + "LOOPBACK_PORTS"
	EnvIdeBridgePorts           = EnvPrefix + "IDE_BRIDGE_PORTS"
	EnvDNSGateway               = EnvPrefix + "DNS_GATEWAY"
	EnvDevcontainer             = EnvPrefix + "DEVCONTAINER"
	EnvDevcontainerRemoteUser   = EnvPrefix + "DEVCONTAINER_REMOTE_USER"
	EnvDevcontainerPostCreate   = EnvPrefix + "DEVCONTAINER_POST_CREATE"
	EnvDevcontainerPostStart    = EnvPrefix + "DEVCONTAINER_POST_START"
	EnvToolConfigDir            = EnvPrefix + "TOOL_CONFIG_DIR"
	EnvToolSkillsDir            = EnvPrefix + "TOOL_SKILLS_DIR"
	EnvToolSettingsTemplate     = EnvPrefix + "TOOL_SETTINGS_TEMPLATE"
	EnvToolSettingsTarget       = EnvPrefix + "TOOL_SETTINGS_TARGET"
	EnvAuthDir                  = EnvPrefix + "AUTH_DIR"
	EnvAuthFiles                = EnvPrefix + "AUTH_FILES"
	EnvNetworkLogFile           = EnvPrefix + "NETWORK_LOG_FILE"
	EnvNetworkLogMode           = EnvPrefix + "NETWORK_LOG_MODE"
	EnvNetworkCaptureDir        = EnvPrefix + "NETWORK_CAPTURE_DIR"
	EnvGatewayConfigDir         = EnvPrefix + "GATEWAY_CONFIG_DIR"
	EnvGatewayPublishedPorts    = EnvPrefix + "GATEWAY_PUBLISHED_PORTS"
	EnvFeatureAuthMap           = EnvPrefix + "FEATURE_AUTH_MAP"
	EnvYolo                     = EnvPrefix + "YOLO"
	EnvSecretPlaceholders       = EnvPrefix + "SECRET_PLACEHOLDERS"
	EnvSecretReleaseFile        = EnvPrefix + "SECRET_RELEASE_FILE"
	EnvGatewayTLSRoot           = EnvPrefix + "GATEWAY_TLS_ROOT"
	EnvGatewayProxyReadyFile    = EnvPrefix + "GATEWAY_PROXY_READY_FILE"
	EnvGatewaySession           = EnvPrefix + "GATEWAY_SESSION"
	EnvGatewayNetworkMode       = EnvPrefix + "GATEWAY_NETWORK_MODE"
	EnvGatewayCACertPath        = EnvPrefix + "GATEWAY_CA_CERT_PATH"
	EnvGatewayCABundlePath      = EnvPrefix + "GATEWAY_CA_BUNDLE_PATH"
	EnvImageInbox               = EnvPrefix + "IMAGE_INBOX"
	EnvBin                      = EnvPrefix + "BIN"
	EnvProjectRoot              = EnvPrefix + "PROJECT_ROOT"
	EnvProjectMount             = EnvPrefix + "PROJECT_MOUNT"
	EnvConfigDir                = EnvPrefix + "CONFIG_DIR"
	EnvGitName                  = EnvPrefix + "GIT_NAME"
	EnvGitEmail                 = EnvPrefix + "GIT_EMAIL"
)

// IsReservedEnvKey reports whether key is set by Enclave and must not be
// overridden by project or extension environment values.
func IsReservedEnvKey(key string) bool {
	switch key {
	case "PROJECT_DIR", "HOST_PROJECT_DIR", "TOOL":
		return true
	}
	return strings.HasPrefix(key, EnvPrefix)
}

// FilterReservedEnv splits KEY=VALUE entries into those Enclave may pass on
// and the keys it must drop because IsReservedEnvKey reserves them.
func FilterReservedEnv(values []string) (kept []string, dropped []string) {
	kept = make([]string, 0, len(values))
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		if IsReservedEnvKey(key) {
			dropped = append(dropped, key)
			continue
		}
		kept = append(kept, value)
	}
	return kept, dropped
}
