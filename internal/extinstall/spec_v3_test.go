// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestV3WorkloadInstallUpdateRemove(t *testing.T) {
	const source = `schemaVersion: "3"
kind: workload
displayName: Demo Tool
capabilities:
  - type: org.eclipse.enclave/runtime@1
    config:
      name: demo
      defaultIncluded: false
      sandbox:
        entrypoint: {run: [demo]}
        configDir: .demo
        memoryDir: .demo/memory
        statePaths: [memory/]
        continueArgs: [--continue]
`
	files := map[string]string{
		"tools/demo/spec.yaml":  source,
		"tools/demo/README.md":  "Demo tool",
		"tools/demo/install.sh": "#!/bin/sh\necho install\n",
	}
	env, _ := testEnv(t, newFakeFetcher(t, "a1b2c3d4", files), "")
	req := Request{Kind: model.KindTool, Op: OpAdd, Source: "acme/kits", Yes: true}
	results, err := Add(context.Background(), env, req)
	if err != nil || len(results) != 1 || results[0].Action != ActionInstalled {
		t.Fatalf("Add: %+v, %v", results, err)
	}
	installed := filepath.Join(env.Paths.UserToolsDir, "demo")
	origin, err := readOrigin(installed)
	if err != nil || origin == nil || origin.SchemaVersion != "1" || origin.Kind != "tool" {
		t.Fatalf("provenance contract changed: %+v, %v", origin, err)
	}
	profile, err := config.LoadProfile(env.Paths, "demo")
	if err != nil || profile.ConfigDir != ".demo" || profile.MemoryDir != ".demo/memory" {
		t.Fatalf("installed profile: %+v, %v", profile, err)
	}
	files["tools/demo/spec.yaml"] = strings.Replace(source, "[--continue]", "[--continue, --last]", 1)
	env.Fetcher = newFakeFetcher(t, "b2c3d4e5", files)
	req.Op, req.Names = OpUpdate, []string{"demo"}
	results, err = Update(context.Background(), env, req)
	if err != nil || len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("Update: %+v, %v", results, err)
	}
	profile, err = config.LoadProfile(env.Paths, "demo")
	if err != nil || len(profile.ContinueArgs) != 2 || profile.ContinueArgs[1] != "--last" {
		t.Fatalf("updated profile: %+v, %v", profile, err)
	}
	req.Op = OpRemove
	results, err = Remove(context.Background(), env, req)
	if err != nil || len(results) != 1 || results[0].Action != ActionRemoved {
		t.Fatalf("Remove: %+v, %v", results, err)
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatal("extension directory survived removal")
	}
}
