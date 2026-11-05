// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"enclave/internal/peergroup"
)

const (
	hubRulesTwoMembers = `*filter
:INPUT DROP [0:0]
:FORWARD DROP [0:0]
:OUTPUT DROP [0:0]
-A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
-A INPUT -i eth0 -s 172.18.0.2/32 -d 172.18.0.14/32 -p tcp --dport 41820 -j ACCEPT
-A INPUT -i eth1 -s 172.19.0.2/32 -d 172.19.0.14/32 -p tcp --dport 41820 -j ACCEPT
-A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
-A OUTPUT -o eth1 -d 172.19.0.2/32 -p tcp --dport 41821 -j ACCEPT
COMMIT
`
	hubRulesOrchestratorOnly = `*filter
:INPUT DROP [0:0]
:FORWARD DROP [0:0]
:OUTPUT DROP [0:0]
-A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
-A INPUT -i eth0 -s 172.18.0.2/32 -d 172.18.0.14/32 -p tcp --dport 41820 -j ACCEPT
-A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
COMMIT
`
	hubTestGroupGeneration = "3f9c0a1b2c3d4e5f"
	hubTestPriorEpoch      = "aaaaaaaaaaaaaaaa"
)

// hubRender is one canned render-rules result.
type hubRender struct {
	rules  string
	stderr string
	rc     int
}

type hubScriptResult struct {
	calls  []string
	output string
	err    error
	dir    string
}

func (r hubScriptResult) status(t *testing.T) peergroup.StatusFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.dir, "status", "supervisor.json"))
	if err != nil {
		t.Fatalf("read supervisor.json: %v", err)
	}
	var s peergroup.StatusFile
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("supervisor.json is not JSON: %v\n%s", err, data)
	}
	return s
}

func (r hubScriptResult) restored(t *testing.T, n int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.dir, "mock", "restore-"+strings.Repeat("i", n)+".v4"))
	if err != nil {
		t.Fatalf("read iptables-restore input %d: %v", n, err)
	}
	return string(data)
}

// index returns the position of the first call with the prefix, or -1.
func (r hubScriptResult) index(prefix string) int {
	return slices.IndexFunc(r.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func (r hubScriptResult) count(prefix string) int {
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// hubMockCommands are executables because sh function names cannot contain hyphens.
var hubMockCommands = map[string]string{
	"iptables-restore": `printf i >> "$MOCK/restore.n"
echo iptables-restore >> "$MOCK/calls"
cat > "$MOCK/restore-$(cat "$MOCK/restore.n").v4"
[ ! -e "$MOCK/restore.fail" ] || { echo "iptables-restore: line 5 failed" >&2; exit 1; }
`,
	"ip6tables-restore": `echo ip6tables-restore >> "$MOCK/calls"
cat > "$MOCK/restore6.v6"
`,
	"su-exec": `echo "su-exec $*" >> "$MOCK/calls"
sleep 0.2
[ -e "$MOCK/proxy.silent" ] || printf '{\n  "reader": "proxy",\n  "epoch": "%s"\n}\n' "${PROXY_EPOCH:-bbbbbbbbbbbbbbbb}" > "$DIR/status/proxy.json"
echo "proxy status written" >> "$MOCK/calls"
[ ! -e "$MOCK/proxy.exit" ] || exit 1
exec sleep 30
`,
}

func writeHubMockCommands(t *testing.T, mock string) {
	t.Helper()
	bin := filepath.Join(mock, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range hubMockCommands {
		// #nosec G306 -- the mock must be executable.
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func hubScriptFunctions(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "a2ahub-entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// Source the shipping functions without starting the supervisor on the host.
	source, ok := strings.CutSuffix(string(data), "\nmain \"$@\"\n")
	if !ok {
		t.Fatal("hub entrypoint main invocation not found")
	}
	return source
}

// runHubScript sources the entrypoint with mocked engines; renders[i] answers the i-th render-rules call.
func runHubScript(t *testing.T, renders []hubRender, invocation string, env ...string) hubScriptResult {
	t.Helper()
	dir := t.TempDir()
	mock := filepath.Join(dir, "mock")
	for _, sub := range []string{mock, filepath.Join(dir, "status"), filepath.Join(dir, "snapshots")} {
		if err := os.MkdirAll(sub, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for i, r := range renders {
		base := filepath.Join(mock, "render-"+strings.Repeat("i", i+1))
		for suffix, content := range map[string]string{".out": r.rules, ".err": r.stderr, ".rc": strings.Repeat("1", min(r.rc, 1))} {
			if err := os.WriteFile(base+suffix, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeHubMockCommands(t, mock)
	scriptPath := filepath.Join(dir, "hub.sh")
	if err := os.WriteFile(scriptPath, []byte(hubScriptFunctions(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	forward := filepath.Join(dir, "ip_forward")
	if err := os.WriteFile(forward, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `. "$1"
call() { printf '%s\n' "$*" >> "$MOCK/calls"; }
next_tick() { printf 'i' >> "$MOCK/$1.n"; cat "$MOCK/$1.n"; }
hub_mock() {
    call "hub $1"
    n=$(next_tick render)
    cat "$MOCK/render-$n.out"
    cat "$MOCK/render-$n.err" >&2
    [ ! -s "$MOCK/render-$n.rc" ]
}
ss() { call "ss $*"; }
conntrack() { call "conntrack $*"; return 1; }
arping() { call "arping $*"; }
ip() {
    printf '%s\n' '1: lo    inet 127.0.0.1/8 scope host lo' \
        '7: eth0    inet 172.18.0.14/28 brd 172.18.0.15 scope global eth0' \
        '9: eth1@if12    inet 172.19.0.14/28 brd 172.19.0.15 scope global eth1'
}
kill() {
    if [ "$1" = "-HUP" ]; then
        call "kill -HUP $2 status=$(sed -n 's/.*"appliedRevision": \([0-9]*\).*/\1/p' "$STATUS_DIR/supervisor.json")"
        return 0
    fi
    command kill "$@"
}
HUB_BIN=hub_mock
SNAPSHOT_DIR="$DIR/snapshots"
STATUS_DIR="$DIR/status"
WORK_DIR="$DIR/work"
IP_FORWARD_FILE="$DIR/ip_forward"
prepare_work() { mkdir -p "$WORK_DIR"; : > "$WORK_DIR/endpoints"; EPOCH=0123456789abcdef; }
`+invocation, "a2ahub-supervisor-test", scriptPath)
	cmd.Env = append([]string{
		"PATH=" + filepath.Join(mock, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DIR=" + dir,
		"MOCK=" + mock,
		"ENCLAVE_A2A_GROUP_GENERATION=" + hubTestGroupGeneration,
	}, env...)
	out, runErr := cmd.CombinedOutput()
	if len(out) > 0 {
		t.Logf("supervisor output: %s", out)
	}
	res := hubScriptResult{output: string(out), err: runErr, dir: dir}
	if data, err := os.ReadFile(filepath.Join(mock, "calls")); err == nil {
		res.calls = strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	return res
}

func TestA2AHubStartDeniesBeforeReadingAndWaitsForProxy(t *testing.T) {
	res := runHubScript(t, []hubRender{{rules: hubRulesTwoMembers, stderr: "revision=1\n"}}, `
printf '{\n  "epoch": "`+hubTestPriorEpoch+`"\n}\n' > "$STATUS_DIR/proxy.json"
start_hub
command kill "$PROXY_PID"
`)
	if res.err != nil {
		t.Fatalf("start_hub: %v", res.err)
	}
	order := []string{"iptables-restore", "ip6tables-restore", "su-exec 65532:65532 env ENCLAVE_A2A_HUB_EPOCH=", "proxy status written", "hub render-rules", "iptables-restore", "arping"}
	last := -1
	for _, want := range order {
		idx := slices.IndexFunc(res.calls[last+1:], func(c string) bool { return strings.HasPrefix(c, want) })
		if idx < 0 {
			t.Fatalf("calls %q: missing %q after position %d", res.calls, want, last)
		}
		last += idx + 1
	}
	deny := res.restored(t, 1)
	for _, line := range []string{":INPUT DROP [0:0]", ":FORWARD DROP [0:0]", ":OUTPUT DROP [0:0]", "-A INPUT -i lo -j ACCEPT", "-A OUTPUT -o lo -j ACCEPT"} {
		if !strings.Contains(deny, line+"\n") {
			t.Fatalf("default deny lacks %q:\n%s", line, deny)
		}
	}

	status := res.status(t)
	if status.Reader != "supervisor" || status.SchemaVersion != "1" || status.GroupGeneration != hubTestGroupGeneration {
		t.Fatalf("status identity = %+v", status)
	}
	if status.AppliedRevision != 1 || status.RejectedRevision != 0 || status.Error != "" {
		t.Fatalf("status = %+v, want applied 1", status)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(status.Epoch) || status.Epoch == hubTestPriorEpoch {
		t.Fatalf("epoch = %q", status.Epoch)
	}
	if !slices.ContainsFunc(res.calls, func(c string) bool {
		return strings.Contains(c, "ENCLAVE_A2A_HUB_EPOCH="+status.Epoch+" hub_mock proxy")
	}) {
		t.Fatalf("proxy not started with the supervisor epoch %s: %q", status.Epoch, res.calls)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`).MatchString(status.UpdatedAt) {
		t.Fatalf("updatedAt = %q", status.UpdatedAt)
	}
}

func TestA2AHubStartFailsWhenProxyWritesNoStatus(t *testing.T) {
	cases := map[string]string{
		"proxy exits":       `touch "$MOCK/proxy.silent" "$MOCK/proxy.exit"`,
		"stale epoch":       `PROXY_EPOCH=` + hubTestPriorEpoch + `; export PROXY_EPOCH`,
		"no status written": `touch "$MOCK/proxy.silent"`,
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			res := runHubScript(t, nil, setup+`
printf '{\n  "epoch": "`+hubTestPriorEpoch+`"\n}\n' > "$STATUS_DIR/proxy.json"
PROXY_STATUS_TICKS=5
if start_hub; then rc=0; else rc=1; fi
command kill "$PROXY_PID" 2>/dev/null || true
exit "$rc"
`)
			if res.err == nil {
				t.Fatal("start_hub succeeded without a fresh proxy status")
			}
			if res.index("hub render-rules") >= 0 {
				t.Fatalf("rules applied before the proxy reported: %q", res.calls)
			}
		})
	}
}

func TestA2AHubStartRefusesForwarding(t *testing.T) {
	res := runHubScript(t, nil, `
printf '1\n' > "$IP_FORWARD_FILE"
chmod 0444 "$IP_FORWARD_FILE"
start_hub
`)
	if res.err == nil || !strings.Contains(res.output, "forwarding is enabled") {
		t.Fatalf("start_hub with forwarding on: err=%v output=%s", res.err, res.output)
	}
	if res.index("su-exec") >= 0 {
		t.Fatalf("proxy started with forwarding on: %q", res.calls)
	}

	res = runHubScript(t, nil, `
printf '1\n' > "$IP_FORWARD_FILE"
ensure_forwarding_off
cat "$IP_FORWARD_FILE"
`)
	if res.err != nil || !strings.HasSuffix(strings.TrimSpace(res.output), "0") {
		t.Fatalf("writable ip_forward not disabled: err=%v output=%s", res.err, res.output)
	}
}

func TestA2AHubApplyFlushesRemovedEndpointsAfterRules(t *testing.T) {
	renders := []hubRender{
		{rules: hubRulesTwoMembers, stderr: "revision=1\n"},
		{rules: hubRulesOrchestratorOnly, stderr: "revision=2\n"},
	}
	res := runHubScript(t, renders, `
prepare_work
apply_current
: > "$MOCK/calls"
apply_current
`)
	if res.err != nil {
		t.Fatalf("apply: %v", res.err)
	}
	want := []string{
		"hub render-rules",
		"iptables-restore",
		"ss -K dst 172.19.0.2",
		"conntrack -D -s 172.19.0.2",
		"conntrack -D -d 172.19.0.2",
	}
	if !slices.Equal(res.calls, want) {
		t.Fatalf("removal calls = %q, want %q", res.calls, want)
	}
	if s := res.status(t); s.AppliedRevision != 2 || s.RejectedRevision != 0 {
		t.Fatalf("status = %+v, want applied 2", s)
	}
}

func TestA2AHubApplyKeepsLoopbackOpen(t *testing.T) {
	res := runHubScript(t, []hubRender{{rules: hubRulesOrchestratorOnly, stderr: "revision=1\n"}}, `
prepare_work
apply_current
`)
	if res.err != nil {
		t.Fatalf("apply: %v", res.err)
	}
	rules := res.restored(t, 1)
	want := "-I INPUT 1 -i lo -j ACCEPT\n-I OUTPUT 1 -o lo -j ACCEPT\nCOMMIT\n"
	if !strings.HasSuffix(rules, want) {
		t.Fatalf("applied rules do not end with loopback accepts:\n%s", rules)
	}
}

func TestA2AHubRejectedRevisionKeepsAppliedRules(t *testing.T) {
	escaped := `manifest hash mismatch for "secrets.json" \ revision 2`
	cases := []struct {
		name      string
		render    hubRender
		setup     string
		wantError string
		restores  int
	}{
		{
			name:      "render refused",
			render:    hubRender{stderr: "revision=2\n" + escaped + "\n", rc: 1},
			wantError: escaped,
			restores:  1,
		},
		{
			name:      "render refused without revision",
			render:    hubRender{stderr: "groupGeneration mismatch\n", rc: 1},
			setup:     `ln -s 2 "$SNAPSHOT_DIR/current"`,
			wantError: "groupGeneration mismatch",
			restores:  1,
		},
		{
			name:      "open policy",
			render:    hubRender{rules: strings.Replace(hubRulesOrchestratorOnly, ":OUTPUT DROP", ":OUTPUT ACCEPT", 1), stderr: "revision=2\n"},
			wantError: "rendered rules do not set OUTPUT policy DROP",
			restores:  1,
		},
		{
			name:      "extra table",
			render:    hubRender{rules: "*nat\nCOMMIT\n" + hubRulesOrchestratorOnly, stderr: "revision=2\n"},
			wantError: "rendered rules must contain only the filter table",
			restores:  1,
		},
		{
			name:      "iptables-restore fails",
			render:    hubRender{rules: hubRulesOrchestratorOnly, stderr: "revision=2\n"},
			setup:     `touch "$MOCK/restore.fail"`,
			wantError: "iptables-restore failed: iptables-restore: line 5 failed",
			restores:  2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			renders := []hubRender{
				{rules: hubRulesTwoMembers, stderr: "revision=1\n"},
				tc.render,
				{rules: hubRulesOrchestratorOnly, stderr: "revision=3\n"},
			}
			res := runHubScript(t, renders, `
prepare_work
apply_current
`+tc.setup+`
apply_current
cp "$STATUS_DIR/supervisor.json" "$DIR/rejected.json"
rm -f "$MOCK/restore.fail"
apply_current
`)
			if res.err != nil {
				t.Fatalf("apply: %v", res.err)
			}
			data, err := os.ReadFile(filepath.Join(res.dir, "rejected.json"))
			if err != nil {
				t.Fatal(err)
			}
			var rejected peergroup.StatusFile
			if err := json.Unmarshal(data, &rejected); err != nil {
				t.Fatalf("rejected status is not JSON: %v\n%s", err, data)
			}
			if rejected.AppliedRevision != 1 || rejected.RejectedRevision != 2 || rejected.Error != tc.wantError {
				t.Fatalf("rejected status = %+v, want applied 1, rejected 2, error %q", rejected, tc.wantError)
			}
			if got := res.count("iptables-restore"); got != tc.restores+1 {
				t.Fatalf("iptables-restore ran %d times, want %d: %q", got, tc.restores+1, res.calls)
			}
			// The flush diff is against revision 1, the last applied one.
			if res.count("ss -K dst 172.19.0.2") != 1 {
				t.Fatalf("removed endpoint not flushed exactly once after the rejection: %q", res.calls)
			}
			if s := res.status(t); s.AppliedRevision != 3 || s.RejectedRevision != 0 || s.Error != "" {
				t.Fatalf("status after recovery = %+v", s)
			}
		})
	}
}

func TestA2AHubReloadSignalsProxyAfterStatus(t *testing.T) {
	res := runHubScript(t, []hubRender{
		{rules: hubRulesTwoMembers, stderr: "revision=4\n"},
		{stderr: "revision=5\nbad\n", rc: 1},
	}, `
prepare_work
PROXY_PID=4242
reload_hub
reload_hub
`)
	if res.err != nil {
		t.Fatalf("reload: %v", res.err)
	}
	want := []string{"kill -HUP 4242 status=4", "kill -HUP 4242 status=4"}
	var got []string
	for _, c := range res.calls {
		if strings.HasPrefix(c, "kill -HUP") {
			got = append(got, c)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("proxy signals = %q, want %q (calls %q)", got, want, res.calls)
	}
	arps := []string{"arping -U -c 1 -w 1 -I eth0 172.18.0.14", "arping -U -c 1 -w 1 -I eth1 172.19.0.14"}
	for _, arp := range arps {
		if res.count(arp) != 2 {
			t.Fatalf("gratuitous ARP %q not sent on both reloads: %q", arp, res.calls)
		}
	}
	if res.count("arping -U -c 1 -w 1 -I lo") != 0 {
		t.Fatalf("gratuitous ARP on loopback: %q", res.calls)
	}
	if hup := res.index("kill -HUP"); hup < res.index("iptables-restore") {
		t.Fatalf("proxy signalled before rules were applied: %q", res.calls)
	}
}
