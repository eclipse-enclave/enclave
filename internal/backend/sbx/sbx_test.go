// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package sbx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/backend/qemu"
	"enclave/internal/model"
)

type call struct {
	kind string // output, run, attach
	args []string
}

type fakeRunner struct {
	calls      []call
	outputs    map[string]string // keyed by joined args
	runErr     map[string]error  // keyed by verb
	runHook    func(args []string)
	attachCode int
}

func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{"output", args})
	return []byte(f.outputs[strings.Join(args, " ")]), nil
}

func (f *fakeRunner) Run(_ context.Context, _ io.Writer, args ...string) error {
	f.calls = append(f.calls, call{"run", args})
	if f.runHook != nil {
		f.runHook(args)
	}
	return f.runErr[args[0]]
}

func (f *fakeRunner) Attach(args []string, _ backend.AttachIO) (int, error) {
	f.calls = append(f.calls, call{"attach", args})
	return f.attachCode, nil
}

func (f *fakeRunner) verbs() []string {
	var out []string
	for _, c := range f.calls {
		verb := c.kind + ":" + c.args[0]
		if c.args[0] == "template" || (c.args[0] == "exec" && c.kind == "run") {
			verb += " " + c.args[1]
		}
		out = append(out, verb)
	}
	return out
}

type fakeImages struct {
	img   ImageConfig
	saved []string
}

func (f *fakeImages) Inspect(context.Context, string) (ImageConfig, error) { return f.img, nil }

func (f *fakeImages) Save(_ context.Context, ref string, path string) error {
	f.saved = append(f.saved, ref)
	return os.WriteFile(path, []byte("tar"), 0o600)
}

const loadedTemplates = `[{"id":"sha256:aaa","repository":"docker.io/library/enclave-claude","tag":"latest"}]`

func newTestBackend(t *testing.T, cli *fakeRunner, images *fakeImages) *Backend {
	t.Helper()
	t.Cleanup(func() { lookPath = defaultLookPath })
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	home := t.TempDir()
	host := model.Host{Home: home}
	return &Backend{
		opts:   Options{Host: host, StateDir: filepath.Join(home, "sbx-state")},
		cli:    cli,
		images: images,
		stores: qemu.New(qemu.Options{Host: host}),
	}
}

var defaultLookPath = lookPath

func testRequest(t *testing.T) backend.Request {
	t.Helper()
	project := t.TempDir()
	return backend.Request{
		Session:    backend.SessionMeta{Tool: "claude", Name: "enclave-claude-abc-1", Worktree: project},
		Image:      "enclave-claude:latest",
		Argv:       []string{"claude"},
		WorkingDir: project,
		Mounts:     []backend.Mount{{Type: backend.MountTypeBind, Source: project, ContainerPath: project}},
		Network:    backend.NetworkPolicy{Mode: backend.NetworkModeUnrestricted},
	}
}

func TestRunCreatesSandboxExecsSessionAndRemovesIt(t *testing.T) {
	cli := &fakeRunner{outputs: map[string]string{"template ls --json": loadedTemplates}, attachCode: 3}
	images := &fakeImages{img: ImageConfig{ID: "sha256:host1", Entrypoint: []string{"/entry"}}}
	b := newTestBackend(t, cli, images)
	b.writeImageStamps(map[string]string{"enclave-claude:latest": "sha256:host1"})

	started := 0
	status, err := b.Run(context.Background(), testRequest(t), backend.AttachIO{OnStarted: func() { started++ }})

	var exitErr *backend.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 3 || status.Code != 3 {
		t.Fatalf("Run = %+v, %v; want exit status 3", status, err)
	}
	if started != 1 {
		t.Fatalf("OnStarted called %d times", started)
	}
	want := []string{"output:template ls", "run:create", "attach:exec", "run:rm"}
	if got := cli.verbs(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if len(images.saved) != 0 {
		t.Fatalf("unchanged image was exported again")
	}
	if rm := cli.calls[3].args; strings.Join(rm, " ") != "rm -f enclave-claude-abc-1" {
		t.Fatalf("remove args = %q", rm)
	}
}

func TestRunLoadsImageWhenHostImageChanged(t *testing.T) {
	cli := &fakeRunner{outputs: map[string]string{"template ls --json": loadedTemplates}}
	images := &fakeImages{img: ImageConfig{ID: "sha256:host2", Cmd: []string{"bash"}}}
	b := newTestBackend(t, cli, images)
	b.writeImageStamps(map[string]string{"enclave-claude:latest": "sha256:host1"})

	if _, err := b.Run(context.Background(), testRequest(t), backend.AttachIO{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(images.saved) != 1 {
		t.Fatalf("image exported %d times, want 1", len(images.saved))
	}
	if got := cli.verbs()[0]; got != "run:template load" {
		t.Fatalf("first call = %s, want template load", got)
	}
	if got := b.readImageStamps()["enclave-claude:latest"]; got != "sha256:host2" {
		t.Fatalf("stamp = %q, want the new image ID", got)
	}
}

func TestRunReloadsImageMissingFromSbx(t *testing.T) {
	cli := &fakeRunner{outputs: map[string]string{"template ls --json": `[]`}}
	images := &fakeImages{img: ImageConfig{ID: "sha256:host1", Cmd: []string{"bash"}}}
	b := newTestBackend(t, cli, images)
	b.writeImageStamps(map[string]string{"enclave-claude:latest": "sha256:host1"})

	if _, err := b.Run(context.Background(), testRequest(t), backend.AttachIO{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(images.saved) != 1 {
		t.Fatalf("image exported %d times, want 1", len(images.saved))
	}
}

func TestRunKeepsSandboxItDidNotCreate(t *testing.T) {
	cli := &fakeRunner{
		outputs: map[string]string{"template ls --json": loadedTemplates},
		runErr:  map[string]error{"create": errors.New("sandbox already exists")},
	}
	b := newTestBackend(t, cli, &fakeImages{img: ImageConfig{ID: "sha256:host1", Cmd: []string{"bash"}}})
	b.writeImageStamps(map[string]string{"enclave-claude:latest": "sha256:host1"})

	_, err := b.Run(context.Background(), testRequest(t), backend.AttachIO{OnStarted: func() { t.Fatal("OnStarted after a failed create") }})
	if err == nil || !strings.Contains(err.Error(), "sbx rm enclave-claude-abc-1") {
		t.Fatalf("err = %v, want a hint naming the sandbox", err)
	}
	for _, c := range cli.calls {
		if c.args[0] == "rm" {
			t.Fatalf("removed a sandbox this run did not create")
		}
	}
}

func TestRunInterruptedCreateCleanup(t *testing.T) {
	for _, tc := range []struct {
		name       string
		createErr  error
		wantRemove bool
	}{
		{name: "existing sandbox", createErr: errors.New("sandbox already exists")},
		{name: "ambiguous cancellation", createErr: context.Canceled},
		{name: "successful create", wantRemove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cli := &fakeRunner{
				outputs: map[string]string{"template ls --json": loadedTemplates},
				runErr:  map[string]error{"create": tc.createErr},
				runHook: func(args []string) {
					if args[0] == "create" {
						cancel()
					}
				},
			}
			b := newTestBackend(t, cli, &fakeImages{img: ImageConfig{ID: "sha256:host1", Cmd: []string{"bash"}}})
			b.writeImageStamps(map[string]string{"enclave-claude:latest": "sha256:host1"})

			_, err := b.Run(ctx, testRequest(t), backend.AttachIO{OnStarted: func() { t.Fatal("OnStarted after cancellation") }})
			if !errors.Is(err, backend.ErrInterrupted) {
				t.Fatalf("Run = %v, want ErrInterrupted", err)
			}
			removed := false
			for _, c := range cli.calls {
				if c.args[0] == "rm" {
					removed = true
				}
			}
			if removed != tc.wantRemove {
				t.Fatalf("sandbox removed = %v, want %v", removed, tc.wantRemove)
			}
		})
	}
}

func TestRunRejectsDetachedSessions(t *testing.T) {
	b := newTestBackend(t, &fakeRunner{}, &fakeImages{})
	req := testRequest(t)
	req.Detached = true
	if _, err := b.Run(context.Background(), req, backend.AttachIO{}); err == nil {
		t.Fatal("detached request accepted")
	}
}

func TestRunCopiesWritableFileMountsInAndBack(t *testing.T) {
	req := testRequest(t)
	npmrc := filepath.Join(t.TempDir(), "npmrc")
	if err := os.WriteFile(npmrc, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	req.Mounts = append(req.Mounts, backend.Mount{Type: backend.MountTypeBind, Source: npmrc, ContainerPath: "/home/agent/.npmrc"})

	var hostStageDir string
	cli := &fakeRunner{outputs: map[string]string{"template ls --json": loadedTemplates}}
	cli.runHook = func(args []string) {
		switch {
		case args[0] == "create":
			for _, arg := range args {
				if strings.HasSuffix(arg, ":"+guestFileStageDir) {
					hostStageDir = strings.TrimSuffix(arg, ":"+guestFileStageDir)
				}
			}
		case args[0] == "exec" && strings.Contains(strings.Join(args, " "), copyOutScript):
			// The session changed the file; copy-out lands it in the staging dir.
			if err := os.WriteFile(filepath.Join(hostStageDir, "00", "npmrc"), []byte("after\n"), 0o600); err != nil {
				t.Errorf("simulate copy-out: %v", err)
			}
		}
	}
	b := newTestBackend(t, cli, &fakeImages{img: ImageConfig{ID: "sha256:host1", Cmd: []string{"bash"}}})
	b.writeImageStamps(map[string]string{"enclave-claude:latest": "sha256:host1"})

	if _, err := b.Run(context.Background(), req, backend.AttachIO{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"output:template ls", "run:create", "run:exec -u", "attach:exec", "run:exec -u", "run:rm"}
	if got := cli.verbs(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	copyIn := strings.Join(cli.calls[2].args, " ")
	if !strings.Contains(copyIn, copyInScript) || !strings.HasSuffix(copyIn, guestFileStageDir+"/00/npmrc /home/agent/.npmrc") {
		t.Fatalf("copy-in args = %q", cli.calls[2].args)
	}
	data, err := os.ReadFile(npmrc)
	if err != nil || string(data) != "after\n" {
		t.Fatalf("host file = %q, %v; want the session's change", data, err)
	}
	if info, err := os.Stat(npmrc); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("host file mode changed: %v", info.Mode())
	}
}

func TestListReportsEnclaveSandboxesOnly(t *testing.T) {
	hash := strings.Repeat("ab", model.HashLength/2)
	entries := []sandboxEntry{
		{Name: "enclave-claude-" + hash + "-1", Status: "running"},
		{Name: "enclave-claude-" + hash + "-2", Status: "stopped"},
		{Name: "enclave-codex-" + hash + "-1", Status: "running"},
		{Name: "claude-myproject", Status: "running"},
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	b := newTestBackend(t, &fakeRunner{outputs: map[string]string{"ls --json": string(data)}}, &fakeImages{})

	all, err := b.List(context.Background(), backend.SessionFilter{All: true, NamePrefix: "enclave-claude-" + hash + "-"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 || all[0].Name != "1" || all[1].Name != "2" || all[0].Tool != "claude" || all[0].ProjectHash != hash {
		t.Fatalf("prefix listing = %+v", all)
	}
	running, err := b.List(context.Background(), backend.SessionFilter{RunningOnly: true, Tool: "claude"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(running) != 1 || running[0].Ref.Name != "enclave-claude-"+hash+"-1" {
		t.Fatalf("running listing = %+v", running)
	}
}
