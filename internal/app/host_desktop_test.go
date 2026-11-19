// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// fakeDesktop scripts the host tools img import runs. Each tool invocation is
// keyed by its full command line.
type fakeDesktop struct {
	goos      string
	kernel    string
	env       map[string]string
	installed map[string]bool
	outputs   map[string][]byte
	failures  map[string]error
	ran       []string
	copied    map[string]string
}

func (f *fakeDesktop) desktop() hostDesktop {
	return hostDesktop{
		goos:          f.goos,
		kernelRelease: func() string { return f.kernel },
		getenv:        func(key string) string { return f.env[key] },
		lookPath: func(name string) (string, error) {
			if f.installed[name] {
				return "/usr/bin/" + name, nil
			}
			return "", exec.ErrNotFound
		},
		runCapture: func(_ context.Context, _ int64, name string, args ...string) ([]byte, error) {
			line := strings.Join(append([]string{name}, args...), " ")
			f.ran = append(f.ran, line)
			if err := f.failures[line]; err != nil {
				return nil, err
			}
			return f.outputs[line], nil
		},
		runWithStdin: func(_ context.Context, stdin string, name string, args ...string) error {
			line := strings.Join(append([]string{name}, args...), " ")
			f.ran = append(f.ran, line)
			if err := f.failures[line]; err != nil {
				return err
			}
			if f.copied == nil {
				f.copied = map[string]string{}
			}
			f.copied[line] = stdin
			return nil
		},
	}
}

func waylandDesktop() *fakeDesktop {
	return &fakeDesktop{
		goos:      "linux",
		env:       map[string]string{"WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":0"},
		installed: map[string]bool{"wl-paste": true, "wl-copy": true, "grim": true, "slurp": true},
		outputs:   map[string][]byte{},
		failures:  map[string]error{},
	}
}

func x11Desktop() *fakeDesktop {
	return &fakeDesktop{
		goos:      "linux",
		env:       map[string]string{"DISPLAY": ":0"},
		installed: map[string]bool{"xclip": true, "maim": true},
		outputs:   map[string][]byte{},
		failures:  map[string]error{},
	}
}

func TestCaptureClipboardWayland(t *testing.T) {
	f := waylandDesktop()
	f.outputs["wl-paste --list-types"] = []byte("text/html\nimage/jpeg\nimage/png\n")
	f.outputs["wl-paste --no-newline --type image/png"] = pngMagic

	data, mime, err := f.desktop().captureClipboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/png" || !bytes.Equal(data, pngMagic) {
		t.Fatalf("captureClipboard = %v, %q", data, mime)
	}
}

func TestCaptureClipboardX11(t *testing.T) {
	f := x11Desktop()
	f.outputs["xclip -selection clipboard -target TARGETS -out"] = []byte("TARGETS\nimage/jpeg\n")
	f.outputs["xclip -selection clipboard -target image/jpeg -out"] = jpegMagic

	data, mime, err := f.desktop().captureClipboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/jpeg" || !bytes.Equal(data, jpegMagic) {
		t.Fatalf("captureClipboard = %v, %q", data, mime)
	}
}

// The advertised type travels with the data so that validateImage rejects
// content that contradicts it.
func TestCaptureClipboardReportsAdvertisedType(t *testing.T) {
	f := waylandDesktop()
	f.outputs["wl-paste --list-types"] = []byte("image/png\n")
	f.outputs["wl-paste --no-newline --type image/png"] = jpegMagic

	data, mime, err := f.desktop().captureClipboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateImage(data, mime, 1024); err == nil {
		t.Fatal("JPEG content advertised as image/png was accepted")
	}
}

func TestCaptureScreenshotWayland(t *testing.T) {
	f := waylandDesktop()
	f.outputs["slurp"] = []byte("10,20 300x200\n")
	f.outputs["grim -g 10,20 300x200 -"] = pngMagic

	data, mime, err := f.desktop().captureScreenshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/png" || !bytes.Equal(data, pngMagic) {
		t.Fatalf("captureScreenshot = %v, %q", data, mime)
	}
}

func TestCaptureScreenshotX11(t *testing.T) {
	f := x11Desktop()
	f.outputs["maim --select --format png"] = pngMagic

	data, mime, err := f.desktop().captureScreenshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/png" || !bytes.Equal(data, pngMagic) {
		t.Fatalf("captureScreenshot = %v, %q", data, mime)
	}
}

func TestCopyTextToClipboard(t *testing.T) {
	for name, f := range map[string]struct {
		desktop *fakeDesktop
		tool    string
	}{
		"wayland": {waylandDesktop(), "wl-copy"},
		"x11":     {x11Desktop(), "xclip -selection clipboard -in"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := f.desktop.desktop().copyText(context.Background(), "/mnt/host-images/a.png"); err != nil {
				t.Fatal(err)
			}
			if got := f.desktop.copied[f.tool]; got != "/mnt/host-images/a.png" {
				t.Fatalf("copied %q via %q, all: %v", got, f.tool, f.desktop.copied)
			}
		})
	}
}

func TestDesktopCaptureErrors(t *testing.T) {
	tests := []struct {
		name       string
		setup      func() *fakeDesktop
		screenshot bool
		is         error
		has        []string
	}{
		{
			name:  "macOS",
			setup: func() *fakeDesktop { f := waylandDesktop(); f.goos = "darwin"; return f },
			is:    errCaptureUnsupported,
			has:   []string{"macOS", "--file"},
		},
		{
			name:       "WSL",
			setup:      func() *fakeDesktop { f := waylandDesktop(); f.env["WSL_DISTRO_NAME"] = "Ubuntu"; return f },
			screenshot: true,
			is:         errCaptureUnsupported,
			has:        []string{"WSL", "--file"},
		},
		{
			name: "WSL without its environment",
			setup: func() *fakeDesktop {
				f := waylandDesktop()
				f.kernel = "6.6.87.2-microsoft-standard-WSL2"
				return f
			},
			is:  errCaptureUnsupported,
			has: []string{"WSL"},
		},
		{
			name:  "no display",
			setup: func() *fakeDesktop { f := waylandDesktop(); f.env = map[string]string{}; return f },
			is:    errCaptureUnsupported,
			has:   []string{"WAYLAND_DISPLAY", "DISPLAY", "--file"},
		},
		{
			name:  "wl-paste missing",
			setup: func() *fakeDesktop { f := waylandDesktop(); delete(f.installed, "wl-paste"); return f },
			is:    errCaptureToolMissing,
			has:   []string{"wl-paste", "wl-clipboard"},
		},
		{
			name:  "xclip missing",
			setup: func() *fakeDesktop { f := x11Desktop(); delete(f.installed, "xclip"); return f },
			is:    errCaptureToolMissing,
			has:   []string{"xclip"},
		},
		{
			name: "grim and slurp missing",
			setup: func() *fakeDesktop {
				f := waylandDesktop()
				delete(f.installed, "grim")
				delete(f.installed, "slurp")
				return f
			},
			screenshot: true,
			is:         errCaptureToolMissing,
			has:        []string{"grim", "slurp"},
		},
		{
			name:       "maim missing",
			setup:      func() *fakeDesktop { f := x11Desktop(); delete(f.installed, "maim"); return f },
			screenshot: true,
			is:         errCaptureToolMissing,
			has:        []string{"maim"},
		},
		{
			name: "empty Wayland clipboard",
			setup: func() *fakeDesktop {
				f := waylandDesktop()
				f.failures["wl-paste --list-types"] = &toolExitError{tool: "wl-paste", stderr: "No selection"}
				return f
			},
			is: errClipboardEmpty,
		},
		{
			name: "empty X11 clipboard",
			setup: func() *fakeDesktop {
				f := x11Desktop()
				f.failures["xclip -selection clipboard -target TARGETS -out"] = &toolExitError{tool: "xclip", stderr: "Error: target TARGETS not available"}
				return f
			},
			is: errClipboardEmpty,
		},
		{
			name: "clipboard holds text",
			setup: func() *fakeDesktop {
				f := waylandDesktop()
				f.outputs["wl-paste --list-types"] = []byte("text/plain\nUTF8_STRING\n")
				return f
			},
			is:  errClipboardNoImage,
			has: []string{"text/plain"},
		},
		{
			name: "Wayland selection cancelled",
			setup: func() *fakeDesktop {
				f := waylandDesktop()
				f.failures["slurp"] = &toolExitError{tool: "slurp", stderr: "selection cancelled"}
				return f
			},
			screenshot: true,
			is:         errSelectionCancelled,
		},
		{
			name: "X11 selection cancelled",
			setup: func() *fakeDesktop {
				f := x11Desktop()
				f.failures["maim --select --format png"] = &toolExitError{tool: "maim", stderr: "Selection was cancelled by keystroke or right-click."}
				return f
			},
			screenshot: true,
			is:         errSelectionCancelled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.setup().desktop()
			var err error
			if tt.screenshot {
				_, _, err = d.captureScreenshot(context.Background())
			} else {
				_, _, err = d.captureClipboard(context.Background())
			}
			if !errors.Is(err, tt.is) {
				t.Fatalf("error %v, want %v", err, tt.is)
			}
			for _, s := range tt.has {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not mention %q", err, s)
				}
			}
		})
	}
}

// A tool failure that is neither an empty clipboard nor a cancelled
// selection keeps the tool's own message.
func TestDesktopToolFailureKeepsStderr(t *testing.T) {
	f := waylandDesktop()
	f.failures["wl-paste --list-types"] = &toolExitError{tool: "wl-paste", stderr: "Failed to connect to a Wayland server"}

	_, _, err := f.desktop().captureClipboard(context.Background())
	if err == nil || errors.Is(err, errClipboardEmpty) || !strings.Contains(err.Error(), "Failed to connect") {
		t.Fatalf("error = %v", err)
	}
}

// Platform and tool checks run without invoking any tool, so a screenshot
// import can fail before the user selects a region.
func TestCheckScreenshotRunsNothing(t *testing.T) {
	f := waylandDesktop()
	delete(f.installed, "slurp")
	if err := f.desktop().checkScreenshot(); !errors.Is(err, errCaptureToolMissing) {
		t.Fatalf("checkScreenshot = %v", err)
	}
	if len(f.ran) != 0 {
		t.Fatalf("ran %v", f.ran)
	}
	if err := waylandDesktop().desktop().checkScreenshot(); err != nil {
		t.Fatalf("checkScreenshot with tools = %v", err)
	}
}

func TestNotifySendsDesktopNotification(t *testing.T) {
	f := waylandDesktop()
	f.installed["notify-send"] = true
	if err := f.desktop().notify(context.Background(), notifyNormal, "Imported host image", "for /work/project"); err != nil {
		t.Fatalf("notify: %v", err)
	}
	want := []string{"notify-send --app-name=enclave --urgency=normal -- Imported host image for /work/project"}
	if !reflect.DeepEqual(f.ran, want) {
		t.Errorf("ran = %q, want %q", f.ran, want)
	}
}

func TestNotifyEscapesBodyMarkup(t *testing.T) {
	f := waylandDesktop()
	f.installed["notify-send"] = true
	if err := f.desktop().notify(context.Background(), notifyCritical, "Host image import failed", "pass --session <target> & retry"); err != nil {
		t.Fatalf("notify: %v", err)
	}
	want := []string{"notify-send --app-name=enclave --urgency=critical -- Host image import failed pass --session &lt;target&gt; &amp; retry"}
	if !reflect.DeepEqual(f.ran, want) {
		t.Errorf("ran = %q, want %q", f.ran, want)
	}
}

func TestNotifyWithoutNotifySendDoesNothing(t *testing.T) {
	f := waylandDesktop()
	if err := f.desktop().notify(context.Background(), notifyNormal, "Imported host image", "for /work/project"); err != nil {
		t.Errorf("notify without notify-send = %v, want nil", err)
	}
	if len(f.ran) != 0 {
		t.Errorf("ran = %q, want nothing", f.ran)
	}
}
