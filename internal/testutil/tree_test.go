// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder collects the failures AssertTreeUnchanged reports instead of
// failing the surrounding test.
type recorder struct {
	testing.TB
	failures []string
}

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func TestAssertTreeUnchangedReportsWrites(t *testing.T) {
	file := filepath.Join("dir", "file")
	for _, tc := range []struct {
		name  string
		write func(root string) error
		want  string
	}{
		{"untouched", func(string) error { return nil }, ""},
		{"create", func(root string) error {
			return os.WriteFile(filepath.Join(root, "dir", "new"), nil, 0o600)
		}, filepath.Join("dir", "new") + " was created"},
		{"modify", func(root string) error {
			return os.WriteFile(filepath.Join(root, file), []byte("changed"), 0o600)
		}, file + " changed"},
		{"remove", func(root string) error {
			return os.Remove(filepath.Join(root, file))
		}, file + " was removed"},
		{"undone write", func(root string) error {
			probe := filepath.Join(root, "dir", "probe")
			if err := os.WriteFile(probe, nil, 0o600); err != nil {
				return err
			}
			return os.Remove(probe)
		}, "dir changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, file), []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := PinTree(t, root)
			if err := tc.write(root); err != nil {
				t.Fatal(err)
			}

			rec := &recorder{TB: t}
			AssertTreeUnchanged(rec, root, before)
			if tc.want == "" {
				if len(rec.failures) != 0 {
					t.Errorf("unexpected failure: %q", rec.failures)
				}
				return
			}
			if len(rec.failures) != 1 || !strings.Contains(rec.failures[0], tc.want) {
				t.Errorf("want one failure mentioning %q, got %q", tc.want, rec.failures)
			}
		})
	}
}
