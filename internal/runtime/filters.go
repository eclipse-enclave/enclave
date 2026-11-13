// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/filterhost"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/policy"
	"enclave/internal/util"
)

// SetFilters selects the session's request filters. Call it before the
// session starts: their governed hosts join the session allowlist.
func (r *Runtime) SetFilters(filters []filterhost.SessionFilter) {
	r.filters = append([]filterhost.SessionFilter(nil), filters...)
	r.policyResolved = false
	r.policyResult = policy.ResolveResult{}
	r.policyErr = nil
}

// filterHosts are the hosts the selected filters govern.
func (r *Runtime) filterHosts() []string {
	var hosts []string
	for _, sf := range r.filters {
		hosts = append(hosts, sf.Hosts()...)
	}
	return hosts
}

// filterConfine are the domains the selected filters confine.
func (r *Runtime) filterConfine() []string {
	var domains []string
	for _, sf := range r.filters {
		domains = append(domains, sf.Confine()...)
	}
	return domains
}

// checkFilterEnforcement refuses to start a session with filters that no
// gateway would enforce. The app checks the options before building; this
// checks the session as it is about to run.
func (r *Runtime) checkFilterEnforcement() error {
	if len(r.filters) == 0 {
		return nil
	}
	if r.backend != nil && !r.backend.Capabilities().RequestFilters {
		return fmt.Errorf("request filters need the %s or %s backend (the %s backend cannot enforce them)", backend.NameDocker, backend.NamePodman, r.backend.Name())
	}
	if !r.networkRestricted() {
		return fmt.Errorf("request filters need restricted network mode")
	}
	if !r.gatewayRuns() {
		return fmt.Errorf("request filters need the network gateway")
	}
	if reason := r.rootAccess(); reason != "" {
		return fmt.Errorf("request filters need a session without root access, but %s", reason)
	}
	return nil
}

// rootAccess says why the session would have root, or "" if it would not.
// Root could take the gateway proxy's UID, which the gateway firewall trusts,
// and reach governed hosts directly.
func (r *Runtime) rootAccess() string {
	switch {
	case r.run.Admin:
		return "this is an admin session"
	case r.run.AllowRoot:
		return "--allow-root or ENCLAVE_ALLOW_ROOT is set"
	case r.build.RuntimeUIDRemap:
		return "--runtime-uid-remap starts it as root"
	case r.devcontainer != nil && isRootUser(r.devcontainerRemoteUser()) && (r.build.UseRemoteUser || r.run.Shell):
		return "it runs as the devcontainer remoteUser root"
	}
	return ""
}

func isRootUser(user string) bool {
	user = strings.TrimSpace(user)
	return user == "root" || user == "0"
}

// checkAdminExec refuses a root exec into a session whose gateway enforces
// request filters. It fails closed: when the gateways cannot be listed, it
// cannot tell and refuses.
func (r *Runtime) checkAdminExec(containerName string) error {
	if !r.run.Admin {
		return nil
	}
	manager, ok := r.backend.(backend.GatewayManager)
	if !ok {
		if !r.backend.Capabilities().RequestFilters {
			return nil
		}
		return fmt.Errorf("refusing exec --admin: cannot tell whether session %s has request filters", containerName)
	}
	gateways, err := manager.ListGateways(context.Background(), backend.GatewayFilter{})
	if err != nil {
		return fmt.Errorf("refusing exec --admin: cannot tell whether session %s has request filters: %w", containerName, err)
	}
	name := strings.TrimPrefix(containerName, "/")
	for _, gw := range gateways {
		if strings.TrimPrefix(gw.SessionContainer, "/") == name && len(gw.Filters) > 0 {
			return fmt.Errorf("refusing exec --admin: session %s has request filters (%s), which root could bypass", name, strings.Join(gw.Filters, ", "))
		}
	}
	return nil
}

// checkFilterReleaseHosts fails when a credential release rule, including one
// whose hosts were selected at session start, covers a governed host.
func (r *Runtime) checkFilterReleaseHosts(mapping SecretMapping) error {
	for _, entry := range mapping.Entries {
		if name, host, ok := filterhost.ReleaseHostCollision(r.filters, entry.Hosts, entry.ExactHosts); ok {
			return fmt.Errorf("filter %s governs %s, where credential %s is released", name, host, entry.SecretID)
		}
	}
	return nil
}

// addFilterSummaryMount writes the policy summary of each filter into a
// per-session host directory and mounts it read-only. The summaries hold
// neither the placeholder nor any credential.
func (r *Runtime) addFilterSummaryMount(mountArgs *mountAccumulator) error {
	if len(r.filters) == 0 {
		return nil
	}
	dir := config.HostProjectFilterSummaryDir(r.host.Home, r.project.Hash, r.profile.Name, r.sessionGeneratedKey())
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("reset filter summary directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- the summaries are non-secret and read by the container user.
		return fmt.Errorf("create filter summary directory: %w", err)
	}
	for _, sf := range r.filters {
		data, err := sf.SummaryJSON()
		if err != nil {
			return fmt.Errorf("encode filter %s summary: %w", sf.Filter.Name, err)
		}
		path := filepath.Join(dir, sf.Filter.Name+".json")
		if err := util.WriteFileAtomic(path, data, 0o644); err != nil {
			return fmt.Errorf("write filter %s summary: %w", sf.Filter.Name, err)
		}
	}
	mountArgs.AddMount(bindMount(dir, model.FilterSummaryContainerDir, true))
	return nil
}

// withFilterEnv sets the placeholder env vars, ENCLAVE_ACTIVE_FILTERS, and
// ENCLAVE_FILTER_SUMMARY_DIR. They win over any earlier entry of the same
// name; one from the project .env is dropped with a warning.
func (r *Runtime) withFilterEnv(env []string, projectEnv []string) []string {
	if len(r.filters) == 0 {
		return env
	}
	set := map[string]string{
		model.EnvActiveFilters:    strings.Join(filterhost.Labels(r.filters), ","),
		model.EnvFilterSummaryDir: model.FilterSummaryContainerDir,
	}
	for _, sf := range r.filters {
		for name, value := range sf.Env() {
			set[name] = value
		}
	}
	for _, entry := range projectEnv {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := set[key]; ok {
			logx.Warnf("Ignoring %s from the project .env file: a request filter sets it", key)
		}
	}
	out := make([]string, 0, len(env)+len(set))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := set[key]; ok {
			continue
		}
		out = append(out, entry)
	}
	for _, key := range slices.Sorted(maps.Keys(set)) {
		out = append(out, key+"="+set[key])
	}
	return out
}

// prepareFilterBundles checks the filters against the session's credential
// release rules and returns the gateway's share of them.
func (r *Runtime) prepareFilterBundles(mapping SecretMapping) ([]backend.FilterBundle, error) {
	if len(r.filters) == 0 {
		return nil, nil
	}
	if err := r.checkFilterReleaseHosts(mapping); err != nil {
		return nil, err
	}
	bundles := make([]backend.FilterBundle, 0, len(r.filters))
	for _, sf := range r.filters {
		bundle, err := sf.Bundle()
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, bundle)
	}
	return bundles, nil
}
