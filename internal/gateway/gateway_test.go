// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

func TestEnsureExistingGatewayImageWith(t *testing.T) {
	t.Run("returns nil when image exists", func(t *testing.T) {
		err := ensureExistingGatewayImageWith(model.Profile{Name: "codex"}, func(context.Context, string) (bool, error) {
			return true, nil
		})
		if err != nil {
			t.Fatalf("ensureExistingGatewayImageWith returned error: %v", err)
		}
	})

	t.Run("returns actionable error when image is missing", func(t *testing.T) {
		err := ensureExistingGatewayImageWith(model.Profile{Name: "codex"}, func(context.Context, string) (bool, error) {
			return false, nil
		})
		if err == nil {
			t.Fatal("expected missing-image error, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "does not exist locally") || !strings.Contains(msg, "--no-rebuild") || !strings.Contains(msg, "--allow-all-network") {
			t.Fatalf("unexpected error message: %q", msg)
		}
	})

	t.Run("propagates inspect errors", func(t *testing.T) {
		wantErr := errors.New("inspect failed")
		err := ensureExistingGatewayImageWith(model.Profile{Name: "codex"}, func(context.Context, string) (bool, error) {
			return false, wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected wrapped inspect error %v, got %v", wantErr, err)
		}
	})
}

func TestGatewayLabelsHashWorkspaceIDBeforeProjectDir(t *testing.T) {
	labels := gatewayLabels(StartConfig{
		Profile:       model.Profile{Name: "codex"},
		ContainerName: "enclave-codex-abc123abc123",
		ProjectDir:    "/workspace/project",
		WorkspaceID:   "/real/project",
		ProjectHash:   "abc123abc123",
	})

	want := util.WorkspaceIdentityHash("/real/project", "/workspace/project")
	if labels[model.GatewayLabelWorkspaceHash] != want {
		t.Fatalf("workspace hash = %q, want %q", labels[model.GatewayLabelWorkspaceHash], want)
	}
}

func TestCalculateAllowlistHashIgnoresEscapingInclude(t *testing.T) {
	root := t.TempDir()
	allowlistsDir := filepath.Join(root, "allowlists")
	allowlistPath := filepath.Join(allowlistsDir, "allowlist.conf")
	outsidePath := filepath.Join(root, "outside.conf")
	writeGatewayFile(t, allowlistPath, "conf-file=../outside.conf\n", 0o644)
	writeGatewayFile(t, outsidePath, "server=/first.example/8.8.8.8\n", 0o644)

	first, err := calculateAllowlistHash(allowlistPath, allowlistsDir)
	if err != nil {
		t.Fatal(err)
	}
	writeGatewayFile(t, outsidePath, "server=/second.example/8.8.8.8\n", 0o644)
	second, err := calculateAllowlistHash(allowlistPath, allowlistsDir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("escaping include affected allowlist hash")
	}
}

func TestPrepareGatewayContextUsesAppRootWhenAllowlistIsInside(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app")
	paths := gatewayTestPaths(t, appRoot)
	allowlistPath := filepath.Join(appRoot, "extensions", "tools", "claude", model.AllowlistFilename)
	writeGatewayFile(t, allowlistPath, "server=/example.com/8.8.8.8\n", 0o644)

	contextDir, allowlistRel, cleanup, err := prepareGatewayContext(paths, allowlistPath)
	if err != nil {
		t.Fatalf("prepareGatewayContext: %v", err)
	}
	defer cleanup()

	if contextDir != appRoot {
		t.Fatalf("expected contextDir=%s, got %s", appRoot, contextDir)
	}
	expectedRel := filepath.ToSlash(filepath.Join("extensions", "tools", "claude", model.AllowlistFilename))
	if allowlistRel != expectedRel {
		t.Fatalf("allowlistRel=%q want=%q", allowlistRel, expectedRel)
	}
}

func TestPrepareGatewayContextStagesGatewayBuildInputsForExternalAllowlist(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app")
	paths := gatewayTestPaths(t, appRoot)

	externalAllowlist := filepath.Join(tmp, "user-ext", "tools", "claude", model.AllowlistFilename)
	writeGatewayFile(t, externalAllowlist, "server=/example.com/8.8.8.8\n", 0o644)

	contextDir, allowlistRel, cleanup, err := prepareGatewayContext(paths, externalAllowlist)
	if err != nil {
		t.Fatalf("prepareGatewayContext: %v", err)
	}

	if contextDir == appRoot {
		t.Fatalf("expected staged context for external allowlist, got %s", contextDir)
	}
	if allowlistRel != "runtime-assets/gateway-allowlists/__user_allowlist.conf" {
		t.Fatalf("unexpected allowlistRel: %s", allowlistRel)
	}
	if _, err := os.Stat(filepath.Join(contextDir, "Dockerfile.gateway")); err != nil {
		t.Fatalf("missing staged Dockerfile.gateway: %v", err)
	}
	if _, err := os.Stat(filepath.Join(contextDir, "gateway-entrypoint.sh")); err != nil {
		t.Fatalf("missing staged gateway-entrypoint.sh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(contextDir, "runtime-assets", "net.sh")); err != nil {
		t.Fatalf("missing staged net helper: %v", err)
	}
	for _, relPath := range gatewayProxyBuildInputs {
		if _, err := os.Stat(filepath.Join(contextDir, filepath.FromSlash(relPath))); err != nil {
			t.Fatalf("missing staged gateway build input %s: %v", relPath, err)
		}
	}
	if _, err := os.Stat(filepath.Join(contextDir, "runtime-assets", "gateway-allowlists", "base.conf")); err != nil {
		t.Fatalf("missing staged built-in allowlists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(contextDir, filepath.FromSlash(allowlistRel))); err != nil {
		t.Fatalf("missing staged external allowlist: %v", err)
	}

	cleanup()
	if _, err := os.Stat(contextDir); !os.IsNotExist(err) {
		t.Fatalf("expected cleanup to remove staged context %s", contextDir)
	}
}

func gatewayTestPaths(t *testing.T, appRoot string) model.Paths {
	t.Helper()
	writeGatewayFile(t, filepath.Join(appRoot, "Dockerfile.gateway"), "FROM alpine:3.23\n", 0o644)
	writeGatewayFile(t, filepath.Join(appRoot, "gateway-entrypoint.sh"), "#!/bin/sh\n", 0o755)
	writeGatewayFile(t, filepath.Join(appRoot, "runtime-assets", "net.sh"), "enclave_ensure_local_resolver() { :; }\n", 0o644)
	writeGatewayFile(t, filepath.Join(appRoot, "runtime-assets", "gateway-allowlists", "base.conf"), "server=/example.com/8.8.8.8\n", 0o644)
	writeGatewayProxyBuildInputsFixture(t, appRoot)
	return model.Paths{
		AppRoot:           appRoot,
		GatewayDockerfile: filepath.Join(appRoot, "Dockerfile.gateway"),
		GatewayEntrypoint: filepath.Join(appRoot, "gateway-entrypoint.sh"),
		AllowlistsDir:     filepath.Join(appRoot, "runtime-assets", "gateway-allowlists"),
	}
}

func writeGatewayProxyBuildInputsFixture(t *testing.T, appRoot string) {
	t.Helper()
	for _, relPath := range gatewayProxyBuildInputs {
		fullPath := filepath.Join(appRoot, filepath.FromSlash(relPath))
		switch relPath {
		case "go.mod":
			writeGatewayFile(t, fullPath, "module enclave\n\ngo 1.24.0\n", 0o644)
		case "go.sum":
			writeGatewayFile(t, fullPath, "", 0o644)
		default:
			packageName := filepath.Base(filepath.FromSlash(relPath))
			if strings.HasPrefix(relPath, "cmd/") {
				packageName = "main"
			}
			writeGatewayFile(t, filepath.Join(fullPath, "placeholder.go"), "package "+packageName+"\n", 0o644)
		}
	}
}

func writeGatewayFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestHasGatewayLogLineMatchesWholeLine(t *testing.T) {
	logs := "[enclave-gateway] Gateway ready\nother line\n"
	if !HasLogLine(logs, "Gateway ready") {
		t.Fatal("expected marker to match gateway log line")
	}
}

func TestHasGatewayLogLineDoesNotMatchEmbeddedSubstring(t *testing.T) {
	logs := "[enclave-gateway] prefix Gateway ready suffix\n"
	if HasLogLine(logs, "Gateway ready") {
		t.Fatal("expected embedded substring not to match marker")
	}
}

func TestValidateStartConfig(t *testing.T) {
	tlsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(tlsRoot, "ca.crt"), []byte("cert"), 0o644); err != nil {
		t.Fatalf("write ca.crt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tlsRoot, "ca.key"), []byte("key"), 0o600); err != nil {
		t.Fatalf("write ca.key: %v", err)
	}
	gatewayDir := t.TempDir()

	if err := validateStartConfig(StartConfig{
		GatewayConfigDir: gatewayDir,
		TLSRootDir:       tlsRoot,
	}); err != nil {
		t.Fatalf("validateStartConfig() error = %v", err)
	}
	if err := validateStartConfig(StartConfig{
		GatewayConfigDir: gatewayDir,
		TLSRootDir:       tlsRoot,
		NetworkLogMode:   model.NetworkLogRequests,
	}); err != nil {
		t.Fatalf("validateStartConfig() with network log mode error = %v", err)
	}

	err := validateStartConfig(StartConfig{
		GatewayConfigDir: filepath.Join(t.TempDir(), "missing"),
	})
	if err == nil {
		t.Fatal("expected missing gateway config dir to fail validation")
	}

	err = validateStartConfig(StartConfig{NetworkLogMode: "invalid"})
	if err == nil {
		t.Fatal("expected invalid network log mode to fail validation")
	}
}

func TestGatewayAllowlistOverridePathResolution(t *testing.T) {
	home := t.TempDir()
	profile := model.Profile{Name: "codex"}
	globalDir := config.HostGatewayAllowlistsDir(home)
	if err := os.MkdirAll(globalDir, 0o750); err != nil {
		t.Fatalf("mkdir global override dir: %v", err)
	}
	globalPath := filepath.Join(globalDir, "codex.conf")
	if err := os.WriteFile(globalPath, []byte("server=/global.example.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatalf("write global override: %v", err)
	}

	overridePath, scope := config.GatewayAllowlistOverridePath(profile, home, "p1")
	if overridePath != globalPath || scope != "global" {
		t.Fatalf("override = (%q, %q), want (%q, %q)", overridePath, scope, globalPath, "global")
	}

	projectDir := config.HostProjectGatewayAllowlistsDir(home, "p1")
	if err := os.MkdirAll(projectDir, 0o750); err != nil {
		t.Fatalf("mkdir project override dir: %v", err)
	}
	projectPath := filepath.Join(projectDir, "codex.conf")
	if err := os.WriteFile(projectPath, []byte("server=/project.example.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatalf("write project override: %v", err)
	}

	overridePath, scope = config.GatewayAllowlistOverridePath(profile, home, "p1")
	if overridePath != projectPath || scope != "project-specific" {
		t.Fatalf("override = (%q, %q), want (%q, %q)", overridePath, scope, projectPath, "project-specific")
	}
}

func TestNeedsRebuildHashChangesWhenInputsChange(t *testing.T) {
	root := t.TempDir()
	paths := gatewayTestPaths(t, root)
	allowlistPath := filepath.Join(paths.AllowlistsDir, "codex.conf")
	if err := os.WriteFile(allowlistPath, []byte("server=/example.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatalf("write allowlist: %v", err)
	}

	_, firstHash, err := needsRebuild(paths, model.Profile{Name: "codex"}, allowlistPath)
	if err != nil {
		t.Fatalf("needsRebuild() first error = %v", err)
	}
	if err := os.WriteFile(allowlistPath, []byte("server=/changed.example.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatalf("rewrite allowlist: %v", err)
	}
	_, secondHash, err := needsRebuild(paths, model.Profile{Name: "codex"}, allowlistPath)
	if err != nil {
		t.Fatalf("needsRebuild() second error = %v", err)
	}
	if firstHash == secondHash {
		t.Fatalf("build hash did not change after allowlist update: %q", firstHash)
	}
}

func TestNeedsRebuildHashChangesWhenGatewayProxySourceChanges(t *testing.T) {
	root := t.TempDir()
	paths := gatewayTestPaths(t, root)
	allowlistPath := filepath.Join(paths.AllowlistsDir, "codex.conf")
	if err := os.WriteFile(allowlistPath, []byte("server=/example.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatalf("write allowlist: %v", err)
	}

	_, firstHash, err := needsRebuild(paths, model.Profile{Name: "codex"}, allowlistPath)
	if err != nil {
		t.Fatalf("needsRebuild() first error = %v", err)
	}

	sourceFile := filepath.Join(root, "internal", "gateway", "bundle", "placeholder.go")
	if err := os.WriteFile(sourceFile, []byte("package bundle\nconst sourceVersion = 2\n"), 0o644); err != nil {
		t.Fatalf("rewrite source file: %v", err)
	}

	_, secondHash, err := needsRebuild(paths, model.Profile{Name: "codex"}, allowlistPath)
	if err != nil {
		t.Fatalf("needsRebuild() second error = %v", err)
	}
	if firstHash == secondHash {
		t.Fatalf("build hash did not change after gateway proxy source update: %q", firstHash)
	}
}
