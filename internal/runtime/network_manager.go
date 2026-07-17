// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"fmt"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/mounts"
	"enclave/internal/network"
)

type networkManager struct {
	*Runtime
}

type NetworkResult struct {
	Network backend.NetworkPolicy
	Env     []string
	Ports   []backend.PortMapping
	Cleanup func()
}

func newNetworkManager(r *Runtime) networkManager {
	return networkManager{Runtime: r}
}

func (m networkManager) Prepare(_ string, authState model.AuthState, _ SecretMapping) (NetworkResult, error) {
	runCtx := model.RunContext{
		Host:       m.host,
		Project:    m.project,
		Profile:    m.profile,
		Run:        m.run,
		Auth:       m.auth,
		AuthState:  authState,
		Build:      m.build,
		RunSources: m.runSources,
	}
	loopbackPorts := m.handler.LoopbackPorts(runCtx)

	ports := mounts.ResolvePorts(m.run.Ports)

	resolved, err := m.resolveEffectivePolicy()
	if err != nil {
		return NetworkResult{}, fmt.Errorf("failed to resolve effective network policy: %w", err)
	}
	effectivePolicy := resolved.Effective

	if effectivePolicy.Mode == model.NetworkModeUnrestricted {
		logx.Warnf("Network restrictions disabled - container has unrestricted internet access")
		var env []string
		if len(loopbackPorts) > 0 {
			env = append(env, model.EnvLoopbackPorts+"="+strings.Join(loopbackPorts, ","))
		}
		if len(m.ideBridgePorts) > 0 {
			env = append(env, model.EnvIdeBridgePorts+"="+strings.Join(m.ideBridgePorts, ","))
		}
		return NetworkResult{
			Network: backend.NetworkPolicy{
				Mode:           backend.NetworkModeUnrestricted,
				LoopbackPorts:  append([]string(nil), loopbackPorts...),
				IdeBridgePorts: append([]string(nil), m.ideBridgePorts...),
			},
			Env:   env,
			Ports: ports,
		}, nil
	}

	if len(m.run.AllowDomains) > 0 {
		// Copy first to avoid mutating the resolved policy's shared backing array.
		effectivePolicy.Domains = append(append([]string(nil), effectivePolicy.Domains...), m.run.AllowDomains...)
		logx.Infof("Allowlisted ad-hoc domains: %s", strings.Join(m.run.AllowDomains, ", "))
	}

	// Render-ready domains: the allow set has exact and subdomain matches of
	// denied domains filtered out, and the denied set is carried alongside so
	// the startup gateway bundle blackholes them (deny-wins, same rendering
	// `enclave network print` shows).
	allowedDomains, deniedDomains, err := network.EffectiveRenderDomainsForTool(effectivePolicy, m.profile.Name)
	if err != nil {
		return NetworkResult{}, fmt.Errorf("failed to resolve effective network domains: %w", err)
	}

	env := []string{model.EnvDNSGateway + "=1"}
	logx.Infof("Network restricted to allowlisted domains via DNS gateway")
	policySourcePaths := policyDomainSourcePaths(m.host.Home, m.project.Dir, m.project.Hash, m.profile.Name, resolved.GlobalPolicy, resolved.ProjectPolicy)
	switch len(policySourcePaths) {
	case 1:
		logx.Infof("Merged policy domains from %s included", policySourcePaths[0])
	case 2:
		logx.Infof("Merged policy domains from %s and %s included", policySourcePaths[0], policySourcePaths[1])
	}
	if m.run.NetworkLog == model.NetworkLogRequests {
		logx.Infof("Request-level network logging enabled; forcing HTTPS MITM for allowlisted hosts")
	}

	return NetworkResult{
		Network: backend.NetworkPolicy{
			Mode: backend.NetworkModeRestricted,
			Egress: backend.EgressPolicy{
				AllowedDomains: allowedDomains,
				DeniedDomains:  deniedDomains,
				Resolvers:      append([]string(nil), effectivePolicy.Resolvers...),
				AllowlistPath:  resolved.AllowlistPath,
			},
			LoopbackPorts:  append([]string(nil), loopbackPorts...),
			IdeBridgePorts: append([]string(nil), m.ideBridgePorts...),
		},
		Env:   env,
		Ports: ports,
	}, nil
}

func policyDomainSourcePaths(home string, projectDir string, projectHash string, tool string, globalPolicy network.Policy, projectPolicy network.Policy) []string {
	paths := []string{}
	if policyHasDomains(globalPolicy, tool) {
		paths = append(paths, config.HostNetworkPolicyPath(home))
	}
	if projectDir != "" && policyHasDomains(projectPolicy, tool) {
		paths = append(paths, config.HostProjectNetworkPolicyPath(home, projectHash))
	}
	return paths
}

func policyHasDomains(p network.Policy, tool string) bool {
	if len(p.Domains.Global) > 0 {
		return true
	}
	return len(p.Domains.Tools[tool]) > 0
}
