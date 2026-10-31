// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

// networkPreviewTestInput selects a tool with a spec domain, a feature with
// allowed, denied, and credential release domains, and an allow_domains entry.
func networkPreviewTestInput(t *testing.T) *CommandInput {
	t.Helper()
	root := t.TempDir()
	paths := model.Paths{
		ToolsDir:      filepath.Join(root, "tools"),
		FeaturesDir:   filepath.Join(root, "features"),
		AllowlistsDir: filepath.Join(root, "allowlists"),
	}
	writeAppFile(t, filepath.Join(paths.ToolsDir, "speccy", config.SpecFilename), `schemaVersion: "1"
kind: sandbox
name: speccy
sandbox:
  entrypoint: { run: [speccy] }
  configDir: .speccy
network:
  allowedDomains: [tool.example.com]
`, 0o644)
	writeAppFile(t, filepath.Join(paths.FeaturesDir, "svc", config.SpecFilename), `schemaVersion: "1"
kind: mixin
name: svc
defaultEnabled: false
credentials:
  sources:
    svc-token: { env: [SVC_TOKEN] }
network:
  allowedDomains: [svc.example.com]
  deniedDomains: [telemetry.svc.example.com]
  serviceDomains:
    api.svc.example.org: svc-token
  serviceAuth:
    svc-token: { headerName: authorization, valueFormat: "Bearer %s" }
`, 0o644)

	opts := config.DefaultOptions()
	opts.Tool = "speccy"
	opts.Features = []string{"+svc"}
	opts.AllowDomains = []string{"Adhoc.Example.com"}
	return configTestInput(t, paths, opts, nil)
}

func TestNetworkPrintReflectsSessionOptions(t *testing.T) {
	input := networkPreviewTestInput(t)

	var code int
	out := captureStdout(t, func() { code = runNetworkPrint(input) })
	if code != 0 {
		t.Fatalf("runNetworkPrint exit code = %d\n%s", code, out)
	}
	for _, want := range []string{
		"server=/tool.example.com/",
		"server=/svc.example.com/",
		"server=/api.svc.example.org/",
		"server=/adhoc.example.com/",
		"address=/telemetry.svc.example.com/",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("network print missing %q:\n%s", want, out)
		}
	}

	input.Options.AllowAllNetwork = true
	out = captureStdout(t, func() { code = runNetworkPrint(input) })
	if code != 0 || out != "# Mode: unrestricted\n" {
		t.Fatalf("network print with allow_all_network = %d %q, want the unrestricted marker", code, out)
	}
}

func runNetworkPrintJSON(t *testing.T, input *CommandInput) (int, string, map[string]any) {
	t.Helper()
	input.NetworkPrintJSON = true
	var code int
	out, _ := captureOutput(t, func() { code = runNetworkPrint(input) })
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshal network print JSON: %v\n%s", err, out)
	}
	return code, out, doc
}

func TestNetworkPrintJSONEnvelope(t *testing.T) {
	input := networkPreviewTestInput(t)
	project, err := input.Ctx.Project()
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}

	code, out, doc := runNetworkPrintJSON(t, input)
	want := map[string]any{
		"schemaVersion": "1",
		"kind":          "network-policy",
		"tool":          "speccy",
		"project":       map[string]any{"dir": project.Dir, "hash": project.Hash},
		"mode":          "restricted",
		"domains":       []any{"adhoc.example.com", "api.svc.example.org", "svc.example.com", "tool.example.com"},
		"denied":        []any{"telemetry.svc.example.com"},
		"notes":         []any{networkPolicyNoteHostsFromCredential},
		"error":         "",
	}
	if code != 0 || !reflect.DeepEqual(doc, want) {
		t.Fatalf("network print --json = %d\n%s\nwant %#v", code, out, want)
	}

	input.Options.AllowAllNetwork = true
	code, out, doc = runNetworkPrintJSON(t, input)
	want["mode"] = "unrestricted"
	want["domains"], want["denied"], want["notes"] = []any{}, []any{}, []any{}
	if code != 0 || !reflect.DeepEqual(doc, want) {
		t.Fatalf("network print --json with allow_all_network = %d\n%s\nwant %#v", code, out, want)
	}

	input.Options.AllowAllNetwork = false
	input.Options.ProjectMount = "bogus"
	code, out, doc = runNetworkPrintJSON(t, input)
	if code != 1 || doc["mode"] != nil || !strings.Contains(doc["error"].(string), "--project-mount") {
		t.Fatalf("network print --json with invalid options = %d, want exit 1, null mode, and the error:\n%s", code, out)
	}
	want["mode"], want["error"] = nil, doc["error"]
	if !reflect.DeepEqual(doc, want) {
		t.Fatalf("network print --json error envelope:\n%s\nwant %#v", out, want)
	}

	input.Options.ProjectMount = ""
	input.Options.Tool = "bogus"
	code, out, doc = runNetworkPrintJSON(t, input)
	want["tool"], want["error"] = "bogus", "unknown tool: bogus (available: speccy)"
	if code != 1 || !reflect.DeepEqual(doc, want) {
		t.Fatalf("network print --json with an unknown tool = %d\n%s\nwant %#v", code, out, want)
	}
	input.NetworkPrintJSON = false
	if captureOutput(t, func() { code = runNetworkPrint(input) }); code != 1 {
		t.Fatalf("network print with an unknown tool exit code = %d, want 1", code)
	}

}

func TestNetworkPreviewWarnsAboutUnknownFeatures(t *testing.T) {
	input := networkPreviewTestInput(t)
	input.Options.Features = []string{"+svc", "+typo"}

	var code int
	out, errOut := captureOutput(t, func() { code = runNetworkPrint(input) })
	if code != 0 || !strings.Contains(out, "server=/svc.example.com/") {
		t.Fatalf("network print with an unknown feature = %d, want the policy of the known ones:\n%s", code, out)
	}
	if !strings.Contains(errOut, `Unknown feature "typo" selected; it is ignored`) {
		t.Fatalf("stderr = %q, want the unknown-feature warning", errOut)
	}

	code, out, doc := runNetworkPrintJSON(t, input)
	domains, _ := doc["domains"].([]any)
	if code != 0 || doc["error"] != "" || !slices.Contains(domains, any("svc.example.com")) {
		t.Fatalf("network print --json with an unknown feature = %d, want the policy without an error:\n%s", code, out)
	}
}

func TestNetworkPrintJSONWithoutTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME does not select the home directory on Windows")
	}
	setRunningAsRoot(t, false)
	stubToolResolution(t, nil, false, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	projectDir := t.TempDir()
	t.Chdir(projectDir)
	project, err := config.ResolveProjectFromDir(projectDir)
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}

	var code int
	out, _ := captureOutput(t, func() { code = Run([]string{"network", "print", "--json"}) })
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshal network print JSON: %v\n%s", err, out)
	}
	errText, _ := doc["error"].(string)
	want := map[string]any{
		"schemaVersion": "1",
		"kind":          "network-policy",
		"tool":          nil,
		"project":       map[string]any{"dir": project.Dir, "hash": project.Hash},
		"mode":          nil,
		"domains":       []any{},
		"denied":        []any{},
		"notes":         []any{},
		"error":         errText,
	}
	if code != 1 || !strings.HasPrefix(errText, "no tool configured") || !reflect.DeepEqual(doc, want) {
		t.Fatalf("network print --json without a tool = %d\n%s\nwant %#v", code, out, want)
	}
}

// Invalid options unrelated to the network must not hide running gateways
// from status, while print and diff still reject them.
// TestOnlyNetworkAffectingOptionsChangeThePreview sets every option without
// AffectsNetwork and expects the same policy, so an option that starts shaping
// the allowlist gets its flags on the network preview commands.
func TestOnlyNetworkAffectingOptionsChangeThePreview(t *testing.T) {
	root := t.TempDir()
	paths := model.Paths{
		ToolsDir:      filepath.Join(root, "tools"),
		FeaturesDir:   filepath.Join(root, "features"),
		AllowlistsDir: filepath.Join(root, "allowlists"),
	}
	writeAppFile(t, filepath.Join(paths.ToolsDir, "claude", config.SpecFilename), `schemaVersion: "1"
kind: sandbox
name: claude
sandbox:
  entrypoint: { run: [claude] }
  configDir: .claude
network:
  allowedDomains: [tool.example.com]
`, 0o644)
	for name, defaultEnabled := range map[string]bool{"base": true, "gitlab-cli": false} {
		writeAppFile(t, filepath.Join(paths.FeaturesDir, name, config.SpecFilename), fmt.Sprintf(`schemaVersion: "1"
kind: mixin
name: %[1]s
defaultEnabled: %[2]t
network:
  allowedDomains: [%[1]s.example.com]
`, name, defaultEnabled), 0o644)
	}
	opts := config.DefaultOptions()
	opts.Tool = "claude"
	input := configTestInput(t, paths, opts, nil)
	_, out, want := runNetworkPrintJSON(t, input)
	if want["error"] != "" || !strings.Contains(out, "base.example.com") {
		t.Fatalf("baseline network print --json:\n%s", out)
	}

	options := reflect.ValueOf(&input.Options).Elem()
	for _, def := range config.OptionDefs() {
		if def.AffectsNetwork || def.Kind == config.OptionKindYolo {
			continue
		}
		field := options.FieldByName(def.OptionField)
		if !field.IsValid() {
			t.Fatalf("option %s: model.Options has no field %s", def.Name, def.OptionField)
		}
		saved := reflect.New(field.Type()).Elem()
		saved.Set(field)
		switch def.Kind {
		case config.OptionKindBool:
			field.SetBool(!field.Bool())
		case config.OptionKindStringSlice:
			field.Set(reflect.ValueOf([]string{"probe.example.com"}))
		case config.OptionKindStringMap:
			field.Set(reflect.ValueOf(map[string]string{"PROBE": "probe.example.com"}))
		default:
			field.SetString("probe")
		}
		_, out, doc := runNetworkPrintJSON(t, input)
		field.Set(saved)
		if doc["error"] != "" {
			continue
		}
		if !reflect.DeepEqual(doc, want) {
			t.Errorf("option %s changes the network policy but lacks AffectsNetwork:\n%s", def.Name, out)
		}
	}
}

func TestNetworkStatusFallsBackOnInvalidOptions(t *testing.T) {
	stubDockerPing(t, errors.New("engine down"))
	input := networkPreviewTestInput(t)
	input.Options.ProjectMount = "bogus"

	var code int
	out, errOut := captureOutput(t, func() { code = runNetworkStatus(input) })
	if code != 0 || !strings.Contains(out, "Runtime policy state:") {
		t.Fatalf("runNetworkStatus = %d, want the runtime section:\n%s", code, out)
	}
	if !strings.Contains(errOut, "Cannot preview the session policy for these options (--project-mount") {
		t.Fatalf("stderr = %q, want the preview fallback warning", errOut)
	}
	for name, run := range map[string]func(*CommandInput) int{"print": runNetworkPrint, "diff": runNetworkDiff} {
		captureOutput(t, func() { code = run(input) })
		if code != 1 {
			t.Fatalf("%s exit code = %d, want 1 for invalid options", name, code)
		}
	}
}

func TestHashGatewayBundleDirStableForContent(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		filepath.Base(model.GatewayConfigDNSMasqPath): "dnsmasq",
		filepath.Base(model.GatewayConfigDomainsPath): "example.com\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	first, err := hashGatewayBundleDir(dir)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	second, err := hashGatewayBundleDir(dir)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first != second {
		t.Fatalf("expected stable hash, got %q and %q", first, second)
	}

	if err := os.WriteFile(filepath.Join(dir, filepath.Base(model.GatewayConfigDomainsPath)), []byte("changed.example.com\n"), 0o644); err != nil {
		t.Fatalf("rewrite domains: %v", err)
	}
	third, err := hashGatewayBundleDir(dir)
	if err != nil {
		t.Fatalf("third hash: %v", err)
	}
	if third == first {
		t.Fatal("expected hash to change when bundle content changes")
	}
}

func TestRunMutationRuntimeApplyNoApply(t *testing.T) {
	input := &CommandInput{Options: model.Options{RunOptions: model.RunOptions{NoApply: true}}}
	if exitCode := runMutationRuntimeApply(input); exitCode != 0 {
		t.Fatalf("expected no-apply mutation to succeed, got %d", exitCode)
	}
}

func TestGlobalMutationsApplyToEveryToolOfTheProject(t *testing.T) {
	project := model.Project{Dir: "/work/current", RealDir: "/work/current", Hash: "p1"}

	filter, label := gatewayTargetFilter(project, "claude", mutationGatewayScope(false))
	if filter.Tool != "" {
		t.Fatalf("expected no tool filter for a global mutation, got %q", filter.Tool)
	}
	if filter.ProjectHash != "p1" {
		t.Fatalf("expected project filter p1, got %q", filter.ProjectHash)
	}
	if !strings.Contains(label, "all tools") {
		t.Fatalf("expected scope label to name all tools, got %q", label)
	}

	filter, _ = gatewayTargetFilter(project, "claude", mutationGatewayScope(true))
	if filter != (backend.GatewayFilter{}) {
		t.Fatalf("expected an empty filter with --all-running, got %+v", filter)
	}
}

func TestNetworkApplyKeepsTheCurrentToolScope(t *testing.T) {
	project := model.Project{Dir: "/work/current", RealDir: "/work/current", Hash: "p1"}

	filter, label := gatewayTargetFilter(project, "claude", gatewayScopeProjectTool)
	if filter.Tool != "claude" || filter.ProjectHash != "p1" {
		t.Fatalf("expected project/tool filter p1/claude, got %+v", filter)
	}
	if label != currentProjectToolLabel("p1", "claude") {
		t.Fatalf("unexpected scope label %q", label)
	}
}

func TestRetryApplyCommandsCoverEveryFailedTool(t *testing.T) {
	got := retryApplyCommands(gatewayScopeProject, []string{"codex", "claude", "codex"})
	want := []string{"'enclave network apply --tool claude'", "'enclave network apply --tool codex'"}
	if !slices.Equal(got, want) {
		t.Fatalf("retryApplyCommands(project) = %v, want %v", got, want)
	}

	if got := retryApplyCommands(gatewayScopeProject, []string{""}); len(got) != 0 {
		t.Fatalf("a gateway without a tool label cannot be retried with --tool, got %v", got)
	}

	got = retryApplyCommands(gatewayScopeAllRunning, []string{"codex"})
	if !slices.Equal(got, []string{"'enclave network apply --all-running'"}) {
		t.Fatalf("retryApplyCommands(all running) = %v", got)
	}
}

func TestResolveGatewayTargetProjectDirFallbacksToCurrent(t *testing.T) {
	current := model.Project{Dir: "/work/current", Hash: "p1"}
	target := backend.GatewayInfo{ID: "aaaaaaaaaaaa", ProjectHash: "p1"}
	got, err := resolveGatewayTargetProjectDir(current, target)
	if err != nil {
		t.Fatalf("resolve target project dir: %v", err)
	}
	if got != "/work/current" {
		t.Fatalf("expected current project dir fallback, got %q", got)
	}
}

func TestResolveGatewayTargetProjectDirRejectsMissingLabelForForeignTarget(t *testing.T) {
	current := model.Project{Dir: "/work/current", Hash: "p1"}
	target := backend.GatewayInfo{ID: "bbbbbbbbbbbb", ProjectHash: "other"}
	if _, err := resolveGatewayTargetProjectDir(current, target); err == nil {
		t.Fatal("expected error for foreign target without project-dir label")
	}
}

func TestReadGatewayBundleGeneration(t *testing.T) {
	dir := t.TempDir()
	metaPath := filepath.Join(dir, filepath.Base(model.GatewayConfigMetaPath))
	const generation = "2026-02-15T00:00:00.000000001Z"
	if err := os.WriteFile(metaPath, []byte("{\"generation\":\""+generation+"\"}\n"), 0o644); err != nil {
		t.Fatalf("write meta.json: %v", err)
	}
	got, err := readGatewayBundleGeneration(dir)
	if err != nil {
		t.Fatalf("read generation: %v", err)
	}
	if got != generation {
		t.Fatalf("unexpected generation: want %q, got %q", generation, got)
	}
}
