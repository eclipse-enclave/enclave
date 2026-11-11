// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/util"
)

// updateLockin regenerates the session lock-in snapshots. Run:
//
//	go test ./internal/backend/docker/ -run TestLockin -update-lockin
var updateLockin = flag.Bool("update-lockin", false, "rewrite the session lock-in golden files")

// lockinEngine installs a container CLI that answers the calls a session start
// makes (image present, no stale gateway, gateway ready) and records every
// invocation, one argument per line, each call terminated by "---".
func lockinEngine(t *testing.T, engine string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, engine)
	script := `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg" >> "$LOCKIN_CALL_LOG"; done
printf -- '---\n' >> "$LOCKIN_CALL_LOG"
case "$1 $2" in
"image inspect") printf '%s\n' '{"Id":"sha256:gateway"}' ;;
"container inspect") printf 'Error: No such container\n' >&2; exit 1 ;;
"logs "*) printf 'Gateway ready\n' ;;
"run "*) printf '%064d\n' 0 ;;
esac
exit 0
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil { // #nosec G306 -- test stub must be executable.
		t.Fatalf("write engine stub: %v", err)
	}
	previous := dockercmd.Binary()
	dockercmd.SetBinary(stub)
	t.Cleanup(func() { dockercmd.SetBinary(previous) })
	t.Setenv("LOCKIN_CALL_LOG", logPath)
	return logPath
}

func lockinBackend(t *testing.T) (*Backend, string) {
	t.Helper()
	if util.IsSELinuxEnforcing() {
		t.Skip("lock-in snapshots are recorded without SELinux relabeling")
	}
	// The backend writes the secret release file (real secret values) to the
	// temp dir; keep it inside the test's own directory so it never outlives it.
	t.Setenv("TMPDIR", t.TempDir())
	home := t.TempDir()
	tlsDir := config.HostTLSDir(home)
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca.crt", "ca.key"} {
		if err := os.WriteFile(filepath.Join(tlsDir, name), []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return New(Options{
		Host:           model.Host{Home: home, UID: "1000", GID: "1000"},
		NoRebuild:      true,
		NetworkLogMode: model.NetworkLogCoarse,
		ProjectDir:     "/work/project",
	}), home
}

func lockinRequest(meta backend.SessionMeta) backend.Request {
	meta.Tool = "codex"
	meta.Worktree = "/work/project"
	meta.RealWorktree = "/work/project"
	return backend.Request{
		Session:    meta,
		Image:      "enclave-codex:latest",
		Argv:       []string{"codex"},
		Entrypoint: []string{"/usr/local/bin/entrypoint.sh"},
		Env:        []backend.EnvVar{{Name: "API_KEY", Value: "placeholder"}, {Name: "ENCLAVE_DNS_GATEWAY", Value: "1"}},
		WorkingDir: "/work/project",
		User:       "1000:1000",
		Mounts:     []backend.Mount{{Type: backend.MountTypeBind, Source: "/work/project", ContainerPath: "/work/project"}},
		Ports:      []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "13000", ContainerPort: "3000", Protocol: "tcp"}},
		Network: backend.NetworkPolicy{
			LoopbackPorts:  []string{"1455"},
			IdeBridgePorts: []string{"9800"},
		},
		Detached: true,
	}
}

// TestLockinRestrictedSessionStart pins the engine invocations of a restricted
// session start (gateway sidecar and session container) and the gateway config
// bundle it writes.
func TestLockinRestrictedSessionStart(t *testing.T) {
	for _, engine := range []string{backend.NameDocker, backend.NamePodman} {
		t.Run(engine, func(t *testing.T) {
			b, home := lockinBackend(t)
			logPath := lockinEngine(t, engine)
			req := lockinRequest(prepareRunSession(t))
			req.Network.Mode = backend.NetworkModeRestricted
			req.Network.Gateway = true
			req.Network.Egress = backend.EgressPolicy{
				AllowedDomains: []string{"api.example.com", "example.org"},
				DeniedDomains:  []string{"blocked.example.org"},
				Resolvers:      []string{"9.9.9.9"},
			}
			req.Secrets = []backend.SecretRelease{{
				SecretID:    "api-key",
				Placeholder: "placeholder",
				EnvVars:     []string{"API_KEY"},
				Value:       "real-secret",
				HTTP:        &backend.HTTPReleaseRule{Hosts: []string{"api.example.com"}, Header: "Authorization", Format: "Bearer {}"},
			}}

			if _, err := b.Start(context.Background(), req); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			gatewayDir := config.HostProjectGatewayConfigDir(home, req.Session.ProjectHash, req.Session.Tool)
			got := lockinCalls(t, logPath, home) +
				"\n=== secret release file ===\n" + lockinSecretReleaseFile(t, logPath) +
				"\n=== gateway config bundle ===\n" + lockinDir(t, gatewayDir)
			assertLockinGolden(t, "restricted-session-start-"+engine, got)
		})
	}
}

// TestLockinUnrestrictedSessionStart pins the engine invocations of a
// gateway-less unrestricted session start.
func TestLockinUnrestrictedSessionStart(t *testing.T) {
	for _, engine := range []string{backend.NameDocker, backend.NamePodman} {
		t.Run(engine, func(t *testing.T) {
			b, home := lockinBackend(t)
			logPath := lockinEngine(t, engine)
			req := lockinRequest(prepareRunSession(t))
			req.Network.Mode = backend.NetworkModeUnrestricted
			req.Hostname = model.HostnamePrefix + "project"
			req.Env = []backend.EnvVar{{Name: "API_KEY", Value: "real-secret"}}

			if _, err := b.Start(context.Background(), req); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			gatewayDir := config.HostProjectGatewayConfigDir(home, req.Session.ProjectHash, req.Session.Tool)
			if util.PathExists(gatewayDir) {
				t.Fatalf("unrestricted session wrote a gateway config bundle at %s", gatewayDir)
			}
			assertLockinGolden(t, "unrestricted-session-start-"+engine, lockinCalls(t, logPath, home))
		})
	}
}

// A restricted session without a gateway would run with no enforcement at all,
// so the backend must refuse it rather than fall back to the no-gateway path.
func TestRestrictedSessionWithoutGatewayFailsClosed(t *testing.T) {
	b, _ := lockinBackend(t)
	logPath := lockinEngine(t, backend.NameDocker)
	req := lockinRequest(prepareRunSession(t))
	req.Network.Mode = backend.NetworkModeRestricted

	if _, err := b.Start(context.Background(), req); err == nil {
		t.Fatal("Start() succeeded for a restricted session without a gateway")
	}
	data, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if slices.Contains(strings.Split(string(data), "\n"), "run") {
		t.Fatalf("restricted session without a gateway reached the engine:\n%s", data)
	}
}

var (
	lockinSinceArg      = regexp.MustCompile(`(?m)^--since\n[0-9]+$`)
	lockinUserNSWarmup  = regexp.MustCompile(`enclave-userns-warmup-[0-9-]+`)
	lockinSecretRelease = regexp.MustCompile(`[^\s,=]*/enclave-secret-release-[0-9]+\.json`)
)

func lockinCalls(t *testing.T, logPath string, home string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read engine call log: %v", err)
	}
	// The state root differs per platform (XDG on Linux, Application Support
	// on macOS); normalizing it keeps the snapshots host-independent.
	out := strings.ReplaceAll(string(data), filepath.Dir(config.HostTLSDir(home)), "<state>")
	out = strings.ReplaceAll(out, home, "<home>")
	out = lockinSinceArg.ReplaceAllString(out, "--since\n<since>")
	out = lockinUserNSWarmup.ReplaceAllString(out, "<userns-warmup>")
	return lockinSecretRelease.ReplaceAllString(out, "<secret-release-file>")
}

func lockinSecretReleaseFile(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read engine call log: %v", err)
	}
	path := lockinSecretRelease.FindString(string(data))
	if path == "" {
		t.Fatal("gateway invocation mounts no secret release file")
	}
	content, err := os.ReadFile(path) // #nosec G304 -- path is the temp file the backend created.
	if err != nil {
		t.Fatalf("read secret release file: %v", err)
	}
	return string(content) + "\n"
}

var lockinMetaTimes = regexp.MustCompile(`"(generated_at|generation)": "[^"]*"`)

func lockinDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	var out strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out.WriteString("--- " + name + " ---\n")
		out.WriteString(lockinMetaTimes.ReplaceAllString(string(data), `"$1": "<time>"`))
	}
	return out.String()
}

func assertLockinGolden(t *testing.T, name string, got string) {
	t.Helper()
	path := filepath.Join("testdata", "lockin", name+".golden")
	if *updateLockin {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- fixed testdata path.
	if err != nil {
		t.Fatalf("read golden %s (regenerate with -update-lockin): %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("%s drifted from %s (regenerate with -update-lockin only for an intended change)\n--- got ---\n%s", name, path, got)
	}
}
