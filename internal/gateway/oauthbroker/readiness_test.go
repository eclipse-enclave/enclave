// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedReadinessRequiresInitializedAdaptersAndExactSession(t *testing.T) {
	for _, github := range []bool{false, true} {
		name := "claude"
		if github {
			name += "-github"
		}
		t.Run(name, func(t *testing.T) {
			b := fixtureBroker(t)
			if github {
				b = fixtureGitHub(t, githubNativeFixture())
			}
			if err := VerifyReadiness(b.config); err == nil {
				t.Fatal("unstarted broker attested readiness")
			}
			if err := b.MarkReady(); err == nil {
				t.Fatal("uninitialized native view attested readiness")
			}
			if github {
				b.registry.ViewInitialized = true
				if err := b.MarkReady(); err == nil {
					t.Fatal("uninitialized GitHub adapter attested readiness")
				}
				b.registry.ViewInitialized = false
			}
			view := fixtureViewDirectory(t)
			if err := b.InitializeView(view); err != nil {
				t.Fatal(err)
			}
			if err := b.MarkReady(); err != nil {
				t.Fatal(err)
			}
			if err := VerifyReadiness(b.config); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"identity", "session", "incarnation", "github"} {
				expected := b.config
				switch field {
				case "identity":
					expected.Identity = "different"
				case "session":
					expected.Session = "different"
				case "incarnation":
					expected.Incarnation = "different"
				case "github":
					if github {
						expected.GitHub = nil
					} else {
						expected.GitHub = &GitHubConfig{Identity: "different"}
					}
				}
				if err := VerifyReadiness(expected); err == nil {
					t.Fatalf("readiness accepted different %s", field)
				}
			}
			if err := ClearReadiness(b.config.RuntimeDir); err != nil {
				t.Fatal(err)
			}
			if err := VerifyReadiness(b.config); err == nil {
				t.Fatal("previous gateway readiness survived clearing")
			}
			// A private-looking alias is not attestation from this runtime.
			outside := filepath.Join(t.TempDir(), "attestation")
			if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(b.config.RuntimeDir, readinessFile)); err != nil {
				t.Fatal(err)
			}
			if err := VerifyReadiness(b.config); err == nil {
				t.Fatal("readiness followed a symlink")
			}
			if err := ClearReadiness(b.config.RuntimeDir); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(outside); err != nil {
				t.Fatal("clearing readiness removed the symlink target")
			}
		})
	}
}
