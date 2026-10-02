// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeInstallerUsesPrivateNPMWrapper(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home with spaces")
	bin := pathWithCommands(t, home, "bash", "cat", "chmod", "dirname", "ln", "mkdir", "mktemp", "rm", "which")
	nodeRoot := filepath.Join(home, "private-node")
	node := filepath.Join(nodeRoot, "bin", "node")
	writeClaudeInstallStub(t, node, claudeInstallNodeStub)
	for _, name := range []string{"npm-cli.js", "npx-cli.js"} {
		writeFile(t, filepath.Join(nodeRoot, "lib", "node_modules", "npm", "bin", name), "fixture entrypoint\n")
	}
	for name, script := range map[string]string{
		"curl":                      claudeInstallCurlStub,
		"sha512sum":                 claudeInstallChecksumStub,
		"jq":                        claudeInstallJQStub,
		"enclave-agent-npm-install": claudeInstallSandboxStub,
	} {
		writeClaudeInstallStub(t, filepath.Join(bin, name), script)
	}
	env := []string{
		"HOME=" + home,
		"PATH=" + filepath.Join(home, ".local", "bin") + ":" + bin,
		"ENCLAVE_AGENT_NODE_DIR=" + nodeRoot,
		"ENCLAVE_AGENT_NODE_BIN=" + node,
		"ENCLAVE_AGENT_NPM_BIN=" + filepath.Join(nodeRoot, "bin", "npm"),
		"npm_config_prefix=" + filepath.Join(home, ".local"),
		"TMPDIR=" + home,
	}
	run := func(t *testing.T, script string) {
		t.Helper()
		cmd := exec.Command("bash", script)
		cmd.Env = env
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", script, err, output)
		}
	}
	// Generate the real shell wrapper: a stub npm executable would miss the
	// regression where the installer asks node to parse that wrapper as JS.
	run(t, filepath.Join("..", "..", "runtime-assets", "build-scripts", "install-agent-node-runtime.sh"))
	installer := filepath.Join("..", "..", "extensions", "tools", "claude", "install.sh")
	protectedPrefix := filepath.Join(home, ".local", "share", "enclave", "claude-2.1.63")
	launcher := filepath.Join(protectedPrefix, "bin", "claude")
	for _, name := range []string{"fresh", "reinstall"} {
		t.Run(name, func(t *testing.T) {
			run(t, installer)
			info, err := os.Lstat(launcher)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				t.Fatalf("protected launcher is not an executable regular file: info=%v err=%v", info, err)
			}
			assertFileContent(t, filepath.Join(protectedPrefix, "lib", "node_modules", "@anthropic-ai", "claude-code", "cli.js"), "fixture pinned client\n")
			assertFileContent(t, filepath.Join(home, ".local", "lib", "node_modules", "@anthropic-ai", "sandbox-runtime", "package.json"), "{}\n")
			if _, err := os.Stat(filepath.Join(protectedPrefix, "lib", "node_modules", "@anthropic-ai", "sandbox-runtime")); !os.IsNotExist(err) {
				t.Fatalf("sandbox runtime leaked into protected prefix: %v", err)
			}
			archives, err := filepath.Glob(filepath.Join(home, "*.tgz"))
			if err != nil || len(archives) != 0 {
				t.Fatalf("download archive was not removed: %v, %v", archives, err)
			}
		})
	}
	assertFileContent(t, filepath.Join(home, "pinned-client.log"), strings.Repeat("--setting-sources\nuser\n--version\n", 2))
	assertFileContent(t, filepath.Join(home, "native-client.log"), strings.Repeat("--version\n", 2))
	assertFileContent(t, filepath.Join(home, "npm-prefix.log"), strings.Repeat(protectedPrefix+"\n", 2))
	assertFileContent(t, filepath.Join(home, "sandbox-prefix.log"), strings.Repeat(filepath.Join(home, ".local")+"\n", 2))
}

func writeClaudeInstallStub(t *testing.T, path, body string) {
	t.Helper()
	writeFile(t, path, "#!/bin/sh\nset -eu\n"+body)
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

const claudeInstallNodeStub = `
case "$1" in
    "$ENCLAVE_AGENT_NODE_DIR/lib/node_modules/npm/bin/npm-cli.js")
        shift
        if [ "$#" -eq 1 ] && [ "$1" = --version ]; then
            printf 'fixture-npm\n'
            exit 0
        fi
        [ "$#" -eq 7 ]
        [ "$1" = install ] && [ "$2" = -g ] && [ "$3" = --ignore-scripts ]
        [ "$4" = --no-audit ] && [ "$5" = --no-fund ] && [ "$6" = -- ]
        [ "$(cat "$7")" = fixture-archive ]
        [ "$npm_config_prefix" = "$HOME/.local/share/enclave/claude-2.1.63" ]
        printf '%s\n' "$npm_config_prefix" >> "$HOME/npm-prefix.log"
        package="$npm_config_prefix/lib/node_modules/@anthropic-ai/claude-code"
        target="$npm_config_prefix/bin/claude"
        mkdir -p "$package" "$npm_config_prefix/bin"
        printf '{"version":"2.1.63"}\n' > "$package/package.json"
        printf 'fixture pinned client\n' > "$package/cli.js"
        ln -sf "$package/cli.js" "$target"
        ;;
    "$HOME/.local/share/enclave/claude-2.1.63/lib/node_modules/@anthropic-ai/claude-code/cli.js")
        shift
        [ "$#" -eq 3 ] && [ "$1" = --setting-sources ] && [ "$2" = user ] && [ "$3" = --version ]
        printf '%s\n' "$@" >> "$HOME/pinned-client.log"
        printf '2.1.63 (Claude Code)\n'
        ;;
    *)
        echo "node refuses non-JavaScript entrypoint: $1" >&2
        exit 88
        ;;
esac
`

const claudeInstallCurlStub = `
[ "$1" = -fsSL ]
case "$2" in
    https://claude.ai/install.sh)
        [ "$#" -eq 2 ]
        cat <<'NATIVE_INSTALL'
set -e
mkdir -p "$HOME/.local/bin"
cat > "$HOME/.local/bin/claude" <<'NATIVE_CLIENT'
#!/bin/sh
printf '%s\n' "$@" >> "$HOME/native-client.log"
printf 'ordinary Claude client\n'
NATIVE_CLIENT
chmod 755 "$HOME/.local/bin/claude"
NATIVE_INSTALL
        ;;
    https://registry.npmjs.org/@anthropic-ai/claude-code/-/claude-code-2.1.63.tgz)
        [ "$#" -eq 4 ] && [ "$3" = -o ]
        printf 'fixture-archive\n' > "$4"
        ;;
    *)
        echo 'unexpected network request' >&2
        exit 1
        ;;
esac
`

const claudeInstallChecksumStub = `
[ "$#" -eq 1 ] && [ "$(cat "$1")" = fixture-archive ]
printf 'a63c4616d39673a496e2abff73617c85cf90149dfaf8e95ebd8b2752f055aabe01fb89898c7fbc8a7485f2c1f651ff5241df7d906e42c64907c66520dab6a4a3  %s\n' "$1"
`

const claudeInstallJQStub = `
[ "$#" -eq 3 ] && [ "$1" = -r ] && [ "$2" = .version ]
[ "$3" = "$HOME/.local/share/enclave/claude-2.1.63/lib/node_modules/@anthropic-ai/claude-code/package.json" ]
[ "$(cat "$3")" = '{"version":"2.1.63"}' ]
printf '2.1.63\n'
`

const claudeInstallSandboxStub = `
[ "$#" -eq 2 ] && [ "$1" = --ignore-scripts ] && [ "$2" = @anthropic-ai/sandbox-runtime ]
[ "$npm_config_prefix" = "$HOME/.local" ]
printf '%s\n' "$npm_config_prefix" >> "$HOME/sandbox-prefix.log"
package="$npm_config_prefix/lib/node_modules/@anthropic-ai/sandbox-runtime"
mkdir -p "$package"
printf '{}\n' > "$package/package.json"
`
