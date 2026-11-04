// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestProjectEnvRefusesSymlinksAndDirectories(t *testing.T) {
	for _, kind := range []string{"external symlink", "internal symlink", "directory", "missing"} {
		t.Run(kind, func(t *testing.T) {
			project := t.TempDir()
			envPath := filepath.Join(project, ".env")
			switch kind {
			case "external symlink", "internal symlink":
				root := project
				if kind == "external symlink" {
					root = t.TempDir()
				}
				target := filepath.Join(root, "secrets.env")
				if err := os.WriteFile(target, []byte("KEY=synthetic-secret\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, envPath); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(envPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			r := &Runtime{project: model.Project{Dir: project}}
			if values := r.envFileValues(); len(values) != 0 {
				t.Fatalf("loaded .env that is not a regular file: %v", values)
			}
		})
	}
}

func TestProjectEnvReservesProxyAndTLSTrustKeysAndKeepsOrdinaryValues(t *testing.T) {
	project := t.TempDir()
	var content strings.Builder
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "ALL_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "GIT_SSL_CAINFO"} {
		content.WriteString(key + "=project-value\n" + strings.ToLower(key) + "=project-value\n")
	}
	large := strings.Repeat("x", 70*1024)
	content.WriteString("# comment\nexport APP=first\nAPP=last\nQUOTED='hello world'\nLONG=" + large + "\n")
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{project: model.Project{Dir: project}}
	var got []string
	warnings := captureStderr(t, func() { got = r.envFileValues() })
	want := []string{"APP=first", "APP=last", "QUOTED=hello world", "LONG=" + large}
	if !slices.Equal(got, want) {
		t.Fatalf(".env parse/filter mismatch: assignments=%d", len(got))
	}
	for _, key := range []string{"HTTP_PROXY", "no_proxy", "git_ssl_cainfo"} {
		if !strings.Contains(warnings, "Ignoring project .env key \""+key+"\": reserved for Enclave") {
			t.Fatalf("no warning names %s: %q", key, warnings)
		}
	}
	if strings.Contains(warnings, "project-value") {
		t.Fatalf("warnings carry a value: %q", warnings)
	}
}
