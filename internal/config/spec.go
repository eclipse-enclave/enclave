// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import "enclave/internal/model"

// On-disk kinds and filenames for the spec.yaml format. The kind tokens are the
// same values as the runtime Extension.Type discriminator (model is the
// canonical source), so they are aliased rather than re-declared.
const (
	KindSandbox      = model.ExtensionKindSandbox
	KindMixin        = model.ExtensionKindMixin
	SpecFilename     = "spec.yaml"
	SpecFilenameJSON = "spec.json"
	// SpecSchemaVersion is the only schemaVersion this loader accepts. Bump it
	// (and branch on doc.SchemaVersion) when the on-disk schema evolves.
	SpecSchemaVersion = "1"
)

// specDocument is the full on-disk schema for an enclave extension.
// It is the parse target only; specToProfile / specToExtension project it
// onto the runtime model types. sigs.k8s.io/yaml uses json tags.
type specDocument struct {
	SchemaVersion string `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	DisplayName   string `json:"displayName,omitempty"`
	Description   string `json:"description,omitempty"`

	Sandbox   *specSandbox   `json:"sandbox,omitempty"`
	PostStart *specPostStart `json:"postStart,omitempty"`

	AgentContext string           `json:"agentContext,omitempty"` // reserved
	Commands     *specCommands    `json:"commands,omitempty"`
	Network      *specNetwork     `json:"network,omitempty"`
	Environment  *specEnvironment `json:"environment,omitempty"`
	Credentials  *specCredentials `json:"credentials,omitempty"`
	Providers    []specProvider   `json:"providers,omitempty"`
	Ports        []specPort       `json:"ports,omitempty"`

	// Build-selection metadata. Priority/aptPackages/needsRoot/failOnInstallError
	// and defaultEnabled are mixin-only (kind: mixin); defaultIncluded is
	// tool-only (kind: sandbox — whether an "all" selection includes the tool).
	Priority           *int     `json:"priority,omitempty"`
	AptPackages        []string `json:"aptPackages,omitempty"`
	NeedsRoot          bool     `json:"needsRoot,omitempty"`
	FailOnInstallError *bool    `json:"failOnInstallError,omitempty"`
	DefaultEnabled     *bool    `json:"defaultEnabled,omitempty"`  // mixin-only
	DefaultIncluded    *bool    `json:"defaultIncluded,omitempty"` // tool-only

	// mixin enclave-native auth (features that carry credentials, e.g. github-cli)
	ConfigDir string   `json:"configDir,omitempty"`
	AuthFiles []string `json:"authFiles,omitempty"`
}

type specSandbox struct {
	Image      string          `json:"image,omitempty"`      // hint only
	AIFilename string          `json:"aiFilename,omitempty"` // reserved (memory file)
	Entrypoint *specEntrypoint `json:"entrypoint,omitempty"`

	// enclave-native tool fields
	ConfigDir          string   `json:"configDir,omitempty"`
	SkillsDir          string   `json:"skillsDir,omitempty"`
	MemoryDir          string   `json:"memoryDir,omitempty"`
	MemoryFiles        []string `json:"memoryFiles,omitempty"`
	SettingsFile       string   `json:"settingsFile,omitempty"`
	SettingsTarget     string   `json:"settingsTarget,omitempty"`
	YoloFlag           string   `json:"yoloFlag,omitempty"`
	YoloEnabled        *bool    `json:"yoloEnabled,omitempty"`
	ContinueArgs       []string `json:"continueArgs,omitempty"`
	ResumeArgs         []string `json:"resumeArgs,omitempty"`
	PassthroughPaths   []string `json:"passthroughPaths,omitempty"`
	QEMUMinMemoryMiB   int      `json:"qemuMinMemoryMiB,omitempty"`
	QEMUStoreCacheMmap bool     `json:"qemuStoreCacheMmap,omitempty"`
	HostConfigDir      string   `json:"hostConfigDir,omitempty"`
	HostCredentials    string   `json:"hostCredentialsFile,omitempty"`
	HostOAuthJSON      string   `json:"hostOauthJson,omitempty"`
}

type specEntrypoint struct {
	Run  []string `json:"run,omitempty"`
	Args []string `json:"args,omitempty"`
}

// specPostStart mirrors model.PostStartActions: side effects performed once the
// container is running. Currently only host-IDE launch (e.g. Theia/Theia-Next).
type specPostStart struct {
	OpenIDE string `json:"openIDE,omitempty"`
}

type specCommands struct {
	Install   []specCommand  `json:"install,omitempty"`
	Startup   []specCommand  `json:"startup,omitempty"`
	InitFiles []specInitFile `json:"initFiles,omitempty"`
}

type specCommand struct {
	Command     interface{} `json:"command,omitempty"` // string or []string (sbx varies by stage)
	User        string      `json:"user,omitempty"`
	Background  bool        `json:"background,omitempty"`
	Description string      `json:"description,omitempty"`
}

type specInitFile struct {
	Path          string `json:"path,omitempty"`
	Content       string `json:"content,omitempty"`
	Mode          string `json:"mode,omitempty"`
	OnlyIfMissing bool   `json:"onlyIfMissing,omitempty"`
	Description   string `json:"description,omitempty"`
}

type specNetwork struct {
	AllowedDomains []string                   `json:"allowedDomains,omitempty"`
	DeniedDomains  []string                   `json:"deniedDomains,omitempty"`
	ServiceDomains map[string]string          `json:"serviceDomains,omitempty"`
	ServiceAuth    map[string]specServiceAuth `json:"serviceAuth,omitempty"`
}

type specServiceAuth struct {
	HeaderName  string `json:"headerName"`
	ValueFormat string `json:"valueFormat,omitempty"`
	// Hosts is enclave-native: it lets a service declare its own hosts
	// directly, needed when multiple services share the same host — which
	// sbx's host->single-service serviceDomains map cannot express. When set,
	// these hosts are unioned with any serviceDomains entries pointing at this
	// service id.
	Hosts []string `json:"hosts,omitempty"`
}

type specEnvironment struct {
	Variables    map[string]string `json:"variables,omitempty"`
	ProxyManaged []string          `json:"proxyManaged,omitempty"`
}

type specCredentials struct {
	Sources map[string]specCredentialSource `json:"sources,omitempty"`
}

type specCredentialSource struct {
	Env      []string            `json:"env,omitempty"`
	File     *specCredentialFile `json:"file,omitempty"`     // host file source
	Priority string              `json:"priority,omitempty"` // env-first | file-first
	APIKey   *bool               `json:"apiKey,omitempty"`   // enclave-native
}

type specCredentialFile struct {
	Path   string `json:"path"`
	Parser string `json:"parser,omitempty"`
}

type specProvider struct {
	Name                string           `json:"name"`
	Credentials         []string         `json:"credentials,omitempty"` // was credential_secrets
	AuthFiles           []string         `json:"authFiles,omitempty"`
	AuthSession         *specAuthSession `json:"authSession,omitempty"`
	OAuthPorts          []specOAuthPort  `json:"oauthPorts,omitempty"`
	SecurestorageDirEnv string           `json:"securestorageDirEnv,omitempty"`
}

type specAuthSession struct {
	Mode   string                 `json:"mode,omitempty"`
	Checks []specAuthSessionCheck `json:"checks,omitempty"`
}

type specAuthSessionCheck struct {
	File    string `json:"file"`
	Type    string `json:"type"`
	Pointer string `json:"pointer,omitempty"`
}

type specOAuthPort struct {
	Port                            string `json:"port"`
	AutoHintWhenNoSession           *bool  `json:"autoHintWhenNoSession,omitempty"`
	RequireMappingWhenNoCredentials *bool  `json:"requireMappingWhenNoCredentials,omitempty"`
}

type specPort struct {
	Container int    `json:"container"`
	Publish   bool   `json:"publish,omitempty"`
	Label     string `json:"label,omitempty"`
	OpenURL   string `json:"openUrl,omitempty"`
}
