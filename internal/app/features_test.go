// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/extinstall"
	"enclave/internal/model"
)

// displayName is part of the documented `list --json` contract.
func TestRunFeaturesJSONPopulatesDisplayName(t *testing.T) {
	tmp := t.TempDir()
	featuresDir := filepath.Join(tmp, "extensions", "features")
	spec := "schemaVersion: \"1\"\nkind: mixin\nname: demo\ndisplayName: Demo Feature\n"
	writeAppFile(t, filepath.Join(featuresDir, "demo", "spec.yaml"), spec, 0o644)

	ctx := NewAppContext(model.Paths{FeaturesDir: featuresDir}, tmp)
	req := &extinstall.Request{Kind: model.KindFeature, JSON: true}

	out := captureStdout(t, func() {
		if code := runFeatures(ctx, req, model.Options{}, model.OptionSources{}); code != 0 {
			t.Errorf("runFeatures exit code = %d", code)
		}
	})

	var decoded struct {
		Extensions []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(decoded.Extensions) != 1 || decoded.Extensions[0].DisplayName != "Demo Feature" {
		t.Fatalf("extensions = %+v, want displayName populated", decoded.Extensions)
	}
}

// `list` renders local modifications in both output shapes.
func TestRunFeaturesReportsLocalModification(t *testing.T) {
	tmp := t.TempDir()
	featuresDir := filepath.Join(tmp, "extensions", "features")
	userFeaturesDir := filepath.Join(tmp, "user", "extensions", "features")
	extDir := filepath.Join(userFeaturesDir, "demo")
	writeAppFile(t, filepath.Join(extDir, "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: demo\n", 0o644)
	hash, err := extinstall.TreeHash(extDir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	if err := extinstall.WriteOrigin(extDir, extinstall.Origin{
		SchemaVersion: extinstall.OriginSchemaVersion,
		Kind:          string(model.KindFeature),
		Name:          "demo",
		Remote:        "https://github.com/acme/kits",
		Source:        "acme/kits",
		Ref:           "main",
		RefType:       extinstall.RefTypeBranch,
		Commit:        "a1b2c3d4e5f6a7b8",
		TreeHash:      hash,
	}); err != nil {
		t.Fatalf("WriteOrigin: %v", err)
	}
	writeAppFile(t, filepath.Join(extDir, "local.sh"), "echo mine\n", 0o644)

	paths := model.Paths{FeaturesDir: featuresDir, UserFeaturesDir: userFeaturesDir}
	ctx := NewAppContext(paths, tmp)

	text := captureStdout(t, func() {
		if code := runFeatures(ctx, nil, model.Options{}, model.OptionSources{}); code != 0 {
			t.Errorf("runFeatures exit code = %d", code)
		}
	})
	if !strings.Contains(text, ", modified]") {
		t.Errorf("text listing does not mark the local edit:\n%s", text)
	}

	out := captureStdout(t, func() {
		req := &extinstall.Request{Kind: model.KindFeature, JSON: true}
		if code := runFeatures(ctx, req, model.Options{}, model.OptionSources{}); code != 0 {
			t.Errorf("runFeatures --json exit code = %d", code)
		}
	})
	var decoded struct {
		Extensions []struct {
			Origin *struct {
				LocallyModified bool `json:"locallyModified"`
			} `json:"origin"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(decoded.Extensions) != 1 || decoded.Extensions[0].Origin == nil || !decoded.Extensions[0].Origin.LocallyModified {
		t.Fatalf("extensions = %+v, want locallyModified true", decoded.Extensions)
	}
}
