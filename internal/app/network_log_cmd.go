// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/netlog"
)

// sinceSession resolves --since against the most recent session marker in
// scope instead of a clock value.
const sinceSession = "session"

// networkLogTarget is one gateway's audit log.
type networkLogTarget struct {
	label string
	path  string
}

func runNetworkLog(input *CommandInput) int {
	view := input.NetworkLogView

	filter, err := netlog.Filter{
		Verdict: view.Verdict,
		Domain:  view.Domain,
		Type:    view.Type,
	}.Normalize()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	targets, scopeLabel, err := resolveNetworkLogTargets(input)
	if err != nil {
		logx.Errorf("Failed to resolve network log scope: %v", err)
		return 1
	}
	if len(targets) == 0 {
		logx.Errorf("No network log found for %s", scopeLabel)
		return 1
	}
	logx.Debugf("Reading network log for %s (%d target(s))", scopeLabel, len(targets))

	events, skipped, err := readNetworkLogEvents(targets)
	if err != nil {
		logx.Errorf("Failed to read network log: %v", err)
		return 1
	}
	if skipped > 0 {
		logx.Warnf("Skipped %d unreadable line(s); older logs contain raw dnsmasq output", skipped)
	}

	since, err := resolveNetworkLogSince(view.Since, events)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	filter.Since = since

	render := netlog.RenderOptions{Style: netlog.StyleMachine}
	if !view.Plain && !view.JSON && logx.ColorEnabledFor(os.Stdout) {
		render.Style = netlog.StyleHuman
		render.Color = true
	}

	out := bufio.NewWriter(os.Stdout)
	matched := filter.Apply(events)
	if view.Summary {
		if _, err := out.WriteString(netlog.RenderSummary(netlog.Aggregate(matched), render)); err != nil {
			logx.Errorf("Failed to write output: %v", err)
			return 1
		}
		return flushNetworkLog(out)
	}

	if err := writeNetworkLogEvents(out, matched, view.JSON, render); err != nil {
		logx.Errorf("Failed to write output: %v", err)
		return 1
	}
	if !view.Follow {
		if len(matched) == 0 && render.Style == netlog.StyleHuman {
			logx.Infof("No network events recorded for %s", scopeLabel)
		}
		return flushNetworkLog(out)
	}
	if code := flushNetworkLog(out); code != 0 {
		return code
	}
	return followNetworkLog(targets, filter, view.JSON, render, out)
}

func flushNetworkLog(out *bufio.Writer) int {
	if err := out.Flush(); err != nil {
		logx.Errorf("Failed to write output: %v", err)
		return 1
	}
	return 0
}

// resolveNetworkLogTargets scopes the read. The default case is a path
// computation, so the log of a session that has already exited is still
// readable; only --all-running and --session need Docker.
func resolveNetworkLogTargets(input *CommandInput) ([]networkLogTarget, string, error) {
	home, err := config.ResolveHostHome()
	if err != nil {
		return nil, "", fmt.Errorf("resolve home: %w", err)
	}

	sessionName := strings.TrimSpace(input.NetworkLogView.Session)
	if !input.Options.AllRunning && sessionName == "" {
		project, err := input.Ctx.Project()
		if err != nil {
			return nil, "", fmt.Errorf("resolve project context: %w", err)
		}
		label := fmt.Sprintf("current project/tool (%s/%s)", project.Hash, input.Options.Tool)
		return []networkLogTarget{{
			label: label,
			path:  config.HostProjectNetworkLogPath(home, project.Hash, input.Options.Tool),
		}}, label, nil
	}

	if err := checkDocker(); err != nil {
		return nil, "", err
	}
	manager, code := gatewayManagerForInput(input)
	if code != 0 {
		return nil, "", fmt.Errorf("select backend for gateway discovery")
	}
	gateways, scopeLabel, err := discoverGatewayTargets(input, manager, true)
	if err != nil {
		return nil, "", err
	}
	if sessionName != "" {
		gateways = filterGatewaysBySession(gateways, sessionName)
		scopeLabel = fmt.Sprintf("session %s", sessionName)
	}

	targets := make([]networkLogTarget, 0, len(gateways))
	for _, gateway := range gateways {
		targets = append(targets, networkLogTarget{
			label: gateway.Name,
			path:  config.HostProjectNetworkLogPath(home, gateway.ProjectHash, gateway.Tool),
		})
	}
	return targets, scopeLabel, nil
}

// filterGatewaysBySession accepts either the session container name or the
// gateway container name, since both are visible to a user reading `ps`.
func filterGatewaysBySession(gateways []backend.GatewayInfo, sessionName string) []backend.GatewayInfo {
	gatewayName := sessionName
	if !strings.HasSuffix(gatewayName, model.GatewayContainerSuffix) {
		gatewayName += model.GatewayContainerSuffix
	}
	matched := make([]backend.GatewayInfo, 0, 1)
	for _, gateway := range gateways {
		if gateway.Name == gatewayName || gateway.SessionContainer == sessionName {
			matched = append(matched, gateway)
		}
	}
	return matched
}

// readNetworkLogEvents reads every target, oldest generation first, so a
// rotation boundary is invisible. Events from several gateways are merged in
// timestamp order because their sessions interleave.
func readNetworkLogEvents(targets []networkLogTarget) ([]netlog.Event, int, error) {
	var events []netlog.Event
	skipped := 0
	for _, target := range targets {
		for _, path := range netlog.ReadPaths(target.path) {
			file, err := os.Open(path) // #nosec G304 -- path is derived from enclave's own state directory.
			if err != nil {
				return nil, skipped, fmt.Errorf("open %s: %w", path, err)
			}
			result, err := netlog.Scan(file)
			closeErr := file.Close()
			if err != nil {
				return nil, skipped, fmt.Errorf("read %s: %w", path, err)
			}
			if closeErr != nil {
				return nil, skipped, fmt.Errorf("close %s: %w", path, closeErr)
			}
			events = append(events, result.Events...)
			skipped += result.Skipped
		}
	}
	if len(targets) > 1 {
		sort.SliceStable(events, func(i, j int) bool {
			left, leftOK := events[i].Time()
			right, rightOK := events[j].Time()
			if !leftOK || !rightOK {
				return false
			}
			return left.Before(right)
		})
	}
	return events, skipped, nil
}

// resolveNetworkLogSince turns the flag into a lower bound. "session" resolves
// to the most recent session marker in scope, which is why it requires a scope
// covering exactly one session.
func resolveNetworkLogSince(value string, events []netlog.Event) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, nil
	}
	if strings.EqualFold(trimmed, sinceSession) {
		for i := len(events) - 1; i >= 0; i-- {
			if !events[i].IsSessionMarker() {
				continue
			}
			at, ok := events[i].Time()
			if !ok {
				continue
			}
			return at, nil
		}
		return time.Time{}, fmt.Errorf("--since session: no session marker in this log; it predates session markers")
	}
	if duration, err := time.ParseDuration(trimmed); err == nil {
		if duration < 0 {
			return time.Time{}, fmt.Errorf("--since %q must not be negative", value)
		}
		return time.Now().Add(-duration), nil
	}
	at, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, fmt.Errorf("--since %q: expected a duration (10m), an RFC3339 timestamp, or %q", value, sinceSession)
	}
	return at, nil
}

func writeNetworkLogEvents(out *bufio.Writer, events []netlog.Event, asJSON bool, render netlog.RenderOptions) error {
	if asJSON {
		for _, event := range events {
			if err := writeNetworkLogJSON(out, event); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := out.WriteString(netlog.RenderEvents(events, render))
	return err
}

func writeNetworkLogJSON(out *bufio.Writer, event netlog.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := out.Write(payload); err != nil {
		return err
	}
	return out.WriteByte('\n')
}

// followNetworkLog streams events appended after the backlog was printed. Only
// the live log is followed: the rotated generation never grows.
func followNetworkLog(targets []networkLogTarget, filter netlog.Filter, asJSON bool, render netlog.RenderOptions, out *bufio.Writer) int {
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.path)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var writeErr error
	follower := netlog.NewFollower(paths, netlog.FollowOptions{})
	err := follower.Run(ctx, func(event netlog.Event) {
		if writeErr != nil || !filter.Match(event) {
			return
		}
		if asJSON {
			writeErr = writeNetworkLogJSON(out, event)
		} else {
			_, writeErr = out.WriteString(netlog.RenderEvent(event, render) + "\n")
		}
		if writeErr == nil {
			writeErr = out.Flush()
		}
	})
	if writeErr != nil {
		logx.Errorf("Failed to write output: %v", writeErr)
		return 1
	}
	if err != nil {
		logx.Errorf("Failed to follow network log: %v", err)
		return 1
	}
	return flushNetworkLog(out)
}
