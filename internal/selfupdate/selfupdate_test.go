// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package selfupdate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/buildinfo"
)

const (
	testArtifact = "enclave-linux-amd64"
	testCommit   = "1234567890abcdef1234567890abcdef12345678"
	otherCommit  = "abcdef0123456789abcdef0123456789abcdef01"
)

// testRelease serves a rolling release. Its handlers can be swapped per test
// to simulate a broken or moving release.
type testRelease struct {
	ref       http.HandlerFunc
	checksums http.HandlerFunc
	binary    http.HandlerFunc
}

func newTestRelease(binary []byte) *testRelease {
	digest := sha256.Sum256(binary)
	return &testRelease{
		ref: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"object":{"sha":%q,"type":"commit"}}`, testCommit)
		},
		checksums: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, "%x  enclave-darwin-arm64\n%x  %s\n", sha256.Sum256(nil), digest, testArtifact)
		},
		binary: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(binary)
		},
	}
}

func (r *testRelease) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/ref":
			r.ref(w, req)
		case "/checksums.txt":
			r.checksums(w, req)
		case "/" + testArtifact:
			r.binary(w, req)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestService(server *httptest.Server, target string) *Service {
	service := New()
	service.refURL = server.URL + "/ref"
	service.assetsURL = server.URL
	service.goos = "linux"
	service.goarch = "amd64"
	service.current = buildinfo.Info{Version: rollingVersion, Commit: "abcdef0"}
	service.executablePath = func() (string, error) { return target, nil }
	service.externalAssets = func(string) (string, bool) { return "", false }
	return service
}

func writeExecutable(t *testing.T, mode os.FileMode) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "enclave")
	if err := os.WriteFile(target, []byte("old binary"), mode); err != nil {
		t.Fatal(err)
	}
	return target
}

func assertUnchanged(t *testing.T, target string) {
	t.Helper()
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary" {
		t.Fatalf("executable changed after failed update: %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("failed update left files behind: %v", entries)
	}
}

func TestCheckComparesShortAndFullCommits(t *testing.T) {
	server := newTestRelease(nil).serve(t)
	service := newTestService(server, "")
	for _, tc := range []struct {
		name      string
		current   string
		available bool
	}{
		{name: "same short commit", current: "1234567", available: false},
		{name: "dirty same commit", current: "1234567-dirty", available: false},
		{name: "different commit", current: "abcdef0", available: true},
		{name: "unknown build", current: "unknown", available: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service.current.Commit = tc.current
			status, err := service.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if status.UpdateAvailable != tc.available {
				t.Fatalf("UpdateAvailable = %v, want %v", status.UpdateAvailable, tc.available)
			}
		})
	}
}

func TestCheckRejectsBadReferences(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"annotated tag": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"object":{"sha":%q,"type":"tag"}}`, testCommit)
		},
		"not hex": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"object":{"sha":"not-a-commit","type":"commit"}}`)
		},
		"missing release": func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			release := newTestRelease(nil)
			release.ref = handler
			service := newTestService(release.serve(t), "")
			if _, err := service.Check(context.Background()); err == nil {
				t.Fatal("Check accepted a bad rolling reference")
			}
		})
	}
}

func TestInstallVerifiesAndAtomicallyReplacesExecutable(t *testing.T) {
	newBinary := []byte("new enclave binary")
	server := newTestRelease(newBinary).serve(t)
	target := writeExecutable(t, 0o751)

	installed, err := newTestService(server, target).Install(context.Background(), testCommit)
	if err != nil {
		t.Fatal(err)
	}
	if installed != target {
		t.Fatalf("installed path = %q, want %q", installed, target)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBinary) {
		t.Fatalf("installed contents = %q, want %q", got, newBinary)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o751 {
		t.Fatalf("installed mode = %o, want 751", info.Mode().Perm())
	}
}

func TestInstallFailuresPreserveExecutable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(*testRelease, *Service)
		wantErr string
	}{
		{
			name: "checksum mismatch",
			setup: func(r *testRelease, _ *Service) {
				r.binary = func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "tampered binary") }
			},
			wantErr: "checksum mismatch",
		},
		{
			name: "no checksum entry",
			setup: func(r *testRelease, _ *Service) {
				r.checksums = func(w http.ResponseWriter, _ *http.Request) {
					_, _ = fmt.Fprintf(w, "%x  enclave-darwin-arm64\n", sha256.Sum256(nil))
				}
			},
			wantErr: "has no checksum for " + testArtifact,
		},
		{
			name: "binary missing",
			setup: func(r *testRelease, _ *Service) {
				r.binary = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }
			},
			wantErr: "502",
		},
		{
			name:    "oversized download",
			setup:   func(_ *testRelease, s *Service) { s.maxBinaryBytes = 4 },
			wantErr: "exceeds 4 bytes",
		},
		{
			name: "release moved during download",
			setup: func(r *testRelease, _ *Service) {
				r.ref = func(w http.ResponseWriter, _ *http.Request) {
					_, _ = fmt.Fprintf(w, `{"object":{"sha":%q,"type":"commit"}}`, otherCommit)
				}
			},
			wantErr: "moved from 12345678 to abcdef01",
		},
		{
			name:    "source build",
			setup:   func(_ *testRelease, s *Service) { s.current.Version = "0.1.0" },
			wantErr: `version "0.1.0"`,
		},
		{
			name: "adjacent assets",
			setup: func(_ *testRelease, s *Service) {
				s.externalAssets = func(exe string) (string, bool) { return filepath.Dir(exe), true }
			},
			wantErr: "cannot replace",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := newTestRelease([]byte("new enclave binary"))
			target := writeExecutable(t, 0o755)
			server := release.serve(t)
			service := newTestService(server, target)
			tc.setup(release, service)

			_, err := service.Install(context.Background(), testCommit)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Install error = %v, want it to contain %q", err, tc.wantErr)
			}
			assertUnchanged(t, target)
		})
	}
}

func TestSupportedRequiresStandaloneRollingBinary(t *testing.T) {
	service := newTestService(newTestRelease(nil).serve(t), "/opt/enclave/enclave")
	if err := service.Supported(); err != nil {
		t.Fatalf("standalone rolling binary not supported: %v", err)
	}

	service.current.Version = "0.1.1~git.20261001.42.12345678"
	if err := service.Supported(); err == nil {
		t.Fatal("package build was supported")
	}

	service.current.Version = rollingVersion
	var checkedExe string
	service.externalAssets = func(exe string) (string, bool) {
		checkedExe = exe
		return "/usr/share/enclave", true
	}
	if err := service.Supported(); err == nil {
		t.Fatal("binary with external assets was supported")
	}
	if checkedExe != "/opt/enclave/enclave" {
		t.Fatalf("looked for assets of %q, want the running executable", checkedExe)
	}

	service.externalAssets = func(string) (string, bool) { return "", false }
	service.goos = "windows"
	if err := service.Supported(); err == nil {
		t.Fatal("windows was supported")
	}
}

func TestInstallRemovesStaleUpdateDownloads(t *testing.T) {
	server := newTestRelease([]byte("new enclave binary")).serve(t)
	target := writeExecutable(t, 0o755)
	dir := filepath.Dir(target)
	stale := filepath.Join(dir, ".enclave-update-stale")
	recent := filepath.Join(dir, ".enclave-update-recent")
	for _, path := range []string{stale, recent} {
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleUpdateTempAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := newTestService(server, target).Install(context.Background(), testCommit); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale download survived: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent download, possibly another install's, was removed: %v", err)
	}
}

func TestArtifactNameRejectsWindowsLauncher(t *testing.T) {
	if _, err := artifactName("windows", "amd64"); err == nil {
		t.Fatal("artifactName accepted the Windows launcher archive")
	}
}
