// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
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

// policyResolverContext is what resolving a policy needs before a tool is
// chosen; gateway apply resolves per target with that gateway's tool.
type policyResolverContext struct {
	Home     string
	Project  model.Project
	Resolver policy.EffectiveResolver
}

type currentPolicyContext struct {
	policyResolverContext
	Tool     string
	Resolved policy.ResolveResult
}

// resolveCurrentPolicyContext resolves the policy from the tool spec and the
// policy files only, as running gateways are compared and reloaded against it.
func resolveCurrentPolicyContext(input *CommandInput) (currentPolicyContext, error) {
	return resolvePolicyContext(input, input.Options.Tool, nil)
}

// resolveSessionPolicyContext resolves the policy a run with the same options
// enforces. requireTool rejects a tool without a spec as a run rejects it.
func resolveSessionPolicyContext(input *CommandInput, requireTool bool) (currentPolicyContext, error) {
	opts, warnings, err := previewAsRun(input.Ctx, input.Options, input.Options.Sources)
	if err != nil {
		return currentPolicyContext{}, err
	}
	for _, warning := range warnings {
		logx.Warnf(warning)
	}
	profile, err := config.LoadProfile(input.Ctx.Paths, opts.Tool)
	if errors.Is(err, os.ErrNotExist) && requireTool {
		return currentPolicyContext{}, unknownToolError(input.Ctx.Paths, opts.Tool)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return currentPolicyContext{}, fmt.Errorf("load tool %q: %w", opts.Tool, err)
	}
	selection, err := resolveFeatureSelection(input.Ctx.Paths, opts.BuildOptions)
	for _, warning := range featureSelectionWarnings(selection, err) {
		logx.Warnf("%s", warning.Message)
	}
	session := policy.NewSessionNetworkInputs(profile, selection.enabled, opts.RunOptions)
	return resolvePolicyContext(input, opts.Tool, &session)
}

func unknownToolError(paths model.Paths, tool string) error {
	available, err := config.ListProfiles(paths)
	if err != nil || len(available) == 0 {
		return fmt.Errorf("unknown tool: %s", tool)
	}
	return fmt.Errorf("unknown tool: %s (available: %s)", tool, strings.Join(available, " "))
}

func resolvePolicyResolverContext(input *CommandInput) (policyResolverContext, error) {
	home, err := config.ResolveHostHome()
	if err != nil {
		return policyResolverContext{}, fmt.Errorf("resolve home: %w", err)
	}
	project, err := input.Ctx.Project()
	if err != nil {
		return policyResolverContext{}, fmt.Errorf("resolve project context: %w", err)
	}
	return policyResolverContext{
		Home:     home,
		Project:  project,
		Resolver: policy.NewEffectiveResolver(input.Ctx.Paths, home),
	}, nil
}

func resolvePolicyContext(input *CommandInput, tool string, session *policy.SessionNetworkInputs) (currentPolicyContext, error) {
	resolverCtx, err := resolvePolicyResolverContext(input)
	if err != nil {
		return currentPolicyContext{}, err
	}
	resolved, err := resolverCtx.Resolver.Resolve(policy.ResolveInput{
		ProjectDir:  input.Ctx.ProjectDir,
		ProjectHash: resolverCtx.Project.Hash,
		Tool:        tool,
		Session:     session,
	})
	if err != nil {
		return currentPolicyContext{}, fmt.Errorf("resolve effective policy: %w", err)
	}
	return currentPolicyContext{
		policyResolverContext: resolverCtx,
		Tool:                  tool,
		Resolved:              resolved,
	}, nil
}

func runNetworkStatus(input *CommandInput) int {
	policyCtx, err := resolveSessionPolicyContext(input, false)
	if err != nil {
		logx.Warnf("Cannot preview the session policy for these options (%v); showing the tool spec and policy files only", err)
		policyCtx, err = resolveCurrentPolicyContext(input)
	}
	if err != nil {
		logx.Errorf("Failed to resolve network policy: %v", err)
		return 1
	}
	ep := policyCtx.Resolved.Effective

	fmt.Printf("Network Policy Status\n")
	fmt.Printf("  Mode:           %s (source: %s)\n", ep.Mode, ep.ModeSource)
	fmt.Printf("  Tool:           %s\n", policyCtx.Tool)
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
	targets, scopeLabel, err := discoverGatewayTargets(input, manager, gatewayScopeProjectTool)
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

	var out strings.Builder
	printGatewayRuntimeStates(&out, manager, policyCtx.policyResolverContext, targets)
	fmt.Print(out.String())
	return 0
}

// printGatewayRuntimeStates compares each restricted gateway's running bundle
// with the persisted policy. Unrestricted gateways run no policy, so they are
// reported as such rather than compared.
func printGatewayRuntimeStates(out *strings.Builder, manager backend.GatewayManager, policyCtx policyResolverContext, targets []backend.GatewayInfo) {
	type runtimeState struct {
		target backend.GatewayInfo
		status gatewayRuntimeStatus
		detail string
	}
	outcomes := make([]runtimeState, 0, len(targets))
	counts := map[gatewayRuntimeStatus]int{}
	for _, target := range targets {
		status, detail := gatewayRuntimeState(manager, policyCtx, target)
		counts[status]++
		outcomes = append(outcomes, runtimeState{target: target, status: status, detail: detail})
	}

	fmt.Fprintf(out, "  In sync:         %d\n", counts[gatewayStateInSync])
	fmt.Fprintf(out, "  Needs apply:     %d\n", counts[gatewayStateStale])
	fmt.Fprintf(out, "  Unknown:         %d\n", counts[gatewayStateUnknown])
	if counts[gatewayStateUnrestricted] > 0 {
		fmt.Fprintf(out, "  Unrestricted:    %d\n", counts[gatewayStateUnrestricted])
	}
	fmt.Fprintf(out, "\nRunning gateways:\n")
	for _, outcome := range outcomes {
		label := gatewayTargetLabel(outcome.target)
		if outcome.detail == "" {
			fmt.Fprintf(out, "  - %s %s\n", outcome.status, label)
		} else {
			fmt.Fprintf(out, "  - %s %s: %s\n", outcome.status, label, outcome.detail)
		}
	}
}

type gatewayRuntimeStatus string

const (
	gatewayStateInSync       gatewayRuntimeStatus = "IN-SYNC"
	gatewayStateStale        gatewayRuntimeStatus = "STALE"
	gatewayStateUnknown      gatewayRuntimeStatus = "UNKNOWN"
	gatewayStateUnrestricted gatewayRuntimeStatus = "UNRESTRICTED"
)

func gatewayRuntimeState(manager backend.GatewayManager, policyCtx policyResolverContext, target backend.GatewayInfo) (status gatewayRuntimeStatus, detail string) {
	if target.Unrestricted() {
		return gatewayStateUnrestricted, "logs traffic without a policy; restart the session to change its network mode"
	}
	expectedBundleDir := config.HostProjectGatewayConfigDir(policyCtx.Home, target.ProjectHash, target.Tool)
	if err := manager.VerifyGatewayConfigMount(context.Background(), target.ID, expectedBundleDir); err != nil {
		return gatewayStateUnknown, err.Error()
	}
	targetProjectDir, err := resolveGatewayTargetProjectDir(policyCtx.Project, target)
	if err != nil {
		return gatewayStateUnknown, err.Error()
	}
	desiredHash, err := desiredGatewayBundleHash(policyCtx.Resolver, target, targetProjectDir)
	if err != nil {
		return gatewayStateStale, fmt.Sprintf("cannot render desired bundle: %v", err)
	}
	runningHash, err := hashGatewayBundleDir(expectedBundleDir)
	if err != nil {
		return gatewayStateUnknown, err.Error()
	}
	if desiredHash != runningHash {
		return gatewayStateStale, "persisted policy differs from running gateway config"
	}
	return gatewayStateInSync, ""
}

func runNetworkPrint(input *CommandInput) int {
	if input.NetworkPrintJSON {
		return writeNetworkPolicyJSON(os.Stdout, input)
	}
	policyCtx, err := resolveSessionPolicyContext(input, true)
	if err != nil {
		logx.Errorf("Failed to resolve network policy: %v", err)
		return 1
	}
	ep := policyCtx.Resolved.Effective

	// Use the same resolver selection as the deployed bundle so the preview
	// faithfully reflects what the gateway runs.
	resolver := network.FirstIPv4Resolver(ep.Resolvers)
	out, err := network.RenderDnsmasqConfigPreviewForTool(ep, policyCtx.Tool, resolver)
	if err != nil {
		logx.Errorf("Failed to render dnsmasq config: %v", err)
		return 1
	}
	fmt.Print(out)
	return 0
}

func runNetworkDiff(input *CommandInput) int {
	policyCtx, err := resolveSessionPolicyContext(input, false)
	if err != nil {
		logx.Errorf("Failed to resolve network policy: %v", err)
		return 1
	}
	ep := policyCtx.Resolved.Effective

	// Extract built-in domains for comparison
	var builtInDomains []string
	if policyCtx.Resolved.AllowlistPath != "" {
		builtInDomains, _ = network.ExtractDomainsRecursive(policyCtx.Resolved.AllowlistPath, input.Ctx.Paths.AllowlistsDir)
	}

	diff := network.Diff(ep, builtInDomains, policyCtx.Tool)
	fmt.Print(network.FormatDiff(diff))
	return 0
}

func runNetworkApply(input *CommandInput) int {
	return applyPolicyToGateways(input, applyGatewayScope(input.Options.AllRunning))
}

func applyPolicyToGateways(input *CommandInput, scope gatewayTargetScope) int {
	if code := requireDocker(); code != 0 {
		return code
	}

	policyCtx, err := resolvePolicyResolverContext(input)
	if err != nil {
		logx.Errorf("Failed to resolve current policy context: %v", err)
		return 1
	}

	manager, code := gatewayManagerForInput(input)
	if code != 0 {
		return code
	}
	targets, scopeLabel, err := discoverGatewayTargets(input, manager, scope)
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
	var out strings.Builder
	code = applyPolicyToTargets(&out, manager, policyCtx, targets, scope)
	fmt.Print(out.String())
	return code
}

// applyPolicyToTargets writes the persisted policy into each restricted
// gateway's bundle and reloads it. Unrestricted gateways are skipped: a running
// gateway never switches modes, so they get no policy.
func applyPolicyToTargets(out *strings.Builder, manager backend.GatewayManager, policyCtx policyResolverContext, targets []backend.GatewayInfo, scope gatewayTargetScope) int {
	type applyOutcome struct {
		target  backend.GatewayInfo
		skipped bool
		err     error
	}
	outcomes := make([]applyOutcome, 0, len(targets))
	applied := 0
	failed := 0
	skipped := 0
	for _, target := range targets {
		if target.Unrestricted() {
			skipped++
			outcomes = append(outcomes, applyOutcome{target: target, skipped: true})
			continue
		}
		applyErr := applyPolicyToTarget(manager, policyCtx, target)
		if applyErr == nil {
			applied++
		} else {
			failed++
		}
		outcomes = append(outcomes, applyOutcome{target: target, err: applyErr})
	}

	fmt.Fprintf(out, "\nTargets:\n")
	var failedTools []string
	for _, outcome := range outcomes {
		target := outcome.target
		label := gatewayTargetLabel(target)
		switch {
		case outcome.skipped:
			fmt.Fprintf(out, "  - SKIPPED %s: unrestricted session; restart it to change its network mode\n", label)
		case outcome.err == nil:
			fmt.Fprintf(out, "  - APPLIED %s\n", label)
		default:
			fmt.Fprintf(out, "  - FAILED  %s: %v\n", label, outcome.err)
			failedTools = append(failedTools, target.Tool)
		}
	}

	if skipped > 0 {
		fmt.Fprintf(out, "\nApply summary: %d applied, %d failed, %d skipped (unrestricted)\n", applied, failed, skipped)
	} else {
		fmt.Fprintf(out, "\nApply summary: %d applied, %d failed\n", applied, failed)
	}
	if failed > 0 {
		retry := retryApplyCommands(scope, failedTools)
		if len(retry) == 0 {
			fmt.Fprintf(out, "Some gateways were not updated; restart affected sessions.\n")
		} else {
			fmt.Fprintf(out, "Some gateways were not updated; restart affected sessions or run %s after fixing errors.\n", strings.Join(retry, " and "))
		}
		return 1
	}
	return 0
}

func applyPolicyToTarget(manager backend.GatewayManager, policyCtx policyResolverContext, target backend.GatewayInfo) error {
	expectedBundleDir := config.HostProjectGatewayConfigDir(policyCtx.Home, target.ProjectHash, target.Tool)
	if err := manager.VerifyGatewayConfigMount(context.Background(), target.ID, expectedBundleDir); err != nil {
		return err
	}
	targetProjectDir, err := resolveGatewayTargetProjectDir(policyCtx.Project, target)
	if err != nil {
		return err
	}
	resolved, err := policyCtx.Resolver.Resolve(policy.ResolveInput{
		ProjectDir:  targetProjectDir,
		ProjectHash: target.ProjectHash,
		Tool:        target.Tool,
	})
	if err != nil {
		return err
	}
	if resolved.Effective.Mode == model.NetworkModeUnrestricted {
		return fmt.Errorf("live apply does not support unrestricted mode; restart the session to switch mode safely")
	}
	if err := bundle.WriteConfigBundle(bundle.BundleWriteConfig{
		Dir:    expectedBundleDir,
		Policy: resolved.Effective,
		Tool:   target.Tool,
	}); err != nil {
		return err
	}
	generation, err := readGatewayBundleGeneration(expectedBundleDir)
	if err != nil {
		return err
	}
	return manager.ReloadGatewayNetwork(context.Background(), target.ID, generation)
}

func gatewayTargetLabel(target backend.GatewayInfo) string {
	return fmt.Sprintf("%s (%s, tool=%s, project=%s)", target.Name, target.ShortID(), target.Tool, target.ProjectHash)
}

// retryApplyCommands names the apply runs that reach exactly the failed
// gateways' scope: a project-wide mutation can fail for a tool that a plain
// `network apply` would not target. A gateway without a tool label cannot be
// selected by --tool, so it only gets the restart advice.
func retryApplyCommands(scope gatewayTargetScope, failedTools []string) []string {
	if scope == gatewayScopeAllRunning {
		return []string{"'enclave network apply --all-running'"}
	}
	tools := slices.DeleteFunc(slices.Clone(failedTools), func(tool string) bool { return tool == "" })
	slices.Sort(tools)
	tools = slices.Compact(tools)
	commands := make([]string, 0, len(tools))
	for _, tool := range tools {
		commands = append(commands, fmt.Sprintf("'enclave network apply --tool %s'", tool))
	}
	return commands
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
	exitCode := applyPolicyToGateways(input, mutationGatewayScope(input.Options.AllRunning))
	if exitCode != 0 {
		fmt.Printf("Persistence succeeded but runtime apply failed. Running sessions may still enforce the previous policy.\n")
	}
	return exitCode
}
