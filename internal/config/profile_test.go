// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"enclave/internal/model"
)

func TestValidateAndNormalizePortsDefaults(t *testing.T) {
	p := &model.Profile{
		Name:  "theia",
		Ports: []model.PortConfig{{Container: 3000, Publish: true}},
	}
	if err := validateAndNormalizeProfile(p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.Ports[0].Label; got != "theia" {
		t.Errorf("Label default = %q, want %q", got, "theia")
	}
	if got, want := p.Ports[0].OpenURL, "http://localhost:"+model.PortHostPlaceholder; got != want {
		t.Errorf("OpenURL default = %q, want %q", got, want)
	}
}

func TestValidateAndNormalizePortsPreservesExplicitValues(t *testing.T) {
	p := &model.Profile{
		Name:  "theia",
		Ports: []model.PortConfig{{Container: 3000, Publish: true, Label: "Theia IDE", OpenURL: "https://example/{host_port}"}},
	}
	if err := validateAndNormalizeProfile(p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Ports[0].Label != "Theia IDE" || p.Ports[0].OpenURL != "https://example/{host_port}" {
		t.Errorf("explicit values not preserved: %+v", p.Ports[0])
	}
}

func TestValidateAndNormalizePortsRejectsOutOfRange(t *testing.T) {
	for _, bad := range []int{0, -1, 70000} {
		p := &model.Profile{Ports: []model.PortConfig{{Container: bad, Publish: true}}}
		if err := validateAndNormalizeProfile(p); err == nil {
			t.Errorf("expected error for container %d", bad)
		}
	}
}

func TestValidateAndNormalizeProfileRejectsNegativeQEMUMemory(t *testing.T) {
	p := &model.Profile{QEMUMinMemoryMiB: -1}
	if err := validateAndNormalizeProfile(p); err == nil || !strings.Contains(err.Error(), "qemu_min_memory_mib") {
		t.Fatalf("expected qemu_min_memory_mib validation error, got %v", err)
	}
}

func TestValidateAndNormalizeProfileValidatesSkillsPath(t *testing.T) {
	tests := []struct {
		name      string
		configDir string
		skillsDir string
		wantError bool
	}{
		{name: "unsupported tool", configDir: ".theia"},
		{name: "relative child", configDir: ".pi", skillsDir: ".pi/agent/skills"},
		{name: "absolute child", configDir: "/opt/tool", skillsDir: "/opt/tool/skills"},
		{name: "missing config", skillsDir: ".tool/skills", wantError: true},
		{name: "same directory", configDir: ".tool", skillsDir: ".tool", wantError: true},
		{name: "sibling directory", configDir: ".tool", skillsDir: ".other/skills", wantError: true},
		{name: "prefix sibling", configDir: ".tool", skillsDir: ".tool-extra/skills", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := &model.Profile{ConfigDir: tt.configDir, SkillsDir: tt.skillsDir}
			err := validateAndNormalizeProfile(profile)
			if tt.wantError && (err == nil || !strings.Contains(err.Error(), "skills_dir")) {
				t.Fatalf("expected skills_dir validation error, got %v", err)
			}
			if !tt.wantError && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestLoadProfileReadsQEMUSettings(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"qemu_min_memory_mib": 4096,
		"qemu_store_cache_mmap": true
	}`)

	profile, err := LoadProfile(paths, "tool")
	if err != nil {
		t.Fatalf("LoadProfile() error = %v", err)
	}
	if profile.QEMUMinMemoryMiB != 4096 {
		t.Fatalf("QEMUMinMemoryMiB = %d, want 4096", profile.QEMUMinMemoryMiB)
	}
	if !profile.QEMUStoreCacheMmap {
		t.Fatal("QEMUStoreCacheMmap = false, want true")
	}
}

func TestLoadProfileNormalizesSecretsAndCredentialSecrets(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"providers": [
			{
				"name": "provider",
				"credential_secrets": [" tool-api-key ", "tool-api-key"]
			}
		],
		"secrets": {
			"tool-api-key": {
				"env_vars": [" TOOL_API_KEY ", "TOOL_API_KEY"],
				"release": {
					"http": {
						"hosts": ["API.EXAMPLE.COM", "api.example.com", "*.Example.com"],
						"header": " Authorization ",
						"format": "Bearer %s"
					}
				}
			}
		}
	}`)

	profile, err := LoadProfile(paths, "tool")
	if err != nil {
		t.Fatalf("LoadProfile() error = %v", err)
	}

	cfg, ok := profile.Secrets["tool-api-key"]
	if !ok {
		t.Fatalf("secret tool-api-key not found")
	}
	if got, want := strings.Join(cfg.EnvVars, ","), "TOOL_API_KEY"; got != want {
		t.Fatalf("env_vars = %q, want %q", got, want)
	}
	release := cfg.ReleaseHTTP()
	if release == nil {
		t.Fatalf("release.http = nil, want non-nil")
		return
	}
	if release.Header != "authorization" {
		t.Fatalf("header = %q, want %q", release.Header, "authorization")
	}
	// Host order here reflects the spec loader's serviceAuth merge, which
	// sorts hosts before de-duplication (see buildSecrets/sortDedupeStrings
	// in spec_map.go) rather than preserving declaration order.
	if got, want := strings.Join(release.Hosts, ","), "*.example.com,api.example.com"; got != want {
		t.Fatalf("hosts = %q, want %q", got, want)
	}
	if got, want := strings.Join(profile.Providers[0].CredentialSecrets, ","), "tool-api-key"; got != want {
		t.Fatalf("credential_secrets = %q, want %q", got, want)
	}
}

func TestLoadProfileRejectsUnknownProviderCredentialSecret(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"providers": [
			{
				"name": "provider",
				"credential_secrets": ["missing-secret"]
			}
		],
		"secrets": {
			"tool-api-key": {
				"env_vars": ["TOOL_API_KEY"]
			}
		}
	}`)

	_, err := LoadProfile(paths, "tool")
	if err == nil {
		t.Fatalf("LoadProfile() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "credential secret") {
		t.Fatalf("LoadProfile() error = %q, want credential secret validation", err)
	}
}

func TestLoadProfilePreservesSecretAPIKeyFalse(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"providers": [
			{
				"name": "provider",
				"credential_secrets": ["oauth-token"]
			}
		],
		"secrets": {
			"oauth-token": {
				"env_vars": ["OAUTH_TOKEN"],
				"api_key": false
			}
		}
	}`)

	profile, err := LoadProfile(paths, "tool")
	if err != nil {
		t.Fatalf("LoadProfile() error = %v", err)
	}
	if profile.Secrets["oauth-token"].APIKey == nil || *profile.Secrets["oauth-token"].APIKey {
		t.Fatalf("oauth-token api_key = %#v, want false", profile.Secrets["oauth-token"].APIKey)
	}
	if profile.ProviderAPIKeySecretIDs()["oauth-token"] {
		t.Fatalf("ProviderAPIKeySecretIDs()[oauth-token] = true, want false")
	}
}

func TestLoadProfileDefaultsProviderCredentialSecretsToAPIKeys(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"providers": [
			{
				"name": "provider",
				"credential_secrets": ["tool-api-key"]
			}
		],
		"secrets": {
			"tool-api-key": {
				"env_vars": ["TOOL_API_KEY"]
			}
		}
	}`)

	profile, err := LoadProfile(paths, "tool")
	if err != nil {
		t.Fatalf("LoadProfile() error = %v", err)
	}
	if !profile.ProviderAPIKeySecretIDs()["tool-api-key"] {
		t.Fatalf("ProviderAPIKeySecretIDs()[tool-api-key] = false, want true")
	}
}

func TestLoadProfileRejectsInvalidFormatTemplate(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"secrets": {
			"tool-api-key": {
				"env_vars": ["TOOL_API_KEY"],
				"release": {
					"http": {
						"hosts": ["api.example.com"],
						"header": "authorization",
						"format": "Bearer token"
					}
				}
			}
		}
	}`)

	_, err := LoadProfile(paths, "tool")
	if err == nil {
		t.Fatalf("LoadProfile() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "format must contain %s") {
		t.Fatalf("LoadProfile() error = %q, want format validation error", err)
	}
}

func TestLoadProfileRejectsDuplicateSecretEnvVarAcrossSecrets(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"secrets": {
			"first-secret": {
				"env_vars": ["SHARED_KEY"]
			},
			"second-secret": {
				"env_vars": ["SHARED_KEY"]
			}
		}
	}`)

	_, err := LoadProfile(paths, "tool")
	if err == nil {
		t.Fatalf("LoadProfile() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "already declared") {
		t.Fatalf("LoadProfile() error = %q, want duplicate env var validation", err)
	}
}

func TestLoadProfileRejectsSecretReleaseWithEmptyHosts(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"secrets": {
			"tool-api-key": {
				"env_vars": ["TOOL_API_KEY"],
				"release": {
					"http": {
						"hosts": [],
						"header": "authorization"
					}
				}
			}
		}
	}`)

	_, err := LoadProfile(paths, "tool")
	if err == nil || !strings.Contains(err.Error(), "hosts must contain at least one domain pattern") {
		t.Fatalf("LoadProfile() error = %v, want empty-hosts validation error", err)
	}
}

func TestLoadProfileRejectsSecretReleaseHeaderWithNewlines(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"secrets": {
			"tool-api-key": {
				"env_vars": ["TOOL_API_KEY"],
				"release": {
					"http": {
						"hosts": ["api.example.com"],
						"header": "authorization\nx-extra"
					}
				}
			}
		}
	}`)

	_, err := LoadProfile(paths, "tool")
	if err == nil || !strings.Contains(err.Error(), "must not contain newlines") {
		t.Fatalf("LoadProfile() error = %v, want newline validation error", err)
	}
}

func TestLoadProfileNormalizesProviderSecurestorageDirEnv(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"providers": [
			{
				"name": "provider",
				"securestorage_dir_env": "  TOOL_SECURESTORAGE_DIR  "
			}
		]
	}`)

	profile, err := LoadProfile(paths, "tool")
	if err != nil {
		t.Fatalf("LoadProfile() error = %v", err)
	}
	if got := profile.Providers[0].SecurestorageDirEnv; got != "TOOL_SECURESTORAGE_DIR" {
		t.Fatalf("securestorage_dir_env = %q, want %q", got, "TOOL_SECURESTORAGE_DIR")
	}
}

func TestLoadProfileRejectsInvalidProviderSecurestorageDirEnv(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"providers": [
			{
				"name": "provider",
				"securestorage_dir_env": "BAD=NAME"
			}
		]
	}`)

	_, err := LoadProfile(paths, "tool")
	if err == nil || !strings.Contains(err.Error(), "invalid securestorage_dir_env") {
		t.Fatalf("LoadProfile() error = %v, want securestorage_dir_env validation error", err)
	}
}

func TestLoadProfileParsesMemoryDir(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"memory_dir": ".tool/memory"
	}`)

	profile, err := LoadProfile(paths, "tool")
	if err != nil {
		t.Fatalf("LoadProfile() error = %v", err)
	}
	if got, want := profile.MemoryDir, filepath.FromSlash(".tool/memory"); got != want {
		t.Fatalf("MemoryDir = %q, want %q", got, want)
	}
}

func TestLoadProfileRejectsAbsoluteMemoryDir(t *testing.T) {
	paths := setupProfileTestPaths(t, `{
		"name": "tool",
		"command": "tool",
		"memory_dir": "/etc/memory"
	}`)

	_, err := LoadProfile(paths, "tool")
	if err == nil || !strings.Contains(err.Error(), "memory_dir") {
		t.Fatalf("LoadProfile() error = %v, want memory_dir validation error", err)
	}
}

// profileFixture keeps validation test inputs concise and is projected onto a
// spec.yaml document by setupProfileTestPaths.
type profileFixture struct {
	Name               string                   `json:"name"`
	Command            string                   `json:"command"`
	MemoryDir          string                   `json:"memory_dir"`
	QEMUMinMemoryMiB   int                      `json:"qemu_min_memory_mib"`
	QEMUStoreCacheMmap bool                     `json:"qemu_store_cache_mmap"`
	Providers          []providerFixture        `json:"providers"`
	Secrets            map[string]secretFixture `json:"secrets"`
}

type providerFixture struct {
	Name                string   `json:"name"`
	CredentialSecrets   []string `json:"credential_secrets"`
	SecurestorageDirEnv string   `json:"securestorage_dir_env"`
}

type secretFixture struct {
	EnvVars []string              `json:"env_vars"`
	APIKey  *bool                 `json:"api_key"`
	Release *secretReleaseFixture `json:"release"`
}

type secretReleaseFixture struct {
	HTTP *httpReleaseFixture `json:"http"`
}

type httpReleaseFixture struct {
	Hosts  []string `json:"hosts"`
	Header string   `json:"header"`
	Format string   `json:"format"`
}

func setupProfileTestPaths(t *testing.T, profileJSON string) model.Paths {
	t.Helper()
	root := t.TempDir()
	toolsDir := filepath.Join(root, "extensions", "tools")
	toolDir := filepath.Join(toolsDir, "tool")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", toolDir, err)
	}

	var fixture profileFixture
	if err := json.Unmarshal([]byte(profileJSON), &fixture); err != nil {
		t.Fatalf("unmarshal profile fixture: %v", err)
	}

	doc := specDocument{
		SchemaVersion: "1",
		Kind:          KindSandbox,
		Name:          fixture.Name,
		Sandbox: &specSandbox{
			Entrypoint:         &specEntrypoint{Run: []string{fixture.Command}},
			MemoryDir:          fixture.MemoryDir,
			QEMUMinMemoryMiB:   fixture.QEMUMinMemoryMiB,
			QEMUStoreCacheMmap: fixture.QEMUStoreCacheMmap,
		},
	}

	if len(fixture.Secrets) > 0 {
		sources := make(map[string]specCredentialSource, len(fixture.Secrets))
		serviceAuth := map[string]specServiceAuth{}
		for id, sc := range fixture.Secrets {
			sources[id] = specCredentialSource{Env: sc.EnvVars, APIKey: sc.APIKey}
			if sc.Release != nil && sc.Release.HTTP != nil {
				serviceAuth[id] = specServiceAuth{
					HeaderName:  sc.Release.HTTP.Header,
					ValueFormat: sc.Release.HTTP.Format,
					Hosts:       sc.Release.HTTP.Hosts,
				}
			}
		}
		doc.Credentials = &specCredentials{Sources: sources}
		if len(serviceAuth) > 0 {
			doc.Network = &specNetwork{ServiceAuth: serviceAuth}
		}
	}

	for _, p := range fixture.Providers {
		doc.Providers = append(doc.Providers, specProvider{
			Name:                p.Name,
			Credentials:         p.CredentialSecrets,
			SecurestorageDirEnv: p.SecurestorageDirEnv,
		})
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal spec.yaml: %v", err)
	}
	specPath := filepath.Join(toolDir, SpecFilename)
	if err := os.WriteFile(specPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", specPath, err)
	}
	return model.Paths{ToolsDir: toolsDir}
}
