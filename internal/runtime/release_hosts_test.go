// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
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

// The replacement host must reach the allow set, or the release rule would name
// a host the sandbox cannot resolve, and it must not leak into the loaded spec,
// which is shared across the session.
func TestSpecNetworkDomainsIncludesOverriddenReleaseHost(t *testing.T) {
	r := runtimeWithProfile(t, gitlabLikeProfile())
	r.releaseHostOverrides = map[string][]string{
		"gitlab-token":     {"gitlab.example.com"},
		"gitlab-job-token": {"gitlab.example.com"},
	}

	allowed, _ := r.specNetworkDomains()
	joined := strings.Join(allowed, ",")

	if !strings.Contains(joined, "gitlab.example.com") {
		t.Fatalf("allowed = %v, want the overridden host included", allowed)
	}
	if strings.Contains(joined, "gitlab.com,") || strings.Contains(joined, ",gitlab.com") {
		t.Fatalf("allowed = %v, want the replaced default host dropped", allowed)
	}
	if hosts := r.profile.Secrets["gitlab-token"].Release.HTTP.Hosts; strings.Join(hosts, ",") != "gitlab.com,*.gitlab.com" {
		t.Fatalf("profile hosts = %v, want the declared hosts unchanged", hosts)
	}
}
