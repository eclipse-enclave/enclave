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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"enclave/internal/buildinfo"
	"enclave/internal/model"
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

// newTestService returns a rolling binary updating from a rolling channel
// that server publishes.
func newTestService(server *httptest.Server, target string) *Service {
	service := New(model.UpdateChannelRolling)
	service.sources[model.UpdateChannelRolling] = rollingSource{fetcher: service.fetcher, refURL: server.URL + "/ref", assetsURL: server.URL}
	service.buildChannel = model.UpdateChannelRolling
	service.goos = "linux"
	service.goarch = "amd64"
	service.current = buildinfo.Info{Version: "rolling", Commit: "abcdef0"}
	service.executablePath = func() (string, error) { return target, nil }
	service.externalAssets = func(string) (string, bool) { return "", false }
	service.writableDir = func(string) bool { return true }
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

	installed, replaced, err := newTestService(server, target).Install(context.Background(), testCommit)
	if err != nil {
		t.Fatal(err)
	}
	if installed != target || !replaced {
		t.Fatalf("Install = %q, %v; want %q replaced", installed, replaced, target)
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
				// The release moves once Install has confirmed it and started
				// downloading.
				var lookups atomic.Int32
				r.ref = func(w http.ResponseWriter, _ *http.Request) {
					commit := testCommit
					if lookups.Add(1) > 1 {
						commit = otherCommit
					}
					_, _ = fmt.Fprintf(w, `{"object":{"sha":%q,"type":"commit"}}`, commit)
				}
			},
			wantErr: "moved from 1234567 to abcdef0",
		},
		{
			name: "stalled download",
			setup: func(r *testRelease, s *Service) {
				s.fetcher.idleTimeout = 50 * time.Millisecond
				r.binary = func(w http.ResponseWriter, req *http.Request) {
					w.Header().Set("Content-Length", "1000")
					_, _ = fmt.Fprint(w, "partial")
					w.(http.Flusher).Flush()
					<-req.Context().Done()
				}
			},
			wantErr: "no data received for 50ms",
		},
		{
			name: "source build",
			setup: func(_ *testRelease, s *Service) {
				s.buildChannel = ""
				s.current.Version = "0.1.0"
			},
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

			_, _, err := service.Install(context.Background(), testCommit)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Install error = %v, want it to contain %q", err, tc.wantErr)
			}
			assertUnchanged(t, target)
		})
	}
}

func TestSupportedRequiresStandaloneReleaseBinary(t *testing.T) {
	service := newTestService(newTestRelease(nil).serve(t), "/opt/enclave/enclave")
	if err := service.Supported(); err != nil {
		t.Fatalf("standalone rolling binary not supported: %v", err)
	}

	service.buildChannel = ""
	if err := service.Supported(); err == nil {
		t.Fatal("package build without a channel stamp was supported")
	}
	if _, err := service.Check(context.Background()); err == nil {
		t.Fatal("Check accepted a package build")
	}

	service.buildChannel = model.UpdateChannelRolling
	service.channel = "nightly"
	if err := service.Supported(); err == nil {
		t.Fatal("unknown update channel was supported")
	}

	service.channel = model.UpdateChannelRolling
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

	if _, _, err := newTestService(server, target).Install(context.Background(), testCommit); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale download survived: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent download, possibly another install's, was removed: %v", err)
	}
}

func TestTargetChannelDefaultsToBuildChannel(t *testing.T) {
	for _, tc := range []struct{ configured, build, want string }{
		{configured: "", build: "rolling", want: "rolling"},
		{configured: "", build: "stable", want: "stable"},
		{configured: "stable", build: "rolling", want: "stable"},
		{configured: "rolling", build: "stable", want: "rolling"},
		{configured: "", build: "", want: ""},
	} {
		if got := targetChannel(tc.configured, tc.build); got != tc.want {
			t.Errorf("targetChannel(%q, %q) = %q, want %q", tc.configured, tc.build, got, tc.want)
		}
	}
}

func TestStableChannelHasNoSourceYet(t *testing.T) {
	target := writeExecutable(t, 0o755)
	service := newTestService(newTestRelease(nil).serve(t), target)
	service.channel = model.UpdateChannelStable

	if err := service.Supported(); err != nil {
		t.Fatalf("a rolling binary cannot select the stable channel: %v", err)
	}
	if _, err := service.Check(context.Background()); !errors.Is(err, ErrChannelUnavailable) {
		t.Fatalf("Check error = %v, want ErrChannelUnavailable", err)
	}
	if _, _, err := service.Install(context.Background(), testCommit); !errors.Is(err, ErrChannelUnavailable) {
		t.Fatalf("Install error = %v, want ErrChannelUnavailable", err)
	}
	assertUnchanged(t, target)
}

func TestSwitchingChannelsInstallsTheOtherChannelsBuild(t *testing.T) {
	newBinary := []byte("stable enclave binary")
	server := newTestRelease(newBinary).serve(t)
	target := writeExecutable(t, 0o755)
	service := newTestService(server, target)
	// The stable binary is built from the same commit as the running rolling
	// one, yet it is a different binary and must still be offered.
	service.current.Commit = testCommit
	service.sources[model.UpdateChannelStable] = service.sources[model.UpdateChannelRolling]
	service.channel = model.UpdateChannelStable

	status, err := service.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || status.Channel != model.UpdateChannelStable {
		t.Fatalf("status = %+v, want an available stable update", status)
	}
	if status.Current != testCommit {
		t.Fatalf("current build labelled %q, want its commit", status.Current)
	}
	if _, _, err := service.Install(context.Background(), status.Latest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != string(newBinary) {
		t.Fatalf("installed contents = %q, want the stable binary", got)
	}
}

func TestArtifactNameRejectsWindowsLauncher(t *testing.T) {
	if _, err := artifactName("windows", "amd64"); err == nil {
		t.Fatal("artifactName accepted the Windows launcher archive")
	}
}

func TestInstallLeavesMatchingExecutableAlone(t *testing.T) {
	newBinary := []byte("new enclave binary")
	release := newTestRelease(newBinary)
	var downloads atomic.Int32
	serveBinary := release.binary
	release.binary = func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		serveBinary(w, r)
	}
	target := filepath.Join(t.TempDir(), "enclave")
	if err := os.WriteFile(target, newBinary, 0o755); err != nil {
		t.Fatal(err)
	}

	// The running build is older, but another update already replaced the
	// executable on disk.
	path, replaced, err := newTestService(release.serve(t), target).Install(context.Background(), testCommit)
	if err != nil {
		t.Fatal(err)
	}
	if path != target || replaced {
		t.Fatalf("Install = %q, %v; want %q left alone", path, replaced, target)
	}
	if downloads.Load() != 0 {
		t.Fatalf("downloaded the binary %d times, want none", downloads.Load())
	}
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 1 {
		t.Fatalf("Install left files behind: %v", entries)
	}
}

func TestInstallIntoUnwritableDirectoryFailsOffline(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write any directory")
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	target := writeExecutable(t, 0o755)
	dir := filepath.Dir(target)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, _, err := newTestService(server, target).Install(context.Background(), testCommit)
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("Install error = %v, want ErrNotWritable", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d requests before finding the directory unwritable", requests.Load())
	}
	assertUnchanged(t, target)
}

func TestWritableNamesTheReinstallCommand(t *testing.T) {
	server := newTestRelease(nil).serve(t)
	service := newTestService(server, "/opt/my tools/enclave")
	if err := service.Writable(); err != nil {
		t.Fatalf("writable directory reported: %v", err)
	}

	var checked string
	service.writableDir = func(dir string) bool {
		checked = dir
		return false
	}
	err := service.Writable()
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("Writable error = %v, want ErrNotWritable", err)
	}
	if checked != "/opt/my tools" {
		t.Fatalf("checked %q, want the executable's directory", checked)
	}
	for _, want := range []string{
		server.URL + "/" + testArtifact,
		"sudo install " + testArtifact + " '/opt/my tools/enclave'",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Writable error %q lacks %q", err, want)
		}
	}
}

func TestShellArgQuotesOnlyWhenNeeded(t *testing.T) {
	for path, want := range map[string]string{
		"/usr/local/bin/enclave":   "/usr/local/bin/enclave",
		"/home/me/.local/bin/x_1+": "/home/me/.local/bin/x_1+",
		"/opt/my tools/enclave":    "'/opt/my tools/enclave'",
		"/opt/$HOME/enclave":       "'/opt/$HOME/enclave'",
		"/opt/it's/enclave":        `'/opt/it'"'"'s/enclave'`,
		"/opt/é/enclave":           "'/opt/é/enclave'",
	} {
		if got := shellArg(path); got != want {
			t.Errorf("shellArg(%q) = %s, want %s", path, got, want)
		}
	}
}

func TestFailedRequestsExplainThemselves(t *testing.T) {
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "rate limit",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprint(w, `{"message":"API rate limit exceeded for 203.0.113.7."}`)
			},
			want: "403 Forbidden: GitHub API rate limit exceeded; it resets at " + reset.Format("15:04"),
		},
		{
			name: "github message",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit.\n\u001b[31mRetry later."}`)
			},
			want: "403 Forbidden: You have exceeded a secondary rate limit. [31mRetry later.",
		},
		{
			name: "plain body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, "Not Found")
			},
			want: "404 Not Found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			t.Cleanup(server.Close)
			_, err := New(model.UpdateChannelRolling).fetcher.get(context.Background(), server.URL)
			if err == nil || !strings.HasSuffix(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to end with %q", err, tc.want)
			}
		})
	}
}
