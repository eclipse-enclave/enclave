// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"fmt"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/gateway/bundle"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/network"
	"enclave/internal/policy"
	"enclave/internal/util"
)

type currentPolicyContext struct {
	Home     string
	Project  model.Project
	Resolver policy.EffectiveResolver
	Resolved policy.ResolveResult
}

func resolveCurrentPolicyContext(input *CommandInput) (currentPolicyContext, error) {
	home, err := config.ResolveHostHome()
	if err != nil {
		return currentPolicyContext{}, fmt.Errorf("resolve home: %w", err)
	}
	project, err := input.Ctx.Project()
	if err != nil {
		return currentPolicyContext{}, fmt.Errorf("resolve project context: %w", err)
	}
	policyResolver := policy.NewEffectiveResolver(input.Ctx.Paths, home)
	resolved, err := policyResolver.Resolve(policy.ResolveInput{
		ProjectDir:  input.Ctx.ProjectDir,
		ProjectHash: project.Hash,
		Tool:        input.Options.Tool,
	})
	if err != nil {
		return currentPolicyContext{}, fmt.Errorf("resolve effective policy: %w", err)
	}
	return currentPolicyContext{
		Home:     home,
		Project:  project,
		Resolver: policyResolver,
		Resolved: resolved,
	}, nil
}

func runNetworkStatus(input *CommandInput) int {
	policyCtx, err := resolveCurrentPolicyContext(input)
	if err != nil {
		logx.Errorf("Failed to resolve current policy context: %v", err)
		return 1
	}
	ep := policyCtx.Resolved.Effective

	fmt.Printf("Network Policy Status\n")
	fmt.Printf("  Mode:           %s (source: %s)\n", ep.Mode, ep.ModeSource)
	fmt.Printf("  Tool:           %s\n", input.Options.Tool)
	fmt.Printf("  Inherit built-in: %v\n", ep.InheritToolAllowlist)
	fmt.Printf("  Global domains: %d\n", len(ep.Domains))
	toolDomainCount := 0
	for _, domains := range ep.ToolDomains {
		toolDomainCount += len(domains)
	}
	fmt.Printf("  Tool domains:   %d\n", toolDomainCount)

	if len(ep.Sources) > 0 {
		fmt.Printf("\nSources:\n")
		for _, s := range ep.Sources {
			if s.Path != "" {
				fmt.Printf("  - %s (%s)\n", s.Name, s.Path)
			} else {
				fmt.Printf("  - %s\n", s.Name)
			}
		}
	}

	globalPolicyPath := network.GlobalPolicyPath(policyCtx.Home)
	projectPolicyPath := network.ProjectPolicyPath(policyCtx.Home, policyCtx.Project.Hash)
	fmt.Printf("\nPolicy files:\n")
	if util.PathExists(globalPolicyPath) {
		fmt.Printf("  Global:  %s\n", globalPolicyPath)
	} else {
		fmt.Printf("  Global:  none\n")
	}
	if util.PathExists(projectPolicyPath) {
		fmt.Printf("  Project: %s\n", projectPolicyPath)
	} else {
		fmt.Printf("  Project: none\n")
	}

	fmt.Printf("\nRuntime policy state:\n")
	if err := checkDocker(); err != nil {
		fmt.Printf("  Docker: unavailable (%v)\n", err)
		return 0
	}

	manager, code := gatewayManagerForInput(input)
	if code != 0 {
		return code
	}
	targets, scopeLabel, err := discoverGatewayTargets(input, manager, false)
	if err != nil {
		fmt.Printf("  Discovery failed: %v\n", err)
		return 0
	}
	fmt.Printf("  Scope:           %s\n", scopeLabel)
	fmt.Printf("  Gateways matched: %d\n", len(targets))
	if len(targets) == 0 {
		fmt.Printf("  Status:          no running gateways matched\n")
		return 0
	}

	type runtimeState struct {
		target backend.GatewayInfo
		status string
		detail string
	}
	outcomes := make([]runtimeState, 0, len(targets))
	inSync := 0
	needsApply := 0
	unknown := 0
	for _, target := range targets {
		expectedBundleDir := config.HostProjectGatewayConfigDir(policyCtx.Home, target.ProjectHash, target.Tool)
		state := runtimeState{target: target, status: "IN-SYNC"}

		if err := manager.VerifyGatewayConfigMount(context.Background(), target.ID, expectedBundleDir); err != nil {
			state.status = "UNKNOWN"
			state.detail = err.Error()
			unknown++
			outcomes = append(outcomes, state)
			continue
		}
		targetProjectDir, err := resolveGatewayTargetProjectDir(policyCtx.Project, target)
		if err != nil {
			state.status = "UNKNOWN"
			state.detail = err.Error()
			unknown++
			outcomes = append(outcomes, state)
			continue
		}

		desiredHash, err := desiredGatewayBundleHash(policyCtx.Resolver, target, targetProjectDir)
		if err != nil {
			state.status = "STALE"
			state.detail = fmt.Sprintf("cannot render desired bundle: %v", err)
			needsApply++
			outcomes = append(outcomes, state)
			continue
		}
		runningHash, err := hashGatewayBundleDir(expectedBundleDir)
		if err != nil {
			state.status = "UNKNOWN"
			state.detail = err.Error()
			unknown++
			outcomes = append(outcomes, state)
			continue
		}
		if desiredHash != runningHash {
			state.status = "STALE"
			state.detail = "persisted policy differs from running gateway config"
			needsApply++
			outcomes = append(outcomes, state)
			continue
		}

		inSync++
		outcomes = append(outcomes, state)
	}

	fmt.Printf("  In sync:         %d\n", inSync)
	fmt.Printf("  Needs apply:     %d\n", needsApply)
	fmt.Printf("  Unknown:         %d\n", unknown)
	fmt.Printf("\nRunning gateways:\n")
	for _, outcome := range outcomes {
		label := fmt.Sprintf("%s (%s, tool=%s, project=%s)", outcome.target.Name, outcome.target.ShortID(), outcome.target.Tool, outcome.target.ProjectHash)
		if outcome.detail == "" {
			fmt.Printf("  - %s %s\n", outcome.status, label)
		} else {
			fmt.Printf("  - %s %s: %s\n", outcome.status, label, outcome.detail)
		}
	}

	return 0
}

func runNetworkPrint(input *CommandInput) int {
	policyCtx, err := resolveCurrentPolicyContext(input)
	if err != nil {
		logx.Errorf("Failed to resolve current policy context: %v", err)
		return 1
	}
	ep := policyCtx.Resolved.Effective

	// Use the same resolver selection as the deployed bundle so the preview
	// faithfully reflects what the gateway runs.
	resolver := network.FirstIPv4Resolver(ep.Resolvers)
	out, err := network.RenderDnsmasqConfigPreviewForTool(ep, input.Options.Tool, resolver)
	if err != nil {
		logx.Errorf("Failed to render dnsmasq config: %v", err)
		return 1
	}
	fmt.Print(out)
	return 0
}

func runNetworkDiff(input *CommandInput) int {
	policyCtx, err := resolveCurrentPolicyContext(input)
	if err != nil {
		logx.Errorf("Failed to resolve current policy context: %v", err)
		return 1
	}
	ep := policyCtx.Resolved.Effective

	// Extract built-in domains for comparison
	var builtInDomains []string
	if policyCtx.Resolved.AllowlistPath != "" {
		builtInDomains, _ = network.ExtractDomainsRecursive(policyCtx.Resolved.AllowlistPath, input.Ctx.Paths.AllowlistsDir)
	}

	diff := network.Diff(ep, builtInDomains, input.Options.Tool)
	fmt.Print(network.FormatDiff(diff))
	return 0
}

func runNetworkApply(input *CommandInput) int {
	if code := requireDocker(); code != 0 {
		return code
	}

	policyCtx, err := resolveCurrentPolicyContext(input)
	if err != nil {
		logx.Errorf("Failed to resolve current policy context: %v", err)
		return 1
	}

	manager, code := gatewayManagerForInput(input)
	if code != 0 {
		return code
	}
	targets, scopeLabel, err := discoverGatewayTargets(input, manager, input.Options.AllRunning)
	if err != nil {
		logx.Errorf("Failed to discover gateway targets: %v", err)
		return 1
	}

	fmt.Printf("Network Apply\n")
	fmt.Printf("  Scope:            %s\n", scopeLabel)
	fmt.Printf("  Gateways matched: %d\n", len(targets))
	if len(targets) == 0 {
		fmt.Printf("\nApply summary: 0 applied, 0 failed\n")
		return 0
	}

	type applyOutcome struct {
		target backend.GatewayInfo
		err    error
	}
	outcomes := make([]applyOutcome, 0, len(targets))
	applied := 0
	failed := 0
	for _, target := range targets {
		expectedBundleDir := config.HostProjectGatewayConfigDir(policyCtx.Home, target.ProjectHash, target.Tool)
		applyErr := manager.VerifyGatewayConfigMount(context.Background(), target.ID, expectedBundleDir)
		targetProjectDir := ""
		if applyErr == nil {
			targetProjectDir, applyErr = resolveGatewayTargetProjectDir(policyCtx.Project, target)
		}
		generation := ""
		if applyErr == nil {
			resolved, err := policyCtx.Resolver.Resolve(policy.ResolveInput{
				ProjectDir:  targetProjectDir,
				ProjectHash: target.ProjectHash,
				Tool:        target.Tool,
			})
			if err != nil {
				applyErr = err
			} else if strings.EqualFold(strings.TrimSpace(resolved.Effective.Mode), model.NetworkModeUnrestricted) {
				applyErr = fmt.Errorf("live apply does not support unrestricted mode; restart the session to switch mode safely")
			} else {
				applyErr = bundle.WriteConfigBundle(bundle.BundleWriteConfig{
					Dir:    expectedBundleDir,
					Policy: resolved.Effective,
					Tool:   target.Tool,
				})
			}
		}
		if applyErr == nil {
			generation, applyErr = readGatewayBundleGeneration(expectedBundleDir)
		}
		if applyErr == nil {
			applyErr = manager.ReloadGatewayNetwork(context.Background(), target.ID, generation)
		}
		if applyErr == nil {
			applied++
		} else {
			failed++
		}
		outcomes = append(outcomes, applyOutcome{target: target, err: applyErr})
	}

	fmt.Printf("\nTargets:\n")
	for _, outcome := range outcomes {
		target := outcome.target
		label := fmt.Sprintf("%s (%s, tool=%s, project=%s)", target.Name, target.ShortID(), target.Tool, target.ProjectHash)
		if outcome.err == nil {
			fmt.Printf("  - APPLIED %s\n", label)
			continue
		}
		fmt.Printf("  - FAILED  %s: %v\n", label, outcome.err)
	}

	fmt.Printf("\nApply summary: %d applied, %d failed\n", applied, failed)
	if failed > 0 {
		fmt.Printf("Some gateways were not updated; restart affected sessions or run 'enclave network apply' again after fixing errors.\n")
		return 1
	}
	return 0
}

func runNetworkAddDomain(input *CommandInput) int {
	if len(input.Options.CmdArgs) < 1 {
		logx.Errorf("domain argument required")
		return 1
	}
	domain := strings.ToLower(strings.TrimSpace(input.Options.CmdArgs[0]))
	normalizedDomain, err := network.NormalizePolicyDomain(domain)
	if err != nil {
		logx.Errorf("Invalid domain %q: %v", domain, err)
		return 1
	}
	domain = normalizedDomain
	return mutateGlobalPolicy(input, func(p *network.Policy) (bool, string, error) {
		for _, d := range p.Domains.Global {
			if strings.EqualFold(d, domain) {
				return false, fmt.Sprintf("Persistence: unchanged (%s already present in %%s)", domain), nil
			}
		}
		p.Domains.Global = append(p.Domains.Global, domain)
		return true, "Persistence: saved to %s", nil
	})
}

func runNetworkRemoveDomain(input *CommandInput) int {
	if len(input.Options.CmdArgs) < 1 {
		logx.Errorf("domain argument required")
		return 1
	}
	domain := strings.ToLower(strings.TrimSpace(input.Options.CmdArgs[0]))
	normalizedDomain, err := network.NormalizePolicyDomain(domain)
	if err != nil {
		logx.Errorf("Invalid domain %q: %v", domain, err)
		return 1
	}
	domain = normalizedDomain
	return mutateGlobalPolicy(input, func(p *network.Policy) (bool, string, error) {
		found := false
		filtered := make([]string, 0, len(p.Domains.Global))
		for _, d := range p.Domains.Global {
			if strings.EqualFold(d, domain) {
				found = true
				continue
			}
			filtered = append(filtered, d)
		}
		if !found {
			return false, "", fmt.Errorf("domain %s not found in global allowlist", domain)
		}
		p.Domains.Global = filtered
		return true, "Persistence: saved to %s", nil
	})
}

func runNetworkSetMode(input *CommandInput) int {
	if len(input.Options.CmdArgs) < 1 {
		logx.Errorf("mode argument required")
		return 1
	}
	mode := input.Options.CmdArgs[0]
	return mutateGlobalPolicy(input, func(p *network.Policy) (bool, string, error) {
		if strings.TrimSpace(p.Mode) == strings.TrimSpace(mode) {
			return false, fmt.Sprintf("Persistence: unchanged (mode already %s in %%s)", mode), nil
		}
		p.Mode = mode
		return true, "Persistence: saved to %s", nil
	})
}

func mutateGlobalPolicy(input *CommandInput, mutate func(*network.Policy) (changed bool, msg string, err error)) int {
	home, err := config.ResolveHostHome()
	if err != nil {
		logx.Errorf("Failed to resolve home: %v", err)
		return 1
	}
	policyPath := network.GlobalPolicyPath(home)
	p, err := network.LoadPolicy(policyPath)
	if err != nil {
		logx.Errorf("Failed to load policy: %v", err)
		return 1
	}
	changed, msg, err := mutate(&p)
	if err != nil {
		logx.Warnf("%v", err)
		return 1
	}
	if changed {
		if err := network.SavePolicy(policyPath, p); err != nil {
			logx.Errorf("Failed to save policy: %v", err)
			return 1
		}
	}
	if msg != "" {
		fmt.Printf(msg+"\n", policyPath)
	}
	return runMutationRuntimeApply(input)
}

func runMutationRuntimeApply(input *CommandInput) int {
	if input.Options.NoApply {
		fmt.Printf("Runtime apply: skipped (--no-apply)\n")
		return 0
	}

	fmt.Printf("\n")
	exitCode := runNetworkApply(input)
	if exitCode != 0 {
		fmt.Printf("Persistence succeeded but runtime apply failed. Running sessions may still enforce the previous policy.\n")
	}
	return exitCode
}
