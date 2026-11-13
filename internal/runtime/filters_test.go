// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/filter"
	"enclave/internal/filterhost"
	"enclave/internal/model"
	"enclave/internal/testutil"
)

const (
	testRefreshToken = "rt-very-secret-refresh-token"
	testClientSecret = "very-secret-client-secret"
)

// sessionFiltersForTest resolves the gws fixture with a stored grant, the
// way a run does.
func sessionFiltersForTest(t *testing.T, home string) []filterhost.SessionFilter {
	t.Helper()
	paths := model.Paths{UserFilterExtensionsDir: filepath.Join(t.TempDir(), "filters")}
	testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	store := filterhost.NewStore(home, "gws")
	if err := store.SaveGrant(filterhost.Grant{
		ID: "readonly", ClientID: "client-id", ClientSecret: testClientSecret, RefreshToken: testRefreshToken,
		Scopes: []string{"https://www.googleapis.com/auth/gmail.readonly", "https://www.googleapis.com/auth/drive.readonly"},
	}); err != nil {
		t.Fatal(err)
	}
	filters, err := filterhost.Resolve(filterhost.ResolveRequest{Paths: paths, Home: home, Entries: []string{"gws"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return filters
}

func filterRuntime(t *testing.T) *Runtime {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o750); err != nil {
		t.Fatal(err)
	}
	identity := "GIT_AUTHOR_NAME=A\nGIT_AUTHOR_EMAIL=a@example.com\nGIT_COMMITTER_NAME=A\nGIT_COMMITTER_EMAIL=a@example.com\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte(identity), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(model.RuntimeConfig{
		Host:    model.Host{Home: home, UID: "1000", GID: "1000"},
		Project: model.Project{Name: "project", Hash: "abc123abc123", Dir: projectDir, RealDir: projectDir},
		Profile: model.Profile{Name: "tool", Command: "tool"},
		Run:     model.RunOptions{NoHistory: true, NoMemory: true, NoCache: true, NetworkLog: model.NetworkLogCoarse},
		Auth:    model.AuthOptions{SecretsScope: model.SecretsScopeProject},
		Build:   model.BuildOptions{ImageName: "image"},
	})
	r.SetBackend(&fakeBackend{})
	r.SetFilters(sessionFiltersForTest(t, home))
	return r
}

// The session gets the placeholder, the active filter list, and a read-only
// summary. The credential only ever reaches the gateway bundle.
func TestPrepareExecutionExposesFiltersWithoutCredentials(t *testing.T) {
	r := filterRuntime(t)
	placeholder := r.filters[0].Placeholder
	ctx, err := r.prepareExecution()
	if err != nil {
		t.Fatalf("prepareExecution: %v", err)
	}
	if ctx.Cleanup != nil {
		defer ctx.Cleanup()
	}
	req := r.backendRequest(ctx, true, false)

	env := map[string]string{}
	for _, e := range req.Env {
		env[e.Name] = e.Value
	}
	if env["GOOGLE_WORKSPACE_CLI_TOKEN"] != placeholder || !strings.HasPrefix(placeholder, "ENCLAVE_SECRET_") {
		t.Errorf("GOOGLE_WORKSPACE_CLI_TOKEN = %q, want the placeholder %q", env["GOOGLE_WORKSPACE_CLI_TOKEN"], placeholder)
	}
	if env[model.EnvActiveFilters] != "gws:readonly" || env[model.EnvFilterSummaryDir] != model.FilterSummaryContainerDir {
		t.Errorf("filter env = %q, %q", env[model.EnvActiveFilters], env[model.EnvFilterSummaryDir])
	}

	mount, ok := findMountByTarget(req.Mounts, model.FilterSummaryContainerDir)
	if !ok || !mount.ReadOnly {
		t.Fatalf("summary mount = %+v, %v; want a read-only mount", mount, ok)
	}
	summaryPath := filepath.Join(mount.Source, "gws.json")
	data, err := os.ReadFile(summaryPath) // #nosec G304 -- test output.
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var summary filter.Summary
	if err := json.Unmarshal(data, &summary); err != nil || summary.Kind != filter.SummaryKind || summary.Preset != "readonly" ||
		!slices.Contains(summary.Hosts, "gmail.googleapis.com") {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
	if !strings.HasPrefix(mount.Source, config.HostStateRootDir(r.host.Home)) {
		t.Errorf("summary dir %s is outside the state root", mount.Source)
	}

	if len(req.Filters) != 1 || req.Filters[0].Name != "gws" {
		t.Fatalf("request filters = %d", len(req.Filters))
	}
	pol, err := filter.ParsePolicy(req.Filters[0].Policy)
	if err != nil || pol.Placeholder != placeholder || pol.Preset != "readonly" {
		t.Fatalf("policy = %v, %v", pol, err)
	}
	cred, err := filter.ParseCredential(req.Filters[0].Credential)
	if err != nil || cred.OAuth2 == nil || cred.OAuth2.RefreshToken != testRefreshToken || cred.OAuth2.Grant != "readonly" {
		t.Fatalf("credential = %v, %v", cred, err)
	}
	if err := pol.CheckCredential(cred); err != nil {
		t.Fatal(err)
	}

	// I1: no credential material in the tool container's env, mounts,
	// mounted files, or provenance.
	secrets := []string{testRefreshToken, testClientSecret}
	for _, e := range req.Env {
		for _, secret := range secrets {
			if strings.Contains(e.Value, secret) {
				t.Errorf("env %s carries credential material", e.Name)
			}
		}
	}
	for _, m := range req.Mounts {
		for _, secret := range secrets {
			if strings.Contains(m.Source, secret) || strings.Contains(m.ContainerPath, secret) {
				t.Errorf("mount %+v names credential material", m)
			}
		}
		if strings.HasPrefix(m.Source, config.HostFiltersStateDir(r.host.Home)) {
			t.Errorf("mount %+v exposes the filter state directory", m)
		}
	}
	for _, secret := range append(secrets, placeholder) {
		if strings.Contains(string(data), secret) {
			t.Errorf("summary carries %q", secret)
		}
	}
	session, _ := json.Marshal(req.Session)
	for _, secret := range secrets {
		if strings.Contains(string(session), secret) {
			t.Errorf("session metadata carries credential material")
		}
	}

	// The governed hosts join the allowlist.
	allowed := req.Network.Egress.AllowedDomains
	for _, host := range []string{"gmail.googleapis.com", "docs.googleapis.com"} {
		if !slices.Contains(allowed, host) {
			t.Errorf("allowlist %v lacks governed host %s", allowed, host)
		}
	}
	if !req.Network.Gateway || req.Network.Mode != backend.NetworkModeRestricted {
		t.Errorf("network = %+v, want a restricted session with a gateway", req.Network)
	}
}

func TestPrepareExecutionRefusesFiltersWithoutEnforcement(t *testing.T) {
	r := filterRuntime(t)
	r.run.AllowAllNetwork = true
	r.run.DisableGateway = true
	if _, err := r.prepareExecution(); err == nil || !strings.Contains(err.Error(), "request filters need restricted network mode") {
		t.Fatalf("prepareExecution = %v, want the enforcement error", err)
	}
}

// A session with root access could take the gateway proxy's UID and bypass
// the filters, so the runtime refuses them even if the app missed it.
func TestPrepareExecutionRefusesFiltersWithRootAccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(r *Runtime)
		want string
	}{
		{"admin", func(r *Runtime) { r.run.Admin, r.run.Shell = true, true }, "this is an admin session"},
		{"allow root", func(r *Runtime) { r.run.AllowRoot = true }, "--allow-root or ENCLAVE_ALLOW_ROOT is set"},
		{"runtime uid remap", func(r *Runtime) { r.build.RuntimeUIDRemap = true }, "--runtime-uid-remap starts it as root"},
		{"devcontainer shell as root", func(r *Runtime) {
			r.devcontainer = &model.DevcontainerConfig{RemoteUser: "root"}
			r.run.Shell = true
		}, "it runs as the devcontainer remoteUser root"},
		{"devcontainer remote user root", func(r *Runtime) {
			r.devcontainer = &model.DevcontainerConfig{RemoteUser: "0"}
			r.build.UseRemoteUser = true
		}, "it runs as the devcontainer remoteUser root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := filterRuntime(t)
			tc.edit(r)
			want := "request filters need a session without root access, but " + tc.want
			if _, err := r.prepareExecution(); err == nil || err.Error() != want {
				t.Fatalf("prepareExecution = %v, want %q", err, want)
			}
		})
	}
	// The devcontainer remoteUser root is ignored in agent mode.
	r := &Runtime{devcontainer: &model.DevcontainerConfig{RemoteUser: "root"}}
	if reason := r.rootAccess(); reason != "" {
		t.Fatalf("rootAccess = %q for an agent session", reason)
	}
}

// gatewayExecBackend lists gateways for the admin exec check and records
// whether an exec ran.
type gatewayExecBackend struct {
	*execRecordingBackend
	gateways []backend.GatewayInfo
	listErr  error
}

func (b *gatewayExecBackend) ListGateways(context.Context, backend.GatewayFilter) ([]backend.GatewayInfo, error) {
	return b.gateways, b.listErr
}
func (b *gatewayExecBackend) VerifyGatewayConfigMount(context.Context, string, string) error {
	return nil
}
func (b *gatewayExecBackend) ReloadGatewayNetwork(context.Context, string, string) error { return nil }

func TestAdminExecRefusedForFilteredSession(t *testing.T) {
	const target = "enclave-claude-abc123-review"
	newBackend := func(gateways []backend.GatewayInfo, listErr error) *gatewayExecBackend {
		return &gatewayExecBackend{
			execRecordingBackend: &execRecordingBackend{fakeBackend: &fakeBackend{sessions: []backend.Session{{Ref: backend.SessionRef{Name: target}}}}},
			gateways:             gateways,
			listErr:              listErr,
		}
	}
	filtered := []backend.GatewayInfo{
		{SessionContainer: "enclave-claude-abc123-other", Filters: []string{"other"}},
		{SessionContainer: target, Filters: []string{"gws", "tok"}},
	}
	exec := func(be backend.Backend, admin bool) error {
		r := &Runtime{run: model.RunOptions{Admin: admin, CmdArgs: []string{"true"}}, backend: be}
		return r.Exec(ExecTarget{Name: target})
	}

	be := newBackend(filtered, nil)
	want := "refusing exec --admin: session " + target + " has request filters (gws, tok), which root could bypass"
	if err := exec(be, true); err == nil || err.Error() != want {
		t.Fatalf("admin exec = %v, want %q", err, want)
	}
	if be.execRef.Name != "" {
		t.Fatalf("admin exec ran in %s", be.execRef.Name)
	}
	if err := exec(be, false); err != nil || be.execRef.Name != target {
		t.Fatalf("exec = %v into %q, want it to run", err, be.execRef.Name)
	}

	be = newBackend(nil, errors.New("engine down"))
	if err := exec(be, true); err == nil || !strings.Contains(err.Error(), "cannot tell whether session "+target+" has request filters: engine down") || be.execRef.Name != "" {
		t.Fatalf("admin exec with a failed lookup = %v, ran in %q", err, be.execRef.Name)
	}

	be = newBackend([]backend.GatewayInfo{{SessionContainer: target}, filtered[0]}, nil)
	if err := exec(be, true); err != nil || be.execRef.Name != target {
		t.Fatalf("admin exec without filters = %v into %q", err, be.execRef.Name)
	}

	// A backend that can run filters but cannot list gateways fails closed.
	plain := &execRecordingBackend{fakeBackend: &fakeBackend{sessions: []backend.Session{{Ref: backend.SessionRef{Name: target}}}}}
	if err := exec(plain, true); err == nil || !strings.Contains(err.Error(), "cannot tell") {
		t.Fatalf("admin exec without a gateway lookup = %v", err)
	}
	plain.noRequestFilters = true
	if err := exec(plain, true); err != nil {
		t.Fatalf("admin exec on a backend without filters = %v", err)
	}
}

func TestPrepareExecutionRefusesFiltersWithoutBackendCapability(t *testing.T) {
	r := filterRuntime(t)
	r.SetBackend(&fakeBackend{noRequestFilters: true})
	if _, err := r.prepareExecution(); err == nil || !strings.Contains(err.Error(), "the fake backend cannot enforce them") {
		t.Fatalf("prepareExecution = %v, want the capability error", err)
	}
}

// A release rule selected at session start must not cover a governed host.
func TestFilterReleaseHostCollision(t *testing.T) {
	r := &Runtime{filters: sessionFiltersForTest(t, t.TempDir())}
	err := r.checkFilterReleaseHosts(SecretMapping{Entries: []model.SecretReleaseEntry{
		{SecretID: "other", Hosts: []string{"googleapis.com"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "credential other") {
		t.Fatalf("checkFilterReleaseHosts = %v", err)
	}
	if err := r.checkFilterReleaseHosts(SecretMapping{Entries: []model.SecretReleaseEntry{
		{SecretID: "exact", Hosts: []string{"googleapis.com"}, ExactHosts: true},
		{SecretID: "elsewhere", Hosts: []string{"api.github.com"}},
	}}); err != nil {
		t.Fatalf("checkFilterReleaseHosts(no overlap) = %v", err)
	}
}

func TestWithFilterEnvWinsOverEarlierEntries(t *testing.T) {
	r := &Runtime{filters: sessionFiltersForTest(t, t.TempDir())}
	projectEnv := []string{"GOOGLE_WORKSPACE_CLI_TOKEN=from-dotenv"}
	env := r.withFilterEnv(append([]string{"A=1"}, projectEnv...), projectEnv)
	var token []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "GOOGLE_WORKSPACE_CLI_TOKEN=") {
			token = append(token, entry)
		}
	}
	if len(token) != 1 || token[0] != "GOOGLE_WORKSPACE_CLI_TOKEN="+r.filters[0].Placeholder || !slices.Contains(env, "A=1") {
		t.Fatalf("env = %v", env)
	}
}
