// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/usercmd"
)

// userCommandGroupID groups the name-only stub commands registered for
// user-defined subcommands so they appear under their own --help section.
const userCommandGroupID = "user-commands"

type Result struct {
	Options      model.Options
	Action       string
	HelpShown    bool
	Sources      model.OptionSources
	ConfigView   model.ConfigView
	ReviewTarget string
	// UserCommand is set when Action == "user-command": a user-defined
	// subcommand matched the first positional argument. UserCommandArgs holds
	// every argument after the command name, verbatim.
	UserCommand     *usercmd.Command
	UserCommandArgs []string
	// Warnings collects non-fatal messages produced during parsing (e.g. a user
	// command shadowed by a built-in). Callers log these via their own facility.
	Warnings []string
}

func Parse(args []string, defaults model.Options, userCmds ...usercmd.Command) (Result, error) {
	tool := strings.TrimSpace(defaults.Tool)
	if tool == "" {
		tool = strings.TrimSpace(config.DefaultOptions().Tool)
	}

	opts := defaults
	opts.Tool = tool

	res := Result{Options: opts, Action: "run"}
	sources := &res.Sources

	rootCmd := &cobra.Command{
		Use:   "enclave",
		Short: "Docker environment for AI coding tools",
		Long: `Docker environment for AI coding tools.

Running enclave with no subcommand defaults to "run", so run's flags apply
directly: "enclave --tool codex" is equivalent to "enclave run --tool codex".`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	defaultHelp := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		res.HelpShown = true
		defaultHelp(cmd, args)
	})

	addOptionFlags(rootCmd.PersistentFlags(), &res.Options, sources, config.OptionGroupGlobal)

	rootCmd.AddCommand(
		runCommand(&res),
		psCommand(&res),
		statusCommand(&res),
		continueCommand(&res),
		resumeCommand(&res),
		configCommand(&res),
		cleanupCommand(&res),
		execCommand(&res),
		shellCommand(&res),
		authCommand(&res),
		infoCommand(&res),
		hiddenSimpleCommand("validate-extensions", "Validate extension metadata", &res),
		hiddenSimpleCommand("ssh-init", "Initialize SSH directory", &res),
		simpleCommandWithTool("tools", "List available tool profiles", &res),
		featuresCommand(&res),
		updateCommand(&res),
		stopCommand(&res),
		attachCommand(&res),
		theiaCommand(&res, "theia"),
		theiaCommand(&res, "theia-next"),
		devcontainerCommand(&res),
		networkCommand(&res),
		imgCommand(&res),
		extensionCommand(&res),
		completionCommand(&res),
		reviewTargetCommand(&res),
	)

	// Completion registration is best-effort at runtime; correctness (every
	// registered completion targets a real flag) is enforced by
	// TestRegisterCompletionsTargetRealFlags.
	_ = registerCompletions(rootCmd)

	// Built-in command names are now known: drop colliding user commands and
	// register name-only stubs for the survivors so they list in --help and
	// complete. Stubs never execute; interception below fires first.
	userCmds = registerUserCommands(rootCmd, userCmds, &res)

	cmdTree := buildRootCommandTree(rootCmd)
	flagIndex := augmentValueFlags(config.CLIFlagIndex(), rootCmd)

	// Intercept user commands before Cobra: if the first positional matches a
	// discovered user command, hand the line off verbatim without letting
	// normalizeArgs or Cobra rewrite or reject it (preserves the standard
	// unknown-command behavior for everything else).
	if len(userCmds) > 0 {
		if start, _ := commandChain(args, cmdTree, flagIndex); start >= 0 {
			if uc := findUserCommand(userCmds, args[start]); uc != nil {
				return interceptUserCommand(rootCmd, uc, args, start, res)
			}
		}
	}

	normalized, err := normalizeArgs(args, cmdTree, flagIndex)
	if err != nil {
		return res, err
	}
	rootCmd.SetArgs(normalized)

	// Cobra's internal completion commands (__complete, __completeNoDesc) are
	// handled entirely inside Execute(). Mark the action so the caller can
	// short-circuit without heavy initialization.
	if len(normalized) > 0 && (normalized[0] == "__complete" || normalized[0] == "__completeNoDesc") {
		res.Action = "completion"
	}

	if err := rootCmd.Execute(); err != nil {
		return res, err
	}

	return res, nil
}

// registerUserCommands drops user commands whose name collides with a built-in
// (built-ins always win, with a warning) and registers a name-only stub for
// each survivor so it appears in --help and shell completion. It returns the
// surviving commands.
func registerUserCommands(rootCmd *cobra.Command, userCmds []usercmd.Command, res *Result) []usercmd.Command {
	if len(userCmds) == 0 {
		return nil
	}
	builtins := map[string]struct{}{
		"help":             {},
		"completion":       {},
		"__complete":       {},
		"__completeNoDesc": {},
	}
	for _, c := range rootCmd.Commands() {
		builtins[c.Name()] = struct{}{}
	}

	kept := make([]usercmd.Command, 0, len(userCmds))
	groupAdded := false
	for _, uc := range userCmds {
		if _, ok := builtins[uc.Name]; ok {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"user command %q (%s) is shadowed by a built-in command; ignoring", uc.Name, uc.Path))
			continue
		}
		if !groupAdded {
			rootCmd.AddGroup(&cobra.Group{ID: userCommandGroupID, Title: "User Commands:"})
			groupAdded = true
		}
		rootCmd.AddCommand(&cobra.Command{
			Use:                uc.Name,
			Short:              fmt.Sprintf("User command (%s)", uc.Path),
			GroupID:            userCommandGroupID,
			DisableFlagParsing: true,
			// The no-op Run makes IsAvailableCommand() true so the stub is
			// grouped under "User Commands:" in --help and offered by shell
			// completion. It is unreachable: interception in Parse fires before
			// rootCmd.Execute() for any line whose first positional is a
			// surviving user command name.
			Run: func(*cobra.Command, []string) {},
		})
		kept = append(kept, uc)
	}
	return kept
}

// findUserCommand returns the user command with the given name, or nil.
func findUserCommand(userCmds []usercmd.Command, name string) *usercmd.Command {
	for i := range userCmds {
		if userCmds[i].Name == name {
			return &userCmds[i]
		}
	}
	return nil
}

// interceptUserCommand splits the command line at the user command name,
// parses the leading enclave flags against the allowed group set, and hands
// every trailing argument to the script verbatim. Host commands accept only
// the global flag group; session commands additionally accept the full session
// flag set (Run+Auth+Build), mirroring `shell`.
func interceptUserCommand(rootCmd *cobra.Command, uc *usercmd.Command, args []string, start int, res Result) (Result, error) {
	res.Action = "user-command"
	res.UserCommand = uc
	res.UserCommandArgs = append([]string(nil), args[start+1:]...)

	groups := []config.OptionGroup{config.OptionGroupGlobal}
	if uc.Target == usercmd.TargetSession {
		groups = append(groups, config.OptionGroupRun, config.OptionGroupAuth, config.OptionGroupBuild)
	}

	fs := pflag.NewFlagSet("enclave", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addOptionFlags(fs, &res.Options, &res.Sources, groups...)
	if err := fs.Parse(args[:start]); err != nil {
		// A help flag before the command name (e.g. `enclave --help deploy`)
		// makes pflag return ErrHelp. Help is name-only by design, so render the
		// stub's help via the root command rather than execute the script.
		// res is passed by value here, so set HelpShown on this local copy (the
		// SetHelpFunc closure in Parse mutates a different, outer res).
		if errors.Is(err, pflag.ErrHelp) {
			res.HelpShown = true
			rootCmd.SetArgs([]string{"help", uc.Name})
			if execErr := rootCmd.Execute(); execErr != nil {
				return res, execErr
			}
			return res, nil
		}
		if uc.Target == usercmd.TargetHost {
			return res, fmt.Errorf("%w: host commands accept only global flags (e.g. --verbose) before the command name", err)
		}
		return res, err
	}
	return res, nil
}

// cliBoolValue handles boolean CLI flags.
// It implements pflag's boolFlag interface so pflag recognizes it as a bool.
type cliBoolValue struct {
	flag    config.CLIFlag
	opts    *model.Options
	sources *model.OptionSources
	value   string
}

func (v *cliBoolValue) String() string {
	if v.value != "" {
		return v.value
	}
	return "false"
}

func (v *cliBoolValue) Set(value string) error {
	v.value = value
	return v.flag.Apply(v.opts, v.sources, value)
}

func (v *cliBoolValue) Type() string { return "bool" }

func (v *cliBoolValue) IsBoolFlag() bool { return true }

// cliStringValue handles string CLI flags.
// It does NOT implement boolFlag interface, so pflag's defaultIsZeroValue()
// falls through to the default case which recognizes "" as zero.
type cliStringValue struct {
	flag    config.CLIFlag
	opts    *model.Options
	sources *model.OptionSources
	value   string
}

func (v *cliStringValue) String() string { return v.value }

func (v *cliStringValue) Set(value string) error {
	v.value = value
	return v.flag.Apply(v.opts, v.sources, value)
}

func (v *cliStringValue) Type() string { return "string" }

// addOptionFlags registers every CLIFlag belonging to the given option groups
// onto the provided FlagSet. With no groups, no flags are registered.
func addOptionFlags(flags *pflag.FlagSet, opts *model.Options, sources *model.OptionSources, groups ...config.OptionGroup) {
	for _, spec := range config.OptionSpecsForGroups(groups...) {
		for _, flag := range spec.CLIFlags {
			addCLIFlag(flags, flag, opts, sources)
		}
	}
}

// addSessionOptionFlags is the registration used by every command that starts
// a new container (run/continue/resume/shell and devcontainer variants). These
// commands need the full Run+Auth+Build flag set.
func addSessionOptionFlags(cmd *cobra.Command, res *Result) {
	addOptionFlags(cmd.Flags(), &res.Options, &res.Sources,
		config.OptionGroupRun,
		config.OptionGroupAuth,
		config.OptionGroupBuild,
	)
}

// addOptionFlagsByName registers specific CLIFlags by their OptionDef name.
// Use this for commands that need a single flag from another group without
// inheriting the whole group (e.g. `ps --name` for session filtering).
func addOptionFlagsByName(flags *pflag.FlagSet, opts *model.Options, sources *model.OptionSources, names ...string) {
	for _, spec := range config.OptionSpecsByName(names...) {
		for _, flag := range spec.CLIFlags {
			addCLIFlag(flags, flag, opts, sources)
		}
	}
}

func addCLIFlag(flags *pflag.FlagSet, flag config.CLIFlag, opts *model.Options, sources *model.OptionSources) {
	isBool := flag.ValueKind == config.CLIValueNone
	var value pflag.Value
	if isBool {
		value = &cliBoolValue{flag: flag, opts: opts, sources: sources}
	} else {
		value = &cliStringValue{flag: flag, opts: opts, sources: sources}
	}

	name := flag.Name
	var registered string
	switch {
	case strings.HasPrefix(name, "--"):
		registered = strings.TrimPrefix(name, "--")
		flags.Var(value, registered, flag.Usage)
	case strings.HasPrefix(name, "-"):
		registered = strings.TrimPrefix(name, "-")
		if registered == "" {
			return
		}
		flags.VarP(value, registered, registered, flag.Usage)
	default:
		registered = name
		flags.Var(value, registered, flag.Usage)
	}
	if isBool {
		setNoOptDefVal(flags, registered)
	}
}

func setNoOptDefVal(flags *pflag.FlagSet, name string) {
	if flag := flags.Lookup(name); flag != nil {
		flag.NoOptDefVal = "true"
	}
}

// rejectUnknownSubcommand is RunE for parent commands that only group subcommands.
// Without it, Cobra silently falls back to help on unknown sub-subcommands.
func rejectUnknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown command: %q for %q", args[0], cmd.CommandPath())
	}
	return cmd.Help()
}

// commandTree mirrors the rootCmd subcommand graph so normalizeArgs can walk
// the full command path before deciding where to insert pre-command flags.
type commandTree map[string]commandTree

func buildRootCommandTree(cmd *cobra.Command) commandTree {
	tree := buildCommandTree(cmd)
	// Cobra installs these commands during Execute(), after this tree is built.
	// Pre-add them so normalizeArgs recognizes the same root-level commands
	// that Cobra will later accept.
	tree["help"] = commandTree{}
	tree["__complete"] = commandTree{}
	tree["__completeNoDesc"] = commandTree{}
	return tree
}

func buildCommandTree(cmd *cobra.Command) commandTree {
	tree := commandTree{}
	for _, child := range cmd.Commands() {
		tree[child.Name()] = buildCommandTree(child)
	}
	return tree
}

func normalizeArgs(args []string, cmdTree commandTree, flags map[string]config.CLIFlag) ([]string, error) {
	if len(args) == 0 {
		return []string{"run"}, nil
	}

	// Cobra's completion requests carry the real command line as trailing args
	// (`__complete <line...> <toComplete>`). Normalize that inner line so
	// completion sees the same implicit `run` as execution, then restore the
	// request prefix.
	if args[0] == cobra.ShellCompRequestCmd || args[0] == cobra.ShellCompNoDescRequestCmd {
		return append([]string{args[0]}, normalizeCompletionLine(args[1:], cmdTree, flags)...), nil
	}

	// A help flag among the leading flags: let Cobra render help verbatim
	// rather than rewriting the line, unless those flags belong to the implicit
	// run command and no explicit command was supplied.
	if leadingHelpFlag(args, flags) {
		if implicitRunHelp(args, flags) {
			return append([]string{"run"}, args...), nil
		}
		return args, nil
	}

	start, end := commandChain(args, cmdTree, flags)
	switch {
	case start == -1:
		// No command was given — fall back to the implicit `run` command.
		return append([]string{"run"}, args...), nil
	case end == start:
		return nil, fmt.Errorf("unknown command: %q", args[start])
	case start == 0:
		return args, nil
	}

	// Most option flags are registered on the leaf command rather than on
	// root, so `enclave --ephemeral devcontainer run` would otherwise be
	// parsed against root or the devcontainer parent. Move pre-command flags
	// to right after the command chain so the documented `[FLAGS] [COMMAND]`
	// form keeps working even for nested commands.
	result := make([]string, 0, len(args))
	result = append(result, args[start:end]...)
	result = append(result, args[:start]...)
	result = append(result, args[end:]...)
	return result, nil
}

// augmentValueFlags returns a copy of base extended with every value-taking
// flag registered on root or any of its subcommands. commandChain and
// normalizeArgs use the index to skip a flag's value when scanning for the
// command, so the documented `[FLAGS] [COMMAND]` form keeps working for
// subcommand-specific flags too (e.g. `--view <mode> config`, `--keep <kinds>
// cleanup`). Without this, only the generated run/global flags are known and a
// subcommand flag's value is mistaken for the command. Bool flags carry a
// NoOptDefVal and never consume the next token, so they are left out.
func augmentValueFlags(base map[string]config.CLIFlag, root *cobra.Command) map[string]config.CLIFlag {
	merged := make(map[string]config.CLIFlag, len(base))
	for name, flag := range base {
		merged[name] = flag
	}
	add := func(f *pflag.Flag) {
		if f.NoOptDefVal != "" {
			return
		}
		if name := "--" + f.Name; merged[name].Name == "" {
			merged[name] = config.CLIFlag{Name: name, ValueKind: config.CLIValueRequired}
		}
		if f.Shorthand != "" {
			if name := "-" + f.Shorthand; merged[name].Name == "" {
				merged[name] = config.CLIFlag{Name: name, ValueKind: config.CLIValueRequired}
			}
		}
	}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		cmd.Flags().VisitAll(add)
		cmd.PersistentFlags().VisitAll(add)
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return merged
}

// commandChain locates the contiguous chain of known (sub)commands in args,
// skipping leading flags and the values of value-taking flags. It returns the
// half-open range [start, end):
//
//   - start == -1         no positional precedes `--` or the end of args.
//   - end == start (>= 0) the first positional is not a known command.
//   - end >  start        args[start:end] is the matched command chain.
func commandChain(args []string, cmdTree commandTree, flags map[string]config.CLIFlag) (start, end int) {
	start = -1
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return -1, -1
		}
		if strings.HasPrefix(arg, "-") {
			if strings.Contains(arg, "=") {
				continue
			}
			if flag, ok := flags[arg]; ok && flag.ValueKind == config.CLIValueRequired {
				i++
			}
			continue
		}
		start = i
		break
	}
	if start == -1 {
		return -1, -1
	}

	// Greedily extend the chain (e.g. `devcontainer run`) so leading flags are
	// inserted after the leaf, not after the parent.
	end = start
	cur := cmdTree
	for end < len(args) {
		sub, ok := cur[args[end]]
		if !ok {
			break
		}
		cur = sub
		end++
	}
	return start, end
}

// leadingHelpFlag reports whether -h/--help appears among the flags that
// precede the first positional argument. Such a request reaches Cobra
// unmodified so it renders help.
func leadingHelpFlag(args []string, flags map[string]config.CLIFlag) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			return false
		}
		if arg == "-h" || arg == "--help" {
			return true
		}
		if strings.Contains(arg, "=") {
			continue
		}
		if flag, ok := flags[arg]; ok && flag.ValueKind == config.CLIValueRequired {
			i++
		}
	}
	return false
}

// implicitRunHelp reports whether a leading help request also includes flags
// that are registered on the implicit `run` command instead of the root. In
// that case `enclave --tool codex --help` should behave like
// `enclave run --tool codex --help`.
func implicitRunHelp(args []string, flags map[string]config.CLIFlag) bool {
	rootFlags := rootFlagNames()
	sawRunScopedFlag := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			return false
		}
		name := flagName(arg)
		if name == "-h" || name == "--help" {
			return sawRunScopedFlag
		}
		if _, ok := rootFlags[name]; ok {
			continue
		}
		flag, ok := flags[name]
		if !ok {
			return false
		}
		sawRunScopedFlag = true
		if !strings.Contains(arg, "=") && flag.ValueKind == config.CLIValueRequired {
			i++
		}
	}
	return false
}

func rootFlagNames() map[string]struct{} {
	names := map[string]struct{}{
		"-h":     {},
		"--help": {},
	}
	for _, spec := range config.OptionSpecsForGroups(config.OptionGroupGlobal) {
		for _, flag := range spec.CLIFlags {
			names[flag.Name] = struct{}{}
		}
	}
	return names
}

func flagName(arg string) string {
	if before, _, ok := strings.Cut(arg, "="); ok {
		return before
	}
	return arg
}

// normalizeCompletionLine rewrites the command line carried inside a Cobra
// completion request so that completion mirrors execution. The final element of
// inner is the partial word under the cursor (Cobra's "toComplete"); it is
// preserved verbatim and only the preceding words are normalized.
//
// The case that matters is the implicit `run`: because `enclave` with no
// subcommand runs `run` (see normalizeArgs), `enclave --<TAB>` must complete
// against run's flags, not the bare root command — otherwise only the global
// flags (--verbose, --help) complete.
func normalizeCompletionLine(inner []string, cmdTree commandTree, flags map[string]config.CLIFlag) []string {
	if len(inner) == 0 {
		return inner
	}
	typed, toComplete := inner[:len(inner)-1], inner[len(inner)-1]

	start, end := commandChain(typed, cmdTree, flags)
	switch {
	case end > start:
		// An explicit (sub)command is on the line; mirror normalizeArgs and move
		// any leading flags after the command chain so Cobra targets the leaf.
		line := make([]string, 0, len(inner))
		line = append(line, typed[start:end]...)
		line = append(line, typed[:start]...)
		line = append(line, typed[end:]...)
		return append(line, toComplete)
	case strings.HasPrefix(toComplete, "-") || containsFlag(typed):
		// No subcommand yet, but the user is completing a flag (or has already
		// typed one): apply the implicit `run` so its flags and flag values
		// complete.
		line := make([]string, 0, len(inner)+1)
		line = append(line, "run")
		line = append(line, typed...)
		return append(line, toComplete)
	default:
		// Bare or partial subcommand name (e.g. `enclave <TAB>` or `co<TAB>`):
		// leave the line so Cobra lists/matches subcommand names.
		return inner
	}
}

// containsFlag reports whether any argument before the `--` terminator is a
// flag (begins with "-").
func containsFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if strings.HasPrefix(arg, "-") {
			return true
		}
	}
	return false
}

func configCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show configuration values",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = "config"
			switch res.ConfigView.Mode {
			case "matrix", "effective", "diff", "source":
				return nil
			default:
				return fmt.Errorf("invalid --view %q (valid: matrix,effective,diff,source)", res.ConfigView.Mode)
			}
		},
	}
	addOptionFlags(cmd.Flags(), &res.Options, &res.Sources,
		config.OptionGroupRun,
		config.OptionGroupAuth,
		config.OptionGroupBuild,
	)
	cmd.Flags().StringVar(&res.ConfigView.Mode, "view", "matrix", "View: matrix|effective|diff|source")
	cmd.Flags().BoolVar(&res.ConfigView.JSON, "json", false, "Emit JSON output")
	return cmd
}

func sessionCommand(use, short, action string, res *Result, withBackground bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			res.Action = action
			res.Options.CmdArgs = append(res.Options.CmdArgs, cmdArgs...)
			return nil
		},
	}
	addSessionOptionFlags(cmd, res)
	if withBackground {
		cmd.Flags().BoolVar(&res.Options.Background, "background", false, "Start container detached with tool command and TTY")
	}
	return cmd
}

func runCommand(res *Result) *cobra.Command {
	return sessionCommand("run", "Start container with tool CLI (default)", "run", res, true)
}

func continueCommand(res *Result) *cobra.Command {
	return sessionCommand("continue", "Continue the latest tool session", "continue", res, true)
}

func resumeCommand(res *Result) *cobra.Command {
	return sessionCommand("resume", "Resume a previous tool session", "resume", res, true)
}

func cleanupCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove persistent stores and caches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res.Action = "cleanup"
			keep, err := cmd.Flags().GetStringSlice("keep")
			if err != nil {
				return err
			}
			for _, token := range keep {
				switch strings.TrimSpace(token) {
				case "":
					continue
				case "cache":
					res.Options.CleanupKeepCache = true
				case "history":
					res.Options.CleanupKeepHist = true
				case "auth":
					res.Options.CleanupKeepAuth = true
				case "memory":
					res.Options.CleanupKeepMemory = true
				default:
					return fmt.Errorf("unknown --keep value %q (valid: cache,history,auth,memory)", token)
				}
			}
			return nil
		},
	}
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool")
	cmd.Flags().BoolVar(&res.Options.CleanupAll, "all", false, "Remove stores and caches for all projects")
	cmd.Flags().BoolVar(&res.Options.CleanupEphemeral, "ephemeral", false, "Remove stopped containers and ephemeral session stores")
	cmd.Flags().StringSlice("keep", nil, "Keep stores of the listed kinds: cache,history,auth,memory (memory has no selective effect with --all)")
	cmd.Flags().BoolVar(&res.Options.CleanupBuildCache, "build-cache", false, "Prune Docker build cache (requires confirmation)")
	cmd.Flags().BoolVar(&res.Options.CleanupDryRun, "dry-run", false, "Show what would be removed")
	return cmd
}

func updateCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update [tool...]",
		Short: "Rebuild tool image(s) with the latest agent CLI, without starting a session",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, toolArgs []string) error {
			res.Action = "update"
			res.Options.UpdateTools = append(res.Options.UpdateTools, toolArgs...)
			return nil
		},
	}
	// update builds an image without starting it, so it needs the same
	// build-affecting flags as run (e.g. --slim, --features, --base-image)
	// to refresh the exact image variant a matching run would use. --tool
	// selects the target when no positional tool arguments are given.
	addOptionFlags(cmd.Flags(), &res.Options, &res.Sources, config.OptionGroupBuild)
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool")
	return cmd
}

func execCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Run command in existing container",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			res.Action = "exec"
			res.Options.CmdArgs = append(res.Options.CmdArgs, cmdArgs...)
			return nil
		},
	}
	// exec attaches to an already-running container; --name picks which one
	// when multiple sessions exist for the same tool.
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool", "session_name")
	cmd.Flags().BoolVar(&res.Options.Admin, "admin", false, "Enable package-management sudo")
	return cmd
}

func shellCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Start interactive shell",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			res.Action = "shell"
			res.Options.Shell = true
			res.Options.CmdArgs = append(res.Options.CmdArgs, cmdArgs...)
			return nil
		},
	}
	addSessionOptionFlags(cmd, res)
	cmd.Flags().BoolVar(&res.Options.Admin, "admin", false, "Enable package-management sudo")
	return cmd
}

// infoCommand previews configuration and the resolved image. runInfo derives
// the reported image from --tool plus the Build group (--image-name, --slim,
// feature directives), so it registers both like `update` does.
func infoCommand(res *Result) *cobra.Command {
	cmd := simpleCommand("info", "Show configuration and image details", res)
	addOptionFlags(cmd.Flags(), &res.Options, &res.Sources, config.OptionGroupBuild)
	// auth_name lets `info` preview the named shared auth store for an identity.
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool", "auth_name")
	return cmd
}

func featuresCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "features",
		Short: "List available features",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = "features"
			return nil
		},
	}
	// runFeatures previews which features a run would enable; that depends on
	// --slim and --features.
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "slim", "features")
	return cmd
}

func simpleCommand(name string, short string, res *Result) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = name
			return nil
		},
	}
}

// hiddenSimpleCommand is simpleCommand with Hidden set, for maintenance verbs
// that stay callable and completable but are omitted from --help listings.
func hiddenSimpleCommand(name string, short string, res *Result) *cobra.Command {
	cmd := simpleCommand(name, short, res)
	cmd.Hidden = true
	return cmd
}

// simpleCommandWithTool is the same as simpleCommand but also registers
// --tool, for commands whose handler reads opts.Tool (e.g. tools).
func simpleCommandWithTool(name string, short string, res *Result) *cobra.Command {
	cmd := simpleCommand(name, short, res)
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool")
	return cmd
}
