// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"reflect"
	"strings"
	"testing"
)

func wslenvOf(t *testing.T, environ []string) string {
	t.Helper()
	value, ok := lookupIn(environ)(wslenvVar)
	if !ok {
		return ""
	}
	return value
}

func TestForwardEnvForwardsEnclaveVariables(t *testing.T) {
	environ := []string{
		`Path=C:\Windows`,
		"ENCLAVE_LOG_LEVEL=debug",
		"ENCLAVE_AGENT_UPDATE_INTERVAL_HOURS=0",
		"ANTHROPIC_API_KEY=secret",
	}

	got, warnings, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}

	want := "ENCLAVE_AGENT_UPDATE_INTERVAL_HOURS:ENCLAVE_LOG_LEVEL"
	if got := wslenvOf(t, got); got != want {
		t.Errorf("WSLENV = %q, want %q", got, want)
	}
}

// Credentials are the sharp edge of the launcher: nothing crosses into the
// distribution unless it is named, and that has to stay true.
func TestForwardEnvDoesNotForwardUnrelatedVariables(t *testing.T) {
	environ := []string{"ANTHROPIC_API_KEY=secret", "OPENAI_API_KEY=secret", "GITHUB_TOKEN=secret"}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if value := wslenvOf(t, got); value != "" {
		t.Errorf("WSLENV = %q, want empty: no variable was named for forwarding", value)
	}
	if !reflect.DeepEqual(got, environ) {
		t.Errorf("environment was modified: %q", got)
	}
}

func TestForwardEnvHonorsAllowList(t *testing.T) {
	environ := []string{
		"ANTHROPIC_API_KEY=secret",
		"GITHUB_TOKEN=secret",
		"IGNORED=x",
		envForwardEnv + "=ANTHROPIC_API_KEY, GITHUB_TOKEN",
	}

	got, warnings, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
	if value := wslenvOf(t, got); value != "ANTHROPIC_API_KEY:GITHUB_TOKEN" {
		t.Errorf("WSLENV = %q", value)
	}
}

func TestForwardEnvAcceptsWSLENVFlagSuffixes(t *testing.T) {
	environ := []string{
		`MY_DIR=C:\work`,
		"MY_TOKEN=secret",
		envForwardEnv + "=MY_DIR/p,MY_TOKEN",
	}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if value := wslenvOf(t, got); value != "MY_DIR/p:MY_TOKEN" {
		t.Errorf("WSLENV = %q", value)
	}
}

func TestForwardEnvWarnsAboutUnsetAllowListEntries(t *testing.T) {
	environ := []string{envForwardEnv + "=ANTHROPIC_API_KEY"}

	got, warnings, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ANTHROPIC_API_KEY") {
		t.Fatalf("warnings = %q, want one mentioning the unset variable", warnings)
	}
	if value := wslenvOf(t, got); value != "" {
		t.Errorf("WSLENV = %q, want empty: the variable is not set", value)
	}
}

func TestForwardEnvRejectsInvalidAllowListEntries(t *testing.T) {
	cases := []string{
		"NOT A NAME",
		"9LEADING_DIGIT",
		"HAS-DASH",
		"/p",
		"MY_VAR/z",
		"MY_VAR/pz",
	}
	for _, entry := range cases {
		t.Run(entry, func(t *testing.T) {
			if _, _, err := forwardEnv([]string{envForwardEnv + "=" + entry}); err == nil {
				t.Errorf("expected %q to be rejected", entry)
			}
		})
	}
}

func TestForwardEnvAcceptsAllWSLENVFlags(t *testing.T) {
	for _, flags := range []string{"p", "l", "u", "w", "up", "pl"} {
		environ := []string{"MY_VAR=x", envForwardEnv + "=MY_VAR/" + flags}
		if _, _, err := forwardEnv(environ); err != nil {
			t.Errorf("MY_VAR/%s should be accepted: %v", flags, err)
		}
	}
}

func TestForwardEnvSkipsLauncherOwnControls(t *testing.T) {
	environ := []string{
		envDistro + "=Ubuntu",
		envAllowWindowsPath + "=1",
		envForwardEnv + "=",
		`ENCLAVE_HOME=C:\src\enclave`,
		"ENCLAVE_LOG_LEVEL=debug",
	}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if value := wslenvOf(t, got); value != "ENCLAVE_LOG_LEVEL" {
		t.Errorf("WSLENV = %q, want only ENCLAVE_LOG_LEVEL", value)
	}
}

// WSLENV is colon-separated, so an ENCLAVE_ variable whose name carries a
// delimiter would splice a second entry into the list and forward a variable
// nobody named — the one thing the allow-list exists to prevent.
func TestForwardEnvSkipsAutomaticNamesThatWouldSpliceWSLENV(t *testing.T) {
	environ := []string{
		"ENCLAVE_X:GITHUB_TOKEN=1",
		"ENCLAVE_Y/p=1",
		"GITHUB_TOKEN=secret",
		"ENCLAVE_LOG_LEVEL=debug",
	}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}

	value := wslenvOf(t, got)
	if value != "ENCLAVE_LOG_LEVEL" {
		t.Errorf("WSLENV = %q, want only ENCLAVE_LOG_LEVEL", value)
	}
	if strings.Contains(value, "GITHUB_TOKEN") {
		t.Errorf("WSLENV = %q forwards a variable that was never named", value)
	}
}

// The launcher's own controls are excluded by prefix, so a control added later
// cannot cross into the distribution because someone forgot to list it.
func TestForwardEnvExcludesLauncherControlsByPrefix(t *testing.T) {
	environ := []string{
		"ENCLAVE_WSL_SOME_FUTURE_CONTROL=1",
		"ENCLAVE_LOG_LEVEL=debug",
	}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if value := wslenvOf(t, got); value != "ENCLAVE_LOG_LEVEL" {
		t.Errorf("WSLENV = %q, want only ENCLAVE_LOG_LEVEL", value)
	}
}

// An ENCLAVE_ variable is forwarded automatically under its bare name, so
// naming it with a flag has to win: /p on a variable holding a Windows path is
// the whole reason to name it.
func TestForwardEnvAppliesFlagsToAutomaticallyForwardedVariables(t *testing.T) {
	environ := []string{
		`ENCLAVE_SCRATCH=C:\tmp\scratch`,
		"ENCLAVE_LOG_LEVEL=debug",
		envForwardEnv + "=ENCLAVE_SCRATCH/p",
	}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}

	value := wslenvOf(t, got)
	if !strings.Contains(value, "ENCLAVE_SCRATCH/p") {
		t.Errorf("WSLENV = %q, want ENCLAVE_SCRATCH to keep its /p flag", value)
	}
	if strings.Contains(value, "ENCLAVE_SCRATCH:") || strings.HasSuffix(value, "ENCLAVE_SCRATCH") {
		t.Errorf("WSLENV = %q, want no unflagged duplicate of ENCLAVE_SCRATCH", value)
	}
	if !strings.Contains(value, "ENCLAVE_LOG_LEVEL") {
		t.Errorf("WSLENV = %q, want the other ENCLAVE_ variable still forwarded", value)
	}
}

// Windows environment variable names are case-insensitive, so the case a
// variable was set with must not decide whether it is forwarded. ENCLAVE_HOME in
// particular holds a Windows path the Linux binary cannot use.
func TestForwardEnvClassifiesNamesCaseInsensitively(t *testing.T) {
	environ := []string{
		`ENCLAVE_Home=C:\src\enclave`,
		"Enclave_Wsl_Distro=Ubuntu",
		"enclave_log_level=debug",
	}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if value := wslenvOf(t, got); value != "enclave_log_level" {
		t.Errorf("WSLENV = %q, want only enclave_log_level", value)
	}
}

// The deny list is a default, not a prohibition: naming a variable explicitly is
// an informed choice.
func TestForwardEnvAllowListOverridesDenyList(t *testing.T) {
	environ := []string{
		"ENCLAVE_HOME=/home/p/src/enclave",
		envForwardEnv + "=ENCLAVE_HOME",
	}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if value := wslenvOf(t, got); value != "ENCLAVE_HOME" {
		t.Errorf("WSLENV = %q", value)
	}
}

func TestForwardEnvPreservesExistingWSLENV(t *testing.T) {
	environ := []string{
		"WSLENV=USERPROFILE/p:MY_TOKEN",
		"ENCLAVE_LOG_LEVEL=debug",
	}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if value := wslenvOf(t, got); value != "USERPROFILE/p:MY_TOKEN:ENCLAVE_LOG_LEVEL" {
		t.Errorf("WSLENV = %q", value)
	}
	if len(got) != len(environ) {
		t.Errorf("WSLENV should have been replaced in place, got %q", got)
	}
}

func TestForwardEnvDoesNotDuplicateExistingWSLENVEntry(t *testing.T) {
	environ := []string{
		"WSLENV=ENCLAVE_LOG_LEVEL/u",
		"ENCLAVE_LOG_LEVEL=debug",
	}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	// The user's flags win for a name they already listed.
	if value := wslenvOf(t, got); value != "ENCLAVE_LOG_LEVEL/u" {
		t.Errorf("WSLENV = %q", value)
	}
}

func TestForwardEnvMatchesWSLENVCaseInsensitively(t *testing.T) {
	environ := []string{"wslenv=MY_TOKEN", "ENCLAVE_LOG_LEVEL=debug"}

	got, _, err := forwardEnv(environ)
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if len(got) != len(environ) {
		t.Fatalf("a differently-cased WSLENV must be replaced, not appended: %q", got)
	}
	if got[0] != "wslenv=MY_TOKEN:ENCLAVE_LOG_LEVEL" {
		t.Errorf("environ[0] = %q", got[0])
	}
}

func TestForwardEnvIsStableRegardlessOfEnvironOrder(t *testing.T) {
	first, _, err := forwardEnv([]string{"ENCLAVE_B=2", "ENCLAVE_A=1"})
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	second, _, err := forwardEnv([]string{"ENCLAVE_A=1", "ENCLAVE_B=2"})
	if err != nil {
		t.Fatalf("forwardEnv: %v", err)
	}
	if wslenvOf(t, first) != wslenvOf(t, second) {
		t.Errorf("WSLENV depends on environ order: %q vs %q", wslenvOf(t, first), wslenvOf(t, second))
	}
}
