// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

var (
	pngMagic  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	jpegMagic = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
)

func TestSniffImageType(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		wantMIME string
		wantExt  string
		wantOK   bool
	}{
		{"png", pngMagic, "image/png", "png", true},
		{"jpeg", jpegMagic, "image/jpeg", "jpg", true},
		{"gif", []byte("GIF89a"), "", "", false},
		{"empty", nil, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mime, ext, ok := sniffImageType(tt.data)
			if mime != tt.wantMIME || ext != tt.wantExt || ok != tt.wantOK {
				t.Fatalf("sniffImageType = (%q,%q,%v), want (%q,%q,%v)", mime, ext, ok, tt.wantMIME, tt.wantExt, tt.wantOK)
			}
		})
	}
}

func TestValidateImage(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		advertised string
		maxBytes   int64
		wantExt    string
		wantErr    bool
	}{
		{"png ok", pngMagic, "image/png", 1024, "png", false},
		{"jpeg alias ok", jpegMagic, "image/jpg", 1024, "jpg", false},
		{"no advertised ok", pngMagic, "", 1024, "png", false},
		{"mime mismatch", pngMagic, "image/jpeg", 1024, "", true},
		{"too large", pngMagic, "image/png", 4, "", true},
		{"not image", []byte("hello world"), "", 1024, "", true},
		{"empty", nil, "", 1024, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext, err := validateImage(tt.data, tt.advertised, tt.maxBytes)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got ext=%q", ext)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ext != tt.wantExt {
				t.Fatalf("ext = %q, want %q", ext, tt.wantExt)
			}
		})
	}
}

func TestAdvertisedMIMEForPath(t *testing.T) {
	tests := map[string]string{
		"shot.png":      "image/png",
		"shot.PNG":      "image/png",
		"photo.jpg":     "image/jpeg",
		"photo.jpeg":    "image/jpeg",
		"anim.gif":      "",
		"no-extension":  "",
		"dir.png/blob":  "",
		"archive.tar.x": "",
	}
	for path, want := range tests {
		if got := advertisedMIMEForPath(path); got != want {
			t.Errorf("advertisedMIMEForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestReadHostImageCapsInput(t *testing.T) {
	data, err := readHostImage(bytes.NewReader(pngMagic), int64(len(pngMagic)))
	if err != nil || !bytes.Equal(data, pngMagic) {
		t.Fatalf("readHostImage at the cap = (%v, %v)", data, err)
	}
	if _, err := readHostImage(bytes.NewReader(pngMagic), int64(len(pngMagic))-1); err == nil {
		t.Fatal("expected an error for input over the cap")
	}
}

func TestInboxFilename(t *testing.T) {
	ts := time.Date(2026, 7, 6, 14, 57, 21, 0, time.UTC)
	got := inboxFilename(ts, []byte{0xAB, 0xCD, 0xEF, 0x01}, "png")
	want := "20260706T145721Z-abcdef01.png"
	if got != want {
		t.Fatalf("inboxFilename = %q, want %q", got, want)
	}
}

func TestWriteHostImageWritesToProjectInbox(t *testing.T) {
	home := t.TempDir()
	containerPath, err := writeHostImage(home, "projA", jpegMagic, "jpg")
	if err != nil {
		t.Fatalf("writeHostImage: %v", err)
	}
	if !regexp.MustCompile(`^/mnt/host-images/\d{8}T\d{6}Z-[0-9a-f]{8}\.jpg$`).MatchString(containerPath) {
		t.Fatalf("container path = %q", containerPath)
	}
	inbox := config.HostImageInboxDir(home, "projA")
	final := filepath.Join(inbox, filepath.Base(containerPath))
	info, err := os.Stat(final)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
	if got, _ := os.ReadFile(final); !bytes.Equal(got, jpegMagic) {
		t.Fatal("content mismatch")
	}
	if dirInfo, err := os.Stat(inbox); err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("inbox dir = %v, %v; want mode 700", dirInfo, err)
	}
	entries, _ := os.ReadDir(inbox)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file (no temp leftovers), got %d", len(entries))
	}
	if _, err := os.Stat(config.HostImageInboxDir(home, "projB")); !os.IsNotExist(err) {
		t.Fatalf("another project's inbox was touched: %v", err)
	}
}

func inboxSession(name, hash, dir string, inbox bool) backend.Session {
	s := backend.Session{
		Ref:         backend.SessionRef{Name: name},
		ProjectHash: hash,
		Worktree:    dir,
		ProjectDir:  dir,
		Status:      "running",
	}
	if inbox {
		s.Provenance = map[string]any{"image_inbox": true}
	} else {
		s.Provenance = map[string]any{"image_inbox": false}
	}
	return s
}

func TestResolveInboxProject(t *testing.T) {
	a := inboxSession("enclave-claude-a", "hashA", "/src/a", true)
	a.Name = "review"
	a.Ref.ID = "aaaaaaaa1111"
	a2 := inboxSession("enclave-codex-a", "hashA", "/src/a", true)
	b := inboxSession("enclave-claude-b", "hashB", "/src/b", true)
	b.Name = "review"
	b.Ref.ID = "bbbbbbbb2222"
	cPlain := inboxSession("enclave-claude-c", "hashC", "/src/c", false)
	cPlain.Name = "plain"
	cPlain.Ref.ID = "cccccccc3333"
	cReview := cPlain
	cReview.Name = "review"
	legacy := backend.Session{Ref: backend.SessionRef{Name: "enclave-old"}, ProjectHash: "hashD"}

	tests := []struct {
		name      string
		sessions  []backend.Session
		cwd       model.Project
		requested string
		want      string
		errHas    []string
	}{
		{"session by container name", []backend.Session{a, b}, model.Project{}, "enclave-claude-b", "hashB", nil},
		{"session by ID prefix", []backend.Session{a, b}, model.Project{}, "bbbbbbbb", "hashB", nil},
		{"session name prefers cwd project", []backend.Session{a, b}, model.Project{Hash: "hashB"}, "review", "hashB", nil},
		{"session name outside cwd project", []backend.Session{b, cPlain}, model.Project{Hash: "hashC"}, "review", "hashB", nil},
		{"cwd session name without inbox is not skipped", []backend.Session{b, cReview}, model.Project{Hash: "hashC"}, "review", "", []string{"no image inbox"}},
		{"session name in two other projects is ambiguous", []backend.Session{a, b}, model.Project{Hash: "hashC"}, "review", "", []string{"enclave-claude-a", "enclave-claude-b"}},
		{"session without inbox", []backend.Session{a, cPlain}, model.Project{}, "plain", "", []string{`"plain"`, "no image inbox", "--image-inbox"}},
		{"session without inbox by container name", []backend.Session{a, cPlain}, model.Project{}, "enclave-claude-c", "", []string{"no image inbox"}},
		{"unknown session", []backend.Session{a}, model.Project{}, "nope", "", []string{`"nope"`}},
		{"session beats cwd project", []backend.Session{a, b}, model.Project{Hash: "hashA"}, "enclave-claude-b", "hashB", nil},
		{"cwd project wins", []backend.Session{a, b}, model.Project{Hash: "hashB"}, "", "hashB", nil},
		{"two sessions of one project", []backend.Session{a, a2}, model.Project{Hash: "hashC"}, "", "hashA", nil},
		{"single inbox project from elsewhere", []backend.Session{a, a2, cPlain}, model.Project{Hash: "hashC"}, "", "hashA", nil},
		{"single inbox project without cwd project", []backend.Session{a, legacy}, model.Project{}, "", "hashA", nil},
		{"ambiguous lists candidates", []backend.Session{a, a2, b}, model.Project{Hash: "hashC"}, "", "", []string{"enclave-codex-a, review (enclave-claude-a)", "review (enclave-claude-b)", "/src/a", "/src/b", "--session"}},
		{"no inbox session", []backend.Session{cPlain, legacy}, model.Project{Hash: "hashC"}, "", "", []string{"no running session has an image inbox", "--image-inbox"}},
		{"nothing running", nil, model.Project{}, "", "", []string{"no running session has an image inbox"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveInboxProject(tt.sessions, tt.cwd, tt.requested)
			if tt.errHas != nil {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				for _, s := range tt.errHas {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q does not mention %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("project = %q, want %q", got, tt.want)
			}
		})
	}
}

// img import finds inbox-enabled sessions by this key in their recorded
// options, so the option must keep its name and stay session-affecting.
func TestImageInboxOptionIsRecordedOnSessions(t *testing.T) {
	for _, def := range config.OptionDefs() {
		if def.Name == imageInboxOption {
			if def.AffectsSession != config.SessionAffected || def.Kind != config.OptionKindBool {
				t.Fatalf("%s must be a session-affecting bool, got %v/%v", imageInboxOption, def.AffectsSession, def.Kind)
			}
			return
		}
	}
	t.Fatalf("no option named %q", imageInboxOption)
}

func TestCopiesPathToClipboard(t *testing.T) {
	tests := []struct {
		img  model.ImgOptions
		want bool
	}{
		{model.ImgOptions{}, true},
		{model.ImgOptions{ImgScreenshot: true}, true},
		{model.ImgOptions{ImgNoClipboard: true}, false},
		{model.ImgOptions{ImgScreenshot: true, ImgNoClipboard: true}, false},
		{model.ImgOptions{ImgFile: "shot.png"}, false},
		{model.ImgOptions{ImgStdin: true}, false},
	}
	for _, tt := range tests {
		if got := copiesPathToClipboard(tt.img); got != tt.want {
			t.Errorf("copiesPathToClipboard(%+v) = %v, want %v", tt.img, got, tt.want)
		}
	}
}

func TestReportImportPrintsOnlyThePathOnStdout(t *testing.T) {
	const containerPath = model.ContainerImageInboxDir + "/20260101T000000Z-0123abcd.png"
	const imported = "ok: Imported host image for /work/project"
	const copiedNotice = "info: Container path copied to the clipboard"
	tests := []struct {
		name       string
		img        model.ImgOptions
		wantCopied bool
	}{
		{"file", model.ImgOptions{ImgFile: "shot.png"}, false},
		{"stdin", model.ImgOptions{ImgStdin: true}, false},
		{"clipboard", model.ImgOptions{}, true},
		{"clipboard without copy", model.ImgOptions{ImgNoClipboard: true}, false},
		{"screenshot", model.ImgOptions{ImgScreenshot: true}, true},
		{"screenshot without copy", model.ImgOptions{ImgScreenshot: true, ImgNoClipboard: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "1")
			f := waylandDesktop()
			stdout, stderr := captureOutput(t, func() {
				reportImport(tt.img, f.desktop(), containerPath, "/work/project")
			})
			if stdout != containerPath+"\n" {
				t.Errorf("stdout = %q, want only the container path", stdout)
			}
			if !strings.Contains(stderr, imported) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, imported)
			}
			if got := strings.Contains(stderr, copiedNotice); got != tt.wantCopied {
				t.Errorf("stderr = %q, clipboard notice present = %v, want %v", stderr, got, tt.wantCopied)
			}
			wantClipboard := map[string]string(nil)
			if tt.wantCopied {
				wantClipboard = map[string]string{"wl-copy": containerPath}
			}
			if !reflect.DeepEqual(f.copied, wantClipboard) {
				t.Errorf("clipboard writes = %v, want %v", f.copied, wantClipboard)
			}
		})
	}
}

func TestReportImportWarnsWhenTheClipboardCopyFails(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const containerPath = model.ContainerImageInboxDir + "/20260101T000000Z-0123abcd.png"
	f := waylandDesktop()
	f.failures["wl-copy"] = errors.New("compositor gone")
	stdout, stderr := captureOutput(t, func() {
		reportImport(model.ImgOptions{}, f.desktop(), containerPath, "/work/project")
	})
	if stdout != containerPath+"\n" {
		t.Errorf("stdout = %q, want only the container path", stdout)
	}
	if !strings.Contains(stderr, "warn: Could not copy the container path to the clipboard: compositor gone") {
		t.Errorf("stderr = %q, want the clipboard warning", stderr)
	}
	if strings.Contains(stderr, "copied to the clipboard;") {
		t.Errorf("stderr = %q, want no clipboard success notice", stderr)
	}
}
