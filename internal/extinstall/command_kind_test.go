// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/model"
)

const usageCommandSpec = "schemaVersion: \"1\"\nkind: command\nname: usage\ndescription: Token usage\n"

const usageCommandRel = "commands/usage/commands/host/usage"

// usageRepo serves a command extension beside a feature, the way a kit
// repository mixes kinds.
func usageRepo(t *testing.T, spec string) *fakeFetcher {
	t.Helper()
	files := fooRepoFiles()
	files["commands/usage/spec.yaml"] = spec
	files["commands/usage/README.md"] = "docs\n"
	files[usageCommandRel] = "#!/bin/sh\necho usage\n"
	fetcher := newFakeFetcher(t, "a1b2c3d4", files)
	markExecutable(t, fetcher, usageCommandRel)
	return fetcher
}

func commandRequest(op Op) Request {
	return Request{Kind: model.KindCommand, Op: op, Source: "acme/kits", Yes: true}
}

// A command extension reaches no image and has nothing to enable, so the
// install must not promise a rebuild or tell the user to enable it.
func TestAddCommandExtension(t *testing.T) {
	env, out := testEnv(t, usageRepo(t, usageCommandSpec), "")

	results, err := Add(context.Background(), env, commandRequest(OpAdd))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Name != "usage" || results[0].Action != ActionInstalled {
		t.Fatalf("results = %s, want only the command extension", fmtResults(results))
	}
	if !slices.Equal(results[0].HostCommands, []string{"usage"}) {
		t.Errorf("HostCommands = %v, want [usage]", results[0].HostCommands)
	}
	installed := filepath.Join(env.Paths.UserCommandExtensionsDir, "usage")
	origin, err := readOrigin(installed)
	if err != nil || origin == nil || origin.Kind != string(model.KindCommand) {
		t.Fatalf("readOrigin = %+v, %v", origin, err)
	}
	narration := out.String()
	for _, unwanted := range []string{"rebuilds the image", "enable it with"} {
		if strings.Contains(narration, unwanted) {
			t.Errorf("command extension install printed %q:\n%s", unwanted, narration)
		}
	}

	removed, err := Remove(context.Background(), env, Request{Kind: model.KindCommand, Op: OpRemove, Names: []string{"usage"}, Yes: true})
	if err != nil || len(removed) != 1 || removed[0].Action != ActionRemoved {
		t.Fatalf("Remove = %s, %v", fmtResults(removed), err)
	}
}

func TestAddCommandExtensionRejectsRuntimeFields(t *testing.T) {
	spec := usageCommandSpec + "aptPackages: [jq]\n"
	env, _ := testEnv(t, usageRepo(t, spec), "")

	results, err := Add(context.Background(), env, commandRequest(OpAdd))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionFailed || !strings.Contains(results[0].Error, "aptPackages") {
		t.Fatalf("results = %s, want a failure naming aptPackages", fmtResults(results))
	}
}

func TestAddCommandExtensionNeedsAnExecutable(t *testing.T) {
	files := map[string]string{
		"commands/usage/spec.yaml":            usageCommandSpec,
		"commands/usage/commands/host/README": "not a command\n",
	}
	env, _ := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")

	results, err := Add(context.Background(), env, commandRequest(OpAdd))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionFailed || !strings.Contains(results[0].Error, "adds no command") {
		t.Fatalf("results = %s, want a failure for a command extension without a command", fmtResults(results))
	}
}

// With three kinds there is no single "other" verb: the error names one per
// kind the source actually holds.
func TestAddWrongKindNamesEveryOtherVerb(t *testing.T) {
	files := fooRepoFiles()
	files["extensions/tools/bar/spec.yaml"] = "schemaVersion: \"1\"\nkind: sandbox\nname: bar\n"
	env, _ := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")

	_, err := Add(context.Background(), env, commandRequest(OpAdd))
	if err == nil {
		t.Fatal("Add installed a feature or tool as a command extension")
	}
	for _, want := range []string{"1 tool and 1 feature extension(s) but no command extension", "`enclave tools add`", "`enclave features add`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestAddCommandsOnlyFeatureWarns(t *testing.T) {
	const rel = "features/usage/commands/host/usage"
	files := map[string]string{
		"features/usage/spec.yaml": "schemaVersion: \"1\"\nkind: mixin\nname: usage\ndefaultEnabled: false\n",
		"features/usage/README.md": "docs\n",
		rel:                        "#!/bin/sh\necho usage\n",
	}
	fetcher := newFakeFetcher(t, "a1b2c3d4", files)
	markExecutable(t, fetcher, rel)
	env, out := testEnv(t, fetcher, "")

	if _, err := Add(context.Background(), env, addRequest()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !strings.Contains(out.String(), "ships nothing but host commands") ||
		!strings.Contains(out.String(), "enclave commands add") {
		t.Fatalf("commands-only feature was not pointed at command extensions:\n%s", out.String())
	}

	// A feature that also installs something is a real feature.
	files["features/usage/install.sh"] = "#!/bin/sh\necho install\n"
	fetcher = newFakeFetcher(t, "a1b2c3d4", files)
	markExecutable(t, fetcher, rel)
	env, out = testEnv(t, fetcher, "")
	if _, err := Add(context.Background(), env, addRequest()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if strings.Contains(out.String(), "ships nothing but host commands") {
		t.Fatalf("feature with an install script was told it only ships commands:\n%s", out.String())
	}
}
