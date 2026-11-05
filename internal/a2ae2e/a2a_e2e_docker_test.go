// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build docker && linux

package a2ae2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

type e2e struct {
	t       *testing.T
	engine  string
	bin     string
	project string
	env     []string
	state   string
	group   string
	prefix  string
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	engine := os.Getenv("ENCLAVE_TEST_ENGINE")
	if engine == "" {
		engine = "docker"
	}
	if _, err := exec.LookPath(engine); err != nil {
		t.Skipf("%s not found", engine)
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	base := os.Getenv("ENCLAVE_TEST_A2A_PREFIX")
	if base == "" {
		base = "a2a-e2e"
	}
	root := t.TempDir()
	e := &e2e{t: t, engine: engine, prefix: base + "-" + hex.EncodeToString(suffix), project: filepath.Join(root, "project")}
	e.group = e.prefix
	e.state = filepath.Join(root, "state")
	if err := os.MkdirAll(e.project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.project, "README.md"), []byte("a2a e2e\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		// No credentials reach a session, so nothing is billed.
		if strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_") || strings.HasPrefix(name, "ENCLAVE_") {
			continue
		}
		e.env = append(e.env, kv)
	}
	e.env = append(e.env,
		"XDG_STATE_HOME="+e.state,
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
	)
	// stop resolves a tool profile too, and has no --tool flag.
	if err := os.MkdirAll(filepath.Join(root, "config", "enclave"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "enclave", "config.json"), []byte(`{"tool": "claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.bin = os.Getenv("ENCLAVE_TEST_BIN")
	if e.bin == "" {
		e.bin = filepath.Join(root, "enclave")
		build := exec.Command("go", "build", "-o", e.bin, "./cmd/enclave")
		build.Dir = moduleRoot(t)
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build enclave: %v\n%s", err, out)
		}
	}
	t.Cleanup(e.cleanup)
	return e
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// enclave runs the binary in the project and returns stdout, stderr and the exit code.
func (e *e2e) enclave(t *testing.T, timeout time.Duration, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Dir = e.project
	cmd.Env = e.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("enclave %s: %v", strings.Join(args, " "), err)
	}
	return stdout.String(), stderr.String(), code
}

func (e *e2e) run(t *testing.T, timeout time.Duration, args ...string) string {
	t.Helper()
	stdout, stderr, code := e.enclave(t, timeout, args...)
	if code != 0 {
		t.Fatalf("enclave %s: exit %d\n%s%s", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

func (e *e2e) engineCmd(args ...string) (string, error) {
	out, err := exec.Command(e.engine, args...).CombinedOutput() // #nosec G204 -- test-chosen engine CLI.
	return strings.TrimSpace(string(out)), err
}

func (e *e2e) member(name string) string { return e.prefix + "-" + name }

func (e *e2e) start(t *testing.T, name, role string, extra ...string) {
	t.Helper()
	args := append([]string{"run", "--backend", e.engine, "--tool", "claude", "--slim", "--no-api-key",
		"--peer-group", e.group, "--name", e.member(name), "--role", role, "--background"}, extra...)
	e.run(t, 30*time.Minute, args...)
}

func (e *e2e) container(t *testing.T, name string) string {
	t.Helper()
	out, err := e.engineCmd("ps", "-a", "--filter", "label=enclave.a2a.group="+e.group,
		"--filter", "label=enclave.a2a.member="+e.member(name), "--format", "{{.Names}}")
	if err != nil || out == "" {
		t.Fatalf("no container for member %s: %v %s", name, err, out)
	}
	return strings.Split(out, "\n")[0]
}

type recordMember struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	State string `json:"state"`
}

type groupRecord struct {
	GroupGeneration string            `json:"groupGeneration"`
	Pending         json.RawMessage   `json:"pendingRevision"`
	Members         []recordMember    `json:"members"`
	Obligations     []json.RawMessage `json:"obligations"`
	Departures      []struct {
		Member string `json:"member"`
		State  string `json:"state"`
		Acks   []struct {
			Worker                 string `json:"worker"`
			AdmissionStopPersisted bool   `json:"admissionStopPersisted"`
			Termination            string `json:"termination"`
		} `json:"acks"`
	} `json:"departures"`
}

func (e *e2e) groupDir() string { return filepath.Join(e.state, "enclave", "a2a", e.group) }

func (e *e2e) record(t *testing.T) groupRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.groupDir(), "group.json"))
	if err != nil {
		t.Fatalf("read group record: %v", err)
	}
	var rec groupRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("decode group record: %v", err)
	}
	return rec
}

func (rec groupRecord) has(member string) bool {
	return slices.ContainsFunc(rec.Members, func(m recordMember) bool { return m.Name == member })
}

func (e *e2e) inventoryStores(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.groupDir(), "inventory", e.record(t).GroupGeneration+".json"))
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	var inv struct {
		Entries []struct {
			StorePath string `json:"storePath"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &inv); err != nil {
		t.Fatal(err)
	}
	var stores []string
	for _, entry := range inv.Entries {
		stores = append(stores, entry.StorePath)
	}
	return stores
}

// mcpTools lists the tools of the in-session enclave-a2a MCP server over stdio.
func (e *e2e) mcpTools(t *testing.T, container string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.engine, "exec", "-i", container, "enclave-a2a-agent", "mcp") // #nosec G204 -- test-chosen engine CLI.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Wait() }()
	defer func() { _ = stdin.Close() }()
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"a2a-e2e","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	} {
		if _, err := stdin.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var resp struct {
			ID     int `json:"id"`
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &resp) != nil || resp.ID != 2 {
			continue
		}
		var names []string
		for _, tool := range resp.Result.Tools {
			names = append(names, tool.Name)
		}
		slices.Sort(names)
		return names
	}
	t.Fatalf("no tools/list response from %s: %v", container, scanner.Err())
	return nil
}

func (e *e2e) cleanup() {
	if _, err := os.Stat(e.groupDir()); err == nil {
		_, _, _ = e.enclave(e.t, 5*time.Minute, "stop", "--backend", e.engine, "--peer-group", e.group)
	}
	if out, _ := e.engineCmd("ps", "-a", "--format", "{{.Names}}"); out != "" {
		for _, name := range strings.Split(out, "\n") {
			if strings.Contains(name, "-"+e.prefix+"-") || name == "enclave-a2a-hub-"+e.group {
				_, _ = e.engineCmd("rm", "-f", name)
			}
		}
	}
	if out, _ := e.engineCmd("network", "ls", "--format", "{{.Name}}"); out != "" {
		for _, name := range strings.Split(out, "\n") {
			if strings.Contains(name, "-"+e.prefix+"-") {
				_, _ = e.engineCmd("network", "rm", name)
			}
		}
	}
}

func TestA2APeerGroupEndToEnd(t *testing.T) {
	e := newE2E(t)

	t.Run("rejections", func(t *testing.T) {
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"--peer-group", e.group}, "requires --name"},
			{[]string{"--peer-group", e.group, "--name", e.member("x"), "--backend", "qemu"}, "qemu backend"},
			{[]string{"--peer-group", e.group, "--name", e.member("x"), "--allow-all-network"}, "--allow-all-network"},
			{[]string{"--peer-group", e.group, "--name", e.member("x"), "--no-yolo"}, "needs yolo mode"},
			{[]string{"--role", "worker"}, "requires --peer-group"},
		} {
			args := append([]string{"run", "--tool", "claude", "--slim", "--no-api-key"}, tc.args...)
			if !slices.Contains(tc.args, "--backend") {
				args = append(args, "--backend", e.engine)
			}
			_, stderr, code := e.enclave(t, 2*time.Minute, args...)
			if code != 1 || !strings.Contains(stderr, tc.want) {
				t.Errorf("%v: exit %d, stderr %q; want exit 1 mentioning %q", tc.args, code, stderr, tc.want)
			}
		}
		if _, err := os.Stat(e.groupDir()); err == nil {
			t.Errorf("a rejected run left group state in %s", e.groupDir())
		}
	})

	e.start(t, "w1", "worker")
	e.start(t, "w2", "worker", "--project-mount", "readonly")
	e.start(t, "orch", "orchestrator", "--project-mount", "readonly")
	stores := e.inventoryStores(t)

	t.Run("members", func(t *testing.T) {
		var peers struct {
			SchemaVersion string `json:"schemaVersion"`
			Kind          string `json:"kind"`
			Members       []struct {
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"members"`
		}
		if err := json.Unmarshal([]byte(e.run(t, time.Minute, "a2a", "peers", "--peer-group", e.group, "--json")), &peers); err != nil {
			t.Fatal(err)
		}
		if peers.SchemaVersion != "1" || peers.Kind != "a2a-peers" || len(peers.Members) != 3 {
			t.Fatalf("peers = %+v; want schemaVersion 1, kind a2a-peers, three members", peers)
		}
		var ps []struct {
			A2A *struct {
				Group      string `json:"group"`
				Role       string `json:"role"`
				Port       *int   `json:"port"`
				Quarantine string `json:"quarantine"`
				Departure  string `json:"departure"`
				Hub        *struct {
					Container string `json:"container"`
				} `json:"hub"`
			} `json:"a2a"`
		}
		if err := json.Unmarshal([]byte(e.run(t, time.Minute, "ps", "--backend", e.engine, "--json")), &ps); err != nil {
			t.Fatal(err)
		}
		var roles []string
		for _, s := range ps {
			if s.A2A == nil || s.A2A.Group != e.group {
				continue
			}
			roles = append(roles, s.A2A.Role)
			if (s.A2A.Role == "worker") != (s.A2A.Port != nil && *s.A2A.Port == 41821) {
				t.Errorf("%s port = %v", s.A2A.Role, s.A2A.Port)
			}
			if s.A2A.Hub == nil || s.A2A.Hub.Container != "enclave-a2a-hub-"+e.group || s.A2A.Quarantine != "no" || s.A2A.Departure != "no" {
				t.Errorf("a2a object of a %s: %+v", s.A2A.Role, s.A2A)
			}
		}
		slices.Sort(roles)
		if !slices.Equal(roles, []string{"orchestrator", "worker", "worker"}) {
			t.Errorf("ps --json roles = %v", roles)
		}
	})

	t.Run("worker serves, orchestrator has four MCP tools", func(t *testing.T) {
		args, err := e.engineCmd("exec", e.container(t, "w1"), "ps", "-eo", "args")
		if err != nil || !strings.Contains(args, "\nenclave-a2a-agent serve") {
			t.Errorf("worker processes: %v\n%s", err, args)
		}
		got := e.mcpTools(t, e.container(t, "orch"))
		if want := []string{"cancel_task", "get_task", "list_peers", "send_task"}; !slices.Equal(got, want) {
			t.Errorf("orchestrator MCP tools = %v, want %v", got, want)
		}
	})

	t.Run("plain session is unchanged", func(t *testing.T) {
		name := e.member("plain")
		e.run(t, 30*time.Minute, "run", "--backend", e.engine, "--tool", "claude", "--slim", "--no-api-key", "--name", name, "--background")
		out, err := e.engineCmd("ps", "-a", "--filter", "name=-"+name+"$", "--format", "{{.Names}}")
		if err != nil || out == "" {
			t.Fatalf("plain container: %v %q", err, out)
		}
		env, _ := e.engineCmd("exec", out, "env")
		labels, _ := e.engineCmd("inspect", "-f", "{{json .Config.Labels}}", out)
		agent, _ := e.engineCmd("exec", out, "sh", "-c", "command -v enclave-a2a-agent || true")
		skills, _ := e.engineCmd("exec", out, "sh", "-c", "ls -d /home/agent/.claude/skills/a2a-* 2>/dev/null || true")
		if strings.Contains(env, "ENCLAVE_A2A_") || strings.Contains(labels, "enclave.a2a.") || agent != "" || skills != "" {
			t.Errorf("plain session carries A2A: env A2A=%v labels A2A=%v agent=%q skills=%q",
				strings.Contains(env, "ENCLAVE_A2A_"), strings.Contains(labels, "enclave.a2a."), agent, skills)
		}
		e.run(t, 5*time.Minute, "stop", "--backend", e.engine, "--name", name)
	})

	t.Run("host send reaches the worker", func(t *testing.T) {
		stdout, stderr, code := e.enclave(t, 5*time.Minute, "a2a", "send", "--backend", e.engine, "--peer-group", e.group, "--json", e.member("w1"), "Reply ok.")
		var out struct {
			Kind        string `json:"kind"`
			Handle      string `json:"handle"`
			State       string `json:"state"`
			Termination string `json:"termination"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("send: exit %d, %v\n%s%s", code, err, stdout, stderr)
		}
		// Without a login the harness fails the task; the route is what counts.
		terminal := map[string]int{"TASK_STATE_COMPLETED": 0, "TASK_STATE_FAILED": 5}
		want, ok := terminal[out.State]
		if out.Kind != "a2a-task" || !strings.HasPrefix(out.Handle, "task:"+e.member("w1")+":") || !ok || code != want || out.Termination != "confirmed" {
			t.Errorf("send: exit %d, %+v", code, out)
		}
	})

	t.Run("orchestrator stop collects acknowledgments", func(t *testing.T) {
		e.run(t, 5*time.Minute, "stop", "--backend", e.engine, "--name", e.member("orch"))
		rec := e.record(t)
		if rec.has(e.member("orch")) || len(rec.Obligations) != 0 {
			t.Fatalf("after the orchestrator stop: members %+v, obligations %d", rec.Members, len(rec.Obligations))
		}
		var acked []string
		for _, dep := range rec.Departures {
			if dep.Member != e.member("orch") || dep.State != "complete" {
				continue
			}
			for _, ack := range dep.Acks {
				if ack.AdmissionStopPersisted && ack.Termination != "unconfirmed" {
					acked = append(acked, ack.Worker)
				}
			}
		}
		slices.Sort(acked)
		if want := []string{e.member("w1"), e.member("w2")}; !slices.Equal(acked, want) {
			t.Errorf("acknowledged by %v, want %v (departures %+v)", acked, want, rec.Departures)
		}
	})

	t.Run("reconcile after kill -9 of a joining process", func(t *testing.T) {
		name := e.member("k1")
		cmd := exec.Command(e.bin, "run", "--backend", e.engine, "--tool", "claude", "--slim", "--no-api-key",
			"--peer-group", e.group, "--name", name, "--project-mount", "readonly") // #nosec G204 -- the binary under test.
		cmd.Dir, cmd.Env = e.project, e.env
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Minute)
		for !e.record(t).has(name) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
		e.run(t, 5*time.Minute, "a2a", "reconcile", "--backend", e.engine, "--peer-group", e.group)
		rec := e.record(t)
		if rec.has(name) || (len(rec.Pending) > 0 && string(rec.Pending) != "null") {
			t.Errorf("after reconcile: members %+v, pending %s", rec.Members, rec.Pending)
		}
		networks, _ := e.engineCmd("inspect", "-f", "{{json .NetworkSettings.Networks}}", "enclave-a2a-hub-"+e.group)
		if strings.Contains(networks, "-"+name+"-net") {
			t.Errorf("hub still attached to the network of %s: %s", name, networks)
		}
	})

	t.Run("teardown deletes the inventory", func(t *testing.T) {
		e.run(t, 10*time.Minute, "stop", "--backend", e.engine, "--peer-group", e.group)
		if _, err := os.Stat(e.groupDir()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("group directory left: %v", err)
		}
		for _, store := range stores {
			if _, err := os.Stat(store); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("inventory store left: %s", store)
			}
		}
		out, _ := e.engineCmd("ps", "-a", "--format", "{{.Names}}")
		for _, name := range strings.Split(out, "\n") {
			for _, m := range []string{"w1", "w2", "orch"} {
				if strings.HasSuffix(name, "-"+e.member(m)) || name == "enclave-a2a-hub-"+e.group {
					t.Errorf("container left: %s", name)
				}
			}
		}
	})
}
