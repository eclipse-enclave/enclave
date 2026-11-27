// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var (
	shimPNG  = []byte("\x89PNG\r\n\x1a\nfake-png")
	shimJPEG = []byte("\xff\xd8\xfffake-jpeg")
)

type shimEnv struct {
	t            *testing.T
	bin          string
	inbox        string
	extraEnv     []string
	withoutInbox bool
}

func newShimEnv(t *testing.T) *shimEnv {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "runtime-assets", "clipboard-shim.sh"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	bin := t.TempDir()
	for _, name := range []string{"xclip", "wl-paste"} {
		if err := os.Symlink(script, filepath.Join(bin, name)); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}
	return &shimEnv{t: t, bin: bin, inbox: t.TempDir()}
}

func (e *shimEnv) stage(name string, data []byte, age time.Duration) {
	e.t.Helper()
	path := filepath.Join(e.inbox, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		e.t.Fatalf("write: %v", err)
	}
	mtime := time.Now().Add(-age)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		e.t.Fatalf("chtimes: %v", err)
	}
}

func (e *shimEnv) run(tool string, args ...string) ([]byte, int) {
	e.t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(e.bin, tool)}, args...)...)
	cmd.Env = append([]string{"PATH=" + e.bin + string(os.PathListSeparator) + os.Getenv("PATH")}, e.extraEnv...)
	if !e.withoutInbox {
		cmd.Env = append(cmd.Env, "ENCLAVE_IMAGE_INBOX="+e.inbox)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			e.t.Fatalf("run %s %v: %v", tool, args, err)
		}
		return out, exitErr.ExitCode()
	}
	return out, 0
}

func (e *shimEnv) expectNoImage(tool string, args ...string) {
	e.t.Helper()
	out, code := e.run(tool, args...)
	if code == 0 || len(out) != 0 {
		e.t.Fatalf("%s %v: got exit %d and %q, want non-zero exit and no output", tool, args, code, out)
	}
}

func (e *shimEnv) expectOutput(want []byte, tool string, args ...string) {
	e.t.Helper()
	out, code := e.run(tool, args...)
	if code != 0 || !bytes.Equal(out, want) {
		e.t.Fatalf("%s %v: got exit %d and %q, want exit 0 and %q", tool, args, code, out, want)
	}
}

func TestClipboardShimServesFreshPNG(t *testing.T) {
	e := newShimEnv(t)
	e.stage("20261119T100000Z-aabbccdd.png", shimPNG, 5*time.Second)

	e.expectOutput([]byte("image/png\n"), "xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
	e.expectOutput([]byte("image/png\n"), "xclip", "-selection", "clipboard", "-t", "TARGETS", "-o")
	e.expectOutput(shimPNG, "xclip", "-selection", "clipboard", "-o", "-t", "image/png")
	e.expectOutput(shimPNG, "xclip", "-selection", "clipboard", "-t", "image/png", "-o")
	e.expectOutput([]byte("image/png\n"), "wl-paste", "--list-types")
	e.expectOutput([]byte("image/png\n"), "wl-paste", "-l")
	e.expectOutput(shimPNG, "wl-paste", "-t", "image/png")
	e.expectOutput(shimPNG, "wl-paste", "--type", "image/png", "--no-newline")
	e.expectOutput(shimPNG, "wl-paste", "--type=image/png")
	e.expectNoImage("xclip", "-selection", "clipboard", "-o", "-t", "image/jpeg")
	e.expectNoImage("wl-paste", "--type", "image/jpeg")
}

func TestClipboardShimServesFreshJPEGOnlyAsJPEG(t *testing.T) {
	e := newShimEnv(t)
	e.stage("20261119T100000Z-aabbccdd.jpg", shimJPEG, time.Second)

	e.expectOutput([]byte("image/jpeg\n"), "xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
	e.expectOutput(shimJPEG, "xclip", "-selection", "clipboard", "-o", "-t", "image/jpeg")
	e.expectOutput(shimJPEG, "wl-paste", "--type", "image/jpeg", "--no-newline")
	e.expectNoImage("xclip", "-selection", "clipboard", "-t", "image/png", "-o")
	e.expectNoImage("wl-paste", "-t", "image/png")
}

func TestClipboardShimServesNewestImage(t *testing.T) {
	e := newShimEnv(t)
	e.stage("20261119T100000Z-11111111.jpg", shimJPEG, 20*time.Second)
	e.stage("20261119T100010Z-22222222.png", shimPNG, 2*time.Second)

	e.expectOutput([]byte("image/png\n"), "xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
}

func TestClipboardShimIgnoresImagesOlderThanWindow(t *testing.T) {
	e := newShimEnv(t)
	e.stage("20261119T100000Z-aabbccdd.png", shimPNG, 2*time.Minute)

	e.expectNoImage("xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
	e.expectNoImage("xclip", "-selection", "clipboard", "-o", "-t", "image/png")
	e.expectNoImage("wl-paste", "--list-types")
	e.expectNoImage("wl-paste", "-t", "image/png")
}

func TestClipboardShimWindowBoundary(t *testing.T) {
	e := newShimEnv(t)
	e.stage("20261119T100000Z-11111111.png", shimPNG, 50*time.Second)
	e.expectOutput([]byte("image/png\n"), "xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")

	e.stage("20261119T100000Z-11111111.png", shimPNG, 70*time.Second)
	e.expectNoImage("xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
}

func TestClipboardShimIgnoresPartialWritesAndOtherFiles(t *testing.T) {
	e := newShimEnv(t)
	e.stage(".20261119T100000Z-aabbccdd.png.tmp-123", shimPNG, 0)
	e.stage("notes.txt", []byte("hello"), 0)

	e.expectNoImage("xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
	e.expectNoImage("wl-paste", "--list-types")
}

func TestClipboardShimReportsNoImageWithoutInbox(t *testing.T) {
	e := newShimEnv(t)
	e.stage("20261119T100000Z-aabbccdd.png", shimPNG, 0)
	e.withoutInbox = true

	e.expectNoImage("xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
	e.expectNoImage("xclip", "-selection", "clipboard", "-o", "-t", "image/png")
	e.expectNoImage("wl-paste", "--list-types")
	e.expectNoImage("wl-paste", "-t", "image/png")
}

func TestClipboardShimReportsNoImageForEmptyOrMissingInbox(t *testing.T) {
	e := newShimEnv(t)
	e.expectNoImage("xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")

	e.inbox = filepath.Join(e.inbox, "missing")
	e.expectNoImage("wl-paste", "--list-types")
}

func TestClipboardShimRefusesTextAndOtherModes(t *testing.T) {
	e := newShimEnv(t)
	e.stage("20261119T100000Z-aabbccdd.png", shimPNG, 0)

	e.expectNoImage("xclip", "-selection", "clipboard", "-o")
	e.expectNoImage("xclip", "-selection", "clipboard", "-o", "-t", "UTF8_STRING")
	e.expectNoImage("xclip", "-selection", "clipboard", "-i")
	e.expectNoImage("xclip", "-selection", "clipboard", "-t", "image/png")
	e.expectNoImage("xclip", "-o", "-t", "image/png")
	e.expectNoImage("xclip", "-selection", "primary", "-o", "-t", "image/png")
	e.expectNoImage("wl-paste", "--no-newline")
	e.expectNoImage("wl-paste")
	e.expectNoImage("wl-paste", "--no-newline", "--type", "text/plain")
	e.expectNoImage("wl-paste", "--primary", "--type", "image/png")
	e.expectNoImage("xclip", "-selection", "clipboard", "-o", "-t")
	e.expectNoImage("xclip", "-o", "-selection")
	e.expectNoImage("wl-paste", "--type")
}

// Copy helpers such as OpenCode's pipe the copied text into
// `xclip -selection clipboard` without handling a write error, so the shim
// must read all of stdin before refusing; exiting first breaks the pipe.
func TestClipboardShimDrainsStdinInXclipCopyMode(t *testing.T) {
	e := newShimEnv(t)
	cmd := exec.Command("sh", filepath.Join(e.bin, "xclip"), "-selection", "clipboard")
	cmd.Env = []string{"PATH=" + e.bin + string(os.PathListSeparator) + os.Getenv("PATH"), "ENCLAVE_IMAGE_INBOX=" + e.inbox}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Larger than a pipe buffer, so the write only completes if the shim reads.
	_, writeErr := stdin.Write(bytes.Repeat([]byte("x"), 1<<20))
	closeErr := stdin.Close()
	err = cmd.Wait()

	if writeErr != nil || closeErr != nil {
		t.Fatalf("writing the copied text failed: %v, %v", writeErr, closeErr)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("copy mode exited with %v, want exit status 1", err)
	}
}

func TestClipboardShimDelegatesToRealToolWithDisplay(t *testing.T) {
	e := newShimEnv(t)
	e.stage("20261119T100000Z-aabbccdd.png", shimPNG, 0)
	realBin := t.TempDir()
	for _, name := range []string{"xclip", "wl-paste"} {
		if err := os.WriteFile(filepath.Join(realBin, name), []byte("#!/bin/sh\necho real-"+name+"\n"), 0o755); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	e.extraEnv = []string{"PATH=" + e.bin + string(os.PathListSeparator) + realBin + string(os.PathListSeparator) + "/usr/bin:/bin"}

	e.expectOutput(shimPNG, "xclip", "-selection", "clipboard", "-o", "-t", "image/png")

	e.extraEnv = append(e.extraEnv, "DISPLAY=:0")
	e.expectOutput([]byte("real-xclip\n"), "xclip", "-selection", "clipboard", "-o")
	e.expectOutput(shimPNG, "wl-paste", "-t", "image/png")

	e.extraEnv = append(e.extraEnv, "WAYLAND_DISPLAY=wayland-0")
	e.expectOutput([]byte("real-wl-paste\n"), "wl-paste")
}
