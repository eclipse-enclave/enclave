// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package sbx

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/backend"
)

func TestBuildPlanMapsRequestOntoSbxCommands(t *testing.T) {
	root := t.TempDir()
	project := mkdir(t, root, "project")
	docs := mkdir(t, root, "docs")
	cache := filepath.Join(root, "cache", "npm") // missing: created on demand
	gitconfig := writeFile(t, root, "gitconfig", "[user]\n")
	npmrc := writeFile(t, root, "npmrc", "registry=x\n")
	configStore := filepath.Join(root, "stores", "config")
	stageDir := mkdir(t, root, "stage")

	req := backend.Request{
		Session: backend.SessionMeta{Tool: "claude", Name: "enclave-claude-abc123-1", Worktree: project, RealWorktree: project},
		Image:   "enclave-claude:latest",
		Argv:    []string{"claude", "--dangerously-skip-permissions"},
		Env: []backend.EnvVar{
			{Name: "TOOL", Value: "claude"},
			{Name: "TOKEN", Value: "a=b c"},
		},
		WorkingDir: project,
		Mounts: []backend.Mount{
			{Type: backend.MountTypeBind, Source: docs, ContainerPath: "/work/docs", ReadOnly: true},
			{Type: backend.MountTypeBind, Source: project, ContainerPath: project},
			{Type: backend.MountTypeBind, Source: cache, ContainerPath: "/home/agent/.npm", CreateSourceDir: true},
			{Type: backend.MountTypeBind, Source: gitconfig, ContainerPath: "/tmp/host_gitconfig", ReadOnly: true},
			{Type: backend.MountTypeBind, Source: npmrc, ContainerPath: "/home/agent/.npmrc"},
		},
		Stores: []backend.PersistentStore{
			{Kind: backend.StoreKindConfig, Key: backend.StoreKey{Owner: "claude"}, ContainerPath: "/home/agent/.claude"},
			{Kind: backend.StoreKindEnv, Key: backend.StoreKey{Owner: "claude"}},
		},
		Ports: []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "3000", ContainerPort: "3000", Protocol: "tcp"}},
	}
	plan, err := buildPlan(planInput{
		Request: req,
		Image:   ImageConfig{User: "agent", Entrypoint: []string{"/usr/local/bin/entrypoint.sh"}, Cmd: []string{"/bin/bash"}},
		StoreSource: func(key backend.StoreKey, kind backend.StoreKind) (string, error) {
			if kind != backend.StoreKindConfig {
				t.Fatalf("store without a container path resolved: %s", kind)
			}
			return configStore, nil
		},
		KitDir:      "/tmp/kit",
		EnvFilePath: "/tmp/session.env",
		StageDir:    stageDir,
		TTY:         true,
	})
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}

	wantCreate := []string{
		"create", "--name", "enclave-claude-abc123-1", "--pull", "never", "--skills", "off", "--no-progress",
		"--publish", "127.0.0.1:3000:3000/tcp",
		"/tmp/kit",
		project,
		docs + ":/work/docs:ro",
		cache + ":/home/agent/.npm",
		gitconfig + ":/tmp/host_gitconfig:ro",
		configStore + ":/home/agent/.claude",
		stageDir + ":" + guestFileStageDir,
	}
	if !reflect.DeepEqual(plan.CreateArgs, wantCreate) {
		t.Fatalf("create args:\n got %q\nwant %q", plan.CreateArgs, wantCreate)
	}
	wantExec := []string{
		"exec", "-i", "-t", "-u", "agent", "-w", project, "--env-file", "/tmp/session.env",
		"enclave-claude-abc123-1", "/usr/local/bin/entrypoint.sh", "claude", "--dangerously-skip-permissions",
	}
	if !reflect.DeepEqual(plan.ExecArgs, wantExec) {
		t.Fatalf("exec args:\n got %q\nwant %q", plan.ExecArgs, wantExec)
	}
	if got, want := string(plan.EnvFile), "TOOL=claude\nTOKEN=a=b c\n"; got != want {
		t.Fatalf("env file = %q, want %q", got, want)
	}
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("disposable cache dir not created: %v", err)
	}
	if info, err := os.Stat(configStore); err != nil || !info.IsDir() {
		t.Fatalf("config store dir not created: %v", err)
	}

	if len(plan.StagedFiles) != 1 {
		t.Fatalf("staged files = %+v, want the writable npmrc only", plan.StagedFiles)
	}
	staged := plan.StagedFiles[0]
	if staged.Source != npmrc || staged.Target != "/home/agent/.npmrc" || staged.GuestStaged != guestFileStageDir+"/00/npmrc" {
		t.Fatalf("staged file = %+v", staged)
	}
	if data, err := os.ReadFile(staged.HostStaged); err != nil || string(data) != "registry=x\n" {
		t.Fatalf("staged copy = %q, %v", data, err)
	}

	kit := string(plan.KitSpec)
	for _, want := range []string{`schemaVersion: "2"`, "kind: sandbox", "name: enclave-claude", "image: enclave-claude:latest", "/usr/local/bin/entrypoint.sh"} {
		if !strings.Contains(kit, want) {
			t.Fatalf("kit spec missing %q:\n%s", want, kit)
		}
	}
}

func TestBuildPlanOmitsTTYAndWorkdirWhenUnset(t *testing.T) {
	project := t.TempDir()
	plan, err := buildPlan(planInput{
		Request: backend.Request{
			Session: backend.SessionMeta{Name: "enclave-pi-abc-1", Worktree: project},
			Image:   "img",
			Mounts:  []backend.Mount{{Source: project, ContainerPath: project}},
		},
		Image:       ImageConfig{Cmd: []string{"bash"}},
		EnvFilePath: "/env",
	})
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	want := []string{"exec", "-i", "-u", "root", "--env-file", "/env", "enclave-pi-abc-1", "bash"}
	if !reflect.DeepEqual(plan.ExecArgs, want) {
		t.Fatalf("exec args = %q, want %q", plan.ExecArgs, want)
	}
}

func TestWorkspaceArgsNeedsWritableProjectMount(t *testing.T) {
	project := t.TempDir()
	req := backend.Request{
		Session: backend.SessionMeta{Worktree: project},
		Mounts:  []backend.Mount{{Source: project, ContainerPath: project, ReadOnly: true}},
	}
	if _, _, err := workspaceArgs(req, nil, t.TempDir()); err == nil || !strings.Contains(err.Error(), "primary workspace") {
		t.Fatalf("err = %v, want a primary workspace error", err)
	}
}

func TestWorkspaceArgsFallsBackToAnyWritableSelfMount(t *testing.T) {
	root := t.TempDir()
	other := mkdir(t, root, "other")
	args, _, err := workspaceArgs(backend.Request{
		Session: backend.SessionMeta{Worktree: filepath.Join(root, "elsewhere")},
		Mounts:  []backend.Mount{{Source: other, ContainerPath: other}},
	}, nil, t.TempDir())
	if err != nil {
		t.Fatalf("workspaceArgs: %v", err)
	}
	if !reflect.DeepEqual(args, []string{other}) {
		t.Fatalf("args = %q", args)
	}
}

func TestWorkspaceArgRejectsUnexpressiblePaths(t *testing.T) {
	for _, tc := range []struct{ source, target string }{
		{"/a:b", "/x"},
		{"/a", "relative"},
		{"/a", "/x:y"},
	} {
		if _, err := workspaceArg(tc.source, tc.target, false); err == nil {
			t.Errorf("workspaceArg(%q, %q) succeeded", tc.source, tc.target)
		}
	}
}

func TestSessionArgvFollowsDockerRunSemantics(t *testing.T) {
	img := ImageConfig{Entrypoint: []string{"/entry"}, Cmd: []string{"/bin/bash"}}
	for _, tc := range []struct {
		name string
		req  backend.Request
		want []string
	}{
		{"image defaults", backend.Request{}, []string{"/entry", "/bin/bash"}},
		{"argv replaces cmd", backend.Request{Argv: []string{"claude"}}, []string{"/entry", "claude"}},
		{"entrypoint drops image cmd", backend.Request{Entrypoint: []string{"sh"}}, []string{"sh"}},
		{"entrypoint and argv", backend.Request{Entrypoint: []string{"sh"}, Argv: []string{"-c", "x"}}, []string{"sh", "-c", "x"}},
	} {
		got, err := sessionArgv(tc.req, img)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	if _, err := sessionArgv(backend.Request{Image: "x"}, ImageConfig{}); err == nil {
		t.Error("empty argv accepted")
	}
}

func TestSessionUserPrefersRequestThenImage(t *testing.T) {
	if got := sessionUser(backend.Request{User: "root"}, ImageConfig{User: "agent"}); got != "root" {
		t.Errorf("request user: got %q", got)
	}
	if got := sessionUser(backend.Request{}, ImageConfig{User: "agent"}); got != "agent" {
		t.Errorf("image user: got %q", got)
	}
	if got := sessionUser(backend.Request{}, ImageConfig{}); got != "root" {
		t.Errorf("default user: got %q", got)
	}
}

func TestSandboxNameKeepsManagedNamesAndSanitizesOthers(t *testing.T) {
	for in, want := range map[string]string{
		"enclave-claude-0123abcd-2": "enclave-claude-0123abcd-2",
		"my_session":                "my-session",
		"-a.-b-.c":                  "a.b.c",
		strings.Repeat("x", 80):     strings.Repeat("x", 63),
	} {
		got, err := sandboxName(in)
		if err != nil || got != want {
			t.Errorf("sandboxName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := sandboxName("_"); err == nil {
		t.Error("unmappable name accepted")
	}
}

func TestEnvFileContentRejectsWhatEnvFilesCannotCarry(t *testing.T) {
	for _, env := range [][]backend.EnvVar{
		{{Name: "A", Value: "line1\nline2"}},
		{{Name: "A B", Value: "x"}},
		{{Name: "#A", Value: "x"}},
	} {
		if _, err := envFileContent(env); err == nil {
			t.Errorf("envFileContent(%+v) succeeded", env)
		}
	}
}

func TestPortSpec(t *testing.T) {
	for _, tc := range []struct {
		port backend.PortMapping
		want string
	}{
		{backend.PortMapping{ContainerPort: "8080"}, "8080"},
		{backend.PortMapping{HostPort: "9000", ContainerPort: "8080"}, "9000:8080"},
		{backend.PortMapping{HostIP: "127.0.0.1", HostPort: "0", ContainerPort: "8080", Protocol: "TCP"}, "127.0.0.1:0:8080/tcp"},
	} {
		got, err := portSpec(tc.port)
		if err != nil || got != tc.want {
			t.Errorf("portSpec(%+v) = %q, %v; want %q", tc.port, got, err, tc.want)
		}
	}
}

func TestNormalizeImageRef(t *testing.T) {
	for in, want := range map[string]string{
		"enclave-claude":                 "docker.io/library/enclave-claude:latest",
		"enclave-claude:slim":            "docker.io/library/enclave-claude:slim",
		"docker.io/library/enclave:x":    "docker.io/library/enclave:x",
		"org/img":                        "docker.io/org/img:latest",
		"localhost/enclave-claude:a":     "localhost/enclave-claude:a",
		"registry.example:5000/a/b":      "registry.example:5000/a/b:latest",
		"registry.example:5000/a/b:tag1": "registry.example:5000/a/b:tag1",
	} {
		if got := normalizeImageRef(in); got != want {
			t.Errorf("normalizeImageRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func mkdir(t *testing.T, root string, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, root string, name string, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
