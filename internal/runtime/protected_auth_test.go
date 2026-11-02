// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/protectedauth"
	"enclave/internal/tools"
)

type protectedTestBackend struct{ fakeBackend }

func (*protectedTestBackend) Name() string { return backend.NameDocker }

func protectedTestRuntime() *Runtime {
	return &Runtime{
		backend:       &protectedTestBackend{},
		host:          model.Host{UID: "1000", GID: "1000"},
		containerHome: "/home/agent",
		profile:       model.Profile{Name: "claude", Command: "claude", ConfigDir: ".claude"},
		run:           model.RunOptions{Persist: true},
		auth:          model.AuthOptions{ProtectAuth: true, AuthScope: model.AuthScopeShared, AuthName: "test"},
	}
}

func TestProtectedAuthRejectsUnsupportedSources(t *testing.T) {
	cases := map[string]func(*Runtime){
		"unnamed shared identity": func(r *Runtime) { r.auth.AuthName = "" },
		"admin":                   func(r *Runtime) { r.run.Admin = true },
		"UID remap":               func(r *Runtime) { r.build.RuntimeUIDRemap = true },
		"root host":               func(r *Runtime) { r.host.UID = "0" },
		"different build UID":     func(r *Runtime) { r.build.BuildUID = "999" },
		"different build GID":     func(r *Runtime) { r.build.BuildGID = "999" },
		"custom image":            func(r *Runtime) { r.build.BaseImage = "custom" },
		"native config":           func(r *Runtime) { r.run.HostConfig = model.HostConfigPassthrough },
		"OAuth env injection":     func(r *Runtime) { r.auth.PassEnv = []string{"CLAUDE_CODE_OAUTH_TOKEN"} },
		"Copilot API token env":   func(r *Runtime) { r.auth.PassEnv = []string{"GITHUB_COPILOT_API_TOKEN"} },
		"settings override":       func(r *Runtime) { r.run.CmdArgs = []string{"--settings={}"} },
		"settings precedence":     func(r *Runtime) { r.run.CmdArgs = []string{"--setting-sources", "project"} },
		"unknown tool":            func(r *Runtime) { r.profile.Name = "unknown" },
		"foreign layout":          func(r *Runtime) { r.profile.Name = "codex" },
		"native feature auth": func(r *Runtime) {
			r.features = []model.Extension{{Name: "github-cli", ConfigDir: ".config/gh", AuthFiles: []string{"hosts.yml"}}}
		},
	}
	if err := protectedTestRuntime().validateProtectedAuth(); err != nil {
		t.Fatal(err)
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := protectedTestRuntime()
			change(r)
			if err := r.validateProtectedAuth(); err == nil {
				t.Fatal("unsupported combination accepted")
			}
		})
	}
}

func TestProtectedAuthAllowsInactiveDefaultHostConfigPaths(t *testing.T) {
	r := protectedClaudeConfigRuntime(t)
	for _, mode := range []string{model.HostConfigNone, ""} {
		r.run.HostConfig = mode
		if err := r.validateProtectedAuth(); err != nil {
			t.Fatalf("host_config=%q rejected with resolved Claude defaults %v: %v", mode, r.run.HostConfigPaths, err)
		}
	}
	r.run.HostConfig = model.HostConfigPassthrough
	if err := r.validateProtectedAuth(); err == nil {
		t.Fatal("active native host config passthrough accepted")
	} else if !strings.Contains(err.Error(), "--host-config=none") {
		t.Fatalf("passthrough rejection omitted the safe configuration: %v", err)
	}
}

func TestProtectedAuthAllowsSettingsPatchesWithoutNativeHostConfig(t *testing.T) {
	r := protectedClaudeConfigRuntime(t)
	native := config.HostProfileConfigDir(r.host.Home, r.profile)
	writeFile(t, filepath.Join(native, "settings.json"), `{"apiKeyHelper":"cat native-auth","nativeOnly":true}`)
	writeFile(t, filepath.Join(native, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"native-auth"}}`)
	writeFile(t, filepath.Join(native, "commands", "native-only.md"), "native host command")
	writeSettingsPatch(t, r, false, `{"model":"sonnet"}`)
	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("compose protected settings patch: %v", err)
	}
	settings := settingsRelativePath(t, r)
	if err := r.validateProtectedSettings(generatedConfigPath(r, settings), false); err != nil {
		t.Fatalf("ordinary settings patch rejected: %v", err)
	}
	got := readGeneratedJSON(t, r, settings)
	if got["model"] != "sonnet" {
		t.Fatalf("settings patch was not applied: %v", got)
	}
	for _, key := range []string{"apiKeyHelper", "nativeOnly"} {
		if _, exists := got[key]; exists {
			t.Fatalf("native host setting %s imported with host_config=none", key)
		}
	}
	for _, name := range []string{".credentials.json", "commands/native-only.md"} {
		if _, err := os.Lstat(generatedConfigPath(r, name)); !os.IsNotExist(err) {
			t.Fatalf("native host file %s imported with host_config=none: %v", name, err)
		}
	}
}

func protectedClaudeConfigRuntime(t *testing.T) *Runtime {
	t.Helper()
	r := protectedTestRuntime()
	r.host.Home = t.TempDir()
	r.project.Hash = "protected-config"
	r.paths = model.Paths{ToolsDir: filepath.Join("..", "..", "extensions", "tools")}
	profile, err := config.LoadProfile(r.paths, "claude")
	if err != nil {
		t.Fatalf("load real Claude profile: %v", err)
	}
	r.profile = profile
	r.run.HostConfig = model.HostConfigNone
	r.run.HostConfigPaths = config.ResolveHostConfigPaths(profile, nil)
	if len(r.run.HostConfigPaths) == 0 {
		t.Fatal("real Claude profile did not resolve default host config paths")
	}
	return r
}

func TestProtectedAuthMountsOnlySessionView(t *testing.T) {
	r := protectedTestRuntime()
	acc := &mountAccumulator{}
	ref := &backend.StoreRef{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "claude", Suffix: "test"}}
	r.addAuthMount(acc, ref)
	if got := acc.Stores(); len(got) != 1 || got[0].Kind != backend.StoreKindAuth || got[0].Key != ref.Key || got[0].ContainerPath != "/home/agent/.enclave-auth" || len(acc.Mounts()) != 0 {
		t.Fatalf("auth store is not the separately mounted session view: %+v", got)
	}
	for _, key := range []string{model.EnvAuthDir, "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
		if got, ok := lookupEnv(acc.Env(), key); !ok || got != "/home/agent/.enclave-auth" {
			t.Fatalf("%s = %q", key, got)
		}
	}
	if _, stores := newVolumeManager(r).BuildPrep("task"); !backend.IsProtectedConfigKey(stores.Config.Key) || stores.Config.Key.Suffix != "task.protected" {
		t.Fatalf("protected session shares an ordinary config store: %+v", stores.Config.Key)
	}
	if _, ok := lookupEnv(acc.Env(), model.EnvAuthFiles); ok {
		t.Fatal("legacy auth reconcile enabled")
	}
	if spec := newVolumeManager(r).authSyncSpec(storeSet{Config: &backend.StoreRef{Kind: backend.StoreKindConfig}, Auth: ref}); spec != nil {
		t.Fatal("real grant scheduled for auth reconciliation")
	}
	if err := r.validateProtectedBindings(acc.Env(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedAuthRejectsLateEnvironmentOverrides(t *testing.T) {
	r := protectedTestRuntime()
	for _, assignment := range []string{
		"CLAUDE_CONFIG_DIR=/other", "ENCLAVE_AUTH_FILES=.credentials.json",
		"CLAUDE_CODE_CUSTOM_OAUTH_URL=https://other", "CLAUDE_CODE_OAUTH_TOKEN=real-secret",
		"HTTPS_PROXY=http://other", "ANTHROPIC_API_KEY=real-secret",
		"CLAUDE_CODE_SESSION_ACCESS_TOKEN=real-secret", "CLAUDE_CODE_HOST_CREDS_FILE=/real-secret",
		"CLAUDE_CODE_HOST_AUTH_ENV_VAR=REAL_SECRET", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=1",
	} {
		err := r.validateProtectedBindings([]string{assignment}, nil)
		if err == nil {
			t.Errorf("accepted %s", strings.Split(assignment, "=")[0])
		}
		if err != nil && strings.Contains(err.Error(), "real-secret") {
			t.Fatal("credential appeared in error")
		}
	}
}

func TestProtectedGitHubUsesIndependentNativeViewWithoutReconcile(t *testing.T) {
	r := protectedTestRuntime()
	r.features = []model.Extension{{Name: "github-cli", ConfigDir: ".config/gh", AuthFiles: []string{"hosts.yml", "config.yml"}}}
	if err := r.validateProtectedAuth(); err != nil {
		t.Fatal(err)
	}
	store := backend.StoreRef{Kind: backend.StoreKindFeatureAuth, Key: backend.StoreKey{Owner: "github-cli"}}
	stores := storeSet{Config: &backend.StoreRef{Kind: backend.StoreKindConfig}, Features: map[string]backend.StoreRef{"github-cli": store}}
	acc := &mountAccumulator{}
	r.addFeatureAuthMounts(acc, stores.Features)
	if got := acc.Stores(); len(got) != 1 || got[0].Kind != backend.StoreKindFeatureAuth || got[0].ContainerPath != "/home/agent/.config/gh" {
		t.Fatalf("feature did not use native projected config view: %v", got)
	}
	if _, ok := lookupEnv(acc.Env(), model.EnvFeatureAuthMap); ok {
		t.Fatal("protected feature enabled native-file symlink reconciliation")
	}
	if got, ok := lookupEnv(acc.Env(), model.EnvProtectedGitHub); !ok || got != "1" {
		t.Fatal("protected GitHub launcher was not enabled")
	}
	if err := r.validateProtectedBindings(acc.Env(), nil); err != nil {
		t.Fatal(err)
	}
	if spec := newVolumeManager(r).authSyncSpec(stores); spec != nil {
		t.Fatal("workload feature view scheduled for copyback")
	}
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_CONFIG_DIR", "GH_HOST", model.EnvProtectedGitHub} {
		if err := r.validateProtectedBindings(append(acc.Env(), key+"=unsafe"), nil); err == nil {
			t.Errorf("accepted raw credential or routing override %s", key)
		}
		r.auth.PassEnv = []string{key}
		if err := r.validateProtectedAuth(); err == nil {
			t.Errorf("accepted protected feature --pass-env %s", key)
		}
	}
}

func TestProtectedGitHubRejectsNonstandardNativeAdapter(t *testing.T) {
	for _, feature := range []model.Extension{
		{Name: "github-cli"},
		{Name: "github-cli", ConfigDir: ".claude", AuthFiles: []string{"hosts.yml", "config.yml"}},
		{Name: "github-cli", ConfigDir: ".config/gh", AuthFiles: []string{"hosts.yml", "config.yml", "../token"}},
		{Name: "other-native", ConfigDir: ".config/other", AuthFiles: []string{"auth.json"}},
	} {
		r := protectedTestRuntime()
		r.features = []model.Extension{feature}
		if err := r.validateProtectedAuth(); err == nil {
			t.Fatalf("accepted unsupported native adapter %+v", feature)
		}
	}
}

func TestProtectedGitHubRejectsSettingsCredentialOverrides(t *testing.T) {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_CONFIG_DIR", "GH_HOST", model.EnvProtectedGitHub} {
		file := filepath.Join(t.TempDir(), "settings.json")
		writeFile(t, file, fmt.Sprintf("{\"env\":{%q:\"override\"}}", key))
		if err := protectedTestRuntime().validateProtectedSettings(file, true); err == nil {
			t.Errorf("accepted GitHub settings override %s", key)
		}
	}
}

func TestProtectedAuthSettingsValidation(t *testing.T) {
	for _, tc := range []struct {
		body string
		fail bool
	}{
		{`{"env":{"DISABLE_TELEMETRY":"1"},"permissions":{"allow":[]}}`, false},
		{`{"apiKeyHelper":"cat secret"}`, true},
		{`{"env":{"ANTHROPIC_AUTH_TOKEN":"real-secret"}}`, true},
		{`{"env":{"CLAUDE_CONFIG_DIR":"/other"}}`, true},
		{`{"env":{"CLAUDE_CODE_HOST_CREDS_FILE":"/other"}}`, true},
		{`{"forceLoginMethod":"console"}`, true},
		{`{broken`, true},
	} {
		file := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(file, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		err := protectedTestRuntime().validateProtectedSettings(file, false)
		if (err != nil) != tc.fail {
			t.Errorf("settings accepted=%v, wanted=%v", err == nil, !tc.fail)
		}
		if err != nil && strings.Contains(err.Error(), "real-secret") {
			t.Fatal("credential appeared in error")
		}
	}
}

func TestProtectedCommandUsesOrdinaryClient(t *testing.T) {
	r := protectedTestRuntime()
	r.run.CmdArgs = []string{"-p", "hello"}
	cmd := newCommandBuilder(r).Build()
	if len(cmd) < 7 || cmd[4] != "claude" || strings.Contains(strings.Join(cmd, " "), "--setting-sources") {
		t.Fatalf("protected command does not run the ordinary client with all setting sources: %v", cmd)
	}
}

func TestProtectedRequestPinsNumericWorkloadIdentity(t *testing.T) {
	r := protectedTestRuntime()
	req := r.backendRequest(&ExecutionContext{ContainerName: "protected"}, false, false)
	if req.User != "1000:1000" {
		t.Fatalf("image-default user used: %q", req.User)
	}
}

func TestProtectedAuthCannotLaunchWithoutDeclaredAuthStore(t *testing.T) {
	r := protectedTestRuntime()
	if _, err := r.prepareVolumes("protected", "protected", &mountAccumulator{}); err == nil {
		t.Fatal("protected session accepted without broker admission")
	}
	if len(r.backend.(*protectedTestBackend).prepared) != 0 {
		t.Fatal("prepared legacy stores before rejecting missing broker admission")
	}
}

func TestProtectedClientPinsStayConsistent(t *testing.T) {
	for _, client := range []struct {
		version string
		files   map[string][]string
	}{
		{"2.87.3", map[string][]string{
			"extensions/features/github-cli/install.sh": {"protected_version=%s"},
			"extensions/features/github-cli/feature-entrypoint.d/setup.sh": {
				"/opt/enclave/gh-%s/bin/gh", `"gh version %s "*`,
			},
			"internal/protectedclient/protectedclient.go": {
				"/opt/enclave/gh-%s/bin/gh", "'gh version %s '*",
			},
		}},
	} {
		for name, patterns := range client.files {
			t.Run(name+"/"+client.version, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
				if err != nil {
					t.Fatal(err)
				}
				for _, pattern := range patterns {
					want := fmt.Sprintf(pattern, client.version)
					if !strings.Contains(string(data), want) {
						t.Errorf("protected installer/launcher pin mismatch: missing %q", want)
					}
				}
			})
		}
	}
}

func TestProtectedIdentityFollowsScope(t *testing.T) {
	shared := protectedTestRuntime()
	if got, err := shared.protectedAuthIdentity(); err != nil || got != shared.auth.AuthName {
		t.Fatalf("shared identity = %q, %v", got, err)
	}

	project := protectedTestRuntime()
	project.auth.AuthScope, project.auth.AuthName = model.AuthScopeProject, ""
	if err := project.validateProtectedAuth(); err != nil {
		t.Fatal(err)
	}
	if got, err := project.protectedAuthIdentity(); err != nil || got != "project."+project.project.Hash {
		t.Fatalf("project identity = %q, %v", got, err)
	}

	ephemeral := protectedTestRuntime()
	ephemeral.run.Persist, ephemeral.run.Ephemeral, ephemeral.auth.AuthName = false, true, ""
	if err := ephemeral.validateProtectedAuth(); err != nil {
		t.Fatal(err)
	}
	first, err := ephemeral.protectedAuthIdentity()
	if err != nil || !backend.IsProtectedEphemeralIdentity(backend.StoreKey{Owner: "claude", Suffix: first}) {
		t.Fatalf("ephemeral identity = %q, %v", first, err)
	}
	if again, _ := ephemeral.protectedAuthIdentity(); again != first {
		t.Fatal("ephemeral identity changed within one run")
	}
	other := protectedTestRuntime()
	other.run.Persist, other.run.Ephemeral = false, true
	if second, _ := other.protectedAuthIdentity(); second == first {
		t.Fatal("ephemeral runs share an identity")
	}

	theia := protectedTestRuntime()
	theia.profile = model.Profile{Name: "theia", ConfigDir: ".theia-ide"}
	theia.auth.AuthName = ""
	if err := theia.validateProtectedAuth(); err != nil {
		t.Fatalf("tool without login adapters needs no identity name: %v", err)
	}
	if got, err := theia.protectedAuthIdentity(); err != nil || !backend.IsProtectedEphemeralIdentity(backend.StoreKey{Owner: "theia", Suffix: got}) {
		t.Fatalf("theia identity = %q, %v", got, err)
	}

	ephemeral.features = []model.Extension{{Name: "github-cli", ConfigDir: ".config/gh", AuthFiles: []string{"hosts.yml", "config.yml"}}}
	_, stores := newVolumeManager(ephemeral).BuildPrep("run")
	if stores.Auth == nil || stores.Auth.Key.Suffix != first {
		t.Fatalf("ephemeral session does not use its identity: %+v", stores.Auth)
	}
	if _, ok := stores.Features["github-cli"]; !ok {
		t.Fatal("ephemeral session lacks the gateway-owned GitHub store; a login would reach the workload")
	}
}

func TestProtectedProvidersNameSpecProviders(t *testing.T) {
	paths := model.Paths{ToolsDir: filepath.Join("..", "..", "extensions", "tools")}
	installed, err := config.ListProfiles(paths)
	if err != nil {
		t.Fatal(err)
	}
	var protected []string
	for _, tool := range protectedauth.Tools() {
		protected = append(protected, tool.Name)
	}
	slices.Sort(protected)
	if !slices.Equal(installed, protected) {
		t.Fatalf("shipped tools %v differ from protected tools %v", installed, protected)
	}
	for _, tool := range protectedauth.Tools() {
		profile, err := config.LoadProfile(paths, tool.Name)
		if err != nil {
			t.Fatalf("load %s profile: %v", tool.Name, err)
		}
		covered := map[string]bool{}
		for provider, adapter := range tool.Providers {
			covered[adapter] = true
			if !slices.ContainsFunc(profile.Providers, func(p model.ProviderConfig) bool { return p.Name == provider }) {
				t.Errorf("%s has no provider %s", tool.Name, provider)
			}
			if !tool.Uses(adapter) {
				t.Errorf("%s provider %s maps to unused adapter %s", tool.Name, provider, adapter)
			}
		}
		for _, adapter := range tool.Adapters {
			if protectedauth.GrantFile(adapter) == "" || !covered[adapter] {
				t.Errorf("%s adapter %s has no grant file or provider", tool.Name, adapter)
			}
		}
	}
}

func TestProtectedMistralCompatibilityUsesInstalledProfile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mistral-vibe", config.SpecFilename), `schemaVersion: "1"
kind: sandbox
name: mistral-vibe
sandbox:
  configDir: .vibe
  entrypoint: {run: [vibe]}
providers:
  - name: mistral
    authFiles: [.env]
`)
	profile, err := config.LoadProfile(model.Paths{UserToolsDir: root}, "mistral-vibe")
	if err != nil {
		t.Fatal(err)
	}
	runtime := protectedTestRuntime()
	runtime.profile = profile
	if err := runtime.validateProtectedAuth(); err != nil {
		t.Fatalf("canonical installed extension refused: %v", err)
	}
	if slices.ContainsFunc(protectedauth.Tools(), func(tool protectedauth.Tool) bool { return tool.Name == profile.Name }) {
		t.Fatal("optional extension was advertised as a shipped tool")
	}
	runtime.profile.ConfigDir = ".incompatible"
	if err := runtime.validateProtectedAuth(); err == nil {
		t.Fatal("incompatible installed extension layout accepted")
	}
}

// Like an ordinary login, a captured one makes later sessions skip the login
// callback port, so concurrent sessions do not compete for it.
func TestProtectedLoginsSkipCallbackPorts(t *testing.T) {
	r := protectedTestRuntime()
	profile, err := config.LoadProfile(model.Paths{ToolsDir: filepath.Join("..", "..", "extensions", "tools")}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	r.profile = profile
	for _, tc := range []struct {
		logins []string
		ports  []string
	}{
		{nil, []string{"1455"}},
		{[]string{protectedauth.AdapterOpenAI}, nil},
	} {
		r.authSession = &backend.AuthSession{Protected: true, Logins: tc.logins}
		state := newAuthManager(r).protectedAuthState(nil)
		ctx := model.RunContext{Profile: r.profile, AuthState: state}
		if got := tools.OAuthPortHints(ctx); !slices.Equal(got, tc.ports) {
			t.Errorf("logins %v: port hints = %v, want %v", tc.logins, got, tc.ports)
		}
	}
}

// A protected session refuses credentials it could only pass raw, and native
// credential files in the composed config source.
func TestPrepareProtectedRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(t *testing.T, r *Runtime)
		want  string
	}{
		"credential without release rule": {
			setup: func(t *testing.T, r *Runtime) {
				t.Setenv("VENDOR_TOKEN", "real-secret")
				r.profile.Secrets = map[string]model.SecretConfig{"vendor": secretConfig([]string{"VENDOR_TOKEN"}, nil)}
			},
			want: "without a gateway release rule",
		},
		"credential location override": {
			setup: func(t *testing.T, r *Runtime) {
				t.Setenv("CODEX_HOME", "/real-secret")
				r.profile.Secrets = map[string]model.SecretConfig{"codex-home": secretConfig([]string{"CODEX_HOME"}, nil)}
			},
			want: "conflicts with configured credential",
		},
		"native file in config source": {
			setup: func(t *testing.T, r *Runtime) {
				r.configSourceDir = t.TempDir()
				if err := os.WriteFile(filepath.Join(r.configSourceDir, "auth.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "refuses native auth file in config source",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := runtimeWithProfile(t, model.Profile{Name: "codex"})
			r.auth.ProtectAuth = true
			tc.setup(t, r)
			env := []string{}
			if _, _, err := newAuthManager(r).prepareProtected(&env, storeSet{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("prepareProtected = %v, want %q", err, tc.want)
			}
			if slices.ContainsFunc(env, func(entry string) bool { return strings.Contains(entry, "real-secret") }) {
				t.Fatalf("refused credential reached the environment: %v", env)
			}
		})
	}
}
