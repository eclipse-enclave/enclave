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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/backend/hoststore"
	backendqemu "enclave/internal/backend/qemu"
	"enclave/internal/gateway/exchange"
	"enclave/internal/model"
	"enclave/internal/placeholderstore"
)

type resetBackend struct {
	fakeBackend
	actual backend.Backend
}

func (b *resetBackend) Name() string { return b.actual.Name() }
func (b *resetBackend) PrepareStores(ctx context.Context, prep backend.StorePrep) (backend.StoreState, error) {
	b.prepared = append(b.prepared, prep)
	return b.actual.PrepareStores(ctx, prep)
}

func TestNativeResetPrecedesRefusedAdmissionOnBothBackends(t *testing.T) {
	for _, engine := range []string{"docker", "qemu"} {
		t.Run(engine, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			r := bearerTokenRuntime(t, "claude")
			r.containerHome, r.handler = model.ContainerHome, noopRuntimeHandler{}
			r.run.Persist, r.run.Backend = true, engine
			r.auth.AuthScope, r.auth.ResetAuth, r.auth.AuthName = model.AuthScopeShared, true, "selected"
			var actual backend.Backend = backenddocker.New(backenddocker.Options{Host: r.host})
			if engine == "qemu" {
				actual = backendqemu.New(backendqemu.Options{Host: r.host})
			}
			be := &resetBackend{actual: actual, fakeBackend: fakeBackend{storage: actual.Storage()}}
			r.backend = be
			key := backend.StoreKey{Owner: r.profile.Name, Suffix: r.auth.AuthName}
			configKey := backend.StoreKey{Owner: r.profile.Name, ProjectHash: r.project.Hash}
			ctx := context.Background()
			for _, ref := range []backend.StoreRef{{Key: key, Kind: backend.StoreKindAuth}, {Key: configKey, Kind: backend.StoreKindConfig}} {
				if err := actual.Storage().Ensure(ctx, ref.Key, ref.Kind, ""); err != nil {
					t.Fatal(err)
				}
			}
			otherKey := backend.StoreKey{Owner: r.profile.Name, Suffix: "other"}
			if err := actual.Storage().Ensure(ctx, otherKey, backend.StoreKindAuth, ""); err != nil {
				t.Fatal(err)
			}
			otherFile := filepath.Join(hoststore.Dir(r.host.Home, otherKey, backend.StoreKindAuth), ".credentials.json")
			writeFile(t, otherFile, `{"claudeAiOauth":{"accessToken":"other-identity-synthetic"}}`)
			real := hoststore.Dir(r.host.Home, key, backend.StoreKindAuth)
			configDir := hoststore.Dir(r.host.Home, configKey, backend.StoreKindConfig)
			writeFile(t, filepath.Join(real, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"reset-access-synthetic","refreshToken":"reset-refresh-synthetic","expiresAt":4102444800000}}`)
			writeFile(t, filepath.Join(real, "config.json"), `{"hasCompletedOnboarding":true}`)
			view, err := placeholderstore.Project(real, t.TempDir(), "survivor", r.profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			var access, refresh string
			for _, binding := range view.Bindings.Bindings {
				if binding.Kind == "access" {
					access = binding.Placeholder
				}
				if binding.Kind == "refresh" {
					refresh = binding.Placeholder
				}
			}
			// No test bug may dispatch to a public provider.
			provider := r.profile.Providers[0]
			endpoint := *provider.TokenEndpoint
			endpoint.Host = "localhost"
			provider.TokenEndpoint = &endpoint
			store, err := exchange.New(exchange.Config{Providers: []model.ProviderConfig{provider}, Secrets: r.profile.Secrets}, real, hoststore.LockPath(r.host.Home, real), "survivor", nil)
			if err != nil {
				t.Fatal(err)
			}
			headers := http.Header{"Authorization": {"Bearer " + access}}
			releaseReq := httptest.NewRequest(http.MethodGet, "https://api.anthropic.com/", nil).WithContext(ctx)
			releaseReq.Header = headers
			if releases, err := store.Releases(releaseReq); err != nil || len(releases) != 1 {
				t.Fatalf("release control: %d %v", len(releases), err)
			}
			residue := filepath.Join(configDir, ".credentials.json")
			data, err := os.ReadFile(filepath.Join(view.Dir, ".credentials.json"))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, residue, string(data))
			if err := hoststore.ReserveMode(r.host.Home, configDir, hoststore.ModeHolder{ID: "survivor", Session: "survivor", Protected: true, Engine: "docker", OwnerPID: os.Getpid()}, nil); err != nil {
				t.Fatal(err)
			}
			held, unlock, holderDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() {
				defer close(holderDone)
				_ = actual.Storage().(backend.StoreLocker).WithStoreLock(ctx, key, backend.StoreKindAuth, func() error { close(held); <-unlock; return nil })
			}()
			<-held
			done := make(chan error, 1)
			go func() { _, err := r.prepareExecution(); done <- err }()
			select {
			case err := <-done:
				t.Fatalf("reset skipped the gateway's canonical lock: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			if _, err := os.Stat(filepath.Join(real, placeholderstore.BindingsFile)); err != nil {
				t.Fatal("bindings changed while another writer held the lock")
			}
			close(unlock)
			<-holderDone
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "opposite auth mode") {
					t.Fatalf("admission result: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("reset hung after canonical lock release")
			}
			for _, file := range append(r.profile.ProviderAuthFiles(), placeholderstore.BindingsFile) {
				if _, err := os.Stat(filepath.Join(real, file)); !os.IsNotExist(err) {
					t.Fatalf("canonical %s survived reset: %v", file, err)
				}
			}
			if data, err := os.ReadFile(otherFile); err != nil || !strings.Contains(string(data), "other-identity-synthetic") {
				t.Fatal("reset changed an unselected identity")
			}
			if _, err := os.Stat(residue); err != nil || len(be.prepared) != 0 {
				t.Fatal("refused start touched the survivor's project config")
			}
			if releases, err := store.Releases(releaseReq); err != nil || len(releases) != 0 {
				t.Fatalf("stale release after reset: %d %v", len(releases), err)
			}
			body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refresh})
			request, _ := http.NewRequest(http.MethodPost, "https://localhost/v1/oauth/token", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			response, handled := store.Intercept(request, "https")
			if !handled || response.StatusCode != http.StatusForbidden {
				t.Fatal("stale refresh was not refused after reset")
			}
			_ = response.Body.Close()

			// A later admitted phase must reset only project copies, not delete
			// a new grant committed after the preflight mutation.
			if err := hoststore.AdvanceMode(r.host.Home, configDir, "survivor", "finalizing", "", false); err != nil {
				t.Fatal(err)
			}
			if err := hoststore.AdvanceMode(r.host.Home, configDir, "survivor", "ended", "", false); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(real, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"new-access-synthetic"}}`)
			prepared, err := r.prepareVolumes(r.baseContainerName(), r.baseContainerName(), newMountAccumulator(nil, nil), nil)
			if prepared.Cleanup != nil {
				defer prepared.Cleanup()
			}
			if err != nil || be.prepared[len(be.prepared)-1].Auth != nil {
				t.Fatalf("post-admission reset intent: %v", err)
			}
			if _, err := os.Stat(filepath.Join(real, ".credentials.json")); err != nil {
				t.Fatal("post-admission phase deleted a newer canonical grant")
			}
		})
	}
}
