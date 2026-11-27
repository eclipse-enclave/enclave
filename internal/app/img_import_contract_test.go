// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

// The image inbox as external tools drive it: they detect support through
// `config --json`, start sessions with --image-inbox[=false], read the option
// back from `ps --json`, and pipe images through `img import`. See
// docs/integration.md.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

func TestConfigJSONReportsImageInbox(t *testing.T) {
	projectOn := config.Defaults{ImageInbox: boolPtr(true)}
	tests := []struct {
		name       string
		args       []string
		project    config.Defaults
		wantValue  bool
		wantSource string
	}{
		{"default", nil, config.Defaults{}, false, "default"},
		{"project config", nil, projectOn, true, "project"},
		{"flag", []string{"--image-inbox"}, config.Defaults{}, true, "cli"},
		{"flag turns off what the config turns on", []string{"--image-inbox=false"}, projectOn, false, "cli"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := cli.Parse(append([]string{"config", "--json"}, tt.args...), config.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			opts, _, _ := config.ResolveOptionsForTool(parsed.Options, parsed.Sources, config.Defaults{}, tt.project, "")

			doc := runConfigJSON(t, configTestInput(t, testPathsWithFeatures(t), opts, nil))

			option := configJSONOptionField(t, doc, imageInboxOption)
			if option["kind"] != "bool" || option["value"] != tt.wantValue || option["source"] != tt.wantSource {
				t.Fatalf("options.%s = %#v, want bool %v from %s", imageInboxOption, option, tt.wantValue, tt.wantSource)
			}
		})
	}
}

// --image-inbox=false must win over a config that enables the inbox, for run
// and continue alike, so a caller can pass every boolean option explicitly.
func TestImageInboxFalseFlagOverridesConfig(t *testing.T) {
	for _, verb := range []string{"run", "continue"} {
		t.Run(verb, func(t *testing.T) {
			parsed, err := cli.Parse([]string{verb, "--image-inbox=false"}, config.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			for _, layer := range []struct{ global, project config.Defaults }{
				{config.Defaults{ImageInbox: boolPtr(true)}, config.Defaults{}},
				{config.Defaults{}, config.Defaults{ImageInbox: boolPtr(true)}},
			} {
				opts, _, _ := config.ResolveOptionsForTool(parsed.Options, parsed.Sources, layer.global, layer.project, "")
				if opts.ImageInbox || opts.Sources.ImageInbox != model.SourceCLI {
					t.Fatalf("image inbox = %v from %v, want false from the flag", opts.ImageInbox, opts.Sources.ImageInbox)
				}
			}
		})
	}
}

func TestPSJSONRecordsImageInbox(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		opts := config.DefaultOptions()
		opts.ImageInbox = enabled
		recorded := sessionProvenance(opts, model.Profile{}, featureSelection{})
		if recorded[imageInboxOption] != enabled {
			t.Fatalf("recorded %s = %#v, want %v", imageInboxOption, recorded[imageInboxOption], enabled)
		}

		var out strings.Builder
		if err := renderPSJSON(&out, []backend.Session{{Ref: backend.SessionRef{Name: "enclave-claude-abc"}, Provenance: recorded}}); err != nil {
			t.Fatal(err)
		}
		var entries []struct {
			Options map[string]any `json:"options"`
		}
		if err := json.Unmarshal([]byte(out.String()), &entries); err != nil {
			t.Fatalf("unmarshal ps JSON: %v\n%s", err, out.String())
		}
		if got := entries[0].Options[imageInboxOption]; got != enabled {
			t.Fatalf("ps --json options.%s = %#v, want %v", imageInboxOption, got, enabled)
		}
	}
}

func TestNotifiesDesktopOnlyForClipboardAndScreenshot(t *testing.T) {
	tests := []struct {
		name string
		img  model.ImgOptions
		want bool
	}{
		{"clipboard", model.ImgOptions{}, true},
		{"clipboard without copy", model.ImgOptions{ImgNoClipboard: true}, true},
		{"screenshot", model.ImgOptions{ImgScreenshot: true}, true},
		{"file", model.ImgOptions{ImgFile: "shot.png"}, false},
		{"stdin", model.ImgOptions{ImgStdin: true, ImgNoClipboard: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "1")
			for _, outcome := range []string{"success", "failure"} {
				f := waylandDesktop()
				f.installed["notify-send"] = true
				captureOutput(t, func() {
					if outcome == "success" {
						_ = reportImport(tt.img, f.desktop(), model.ContainerImageInboxDir+"/x.png", workProject)
					} else {
						reportImportFailure(tt.img, f.desktop(), os.ErrNotExist)
					}
				})
				notified := false
				for _, ran := range f.ran {
					notified = notified || strings.HasPrefix(ran, "notify-send ")
				}
				if notified != tt.want {
					t.Errorf("%s notified = %v, want %v (ran %q)", outcome, notified, tt.want, f.ran)
				}
			}
		})
	}
}

var containerPathLine = regexp.MustCompile(`^` + regexp.QuoteMeta(model.ContainerImageInboxDir) + `/\d{8}T\d{6}Z-[0-9a-f]{8}\.png\n$`)

type imgImportEnv struct {
	home     string
	desktop  *fakeDesktop
	backend  *inboxListingBackend
	pngPath  string
	inboxDir string
}

// setupImgImport isolates Run: a temporary home, a fake engine listing the
// given sessions, a fake desktop with notify-send, and no update check.
func setupImgImport(t *testing.T, sessions ...backend.Session) *imgImportEnv {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Chdir(t.TempDir())

	env := &imgImportEnv{
		home:    home,
		desktop: waylandDesktop(),
		backend: &inboxListingBackend{name: backend.NameDocker, sessions: sessions},
		pngPath: filepath.Join(t.TempDir(), "shot.png"),
	}
	env.desktop.installed["notify-send"] = true
	if err := os.WriteFile(env.pngPath, pngMagic, 0o600); err != nil {
		t.Fatal(err)
	}
	resolvedHome, err := config.ResolveHostHome()
	if err != nil {
		t.Fatal(err)
	}
	env.home = resolvedHome
	env.inboxDir = config.HostImageInboxDir(resolvedHome, backend.NameDocker, "hashA")

	previousDesktop, previousCheck, previousBackend, previousPrompt := imgImportDesktop, imgImportCheckDocker, imgImportBackend, promptUsable
	t.Cleanup(func() {
		imgImportDesktop, imgImportCheckDocker, imgImportBackend, promptUsable = previousDesktop, previousCheck, previousBackend, previousPrompt
	})
	imgImportDesktop = env.desktop.desktop
	imgImportCheckDocker = func() error { return nil }
	imgImportBackend = func(model.Options) (backend.Backend, error) { return env.backend, nil }
	promptUsable = func() bool { return false }
	return env
}

// runImgImportWithStdin runs `enclave img import` with args and data on stdin.
func runImgImportWithStdin(t *testing.T, data []byte, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = writer.Write(data)
		_ = writer.Close()
	}()
	previousStdin := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() { os.Stdin = previousStdin; _ = reader.Close() })

	stdout, stderr = captureOutput(t, func() {
		code = Run(append([]string{"img", "import", "--backend", backend.NameDocker}, args...))
	})
	return code, stdout, stderr
}

func contractSessions() []backend.Session {
	withInbox := inboxSession("enclave-claude-a", "hashA", "/src/a", true)
	withInbox.Name = "review"
	withoutInbox := inboxSession("enclave-claude-c", "hashC", "/src/c", false)
	return []backend.Session{withInbox, withoutInbox}
}

// The invocation an integration uses: an explicit session, no clipboard, the
// image on stdin. Stdout is the path alone even with --verbose and while the
// inbox sweep waits for a starting session, and nothing notifies the desktop.
func TestImgImportFromStdinPrintsOnlyThePath(t *testing.T) {
	env := setupImgImport(t, contractSessions()...)
	release, err := util.AcquireSharedFileLock(config.HostImageInboxLockPath(env.home), nil)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(200*time.Millisecond, release)
	t.Cleanup(func() { timer.Stop() })

	code, stdout, stderr := runImgImportWithStdin(t, pngMagic, "--verbose", "--session", "enclave-claude-a", "--no-clipboard", "-")

	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	if !containerPathLine.MatchString(stdout) {
		t.Fatalf("stdout = %q, want only the container path", stdout)
	}
	if !strings.Contains(stderr, "Waiting for an enclave session to finish starting") {
		t.Fatalf("stderr = %q, want the sweep's wait message there", stderr)
	}
	stored, err := os.ReadFile(filepath.Join(env.inboxDir, filepath.Base(strings.TrimSpace(stdout))))
	if err != nil || !reflect.DeepEqual(stored, pngMagic) {
		t.Fatalf("stored image = %v, %v; want the stdin bytes", stored, err)
	}
	if len(env.desktop.ran) != 0 {
		t.Fatalf("ran %q, want no notification or clipboard write", env.desktop.ran)
	}
}

func TestImgImportJSON(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantSession any
	}{
		{"with session", []string{"--session", "enclave-claude-a"}, map[string]any{"name": "enclave-claude-a", "sessionName": "review"}},
		{"without session", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupImgImport(t, contractSessions()...)

			code, stdout, stderr := runImgImportWithStdin(t, pngMagic, append(tt.args, "--json", "-")...)

			if code != 0 {
				t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
				t.Fatalf("unmarshal: %v\n%s", err, stdout)
			}
			path, _ := doc["path"].(string)
			if !containerPathLine.MatchString(path + "\n") {
				t.Fatalf("path = %q, want a container path", path)
			}
			want := map[string]any{
				"schemaVersion": "1",
				"kind":          "image-import",
				"path":          path,
				"project":       map[string]any{"dir": "/src/a", "hash": "hashA"},
				"session":       tt.wantSession,
			}
			if !reflect.DeepEqual(doc, want) {
				t.Fatalf("envelope = %#v, want %#v", doc, want)
			}
		})
	}
}

// A failed import exits 1 with nothing on stdout, with or without --json, and
// one `error: ` line on stderr that a caller can show as is.
func TestImgImportFailureReportsOnStderrOnly(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		args   []string
		errHas string
	}{
		{"session without inbox", pngMagic, []string{"--session", "enclave-claude-c"}, "has no image inbox"},
		{"unknown session", pngMagic, []string{"--session", "enclave-claude-nope"}, `"enclave-claude-nope"`},
		{"not an image", []byte("hello"), []string{"--session", "enclave-claude-a"}, "not a PNG or JPEG"},
	}
	for _, tt := range tests {
		for _, format := range [][]string{nil, {"--json"}} {
			t.Run(strings.TrimSpace(tt.name+" "+strings.Join(format, "")), func(t *testing.T) {
				env := setupImgImport(t, contractSessions()...)
				args := append(append(append([]string{}, tt.args...), format...), "--no-clipboard", "-")

				code, stdout, stderr := runImgImportWithStdin(t, tt.data, args...)

				if code != 1 {
					t.Fatalf("exit code = %d, want 1", code)
				}
				if stdout != "" {
					t.Fatalf("stdout = %q, want nothing", stdout)
				}
				if !strings.HasPrefix(stderr, "error: ") || !strings.Contains(stderr, tt.errHas) {
					t.Fatalf("stderr = %q, want an `error: ` line mentioning %q", stderr, tt.errHas)
				}
				if len(env.desktop.ran) != 0 {
					t.Fatalf("ran %q, want no notification", env.desktop.ran)
				}
				if entries, _ := os.ReadDir(env.inboxDir); len(entries) != 0 {
					t.Fatalf("inbox holds %d entries, want none", len(entries))
				}
			})
		}
	}
}
