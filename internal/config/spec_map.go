// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"sort"
	"strings"

	"enclave/internal/model"
)

// validateServiceAuthMappings fails loudly when a network.serviceAuth or
// network.serviceDomains service id does not map to a declared
// credentials.sources id. buildSecrets only walks credentials.sources, so an
// unmatched id (e.g. a typo) is otherwise silently dropped: the secret ends up
// with no HTTP release rule and its token is injected as a raw env value
// instead of a proxy-swapped placeholder — a secret-leak risk.
func validateServiceAuthMappings(doc specDocument, specPath string) error {
	if doc.Network == nil {
		return nil
	}
	sources := map[string]struct{}{}
	if doc.Credentials != nil {
		for id := range doc.Credentials.Sources {
			sources[id] = struct{}{}
		}
	}
	for id := range doc.Network.ServiceAuth {
		if _, ok := sources[id]; !ok {
			return fmt.Errorf("%s: network.serviceAuth[%q] has no matching credentials.sources entry", specPath, id)
		}
	}
	for host, id := range doc.Network.ServiceDomains {
		if _, ok := sources[id]; !ok {
			return fmt.Errorf("%s: network.serviceDomains[%q] references service %q with no matching credentials.sources entry", specPath, host, id)
		}
	}
	return nil
}

// validateProxyManaged fails loudly when an environment.proxyManaged entry
// does not name a declared credentials.sources env alias. proxyManaged selects
// which aliases carry the proxy-swapped placeholder; a typo'd entry would
// otherwise silently demote the secret's real aliases to receiving the raw
// value in the container environment — the same typo class
// validateServiceAuthMappings guards against for service ids.
func validateProxyManaged(doc specDocument, specPath string) error {
	if doc.Environment == nil || len(doc.Environment.ProxyManaged) == 0 {
		return nil
	}
	declared := map[string]struct{}{}
	if doc.Credentials != nil {
		for _, src := range doc.Credentials.Sources {
			for _, env := range src.Env {
				declared[env] = struct{}{}
			}
		}
	}
	for _, name := range doc.Environment.ProxyManaged {
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("%s: environment.proxyManaged[%q] does not match any credentials.sources env alias", specPath, name)
		}
	}
	return nil
}

// validateEntrypointArgv rejects entrypoint argv tokens containing whitespace.
// specToProfile space-joins run+args into Profile.Command, which the runtime
// command builder re-splits on fields — a token like "My App" would silently
// arrive as two argv tokens, so it is a load error instead.
func validateEntrypointArgv(doc specDocument, specPath string) error {
	if doc.Sandbox == nil || doc.Sandbox.Entrypoint == nil {
		return nil
	}
	check := func(field string, tokens []string) error {
		for _, tok := range tokens {
			if strings.ContainsAny(tok, " \t\r\n") {
				return fmt.Errorf("%s: sandbox.entrypoint.%s token %q must not contain whitespace (argv tokens are joined into the profile command and re-split on spaces)", specPath, field, tok)
			}
		}
		return nil
	}
	if err := check("run", doc.Sandbox.Entrypoint.Run); err != nil {
		return err
	}
	return check("args", doc.Sandbox.Entrypoint.Args)
}

// normalizeInstallUser applies sbx's commands.install default: an omitted or
// blank user means root ("0").
func normalizeInstallUser(user string) string {
	if strings.TrimSpace(user) == "" {
		return "0"
	}
	return user
}

// buildSecrets reconstructs model.SecretConfig entries from the split
// credentials.sources + network.serviceDomains/serviceAuth representation.
func buildSecrets(doc specDocument) map[string]model.SecretConfig {
	if doc.Credentials == nil || len(doc.Credentials.Sources) == 0 {
		return nil
	}
	// service-id -> hosts, unioned from serviceDomains (host -> id inversion)
	// and each serviceAuth entry's own Hosts (enclave-native), then deduped
	// and sorted. serviceAuth.Hosts covers the multi-service-same-host case
	// (e.g. gitlab's three tokens on the same hosts) that the host->single-
	// service serviceDomains map cannot express.
	hostsByService := map[string][]string{}
	if doc.Network != nil {
		for host, id := range doc.Network.ServiceDomains {
			hostsByService[id] = append(hostsByService[id], host)
		}
		for id, auth := range doc.Network.ServiceAuth {
			hostsByService[id] = append(hostsByService[id], auth.Hosts...)
		}
		for id := range hostsByService {
			hostsByService[id] = sortDedupeStrings(hostsByService[id])
		}
	}
	out := make(map[string]model.SecretConfig, len(doc.Credentials.Sources))
	for id, src := range doc.Credentials.Sources {
		sc := model.SecretConfig{EnvVars: append([]string(nil), src.Env...)}
		if src.APIKey != nil {
			v := *src.APIKey
			sc.APIKey = &v
		}
		if src.File != nil {
			sc.File = &model.SecretFileSource{Path: src.File.Path, Parser: src.File.Parser}
		}
		sc.Priority = src.Priority
		if doc.Network != nil {
			if auth, ok := doc.Network.ServiceAuth[id]; ok {
				sc.Release = &model.SecretReleaseConfig{
					HTTP: &model.HTTPSecretReleaseConfig{
						Hosts:  hostsByService[id],
						Header: auth.HeaderName,
						Format: auth.ValueFormat,
					},
				}
			}
		}
		out[id] = sc
	}
	return out
}

// sortDedupeStrings returns a sorted copy of in with adjacent duplicates
// removed. Used to union serviceDomains- and serviceAuth-derived host lists
// deterministically before normalizeHosts runs.
func sortDedupeStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	sorted := append([]string(nil), in...)
	sort.Strings(sorted)
	out := make([]string, 0, len(sorted))
	for i, v := range sorted {
		if i == 0 || v != sorted[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// buildProviders maps specProvider entries onto model.ProviderConfig.
func buildProviders(doc specDocument) []model.ProviderConfig {
	if len(doc.Providers) == 0 {
		return nil
	}
	out := make([]model.ProviderConfig, 0, len(doc.Providers))
	for _, sp := range doc.Providers {
		pc := model.ProviderConfig{
			Name:                sp.Name,
			CredentialSecrets:   sp.Credentials,
			AuthFiles:           sp.AuthFiles,
			SecurestorageDirEnv: sp.SecurestorageDirEnv,
		}
		if sp.AuthSession != nil {
			checks := make([]model.AuthSessionCheck, 0, len(sp.AuthSession.Checks))
			for _, c := range sp.AuthSession.Checks {
				checks = append(checks, model.AuthSessionCheck{
					File:    c.File,
					Type:    c.Type,
					Pointer: c.Pointer,
				})
			}
			pc.AuthSession = &model.AuthSessionConfig{
				Mode:   sp.AuthSession.Mode,
				Checks: checks,
			}
		}
		if len(sp.OAuthPorts) > 0 {
			ports := make([]model.OAuthPortConfig, 0, len(sp.OAuthPorts))
			for _, p := range sp.OAuthPorts {
				ports = append(ports, model.OAuthPortConfig{
					Port:                            p.Port,
					AutoHintWhenNoSession:           p.AutoHintWhenNoSession,
					RequireMappingWhenNoCredentials: p.RequireMappingWhenNoCredentials,
				})
			}
			pc.OAuthPorts = ports
		}
		out = append(out, pc)
	}
	return out
}

// buildPorts maps specPort entries onto model.PortConfig.
func buildPorts(doc specDocument) []model.PortConfig {
	if len(doc.Ports) == 0 {
		return nil
	}
	out := make([]model.PortConfig, 0, len(doc.Ports))
	for _, sp := range doc.Ports {
		out = append(out, model.PortConfig{
			Container: sp.Container,
			Publish:   sp.Publish,
			Label:     sp.Label,
			OpenURL:   sp.OpenURL,
		})
	}
	return out
}

// specToProfile projects a sandbox (kind: sandbox) specDocument onto the
// runtime model.Profile used by the existing run path.
func specToProfile(doc specDocument) model.Profile {
	p := model.Profile{
		Name:      doc.Name,
		Providers: buildProviders(doc),
		Secrets:   buildSecrets(doc),
		Ports:     buildPorts(doc),
	}
	if doc.Network != nil {
		p.AllowedDomains = doc.Network.AllowedDomains
		p.DeniedDomains = doc.Network.DeniedDomains
	}
	if doc.Environment != nil {
		p.EnvVariables = doc.Environment.Variables
		p.ProxyManaged = doc.Environment.ProxyManaged
	}
	if doc.PostStart != nil {
		p.PostStart = &model.PostStartActions{OpenIDE: doc.PostStart.OpenIDE}
	}

	sb := doc.Sandbox
	if sb == nil {
		p.Command = doc.Name
		return p
	}

	if sb.Entrypoint != nil && len(sb.Entrypoint.Run) > 0 {
		// Args are folded after Run: Command is later split back into fields by
		// the runtime command builder, so trailing flags in entrypoint.args flow
		// through as additional argv tokens rather than being silently dropped.
		argv := append(append([]string(nil), sb.Entrypoint.Run...), sb.Entrypoint.Args...)
		p.Command = strings.Join(argv, " ")
	} else {
		p.Command = doc.Name
	}

	p.ContinueArgs = sb.ContinueArgs
	p.ResumeArgs = sb.ResumeArgs
	p.YoloFlag = sb.YoloFlag
	p.YoloEnabled = sb.YoloEnabled
	p.ConfigDir = sb.ConfigDir
	p.SkillsDir = sb.SkillsDir
	p.MemoryDir = sb.MemoryDir
	p.MemoryFiles = sb.MemoryFiles
	p.SettingsFile = sb.SettingsFile
	p.SettingsTarget = sb.SettingsTarget
	p.PassthroughPaths = sb.PassthroughPaths
	p.QEMUMinMemoryMiB = sb.QEMUMinMemoryMiB
	p.QEMUStoreCacheMmap = sb.QEMUStoreCacheMmap
	p.HostConfigDir = sb.HostConfigDir
	p.HostCredentialsFile = sb.HostCredentials
	p.HostOAuthJSON = sb.HostOAuthJSON

	return p
}

// specToExtension projects a specDocument (either kind) onto the runtime
// model.Extension used by the extension-loading path. It returns the raw
// priority/default* values plus an extensionManifestState recording which of
// them were explicitly set in the spec document, so the loader can apply
// kind-specific defaults via applyExtensionDefaults.
func specToExtension(doc specDocument) (model.Extension, extensionManifestState) {
	ext := model.Extension{
		Name:        doc.Name,
		Description: doc.Description,
		AptPackages: doc.AptPackages,
		NeedsRoot:   doc.NeedsRoot,
		ConfigDir:   doc.ConfigDir,
		AuthFiles:   doc.AuthFiles,
		Secrets:     buildSecrets(doc),
	}
	if doc.Network != nil {
		ext.AllowedDomains = doc.Network.AllowedDomains
		ext.DeniedDomains = doc.Network.DeniedDomains
	}
	if doc.Environment != nil {
		ext.EnvVariables = doc.Environment.Variables
		ext.ProxyManaged = doc.Environment.ProxyManaged
	}

	// No default case: an unknown kind intentionally leaves Type == "" here;
	// the loader rejects it during validation.
	switch doc.Kind {
	case KindMixin:
		ext.Type = model.ExtensionKindMixin
	case KindSandbox:
		ext.Type = model.ExtensionKindSandbox
	}

	// Record the per-entry user of each commands.install step in order. Its
	// presence marks a declarative install (mixin-only in practice; harmless for
	// sandboxes, which use an install.sh sidecar). An omitted user defaults to
	// "0" (root), matching the sbx kit format, so a plain sbx kit whose
	// apt/curl install step omits user still runs with the privileges it
	// expects rather than silently failing as the unprivileged agent. The shell
	// synthesizer defaults identically.
	if doc.Commands != nil {
		for _, c := range doc.Commands.Install {
			ext.InstallCommandUsers = append(ext.InstallCommandUsers, normalizeInstallUser(c.User))
		}
	}

	state := extensionManifestState{
		PrioritySet:        doc.Priority != nil,
		DefaultEnabledSet:  doc.DefaultEnabled != nil,
		DefaultIncludedSet: doc.DefaultIncluded != nil,
	}
	if state.PrioritySet {
		ext.Priority = *doc.Priority
	}
	if state.DefaultEnabledSet {
		ext.DefaultEnabled = *doc.DefaultEnabled
	}
	if state.DefaultIncludedSet {
		ext.DefaultIncluded = *doc.DefaultIncluded
	}

	return ext, state
}
