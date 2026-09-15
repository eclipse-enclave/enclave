// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "testing"

func TestExtensionKindMappings(t *testing.T) {
	if KindFeature.SpecKind() != ExtensionKindMixin || KindTool.SpecKind() != ExtensionKindSandbox {
		t.Fatalf("spec kinds wrong: %q %q", KindFeature.SpecKind(), KindTool.SpecKind())
	}
	if KindFeature.DirName() != "features" || KindTool.DirName() != "tools" {
		t.Fatal("dir names wrong")
	}
	if KindFeature.Label() != "feature" || KindTool.Label() != "tool" {
		t.Fatal("labels wrong")
	}
	if KindFeature.Other() != KindTool || KindTool.Other() != KindFeature {
		t.Fatal("Other() wrong")
	}
}
