// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	"enclave/internal/protectedauth"
	"enclave/internal/util"
)

var adapterMarkers = map[string]string{
	protectedauth.AdapterAnthropic: accessHandlePrefix,
	protectedauth.AdapterOpenAI:    openaiHandleMarker,
	protectedauth.AdapterMistral:   mistralHandleMarker,
	protectedauth.AdapterCopilot:   copilotHandlePrefix,
}

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("the engine check script needs bash")
	}
}

// engineCheck calls a function of the engine check script.
func engineCheck(t *testing.T, args ...string) string {
	t.Helper()
	requireBash(t)
	script := filepath.Join("..", "..", "..", "scripts", "protected-auth-engine-check.sh")
	cmd := exec.Command("bash", append([]string{"-c", `. "$1" && shift && "$@"`, "engine-check", script}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

// The script mirrors the protected tool table; a drifted copy would check the
// wrong files.
func TestEngineCheckToolTableMatchesProtectedAuth(t *testing.T) {
	var names []string
	for _, tool := range protectedauth.Tools() {
		names = append(names, tool.Name)
		if got := engineCheck(t, "tool_config_dir", tool.Name); got != tool.ConfigDir {
			t.Errorf("%s config dir = %q, want %q", tool.Name, got, tool.ConfigDir)
		}
		if got := engineCheck(t, "tool_adapters", tool.Name); got != strings.Join(tool.Adapters, " ") {
			t.Errorf("%s adapters = %q, want %v", tool.Name, got, tool.Adapters)
		}
		want := ""
		switch {
		case tool.StorageDirEnv != "":
			want = "/home/agent/.enclave-auth/.credentials.json"
		case len(tool.Files) > 0:
			want = "/home/agent/" + tool.ConfigDir + "/" + tool.Files[0].ConfigPath
		}
		if got := engineCheck(t, "tool_native_file", tool.Name); got != want {
			t.Errorf("%s native file = %q, want %q", tool.Name, got, want)
		}
		for _, adapter := range tool.Adapters {
			if got := engineCheck(t, "adapter_marker", adapter); got != adapterMarkers[adapter] {
				t.Errorf("%s marker = %q, want %q", adapter, got, adapterMarkers[adapter])
			}
		}
	}
	if got := engineCheck(t, "eval", `echo "$ALL_TOOLS"`); got != strings.Join(names, " ") {
		t.Errorf("ALL_TOOLS = %q, want %q", got, strings.Join(names, " "))
	}
}

func TestEngineCheckViewHashIgnoresTerminalControlBytes(t *testing.T) {
	want := strings.Repeat("a", 64)
	t.Setenv("TEST_ENGINE_HASH", want)
	got := engineCheck(t, "eval", `
PROJECT=unused
enclave_in() { printf '\0%s\r\n' "$TEST_ENGINE_HASH"; }
view_hash
`)
	if got != want {
		t.Fatalf("view_hash = %q, want %q", got, want)
	}
}

func TestEngineCheckUnrelatedTLSRequiresPassthroughAudit(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("audit checks need jq")
	}
	for _, test := range []struct {
		name, rule, code, want string
	}{
		{"opaque handle passes", "allowlist", "200", "PASS"},
		{"MITM is not passthrough", "mitm-dispatch", "200", "FAIL"},
		{"missing audit", "missing", "200", "FAIL"},
		{"upstream refusal is still passthrough", "allowlist", "403", "PASS"},
		{"unreachable upstream", "allowlist", "000", "FAIL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TEST_ENGINE_STATE", t.TempDir())
			t.Setenv("TEST_ENGINE_RULE", test.rule)
			t.Setenv("TEST_ENGINE_CODE", test.code)
			got := engineCheck(t, "eval", `
TOOL=claude STATE=$TEST_ENGINE_STATE PROJECT=$TEST_ENGINE_STATE PROJECT_HASH=probe
ensure_grant() { :; }
record() { printf '%s:%s\n' "$1" "$2"; }
protected_shell() {
    case "$*" in
        *'--name check-unrelated'*)
            local log="$STATE/projects/$PROJECT_HASH/$TOOL/logs/network.log"
            mkdir -p "$(dirname "$log")"
            touch "$log"
            if [ "$TEST_ENGINE_RULE" != missing ]; then
                printf '{"type":"tcp","verdict":"pass","rule":"allowlist","session":"%s"}\n' "$(container_name check-unrelated)" >> "$log"
                if [ "$TEST_ENGINE_RULE" = mitm-dispatch ]; then
                    printf '{"type":"tcp","verdict":"pass","rule":"mitm-dispatch","session":"%s"}\n' "$(container_name check-unrelated)" >> "$log"
                fi
            fi
            printf 'unrelated=200\nunrelated-handle=%s\n' "$TEST_ENGINE_CODE"
            ;;
        *) printf '%s\n' pasted-key=403 pasted-token=403 token-lookalike=403 revoke=403 api-key-exchange=403 ;;
    esac
}
check_release_refusals
`)
			if got != test.want+":release-refusals" {
				t.Fatalf("release check = %q, want %s", got, test.want)
			}
		})
	}
}

func TestEngineCheckConcurrencyRequiresFailedResetAndLiveOwnerReason(t *testing.T) {
	for _, test := range []struct {
		message, status, want string
	}{
		{"auth identity has active session reservations", "1", "PASS"},
		{"auth identity is in use", "1", "PASS"},
		{"permission denied", "1", "FAIL"},
		{"auth identity is in use", "0", "FAIL"},
	} {
		t.Run(test.message+test.status, func(t *testing.T) {
			t.Setenv("TEST_ENGINE_STATE", t.TempDir())
			t.Setenv("TEST_ENGINE_MESSAGE", test.message)
			t.Setenv("TEST_ENGINE_STATUS", test.status)
			got := engineCheck(t, "eval", `
TOOL=claude STATE=$TEST_ENGINE_STATE PROJECT=$TEST_ENGINE_STATE PROJECT_HASH=probe ENGINE=docker
ensure_background() { :; }
reset_test_cli() { printf '%s\n' "$TEST_ENGINE_MESSAGE" >&2; return "$TEST_ENGINE_STATUS"; }
CLI_BIN=reset_test_cli
record() { printf '%s:%s\n' "$1" "$2"; }
mkdir -p "$(identity_dir check-a)"
touch "$(identity_dir check-a)/oauth-grant.json"
check_concurrency
`)
			if got != test.want+":concurrency" {
				t.Fatalf("concurrency check = %q, want %s", got, test.want)
			}
		})
	}
}

// The engine check reads identity metadata where the host keeps it.
func TestEngineCheckFindsIdentityMetadata(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	dir, err := hoststore.ResolveDir(home, backend.StoreKey{Owner: "codex", Suffix: "check-a"}, backend.StoreKindAuth)
	if err != nil {
		t.Fatal(err)
	}
	requireBash(t)
	script := filepath.Join("..", "..", "..", "scripts", "protected-auth-engine-check.sh")
	output, err := exec.Command("bash", "-c", `. "$1"; STATE="$2"; identity_meta "$3"`, "engine-check", script, config.HostStateRootDir(home), dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(output)), hoststore.IdentityStatePath(home, dir); got != want {
		t.Fatalf("identity_meta = %q, want %q", got, want)
	}
}

// The engine check seeds these grants into an enrolled identity; a drifted
// schema would make every later check fail for the wrong reason.
func TestEngineCheckSyntheticGrantsAreAccepted(t *testing.T) {
	for _, tool := range protectedauth.Tools() {
		if len(tool.Adapters) == 0 {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			b := fixtureBroker(t)
			config := b.config
			config.Tool = tool.Name
			config.Identity = util.HashString(config.StoreDir)
			config.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
			if err := os.Mkdir(config.RuntimeDir, 0o700); err != nil {
				t.Fatal(err)
			}
			engineCheck(t, "synthetic_grants", tool.Name, config.StoreDir, strconv.FormatInt(b.now().Unix(), 10))
			reopened, err := Open(config)
			if err != nil {
				t.Fatalf("gateway rejected the engine check's grants: %v", err)
			}
			reopened.now = b.now
			view := filepath.Join(t.TempDir(), "view")
			if err := os.Mkdir(view, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := reopened.InitializeView(view); err != nil {
				t.Fatalf("gateway rejected the engine check's grants: %v", err)
			}
			var rendered strings.Builder
			entries, err := os.ReadDir(view)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(view, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				rendered.Write(data)
			}
			for _, adapter := range tool.Adapters {
				if !strings.Contains(rendered.String(), adapterMarkers[adapter]) {
					t.Errorf("view lacks the %s handle: %s", adapter, rendered.String())
				}
			}
			for _, marker := range []string{"synthetic-engine-check", "syntheticenginecheck"} {
				if strings.Contains(rendered.String(), marker) {
					t.Fatalf("view carries a synthetic secret: %s", rendered.String())
				}
			}
		})
	}
}

// The leak scan must fail on any matching file, not only the native one, and
// on a scan that never completed.
func TestEngineCheckHandlesOnlyNeedsEvidence(t *testing.T) {
	native := `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-handle"}}`
	for name, tc := range map[string]struct {
		output, want string
	}{
		"clean":           {native + "\nscan-done\n", "PASS: claude/handles-only"},
		"leak elsewhere":  {native + "\nLEAK: /tmp/leaked-grant.json\nscan-done\n", "FAIL: claude/handles-only"},
		"scan incomplete": {native + "\n", "FAIL: claude/handles-only"},
	} {
		t.Run(name, func(t *testing.T) {
			requireBash(t)
			script := filepath.Join("..", "..", "..", "scripts", "protected-auth-engine-check.sh")
			cmd := exec.Command("bash", "-c", `. "$1"; TOOL=claude PROJECT=/project; ensure_grant() { :; }; protected_shell() { printf '%s' "$OUTPUT"; }; check_handles_only`, "engine-check", script)
			cmd.Env = append(os.Environ(), "OUTPUT="+tc.output)
			output, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(output), tc.want) {
				t.Fatalf("check reported %q, want %q", output, tc.want)
			}
		})
	}
}
