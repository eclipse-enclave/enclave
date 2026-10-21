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

// testRelease serves a rolling channel directory. Its handlers can be swapped
// per test to simulate a broken or moving release.
type testRelease struct {
	latest    http.HandlerFunc
	checksums http.HandlerFunc
	binary    http.HandlerFunc
}

func newTestRelease(binary []byte) *testRelease {
	digest := sha256.Sum256(binary)
	return &testRelease{
		latest: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintln(w, testCommit)
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
		case "/latest":
			r.latest(w, req)
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
	service.sources[model.UpdateChannelRolling] = downloadSource{fetcher: service.fetcher, channelURL: server.URL}
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
		"not hex": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintln(w, "not-a-commit")
		},
		"empty": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintln(w, "")
		},
		"error page": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintln(w, "<html><body>Moved</body></html>")
		},
		"missing release": func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			release := newTestRelease(nil)
			release.latest = handler
			service := newTestService(release.serve(t), "")
			if _, err := service.Check(context.Background()); err == nil {
				t.Fatal("Check accepted a bad rolling reference")
			}
		})
	}
}

func TestCheckReferenceSHALengths(t *testing.T) {
	for _, length := range []int{6, 7, 8, 39, 40, 41, 42, 63, 64, 65} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			release := newTestRelease(nil)
			sha := strings.Repeat("a", length)
			release.latest = func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintln(w, sha)
			}
			service := newTestService(release.serve(t), "")
			status, err := service.Check(context.Background())
			if length != 40 && length != 64 {
				if err == nil {
					t.Fatal("Check accepted a non-full-length SHA")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if status.Latest != sha {
				t.Fatalf("Latest = %q, want %q", status.Latest, sha)
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
				r.latest = func(w http.ResponseWriter, _ *http.Request) {
					commit := testCommit
					if lookups.Add(1) > 1 {
						commit = otherCommit
					}
					_, _ = fmt.Fprintln(w, commit)
				}
			},
			wantErr: "moved from 1234567 to abcdef0",
		},
		{
			name: "release moved between checksum and binary",
			setup: func(r *testRelease, _ *Service) {
				// The release moves once Install has confirmed it and started
				// downloading.
				r.binary = func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "republished binary") }
				var lookups atomic.Int32
				r.latest = func(w http.ResponseWriter, _ *http.Request) {
					commit := testCommit
					if lookups.Add(1) > 1 {
						commit = otherCommit
					}
					_, _ = fmt.Fprintln(w, commit)
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

// serveStableReleases publishes the stable layout: a latest file naming the
// newest version beside one directory per release.
func serveStableReleases(t *testing.T, latest string, binary []byte) *httptest.Server {
	t.Helper()
	digest := sha256.Sum256(binary)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/releases/latest":
			_, _ = fmt.Fprintln(w, latest)
		case "/releases/1.2.0/checksums.txt":
			_, _ = fmt.Fprintf(w, "%x  %s\n", digest, testArtifact)
		case "/releases/1.2.0/" + testArtifact:
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestStableChannelFollowsVersionedReleases(t *testing.T) {
	newBinary := []byte("stable enclave binary")
	server := serveStableReleases(t, "1.2.0", newBinary)
	target := writeExecutable(t, 0o755)
	service := newTestService(server, target)
	service.sources[model.UpdateChannelStable] = downloadSource{fetcher: service.fetcher, channelURL: server.URL + "/releases", stable: true}
	service.buildChannel = model.UpdateChannelStable
	service.channel = model.UpdateChannelStable
	service.current = buildinfo.Info{Version: "1.2.0", Commit: "abcdef0"}

	status, err := service.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.UpdateAvailable || status.Latest != "1.2.0" || status.Current != "1.2.0" {
		t.Fatalf("status = %+v, want 1.2.0 current", status)
	}

	// Newer builds stay: a release installed by hand before the pointer
	// moved, or a pre-release, which the pointer never names.
	for _, newer := range []string{"1.3.0", "1.10.0", "1.2.1", "1.3.0-rc.1", "2.0.0-beta.2"} {
		service.current.Version = newer
		status, err = service.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.UpdateAvailable {
			t.Fatalf("status = %+v for current %s, want no downgrade to 1.2.0", status, newer)
		}
	}
	assertUnchanged(t, target)

	for _, older := range []string{"1.1.0", "1.2.0-rc.1", "1.2.0-rc.10", "1.1.10", "0.9.0", "unknown", "1.1.0-3-gabc1234", "", "01.1.0", "1.1.00"} {
		service.current.Version = older
		status, err = service.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !status.UpdateAvailable {
			t.Fatalf("status = %+v for current %q, want an available update", status, older)
		}
	}
	service.current.Version = "1.1.0"
	status, err = service.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, replaced, err := service.Install(context.Background(), status.Latest); err != nil || !replaced {
		t.Fatalf("Install = %v, %v; want the release installed", replaced, err)
	}
	if got, _ := os.ReadFile(target); string(got) != string(newBinary) {
		t.Fatalf("installed contents = %q, want the stable binary", got)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            int
	}{
		{"1.2.0", "1.2.0", 0},
		{"v1.2.0", "1.2.0", 0},
		{"1.2.0", "1.10.0", -1},
		{"1.10.0", "1.2.0", 1},
		{"1.2.0-rc.1", "1.2.0", -1},
		{"1.2.0", "1.2.0-rc.1", 1},
		{"1.2.0-rc.2", "1.2.0-rc.10", -1},
		{"1.2.0-alpha.1", "1.2.0-beta.1", -1},
		{"1.2.0-beta.3", "1.2.0-rc.1", -1},
		{"1.2.0-rc.1", "1.2.0-alpha.9", 1},
		{"1.2.0-rc.01", "1.2.0-rc.1", -1},
		{"100000000000000000000.0.0", "99999999999999999999.0.0", 1},
		{"1.2.0-rc.100000000000000000000", "1.2.0-rc.99999999999999999999", 1},
		{"unknown", "1.2.0", -1},
		{"1.2.0-3-gabc1234", "1.2.0", -1},
		{"", "0.0.1", -1},
	} {
		if got := compareVersions(tc.current, tc.latest); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestStableChannelRejectsBadVersions(t *testing.T) {
	for _, latest := range []string{"", "rolling", "1.2", "../rolling", "1.2.0/x", "<html>", "v1.2.0", "01.2.0", "1.02.0", "1.2.00", "1.2.0-rc1", "1.2.0-rc-1", "1.2.0-alpha", "1.2.0-rc.01", "1.2.0-rc.1.1"} {
		t.Run(latest, func(t *testing.T) {
			server := serveStableReleases(t, latest, nil)
			service := newTestService(server, "")
			service.sources[model.UpdateChannelStable] = downloadSource{fetcher: service.fetcher, channelURL: server.URL + "/releases", stable: true}
			service.channel = model.UpdateChannelStable
			if _, err := service.Check(context.Background()); err == nil {
				t.Fatalf("Check accepted latest %q", latest)
			}
		})
	}
}

func TestStableAssetURLsNameTheRelease(t *testing.T) {
	src := newStableSource(fetcher{}, "https://download.eclipse.org/enclave")
	if got, want := src.assetURL("1.2.0", checksumsFilename), "https://download.eclipse.org/enclave/releases/1.2.0/checksums.txt"; got != want {
		t.Fatalf("assetURL = %q, want %q", got, want)
	}
	rolling := newRollingSource(fetcher{}, "https://download.eclipse.org/enclave")
	if got, want := rolling.assetURL(testCommit, testArtifact), "https://download.eclipse.org/enclave/rolling/"+testArtifact; got != want {
		t.Fatalf("assetURL = %q, want %q", got, want)
	}
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
	if err := service.Writable(testCommit); err != nil {
		t.Fatalf("writable directory reported: %v", err)
	}

	var checked string
	service.writableDir = func(dir string) bool {
		checked = dir
		return false
	}
	err := service.Writable(testCommit)
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("Writable error = %v, want ErrNotWritable", err)
	}
	if checked != "/opt/my tools" {
		t.Fatalf("checked %q, want the executable's directory", checked)
	}
	for _, want := range []string{
		"download " + server.URL + "/" + testArtifact + " and " + server.URL + "/checksums.txt",
		"with: sha256sum --check --ignore-missing checksums.txt && sudo install " + testArtifact + " '/opt/my tools/enclave'",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Writable error %q lacks %q", err, want)
		}
	}

	service.goos = "darwin"
	want := "with: shasum -a 256 --check --ignore-missing checksums.txt && xattr -c enclave-darwin-amd64 && sudo install enclave-darwin-amd64 '/opt/my tools/enclave'"
	if err := service.Writable(testCommit); err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("Writable error on macOS = %v, want it to end with %q", err, want)
	}
}

// A sticky directory such as /tmp lets anyone create files but refuses to
// replace another user's, so the final rename can be denied after Writable
// and CreateTemp both succeeded. Revoking write access during the download
// produces the same denial without a second user.
func TestInstallReportsDeniedReplacementAsNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can replace any file")
	}
	newBinary := []byte("new enclave binary")
	release := newTestRelease(newBinary)
	target := writeExecutable(t, 0o755)
	dir := filepath.Dir(target)
	serveBinary := release.binary
	release.binary = func(w http.ResponseWriter, r *http.Request) {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Error(err)
		}
		serveBinary(w, r)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, _, err := newTestService(release.serve(t), target).Install(context.Background(), testCommit)
	if !errors.Is(err, ErrNotWritable) || !strings.Contains(err.Error(), "sudo install") {
		t.Fatalf("Install error = %v, want ErrNotWritable with the reinstall command", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old binary" {
		t.Fatalf("executable changed after a denied replacement: %q", got)
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
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "not found",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, "<html><body>Not Found</body></html>")
			},
			want: "404 Not Found",
		},
		{
			name: "forbidden",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			want: "403 Forbidden",
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
