// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildxCommandArgsIncludesCacheAndGeneratedDockerfile(t *testing.T) {
	req := BuildRequest{
		ContextDir:        "/repo",
		Dockerfile:        ".enclave/Dockerfile.generated",
		DockerfileContent: []byte("FROM scratch\n"),
		Tags:              []string{"enclave:test"},
		Target:            "standard",
		BuildArgs: map[string]string{
			"USER_ID":  "1000",
			"GROUP_ID": "1000",
		},
		Labels: map[string]string{
			"enclave.version": "test",
		},
		CacheFrom:       []string{"enclave:latest"},
		BuildxCacheFrom: []string{"type=local,src=/cache"},
		BuildxCacheTo:   []string{"type=local,dest=/cache,mode=max"},
		Progress:        "verbose",
		NetworkMode:     "host",
	}

	args, stdin := buildxCommandArgs(req)
	want := []string{
		"buildx", "build", "--load",
		"--tag", "enclave:test",
		"--file", "-",
		"--target", "standard",
		"--network", "host",
		"--allow", "network.host",
		"--build-arg", "GROUP_ID=1000",
		"--build-arg", "USER_ID=1000",
		"--label", "enclave.version=test",
		"--cache-from", "enclave:latest",
		"--cache-from", "type=local,src=/cache",
		"--cache-to", "type=local,dest=/cache,mode=max",
		"--progress", "plain",
		"/repo",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("buildx args:\ngot  %v\nwant %v", args, want)
	}
	if stdin == nil {
		t.Fatal("expected generated Dockerfile content on stdin")
	}
}

func TestShouldUseBuildxRequiresCacheOptions(t *testing.T) {
	if shouldUseBuildx(BuildRequest{}) {
		t.Fatal("did not expect a build without cache options to use buildx")
	}
	if !shouldUseBuildx(BuildRequest{BuildxCacheTo: []string{"type=local,dest=/cache,mode=max"}}) {
		t.Fatal("expected cache-to request to use buildx")
	}
	if !shouldUseBuildx(BuildRequest{BuildxCacheFrom: []string{"type=registry,ref=example.test/cache"}}) {
		t.Fatal("expected cache-from request to use buildx")
	}
}

func TestBuildWithDockerCLIOmitsProgressWhenBuildkitDisabled(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	stub := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$STUB_ARGS\"\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	orig := dockerBinary
	dockerBinary = stub
	t.Cleanup(func() { dockerBinary = orig })
	t.Setenv("STUB_ARGS", argsPath)
	t.Setenv("DOCKER_BUILDKIT", "0")

	if err := buildWithDockerCLI(context.Background(), BuildRequest{ContextDir: dir, Progress: buildProgressVerbose}, nil); err != nil {
		t.Fatalf("build with docker cli: %v", err)
	}

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	if strings.Contains(string(data), "--progress") {
		t.Fatalf("did not expect --progress with DOCKER_BUILDKIT=0, got:\n%s", data)
	}
}
