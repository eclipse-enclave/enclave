// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"

	"enclave/internal/backend"
)

func TestManagedSettingsIntentSurvivesInterruptedPublication(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	for _, scenario := range []string{"existing", "first-before", "first-after"} {
		t.Run(scenario, func(t *testing.T) {
			root, source, store := t.TempDir(), t.TempDir(), t.TempDir()
			key := backend.StoreKey{Owner: "copilot", ProjectHash: "abc123abc123"}
			base, err := ConfigBasePath(root, key)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
				t.Fatal(err)
			}
			write := func(path, data string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			read := func(path string) map[string]any {
				t.Helper()
				data, err := os.ReadFile(path)
				var value map[string]any
				if err != nil || json.Unmarshal(data, &value) != nil {
					t.Fatal("cannot read settings", err)
				}
				return value
			}
			sourceFile, storeFile := filepath.Join(source, "settings.json"), filepath.Join(store, "settings.json")
			if scenario == "existing" {
				write(base, `{"storeTokenPlaintext":false,"model":"old"}`)
			}
			write(storeFile, `{"storeTokenPlaintext":false,"model":"tool"}`)
			const protected = `{"storeTokenPlaintext":true,"model":"new"}`
			write(sourceFile, protected)
			spec := backend.ConfigOverlaySpec{SourceDir: source, SettingsPath: "settings.json", SettingsBase: []byte(`{"model":"new"}`)}
			interrupted := errors.New("interrupted after settings publication")
			if err := OverlayConfigSettings(root, key, store, spec, func() error {
				intent := read(base)
				_, hasModel := intent["model"]
				if intent["storeTokenPlaintext"] != true || (scenario == "existing" && intent["model"] != "old") || (scenario != "existing" && hasModel) {
					t.Fatal("forced consent was not recorded before publication, or old metadata baseline was lost")
				}
				if scenario != "first-before" {
					write(storeFile, protected)
				}
				return interrupted
			}); !errors.Is(err, interrupted) {
				t.Fatalf("interruption error = %v", err)
			}
			const ordinary = `{"storeTokenPlaintext":false,"model":"new"}`
			write(sourceFile, ordinary)
			spec.SettingsBase = []byte(ordinary)
			defer func() { _ = os.Chmod(filepath.Dir(base), 0o700) }()
			if err := OverlayConfigSettings(root, key, store, spec, func() error {
				if read(base)["storeTokenPlaintext"] != true {
					t.Fatal("restoration intent cleared before ordinary false was published")
				}
				write(storeFile, ordinary)
				return os.Chmod(filepath.Dir(base), 0o500)
			}); err != nil {
				t.Fatal(err)
			}
			if read(storeFile)["storeTokenPlaintext"] != false || read(storeFile)["model"] != "new" || read(base)["storeTokenPlaintext"] != true {
				t.Fatal("failed final snapshot did not retain a safe restoration intent")
			}
			if err := os.Chmod(filepath.Dir(base), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := OverlayConfigSettings(root, key, store, spec, func() error {
				write(storeFile, ordinary)
				return nil
			}); err != nil || read(storeFile)["storeTokenPlaintext"] != false || read(base)["storeTokenPlaintext"] != false {
				t.Fatal("fresh ordinary launch did not recover after snapshot failure", err)
			}
		})
	}
}

func TestMergeSettingsFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ext       string
		base      string
		store     string
		generated string
		want      string
	}{
		{
			name:      "JSON recursive changes, conflicts and deletions",
			ext:       ".json",
			base:      `{"model":"old","gone":1,"array":[1],"nested":{"tool":"old","patch":"old"}}`,
			store:     `{"model":"tool","array":[2],"nested":{"tool":"new","patch":"old"}}`,
			generated: `{"model":"patch","gone":1,"array":[1],"nested":{"tool":"old","patch":"new"}}`,
			want:      `{"model":"patch","array":[2],"nested":{"tool":"new","patch":"new"}}`,
		},
		{
			name:      "TOML recursive changes, conflicts and deletions",
			ext:       ".toml",
			base:      "model = 'old'\ngone = 1\narray = [1]\n[nested]\ntool = 'old'\npatch = 'old'\n",
			store:     "model = 'tool'\narray = [2]\n[nested]\ntool = 'new'\npatch = 'old'\n",
			generated: "model = 'patch'\ngone = 1\narray = [1]\n[nested]\ntool = 'old'\npatch = 'new'\n",
			want:      "model = 'patch'\narray = [2]\n[nested]\ntool = 'new'\npatch = 'new'\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := mergeSettingsFile([]byte(tt.base), []byte(tt.store), []byte(tt.generated), tt.ext)
			if err != nil {
				t.Fatal(err)
			}
			decode := func(data string) any {
				t.Helper()
				if tt.ext == ".json" {
					var value any
					if err := json.Unmarshal([]byte(data), &value); err != nil {
						t.Fatal(err)
					}
					return value
				}
				var value map[string]any
				if _, err := toml.Decode(data, &value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			if !reflect.DeepEqual(decode(string(got)), decode(tt.want)) {
				t.Fatalf("merged settings = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMergeSettingsFileIgnoresFormattingOnlyEdits(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ ext, base, store, generated string }{
		{".json", `{"a":1,"b":2}`, "{\n  \"b\": 2,\n  \"a\": 1\n}\n", "{\"a\": 3, \"b\": 2}\n"},
		{".json", `{"n":1}`, `{"n":1.0}`, `{"n":2}`},
		{".toml", "a = 1\nb = 2\n", "# tool comment\nb = 2\na = 1\n", "a = 3\nb = 2\n"},
	} {
		got, err := mergeSettingsFile([]byte(tt.base), []byte(tt.store), []byte(tt.generated), tt.ext)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tt.generated {
			t.Fatalf("%s formatting-only edit yielded %q, want generated bytes %q", tt.ext, got, tt.generated)
		}
	}
}

func TestMergeSettingsFilePreservesLargeJSONNumbersAndReadableCommands(t *testing.T) {
	t.Parallel()
	base := []byte(`{"n":12345678901234567890,"command":"x && y > z","model":"base"}`)
	store := []byte(`{"n":12345678901234567890,"command":"x && y > z","model":"tool"}`)
	got, err := mergeSettingsFile(base, store, base, ".json")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"command\": \"x && y > z\",\n  \"model\": \"tool\",\n  \"n\": 12345678901234567890\n}\n"
	if string(got) != want {
		t.Fatalf("merged JSON = %s, want %s", got, want)
	}
}

func TestOverlayConfigSettingsInvalidatesSnapshotOnFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key := backend.StoreKey{Owner: "pi", ProjectHash: "abc123abc123"}
	storeDir := filepath.Join(root, "store")
	sourceDir := filepath.Join(root, "source")
	basePath, err := ConfigBasePath(root, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{storeDir, sourceDir, filepath.Dir(basePath)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	storePath := filepath.Join(storeDir, "settings.json")
	sourcePath := filepath.Join(sourceDir, "settings.json")
	write(basePath, `{"x":0}`)
	write(storePath, `{"x":0}`)
	write(sourcePath, `{"x":1}`)
	spec := backend.ConfigOverlaySpec{SourceDir: sourceDir, SettingsPath: "settings.json"}
	if err := OverlayConfigSettings(root, key, storeDir, spec, func() error { return errors.New("overlay failed") }); err == nil {
		t.Fatal("expected overlay failure")
	}
	if _, err := os.Stat(basePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot after failed overlay: %v", err)
	}

	// Simulate a snapshot write failure after the store received x=1.
	write(basePath, `{"x":0}`)
	if err := OverlayConfigSettings(root, key, storeDir, spec, func() error {
		write(storePath, `{"x":1}`)
		if err := os.RemoveAll(filepath.Dir(basePath)); err != nil {
			return err
		}
		return os.WriteFile(filepath.Dir(basePath), []byte("block snapshot directory"), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(basePath)); err != nil {
		t.Fatal(err)
	}
	write(sourcePath, `{"x":0}`)
	if err := OverlayConfigSettings(root, key, storeDir, spec, func() error {
		return os.WriteFile(storePath, []byte(`{"x":0}`), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(storePath)
	if err != nil || string(got) != `{"x":0}` {
		t.Fatalf("patch reversion after snapshot failure = %s, err %v", got, err)
	}
}

func TestOverlayConfigSettingsRegeneratesWithoutUsableStoreFile(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		file string
	}{
		{"missing", ""},
		{"malformed", "{"},
		{"no snapshot", `{"model":"tool"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			key := backend.StoreKey{Owner: "pi", ProjectHash: "abc123abc123"}
			storeDir := filepath.Join(root, "store")
			sourceDir := filepath.Join(root, "source")
			basePath, err := ConfigBasePath(root, key)
			if err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{storeDir, sourceDir, filepath.Dir(basePath)} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			generated := `{"model":"generated"}`
			if err := os.WriteFile(filepath.Join(sourceDir, "settings.json"), []byte(generated), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.file != "" {
				if err := os.WriteFile(filepath.Join(storeDir, "settings.json"), []byte(tt.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.name != "no snapshot" {
				if err := os.WriteFile(basePath, []byte(`{"model":"base"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			spec := backend.ConfigOverlaySpec{SourceDir: sourceDir, SettingsPath: "settings.json"}
			if err := OverlayConfigSettings(root, key, storeDir, spec, func() error {
				return os.WriteFile(filepath.Join(storeDir, "settings.json"), []byte(generated), 0o600)
			}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(storeDir, "settings.json"))
			if err != nil || string(got) != generated {
				t.Fatalf("store settings = %s, err %v", got, err)
			}
			base, err := os.ReadFile(basePath)
			if err != nil || string(base) != generated {
				t.Fatalf("snapshot = %s, err %v", base, err)
			}
		})
	}
}

func TestOverlayConfigSettingsDoesNotTouchStoreWhenSnapshotRemovalFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key := backend.StoreKey{Owner: "pi", ProjectHash: "abc123abc123"}
	storeDir := filepath.Join(root, "store")
	sourceDir := filepath.Join(root, "source")
	basePath, err := ConfigBasePath(root, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{storeDir, sourceDir, basePath} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(basePath, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "settings.json"), []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err = OverlayConfigSettings(root, key, storeDir, backend.ConfigOverlaySpec{
		SourceDir: sourceDir, SettingsPath: "settings.json",
	}, func() error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("snapshot removal error = %v, overlay called = %t", err, called)
	}
}
