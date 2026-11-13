// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

const testFilterCredential = `{"refreshToken":"1//refresh-token-never-in-an-argument"}`

func testFilterBundles() []backend.FilterBundle {
	return []backend.FilterBundle{
		{Name: "gws", Policy: []byte(`{"filter":"gws"}`), Credential: []byte(testFilterCredential), Hosts: []string{"www.googleapis.com", "gmail.googleapis.com"}, Confine: []string{"googleapis.com"}},
		{Name: "other", Policy: []byte(`{"filter":"other"}`), Credential: []byte(`{"static":"x"}`), Hosts: []string{"*.example.com"}},
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

func TestWriteFilterBundles(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir, err := writeFilterBundles(testFilterBundles())
	if err != nil {
		t.Fatalf("writeFilterBundles() error = %v", err)
	}
	defer cleanupDir(dir)

	assertMode(t, dir, 0o700)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
		assertMode(t, filepath.Join(dir, entry.Name()), 0o600)
	}
	if got := strings.Join(names, " "); got != "gws.credential.json gws.policy.json other.credential.json other.policy.json" {
		t.Fatalf("files = %s", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "gws.credential.json"))
	if err != nil || string(data) != testFilterCredential {
		t.Fatalf("gws credential = %q, %v", data, err)
	}

	cleanupDir(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanupDir left %s: %v", dir, err)
	}
}

func TestWriteFilterBundlesNone(t *testing.T) {
	dir, err := writeFilterBundles(nil)
	if err != nil || dir != "" {
		t.Fatalf("writeFilterBundles(nil) = %q, %v, want nothing", dir, err)
	}
}

func TestWriteFilterBundlesRejectsInvalidBundles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	for name, bundles := range map[string][]backend.FilterBundle{
		"path traversal": {{Name: "../gws", Policy: []byte("{}"), Credential: []byte("{}")}},
		"upper case":     {{Name: "GWS", Policy: []byte("{}"), Credential: []byte("{}")}},
		"duplicate":      {{Name: "gws", Policy: []byte("{}"), Credential: []byte("{}")}, {Name: "gws", Policy: []byte("{}"), Credential: []byte("{}")}},
		"no credential":  {{Name: "gws", Policy: []byte("{}")}},
		"no policy":      {{Name: "gws", Credential: []byte("{}")}},
	} {
		t.Run(name, func(t *testing.T) {
			if dir, err := writeFilterBundles(bundles); err == nil {
				cleanupDir(dir)
				t.Fatal("writeFilterBundles() accepted an invalid bundle")
			}
		})
	}
	assertNoFilterDirs(t, tmp)
}

func assertNoFilterDirs(t *testing.T, tmp string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(tmp, "enclave-filters-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > 0 {
		t.Fatalf("request filter directories left behind: %v", matches)
	}
}

// filterSnapshotEngine is lockinEngine that also copies the directory
// mounted at the gateway's filter path when the gateway runs, so a test can
// see what the gateway received after the host removed it.
func filterSnapshotEngine(t *testing.T, engine string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	snapshot := filepath.Join(dir, "snapshot")
	stub := filepath.Join(dir, engine)
	script := `#!/bin/sh
for arg in "$@"; do
	printf '%s\n' "$arg" >> "$LOCKIN_CALL_LOG"
	case "$arg" in
	*target=` + model.GatewayFilterDir + `,*)
		src=${arg#*source=}
		src=${src%%,*}
		cp -Rp "$src" "$FILTER_SNAPSHOT"
		;;
	esac
done
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
		t.Fatal(err)
	}
	previous := dockercmd.Binary()
	dockercmd.SetBinary(stub)
	t.Cleanup(func() { dockercmd.SetBinary(previous) })
	t.Setenv("LOCKIN_CALL_LOG", logPath)
	t.Setenv("FILTER_SNAPSHOT", snapshot)
	return logPath, snapshot
}

func restrictedFilterRequest(t *testing.T) backend.Request {
	t.Helper()
	req := lockinRequest(prepareRunSession(t))
	req.Network.Mode = backend.NetworkModeRestricted
	req.Network.Gateway = true
	req.Filters = testFilterBundles()
	return req
}

// The restricted gateway gets the filters as a read-only directory mount with
// private files, and the host removes its copy once the gateway is ready.
func TestRestrictedGatewayReceivesFilters(t *testing.T) {
	for _, engine := range []string{backend.NameDocker, backend.NamePodman} {
		t.Run(engine, func(t *testing.T) {
			b, home := lockinBackend(t)
			tmp := os.Getenv("TMPDIR")
			logPath, snapshot := filterSnapshotEngine(t, engine)
			req := restrictedFilterRequest(t)

			if _, err := b.Start(context.Background(), req); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			calls := engineInvocations(t, logPath, home)
			gw := runInvocation(t, calls, req.Session.Name+model.GatewayContainerSuffix)
			session := runInvocation(t, calls, req.Session.Name)
			filterDir := filepath.Join(tmp, "enclave-filters-")
			for _, want := range []string{
				"--env " + model.EnvFilterDir + "=" + model.GatewayFilterDir + " ",
				"type=bind,source=" + filterDir,
				",target=" + model.GatewayFilterDir + ",readonly",
				"--label " + model.GatewayLabelFilters + "=gws,other",
				"--label " + model.GatewayLabelFilterHosts + "=*.example.com,gmail.googleapis.com,www.googleapis.com",
				"--label " + model.GatewayLabelFilterConfine + "=googleapis.com",
				// The entrypoint copies the credentials to a tmpfs, never to
				// a container layer.
				"--tmpfs " + model.GatewayProxyDataDir + ":rw,nosuid,nodev,noexec,mode=0755,size=64m",
			} {
				if !strings.Contains(gw, want) {
					t.Errorf("gateway invocation lacks %q:\n%s", want, gw)
				}
			}
			for _, unwanted := range []string{model.GatewayFilterDir, filterDir, model.EnvFilterDir} {
				if strings.Contains(session, unwanted) {
					t.Errorf("session invocation carries %q:\n%s", unwanted, session)
				}
			}
			if strings.Contains(strings.Join(calls, "\n"), "refresh-token-never-in-an-argument") {
				t.Error("credential material appears in an engine invocation")
			}

			assertMode(t, snapshot, 0o700)
			for _, name := range []string{"gws.policy.json", "gws.credential.json", "other.policy.json", "other.credential.json"} {
				assertMode(t, filepath.Join(snapshot, name), 0o600)
			}
			assertNoFilterDirs(t, tmp)

			// The governed names feed the ipset the gateway confines to its
			// proxy, also when no allowlist entry names them.
			conf, err := os.ReadFile(filepath.Join(config.HostProjectGatewayConfigDir(home, req.Session.ProjectHash, req.Session.Tool), "dnsmasq.conf"))
			if err != nil {
				t.Fatal(err)
			}
			for _, domain := range []string{"www.googleapis.com", "gmail.googleapis.com", "example.com"} {
				if want := "ipset=/" + domain + "/enclave_allowed,enclave_governed\n"; !strings.Contains(string(conf), want) {
					t.Errorf("dnsmasq.conf lacks %q:\n%s", want, conf)
				}
			}
			// The confined domain is blackholed below the governed names.
			if want := "address=/googleapis.com/\n"; !strings.Contains(string(conf), want) {
				t.Errorf("dnsmasq.conf lacks %q:\n%s", want, conf)
			}
		})
	}
}

// Defense in depth for I10: an observing gateway enforces nothing, so it
// refuses filters instead of dropping them.
func TestUnrestrictedGatewayRefusesFilters(t *testing.T) {
	b, home := lockinBackend(t)
	logPath := lockinEngine(t, backend.NameDocker)
	req := unrestrictedGatewayRequest(t)
	req.Filters = testFilterBundles()

	_, err := b.Start(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("Start() error = %v, want request filters refused", err)
	}
	if _, statErr := os.Stat(logPath); statErr == nil {
		for _, call := range engineInvocations(t, logPath, home) {
			if strings.HasPrefix(call, "run ") {
				t.Fatalf("a container was started: %s", call)
			}
		}
	}
	assertNoFilterDirs(t, os.Getenv("TMPDIR"))

	// The observing gateway refuses on its own, too.
	if _, _, err := b.startObservingGateway(context.Background(), req, "session-network"); err == nil {
		t.Fatal("startObservingGateway() accepted request filters")
	}
	assertNoFilterDirs(t, os.Getenv("TMPDIR"))
}

func TestFiltersWithoutGatewayRefused(t *testing.T) {
	b, _ := lockinBackend(t)
	lockinEngine(t, backend.NameDocker)
	req := lockinRequest(prepareRunSession(t))
	req.Network.Mode = backend.NetworkModeUnrestricted
	req.Network.Gateway = false
	req.Filters = testFilterBundles()

	if _, err := b.Start(context.Background(), req); err == nil {
		t.Fatal("Start() ran request filters without a gateway")
	}
}

func TestFailedGatewayStartRemovesFilters(t *testing.T) {
	b, _ := lockinBackend(t)
	failingGatewayEngine(t)

	if _, err := b.Start(context.Background(), restrictedFilterRequest(t)); err == nil {
		t.Fatal("Start() succeeded although the gateway failed")
	}
	assertNoFilterDirs(t, os.Getenv("TMPDIR"))
}

// The docker and podman backends are the ones whose gateway enforces request
// filters, so they alone report the capability, and Validate lets their
// filters through.
func TestDockerBackendEnforcesRequestFilters(t *testing.T) {
	for _, engine := range []string{backend.NameDocker, backend.NamePodman} {
		be := New(Options{Engine: engine})
		if !be.Capabilities().RequestFilters {
			t.Fatalf("%s backend lacks the RequestFilters capability", engine)
		}
		req := backend.Request{Network: backend.NetworkPolicy{Mode: backend.NetworkModeRestricted, Gateway: true}, Filters: testFilterBundles()}
		if err := backend.Validate(req, be.Capabilities()); err != nil {
			t.Fatalf("%s: Validate = %v", engine, err)
		}
	}
}
