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

// TestEntrypointNodeVersionSelection exercises the version-selection logic in
// the node-dev feature entrypoint by mocking nvm and checking which version is
// selected. After the CLI/config version plumbing was removed, the precedence
// is simply: project .nvmrc > nvm default.
func TestEntrypointNodeVersionSelection(t *testing.T) {
	tests := []struct {
		name        string
		nvmrc       string // empty means no .nvmrc file
		wantVersion string // empty means nvm default (no explicit version)
	}{
		{
			name:        "nvmrc selects version",
			nvmrc:       "18",
			wantVersion: "18",
		},
		{
			name:        "nvm default when no nvmrc",
			wantVersion: "",
		},
	}

	scriptPath := filepath.Join("..", "..", "extensions", "features", "node-dev", "feature-entrypoint.d", "setup.sh")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			projectDir := filepath.Join(home, "project")
			if err := os.MkdirAll(projectDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			if tt.nvmrc != "" {
				if err := os.WriteFile(filepath.Join(projectDir, ".nvmrc"), []byte(tt.nvmrc+"\n"), 0o644); err != nil {
					t.Fatalf("write .nvmrc: %v", err)
				}
			}

			// Build a wrapper script that:
			// 1. Creates a mock nvm function that records the selected version
			// 2. Sources setup.sh
			// 3. Prints the version that was selected
			//
			// A successful `nvm use` exposes a node stub on PATH, mirroring real
			// nvm: the entrypoint's last-resort `nvm use node` only fires when no
			// Node is active, so without this the fallback would overwrite the
			// recorded selection on a host where node is absent from PATH.
			wrapper := `
set -e
mkdir -p "$HOME/fakebin"
printf '#!/bin/sh\n' > "$HOME/fakebin/node"
chmod +x "$HOME/fakebin/node"
_nvm_used=""
nvm() {
    case "$1" in
        use)
            if [ "$2" = "default" ]; then
                _nvm_used="DEFAULT"
            else
                _nvm_used="$2"
            fi
            export PATH="$HOME/fakebin:$PATH"
            return 0
            ;;
        install)
            return 0
            ;;
    esac
}
. "` + scriptPath + `"
echo "SELECTED=${_nvm_used}"
`
			cmd := exec.Command("bash", "-c", wrapper)
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + home,
				"PROJECT_DIR=" + projectDir,
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("script failed: %v\noutput:\n%s", err, string(out))
			}

			// Parse selected version from output
			var got string
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "SELECTED=") {
					got = strings.TrimPrefix(line, "SELECTED=")
					break
				}
			}

			if tt.wantVersion == "" {
				// Expect nvm default
				if got != "DEFAULT" {
					t.Fatalf("expected nvm default, got %q", got)
				}
			} else {
				if got != tt.wantVersion {
					t.Fatalf("expected version %q, got %q", tt.wantVersion, got)
				}
			}
		})
	}
}

func TestEntrypointNodeBinsExposeSelectedVersionOnSanitizedPath(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".nvmrc"), []byte("18\n"), 0o644); err != nil {
		t.Fatalf("write .nvmrc: %v", err)
	}

	v18Bin := filepath.Join(home, ".nvm", "versions", "node", "v18", "bin")
	v20Bin := filepath.Join(home, ".nvm", "versions", "node", "v20", "bin")
	for _, cmd := range []string{"node", "npm", "npx", "corepack"} {
		writeFakeExecutable(t, filepath.Join(v18Bin, cmd), cmd+"-v18")
		writeFakeExecutable(t, filepath.Join(v20Bin, cmd), cmd+"-v20")
	}

	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatalf("mkdir local bin: %v", err)
	}
	for _, cmd := range []string{"node", "npm"} {
		if err := os.Symlink(filepath.Join(v20Bin, cmd), filepath.Join(localBin, cmd)); err != nil {
			t.Fatalf("seed stale symlink for %s: %v", cmd, err)
		}
	}

	scriptPath := filepath.Join("..", "..", "extensions", "features", "node-dev", "feature-entrypoint.d", "setup.sh")
	wrapper := `
set -e
nvm() {
    case "$1" in
        use)
            version="$2"
            if [ "$version" = "default" ]; then
                version="20"
            fi
            case "$version" in
                v*) ;;
                *) version="v$version" ;;
            esac
            export PATH="$HOME/.nvm/versions/node/$version/bin:$PATH"
            return 0
            ;;
        install)
            return 0
            ;;
    esac
}
. "` + scriptPath + `"
PATH="$HOME/.local/bin:/usr/bin:/bin"
printf 'NODE=%s\n' "$(node)"
printf 'NPM=%s\n' "$(npm)"
`
	cmd := exec.Command("bash", "-c", wrapper)
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "NODE=node-v18\n") {
		t.Fatalf("expected sanitized PATH node to use v18, got:\n%s", string(out))
	}
	if !strings.Contains(string(out), "NPM=npm-v18\n") {
		t.Fatalf("expected sanitized PATH npm to use v18, got:\n%s", string(out))
	}
}

func TestEntrypointNodeBinsFallbackToSnapshotWhenNVMActivationFails(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatalf("mkdir local bin: %v", err)
	}

	toolBin := pathWithCommands(t, home, "basename", "cp", "dirname", "ln", "mkdir", "readlink")
	snapshotBin := filepath.Join(home, ".nvm", "versions-default", "node", "v24.18.0", "bin")
	for _, cmd := range []string{"node", "npm", "npx", "corepack"} {
		writeFakeExecutable(t, filepath.Join(snapshotBin, cmd), "snapshot-"+cmd)
	}

	privateNodeDir := filepath.Join(home, "private-node")
	privateBin := filepath.Join(privateNodeDir, "bin")
	writeFakeExecutable(t, filepath.Join(privateBin, "node"), "private-node")

	scriptPath := filepath.Join("..", "..", "extensions", "features", "node-dev", "feature-entrypoint.d", "setup.sh")
	wrapper := `
set -e
nvm() {
    case "$1" in
        version)
            if [ "${2:-}" = "default" ]; then
                printf 'v24.18.0\n'
            else
                printf 'N/A\n'
            fi
            return 0
            ;;
        use)
            return 3
            ;;
        install)
            return 1
            ;;
    esac
}
. "` + scriptPath + `"
printf 'NODE=%s\n' "$(node)"
printf 'NPM=%s\n' "$(npm)"
printf 'NODE_LINK=%s\n' "$(readlink "$HOME/.local/bin/node")"
`
	cmd := exec.Command("bash", "-c", wrapper)
	cmd.Env = []string{
		"PATH=" + strings.Join([]string{localBin, privateBin, toolBin}, ":"),
		"HOME=" + home,
		"PROJECT_DIR=",
		"ENCLAVE_AGENT_NODE_DIR=" + privateNodeDir,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "NODE=snapshot-node\n") {
		t.Fatalf("expected fallback node from versions-default, got:\n%s", string(out))
	}
	if !strings.Contains(string(out), "NPM=snapshot-npm\n") {
		t.Fatalf("expected fallback npm from versions-default, got:\n%s", string(out))
	}
	if !strings.Contains(string(out), "NODE_LINK="+filepath.Join(snapshotBin, "node")+"\n") {
		t.Fatalf("expected node symlink to point at versions-default, got:\n%s", string(out))
	}
}

// TestEntrypointNodeBinsFallbackToInstalledVersionWhenNoDefaultAlias covers an
// older image whose persistent ~/.nvm/versions volume holds a usable version
// but has neither a default alias (so `nvm use default` fails) nor a build-time
// versions-default snapshot. The entrypoint must still expose Node by activating
// the newest installed version via `nvm use node`.
func TestEntrypointNodeBinsFallbackToInstalledVersionWhenNoDefaultAlias(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatalf("mkdir local bin: %v", err)
	}

	toolBin := pathWithCommands(t, home, "basename", "cp", "dirname", "ln", "mkdir", "readlink")

	// Persistent volume holds a usable version; no versions-default snapshot.
	volBin := filepath.Join(home, ".nvm", "versions", "node", "v24.16.0", "bin")
	for _, cmd := range []string{"node", "npm", "npx", "corepack"} {
		writeFakeExecutable(t, filepath.Join(volBin, cmd), "vol-"+cmd)
	}

	scriptPath := filepath.Join("..", "..", "extensions", "features", "node-dev", "feature-entrypoint.d", "setup.sh")
	wrapper := `
set -e
nvm() {
    case "$1" in
        version)
            printf 'N/A\n'
            return 0
            ;;
        use)
            if [ "$2" = "node" ]; then
                export PATH="$HOME/.nvm/versions/node/v24.16.0/bin:$PATH"
                return 0
            fi
            # No default alias and no requested version resolves.
            return 3
            ;;
        install)
            return 1
            ;;
    esac
}
. "` + scriptPath + `"
PATH="$HOME/.local/bin:` + toolBin + `"
printf 'NODE=%s\n' "$(node)"
printf 'NPM=%s\n' "$(npm)"
printf 'NODE_LINK=%s\n' "$(readlink "$HOME/.local/bin/node")"
`
	cmd := exec.Command("bash", "-c", wrapper)
	cmd.Env = []string{
		"PATH=" + strings.Join([]string{localBin, toolBin}, ":"),
		"HOME=" + home,
		"PROJECT_DIR=",
		"ENCLAVE_AGENT_NODE_DIR=" + filepath.Join(home, "private-node"),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "NODE=vol-node\n") {
		t.Fatalf("expected node from persistent volume, got:\n%s", string(out))
	}
	if !strings.Contains(string(out), "NPM=vol-npm\n") {
		t.Fatalf("expected npm from persistent volume, got:\n%s", string(out))
	}
	if !strings.Contains(string(out), "NODE_LINK="+filepath.Join(volBin, "node")+"\n") {
		t.Fatalf("expected node symlink to point at the volume version, got:\n%s", string(out))
	}
}

func writeFakeExecutable(t *testing.T, path string, output string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	content := "#!/bin/sh\nprintf '%s\\n' '" + output + "'\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake executable: %v", err)
	}
}

func pathWithCommands(t *testing.T, home string, names ...string) string {
	t.Helper()
	bin := filepath.Join(home, "test-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir test bin: %v", err)
	}
	for _, name := range names {
		resolved, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if err := os.Symlink(resolved, filepath.Join(bin, name)); err != nil {
			t.Fatalf("symlink %s: %v", name, err)
		}
	}
	return bin
}
