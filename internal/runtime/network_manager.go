// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/filter"
	"enclave/internal/filterhost"
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
	if m.gatewayRuns() {
		if err := rejectGatewayProxyPorts(ports); err != nil {
			return NetworkResult{}, err
		}
	}

	if effectivePolicy.Mode == model.NetworkModeUnrestricted {
		logx.Warnf("Network restrictions disabled - container has unrestricted internet access")
		if m.run.NetworkLog == model.NetworkLogFull {
			logx.Warnf("--network-log=full captures nothing: full capture requires restricted networking")
		}
		if m.gatewayRuns() {
			return m.observedUnrestrictedNetwork(resolved.AllowlistPath, loopbackPorts, ports), nil
		}
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

	// Render-ready domains: the allow set has exact and subdomain matches of
	// denied domains, and ungoverned names below domains a request filter
	// confines, filtered out, and the denied set is carried alongside so
	// the startup gateway bundle blackholes them (deny-wins, same rendering
	// `enclave network print` shows).
	allowedDomains, deniedDomains, err := network.EffectiveRenderDomainsForTool(effectivePolicy, m.profile.Name)
	if err != nil {
		return NetworkResult{}, fmt.Errorf("failed to resolve effective network domains: %w", err)
	}
	logAdHocDomains(m.run.AllowDomains, func(domain string) bool {
		return network.DomainBlocked(domain, deniedDomains, effectivePolicy.ConfinedDomains, effectivePolicy.GovernedDomains)
	})
	logBlockedFilterHosts(m.filters, deniedDomains)
	if err := logConfinedDomains(m.filters, effectivePolicy, m.profile.Name, allowedDomains, deniedDomains); err != nil {
		return NetworkResult{}, err
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
	switch m.run.NetworkLog {
	case model.NetworkLogRequests:
		logx.Infof("Request-level network logging enabled; forcing HTTPS MITM for allowlisted hosts")
	case model.NetworkLogFull:
		logx.Warnf("Full network capture enabled: headers and bodies of allowlisted HTTP(S) requests are stored in %s", config.HostProjectNetworkCaptureDir(m.host.Home, m.project.Hash, m.profile.Name))
	}

	return NetworkResult{
		Network: backend.NetworkPolicy{
			Mode:    backend.NetworkModeRestricted,
			Gateway: m.gatewayRuns(),
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

// observedUnrestrictedNetwork is an unrestricted session with a gateway that
// only observes. As in restricted mode, the gateway owns the session's ports
// and bridges, so the session gets no forwarding env of its own; unlike it,
// the session gets no DNS-gateway signal and keeps Docker's resolver.
func (m networkManager) observedUnrestrictedNetwork(allowlistPath string, loopbackPorts []string, ports []backend.PortMapping) NetworkResult {
	logx.Infof("Network log records HTTP/HTTPS traffic on ports 80 and 443 (pass --disable-gateway to run without a gateway)")
	if m.run.NetworkLog == model.NetworkLogRequests || m.run.NetworkLog == model.NetworkLogFull {
		logx.Infof("Request-level network logging enabled; forcing HTTPS MITM for all hosts")
	}
	return NetworkResult{
		Network: backend.NetworkPolicy{
			Mode:    backend.NetworkModeUnrestricted,
			Gateway: true,
			// The gateway image is shared by both modes and built from the
			// allowlist, so both must build it from the same one.
			Egress:         backend.EgressPolicy{AllowlistPath: allowlistPath},
			LoopbackPorts:  append([]string(nil), loopbackPorts...),
			IdeBridgePorts: append([]string(nil), m.ideBridgePorts...),
		},
		Ports: ports,
	}
}

// rejectGatewayProxyPorts refuses to publish a container port the gateway's
// proxy listens on: the session shares the gateway's namespace, so nothing in
// the session could serve it.
func rejectGatewayProxyPorts(ports []backend.PortMapping) error {
	for _, port := range ports {
		// Compare numbers: "015080" publishes the same port as "15080".
		containerPort, err := strconv.Atoi(port.ContainerPort)
		if err != nil {
			continue
		}
		if containerPort == model.GatewayProxyHTTPPort || containerPort == model.GatewayProxyHTTPSPort {
			return fmt.Errorf("container port %s is reserved for the network gateway's proxy; publish another container port (or, with --allow-all-network, add --disable-gateway)", port.ContainerPort)
		}
	}
	return nil
}

func policyDomainSourcePaths(home string, projectDir string, projectHash string, tool string, globalPolicy network.Policy, projectPolicy network.Policy) []string {
	paths := []string{}
	if projectPolicy.InheritsGlobal() && policyHasDomains(globalPolicy, tool) {
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

// logAdHocDomains reports the --allow-domain entries the gateway allows, and
// separately those a deny rule or a filter's confine filtered out, so a
// blocked domain never reads as allowlisted.
func logAdHocDomains(adHoc []string, blocked func(domain string) bool) {
	kept, denied := partitionAdHocDomains(adHoc, blocked)
	if len(kept) > 0 {
		logx.Infof("Allowlisted ad-hoc domains: %s", strings.Join(kept, ", "))
	}
	if len(denied) > 0 {
		logx.Warnf("Ad-hoc domains blocked by a deny rule or a filter's confine: %s", strings.Join(denied, ", "))
	}
}

func partitionAdHocDomains(adHoc []string, blocked func(domain string) bool) (kept []string, denied []string) {
	for _, domain := range adHoc {
		canonical, err := network.CanonicalPolicyDomain(domain)
		if err != nil {
			canonical = domain
		}
		if blocked(canonical) {
			denied = append(denied, domain)
		} else {
			kept = append(kept, domain)
		}
	}
	return kept, denied
}

// blockedFilterHost is a host that enabled capabilities of a filter send
// requests to, all or part of which deny rules block.
type blockedFilterHost struct {
	filter       string
	host         string
	denied       []string
	capabilities []string
}

func (b blockedFilterHost) String() string {
	rules := "rule " + strings.Join(b.denied, ", ") + " covers"
	if len(b.denied) > 1 {
		rules = "rules " + strings.Join(b.denied, ", ") + " cover"
	}
	return fmt.Sprintf("Filter %s: deny %s %s, used by %s", b.filter, rules, b.host, strings.Join(b.capabilities, ", "))
}

// logBlockedFilterHosts warns about filter hosts that deny rules block, since
// the capabilities that use them fail.
func logBlockedFilterHosts(filters []filterhost.SessionFilter, deniedDomains []string) {
	for _, blocked := range blockedFilterHosts(filters, deniedDomains) {
		logx.Warnf("%s", blocked)
	}
}

func blockedFilterHosts(filters []filterhost.SessionFilter, deniedDomains []string) []blockedFilterHost {
	var blocked []blockedFilterHost
	for _, sf := range filters {
		if sf.Policy == nil {
			continue
		}
		hostCapabilities := sf.Policy.HostCapabilities()
		for _, host := range filter.SortedKeys(hostCapabilities) {
			if rules := denyRulesCovering(host, deniedDomains); len(rules) > 0 {
				blocked = append(blocked, blockedFilterHost{filter: sf.Filter.Name, host: host, denied: rules, capabilities: hostCapabilities[host]})
			}
		}
	}
	return blocked
}

// denyRulesCovering returns the deny rules that block a host pattern, or some
// of the names "*.domain" matches.
func denyRulesCovering(pattern string, deniedDomains []string) []string {
	domain, wildcard := strings.CutPrefix(pattern, "*.")
	var rules []string
	for _, rule := range deniedDomains {
		if network.DomainDeniedBy(domain, []string{rule}) || (wildcard && strings.HasSuffix(rule, "."+domain)) {
			rules = append(rules, rule)
		}
	}
	return rules
}

// logConfinedDomains reports, per filter, the allowlisted domains its confine
// removed, so a tool or feature domain that stops resolving is explained.
func logConfinedDomains(filters []filterhost.SessionFilter, policy network.EffectivePolicy, tool string, allowed []string, denied []string) error {
	listed, err := network.EffectiveDomainsForTool(policy, tool)
	if err != nil {
		return fmt.Errorf("failed to resolve effective network domains: %w", err)
	}
	for _, line := range confinedDomainLines(filters, listed, allowed, denied) {
		logx.Infof("%s", line)
	}
	return nil
}

// confinedDomainLines names, per filter, the listed domains below its
// confined domains that neither the rendered allow set nor a deny rule
// accounts for.
func confinedDomainLines(filters []filterhost.SessionFilter, listed []string, allowed []string, denied []string) []string {
	var lines []string
	for _, sf := range filters {
		confine := sf.Confine()
		var removed []string
		for _, domain := range listed {
			if network.DomainDeniedBy(domain, confine) && !slices.Contains(allowed, domain) && !network.DomainDeniedBy(domain, denied) {
				removed = append(removed, domain)
			}
		}
		if len(removed) > 0 {
			lines = append(lines, fmt.Sprintf("Filter %s confines %s, blocking allowlisted %s", sf.Filter.Name, strings.Join(confine, ", "), strings.Join(removed, ", ")))
		}
	}
	return lines
}
