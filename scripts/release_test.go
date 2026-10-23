// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// Run the actual workflow shells with local substitutes for the transports.
// No credentials, network access, or remote writes are involved.
func TestReleaseImmutability(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release publication runs on Linux")
	}
	const commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, publisher := range []string{"github", "downloads", "rolling"} {
		for _, scenario := range []string{"first", "identical", "partial", "changed-binary", "changed-checksums", "moved-tag", "no-digest", "changed-no-digest", "extra-file", "starter", "starter-conflict", "open", "corrupt-upload", "missing-upload", "unfinished-upload", "corrupt-manifest"} {
			if publisher == "rolling" && scenario != "first" && scenario != "corrupt-upload" && scenario != "missing-upload" && scenario != "corrupt-manifest" {
				continue
			}
			if publisher == "downloads" && (strings.HasPrefix(scenario, "starter") || scenario == "open" || scenario == "unfinished-upload") {
				continue
			}
			t.Run(publisher+"/"+scenario, func(t *testing.T) {
				root := t.TempDir()
				bin := filepath.Join(root, "bin")
				target, latest, latestPath, immutable := "releases/1.2.3", "1.2.3", "releases/latest", "true"
				if publisher == "rolling" {
					target, latest, latestPath, immutable = "rolling", commit, "rolling/latest", "false"
				}
				published := filepath.Join(root, "remote", target)
				write := func(name, value string, mode os.FileMode) {
					t.Helper()
					if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(name, []byte(value), mode); err != nil {
						t.Fatal(err)
					}
				}
				fresh := scenario == "first" || strings.HasSuffix(scenario, "-upload") || scenario == "corrupt-manifest"
				files := map[string]string{"binary": "original binary\n", "checksums.txt": "original checksums\n", "a-package": "package\n"}
				for name, value := range files {
					write(filepath.Join(root, "artifacts", name), value, 0o644)
					if !fresh && (scenario != "partial" || name != "a-package") {
						write(filepath.Join(published, name), value, 0o644)
					}
				}
				if !fresh {
					write(filepath.Join(published, "commit"), commit+"\n", 0o644)
				}
				wantFailure := false
				gapMustStayMissing := false
				if strings.HasPrefix(scenario, "starter") || scenario == "open" {
					state := "starter"
					if scenario == "open" {
						state = "open"
					}
					write(filepath.Join(root, "state", "initial-state"), state, 0o644)
					write(filepath.Join(published, "binary"), "", 0o644)
				}
				switch scenario {
				case "changed-binary", "changed-checksums", "changed-no-digest", "starter-conflict":
					name := "binary"
					if scenario == "changed-checksums" || scenario == "starter-conflict" {
						name = "checksums.txt"
					}
					write(filepath.Join(root, "artifacts", name), "rebuilt bytes\n", 0o644)
					// A mismatch must be found before any missing file is added.
					if err := os.Remove(filepath.Join(published, "a-package")); err != nil {
						t.Fatal(err)
					}
					wantFailure = true
					gapMustStayMissing = true
				case "open", "corrupt-upload", "missing-upload", "unfinished-upload", "corrupt-manifest":
					wantFailure = true
				case "moved-tag":
					write(filepath.Join(published, "commit"), strings.Repeat("b", 40)+"\n", 0o644)
					wantFailure = true
				case "extra-file":
					write(filepath.Join(published, "older-asset"), "keep me\n", 0o644)
				}
				before := map[string]string{}
				entries, err := os.ReadDir(published)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				stamp := time.Unix(1000000000, 0)
				for _, entry := range entries {
					if scenario == "starter" && entry.Name() == "binary" {
						continue // An incomplete entry may be replaced, unlike a completed asset.
					}
					name := filepath.Join(published, entry.Name())
					data, err := os.ReadFile(name)
					if err != nil {
						t.Fatal(err)
					}
					before[entry.Name()] = string(data)
					if err := os.Chtimes(name, stamp, stamp); err != nil {
						t.Fatal(err)
					}
				}
				write(filepath.Join(bin, "ssh-agent"), "#!/bin/sh\nexit 0\n", 0o755)
				write(filepath.Join(bin, "ssh-add"), "#!/bin/sh\nexit 0\n", 0o755)
				write(filepath.Join(bin, "ssh"), "#!/bin/bash\nwhile [ \"$1\" = -o ]; do shift 2; done\nshift\nexec bash -c \"$1\"\n", 0o755)
				write(filepath.Join(bin, "scp"), fakeReleaseSCP, 0o755)
				write(filepath.Join(bin, "gh"), fakeReleaseGH, 0o755)
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(cwd, filepath.Join(root, "scripts")); err != nil {
					t.Fatal(err)
				}
				var script string
				if publisher == "github" {
					script = releaseShell(t, "../.github/workflows/release.yml", "Create GitHub release")
					script = strings.ReplaceAll(script, "${{ github.server_url }}", "https://github.invalid")
					script = strings.ReplaceAll(script, "${{ github.repository }}", "test/repo")
				} else {
					script = releaseShell(t, "../.github/actions/publish-downloads/action.yml", "Upload ${{ inputs.target }}")
				}
				cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
				cmd.Dir = root
				cmd.Env = append(os.Environ(),
					"PATH="+bin+":"+os.Getenv("PATH"), "PUBLISHED="+published,
					"STATE_DIR="+filepath.Join(root, "state"),
					"GITHUB_SHA="+commit, "GITHUB_REF_NAME=v1.2.3", "GH_REPO=test/repo",
					"VERSION=1.2.3", "PRERELEASE=false", "NO_DIGEST="+scenario,
					"SOURCE=artifacts", "TARGET="+target, "LATEST="+latest, "LATEST_PATH="+latestPath,
					"COMMIT="+commit, "IMMUTABLE="+immutable, "SSH_USERNAME=test", "SSH_KEY=test",
					"SSH_PASSPHRASE=", "SSH_HOST=local", "KNOWN_HOSTS=test", "GITHUB_RUN_ID=1",
					"REMOTE_ROOT="+filepath.Join(root, "remote"))
				output, err := cmd.CombinedOutput()
				if (err != nil) != wantFailure {
					t.Fatalf("publication error = %v, want failure %v:\n%s", err, wantFailure, output)
				}
				if wantFailure {
					reason := "differs"
					switch scenario {
					case "moved-tag":
						reason = "a release tag must not move"
					case "open", "unfinished-upload":
						reason = "not fully uploaded"
					case "missing-upload":
						reason = "missing"
					}
					if publisher != "github" && fresh {
						reason = "FAILED"
					}
					if !strings.Contains(string(output), reason) {
						t.Fatalf("expected failure containing %q, got:\n%s", reason, output)
					}
				}
				for name, value := range before {
					path := filepath.Join(published, name)
					data, err := os.ReadFile(path)
					if err != nil || string(data) != value {
						t.Errorf("published %s changed: %q, %v", name, data, err)
					}
					info, err := os.Stat(path)
					if err != nil || !info.ModTime().Equal(stamp) {
						t.Errorf("published %s was replaced or touched", name)
					}
				}
				if gapMustStayMissing {
					if _, err := os.Stat(filepath.Join(published, "a-package")); !os.IsNotExist(err) {
						t.Error("added a missing asset before rejecting changed contents")
					}
				} else if !wantFailure {
					for name, value := range files {
						data, err := os.ReadFile(filepath.Join(published, name))
						if err != nil || string(data) != value {
							t.Errorf("published %s = %q, %v", name, data, err)
						}
					}
				}
				if publisher != "github" {
					if fresh && wantFailure {
						if _, err := os.Stat(published); !os.IsNotExist(err) {
							t.Error("exposed unverified staging files")
						}
					}
					if _, err := os.Stat(published + ".tmp-1"); !os.IsNotExist(err) {
						t.Error("left the staging directory behind")
					}
					data, err := os.ReadFile(filepath.Join(root, "remote", latestPath))
					if wantFailure {
						if !os.IsNotExist(err) {
							t.Error("latest moved after failed publication")
						}
					} else if err != nil || string(data) != latest+"\n" {
						t.Errorf("latest = %q, %v", data, err)
					}
				}
			})
		}
	}
}

func releaseShell(t *testing.T, path, stepName string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	type step struct{ Name, Run string }
	var doc struct {
		Jobs map[string]struct{ Steps []step }
		Runs struct{ Steps []step }
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	steps := doc.Runs.Steps
	for _, job := range doc.Jobs {
		steps = append(steps, job.Steps...)
	}
	for _, step := range steps {
		if step.Name == stepName {
			return step.Run
		}
	}
	t.Fatalf("step %q not found in %s", stepName, path)
	return ""
}

const fakeReleaseGH = `#!/bin/bash
set -euo pipefail
if [ "$1 $2" = 'release view' ]; then
  [ -f "$PUBLISHED/commit" ] || exit 1
  printf 'https://github.invalid/test/repo/commit/%s\n' "$(cat "$PUBLISHED/commit")"
elif [ "$1" = api ]; then
  if [ "$2" = --method ]; then
    [ "$3" = DELETE ]
    [ "$(cat "$STATE_DIR/initial-state")" = starter ]
    rm "$PUBLISHED/${4##*/}" "$STATE_DIR/initial-state"
  elif [[ $2 = */releases/tags/* ]]; then
    for file in "$PUBLISHED"/*; do
      [ "${file##*/}" != commit ] || continue
      digest="sha256:$(sha256sum "$file" | cut -d' ' -f1)"
      case "$NO_DIGEST" in *no-digest) digest=unknown ;; esac
      state=uploaded
      if [ "${file##*/}" = binary ] && [ -f "$STATE_DIR/initial-state" ]; then state=$(cat "$STATE_DIR/initial-state"); fi
      printf '%s\t%s\t%s\t%s\n' "${file##*/}" "$digest" "${file##*/}" "$state"
    done
  else
    cat "$PUBLISHED/${2##*/}"
  fi
elif [ "$1 $2" = 'release upload' ]; then
  shift 3
  clobber=false
  if [ "$1" = --clobber ]; then clobber=true; shift; fi
  for file in "$@"; do
    if [ "$clobber" = false ] && [ -e "$PUBLISHED/${file##*/}" ]; then exit 1; fi
    cp "$file" "$PUBLISHED/"
  done
elif [ "$1 $2" = 'release create' ]; then
  mkdir -p "$PUBLISHED"
  printf '%s\n' "$GITHUB_SHA" > "$PUBLISHED/commit"
  for arg in "$@"; do
    case "$arg" in artifacts/*) cp "$arg" "$PUBLISHED/" ;; esac
  done
  case "$NO_DIGEST" in
    corrupt-upload) printf broken > "$PUBLISHED/binary" ;;
    corrupt-manifest) printf broken > "$PUBLISHED/checksums.txt" ;;
    missing-upload) rm "$PUBLISHED/binary" ;;
    unfinished-upload) mkdir -p "$STATE_DIR"; printf starter > "$STATE_DIR/initial-state" ;;
  esac
else
  echo "unexpected gh arguments: $*" >&2
  exit 1
fi
`

const fakeReleaseSCP = `#!/bin/bash
set -euo pipefail
while [[ $1 = -* ]]; do if [ "$1" = -o ]; then shift 2; else shift; fi; done
dest=${!#}
dest=${dest#*:}
cp "${@:1:$#-1}" "$dest"
case "$NO_DIGEST" in
  corrupt-upload) printf broken > "$dest/binary" ;;
  corrupt-manifest) printf broken > "$dest/checksums.txt" ;;
  missing-upload) rm "$dest/binary" ;;
esac
`
