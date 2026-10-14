// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/gateway"
	"enclave/internal/model"
	"enclave/internal/protectedauth"
	"enclave/internal/util"
)

func oauthTestBackend(t *testing.T) (*Backend, backend.StoreKey, map[string][]dockercmd.InspectResponse) {
	t.Helper()
	processBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(processBin, "ps"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", processBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := New(Options{Host: model.Host{Home: t.TempDir(), UID: strconv.Itoa(os.Getuid()), GID: strconv.Itoa(os.Getgid())}})
	engines := map[string][]dockercmd.InspectResponse{}
	previous := engineContainers
	engineContainers = func(_ context.Context, engine string) ([]dockercmd.InspectResponse, error) {
		return engines[engine], nil
	}
	t.Cleanup(func() {
		engineContainers = previous
		b.authStarts.Range(func(_, release any) bool { release.(func())(); return true })
	})
	return b, backend.StoreKey{Owner: "claude", Suffix: "protected"}, engines
}

func TestProtectedAdmissionCrossEngineAndPreparingOwner(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	ctx := context.Background()
	first, err := b.PrepareAuthSession(ctx, key, "first", true, false)
	if err != nil {
		t.Fatal(err)
	}
	other := New(Options{Host: b.opts.Host, Engine: backend.NamePodman})
	second, err := other.PrepareAuthSession(ctx, key, "second", true, false)
	if err != nil {
		t.Fatalf("concurrent preparing session: %v", err)
	}
	defer other.releaseAuthStart(second)
	if err := b.validateAuthReservation(first); err != nil {
		t.Fatalf("second admission displaced preparing sibling: %v", err)
	}
	if _, err := other.PrepareAuthSession(ctx, key, "reset", true, true); err == nil {
		t.Fatal("reset admitted while siblings were preparing")
	}
	if _, err := other.PrepareAuthSession(ctx, key, "legacy", false, false); err == nil {
		t.Fatal("omitted protection reopened identity")
	}
	if err := b.ReleaseAuthSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := other.validateAuthReservation(second); err != nil {
		t.Fatalf("sibling release removed preparing session: %v", err)
	}
	if second.Incarnation == first.Incarnation {
		t.Fatal("new session reused incarnation")
	}
}

func TestProtectedAdmissionReportsReadyLogins(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	key.Owner = "pi"
	ctx := context.Background()
	first, err := b.PrepareAuthSession(ctx, key, "first", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Logins) != 0 {
		t.Fatalf("fresh identity reports logins %v", first.Logins)
	}
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err != nil {
		t.Fatal(err)
	}
	for adapter, status := range map[string]string{protectedauth.AdapterAnthropic: "uncertain", protectedauth.AdapterOpenAI: "ready"} {
		if err := os.WriteFile(filepath.Join(dir, protectedauth.GrantFile(adapter)), []byte(`{"status":"`+status+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	second, err := b.PrepareAuthSession(ctx, key, "second", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(second.Logins, []string{protectedauth.AdapterOpenAI}) {
		t.Fatalf("logins = %v, want only the ready ChatGPT grant", second.Logins)
	}
}

func TestLegacyAdmissionBlocksEnrollmentAndStalePreparationRecovers(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	ctx := context.Background()
	legacy, err := b.PrepareAuthSession(ctx, key, "legacy", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.PrepareAuthSession(ctx, key, "protected", true, false); err == nil {
		t.Fatal("enrolled an identity reserved by live legacy preparation")
	}
	b.releaseAuthStart(legacy) // process death before creating either container
	fresh, err := b.PrepareAuthSession(ctx, key, "protected", true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.releaseAuthStart(fresh)
	if err := b.validateAuthReservation(legacy); err == nil {
		t.Fatal("stale legacy reservation survived verified recovery")
	}
}

func TestProtectedReleaseRetainsDetachedOwnerAndCleansVerifiedEnd(t *testing.T) {
	b, key, engines := oauthTestBackend(t)
	ctx := context.Background()
	ref, err := b.PrepareAuthSession(ctx, key, "detached", true, false)
	if err != nil {
		t.Fatal(err)
	}
	engines[backend.NameDocker] = []dockercmd.InspectResponse{{Name: "/detached", State: &dockercmd.ContainerState{Status: "running", Running: true}}}
	if err := b.ReleaseAuthSession(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := b.validateAuthReservation(ref); err != nil {
		t.Fatalf("detached CLI exit released reservation: %v", err)
	}
	engines[backend.NameDocker] = []dockercmd.InspectResponse{{Name: "/detached", State: &dockercmd.ContainerState{Status: "exited"}}, {Name: "/" + gateway.ContainerName("detached"), State: &dockercmd.ContainerState{Status: "running", Running: true}}}
	if err := b.ReleaseAuthSession(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := b.validateAuthReservation(ref); err != nil {
		t.Fatal("gateway still serving but reservation released")
	}
	engines[backend.NameDocker] = nil
	if err := b.ReleaseAuthSession(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.authSessionDir(ref)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session runtime/view survived verified end: %v", err)
	}
	dir, _ := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err := hoststore.RequireUnprotectedAuth(b.opts.Host.Home, dir); err == nil {
		t.Fatal("session end downgraded saved identity")
	}
}

func TestProtectedStoreKeepsHistoryAndScopesCredentialsToView(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	ctx := context.Background()
	ref, err := b.PrepareAuthSession(ctx, key, "session", true, false)
	if err != nil {
		t.Fatal(err)
	}
	ordinaryKey := backend.StoreKey{Owner: "claude", ProjectHash: "project"}
	if _, err := b.PrepareStores(ctx, backend.StorePrep{AuthSession: ref, Config: &backend.ConfigStorePrep{Key: ordinaryKey}, Auth: &backend.StorePrepEntry{Key: key}}); err == nil {
		t.Fatal("protected session accepted an ordinary config store")
	}
	configKey := backend.ProtectedConfigKey(ordinaryKey)
	_, err = b.PrepareStores(ctx, backend.StorePrep{AuthSession: ref, Config: &backend.ConfigStorePrep{Key: configKey, LayoutDirs: []string{"skills"}}, Auth: &backend.StorePrepEntry{Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	persistent, err := hoststore.ResolveDir(b.opts.Host.Home, configKey, backend.StoreKindConfig)
	if err != nil {
		t.Fatal(err)
	}
	if dir, err := b.storage.storeDir(configKey, backend.StoreKindConfig); err != nil || dir != persistent {
		t.Fatalf("protected config store=%s, %v; want persistent %s", dir, err, persistent)
	}
	view, err := b.storage.storeDir(key, backend.StoreKindAuth)
	if err != nil || view != b.authViewDir(ref) {
		t.Fatalf("auth view=%s, %v", view, err)
	}
	cfg, err := b.protectedGatewayConfig(ref)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StoreDir == view || cfg.RuntimeDir == view || cfg.LockPath != hoststore.LockPath(b.opts.Host.Home, cfg.StoreDir) {
		t.Fatal("gateway mounts alias view or use a different identity lock")
	}
	req := backend.Request{AuthSession: ref, Session: backend.SessionMeta{Name: "session", Tool: "claude"}, Stores: []backend.PersistentStore{
		{Kind: backend.StoreKindConfig, Key: configKey, ContainerPath: "/home/agent/.claude"},
		{Kind: backend.StoreKindAuth, Key: key, ContainerPath: "/home/agent/.enclave-auth"},
	}, User: b.opts.Host.UID + ":" + b.opts.Host.GID}
	spec := b.dockerConfig(req)
	if len(spec.hostConfig.Mounts) != 2 || spec.hostConfig.Mounts[0].Source != persistent || spec.hostConfig.Mounts[1].Source != view {
		t.Fatalf("workload mounts=%v", spec.hostConfig.Mounts)
	}
	if err := b.validateProtectedMounts(req, spec); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"", "agent", "0:0", "101:101"} {
		changed := req
		changed.User = user
		if err := b.validateProtectedMounts(changed, spec); err == nil {
			t.Fatalf("accepted ambiguous or privileged workload user %q", user)
		}
	}
	for _, change := range []func(*backend.Request){
		func(req *backend.Request) {
			req.Stores = append(req.Stores, backend.PersistentStore{Kind: backend.StoreKindFeatureAuth})
		},
		func(req *backend.Request) {
			req.AuthSync = &backend.AuthSyncSpec{Features: []backend.FeatureAuthSync{{}}}
		},
		func(req *backend.Request) {
			req.Env = append(req.Env, backend.EnvVar{Name: model.EnvFeatureAuthMap, Value: "github-cli"})
		},
	} {
		changed := req
		change(&changed)
		if err := b.validateProtectedMounts(changed, spec); err == nil {
			t.Fatal("accepted unsupported native feature authentication")
		}
	}
	for _, source := range []string{cfg.StoreDir, filepath.Dir(cfg.StoreDir), config.HostStateRootDir(b.opts.Host.Home), cfg.RuntimeDir, cfg.LockPath} {
		changed := b.dockerConfig(req)
		changed.hostConfig.Mounts = append(changed.hostConfig.Mounts, dockercmd.Mount{Type: dockercmd.MountTypeBind, Source: source, Target: "/extra"})
		if err := b.validateProtectedMounts(req, changed); err == nil {
			t.Fatalf("accepted trusted-state mount %s", source)
		}
	}
}

func TestOrdinaryWorkloadCannotMountProtectedState(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	ctx := context.Background()
	ref, err := b.PrepareAuthSession(ctx, key, "protected", true, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	alias := filepath.Join(t.TempDir(), "identity")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	validate := func(source string) error {
		req := backend.Request{Mounts: []backend.Mount{{Type: backend.MountTypeBind, Source: source, ContainerPath: "/extra"}}}
		return b.validateProtectedMounts(req, b.dockerConfig(req))
	}
	for _, source := range []string{dir, filepath.Join(dir, "oauth-grant.json"), filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)), config.HostStateRootDir(b.opts.Host.Home), alias, hoststore.LockPath(b.opts.Host.Home, dir), hoststore.IdentityStatePath(b.opts.Host.Home, dir), b.authRuntimeDir(ref), b.authViewDir(ref)} {
		if err := validate(source); err == nil {
			t.Errorf("ordinary workload accepted protected source %s", source)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := validate(dir); err == nil {
		t.Fatal("missing grant directory erased saved protection")
	}
	other, _ := hoststore.ResolveDir(b.opts.Host.Home, backend.StoreKey{Owner: "claude", Suffix: "other"}, backend.StoreKindAuth)
	if err := validate(other); err != nil {
		t.Fatalf("unprotected identity bind was rejected: %v", err)
	}
	if err := os.WriteFile(hoststore.IdentityStatePath(b.opts.Host.Home, dir), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validate(other); err == nil {
		t.Fatal("corrupt protection metadata was treated as unprotected")
	}
}

func TestOrdinaryWorkloadAlwaysKeepsAuthControlStateHostOnly(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	dir, _ := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	for _, source := range []string{filepath.Dir(hoststore.LockPath(b.opts.Host.Home, dir)), filepath.Join(config.HostStateRootDir(b.opts.Host.Home), "oauth-sessions")} {
		req := backend.Request{Mounts: []backend.Mount{{Type: backend.MountTypeBind, Source: source, ContainerPath: "/extra"}}}
		if err := b.validateProtectedMounts(req, b.dockerConfig(req)); err == nil {
			t.Fatalf("workload accepted host-only auth control source %s before first enrollment", source)
		}
	}
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	req := backend.Request{Mounts: []backend.Mount{{Type: backend.MountTypeBind, Source: alias, ContainerPath: "/project"}, {Type: backend.MountTypeBind, Source: dir, ContainerPath: "/legacy-auth"}}}
	spec := b.dockerConfig(req)
	if err := b.validateProtectedMounts(req, spec); err != nil {
		t.Fatalf("ordinary mounts changed without protected identity: %v", err)
	}
	if spec.hostConfig.Mounts[0].Source != real {
		t.Fatal("engine would receive unchecked source alias")
	}
}

func TestProtectedEnrollmentWaitsForOrdinaryMountLaunch(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	ctx := context.Background()
	dir, _ := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	var started atomic.Bool
	engineContainers = func(_ context.Context, engine string) ([]dockercmd.InspectResponse, error) {
		if started.Load() && engine == backend.NameDocker {
			return []dockercmd.InspectResponse{{Name: "/ordinary", Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: dir}}}}, nil
		}
		return nil, nil
	}
	req := backend.Request{Session: prepareRunSession(t), Network: backend.NetworkPolicy{Mode: backend.NetworkModeUnrestricted}, Mounts: []backend.Mount{{Type: backend.MountTypeBind, Source: dir, ContainerPath: "/legacy-auth"}}}
	spec, err := b.prepareRun(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer spec.releaseAdmission()
	result := make(chan error, 1)
	go func() {
		_, err := b.PrepareAuthSession(ctx, key, "protected", true, false)
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("enrollment raced an unconfirmed workload launch: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	started.Store(true)
	spec.releaseAdmission()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("enrollment ignored newly started legacy bind")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("enrollment admission lock was not released")
	}
}

func TestProtectedEnrollmentRejectsOlderControlStateBind(t *testing.T) {
	for _, sourceKind := range []string{"locks", "sessions"} {
		t.Run(sourceKind, func(t *testing.T) {
			b, key, engines := oauthTestBackend(t)
			dir, _ := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
			source := filepath.Dir(hoststore.LockPath(b.opts.Host.Home, dir))
			if sourceKind == "sessions" {
				source = filepath.Join(config.HostStateRootDir(b.opts.Host.Home), "oauth-sessions")
			}
			engines[backend.NamePodman] = []dockercmd.InspectResponse{{Name: "/older", Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: source}}}}
			if _, err := b.PrepareAuthSession(context.Background(), key, "protected", true, false); err == nil {
				t.Fatal("enrollment accepted older session exposing host control state")
			}
		})
	}
}

func TestWorkloadAdmissionLockReleasedAfterPreparationFailure(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	dir, _ := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	req := backend.Request{Network: backend.NetworkPolicy{Mode: backend.NetworkModeUnrestricted}, Mounts: []backend.Mount{{Type: backend.MountTypeBind, Source: hoststore.LockPath(b.opts.Host.Home, dir), ContainerPath: "/extra"}}}
	if _, err := b.prepareRun(context.Background(), req); err == nil {
		t.Fatal("preparation unexpectedly accepted trusted-state bind")
	}
	release, _, err := util.TryAcquireFileLock(config.HostLockPath(b.opts.Host.Home, "auth-mount-admission.lock"))
	if err != nil || release == nil {
		t.Fatalf("preparation failure leaked admission lock: %v", err)
	}
	release()
}

func TestWorkloadAdmissionLockReleasedOnEngineOutcomes(t *testing.T) {
	for _, mode := range []string{"foreground", "detached", "failed-start"} {
		t.Run(mode, func(t *testing.T) {
			b, _, _ := oauthTestBackend(t)
			script := "case \"$1\" in\nrun) sleep 0.2 ;;\ncontainer) echo '{\"State\":{\"Running\":true}}' ;;\nesac\n"
			switch mode {
			case "detached":
				script = "if [ \"$1\" = run ]; then echo synthetic-container; fi\n"
			case "failed-start":
				script = "if [ \"$1\" = run ]; then exit 125; fi\n"
			}
			stubCLI(t, "docker", script)
			checkReleased := func() {
				t.Helper()
				release, _, err := util.TryAcquireFileLock(config.HostLockPath(b.opts.Host.Home, "auth-mount-admission.lock"))
				if err != nil || release == nil {
					t.Fatalf("workload admission lock was not released: %v", err)
				}
				release()
			}
			req := backend.Request{Session: prepareRunSession(t), Network: backend.NetworkPolicy{Mode: backend.NetworkModeUnrestricted}}
			sessionRuntimeExists = func(context.Context, string) (bool, error) { return false, nil }
			called := false
			if mode == "detached" {
				if _, err := b.Start(context.Background(), req); err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := b.Run(context.Background(), req, backend.AttachIO{OnStarted: func() {
					checkReleased()
					called = true
				}})
				if (err != nil) != (mode == "failed-start") {
					t.Fatalf("unexpected engine result: %v", err)
				}
				if called != (mode == "foreground") {
					t.Fatalf("unexpected start callback: %t", called)
				}
			}
			checkReleased()
		})
	}
}

func TestProtectedAdmissionRejectsLegacyBindOnEitherEngine(t *testing.T) {
	for _, engine := range []string{backend.NameDocker, backend.NamePodman} {
		t.Run(engine, func(t *testing.T) {
			b, key, engines := oauthTestBackend(t)
			dir, _ := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
			engines[engine] = []dockercmd.InspectResponse{{Name: "/old", State: &dockercmd.ContainerState{Status: "exited"}, Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: filepath.Dir(dir), Destination: "/old-auth"}}}}
			if _, err := b.PrepareAuthSession(context.Background(), key, "new", true, false); err == nil {
				t.Fatal("enrollment accepted restartable legacy raw bind")
			}
		})
	}
}

func TestProtectedPrepareRejectsNativeCredentialsInOverlayAndView(t *testing.T) {
	for _, where := range []string{"persistent", "overlay", "view"} {
		for _, filename := range []string{".credentials.json", "auth.json", ".claude.json", ".config.json", "config.json"} {
			t.Run(where+"/"+filename, func(t *testing.T) {
				b, key, _ := oauthTestBackend(t)
				ctx := context.Background()
				ref, err := b.PrepareAuthSession(ctx, key, "session", true, false)
				if err != nil {
					t.Fatal(err)
				}
				configKey := backend.ProtectedConfigKey(backend.StoreKey{Owner: "claude", ProjectHash: "project"})
				prep := backend.StorePrep{AuthSession: ref, Config: &backend.ConfigStorePrep{Key: configKey}}
				source, _ := hoststore.ResolveDir(b.opts.Host.Home, configKey, backend.StoreKindConfig)
				if where == "overlay" {
					source = t.TempDir()
					prep.Config.Overlay = &backend.ConfigOverlaySpec{SourceDir: source}
				}
				if where == "view" {
					source = b.authViewDir(ref)
				}
				writeFile(t, filepath.Join(source, filename), "synthetic-native-credential")
				_, err = b.PrepareStores(ctx, prep)
				if where == "view" {
					if err != nil {
						t.Fatal(err)
					}
					req := backend.Request{AuthSession: ref, User: b.opts.Host.UID + ":" + b.opts.Host.GID}
					err = b.validateProtectedMounts(req, b.dockerConfig(req))
				}
				// The protected-only store never held a real credential; it
				// keeps the session state the client wrote there before.
				if where == "persistent" {
					if err != nil {
						t.Fatalf("rejected protected session state: %v", err)
					}
				} else if err == nil {
					t.Fatal("accepted preexisting native credentials")
				}
				assertFileContent(t, filepath.Join(source, filename), "synthetic-native-credential")
			})
		}
	}
}

func TestProtectedResetAndExplicitDowngrade(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	key.Owner = "pi"
	ctx := context.Background()
	ref, err := b.PrepareAuthSession(ctx, key, "session", true, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	// Every adapter's grant, and an interrupted write, are cleared.
	for _, name := range append(protectedauth.GrantFiles(), protectedauth.InterruptedWritePrefix+"partial") {
		writeFile(t, filepath.Join(dir, name), "synthetic-grant")
	}
	if err := b.ResetAuthIdentity(ctx, key); err == nil {
		t.Fatal("reset accepted active owner")
	}
	if err := b.ReleaseAuthSession(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := b.DowngradeAuthIdentity(ctx, key); err == nil {
		t.Fatal("downgrade exposed retained grant")
	}
	if err := b.ResetAuthIdentity(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := hoststore.RequireUnprotectedAuth(b.opts.Host.Home, dir); err == nil {
		t.Fatal("reset downgraded saved protection")
	}
	if err := b.DowngradeAuthIdentity(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := hoststore.RequireUnprotectedAuth(b.opts.Host.Home, dir); err != nil {
		t.Fatal(err)
	}
}

func TestEngineInspectionFailureRetainsReservation(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	ctx := context.Background()
	ref, err := b.PrepareAuthSession(ctx, key, "session", true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []error{errors.New("engine unavailable"), &exec.Error{Name: b.Name(), Err: exec.ErrNotFound}} {
		engineContainers = func(context.Context, string) ([]dockercmd.InspectResponse, error) { return nil, failure }
		for _, action := range []func() error{
			func() error { return b.ReleaseAuthSession(ctx, ref) },
			func() error { return b.ResetAuthIdentity(ctx, key) },
			func() error { return b.DowngradeAuthIdentity(ctx, key) },
		} {
			err := action()
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), b.Name()) || !strings.Contains(err.Error(), ref.Session) || !strings.Contains(err.Error(), "restore engine access") {
				t.Fatalf("inspection failure lost recovery context: %v", err)
			}
			if err := b.validateAuthReservation(ref); err != nil {
				t.Fatalf("inspection failure lost reservation: %v", err)
			}
		}
	}
}

func TestProtectedEphemeralRemovalFinalizesReservationAndKeepsGrant(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	ctx := context.Background()
	ref, err := b.PrepareAuthSession(ctx, key, "session", true, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	writeFile(t, filepath.Join(dir, "oauth-grant.json"), "synthetic-grant")
	labels := map[string]string{model.LabelAgent: "claude", model.LabelHash: "project"}
	addAuthSessionLabels(labels, ref)
	info := dockercmd.InspectResponse{Name: "/session", State: &dockercmd.ContainerState{Status: "exited"}, Config: &dockercmd.ContainerConfig{Labels: labels}}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	inspectFile := filepath.Join(t.TempDir(), "inspect.json")
	if err := os.WriteFile(inspectFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OAUTH_INSPECT_FILE", inspectFile)
	t.Setenv("OAUTH_GATEWAY_INSPECT", ownedGatewayInspect("session", "project"))
	calls := stubCLI(t, "docker", `for name do :; done
if [ "$1" = container ]; then
  case "$name" in
  session) cat "$OAUTH_INSPECT_FILE" ;;
  session-gateway) printf '%s\n' "$OAUTH_GATEWAY_INSPECT" ;;
  esac
fi
`)
	if err := b.RemoveEphemeralSession(ctx, backend.SessionRef{Name: "session"}); err != nil {
		t.Fatal(err)
	}
	got := stubCalls(t, calls)
	if !hasCallWithPrefix(got, "rm --force --volumes session") || !hasCallWithPrefix(got, "stop --time 3 gateway-id") || !hasCallWithPrefix(got, "rm --force --volumes gateway-id") {
		t.Fatalf("incomplete lifecycle cleanup: %v", got)
	}
	for _, call := range got {
		if strings.HasPrefix(call, "run ") {
			t.Fatal("protected cleanup ran auth reconcile")
		}
	}
	if _, err := os.Stat(b.authSessionDir(ref)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session state survived: %v", err)
	}
	assertFileContent(t, filepath.Join(dir, "oauth-grant.json"), "synthetic-grant")
	if err := hoststore.RequireUnprotectedAuth(b.opts.Host.Home, dir); err == nil {
		t.Fatal("cleanup downgraded grant identity")
	}
}

func TestProtectedRemovalRetriesLeftoverPodmanGateway(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	b.opts.Engine = backend.NamePodman
	ctx := context.Background()
	ref, err := b.PrepareAuthSession(ctx, key, "enclave-claude-abc123abc123-main", true, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	writeFile(t, filepath.Join(dir, "oauth-grant.json"), "synthetic-grant")
	labels := map[string]string{model.LabelAgent: "claude", model.LabelHash: "abc123abc123"}
	addAuthSessionLabels(labels, ref)
	info := dockercmd.InspectResponse{Name: "/enclave-claude-abc123abc123-main", State: &dockercmd.ContainerState{Status: "exited"}, Config: &dockercmd.ContainerConfig{Labels: labels}}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	writeFile(t, filepath.Join(state, "inspect.json"), string(data))
	writeFile(t, filepath.Join(state, "gateway.json"), ownedGatewayInspect("enclave-claude-abc123abc123-main", "abc123abc123"))
	t.Setenv("OAUTH_TEST_STATE", state)
	t.Setenv("OAUTH_FAIL_GATEWAY_REMOVE", "1")
	calls := stubCLI(t, "podman", `for name do :; done
case "$1" in
container)
  case "$name" in
  enclave-claude-abc123abc123-main)
    if [ -e "$OAUTH_TEST_STATE/workload-removed" ]; then
      echo 'Error: No such container: enclave-claude-abc123abc123-main' >&2
      exit 1
    fi
    cat "$OAUTH_TEST_STATE/inspect.json"
    ;;
  enclave-claude-abc123abc123-main-gateway) cat "$OAUTH_TEST_STATE/gateway.json" ;;
  esac
  ;;
rm)
  case "$name" in
  enclave-claude-abc123abc123-main) touch "$OAUTH_TEST_STATE/workload-removed" ;;
  gateway-id)
    if [ ! -e "$OAUTH_TEST_STATE/workload-removed" ] || [ "$OAUTH_FAIL_GATEWAY_REMOVE" = 1 ]; then
      echo 'gateway removal failed' >&2
      exit 1
    fi
    touch "$OAUTH_TEST_STATE/gateway-removed"
    ;;
  esac
  ;;
esac
`)
	engineContainers = func(_ context.Context, engine string) ([]dockercmd.InspectResponse, error) {
		if _, err := os.Stat(filepath.Join(state, "gateway-removed")); errors.Is(err, os.ErrNotExist) && engine == backend.NamePodman {
			return []dockercmd.InspectResponse{{Name: "/enclave-claude-abc123abc123-main-gateway", State: &dockercmd.ContainerState{Status: "exited"}, Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: dir}}}}, nil
		}
		return nil, nil
	}
	if err := b.RemoveWithoutFinalize(ctx, backend.SessionRef{Name: "enclave-claude-abc123abc123-main"}); err == nil {
		t.Fatal("gateway removal failure was ignored")
	}
	if err := b.validateAuthReservation(ref); err != nil {
		t.Fatalf("gateway removal failure released reservation: %v", err)
	}
	if _, err := os.Stat(b.authSessionDir(ref)); err != nil {
		t.Fatalf("gateway removal failure removed session state: %v", err)
	}
	if unused, err := b.identityUnused(ctx, dir); err != nil || unused {
		t.Fatalf("retained gateway was treated as absent: unused=%t, error=%v", unused, err)
	}
	t.Setenv("OAUTH_FAIL_GATEWAY_REMOVE", "0")
	if err := b.RemoveWithoutFinalize(ctx, backend.SessionRef{Name: "enclave-claude-abc123abc123-main"}); err != nil {
		t.Fatalf("retry removal after workload disappeared: %v", err)
	}
	if !hasCallWithPrefix(stubCalls(t, calls), "rm --force --volumes gateway-id") {
		t.Fatal("leftover gateway was not explicitly removed")
	}
	// The preparing owner is local to this test; a separate lifecycle CLI has
	// no such live startup lock once the workload has been created.
	b.releaseAuthStart(ref)
	if err := b.ResetAuthIdentity(ctx, key); err != nil {
		t.Fatalf("removed gateway still blocked identity reset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "oauth-grant.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reset did not clear grant: %v", err)
	}
	if err := hoststore.RequireUnprotectedAuth(b.opts.Host.Home, dir); err == nil {
		t.Fatal("reset after gateway removal downgraded protection")
	}
}

func ownedGatewayInspect(session string, projectHash string) string {
	return `{"Id":"gateway-id","Config":{"Labels":{"` + model.GatewayLabelManaged + `":"true","` + model.GatewayLabelContainer + `":"` + session + `","` + model.GatewayLabelProjectHash + `":"` + projectHash + `"}}}`
}

func TestProtectedResetRemovesInterruptedWritesWithoutFollowingLinks(t *testing.T) {
	for _, feature := range []bool{false, true} {
		for _, unrelated := range []bool{false, true} {
			t.Run(fmt.Sprintf("feature=%t/unrelated=%t", feature, unrelated), func(t *testing.T) {
				b, key, _ := oauthTestBackend(t)
				ctx := context.Background()
				ref, err := b.PrepareAuthSession(ctx, key, "reset-remnants", true, false)
				if err != nil {
					t.Fatal(err)
				}
				dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
				if err != nil {
					t.Fatal(err)
				}
				reset := func() error { return b.ResetAuthIdentity(ctx, key) }
				downgrade := func() error { return b.DowngradeAuthIdentity(ctx, key) }
				names := []string{"oauth-grant.json", ".oauth-crash-after-sync"}
				if feature {
					ref, err = b.PrepareFeatureAuthSessions(ctx, ref, ref.Session, []string{"github-cli"}, true)
					if err != nil {
						t.Fatal(err)
					}
					dir = config.HostStoreFeatureAuthDir(b.opts.Host.Home, "github-cli")
					reset = func() error { return b.ResetFeatureAuthIdentity(ctx, "github-cli") }
					downgrade = func() error { return b.DowngradeFeatureAuthIdentity(ctx, "github-cli") }
					names = append(names[1:], "hosts.yml", "config.yml", "github-grant.json")
				}
				for _, name := range names {
					writeFile(t, filepath.Join(dir, name), "credential-copy")
				}
				outside := filepath.Join(t.TempDir(), "keep")
				writeFile(t, outside, "outside-secret")
				if err := os.Symlink(outside, filepath.Join(dir, ".oauth-link")); err != nil {
					t.Fatal(err)
				}
				if unrelated {
					writeFile(t, filepath.Join(dir, "unrelated"), "retain")
				}
				if err := reset(); err == nil {
					t.Fatal("reset accepted a live owner")
				}
				assertFileContent(t, filepath.Join(dir, ".oauth-crash-after-sync"), "credential-copy")
				if err := b.ReleaseAuthSession(ctx, ref); err != nil {
					t.Fatal(err)
				}
				if err := reset(); err != nil {
					t.Fatal(err)
				}
				for _, name := range append(names, ".oauth-link") {
					if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("reset retained %s: %v", name, err)
					}
				}
				assertFileContent(t, outside, "outside-secret")
				if err := hoststore.RequireUnprotectedAuth(b.opts.Host.Home, dir); err == nil {
					t.Fatal("reset downgraded protection")
				}
				if unrelated {
					assertFileContent(t, filepath.Join(dir, "unrelated"), "retain")
					if err := downgrade(); err == nil {
						t.Fatal("downgrade ignored unrelated entry")
					}
				} else if err := downgrade(); err != nil {
					t.Fatalf("reset did not permit downgrade: %v", err)
				}
			})
		}
	}
}

func TestQEMULifetimeBlocksProtectedAdmissionAndMutation(t *testing.T) {
	b, key, _ := oauthTestBackend(t)
	ctx := context.Background()
	ref := protectedGitHubTestSession(t, b, key, "before-qemu")
	if err := b.ReleaseAuthSession(ctx, ref); err != nil {
		t.Fatal(err)
	}
	lease, err := hoststore.AcquireQEMULease(b.opts.Host.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	for name, operation := range map[string]func() error{
		"tool reset":        func() error { return b.ResetAuthIdentity(ctx, key) },
		"tool downgrade":    func() error { return b.DowngradeAuthIdentity(ctx, key) },
		"feature reset":     func() error { return b.ResetFeatureAuthIdentity(ctx, "github-cli") },
		"feature downgrade": func() error { return b.DowngradeFeatureAuthIdentity(ctx, "github-cli") },
		"new identity": func() error {
			_, err := b.PrepareAuthSession(ctx, backend.StoreKey{Owner: "claude", Suffix: "new"}, "new", true, false)
			return err
		},
	} {
		if err := operation(); err == nil || !strings.Contains(err.Error(), "QEMU") {
			t.Fatalf("%s did not refuse active QEMU lifetime: %v", name, err)
		}
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.ResetAuthIdentity(ctx, key); err != nil {
		t.Fatalf("ended QEMU lease blocked recovery: %v", err)
	}
}
