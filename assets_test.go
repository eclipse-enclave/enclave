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
	"slices"
	"strings"
	"testing"
)

func TestEmbeddedAssetInventory(t *testing.T) {
	for _, name := range []string{
		".dockerignore",
		"Dockerfile",
		"Dockerfile.gateway",
		"Dockerfile.a2ahub",
		"entrypoint.sh",
		"gateway-entrypoint.sh",
		"a2ahub-entrypoint.sh",
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

	var buildInputs []string
	for _, list := range []string{
		"internal/gateway/gateway_proxy_build_inputs.txt",
		"internal/app/a2a_agent_build_inputs.txt",
		"internal/gateway/a2ahub_build_inputs.txt",
	} {
		inputList, err := os.ReadFile(list)
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
				t.Errorf("%s entry %s: %v", list, name, err)
				continue
			}
			buildInputs = append(buildInputs, name)
			assertEmbeddedTreeMatchesCheckout(t, name)
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
	}
	assertEmbeddedGoSourcesAreBuildInputs(t, buildInputs)

	for _, tree := range []string{"docs", "extensions", "runtime-assets"} {
		assertEmbeddedTreeMatchesCheckout(t, tree)
	}
}

// assertEmbeddedGoSourcesAreBuildInputs keeps the embed list from carrying Go sources that no image build stages.
func assertEmbeddedGoSourcesAreBuildInputs(t *testing.T, buildInputs []string) {
	t.Helper()
	for _, root := range []string{"cmd", "internal"} {
		err := fs.WalkDir(files, root, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			listed := slices.ContainsFunc(buildInputs, func(input string) bool {
				return name == input || strings.HasPrefix(name, input+"/")
			})
			if !listed {
				t.Errorf("embedded %s is in no build-input list", name)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk embedded %s: %v", root, err)
		}
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
