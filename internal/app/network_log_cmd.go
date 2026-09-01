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
	"enclave/internal/netlogview"
	"enclave/internal/util"
)

// networkLogScope is the resolved read scope: which log files to read, how to
// name them in messages, and which session to restrict events to.
type networkLogScope struct {
	paths []string
	label string
	// session restricts events to one session. Concurrent sessions of the same
	// project and tool share a log file, so scoping the file is not enough.
	session string
}

func runNetworkLog(input *CommandInput) int {
	view := input.NetworkLogView

	filter, err := netlogview.Filter{
		Verdict: view.Verdict,
		Domain:  view.Domain,
		Type:    view.Type,
	}.Normalize()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	scope, err := resolveNetworkLogScope(input)
	if err != nil {
		logx.Errorf("Failed to resolve network log scope: %v", err)
		return 1
	}
	if len(scope.paths) == 0 {
		logx.Errorf("No network log found for %s", scope.label)
		return 1
	}
	filter.Session = scope.session
	logx.Debugf("Reading network log for %s (%d target(s))", scope.label, len(scope.paths))

	read, err := readNetworkLogEvents(scope.paths)
	if err != nil {
		logx.Errorf("Failed to read network log: %v", err)
		return 1
	}
	if read.skipped > 0 {
		logx.Warnf("Skipped %d unreadable line(s); older logs contain raw dnsmasq output", read.skipped)
	}
	// Every gateway start writes a marker stamped with its session, so a name
	// that appears on no event at all is not a session of this log.
	if scope.session != "" && !sessionRecorded(read.events, scope.session) {
		logx.Errorf("No events for %s; check the session name", scope.label)
		return 1
	}

	filter, err = applyNetworkLogSince(filter, view.Since, read.events)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	render := netlogview.RenderOptions{Color: logx.ColorEnabledFor(os.Stdout)}

	out := bufio.NewWriter(os.Stdout)
	matched := filter.Apply(read.events)
	if view.Summary {
		if err := writeNetworkLogSummary(out, netlogview.Aggregate(matched), view.JSON, render); err != nil {
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
		if len(matched) == 0 && !view.JSON {
			logx.Infof("No network events recorded for %s", scope.label)
		}
		return flushNetworkLog(out)
	}
	if code := flushNetworkLog(out); code != 0 {
		return code
	}
	return followNetworkLog(scope.paths, read.offsets, filter, view.JSON, render, out)
}

func flushNetworkLog(out *bufio.Writer) int {
	if err := out.Flush(); err != nil {
		logx.Errorf("Failed to write output: %v", err)
		return 1
	}
	return 0
}

// resolveNetworkLogScope scopes the read. Only --all-running needs Docker;
// --session uses it when reachable to find a session of another project, and
// every other case is a path computation, so the log of a session that has
// already exited stays readable.
func resolveNetworkLogScope(input *CommandInput) (networkLogScope, error) {
	home, err := config.ResolveHostHome()
	if err != nil {
		return networkLogScope{}, fmt.Errorf("resolve home: %w", err)
	}

	sessionName := strings.TrimSpace(input.NetworkLogView.Session)
	if input.Options.AllRunning {
		if err := checkDocker(); err != nil {
			return networkLogScope{}, err
		}
		return runningGatewayScope(input, home, "")
	}
	if sessionName == "" {
		return projectNetworkLogScope(input, home, "")
	}
	// A running session may belong to another project, so its gateway is looked
	// up first. An exited one left its events in this project's log, stamped with
	// its own name.
	if err := checkDocker(); err != nil {
		logx.Debugf("Reading this project's log because Docker is unavailable: %v", err)
	} else {
		scope, err := runningGatewayScope(input, home, sessionName)
		if err != nil {
			return networkLogScope{}, err
		}
		if len(scope.paths) > 0 {
			return scope, nil
		}
	}
	return projectNetworkLogScope(input, home, sessionContainerName(sessionName))
}

func projectNetworkLogScope(input *CommandInput, home string, session string) (networkLogScope, error) {
	project, err := input.Ctx.Project()
	if err != nil {
		return networkLogScope{}, fmt.Errorf("resolve project context: %w", err)
	}
	label := currentProjectToolLabel(project.Hash, input.Options.Tool)
	if session != "" {
		// A session of another tool is not in this file, so the label names the log.
		label = fmt.Sprintf("session %s in %s", session, label)
	}
	return networkLogScope{
		paths:   []string{config.HostProjectNetworkLogPath(home, project.Hash, input.Options.Tool)},
		label:   label,
		session: session,
	}, nil
}

// runningGatewayScope is the logs of the running gateways in scope, and needs a
// reachable Docker. An empty sessionName means every gateway it reports.
func runningGatewayScope(input *CommandInput, home string, sessionName string) (networkLogScope, error) {
	manager, code := gatewayManagerForInput(input)
	if code != 0 {
		return networkLogScope{}, fmt.Errorf("select backend for gateway discovery")
	}
	gateways, scopeLabel, err := discoverGatewayTargets(input, manager, true)
	if err != nil {
		return networkLogScope{}, err
	}
	scope := networkLogScope{label: scopeLabel}
	if sessionName != "" {
		gateways = filterGatewaysBySession(gateways, sessionName)
		scope.label = fmt.Sprintf("session %s", sessionName)
		if len(gateways) > 0 {
			// The events carry the session container name, which is not
			// necessarily what the user passed: --session also accepts the
			// gateway container name.
			scope.session = gateways[0].SessionContainer
		}
	}

	scope.paths = networkLogPaths(home, gateways)
	return scope, nil
}

// networkLogPaths is the set of log files the given gateways write to.
// Concurrent sessions of one project and tool share a file, so duplicates are
// collapsed: reading it once per gateway would report every event twice.
func networkLogPaths(home string, gateways []backend.GatewayInfo) []string {
	paths := make([]string, 0, len(gateways))
	for _, gateway := range gateways {
		paths = append(paths, config.HostProjectNetworkLogPath(home, gateway.ProjectHash, gateway.Tool))
	}
	return util.Dedupe(paths)
}

func sessionRecorded(events []netlog.Event, session string) bool {
	for _, event := range events {
		if event.Session == session {
			return true
		}
	}
	return false
}

// sessionContainerName is the name events are stamped with: --session also
// accepts the gateway container name, which adds a suffix to it.
func sessionContainerName(sessionName string) string {
	return strings.TrimSuffix(sessionName, model.GatewayContainerSuffix)
}

// filterGatewaysBySession accepts either the session container name or the
// gateway container name, since both are visible to a user reading `ps`. The
// session container label can be missing, so the gateway name is matched too.
func filterGatewaysBySession(gateways []backend.GatewayInfo, sessionName string) []backend.GatewayInfo {
	session := sessionContainerName(sessionName)
	matched := make([]backend.GatewayInfo, 0, 1)
	for _, gateway := range gateways {
		if gateway.Name == session+model.GatewayContainerSuffix || gateway.SessionContainer == session {
			matched = append(matched, gateway)
		}
	}
	return matched
}

// networkLogRead is everything one pass over the logs in scope produced.
type networkLogRead struct {
	events  []netlog.Event
	skipped int
	// offsets is where reading stopped in each live log, keyed by path. --follow
	// resumes from there instead of re-measuring the file, so an event appended
	// while the backlog was being printed is neither lost nor printed twice.
	offsets map[string]int64
}

// readNetworkLogEvents reads every path in scope, oldest generation first, so a
// rotation boundary is invisible. Events from several gateways are merged in
// timestamp order because their sessions interleave.
func readNetworkLogEvents(livePaths []string) (networkLogRead, error) {
	read := networkLogRead{offsets: make(map[string]int64, len(livePaths))}
	for _, livePath := range livePaths {
		for _, path := range netlog.ReadPaths(livePath) {
			file, err := os.Open(path) // #nosec G304 -- path is derived from enclave's own state directory.
			if err != nil {
				return read, fmt.Errorf("open %s: %w", path, err)
			}
			result, scanErr := netlog.Scan(file)
			closeErr := file.Close()
			if scanErr != nil {
				return read, fmt.Errorf("read %s: %w", path, scanErr)
			}
			if closeErr != nil {
				return read, fmt.Errorf("close %s: %w", path, closeErr)
			}
			if path == livePath {
				// Only the live log is followed; the rotated generation never grows.
				read.offsets[path] = result.Offset
			}
			if read.events == nil {
				read.events = result.Events
			} else {
				read.events = append(read.events, result.Events...)
			}
			read.skipped += result.Skipped
		}
	}
	if len(livePaths) > 1 {
		read.events = mergeByTime(read.events)
	}
	return read, nil
}

// mergeByTime orders events from several logs by timestamp. An event whose
// timestamp does not parse sorts last: ordering it "equal to everything" instead
// would make the comparison intransitive and scramble the whole merge.
func mergeByTime(events []netlog.Event) []netlog.Event {
	type timed struct {
		at    time.Time
		known bool
		event netlog.Event
	}
	decoded := make([]timed, 0, len(events))
	for _, event := range events {
		at, known := event.Time()
		decoded = append(decoded, timed{at: at, known: known, event: event})
	}
	sort.SliceStable(decoded, func(i, j int) bool {
		if decoded[i].known != decoded[j].known {
			return decoded[i].known
		}
		return decoded[i].known && decoded[i].at.Before(decoded[j].at)
	})
	merged := make([]netlog.Event, 0, len(decoded))
	for _, entry := range decoded {
		merged = append(merged, entry.event)
	}
	return merged
}

// applyNetworkLogSince turns the flag into a lower bound on the filter.
// "session" resolves to the most recent session marker in scope and adopts that
// session. Anchoring without adopting would still show a concurrent session's
// events, because one log file holds every session of a project and tool.
func applyNetworkLogSince(filter netlogview.Filter, value string, events []netlog.Event) (netlogview.Filter, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return filter, nil
	}
	if strings.EqualFold(trimmed, netlog.SinceSession) {
		for i := len(events) - 1; i >= 0; i-- {
			if !events[i].IsSessionMarker() {
				continue
			}
			if filter.Session != "" && events[i].Session != filter.Session {
				continue
			}
			at, ok := events[i].Time()
			if !ok {
				continue
			}
			filter.Since = at
			if filter.Session == "" {
				filter.Session = events[i].Session
			}
			return filter, nil
		}
		return filter, fmt.Errorf("--since session: no session marker in this log; it predates session markers")
	}
	if duration, err := time.ParseDuration(trimmed); err == nil {
		if duration < 0 {
			return filter, fmt.Errorf("--since %q must not be negative", value)
		}
		filter.Since = time.Now().Add(-duration)
		return filter, nil
	}
	at, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return filter, fmt.Errorf("--since %q: expected a duration (10m), an RFC3339 timestamp, or %q", value, netlog.SinceSession)
	}
	filter.Since = at
	return filter, nil
}

func writeNetworkLogSummary(out *bufio.Writer, summary netlogview.Summary, asJSON bool, render netlogview.RenderOptions) error {
	if asJSON {
		payload, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		if _, err := out.Write(payload); err != nil {
			return err
		}
		return out.WriteByte('\n')
	}
	_, err := out.WriteString(netlogview.RenderSummary(summary, render))
	return err
}

func writeNetworkLogEvents(out *bufio.Writer, events []netlog.Event, asJSON bool, render netlogview.RenderOptions) error {
	if asJSON {
		for _, event := range events {
			if err := writeNetworkLogJSON(out, event); err != nil {
				return err
			}
		}
		return nil
	}
	return netlogview.WriteEvents(out, events, render)
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
func followNetworkLog(paths []string, offsets map[string]int64, filter netlogview.Filter, asJSON bool, render netlogview.RenderOptions, out *bufio.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var writeErr error
	follower := netlog.NewFollower(paths, netlog.FollowOptions{
		StartOffsets: offsets,
		// Debug rather than a warning: the backlog read already reported the
		// unreadable lines once, and a live stream must not be drowned out.
		OnSkipped: func() { logx.Debugf("Skipped an unreadable network log line") },
	})
	err := follower.Run(ctx, func(event netlog.Event) {
		if writeErr != nil || !filter.Match(event) {
			return
		}
		if asJSON {
			writeErr = writeNetworkLogJSON(out, event)
		} else {
			writeErr = netlogview.WriteEvent(out, event, render)
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
