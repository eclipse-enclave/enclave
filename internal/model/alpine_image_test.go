// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import (
	"regexp"
	"strings"
	"testing"
)

// A digest-pinned `alpine` image reference, optionally tagged. The name must
// start a path segment or follow a shell `:-` default, so variant tags like
// golang's `-alpine` do not match.
var alpineDigestPattern = regexp.MustCompile(`(?:^|[\s/"'=]|:-)alpine(?::[\w.-]+)?@(sha256:[0-9a-f]{64})`)

// TestAlpineImageDigestMatchesOtherPins keeps the hand-maintained Alpine pins
// consistent so a digest bump cannot miss one of them.
func TestAlpineImageDigestMatchesOtherPins(t *testing.T) {
	t.Parallel()

	want := strings.TrimPrefix(AlpineImage, "alpine@")
	if !strings.HasPrefix(want, "sha256:") {
		t.Fatalf("AlpineImage %q is not pinned by digest", AlpineImage)
	}

	for _, relPath := range []string{
		"Dockerfile.gateway",
		"runtime-assets/microvm/alpine/build-bundle.sh",
	} {
		matches := alpineDigestPattern.FindAllStringSubmatch(readRepoFile(t, relPath), -1)
		if len(matches) == 0 {
			t.Fatalf("%s has no digest-pinned alpine image", relPath)
		}
		for _, m := range matches {
			if m[1] != want {
				t.Errorf("%s pins alpine at %s; model.AlpineImage uses %s", relPath, m[1], want)
			}
		}
	}
}
