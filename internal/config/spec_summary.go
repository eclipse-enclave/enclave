// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"enclave/internal/model"
)

// SpecInitFileSummary describes one commands.initFiles entry. kit-init.sh
// writes the entry's content at the entry's mode on every container start, so
// both belong here: ContentDigest stands in for the content itself, which is
// arbitrarily long and is a file to inspect rather than a capability to read.
type SpecInitFileSummary struct {
	Path          string
	Mode          string
	OnlyIfMissing bool
	ContentDigest string
}

// SpecPortSummary describes one ports[] entry. Publish is carried separately
// from the port number because it, not the number, decides whether the port is
// bound on the host.
type SpecPortSummary struct {
	Container int
	Publish   bool
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
//
// The command-bearing fields carry the command text rather than a count, so a
// caller diffing two summaries notices a step that was rewritten in place.
// InstallCommands additionally prefixes "(root) " to a step that runs as root,
// which makes a step changing user visible even when the root count does not
// move.
type SpecSummary struct {
	Kind                  string
	Name                  string
	DisplayName           string
	Description           string
	NeedsRoot             bool
	AptPackages           []string
	InstallCommands       []string
	InstallCommandsAsRoot int
	StartupCommands       []string
	InitFiles             []SpecInitFileSummary
	AllowedDomains        []string
	DeniedDomains         []string
	Ports                 []SpecPortSummary
	CredentialEnv         []string
	CredentialSources     []SpecCredentialSource
	ProxyManaged          []string
	EnvironmentVars       []string
	EntrypointOverride    string
	ContinueArgs          string
	ResumeArgs            string
	PostStartOpenIDE      string
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
		for _, cmd := range doc.Commands.Install {
			text := specCommandText(cmd)
			if IsRootInstallUser(cmd.User) {
				summary.InstallCommandsAsRoot++
				text = "(root) " + text
			}
			summary.InstallCommands = append(summary.InstallCommands, text)
		}
		for _, cmd := range doc.Commands.Startup {
			summary.StartupCommands = append(summary.StartupCommands, specCommandText(cmd))
		}
		for _, file := range doc.Commands.InitFiles {
			summary.InitFiles = append(summary.InitFiles, SpecInitFileSummary{
				Path:          strings.TrimSpace(file.Path),
				Mode:          strings.TrimSpace(file.Mode),
				OnlyIfMissing: file.OnlyIfMissing,
				ContentDigest: shortContentDigest(file.Content),
			})
		}
	}
	if doc.Network != nil {
		summary.AllowedDomains = doc.Network.AllowedDomains
		summary.DeniedDomains = doc.Network.DeniedDomains
	}
	if doc.Environment != nil {
		summary.ProxyManaged = doc.Environment.ProxyManaged
		// Reported as NAME=value: the value is the capability. Setting
		// NODE_TLS_REJECT_UNAUTHORIZED to 0 is not the same grant as setting it
		// to 1, and a summary keyed on names alone cannot tell them apart.
		for _, name := range slices.Sorted(maps.Keys(doc.Environment.Variables)) {
			summary.EnvironmentVars = append(summary.EnvironmentVars, name+"="+doc.Environment.Variables[name])
		}
	}
	if doc.Credentials != nil {
		hostsByService := serviceHostsByID(doc)
		var aliases []string
		for _, id := range slices.Sorted(maps.Keys(doc.Credentials.Sources)) {
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
		summary.Ports = append(summary.Ports, SpecPortSummary{Container: port.Container, Publish: port.Publish})
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
	if doc.PostStart != nil {
		summary.PostStartOpenIDE = strings.TrimSpace(doc.PostStart.OpenIDE)
	}
	if doc.Sandbox != nil {
		if doc.Sandbox.Entrypoint != nil {
			summary.EntrypointOverride = entrypointCommand(doc.Sandbox.Entrypoint)
		}
		// continueArgs/resumeArgs are appended to the agent's argv for
		// `--continue`/`--resume`, so they grant whatever those flags grant —
		// including approval bypass — without going through sandbox.yoloFlag.
		summary.ContinueArgs = strings.Join(doc.Sandbox.ContinueArgs, " ")
		summary.ResumeArgs = strings.Join(doc.Sandbox.ResumeArgs, " ")
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

// shortContentDigest identifies a body of text by a truncated SHA-256, so a
// summary can report that it changed without carrying it. An empty body has no
// digest: there is nothing to identify.
func shortContentDigest(content string) string {
	if content == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:12]
}

// specCommandText renders a command's argv as one string. specCommand.Command
// is string or []string depending on the sbx stage, so both shapes are handled.
// The author's description is deliberately not used in its place: an update
// diff has to react to the shell that actually runs, and a description can stay
// identical while the command underneath it is rewritten.
func specCommandText(cmd specCommand) string {
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
