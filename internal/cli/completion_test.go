// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"enclave/internal/config"
)

// TestWriteCompletionWithHeaderPreservesZshDirective guards the zsh completion
// path: `compinit` only registers a completion file whose first line is the
// `#compdef <name>` directive, so the license header must be spliced in after
// that directive, not before it.
func TestWriteCompletionWithHeaderPreservesZshDirective(t *testing.T) {
	script := "#compdef enclave\ncompdef _enclave enclave\n"
	var buf bytes.Buffer
	if err := writeCompletionWithHeader(&buf, []byte(script)); err != nil {
		t.Fatalf("writeCompletionWithHeader: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "#compdef enclave\n") {
		t.Fatalf("zsh completion must start with #compdef directive, got:\n%s", out)
	}
	if !strings.Contains(out, shellCompletionLicenseHeader) {
		t.Fatal("license header missing from zsh completion output")
	}
	if !strings.Contains(out, "compdef _enclave enclave") {
		t.Fatal("original completion body missing from output")
	}
}

// TestWriteCompletionWithHeaderPrependsHeader confirms that for scripts without
// a leading directive (bash, fish) the header is written first, unchanged.
func TestWriteCompletionWithHeaderPrependsHeader(t *testing.T) {
	script := "# bash completion V2 for enclave\n"
	var buf bytes.Buffer
	if err := writeCompletionWithHeader(&buf, []byte(script)); err != nil {
		t.Fatalf("writeCompletionWithHeader: %v", err)
	}
	if !strings.HasPrefix(buf.String(), shellCompletionLicenseHeader) {
		t.Fatalf("expected license header first, got:\n%s", buf.String())
	}
}

// TestRegisterCompletionsTargetRealFlags guards against stale completion hooks:
// every flag we register a completion for must exist on a command. The flags now
// live on leaf commands rather than the root, so we register the full option set
// here; a completer for a removed flag (e.g. the old --image-mode) would match
// nothing and make registerCompletions return an error.
func TestRegisterCompletionsTargetRealFlags(t *testing.T) {
	var res Result
	rootCmd := &cobra.Command{Use: "enclave"}
	addOptionFlags(rootCmd.PersistentFlags(), &res.Options, &res.Sources,
		config.OptionGroupGlobal,
		config.OptionGroupRun,
		config.OptionGroupAuth,
		config.OptionGroupBuild,
	)

	if err := registerCompletions(rootCmd); err != nil {
		t.Fatalf("registerCompletions registered a completion for a missing flag: %v", err)
	}
}

// TestNetworkLogFlagCompletions guards the value completions of the `network
// log` filter flags: without them the shell falls back to filename completion,
// which is never a useful suggestion for a verdict, an event type, or a domain.
func TestNetworkLogFlagCompletions(t *testing.T) {
	var res Result
	rootCmd := &cobra.Command{Use: "enclave"}
	// The option-set flags satisfy the completers map; only the network group is
	// under test here.
	addOptionFlags(rootCmd.PersistentFlags(), &res.Options, &res.Sources,
		config.OptionGroupGlobal,
		config.OptionGroupRun,
		config.OptionGroupAuth,
		config.OptionGroupBuild,
	)
	rootCmd.AddCommand(networkCommand(&res))
	if err := registerCompletions(rootCmd); err != nil {
		t.Fatalf("registerCompletions: %v", err)
	}

	logCmd := findSubCommand(rootCmd, "network", "log")
	if logCmd == nil {
		t.Fatal("network log command not found")
	}

	want := map[string][]string{
		"verdict": {"pass", "deny"},
		"type":    {"dns", "http", "tcp"},
		"since":   {"session"},
		"domain":  nil,
		"session": nil,
	}
	for flag, values := range want {
		t.Run(flag, func(t *testing.T) {
			completions, directive := completeFlag(t, logCmd, flag)
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Fatalf("--%s directive = %v, want NoFileComp so the shell does not offer files", flag, directive)
			}
			for _, value := range values {
				if !slices.Contains(completions, value) {
					t.Fatalf("--%s completions = %v, missing %q", flag, completions, value)
				}
			}
		})
	}
}

// completeFlag invokes the registered completion function of a flag the way
// Cobra does when the shell asks for candidate values.
func completeFlag(t *testing.T, cmd *cobra.Command, flag string) ([]string, cobra.ShellCompDirective) {
	t.Helper()
	fn, ok := cmd.GetFlagCompletionFunc(flag)
	if !ok {
		t.Fatalf("no completion registered for --%s on %q", flag, cmd.CommandPath())
	}
	return fn(cmd, nil, "")
}

// TestRegisterCompletionsRejectsMissingFlag confirms the guard actually fails
// when a completion targets a flag that does not exist.
func TestRegisterCompletionsRejectsMissingFlag(t *testing.T) {
	rootCmd := &cobra.Command{Use: "enclave"}
	if err := rootCmd.RegisterFlagCompletionFunc("does-not-exist", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}); err == nil {
		t.Fatal("expected error registering completion for a missing flag, got nil")
	}
}
