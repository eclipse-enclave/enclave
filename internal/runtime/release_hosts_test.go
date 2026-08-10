// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"slices"
	"strings"
	"testing"

	"enclave/internal/model"
)

// gitlabLikeProfile mirrors the gitlab-cli shape: several services sharing one
// host credential via serviceAuth.hostsFromCredential.
func gitlabLikeProfile() model.Profile {
	return model.Profile{
		Name: "tool",
		Secrets: map[string]model.SecretConfig{
			"gitlab-host": secretConfig([]string{"GITLAB_HOST"}, nil),
			"gitlab-token": secretConfig([]string{"GITLAB_TOKEN"}, &model.HTTPSecretReleaseConfig{
				Hosts:           []string{"gitlab.com", "*.gitlab.com"},
				Header:          "private-token",
				HostsFromSecret: "gitlab-host",
			}),
			"gitlab-job-token": secretConfig([]string{"JOB_TOKEN"}, &model.HTTPSecretReleaseConfig{
				Hosts:           []string{"gitlab.com", "*.gitlab.com"},
				Header:          "job-token",
				HostsFromSecret: "gitlab-host",
			}),
		},
	}
}

func TestResolveReleaseHostOverridesReplacesDeclaredHosts(t *testing.T) {
	t.Setenv("GITLAB_HOST", "gitlab.example.com")

	r := runtimeWithProfile(t, gitlabLikeProfile())
	overrides := resolveReleaseHostOverrides(mustActiveSecrets(t, r), r.host.Home, map[string]string{}, map[string]string{})

	for _, id := range []string{"gitlab-token", "gitlab-job-token"} {
		hosts, ok := overrides[id]
		if !ok {
			t.Fatalf("overrides[%s] missing, want the resolved host", id)
		}
		// Replacement, not addition: the instance token must not be released
		// to gitlab.com as well.
		if len(hosts) != 1 || hosts[0] != "gitlab.example.com" {
			t.Fatalf("overrides[%s] = %v, want [gitlab.example.com]", id, hosts)
		}
	}
	if _, ok := overrides["gitlab-host"]; ok {
		t.Fatalf("overrides contains the host credential itself, want only referencing services")
	}
}

func TestNormalizeReleaseHost(t *testing.T) {
	cases := map[string]string{
		"https://gitlab.example.com/group": "gitlab.example.com",
		"gitlab.example.com:8443":          "gitlab.example.com",
		"  GitLab.Example.com  ":           "gitlab.example.com",
		"not a host":                       "",
		// A wildcard would release the token to every host under the suffix.
		"*.example.com": "",
	}
	for value, want := range cases {
		t.Run(value, func(t *testing.T) {
			if got := normalizeReleaseHost("gitlab-host", value); got != want {
				t.Fatalf("normalizeReleaseHost(%q) = %q, want %q", value, got, want)
			}
		})
	}
}

func TestResolveReleaseHostOverridesKeepsDeclaredHostsWhenUnset(t *testing.T) {
	t.Setenv("GITLAB_HOST", "")

	r := runtimeWithProfile(t, gitlabLikeProfile())
	overrides := resolveReleaseHostOverrides(mustActiveSecrets(t, r), r.host.Home, map[string]string{}, map[string]string{})

	if len(overrides) != 0 {
		t.Fatalf("overrides = %v, want none when the host credential is unset", overrides)
	}
}

// The selected host must reach the allow set, or the release rule would name a
// host the sandbox cannot resolve. The declared hosts stay reachable — only
// token release is narrowed — and the loaded spec, shared across the session,
// must not be mutated.
func TestSpecNetworkDomainsIncludesOverriddenReleaseHost(t *testing.T) {
	r := runtimeWithProfile(t, gitlabLikeProfile())
	r.releaseHostOverrides = map[string][]string{
		"gitlab-token":     {"gitlab.example.com"},
		"gitlab-job-token": {"gitlab.example.com"},
	}

	allowed, _ := r.specNetworkDomains()

	for _, want := range []string{"gitlab.example.com", "gitlab.com", "*.gitlab.com"} {
		if !slices.Contains(allowed, want) {
			t.Fatalf("allowed = %v, want %q included", allowed, want)
		}
	}
	if hosts := r.profile.Secrets["gitlab-token"].Release.HTTP.Hosts; strings.Join(hosts, ",") != "gitlab.com,*.gitlab.com" {
		t.Fatalf("profile hosts = %v, want the declared hosts unchanged", hosts)
	}
}

// The token itself must still go only to the selected instance.
func TestReleaseHostsForUsesOnlyTheOverride(t *testing.T) {
	r := runtimeWithProfile(t, gitlabLikeProfile())
	r.releaseHostOverrides = map[string][]string{"gitlab-token": {"gitlab.example.com"}}
	manager := newAuthManager(r)

	secrets := mustActiveSecrets(t, r)
	for _, secret := range secrets {
		if secret.ID != "gitlab-token" {
			continue
		}
		if hosts := manager.releaseHostsFor(secret); strings.Join(hosts, ",") != "gitlab.example.com" {
			t.Fatalf("releaseHostsFor(gitlab-token) = %v, want only the selected host", hosts)
		}
	}
}

// The effective policy is memoized and unions the release hosts into the allow
// set, so an override arriving after a policy lookup must invalidate it instead
// of leaving the selected host unresolvable.
func TestSetReleaseHostOverridesInvalidatesResolvedPolicy(t *testing.T) {
	r := runtimeWithProfile(t, gitlabLikeProfile())
	r.policyResolved = true

	r.setReleaseHostOverrides(nil)
	if !r.policyResolved {
		t.Fatalf("policyResolved = false, want the memo kept when there is no override")
	}

	r.setReleaseHostOverrides(map[string][]string{"gitlab-token": {"gitlab.example.com"}})
	if r.policyResolved {
		t.Fatalf("policyResolved = true, want the memo dropped so the selected host reaches the allow set")
	}
}

// The host selector is a choice, not a credential: persisting it would pin
// every later run to the instance one run happened to name.
func TestInjectDeclaredSecretsDoesNotPersistHostSelector(t *testing.T) {
	t.Setenv("GITLAB_HOST", "gitlab.example.com")
	t.Setenv("GITLAB_TOKEN", "gitlab-token-value")
	t.Setenv("JOB_TOKEN", "")

	r := runtimeWithProfile(t, gitlabLikeProfile())
	manager := newAuthManager(r)

	env := []string{}
	injection, err := manager.injectDeclaredSecrets(
		stubHooks{},
		authContextForRuntime(r),
		&env,
		map[string]string{},
		map[string]string{},
		nil,
		mustActiveSecrets(t, r),
	)
	if err != nil {
		t.Fatalf("injectDeclaredSecrets() error = %v", err)
	}

	if got := envValue(env, "GITLAB_HOST"); got != "gitlab.example.com" {
		t.Fatalf("GITLAB_HOST = %q, want it injected into the container", got)
	}
	if _, ok := injection.SecretValues["GITLAB_HOST"]; ok {
		t.Fatalf("SecretValues = %v, want the host selector left out of the persisted set", injection.SecretValues)
	}
	if got := injection.SecretValues["GITLAB_TOKEN"]; got != "gitlab-token-value" {
		t.Fatalf("SecretValues[GITLAB_TOKEN] = %q, want the token still persisted", got)
	}
}
