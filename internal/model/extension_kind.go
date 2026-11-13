// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

// ExtensionKind distinguishes the extension flavours as the CLI presents
// them. The on-disk spec spells the same distinction
// `kind: sandbox|mixin|command|filter`; SpecKind maps a kind to that token.
type ExtensionKind string

const (
	KindTool    ExtensionKind = "tool"
	KindFeature ExtensionKind = "feature"
	// KindCommand ships host commands only. It never reaches an image.
	KindCommand ExtensionKind = "command"
	// KindFilter is a request filter: data the gateway enforces. It never
	// reaches an image.
	KindFilter ExtensionKind = "filter"
)

// ExtensionKinds lists every kind in the order host command names resolve:
// an extension that exists only for its verbs claims a name before one whose
// verbs are a side feature.
var ExtensionKinds = []ExtensionKind{KindCommand, KindTool, KindFeature, KindFilter}

// SpecKind is the `kind` token an extension of this kind declares in its spec
// document, and the Extension.Type it loads as.
func (k ExtensionKind) SpecKind() string {
	switch k {
	case KindTool:
		return ExtensionKindSandbox
	case KindCommand:
		return ExtensionKindCommand
	case KindFilter:
		return ExtensionKindFilter
	default:
		return ExtensionKindMixin
	}
}

// ExtensionKindFor maps an on-disk spec kind token back to the kind it names.
// An unrecognized token names no kind.
func ExtensionKindFor(specKind string) (ExtensionKind, bool) {
	switch specKind {
	case ExtensionKindSandbox:
		return KindTool, true
	case ExtensionKindMixin:
		return KindFeature, true
	case ExtensionKindCommand:
		return KindCommand, true
	case ExtensionKindFilter:
		return KindFilter, true
	default:
		return "", false
	}
}

// DirName is the plural name of this kind: the subdirectory of an extension
// root holding it, and the CLI parent command that manages it.
func (k ExtensionKind) DirName() string {
	switch k {
	case KindTool:
		return "tools"
	case KindCommand:
		return "commands"
	case KindFilter:
		return "filters"
	default:
		return "features"
	}
}

// Label is the singular noun used in messages.
func (k ExtensionKind) Label() string { return string(k) }

// ReachesImage reports whether extensions of this kind are built into the
// tool image. Command extensions and filters are not: nothing they ship is
// staged into the build context or the image hash.
func (k ExtensionKind) ReachesImage() bool {
	return k == KindTool || k == KindFeature
}
