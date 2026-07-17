// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"enclave/internal/config"
	"enclave/internal/model"
)

const shellCompletionLicenseHeader = `# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

`

func completionCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:       "completion <bash|zsh|fish>",
		Short:     "Generate shell completion script",
		ValidArgs: []string{"bash", "zsh", "fish"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			res.Action = "completion"
			var buf bytes.Buffer
			switch args[0] {
			case "bash":
				if err := cmd.Root().GenBashCompletionV2(&buf, true); err != nil {
					return err
				}
			case "zsh":
				if err := cmd.Root().GenZshCompletion(&buf); err != nil {
					return err
				}
			case "fish":
				if err := cmd.Root().GenFishCompletion(&buf, true); err != nil {
					return err
				}
			}
			return writeCompletionWithHeader(os.Stdout, buf.Bytes())
		},
	}
	return cmd
}

// writeCompletionWithHeader prepends the MIT license header to a generated
// completion script. Zsh completion files must keep their `#compdef <name>`
// directive on the first line so `compinit` registers them from `$fpath`, so
// the header is spliced in after any such leading directive rather than before
// it.
func writeCompletionWithHeader(w io.Writer, script []byte) error {
	var directive []byte
	if bytes.HasPrefix(script, []byte("#compdef ")) {
		if nl := bytes.IndexByte(script, '\n'); nl >= 0 {
			directive = script[:nl+1]
			script = script[nl+1:]
		}
	}
	if _, err := w.Write(directive); err != nil {
		return fmt.Errorf("write completion directive: %w", err)
	}
	if _, err := io.WriteString(w, shellCompletionLicenseHeader); err != nil {
		return fmt.Errorf("write completion license header: %w", err)
	}
	if _, err := w.Write(script); err != nil {
		return fmt.Errorf("write completion script: %w", err)
	}
	return nil
}

type flagCompletionFunc func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)

// registerCompletions wires dynamic and static shell-completion callbacks onto
// the commands that actually define each flag. After the CLI simplification the
// flags live on leaf commands (or a parent's persistent flag set), not as root
// persistent flags, so we walk the command tree and register each completer
// wherever its flag is defined. It returns an error if a completer targets a
// flag that exists on no command, so a stale registration (e.g. the removed
// --image-mode) surfaces instead of leaving a quiet dead hook. Callers treat
// registration as best-effort at runtime; TestRegisterCompletionsTargetRealFlags
// asserts the error is nil so stale registrations fail the build.
func registerCompletions(rootCmd *cobra.Command) error {
	toolCompleter := func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		paths, err := config.ResolvePaths()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		tools, err := config.ListTools(paths)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return tools, cobra.ShellCompDirectiveNoFileComp
	}
	featuresCompleter := func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		paths, err := config.ResolvePaths()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		exts, err := config.ListFeatures(paths)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := make([]string, len(exts))
		for i, ext := range exts {
			names[i] = ext.Name
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}

	completers := map[string]flagCompletionFunc{
		"tool":          toolCompleter,
		"features":      featuresCompleter,
		"auth-scope":    staticValuesCompleter(model.AuthScopeShared, model.AuthScopeProject),
		"secrets-scope": staticValuesCompleter(model.SecretsScopeProject, model.SecretsScopeGlobal, model.SecretsScopeBoth),
		"progress":      staticValuesCompleter(model.BuildProgressQuiet, model.BuildProgressCompact, model.BuildProgressVerbose),
	}

	// Each of these flags now lives on the leaf commands that consume it
	// (or as a persistent flag on a parent like `network`), not as a root
	// persistent flag. Walk the tree and register the completer wherever
	// the flag is actually defined.
	matched := make(map[string]bool, len(completers))
	walkCommands(rootCmd, func(cmd *cobra.Command) {
		for name, fn := range completers {
			if cmd.LocalFlags().Lookup(name) == nil && cmd.PersistentFlags().Lookup(name) == nil {
				continue
			}
			matched[name] = true
			_ = cmd.RegisterFlagCompletionFunc(name, fn)
		}
	})

	// ValidArgsFunction on "network set-mode" subcommand.
	setModeCmd := findSubCommand(rootCmd, "network", "set-mode")
	if setModeCmd != nil {
		setModeCmd.ValidArgsFunction = func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return []string{model.NetworkModeRestricted, model.NetworkModeUnrestricted}, cobra.ShellCompDirectiveNoFileComp
		}
	}

	// `update [tool...]` takes tool names as positional arguments.
	if updateCmd := findSubCommand(rootCmd, "update"); updateCmd != nil {
		updateCmd.ValidArgsFunction = toolCompleter
	}

	// `config --view` accepts a fixed set of render modes. The flag lives on the
	// `config` leaf command, not in the shared option set, so register it here
	// rather than via the completers map above.
	if configCmd := findSubCommand(rootCmd, "config"); configCmd != nil {
		_ = configCmd.RegisterFlagCompletionFunc("view", staticValuesCompleter("matrix", "effective", "diff", "source"))
	}

	// Surface any completer that matched no flag anywhere in the tree, so a
	// stale registration (e.g. for a removed flag) fails the build via
	// TestRegisterCompletionsTargetRealFlags rather than silently doing nothing.
	var errs []error
	for name := range completers {
		if !matched[name] {
			errs = append(errs, fmt.Errorf("completion registered for unknown flag --%s", name))
		}
	}
	return errors.Join(errs...)
}

func staticValuesCompleter(values ...string) flagCompletionFunc {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

func walkCommands(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, sub := range cmd.Commands() {
		walkCommands(sub, fn)
	}
}

func findSubCommand(cmd *cobra.Command, names ...string) *cobra.Command {
	cur := cmd
	for _, name := range names {
		found := false
		for _, child := range cur.Commands() {
			if child.Name() == name {
				cur = child
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return cur
}
