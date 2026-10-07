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
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	dockercmd "enclave/internal/docker"
	"enclave/internal/gateway"
	"enclave/internal/gateway/oauthbroker"
	"enclave/internal/model"
)

// Use the real broker to attest the inspected mount fixture, not workload labels.
func runningProtectedGateway(t *testing.T, b *Backend, ref *backend.AuthSession) dockercmd.InspectResponse {
	t.Helper()
	cfg, err := b.protectedGatewayConfig(ref)
	if err != nil {
		t.Fatal(err)
	}
	brokerConfig := oauthbroker.Config{StoreDir: cfg.StoreDir, RuntimeDir: cfg.RuntimeDir, LockPath: cfg.LockPath, Identity: cfg.Identity, Session: ref.Session, Incarnation: ref.Incarnation}
	info := dockercmd.InspectResponse{
		Name:   "/" + gateway.ContainerName(ref.Session),
		State:  &dockercmd.ContainerState{Status: "running", Running: true},
		Config: &dockercmd.ContainerConfig{Env: []string{model.EnvOAuthBroker + "=claude", model.EnvOAuthIdentity + "=" + cfg.Identity, model.EnvOAuthIncarnation + "=" + ref.Incarnation}},
		Mounts: []dockercmd.MountPoint{
			{Type: dockercmd.MountTypeBind, Source: cfg.StoreDir, Destination: model.GatewayOAuthGrantPath},
			{Type: dockercmd.MountTypeBind, Source: cfg.LockPath, Destination: model.GatewayOAuthLockPath},
			{Type: dockercmd.MountTypeBind, Source: cfg.RuntimeDir, Destination: model.GatewayOAuthRuntimePath},
			{Type: dockercmd.MountTypeBind, Source: cfg.ViewDir, Destination: model.GatewayOAuthViewPath},
		},
	}
	for _, feature := range cfg.Features {
		brokerConfig.GitHub = &oauthbroker.GitHubConfig{StoreDir: feature.StoreDir, RuntimeDir: feature.RuntimeDir, ViewDir: feature.ViewDir, LockPath: feature.LockPath, Identity: feature.Identity, Session: ref.Session, Incarnation: ref.Incarnation}
		info.Config.Env = append(info.Config.Env, model.EnvOAuthGitHub+"=1", model.EnvOAuthGitHubIdentity+"="+feature.Identity)
		info.Mounts = append(info.Mounts,
			dockercmd.MountPoint{Type: dockercmd.MountTypeBind, Source: feature.StoreDir, Destination: model.GatewayOAuthGitHubGrantPath},
			dockercmd.MountPoint{Type: dockercmd.MountTypeBind, Source: feature.LockPath, Destination: model.GatewayOAuthGitHubLockPath},
			dockercmd.MountPoint{Type: dockercmd.MountTypeBind, Source: feature.RuntimeDir, Destination: model.GatewayOAuthGitHubRuntimePath},
			dockercmd.MountPoint{Type: dockercmd.MountTypeBind, Source: feature.ViewDir, Destination: model.GatewayOAuthGitHubViewPath})
	}
	broker, err := oauthbroker.Open(brokerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.InitializeView(cfg.ViewDir); err != nil {
		t.Fatal(err)
	}
	if err := broker.MarkReady(); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestProtectedConcurrentRunningSessionsAndIndependentTeardown(t *testing.T) {
	for _, engine := range []string{backend.NameDocker, backend.NamePodman} {
		t.Run(engine, func(t *testing.T) {
			b, key, engines := oauthTestBackend(t)
			ctx := context.Background()
			first := protectedGitHubTestSession(t, b, key, "first-project")
			engines[b.Name()] = []dockercmd.InspectResponse{runningProtectedGateway(t, b, first)}
			b.releaseAuthStart(first)
			other := New(Options{Host: b.opts.Host, Engine: engine})
			second := protectedGitHubTestSession(t, other, key, "second-project")
			defer other.releaseAuthStart(second)
			engines[engine] = append(engines[engine], runningProtectedGateway(t, other, second))
			other.releaseAuthStart(second)

			for _, ref := range []*backend.AuthSession{first, second} {
				if err := b.validateAuthReservation(ref); err != nil {
					t.Fatalf("concurrent reservation: %v", err)
				}
			}
			if b.authViewDir(first) == other.authViewDir(second) {
				t.Fatal("sessions share native views")
			}
			for _, mutate := range []func() error{
				func() error { return b.ResetAuthIdentity(ctx, key) },
				func() error { return b.DowngradeAuthIdentity(ctx, key) },
				func() error { return b.ResetFeatureAuthIdentity(ctx, "github-cli") },
				func() error { return b.DowngradeFeatureAuthIdentity(ctx, "github-cli") },
			} {
				if err := mutate(); err == nil {
					t.Fatal("mutated a shared live identity")
				}
			}
			if err := b.ReleaseAuthSession(ctx, first); err != nil {
				t.Fatal(err)
			}
			if err := b.validateAuthReservation(first); err != nil {
				t.Fatalf("detached exit released live owner: %v", err)
			}

			engines[b.Name()] = engines[b.Name()][1:]
			if err := b.ReleaseAuthSession(ctx, first); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(b.authSessionDir(first)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ended session survived: %v", err)
			}
			if err := other.validateAuthReservation(second); err != nil {
				t.Fatalf("sibling teardown lost live reservations: %v", err)
			}
			if _, err := os.Stat(other.featureViewDir(second, "github-cli")); err != nil {
				t.Fatalf("sibling teardown removed live feature view: %v", err)
			}
			if err := b.ResetAuthIdentity(ctx, key); err == nil {
				t.Fatal("reset ignored remaining session")
			}
			if err := b.ResetFeatureAuthIdentity(ctx, "github-cli"); err == nil {
				t.Fatal("feature reset ignored remaining session")
			}
			third := protectedGitHubTestSession(t, b, key, "third-project")
			if err := b.ReleaseAuthSession(ctx, third); err != nil {
				t.Fatal(err)
			}
			engines[engine] = nil
			if err := other.ReleaseAuthSession(ctx, second); err != nil {
				t.Fatal(err)
			}
			if err := b.ResetAuthIdentity(ctx, key); err != nil {
				t.Fatal(err)
			}
			if err := b.ResetFeatureAuthIdentity(ctx, "github-cli"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProtectedSharingRejectsUnverifiedGatewayAndOverlappingMounts(t *testing.T) {
	for _, feature := range []bool{false, true} {
		name := "claude"
		if feature {
			name = "github"
		}
		t.Run(name, func(t *testing.T) {
			for _, scenario := range []string{"raw", "parent", "alias", "lock-parent", "metadata", "wrong-target", "wrong-runtime", "wrong-incarnation", "wrong-engine", "wrong-name", "duplicate-env", "old-broker", "missing-readiness", "inspection-failure"} {
				t.Run(scenario, func(t *testing.T) {
					b, key, engines := oauthTestBackend(t)
					first := protectedGitHubTestSession(t, b, key, "first")
					info := runningProtectedGateway(t, b, first)
					index := 0
					if feature {
						index = 4
					}
					dir := info.Mounts[index].Source
					engine := b.Name()
					switch scenario {
					case "raw":
						engines[engine] = append(engines[engine], dockercmd.InspectResponse{Name: "/raw", Mounts: []dockercmd.MountPoint{info.Mounts[index]}})
					case "parent", "alias", "lock-parent", "metadata":
						source := filepath.Dir(dir)
						switch scenario {
						case "alias":
							source = filepath.Join(t.TempDir(), "alias")
							if err := os.Symlink(dir, source); err != nil {
								t.Fatal(err)
							}
						case "lock-parent":
							source = filepath.Dir(hoststore.LockPath(b.opts.Host.Home, dir))
						case "metadata":
							source = hoststore.IdentityStatePath(b.opts.Host.Home, dir)
						}
						info.Mounts = append(info.Mounts, dockercmd.MountPoint{Type: dockercmd.MountTypeBind, Source: source, Destination: "/extra"})
					case "wrong-target":
						info.Mounts[index].Destination = "/extra"
					case "wrong-runtime":
						info.Mounts[index+2].Source = t.TempDir()
					case "wrong-incarnation":
						info.Config.Env[2] = model.EnvOAuthIncarnation + "=other"
					case "wrong-engine":
						engine = backend.NamePodman
					case "wrong-name":
						info.Name = "/unreserved-gateway"
					case "duplicate-env":
						info.Config.Env = append(info.Config.Env, info.Config.Env[0])
					case "old-broker", "missing-readiness":
						ready := filepath.Join(b.authRuntimeDir(first), "oauth-ready.json")
						data, err := os.ReadFile(ready)
						if err != nil {
							t.Fatal(err)
						}
						if scenario == "old-broker" {
							if err := os.WriteFile(ready, []byte(strings.Replace(string(data), `"schema":2`, `"schema":1`, 1)), 0o600); err != nil {
								t.Fatal(err)
							}
						} else if err := os.Remove(ready); err != nil {
							t.Fatal(err)
						}
					}
					engines[engine] = append(engines[engine], info)
					if scenario == "inspection-failure" {
						engineContainers = func(context.Context, string) ([]dockercmd.InspectResponse, error) {
							return nil, errors.New("engine unavailable")
						}
					}
					ctx := context.Background()
					if feature {
						// Different tool identity must not bypass shared feature admission.
						key.Suffix = "different"
						second, err := b.PrepareAuthSession(ctx, key, "second", true, false)
						if err != nil {
							if scenario == "lock-parent" || scenario == "inspection-failure" {
								return
							}
							t.Fatal(err)
						}
						if _, err := b.PrepareFeatureAuthSessions(ctx, second, "second", []string{"github-cli"}, true); err == nil {
							t.Fatal("shared feature admission accepted conflicting exposure")
						}
					} else if _, err := b.PrepareAuthSession(ctx, key, "second", true, false); err == nil {
						t.Fatal("shared tool admission accepted conflicting exposure")
					}
					if err := b.validateAuthReservation(first); err != nil {
						t.Fatalf("failed admission damaged sibling reservation: %v", err)
					}
				})
			}
		})
	}
}
