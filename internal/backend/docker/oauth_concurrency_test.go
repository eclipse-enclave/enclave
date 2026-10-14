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
	"time"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	dockercmd "enclave/internal/docker"
	"enclave/internal/gateway"
	"enclave/internal/gateway/oauthbroker"
	"enclave/internal/model"
	"enclave/internal/util"
)

// Use the real broker to attest the inspected mount fixture, not workload labels.
func runningProtectedGateway(t *testing.T, b *Backend, ref *backend.AuthSession) dockercmd.InspectResponse {
	t.Helper()
	cfg, err := b.protectedGatewayConfig(ref)
	if err != nil {
		t.Fatal(err)
	}
	brokerConfig := oauthbroker.Config{Tool: "claude", StoreDir: cfg.StoreDir, RuntimeDir: cfg.RuntimeDir, LockPath: cfg.LockPath, Identity: cfg.Identity, Session: ref.Session, Incarnation: ref.Incarnation}
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
							if err := os.WriteFile(ready, []byte(strings.Replace(string(data), `"schema":3`, `"schema":2`, 1)), 0o600); err != nil {
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

func TestProtectedAdmissionInspectionDoesNotStallSiblingBroker(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	first, err := b.PrepareAuthSession(context.Background(), key, "first", true, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	unblock := make(chan struct{})
	done := make(chan error, 1)
	engineContainers = func(context.Context, string) ([]dockercmd.InspectResponse, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-unblock
		return nil, nil
	}
	go func() {
		_, err := b.PrepareAuthSession(context.Background(), key, "second", true, false)
		done <- err
	}()
	defer func() {
		close(unblock)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("inspection not reached")
	}
	release, err := util.TryAcquireFileLock(hoststore.LockPath(b.opts.Host.Home, dir))
	if err != nil {
		t.Fatal(err)
	}
	if release == nil {
		t.Fatal("engine inspection stalled sibling credential access")
	}
	release()
	if err := b.validateAuthReservation(first); err != nil {
		t.Fatal(err)
	}
}

func TestAuthInspectionReusesOnlyAdmissionScopedEngineSnapshots(t *testing.T) {
	b, key, engines := oauthTestBackend(t)
	ctx := context.Background()
	for _, name := range []string{"first", "second"} {
		ref, err := b.PrepareAuthSession(ctx, key, name, true, false)
		if err != nil {
			t.Fatal(err)
		}
		engines[b.Name()] = append(engines[b.Name()], runningProtectedGateway(t, b, ref))
		b.releaseAuthStart(ref)
	}
	calls := map[string]int{}
	engineContainers = func(_ context.Context, engine string) ([]dockercmd.InspectResponse, error) {
		calls[engine]++
		return engines[engine], nil
	}
	third, err := b.PrepareAuthSession(ctx, key, "third", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if calls[backend.NameDocker] != 1 || calls[backend.NamePodman] != 1 {
		t.Fatalf("inspection repeated per owner: %v", calls)
	}
	clear(calls)
	if err := b.ReleaseAuthSession(ctx, third); err != nil {
		t.Fatal(err)
	}
	if calls[b.Name()] != 1 {
		t.Fatalf("release reused an old snapshot or inspected twice: %v", calls)
	}
	// A new admission must see newly introduced conflicting mounts.
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err != nil {
		t.Fatal(err)
	}
	engines[b.Name()] = append(engines[b.Name()], dockercmd.InspectResponse{
		Name:   "/foreign",
		Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: dir}},
	})
	if _, err := b.PrepareAuthSession(ctx, key, "fourth", true, false); err == nil {
		t.Fatal("cached prior admission hid a conflicting mount")
	}
}

func TestAuthEngineInspectionHandlesVanishedContainers(t *testing.T) {
	for _, mode := range []string{"vanished", "empty", "daemon-error", "churn", "duplicate", "incomplete", "malformed", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "engine")
			t.Setenv("AUTH_ENGINE_COUNT", filepath.Join(root, "count"))
			t.Setenv("AUTH_ENGINE_MODE", mode)
			script := `#!/bin/sh
count=0
if [ -f "$AUTH_ENGINE_COUNT" ]; then read -r count < "$AUTH_ENGINE_COUNT"; fi
case "$1" in
ps)
  count=$((count + 1))
  printf '%s' "$count" > "$AUTH_ENGINE_COUNT"
  case "$AUTH_ENGINE_MODE" in
    churn) printf 'gone-%s\n' "$count";;
    empty) if [ "$count" -eq 1 ]; then printf 'gone\n'; fi;;
    vanished) printf 'survivor\n'; if [ "$count" -eq 1 ]; then printf 'gone\n'; fi;;
    *) printf 'survivor\ngone\n';;
  esac;;
container)
  case "$AUTH_ENGINE_MODE" in
    vanished)
      if [ "$count" -eq 1 ]; then printf 'Error: No such object: gone\n' >&2; exit 1; fi
      printf '[{"Id":"survivor","Name":"/live","Config":{},"State":{"Status":"running"}}]';;
    malformed) printf 'not-json';;
    incomplete) printf '[]';;
    duplicate) printf '[{"Id":"survivor","Name":"/live","Config":{},"State":{"Status":"running"}},{"Id":"survivor","Name":"/live","Config":{},"State":{"Status":"running"}}]';;
    trailing) printf '[] {}';;
    *) printf 'inspection failed\n' >&2; exit 1;;
  esac;;
*) exit 1;;
esac
`
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			containers, err := inspectAuthEngineContainers(context.Background(), binary)
			if mode == "vanished" {
				if err != nil || len(containers) != 1 || containers[0].ID != "survivor" {
					t.Fatalf("vanished ID prevented complete survivor inspection: %+v, %v", containers, err)
				}
			} else if mode == "empty" {
				if err != nil || len(containers) != 0 {
					t.Fatalf("verified disappearance failed: %+v, %v", containers, err)
				}
			} else if err == nil {
				t.Fatal("unsafe or unbounded inspection succeeded")
			}
			count, readErr := os.ReadFile(filepath.Join(root, "count"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "churn" && string(count) != "4" {
				t.Fatalf("churn retries were not bounded: %s", count)
			}
			if mode == "daemon-error" && string(count) != "2" {
				t.Fatalf("inspection retried without a verified vanished ID: %s", count)
			}
		})
	}
}

func TestInaccessibleBindAliasStillFailsClosedWithContext(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("requires an unprivileged user")
	}
	b, key, engines := oauthTestBackend(t)
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(t.TempDir(), "hidden")
	if err := os.Mkdir(hidden, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(hidden, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if hoststore.PathsOverlap(alias, dir) {
		t.Fatal("fixture must appear lexically disjoint")
	}
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(hidden, 0o700) }()
	engines[b.Name()] = []dockercmd.InspectResponse{{
		Name: "/foreign", Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: alias}},
	}}
	_, err = b.PrepareAuthSession(context.Background(), key, "session", true, false)
	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), b.Name()) || !strings.Contains(err.Error(), "/foreign") || !strings.Contains(err.Error(), alias) {
		t.Fatalf("unresolved alias accepted or missing diagnostic context: %v", err)
	}
	if err := os.Chmod(hidden, 0o700); err != nil {
		t.Fatal(err)
	}
	if resolved, err := canonicalAuthPath(alias); err != nil || resolved != dir {
		t.Fatalf("lexically disjoint alias must resolve into the protected identity: %q, %v", resolved, err)
	}
}
