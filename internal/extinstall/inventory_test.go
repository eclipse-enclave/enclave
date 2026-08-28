// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

func inventoryPaths(t *testing.T) model.Paths {
	t.Helper()
	root := t.TempDir()
	return model.Paths{
		ToolsDir:        filepath.Join(root, "extensions", "tools"),
		FeaturesDir:     filepath.Join(root, "extensions", "features"),
		UserToolsDir:    filepath.Join(root, "user", "tools"),
		UserFeaturesDir: filepath.Join(root, "user", "features"),
	}
}

func TestInventoryClassifiesSources(t *testing.T) {
	paths := inventoryPaths(t)
	writeFixture(t, filepath.Join(paths.FeaturesDir, "builtin-only", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: builtin-only\n", 0o644)
	writeFixture(t, filepath.Join(paths.FeaturesDir, "shadowed", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: shadowed\n", 0o644)
	writeFixture(t, filepath.Join(paths.UserFeaturesDir, "shadowed", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: shadowed\n", 0o644)
	writeFixture(t, filepath.Join(paths.UserFeaturesDir, "user-only", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: user-only\n", 0o644)

	inventory, err := Inventory(paths, model.KindFeature, nil, InventoryModifications)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if inventory["builtin-only"].Source != "builtin" {
		t.Errorf("builtin-only source = %q", inventory["builtin-only"].Source)
	}
	if inventory["shadowed"].Source != "override" {
		t.Errorf("shadowed source = %q", inventory["shadowed"].Source)
	}
	if inventory["user-only"].Source != "user" {
		t.Errorf("user-only source = %q", inventory["user-only"].Source)
	}
	for name, entry := range inventory {
		if entry.Origin != nil {
			t.Errorf("%s has provenance without a sidecar", name)
		}
	}
}

func TestInventoryReportsProvenanceAndModification(t *testing.T) {
	paths := inventoryPaths(t)
	extDir := filepath.Join(paths.UserFeaturesDir, "foo")
	writeFixture(t, filepath.Join(extDir, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: foo\n", 0o644)
	hash, err := TreeHash(extDir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	origin := Origin{
		SchemaVersion: OriginSchemaVersion,
		Kind:          string(model.KindFeature),
		Name:          "foo",
		Remote:        "https://github.com/acme/kits",
		Source:        "acme/kits",
		Ref:           "main",
		RefType:       RefTypeBranch,
		Commit:        "a1b2c3d4e5f6a7b8",
		TreeHash:      hash,
	}
	if err := WriteOrigin(extDir, origin); err != nil {
		t.Fatalf("WriteOrigin: %v", err)
	}

	inventory, err := Inventory(paths, model.KindFeature, nil, InventoryModifications)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	entry := inventory["foo"]
	if entry.Origin == nil {
		t.Fatal("expected provenance for a managed extension")
	}
	if entry.Modified {
		t.Error("clean managed extension reported as modified")
	}

	writeFixture(t, filepath.Join(extDir, "extra.sh"), "echo hi\n", 0o644)
	inventory, err = Inventory(paths, model.KindFeature, nil, InventoryModifications)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	entry = inventory["foo"]
	if !entry.Modified {
		t.Fatal("edited managed extension reported as clean")
	}
}

func TestInventoryToleratesCorruptSidecar(t *testing.T) {
	paths := inventoryPaths(t)
	writeFixture(t, filepath.Join(paths.UserFeaturesDir, "broken", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: broken\n", 0o644)
	writeFixture(t, filepath.Join(paths.UserFeaturesDir, "broken", model.ExtensionSourceFilename), "{not valid json", 0o644)
	writeFixture(t, filepath.Join(paths.FeaturesDir, "healthy", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: healthy\n", 0o644)

	inventory, err := Inventory(paths, model.KindFeature, nil, InventoryModifications)
	if err != nil {
		t.Fatalf("Inventory: %v (a corrupt sidecar must not abort the listing)", err)
	}
	if len(inventory) != 2 {
		t.Fatalf("expected both extensions to be listed, got %+v", inventory)
	}

	broken := inventory["broken"]
	if broken.Problem == "" {
		t.Fatal("expected Problem to be set for a corrupt sidecar")
	}
	if !strings.Contains(broken.Problem, "broken") {
		t.Errorf("Problem = %q, want it to name the extension", broken.Problem)
	}
	if broken.Origin != nil {
		t.Errorf("Origin = %+v, want nil when provenance could not be read", broken.Origin)
	}
	if broken.Modified {
		t.Error("Modified = true, want false when provenance could not be read")
	}

	healthy, ok := inventory["healthy"]
	if !ok || healthy.Problem != "" {
		t.Errorf("healthy extension should be listed cleanly, got %+v", healthy)
	}
}

// TestInventoryDetailStopsAtRequestedLevel pins what each detail level is
// allowed to touch, which is the entire point of the parameter: shell
// completion runs Inventory on every keystroke and must not pay for reading
// provenance, let alone for hashing every installed byte.
//
// The proof is a dangling symlink inside the extension. Reading its content
// fails with ENOENT for every user (root included), while the directory
// entries and the sidecar next to it stay perfectly readable — so a level that
// reports no problem here demonstrably never opened the content, and a level
// that does report one demonstrably did.
func TestInventoryDetailStopsAtRequestedLevel(t *testing.T) {
	paths := inventoryPaths(t)
	extDir := filepath.Join(paths.UserFeaturesDir, "foo")
	writeFixture(t, filepath.Join(extDir, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: foo\n", 0o644)
	hash, err := TreeHash(extDir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	if err := WriteOrigin(extDir, Origin{
		SchemaVersion: OriginSchemaVersion,
		Kind:          string(model.KindFeature),
		Name:          "foo",
		Remote:        "https://github.com/acme/kits",
		Source:        "acme/kits",
		Ref:           "main",
		RefType:       RefTypeBranch,
		Commit:        "a1b2c3d4e5f6a7b8",
		TreeHash:      hash,
	}); err != nil {
		t.Fatalf("WriteOrigin: %v", err)
	}
	if err := os.Symlink(filepath.Join(extDir, "gone"), filepath.Join(extDir, "dangling")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	names, err := Inventory(paths, model.KindFeature, nil, InventoryNames)
	if err != nil {
		t.Fatalf("Inventory(InventoryNames): %v", err)
	}
	switch entry := names["foo"]; {
	case entry.Source != "user":
		t.Errorf("Source = %q, want the label to be classified at every level", entry.Source)
	case entry.Origin != nil:
		t.Error("InventoryNames read the provenance sidecar")
	case entry.Problem != "":
		t.Errorf("InventoryNames read the extension's content: %s", entry.Problem)
	}

	provenance, err := Inventory(paths, model.KindFeature, nil, InventoryProvenance)
	if err != nil {
		t.Fatalf("Inventory(InventoryProvenance): %v", err)
	}
	entry := provenance["foo"]
	if entry.Origin == nil {
		t.Fatal("InventoryProvenance did not read the provenance sidecar")
	}
	if entry.Problem != "" {
		t.Errorf("InventoryProvenance read the extension's content: %s", entry.Problem)
	}
	if entry.Modified {
		t.Error("Modified is only answered at InventoryModifications")
	}

	modifications, err := Inventory(paths, model.KindFeature, nil, InventoryModifications)
	if err != nil {
		t.Fatalf("Inventory(InventoryModifications): %v", err)
	}
	if modifications["foo"].Problem == "" {
		t.Error("InventoryModifications did not read the extension's content")
	}
}

// TestInventoryReportsOnlyTheRequestedNames covers the listing path, which has
// already enumerated the extension roots itself and passes the result in
// rather than paying for a second enumeration.
func TestInventoryReportsOnlyTheRequestedNames(t *testing.T) {
	paths := inventoryPaths(t)
	writeFixture(t, filepath.Join(paths.UserFeaturesDir, "foo", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: foo\n", 0o644)
	writeFixture(t, filepath.Join(paths.UserFeaturesDir, "bar", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: bar\n", 0o644)

	inventory, err := Inventory(paths, model.KindFeature, []string{"foo"}, InventoryModifications)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(inventory) != 1 || inventory["foo"].Source != "user" {
		t.Fatalf("inventory = %+v, want only the requested name", inventory)
	}
}
