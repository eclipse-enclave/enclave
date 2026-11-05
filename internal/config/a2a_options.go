// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"enclave/internal/model"
)

const (
	A2ARoleOrchestrator = "orchestrator"
	A2ARoleWorker       = "worker"
	A2ALogCoarse        = "coarse"
	A2ALogFull          = "full"
)

// a2aGroupNamePattern copies peergroup.GroupNamePattern; importing peergroup
// would make A2A code a gateway proxy build input.
var a2aGroupNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// A2ACredentialSlack is added to a host operation bound to get its invocation
// credential lifetime (CR-P3-3), so a host send wait may not exceed
// a2a_credential_lifetime minus this slack.
const A2ACredentialSlack = 60 * time.Second

type a2aValueKind int

const (
	a2aDuration a2aValueKind = iota
	a2aCount
	a2aSize
	a2aRole
	a2aLog
	a2aGroup
)

// a2aOption maps an option to its field, which has the same name in Defaults
// and model.A2AOptions.
type a2aOption struct {
	name  string
	field string
	kind  a2aValueKind
	// min is the smallest accepted duration; below it a bound defeats its purpose.
	min time.Duration
}

var a2aOptions = []a2aOption{
	{"peer_group", "PeerGroup", a2aGroup, 0},
	{"a2a_role", "A2ARole", a2aRole, 0},
	{"a2a_log", "A2ALog", a2aLog, 0},
	{"a2a_context_ttl", "A2AContextTTL", a2aDuration, time.Minute},
	{"a2a_queue_depth", "A2AQueueDepth", a2aCount, 0},
	{"a2a_task_deadline", "A2ATaskDeadline", a2aDuration, 10 * time.Second},
	{"a2a_send_wait", "A2ASendWait", a2aDuration, time.Second},
	{"a2a_host_send_wait", "A2AHostSendWait", a2aDuration, time.Second},
	{"a2a_max_request_size", "A2AMaxRequestSize", a2aSize, 0},
	{"a2a_max_result_size", "A2AMaxResultSize", a2aSize, 0},
	{"a2a_hub_retained_results", "A2AHubRetainedResults", a2aCount, 0},
	{"a2a_record_limit", "A2ARecordLimit", a2aCount, 0},
	{"a2a_record_max_age", "A2ARecordMaxAge", a2aDuration, time.Minute},
	{"a2a_reconnect_window", "A2AReconnectWindow", a2aDuration, 5 * time.Second},
	{"a2a_commit_timeout", "A2ACommitTimeout", a2aDuration, time.Second},
	{"a2a_departure_ack_timeout", "A2ADepartureAckTimeout", a2aDuration, time.Second},
	{"a2a_credential_lifetime", "A2ACredentialLifetime", a2aDuration, 2 * time.Minute},
	{"a2a_status_staleness", "A2AStatusStaleness", a2aDuration, 10 * time.Second},
	{"a2a_termination_timeout", "A2ATerminationTimeout", a2aDuration, time.Second},
}

// normalize validates value for the option, including its duration minimum.
func (o a2aOption) normalize(value string) (string, error) {
	normalized, err := normalizeA2AValue(o.kind, value)
	if err != nil || o.kind != a2aDuration {
		return normalized, err
	}
	if d, _ := ParseA2ADuration(normalized); d < o.min {
		return "", fmt.Errorf("%q is below the minimum of %s", value, o.min)
	}
	return normalized, nil
}

func a2aOptionNamed(name string) a2aOption {
	for _, o := range a2aOptions {
		if o.name == name {
			return o
		}
	}
	panic("unknown A2A option " + name)
}

func defaultA2AOptions() model.A2AOptions {
	return model.A2AOptions{
		A2ARole:                A2ARoleWorker,
		A2ALog:                 A2ALogCoarse,
		A2AContextTTL:          "24h",
		A2AQueueDepth:          "8",
		A2ATaskDeadline:        "30m",
		A2ASendWait:            "90s",
		A2AHostSendWait:        "10m",
		A2AMaxRequestSize:      "1MiB",
		A2AMaxResultSize:       "256KiB",
		A2AHubRetainedResults:  "100",
		A2ARecordLimit:         "1000",
		A2ARecordMaxAge:        "168h",
		A2AReconnectWindow:     "60s",
		A2ACommitTimeout:       "5s",
		A2ADepartureAckTimeout: "30s",
		A2ACredentialLifetime:  "15m",
		A2AStatusStaleness:     "5m",
		A2ATerminationTimeout:  "10s",
	}
}

// ParseA2ADuration parses a positive Go duration such as "90s" or "168h".
func ParseA2ADuration(value string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration (e.g. 90s, 30m, 24h)", value)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q must be positive", value)
	}
	return d, nil
}

// ParseA2ACount parses a positive decimal count.
func ParseA2ACount(value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive integer", value)
	}
	return n, nil
}

// ParseA2ASize parses a positive byte size: a decimal byte count, or a count
// with a KiB or MiB suffix.
func ParseA2ASize(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	digits, unit := trimmed, int64(1)
	switch {
	case strings.HasSuffix(trimmed, "MiB"):
		digits, unit = strings.TrimSuffix(trimmed, "MiB"), 1<<20
	case strings.HasSuffix(trimmed, "KiB"):
		digits, unit = strings.TrimSuffix(trimmed, "KiB"), 1<<10
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 || n > (1<<62)/unit {
		return 0, fmt.Errorf("%q is not a size (bytes, or a count with KiB or MiB)", value)
	}
	return n * unit, nil
}

func normalizeA2AValue(kind a2aValueKind, value string) (string, error) {
	value = strings.TrimSpace(value)
	switch kind {
	case a2aDuration:
		_, err := ParseA2ADuration(value)
		return value, err
	case a2aCount:
		_, err := ParseA2ACount(value)
		return value, err
	case a2aSize:
		_, err := ParseA2ASize(value)
		return value, err
	case a2aRole:
		value = strings.ToLower(value)
		if value != A2ARoleOrchestrator && value != A2ARoleWorker {
			return "", fmt.Errorf("%q is not a role (use: %s|%s)", value, A2ARoleOrchestrator, A2ARoleWorker)
		}
		return value, nil
	case a2aLog:
		value = strings.ToLower(value)
		if value != A2ALogCoarse && value != A2ALogFull {
			return "", fmt.Errorf("%q is not a log mode (use: %s|%s)", value, A2ALogCoarse, A2ALogFull)
		}
		return value, nil
	case a2aGroup:
		if !a2aGroupNamePattern.MatchString(value) {
			return "", fmt.Errorf("%q is not a group name (lowercase letters, digits and hyphens, at most 32, starting with a letter or digit)", value)
		}
		return value, nil
	}
	return "", fmt.Errorf("unknown A2A option kind %d", kind)
}

func applyA2AFlag(target *string, flag string, option a2aOption, value string) error {
	normalized, err := option.normalize(value)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", flag, err)
	}
	*target = normalized
	return nil
}

func applyPeerGroup(opts *model.Options, value string) error {
	return applyA2AFlag(&opts.PeerGroup, "--peer-group", a2aOptionNamed("peer_group"), value)
}

func applyA2ARole(opts *model.Options, value string) error {
	return applyA2AFlag(&opts.A2ARole, "--role", a2aOptionNamed("a2a_role"), value)
}

func applyA2ALog(opts *model.Options, value string) error {
	return applyA2AFlag(&opts.A2ALog, "--a2a-log", a2aOptionNamed("a2a_log"), value)
}

func applyA2AContextTTL(opts *model.Options, value string) error {
	return applyA2AFlag(&opts.A2AContextTTL, "--a2a-context-ttl", a2aOptionNamed("a2a_context_ttl"), value)
}

// ValidateA2AOptions checks every A2A value and the host send wait against the
// credential lifetime cap; an empty peer group is valid, empty bounds are not.
func ValidateA2AOptions(opts model.A2AOptions) error {
	values := reflect.ValueOf(&opts).Elem()
	var errs []error
	for _, option := range a2aOptions {
		field := values.FieldByName(option.field)
		if option.kind == a2aGroup && field.String() == "" {
			continue
		}
		normalized, err := option.normalize(field.String())
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", option.name, err))
			continue
		}
		field.SetString(normalized)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	wait, _ := ParseA2ADuration(opts.A2AHostSendWait)
	lifetime, _ := ParseA2ADuration(opts.A2ACredentialLifetime)
	if wait > lifetime-A2ACredentialSlack {
		return fmt.Errorf("a2a_host_send_wait %s exceeds a2a_credential_lifetime %s minus %s", opts.A2AHostSendWait, opts.A2ACredentialLifetime, A2ACredentialSlack)
	}
	return nil
}

// validateA2ADefaults clears invalid A2A values from a config file, top level
// and per tool override, so the next layer or the built-in default applies.
func validateA2ADefaults(path string, defaults *Defaults) []Warning {
	if defaults == nil {
		return nil
	}
	warnings := validateA2ALayer(path, "", "", defaults)
	names := make([]string, 0, len(defaults.ToolOverrides))
	for name := range defaults.ToolOverrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		override := defaults.ToolOverrides[name]
		warnings = append(warnings, validateA2ALayer(path, name, "tool_overrides."+name+".", &override)...)
		defaults.ToolOverrides[name] = override
	}
	return warnings
}

func validateA2ALayer(path string, toolName string, keyPrefix string, defaults *Defaults) []Warning {
	values := reflect.ValueOf(defaults).Elem()
	var warnings []Warning
	for _, option := range a2aOptions {
		field := values.FieldByName(option.field)
		raw := field.String()
		if strings.TrimSpace(raw) == "" {
			continue
		}
		normalized, err := option.normalize(raw)
		if err != nil {
			warnings = append(warnings, newWarning(WarningInvalidValue, option.name, toolName, path, "Ignoring %s%s in %s: %v", keyPrefix, option.name, path, err))
			normalized = ""
		}
		field.SetString(normalized)
	}
	return warnings
}
