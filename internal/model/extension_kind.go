// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

// ExtensionKind distinguishes the two extension flavours as the CLI presents
// them. The on-disk spec spells the same distinction `kind: sandbox|mixin`;
// SpecKind maps a kind to that token.
type ExtensionKind string

const (
	KindTool    ExtensionKind = "tool"
	KindFeature ExtensionKind = "feature"
)

// SpecKind is the `kind` token an extension of this kind declares in its spec
// document, and the Extension.Type it loads as.
func (k ExtensionKind) SpecKind() string {
	if k == KindTool {
		return ExtensionKindSandbox
	}
	return ExtensionKindMixin
}

// ExtensionKindFor maps an on-disk spec kind token back to the kind it names.
// An unrecognized token names no kind.
func ExtensionKindFor(specKind string) (ExtensionKind, bool) {
	switch specKind {
	case ExtensionKindSandbox:
		return KindTool, true
	case ExtensionKindMixin:
		return KindFeature, true
	default:
		return "", false
	}
}

// DirName is the subdirectory of an extension root holding this kind.
func (k ExtensionKind) DirName() string {
	if k == KindTool {
		return "tools"
	}
	return "features"
}

// Verb is the CLI parent command for this kind.
func (k ExtensionKind) Verb() string { return k.DirName() }

// Label is the singular noun used in messages.
func (k ExtensionKind) Label() string { return string(k) }

// Other is the opposite kind.
func (k ExtensionKind) Other() ExtensionKind {
	if k == KindTool {
		return KindFeature
	}
	return KindTool
}
