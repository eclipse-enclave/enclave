// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"errors"
	"os"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

type admissionCleanupStorage struct{ backend.StoreManager }

func (admissionCleanupStorage) ReadFile(context.Context, backend.StoreKey, backend.StoreKind, string) ([]byte, error) {
	return nil, os.ErrNotExist
}

type admissionCleanupBackend struct {
	fakeBackend
	released []*backend.AuthSession
	storeErr error
}

func (b *admissionCleanupBackend) PrepareAuthSession(_ context.Context, key backend.StoreKey, session string, protected, _ bool) (*backend.AuthSession, error) {
	return &backend.AuthSession{Key: key, Session: session, Protected: protected}, nil
}

func (b *admissionCleanupBackend) ReleaseAuthSession(_ context.Context, ref *backend.AuthSession) error {
	b.released = append(b.released, ref)
	return nil
}

func (*admissionCleanupBackend) ResetAuthIdentity(context.Context, backend.StoreKey) error {
	return nil
}

func (*admissionCleanupBackend) DowngradeAuthIdentity(context.Context, backend.StoreKey) error {
	return nil
}

func (b *admissionCleanupBackend) PrepareStores(ctx context.Context, prep backend.StorePrep) (backend.StoreState, error) {
	if b.storeErr != nil {
		return backend.StoreState{}, b.storeErr
	}
	return b.fakeBackend.PrepareStores(ctx, prep)
}

type featureAdmissionCleanupBackend struct {
	*admissionCleanupBackend
	featureErr error
}

func (b *featureAdmissionCleanupBackend) PrepareFeatureAuthSessions(_ context.Context, parent *backend.AuthSession, session string, names []string, protected bool) (*backend.AuthSession, error) {
	combined := backend.AuthSession{Session: session, Protected: protected}
	if parent != nil {
		combined = *parent
	}
	combined.Features = append([]string(nil), names...)
	return &combined, b.featureErr
}

func TestAuthAdmissionCleanupReleasesLatestCombinedSessionOnce(t *testing.T) {
	for _, scenario := range []string{"success", "feature-only", "feature-error", "store-error", "feature-manager-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_CONFIG_DIR", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
				t.Setenv(key, "")
			}
			base := &admissionCleanupBackend{fakeBackend: fakeBackend{storage: admissionCleanupStorage{}}}
			features := &featureAdmissionCleanupBackend{admissionCleanupBackend: base}
			r := protectedTestRuntime()
			r.host.Home = t.TempDir()
			r.project = model.Project{Hash: "cleanup-project", Dir: t.TempDir()}
			r.profile.Providers = []model.ProviderConfig{{Name: "anthropic", AuthFiles: []string{".credentials.json"}}}
			r.features = []model.Extension{{Name: "github-cli", ConfigDir: ".config/gh", AuthFiles: []string{"hosts.yml", "config.yml"}}}
			r.backend = features
			switch scenario {
			case "feature-only":
				r.auth.ProtectAuth = false
				r.profile = model.Profile{Name: "custom", ConfigDir: ".custom"}
				r.policyResolved = true
				r.policyResult.Effective.Mode = model.NetworkModeUnrestricted
			case "feature-error":
				features.featureErr = errors.New("feature admission failed")
			case "store-error":
				base.storeErr = errors.New("store preparation failed")
			case "feature-manager-unavailable":
				r.backend = base
			}
			prepared, err := r.prepareVolumes("session", "session", &mountAccumulator{})
			success := scenario == "success" || scenario == "feature-only"
			if (err == nil) != success {
				t.Fatalf("unexpected preparation result: %v", err)
			}
			if success {
				if len(base.released) != 0 || prepared.Cleanup == nil {
					t.Fatal("successful preparation released admission before cleanup")
				}
				prepared.Cleanup()
			}
			if len(base.released) != 1 {
				t.Fatalf("cleanup released admission %d times, want once", len(base.released))
			}
			if base.released[0] != r.authSession {
				t.Fatal("cleanup did not release the latest combined admission")
			}
			if scenario != "feature-manager-unavailable" && len(base.released[0].Features) != 1 {
				t.Fatal("cleanup omitted feature admission")
			}
			if scenario == "feature-only" && base.released[0].Key.Owner != "" {
				t.Fatal("feature-only cleanup invented a tool identity")
			}
		})
	}
}
