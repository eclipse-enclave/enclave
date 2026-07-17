// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

const claudeCredentialsFile = ".credentials.json"

func claudeCreds(expiresAt int) string {
	return `{"claudeAiOauth":{"expiresAt":` + strconv.Itoa(expiresAt) + `}}`
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}

// fakeJQDir provides a jq shim that extracts .claudeAiOauth.expiresAt, so the
// reconcile paths under test do not depend on a host jq installation.
func fakeJQDir(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available to provide test jq shim")
	}
	dir := t.TempDir()
	jqPath := filepath.Join(dir, "jq")
	script := `#!/bin/sh
file=""
for arg do
  file=$arg
done
exec ` + python + ` - "$file" <<'PY'
import json
import sys
try:
    with open(sys.argv[1], encoding="utf-8") as f:
        payload = json.load(f)
    value = (payload.get("claudeAiOauth") or {}).get("expiresAt")
except Exception:
    value = None
if value is not None:
    print(value)
PY
`
	if err := os.WriteFile(jqPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write jq shim: %v", err)
	}
	return dir
}
