// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
		for _, scenario := range []string{"first", "identical", "partial", "changed-binary", "changed-checksums", "moved-tag", "no-digest", "changed-no-digest", "extra-file", "starter", "starter-conflict", "open", "corrupt-upload", "missing-upload", "unfinished-upload", "corrupt-manifest", "newer-latest", "prerelease"} {
			if (scenario == "newer-latest" || scenario == "prerelease") && publisher != "downloads" {
				continue
			}
			// On GitHub, the draft's provenance marker pins the commit; see
			// TestReleasePreparationAndProvenance.
			if scenario == "moved-tag" && publisher == "github" {
				continue
			}
			if publisher == "rolling" && scenario != "first" && scenario != "corrupt-upload" && scenario != "missing-upload" && scenario != "corrupt-manifest" {
				continue
			}
			if publisher != "github" && (strings.HasPrefix(scenario, "starter") || scenario == "open" || scenario == "unfinished-upload") {
				continue
			}
			t.Run(publisher+"/"+scenario, func(t *testing.T) {
				root := t.TempDir()
				bin := filepath.Join(root, "bin")
				target, latest, latestPath, immutable := "releases/1.2.3", "1.2.3", "releases/latest", "true"
				if publisher == "rolling" {
					target, latest, latestPath, immutable = "rolling", commit, "rolling/latest", "false"
				}
				if scenario == "prerelease" {
					target, latest = "releases/1.2.3-rc.1", ""
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
				fresh := scenario == "first" || strings.HasSuffix(scenario, "-upload") || scenario == "corrupt-manifest" || scenario == "newer-latest" || scenario == "prerelease"
				wantLatest := latest
				if scenario == "newer-latest" || scenario == "prerelease" {
					wantLatest = "2.0.0"
					write(filepath.Join(root, "remote", latestPath), wantLatest+"\n", 0o644)
				}
				files := map[string]string{"binary": "original binary\n", "checksums.txt": "original checksums\n", "a-package": "package\n"}
				for name, value := range files {
					write(filepath.Join(root, "artifacts", name), value, 0o644)
					if !fresh && (scenario != "partial" || name != "a-package") {
						write(filepath.Join(published, name), value, 0o644)
					}
				}
				if !fresh || publisher == "github" {
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
					script = `./scripts/upload-github-release.sh v1.2.3 artifacts`
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
					if wantFailure || wantLatest == "" {
						if !os.IsNotExist(err) {
							t.Error("latest moved after failed publication")
						}
					} else if err != nil || string(data) != wantLatest+"\n" {
						t.Errorf("latest = %q, %v", data, err)
					}
				}
			})
		}
	}
}

func TestReleaseArtifactManifest(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release publication runs on Linux")
	}
	for _, scenario := range []string{"original", "changed", "missing", "extra", "replaced-manifest"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("binary", "original binary")
			write("package", "original package")
			manifest := fmt.Sprintf("%x  binary\n%x  package\n", sha256.Sum256([]byte("original binary")), sha256.Sum256([]byte("original package")))
			write("checksums.txt", manifest)
			switch scenario {
			case "changed", "replaced-manifest":
				write("binary", "changed binary")
				if scenario == "replaced-manifest" {
					write("checksums.txt", fmt.Sprintf("%x  binary\n%x  package\n", sha256.Sum256([]byte("changed binary")), sha256.Sum256([]byte("original package"))))
				}
			case "missing":
				if err := os.Remove(filepath.Join(dir, "package")); err != nil {
					t.Fatal(err)
				}
			case "extra":
				write("extra", "not in the original build")
			}
			output, err := exec.Command("bash", "verify-release-artifacts.sh", dir, fmt.Sprintf("%x", sha256.Sum256([]byte(manifest)))).CombinedOutput()
			if (err == nil) != (scenario == "original") {
				t.Fatalf("manifest verification = %v:\n%s", err, output)
			}
		})
	}
}

func TestReleasePreparationAndProvenance(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release publication runs on Linux")
	}
	root := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cwd, filepath.Join(root, "scripts")); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), mode); err != nil {
			t.Fatal(err)
		}
	}
	// release.json is the release and assets/ its uploaded files. Publication
	// is not supported: preparation must only ever produce a draft.
	write("gh", `#!/bin/bash
set -euo pipefail
if [ "$1 $2" = 'release view' ]; then
  case "$5" in
    isDraft) jq -r .isDraft release.json ;;
    databaseId) echo 17 ;;
    *) cat release.json ;;
  esac
elif [ "$1 $2" = 'release create' ]; then
  [[ " $* " = *' --draft '* ]]
  shift 3
  while [ "$1" != --notes ]; do shift; done
  jq -n --arg body "$2" '{body: $body, isDraft: true}' > release.json
elif [ "$1 $2" = 'release upload' ]; then
  [ "$(jq -r .isDraft release.json)" = true ]
  mkdir -p assets
  cp "$4" assets/
elif [ "$1 $2" = 'api repos/test/repo/releases/17' ]; then
  for file in assets/*; do
    [ -e "$file" ] || continue
    printf '%s\tsha256:%s\t%s\tuploaded\n' "${file##*/}" "$(sha256sum "$file" | cut -d' ' -f1)" "${file##*/}"
  done
elif [ "$1" = api ]; then
  cat run.json
else
  echo "unexpected gh arguments: $*" >&2; exit 1
fi
`, 0o755)
	run := func(script string, extra ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "GH_REPO=test/repo", "GITHUB_SHA="+strings.Repeat("a", 40), "GITHUB_RUN_ID=123", "GITHUB_REF_NAME=v1.2.3", "VERSION=1.2.3", "PRERELEASE=false", "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"))
		cmd.Env = append(cmd.Env, extra...)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	// A real annotated tag exercises peeling, not just matching a mock SHA.
	output, err := run(`git init -q; git -c user.name=Test -c user.email=test@example.invalid commit -q --allow-empty -m initial; git -c user.name=Test -c user.email=test@example.invalid tag -a v1.2.3 -m release; mkdir artifacts; printf binary > artifacts/binary; cd artifacts; sha256sum binary > checksums.txt`)
	if err != nil {
		t.Fatalf("fixture: %v: %s", err, output)
	}
	commit, err := run("git rev-parse HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit = strings.TrimSpace(commit)
	prepare := releaseShell(t, "../.github/workflows/release.yml", "Create draft release")
	prepare = strings.ReplaceAll(prepare, "${{ github.server_url }}", "https://github.invalid")
	prepare = strings.ReplaceAll(prepare, "${{ github.repository }}", "test/repo")
	assertAssets := func(context string) {
		t.Helper()
		for _, name := range []string{"binary", "checksums.txt"} {
			want, err := os.ReadFile(filepath.Join(root, "artifacts", name))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(filepath.Join(root, "assets", name)); err != nil || string(got) != string(want) {
				t.Fatalf("%s: draft asset %s = %q, %v", context, name, got, err)
			}
		}
	}
	if output, err := run(prepare, "GITHUB_SHA="+commit); err != nil {
		t.Fatalf("prepare: %v: %s", err, output)
	}
	assertAssets("prepare")
	data, err := os.ReadFile(filepath.Join(root, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var release map[string]any
	if err := json.Unmarshal(data, &release); err != nil {
		t.Fatal(err)
	}
	unedited := release["body"].(string)
	// Notes edited on the web come back with CRLF line endings, which reach the
	// marker line once anything follows it.
	release["body"] = strings.ReplaceAll("Human-edited notes\n"+unedited+"\nMore notes\n", "\n", "\r\n")
	data, err = json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	write("release.json", string(data), 0o644)
	if err := os.Remove(filepath.Join(root, "assets", "binary")); err != nil {
		t.Fatal(err)
	}
	if output, err := run(prepare, "GITHUB_SHA="+commit); err != nil {
		t.Fatalf("prepare retry: %v: %s", err, output)
	}
	assertAssets("prepare retry")
	after, err := os.ReadFile(filepath.Join(root, "release.json"))
	if err != nil || string(after) != string(data) {
		t.Fatal("preparation retry changed the edited draft notes")
	}
	if output, err := run(prepare, "GITHUB_SHA="+commit, "GITHUB_RUN_ID=124"); err == nil {
		t.Fatalf("preparation accepted a different build run: %s", output)
	}
	published, err := json.Marshal(map[string]any{"body": release["body"], "isDraft": false})
	if err != nil {
		t.Fatal(err)
	}
	write("release.json", string(published), 0o644)
	if output, err := run(prepare, "GITHUB_SHA="+commit); err == nil || !strings.Contains(output, "already published") {
		t.Fatalf("preparation changed a published release: %v: %s", err, output)
	}
	for _, scenario := range []string{"original", "unedited", "published-retry", "missing-marker", "duplicate-marker", "wrong-workflow", "wrong-event", "wrong-tag", "wrong-commit", "failed-build", "moved-tag"} {
		t.Run(scenario, func(t *testing.T) {
			body := release["body"].(string)
			if scenario == "unedited" {
				body = unedited
			}
			if scenario == "missing-marker" {
				body = "human notes only"
			}
			if scenario == "duplicate-marker" {
				body += "\n" + release["body"].(string)
			}
			metadata, err := json.Marshal(map[string]any{"body": body, "isDraft": scenario != "published-retry"})
			if err != nil {
				t.Fatal(err)
			}
			write("release.json", string(metadata), 0o644)
			build := map[string]string{"path": ".github/workflows/release.yml", "event": "push", "head_branch": "v1.2.3", "head_sha": commit, "conclusion": "success"}
			switch scenario {
			case "wrong-workflow":
				build["path"] = ".github/workflows/rolling.yml"
			case "wrong-event":
				build["event"] = "workflow_dispatch"
			case "wrong-tag":
				build["head_branch"] = "v1.2.3-rc.1"
			case "wrong-commit":
				build["head_sha"] = strings.Repeat("b", 40)
			case "failed-build":
				build["conclusion"] = "failure"
			case "moved-tag":
				if output, err := run(`git -c user.name=Test -c user.email=test@example.invalid commit -q --allow-empty -m moved; git tag -f v1.2.3`); err != nil {
					t.Fatalf("move tag: %v: %s", err, output)
				}
			}
			metadata, err = json.Marshal(build)
			if err != nil {
				t.Fatal(err)
			}
			write("run.json", string(metadata), 0o644)
			output, err := run(`./scripts/release-info.sh v1.2.3`)
			valid := scenario == "original" || scenario == "unedited" || scenario == "published-retry"
			if (err == nil) != valid {
				t.Fatalf("provenance check = %v, valid %v: %s", err, valid, output)
			}
			if valid && (!strings.Contains(output, "commit="+commit+"\n") || !strings.Contains(output, "run-id=123\n") || !strings.Contains(output, "prerelease=false\n")) {
				t.Fatalf("unexpected release provenance: %s", output)
			}
		})
	}
}

func TestPromotionRequiresOriginalBuild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release publication runs on Linux")
	}
	promote := releaseShell(t, "../.github/workflows/promote-release.yml", "Verify draft assets")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"original", "changed-draft", "changed-draft-and-marker", "expired-build"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, value string, mode os.FileMode) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, name), []byte(value), mode); err != nil {
					t.Fatal(err)
				}
			}
			manifest := func(value string) string { return fmt.Sprintf("%x  binary\n", sha256.Sum256([]byte(value))) }
			write("build/binary", "original", 0o644)
			write("build/checksums.txt", manifest("original"), 0o644)
			candidate := "original"
			if strings.HasPrefix(scenario, "changed-") {
				candidate = "substituted release binary"
			}
			write("candidate/binary", candidate, 0o644)
			write("candidate/checksums.txt", manifest(candidate), 0o644)
			checksum := sha256.Sum256([]byte(manifest("original")))
			if scenario == "changed-draft-and-marker" {
				checksum = sha256.Sum256([]byte(manifest(candidate)))
			}
			write("gh", `#!/bin/bash
set -euo pipefail
case "$1 $2" in
  'run download')
    [ "$3" = 123 ]
    if [ "$SCENARIO" = expired-build ]; then echo 'artifact unavailable' >&2; exit 1; fi
    mkdir "${!#}"; cp build/* "${!#}/" ;;
  'release download') mkdir "${!#}"; cp candidate/* "${!#}/" ;;
  'release view') echo 17 ;;
  'api repos/test/repo/releases/17')
    for file in candidate/*; do
      printf '%s\tsha256:%s\t%s\tuploaded\n' "${file##*/}" "$(sha256sum "$file" | cut -d' ' -f1)" "${file##*/}"
    done ;;
  *) echo "unexpected gh arguments: $*" >&2; exit 1 ;;
esac
`, 0o755)
			if err := os.Symlink(cwd, filepath.Join(root, "scripts")); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-euo", "pipefail", "-c", promote)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "GH_REPO=test/repo", "TAG=v1.2.3", "BUILD_RUN=123", "SCENARIO="+scenario, fmt.Sprintf("CHECKSUM=%x", checksum))
			output, err := cmd.CombinedOutput()
			if (err == nil) != (scenario == "original") {
				t.Fatalf("promotion verification = %v: %s", err, output)
			}
		})
	}
}

func TestPromotionMovesGitHubLatestOnlyForward(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release publication runs on Linux")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	publish := releaseShell(t, "../.github/workflows/promote-release.yml", "Publish GitHub release")
	for _, tc := range []struct {
		version, prerelease, current, want string
		changed                            bool
	}{
		{"1.2.3", "false", "", "--latest=true", false},
		{"1.2.3", "false", "v1.2.2", "--latest=true", false},
		{"1.10.0", "false", "v1.9.0", "--latest=true", false},
		{"1.2.3", "false", "v1.10.0", "--latest=false", false},
		{"1.3.0-rc.1", "true", "v1.2.3", "--latest=false", false},
		{"1.2.3", "false", "v1.2.2", "", true},
	} {
		t.Run(tc.version+"/"+tc.current, func(t *testing.T) {
			root := t.TempDir()
			gh := `#!/bin/bash
set -euo pipefail
if [ "$1 $2 $3" = 'release view --json' ]; then
  [ -n "$CURRENT" ] || exit 1
  echo "$CURRENT"
elif [ "$1 $2" = 'release view' ]; then
  echo 17
elif [ "$1 $2" = 'api repos/test/repo/releases/17' ]; then
  printf 'binary\tsha256:%s\t1\tuploaded\n' "$ASSET_DIGEST"
elif [ "$1 $2" = 'release edit' ]; then
  echo "$*" > edit
else
  echo "unexpected gh arguments: $*" >&2; exit 1
fi
`
			if err := os.WriteFile(filepath.Join(root, "gh"), []byte(gh), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(cwd, filepath.Join(root, "scripts")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "original-artifacts"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "original-artifacts", "binary"), []byte("original"), 0o644); err != nil {
				t.Fatal(err)
			}
			asset := "original"
			if tc.changed {
				asset = "changed after initial verification"
			}
			cmd := exec.Command("bash", "-euo", "pipefail", "-c", publish)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "GH_REPO=test/repo", "TAG=v"+tc.version, "VERSION="+tc.version, "PRERELEASE="+tc.prerelease, "CURRENT="+tc.current, fmt.Sprintf("ASSET_DIGEST=%x", sha256.Sum256([]byte(asset))))
			output, err := cmd.CombinedOutput()
			if tc.changed {
				if err == nil || !strings.Contains(string(output), "differs") {
					t.Fatalf("published changed assets: %v: %s", err, output)
				}
				if _, err := os.Stat(filepath.Join(root, "edit")); !os.IsNotExist(err) {
					t.Fatal("published the GitHub draft despite changed assets")
				}
				return
			}
			if err != nil {
				t.Fatalf("publish: %v: %s", err, output)
			}
			edit, err := os.ReadFile(filepath.Join(root, "edit"))
			if err != nil {
				t.Fatal(err)
			}
			if want := "release edit v" + tc.version + " --draft=false " + tc.want + "\n"; string(edit) != want {
				t.Fatalf("gh %q, want %q", edit, want)
			}
		})
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
  case "$5" in
    isDraft) echo true ;;
    databaseId) echo 17 ;;
    *) echo "unexpected release fields: $5" >&2; exit 1 ;;
  esac
elif [ "$1" = api ]; then
  if [ "$2" = --method ]; then
    [ "$3" = DELETE ]
    [ "$(cat "$STATE_DIR/initial-state")" = starter ]
    rm "$PUBLISHED/${4##*/}" "$STATE_DIR/initial-state"
  elif [[ $2 = */releases/17 ]]; then
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
    case "$NO_DIGEST:${file##*/}" in
      corrupt-upload:binary) printf broken > "$PUBLISHED/binary" ;;
      corrupt-manifest:checksums.txt) printf broken > "$PUBLISHED/checksums.txt" ;;
      missing-upload:binary) rm "$PUBLISHED/binary" ;;
      unfinished-upload:binary) mkdir -p "$STATE_DIR"; printf starter > "$STATE_DIR/initial-state" ;;
    esac
  done
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

// GitHub renames uploaded assets, so checksums.txt and download.eclipse.org
// must already carry the names the GitHub release will show.
func TestReleaseAssetNamesMatchGitHub(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release publication runs on Linux")
	}
	names := map[string]string{
		"enclave-0.1.2~~git.20261029.87.e951bfe0-1.fc44.x86_64.rpm": "enclave-0.1.2.git.20261029.87.e951bfe0-1.fc44.x86_64.rpm",
		"enclave_0.1.2~~git.20261029.87.e951bfe0-1_amd64.deb":       "enclave_0.1.2.git.20261029.87.e951bfe0-1_amd64.deb",
		"enclave_1.1.0~rc.1-1_amd64.deb":                            "enclave_1.1.0.rc.1-1_amd64.deb",
		"enclave-1.0.0-1.fc44.x86_64.rpm":                           "enclave-1.0.0-1.fc44.x86_64.rpm",
		"enclave-windows-amd64.zip":                                 "enclave-windows-amd64.zip",
		"enclave-linux-amd64":                                       "enclave-linux-amd64",
		"checksums.txt":                                             "checksums.txt",
		"enclave-\u00e9.txt":                                        "enclave-.txt",
	}
	for name, want := range names {
		cmd := exec.Command("sh", "github-asset-name.sh", name)
		// The script pins the C locale; a UTF-8 one would widen its letter ranges.
		cmd.Env = append(os.Environ(), "LC_ALL=en_US.UTF-8")
		output, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != want {
			t.Errorf("github-asset-name.sh %q = %q, %v; want %q", name, output, err, want)
		}
	}

	root := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cwd, filepath.Join(root, "scripts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name := range names {
		if err := os.WriteFile(filepath.Join(root, "artifacts", name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-euo", "pipefail", "-c", releaseShell(t, "../.github/workflows/reusable-release-assets.yml", "Normalize artifact names"))
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("normalize: %v: %s", err, output)
	}
	entries, err := os.ReadDir(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(root, "artifacts", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got[string(data)] = entry.Name()
	}
	for name, want := range names {
		if got[name] != want {
			t.Errorf("%q was published as %q, want %q", name, got[name], want)
		}
	}

	// The assemble job fails on a name GitHub would still change, before
	// either channel publishes anything.
	verify := releaseShell(t, "../.github/workflows/reusable-release-assets.yml", "Verify asset names")
	cmd = exec.Command("bash", "-euo", "pipefail", "-c", verify)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("verify normalized names: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "enclave_1.0.1~~git.20260421.57.abcdef12-1_amd64.deb"), []byte("package"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", "-euo", "pipefail", "-c", verify)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "would be renamed") {
		t.Fatalf("verification of a name GitHub renames = %v:\n%s", err, output)
	}
}
