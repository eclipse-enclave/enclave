// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !enclave_no_embed

package assets

import (
	"bufio"
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestEmbeddedAssetInventory(t *testing.T) {
	for _, name := range []string{
		".dockerignore",
		"Dockerfile",
		"Dockerfile.gateway",
		"entrypoint.sh",
		"gateway-entrypoint.sh",
		"LICENSE.md",
		"NOTICE.md",
		"docs/README.md",
		"extensions/tools/claude/spec.yaml",
		"runtime-assets/gateway-allowlists/base.conf",
	} {
		if _, err := fs.Stat(files, name); err != nil {
			t.Errorf("required embedded asset %s: %v", name, err)
		}
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", ".git"} {
		if _, err := fs.Stat(files, name); !os.IsNotExist(err) {
			t.Errorf("repository-only path %s was embedded", name)
		}
	}

	inputList, err := os.ReadFile("internal/gateway/gateway_proxy_build_inputs.txt")
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(inputList)))
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if name == "" || strings.HasPrefix(name, "#") {
			continue
		}
		if _, err := fs.Stat(files, name); err != nil {
			t.Errorf("gateway proxy build input %s: %v", name, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	for _, tree := range []string{"docs", "extensions", "runtime-assets"} {
		assertEmbeddedTreeMatchesCheckout(t, tree)
	}
}

func assertEmbeddedTreeMatchesCheckout(t *testing.T, tree string) {
	t.Helper()
	err := fs.WalkDir(os.DirFS("."), tree, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			t.Errorf("asset source %s is a symlink and cannot be embedded", name)
			return nil
		}
		embeddedEntry, err := fs.Stat(files, name)
		if err != nil {
			t.Errorf("checkout asset %s is not embedded: %v", name, err)
			return nil
		}
		if embeddedEntry.IsDir() != entry.IsDir() {
			t.Errorf("embedded asset type differs for %s", name)
		}
		if entry.IsDir() {
			return nil
		}
		checkoutContent, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		embeddedContent, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		if string(embeddedContent) != string(checkoutContent) {
			t.Errorf("embedded content differs for %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("compare embedded tree %s: %v", tree, err)
	}
}
