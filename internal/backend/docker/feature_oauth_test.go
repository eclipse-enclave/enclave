// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/gateway"
	"enclave/internal/model"
)

func protectedGitHubTestSession(t *testing.T, b *Backend, key backend.StoreKey, session string) *backend.AuthSession {
	t.Helper()
	ref, err := b.PrepareAuthSession(context.Background(), key, session, true, false)
	if err != nil {
		t.Fatal(err)
	}
	ref, err = b.PrepareFeatureAuthSessions(context.Background(), ref, session, []string{"github-cli"}, true)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestProtectedFeatureEnrollsNativeStoreWithoutWorkloadCopy(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	dir := config.HostStoreFeatureAuthDir(b.opts.Host.Home, "github-cli")
	writeFile(t, filepath.Join(dir, "hosts.yml"), "synthetic-native-github-token")
	writeFile(t, filepath.Join(dir, "config.yml"), "git_protocol: https")
	ref := protectedGitHubTestSession(t, b, key, "github-session")
	assertFileContent(t, filepath.Join(dir, "hosts.yml"), "synthetic-native-github-token")
	for _, file := range []string{"hosts.yml", "config.yml"} {
		if _, err := os.Stat(filepath.Join(b.featureViewDir(ref, "github-cli"), file)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("host projected real native feature file: %v", err)
		}
	}
	if _, err := hoststore.ResolveLegacyDir(b.opts.Host.Home, backend.StoreKey{Owner: "github-cli"}, backend.StoreKindFeatureAuth); err == nil {
		t.Fatal("saved feature protection allowed legacy store access")
	}
	if err := b.ReleaseAuthSession(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	next := protectedGitHubTestSession(t, b, key, "next-session")
	if next.Incarnation == ref.Incarnation || b.featureViewDir(next, "github-cli") == b.featureViewDir(ref, "github-cli") {
		t.Fatal("new session reused feature handle view")
	}
	if _, err := os.Stat(b.authSessionDir(ref)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ended feature session retained its private session tree")
	}
}

func TestProtectedFeatureSingleOwnerAcrossToolsAndEngines(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	first := protectedGitHubTestSession(t, b, key, "first")
	other := New(Options{Host: b.opts.Host, Engine: backend.NamePodman})
	key.Suffix = "different-claude-identity"
	second, err := other.PrepareAuthSession(context.Background(), key, "second", true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.ReleaseAuthSession(context.Background(), second) }()
	if _, err := other.PrepareFeatureAuthSessions(context.Background(), second, "second", []string{"github-cli"}, true); err == nil {
		t.Fatal("different tool identity/engine shared an active GitHub identity")
	}
	legacy, err := other.PrepareFeatureAuthSessions(context.Background(), nil, "ordinary-tool", []string{"github-cli"}, false)
	if legacy != nil {
		defer func() { _ = other.ReleaseAuthSession(context.Background(), legacy) }()
	}
	if err == nil {
		t.Fatal("ordinary feature-only session downgraded saved GitHub protection")
	}
	if err := b.validateAuthReservation(first); err != nil {
		t.Fatalf("failed admission modified active owner: %v", err)
	}
}

func TestLegacyFeatureOnlyPreparationBlocksProtectedEnrollment(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	legacy, err := b.PrepareFeatureAuthSessions(context.Background(), nil, "legacy", []string{"github-cli"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Key.Owner != "" {
		t.Fatal("feature-only session invented a tool identity")
	}
	ref, err := b.PrepareAuthSession(context.Background(), key, "protected", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.PrepareFeatureAuthSessions(context.Background(), ref, "protected", []string{"github-cli"}, true); err == nil {
		t.Fatal("feature enrollment ignored still-preparing legacy owner")
	}
	if err := b.ReleaseAuthSession(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if err := b.ReleaseAuthSession(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	protectedGitHubTestSession(t, b, key, "retry")
}

func TestProtectedFeatureRejectsExistingRawBindOnEitherEngine(t *testing.T) {
	for _, engine := range []string{backend.NameDocker, backend.NamePodman} {
		t.Run(engine, func(t *testing.T) {
			b, key, engines := oauthTestBackend(t)
			dir := config.HostStoreFeatureAuthDir(b.opts.Host.Home, "github-cli")
			engines[engine] = []dockercmd.InspectResponse{{Name: "/old-tool", State: &dockercmd.ContainerState{Status: "exited"}, Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: dir}}}}
			ref, err := b.PrepareAuthSession(context.Background(), key, "session", true, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.PrepareFeatureAuthSessions(context.Background(), ref, "session", []string{"github-cli"}, true); err == nil {
				t.Fatal("enrolled feature still bound into another container")
			}
		})
	}
}

func TestProtectedFeatureSessionRecoveryRetainsLiveGateway(t *testing.T) {
	b, key, engines := oauthTestBackend(t)
	ref := protectedGitHubTestSession(t, b, key, "session")
	labels := map[string]string{}
	addAuthSessionLabels(labels, ref)
	recovered := authSessionFromInspect(dockercmd.InspectResponse{Name: "/session", Config: &dockercmd.ContainerConfig{Labels: labels}}, b.Name())
	engines[b.Name()] = []dockercmd.InspectResponse{{Name: "/" + gateway.ContainerName("session"), State: &dockercmd.ContainerState{Status: "running"}}}
	if err := b.ReleaseAuthSession(context.Background(), recovered); err != nil {
		t.Fatal(err)
	}
	if len(recovered.Features) != 1 || recovered.Features[0] != "github-cli" {
		t.Fatal("lifecycle did not recover feature admission from host record")
	}
	if err := b.validateAuthReservation(ref); err != nil {
		t.Fatalf("live gateway released feature reservation: %v", err)
	}
	engines[b.Name()] = nil
	if err := b.ReleaseAuthSession(context.Background(), recovered); err != nil {
		t.Fatal(err)
	}
	if err := b.validateAuthReservation(ref); err == nil {
		t.Fatal("ended session retained reservation")
	}
}

func TestProtectedFeatureMountsOnlyAdmittedNativeView(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	ref := protectedGitHubTestSession(t, b, key, "session")
	configKey := backend.StoreKey{Owner: "claude", ProjectHash: "project"}
	featureKey := backend.StoreKey{Owner: "github-cli"}
	if _, err := b.PrepareStores(context.Background(), backend.StorePrep{AuthSession: ref, Config: &backend.ConfigStorePrep{Key: configKey}, Features: []backend.StorePrepEntry{{Key: featureKey}}}); err != nil {
		t.Fatal(err)
	}
	req := backend.Request{
		AuthSession: ref, User: b.opts.Host.UID + ":" + b.opts.Host.GID,
		Stores: []backend.PersistentStore{{Kind: backend.StoreKindConfig, Key: configKey, ContainerPath: "/home/agent/.claude"}, {Kind: backend.StoreKindFeatureAuth, Key: featureKey, ContainerPath: "/home/agent/.config/gh"}},
		Env:    []backend.EnvVar{{Name: "GH_CONFIG_DIR", Value: "/home/agent/.config/gh"}, {Name: "GH_HOST", Value: "github.com"}, {Name: model.EnvProtectedGitHub, Value: "1"}},
	}
	spec := b.dockerConfig(req)
	if err := b.validateProtectedMounts(req, spec); err != nil {
		t.Fatal(err)
	}
	if spec.hostConfig.Mounts[1].Source != b.featureViewDir(ref, "github-cli") {
		t.Fatal("workload feature mount did not resolve to handle-only view")
	}
	cfg, err := b.protectedGatewayConfig(ref)
	if err != nil || len(cfg.Features) != 1 {
		t.Fatalf("gateway feature configuration missing: %v", err)
	}
	if cfg.Features[0].StoreDir == spec.hostConfig.Mounts[1].Source || cfg.Features[0].LockPath != hoststore.LockPath(b.opts.Host.Home, cfg.Features[0].StoreDir) {
		t.Fatal("gateway store aliases workload view or uses wrong lock")
	}
	for _, target := range []string{"/home/agent/.config", "/home/agent/.config/gh/hosts.yml"} {
		changed := b.dockerConfig(req)
		changed.hostConfig.Mounts = append(changed.hostConfig.Mounts, dockercmd.Mount{Type: dockercmd.MountTypeBind, Source: t.TempDir(), Target: target})
		if err := b.validateProtectedMounts(req, changed); err == nil {
			t.Fatalf("accepted GitHub native destination collision %s", target)
		}
	}
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_CONFIG_DIR", "GH_HOST", model.EnvProtectedGitHub} {
		changed := req
		changed.Env = append(append([]backend.EnvVar(nil), req.Env...), backend.EnvVar{Name: key, Value: "override"})
		if err := b.validateProtectedMounts(changed, b.dockerConfig(changed)); err == nil {
			t.Fatalf("accepted protected GitHub environment override %s", key)
		}
	}
}

func TestProtectedFeatureOrdinaryBindGuardAndExplicitReset(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	dir := config.HostStoreFeatureAuthDir(b.opts.Host.Home, "github-cli")
	writeFile(t, filepath.Join(dir, "hosts.yml"), "synthetic-original")
	ref := protectedGitHubTestSession(t, b, key, "session")
	for _, source := range []string{dir, filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)), filepath.Join(dir, "hosts.yml")} {
		req := backend.Request{Mounts: []backend.Mount{{Type: backend.MountTypeBind, Source: source, ContainerPath: "/extra"}}}
		if err := b.validateProtectedMounts(req, b.dockerConfig(req)); err == nil {
			t.Fatalf("ordinary request exposed saved protected feature state %s", source)
		}
	}
	if err := b.ResetFeatureAuthIdentity(context.Background(), "github-cli"); err == nil {
		t.Fatal("reset ignored active feature owner")
	}
	if err := b.ReleaseAuthSession(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "config.yml"), "preferences")
	writeFile(t, filepath.Join(dir, "github-grant.json"), "synthetic-grant")
	if err := b.DowngradeFeatureAuthIdentity(context.Background(), "github-cli"); err == nil {
		t.Fatal("downgrade exposed retained native/grant tokens")
	}
	if err := b.ResetFeatureAuthIdentity(context.Background(), "github-cli"); err != nil {
		t.Fatal(err)
	}
	if err := hoststore.RequireUnprotectedAuth(b.opts.Host.Home, dir); err == nil {
		t.Fatal("feature reset downgraded saved protection")
	}
	for _, name := range []string{"hosts.yml", "config.yml", "github-grant.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reset retained import/grant file %s: %v", name, err)
		}
	}
	if err := b.DowngradeFeatureAuthIdentity(context.Background(), "github-cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := hoststore.ResolveLegacyDir(b.opts.Host.Home, backend.StoreKey{Owner: "github-cli"}, backend.StoreKindFeatureAuth); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedFeatureRejectsUntrustedSessionRecoveryRecord(t *testing.T) {
	for _, mutation := range []string{"malformed", "public-file", "public-parent", "symlink"} {
		t.Run(mutation, func(t *testing.T) {
			b, key, _ := oauthTestBackend(t)
			ref := protectedGitHubTestSession(t, b, key, "session")
			record := filepath.Join(b.authSessionDir(ref), "session.json")
			switch mutation {
			case "malformed":
				writeFile(t, record, "invalid")
			case "public-file":
				if err := os.Chmod(record, 0o644); err != nil {
					t.Fatal(err)
				}
			case "public-parent":
				if err := os.Chmod(b.authSessionDir(ref), 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(record); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), record); err != nil {
					t.Fatal(err)
				}
			}
			labels := map[string]string{}
			addAuthSessionLabels(labels, ref)
			recovered := authSessionFromInspect(dockercmd.InspectResponse{Name: "/session", Config: &dockercmd.ContainerConfig{Labels: labels}}, b.Name())
			if err := b.ReleaseAuthSession(context.Background(), recovered); err == nil {
				t.Fatal("lifecycle trusted invalid feature session record")
			}
			dir := config.HostStoreFeatureAuthDir(b.opts.Host.Home, "github-cli")
			if err := hoststore.RequireIdentityReservation(b.opts.Host.Home, dir, authReservation(ref)); err != nil {
				t.Fatalf("invalid record released feature identity: %v", err)
			}
		})
	}
}
