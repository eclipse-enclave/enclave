// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"enclave/internal/auth"
	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/config"
	"enclave/internal/envflag"
	"enclave/internal/filterhost"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/runtime"
)

// sessionFilterInput is what resolving a session's request filters reads.
type sessionFilterInput struct {
	paths    model.Paths
	home     string
	opts     model.Options
	profile  model.Profile
	features []model.Extension
	// secrets are the sources static credentials resolve from.
	secrets auth.SecretSources
}

// resolveSessionFilters resolves the filters a run selects. It fails before
// anything is built when a filter or preset is unknown, a credential is
// missing, or a filter collides with the session. Option validation already
// rejected every setup in which no gateway could enforce them.
func resolveSessionFilters(in sessionFilterInput) ([]filterhost.SessionFilter, error) {
	selections, err := filterhost.ParseSelection(in.opts.Filters)
	if err != nil || len(selections) == 0 {
		return nil, err
	}
	filters, err := filterhost.Resolve(filterhost.ResolveRequest{
		Paths:          in.paths,
		Home:           in.home,
		Entries:        in.opts.Filters,
		Secrets:        in.secrets,
		OnGrantProblem: warnGrantProblem,
	})
	if err != nil {
		return nil, err
	}
	if err := filterhost.CheckCollisions(filters, sessionFilterClaims(in.profile, in.features, in.opts)); err != nil {
		return nil, err
	}
	return filters, nil
}

// checkFilterRunOptions rejects a backend without the RequestFilters
// capability and, in one error, every option or policy mode that turns off
// the gateway or restricted mode or gives the session root. Root could take
// the gateway proxy's UID, which the gateway firewall trusts, and reach
// governed hosts directly.
func checkFilterRunOptions(opts model.Options, sources model.OptionSources, capabilities func() (backend.Capabilities, error), policyMode func() (string, error)) error {
	caps, err := capabilities()
	if err != nil {
		return err
	}
	if !caps.RequestFilters {
		return fmt.Errorf("request filters need the %s or %s backend (the %s backend cannot enforce them)", backend.NameDocker, backend.NamePodman, opts.Backend)
	}
	flag := func(cli bool, flagName string, key string) string {
		if cli {
			return flagName
		}
		return key + " in config"
	}
	var network, root []string
	if opts.DisableGateway {
		network = append(network, flag(sources.DisableGateway == model.SourceCLI, "--disable-gateway", "disable_gateway"))
	}
	if opts.AllowAllNetwork {
		network = append(network, flag(sources.AllowAllNetwork == model.SourceCLI, "--allow-all-network", "allow_all_network"))
	}
	if opts.Admin {
		root = append(root, "--admin")
	}
	if opts.AllowRoot {
		root = append(root, allowRootOrigins(sources)...)
	}
	if opts.RuntimeUIDRemap {
		root = append(root, "--runtime-uid-remap")
	}
	unrestrictedPolicy := false
	if policyMode != nil {
		mode, err := policyMode()
		if err != nil {
			return fmt.Errorf("resolve network mode for request filters: %w", err)
		}
		unrestrictedPolicy = mode == model.NetworkModeUnrestricted
	}

	var needs, fixes []string
	if len(network) > 0 || unrestrictedPolicy {
		needs = append(needs, "the network gateway in restricted mode")
	}
	if len(root) > 0 {
		needs = append(needs, "a session without root access")
	}
	if remove := slices.Concat(network, root); len(remove) > 0 {
		fixes = append(fixes, "remove "+joinWithAnd(remove))
	}
	if unrestrictedPolicy {
		fixes = append(fixes, fmt.Sprintf("set the network policy mode to restricted (see `%s network status`)", model.AppName))
	}
	if len(needs) == 0 {
		return nil
	}
	return fmt.Errorf("request filters need %s: %s", strings.Join(needs, " and "), strings.Join(fixes, " and "))
}

// allowRootOrigins names where the root opt-in came from: --allow-root,
// ENCLAVE_ALLOW_ROOT, or both. It has no config key.
func allowRootOrigins(sources model.OptionSources) []string {
	var origins []string
	if sources.AllowRoot == model.SourceCLI {
		origins = append(origins, "--allow-root")
	}
	if envflag.Truthy(os.LookupEnv(model.EnvAllowRoot)) {
		origins = append(origins, model.EnvAllowRoot+" in the environment")
	}
	if len(origins) == 0 {
		origins = append(origins, "--allow-root")
	}
	return origins
}

// joinWithAnd joins items as "a", "a and b", or "a, b, and c".
func joinWithAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}

// filtersSelected reports whether opts select at least one request filter.
// A selection that does not parse is reported later, when filters resolve.
func filtersSelected(opts model.Options) bool {
	selections, err := filterhost.ParseSelection(opts.Filters)
	return err == nil && len(selections) > 0
}

// filterRemovalWarnings warns about -name entries for filters that are not
// installed, grouped by where they were set, as for unknown feature removals.
// Removals that merging the layers consumed count too.
func filterRemovalWarnings(paths model.Paths, opts model.Options) []string {
	type group struct {
		label string
		names []string
	}
	var groups []group
	add := func(label string, removal string) {
		name, _, _ := strings.Cut(strings.TrimSpace(removal), ":")
		for i := range groups {
			if groups[i].label == label {
				groups[i].names = append(groups[i].names, name)
				return
			}
		}
		groups = append(groups, group{label: label, names: []string{name}})
	}
	for _, value := range opts.Filters {
		if removal, ok := strings.CutPrefix(strings.TrimSpace(value), "-"); ok {
			add(config.ListEntryOriginLabel("filters", opts.Origins, value), removal)
		}
	}
	for _, dropped := range opts.DroppedRemovals {
		if dropped.Option == "filters" {
			add(config.ListOriginLabel("filters", dropped.Origin), dropped.Name)
		}
	}
	if len(groups) == 0 {
		return nil
	}
	installed, err := config.ListFilterNames(paths)
	if err != nil {
		return nil
	}
	var warnings []string
	for _, g := range groups {
		var unknown []string
		for _, name := range g.names {
			if !slices.Contains(installed, name) && !slices.Contains(unknown, name) {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) == 0 {
			continue
		}
		sort.Strings(unknown)
		warnings = append(warnings, fmt.Sprintf("%s: ignoring removal of unknown filters: %s", g.label, strings.Join(unknown, ", ")))
	}
	return warnings
}

// sessionFilterClaims collects what the tool, the enabled features, and the
// options already claim: credential release hosts and container env vars.
func sessionFilterClaims(profile model.Profile, features []model.Extension, opts model.Options) filterhost.SessionClaims {
	claims := filterhost.SessionClaims{ReleaseHosts: map[string][]string{}, Env: map[string]string{}}
	add := func(owner string, secrets map[string]model.SecretConfig, vars map[string]string) {
		if hosts := model.ReleaseHosts(secrets); len(hosts) > 0 {
			claims.ReleaseHosts[owner] = hosts
		}
		for _, id := range sortedSecretIDs(secrets) {
			for _, name := range secrets[id].EnvVars {
				if _, taken := claims.Env[name]; !taken {
					claims.Env[name] = owner
				}
			}
		}
		for name := range vars {
			if _, taken := claims.Env[name]; !taken {
				claims.Env[name] = owner
			}
		}
	}
	add("tool "+profile.Name, profile.Secrets, profile.EnvVariables)
	for _, feature := range features {
		add("feature "+feature.Name, feature.Secrets, feature.EnvVariables)
	}
	for _, raw := range opts.PassEnv {
		op, value, err := config.ValidateListEntry("pass_env", raw)
		if err != nil || op == "-" || value == "none" {
			continue
		}
		name, _, _ := strings.Cut(value, "=")
		if _, taken := claims.Env[name]; !taken {
			claims.Env[name] = "pass_env"
		}
	}
	return claims
}

func sortedSecretIDs(secrets map[string]model.SecretConfig) []string {
	ids := make([]string, 0, len(secrets))
	for id := range secrets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// sessionSecretSources reads the host sources the declared credentials of a
// run resolve from, the way the runtime reads them for credentials.sources.
func sessionSecretSources(host model.Host, project model.Project, profile model.Profile, opts model.Options) auth.SecretSources {
	var storage backend.StoreManager
	if be, err := selectBackend(opts, backenddocker.Options{Host: host}); err == nil {
		storage = be.Storage()
	}
	return runtime.ReadSecretSources(runtime.SecretSourcesRequest{
		Home:        host.Home,
		ProjectHash: project.Hash,
		Tool:        profile.Name,
		Run:         opts.RunOptions,
		Auth:        opts.AuthOptions,
		Storage:     storage,
	})
}

// prepareSessionFilters resolves the run's filters, prints one line per
// filter, and records them in the session provenance.
func prepareSessionFilters(input *CommandInput, opts model.Options, project model.Project, profile model.Profile, features []model.Extension, provenance map[string]any) ([]filterhost.SessionFilter, error) {
	if !isRunAction(input.Action) {
		return nil, nil
	}
	if provenance != nil {
		provenance["filters"] = []string{}
	}
	selections, err := filterhost.ParseSelection(opts.Filters)
	if err != nil || len(selections) == 0 {
		return nil, err
	}
	host, err := input.Ctx.Host()
	if err != nil {
		return nil, err
	}
	filters, err := resolveSessionFilters(sessionFilterInput{
		paths:    input.Ctx.Paths,
		home:     host.Home,
		opts:     opts,
		profile:  profile,
		features: features,
		secrets:  sessionSecretSources(host, project, profile, opts),
	})
	if err != nil {
		return nil, err
	}
	for _, sf := range filters {
		logx.Infof("%s", sf.StartupLine())
	}
	if provenance != nil {
		provenance["filters"] = filterhost.Labels(filters)
	}
	return filters, nil
}
