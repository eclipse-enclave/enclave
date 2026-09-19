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
	"testing"

	"enclave/internal/buildinfo"
)

func TestCheckComparesShortAndFullCommits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"object":{"sha":"1234567890abcdef1234567890abcdef12345678","type":"commit"}}`)
	}))
	defer server.Close()

	service := New()
	service.refURL = server.URL
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
			status, err := service.Check(context.Background(), buildinfo.Info{Commit: tc.current})
			if err != nil {
				t.Fatal(err)
			}
			if status.UpdateAvailable != tc.available {
				t.Fatalf("UpdateAvailable = %v, want %v", status.UpdateAvailable, tc.available)
			}
		})
	}
}

func TestInstallVerifiesAndAtomicallyReplacesExecutable(t *testing.T) {
	const artifact = "enclave-linux-amd64"
	newBinary := []byte("new enclave binary")
	digest := sha256.Sum256(newBinary)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			_, _ = fmt.Fprintf(w, "%x  %s\n", digest, artifact)
		case "/" + artifact:
			_, _ = w.Write(newBinary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "enclave")
	if err := os.WriteFile(target, []byte("old binary"), 0o751); err != nil {
		t.Fatal(err)
	}
	service := New()
	service.assetsURL = server.URL
	service.goos = "linux"
	service.goarch = "amd64"
	service.executablePath = func() (string, error) { return target, nil }

	installed, err := service.Install(context.Background())
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

func TestInstallChecksumMismatchPreservesExecutable(t *testing.T) {
	const artifact = "enclave-linux-amd64"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checksums.txt" {
			_, _ = fmt.Fprintf(w, "%064x  %s\n", 1, artifact)
			return
		}
		_, _ = fmt.Fprint(w, "tampered binary")
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "enclave")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	service := New()
	service.assetsURL = server.URL
	service.goos = "linux"
	service.goarch = "amd64"
	service.executablePath = func() (string, error) { return target, nil }

	if _, err := service.Install(context.Background()); err == nil {
		t.Fatal("Install succeeded with a mismatched checksum")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary" {
		t.Fatalf("executable changed after failed update: %q", got)
	}
}

func TestArtifactNameRejectsWindowsLauncher(t *testing.T) {
	if _, err := artifactName("windows", "amd64"); err == nil {
		t.Fatal("artifactName accepted the Windows launcher archive")
	}
}
