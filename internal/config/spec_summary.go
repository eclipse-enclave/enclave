// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"enclave/internal/model"
)

// SpecInitFileSummary describes one commands.initFiles entry.
type SpecInitFileSummary struct {
	Path          string
	OnlyIfMissing bool
}

// SpecCredentialSource describes one credentials.sources entry: which env
// aliases it fills, which host file (if any) it can also be read from, and —
// the most consequential grant in the schema — whether it is released as an
// HTTP header to a set of hosts via network.serviceDomains/serviceAuth.
type SpecCredentialSource struct {
	ID            string
	Env           []string
	FilePath      string
	FileParser    string
	ReleaseHeader string
	ReleaseHosts  []string
}

// SpecProviderSummary projects one providers[] entry: the auth files and
// session detection it declares, the OAuth callback ports it opens, and the
// env var (if any) it uses to locate its credential-storage directory.
type SpecProviderSummary struct {
	Name                string
	AuthFiles           []string
	AuthSession         bool
	AuthSessionMode     string
	OAuthPorts          []string
	SecurestorageDirEnv string
}

// SpecHostExposure collects the fields that expose host filesystem paths or
// host credential files to the container: sandbox.* host-passthrough fields,
// and the mixin-native top-level configDir/authFiles pair used by
// enclave-native auth features (e.g. github-cli).
type SpecHostExposure struct {
	PassthroughPaths    []string
	HostConfigDir       string
	HostCredentialsFile string
	HostOAuthJSON       string
	MixinConfigDir      string
	MixinAuthFiles      []string
}

// SpecSummary projects the capability-relevant fields of a spec document. It
// exists so callers outside this package (the extension installer) can report
// what an extension will be able to do without duplicating the on-disk schema,
// which specDocument owns.
type SpecSummary struct {
	Kind                  string
	Name                  string
	DisplayName           string
	Description           string
	NeedsRoot             bool
	AptPackages           []string
	InstallCommands       int
	InstallCommandsAsRoot int
	StartupCommands       []string
	InitFiles             []SpecInitFileSummary
	AllowedDomains        []string
	DeniedDomains         []string
	Ports                 []int
	CredentialEnv         []string
	CredentialSources     []SpecCredentialSource
	ProxyManaged          []string
	EnvironmentVars       []string
	EntrypointOverride    string
	Providers             []SpecProviderSummary
	HostExposure          SpecHostExposure
	YoloFlag              string
	YoloEnabled           bool
	DefaultEnabled        *bool
	DefaultIncluded       *bool
}

// SummarizeSpecDir reads dir's spec.yaml (falling back to spec.json) and
// projects it onto a SpecSummary. It returns os.ErrNotExist when neither file
// is present, and a parse error for a malformed, unknown-field, or
// schemaVersion-mismatched document.
func SummarizeSpecDir(dir string) (SpecSummary, error) {
	specPath, ok := ownSpecFile(dir)
	if !ok {
		return SpecSummary{}, fmt.Errorf("no %s in %s: %w", SpecFilename, dir, os.ErrNotExist)
	}

	doc, err := parseSpecDocument(specPath)
	if err != nil {
		return SpecSummary{}, err
	}
	return summarizeSpecDocument(doc), nil
}

func summarizeSpecDocument(doc specDocument) SpecSummary {
	summary := SpecSummary{
		Kind:            strings.TrimSpace(doc.Kind),
		Name:            strings.TrimSpace(doc.Name),
		DisplayName:     strings.TrimSpace(doc.DisplayName),
		Description:     strings.TrimSpace(doc.Description),
		NeedsRoot:       doc.NeedsRoot,
		AptPackages:     doc.AptPackages,
		DefaultEnabled:  doc.DefaultEnabled,
		DefaultIncluded: doc.DefaultIncluded,
	}
	if doc.Commands != nil {
		summary.InstallCommands = len(doc.Commands.Install)
		for _, cmd := range doc.Commands.Install {
			if IsRootInstallUser(cmd.User) {
				summary.InstallCommandsAsRoot++
			}
		}
		for _, cmd := range doc.Commands.Startup {
			summary.StartupCommands = append(summary.StartupCommands, describeSpecCommand(cmd))
		}
		for _, file := range doc.Commands.InitFiles {
			summary.InitFiles = append(summary.InitFiles, SpecInitFileSummary{
				Path:          strings.TrimSpace(file.Path),
				OnlyIfMissing: file.OnlyIfMissing,
			})
		}
	}
	if doc.Network != nil {
		summary.AllowedDomains = doc.Network.AllowedDomains
		summary.DeniedDomains = doc.Network.DeniedDomains
	}
	if doc.Environment != nil {
		summary.ProxyManaged = doc.Environment.ProxyManaged
		for name := range doc.Environment.Variables {
			summary.EnvironmentVars = append(summary.EnvironmentVars, name)
		}
		sort.Strings(summary.EnvironmentVars)
	}
	if doc.Credentials != nil {
		hostsByService := serviceHostsByID(doc)
		ids := make([]string, 0, len(doc.Credentials.Sources))
		for id := range doc.Credentials.Sources {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var aliases []string
		for _, id := range ids {
			source := doc.Credentials.Sources[id]
			cs := SpecCredentialSource{ID: id, Env: append([]string(nil), source.Env...)}
			if source.File != nil {
				cs.FilePath = source.File.Path
				cs.FileParser = source.File.Parser
			}
			if doc.Network != nil {
				if auth, ok := doc.Network.ServiceAuth[id]; ok {
					cs.ReleaseHeader = auth.HeaderName
					cs.ReleaseHosts = hostsByService[id]
				}
			}
			summary.CredentialSources = append(summary.CredentialSources, cs)
			aliases = append(aliases, source.Env...)
		}
		summary.CredentialEnv = sortDedupeStrings(aliases)
	}
	for _, port := range doc.Ports {
		summary.Ports = append(summary.Ports, port.Container)
	}
	for _, p := range doc.Providers {
		ps := SpecProviderSummary{
			Name:                strings.TrimSpace(p.Name),
			AuthFiles:           append([]string(nil), p.AuthFiles...),
			SecurestorageDirEnv: p.SecurestorageDirEnv,
		}
		if p.AuthSession != nil {
			ps.AuthSession = true
			ps.AuthSessionMode = p.AuthSession.Mode
		}
		for _, op := range p.OAuthPorts {
			ps.OAuthPorts = append(ps.OAuthPorts, op.Port)
		}
		summary.Providers = append(summary.Providers, ps)
	}
	if doc.Sandbox != nil {
		if doc.Sandbox.Entrypoint != nil {
			summary.EntrypointOverride = entrypointCommand(doc.Sandbox.Entrypoint)
		}
		summary.HostExposure.PassthroughPaths = doc.Sandbox.PassthroughPaths
		summary.HostExposure.HostConfigDir = doc.Sandbox.HostConfigDir
		summary.HostExposure.HostCredentialsFile = doc.Sandbox.HostCredentials
		summary.HostExposure.HostOAuthJSON = doc.Sandbox.HostOAuthJSON
		summary.YoloFlag = strings.TrimSpace(doc.Sandbox.YoloFlag)
		summary.YoloEnabled = model.YoloEnabledValue(doc.Sandbox.YoloEnabled)
	}
	summary.HostExposure.MixinConfigDir = doc.ConfigDir
	summary.HostExposure.MixinAuthFiles = doc.AuthFiles
	return summary
}

// describeSpecCommand renders a startup command for display. specCommand.Command
// is string or []string depending on the sbx stage, so both shapes are handled.
func describeSpecCommand(cmd specCommand) string {
	if description := strings.TrimSpace(cmd.Description); description != "" {
		return description
	}
	switch value := cmd.Command.(type) {
	case string:
		return value
	case []interface{}:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, fmt.Sprint(item))
		}
		return strings.Join(parts, " ")
	default:
		return "(command)"
	}
}
