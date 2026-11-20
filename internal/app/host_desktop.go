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
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"enclave/internal/model"
)

var (
	errCaptureUnsupported = errors.New("clipboard and screenshot import are not supported")
	errCaptureToolMissing = errors.New("missing capture tool")
	errClipboardEmpty     = errors.New("the clipboard is empty; copy an image and retry")
	errClipboardNoImage   = errors.New("the clipboard holds no PNG or JPEG image")
	errSelectionCancelled = errors.New("screen selection cancelled")
)

// toolMetadataCap bounds tool output that is metadata (a clipboard type
// listing, a region geometry), not the host image.
const toolMetadataCap = 64 << 10

type displayServer int

const (
	displayWayland displayServer = iota + 1
	displayX11
)

type desktopTool struct {
	name    string
	pkg     string
	purpose string
}

var (
	toolWlPaste = desktopTool{"wl-paste", "wl-clipboard", "read the clipboard"}
	toolWlCopy  = desktopTool{"wl-copy", "wl-clipboard", "write the clipboard"}
	toolXclip   = desktopTool{"xclip", "xclip", "use the clipboard"}
	toolGrim    = desktopTool{"grim", "grim", "take screenshots"}
	toolSlurp   = desktopTool{"slurp", "slurp", "select a screen region"}
	toolMaim    = desktopTool{"maim", "maim", "take screenshots"}
	// toolNotifySend is optional: without it there are no notifications.
	toolNotifySend = desktopTool{"notify-send", "libnotify-bin", "show notifications"}
)

type notifyUrgency string

const (
	notifyNormal   notifyUrgency = "normal"
	notifyCritical notifyUrgency = "critical"
)

// hostDesktop is img import's seam to the host's display server and its
// clipboard and screenshot tools.
type hostDesktop struct {
	goos string
	// kernelRelease returns the running kernel's release string.
	kernelRelease func() string
	getenv        func(string) string
	lookPath      func(string) (string, error)
	// runCapture runs a tool and returns at most maxBytes+1 bytes of its stdout.
	// A non-zero exit is reported as a *toolExitError.
	runCapture func(ctx context.Context, maxBytes int64, name string, args ...string) ([]byte, error)
	// runWithStdin runs a tool with the given stdin.
	runWithStdin func(ctx context.Context, stdin string, name string, args ...string) error
}

func systemDesktop() hostDesktop {
	return hostDesktop{
		goos:          runtime.GOOS,
		kernelRelease: readKernelRelease,
		getenv:        os.Getenv,
		lookPath:      exec.LookPath,
		runCapture:    runToolOutput,
		runWithStdin:  runToolInput,
	}
}

// toolExitError is a capture tool that ran and exited non-zero.
type toolExitError struct {
	tool   string
	stderr string
	err    error
}

func (e *toolExitError) Error() string {
	if e.stderr != "" {
		return fmt.Sprintf("%s failed: %s", e.tool, e.stderr)
	}
	return fmt.Sprintf("%s failed: %v", e.tool, e.err)
}

func (e *toolExitError) Unwrap() error { return e.err }

// display returns the display server whose tools capture host images, or why
// there is none.
func (d hostDesktop) display() (displayServer, error) {
	if d.goos != "linux" {
		return 0, fmt.Errorf("%w on %s; use --file <path> or - instead", errCaptureUnsupported, platformName(d.goos))
	}
	if d.isWSL() {
		return 0, fmt.Errorf("%w on WSL; use --file <path> or - instead", errCaptureUnsupported)
	}
	switch {
	case d.getenv("WAYLAND_DISPLAY") != "":
		return displayWayland, nil
	case d.getenv("DISPLAY") != "":
		return displayX11, nil
	}
	return 0, fmt.Errorf("%w without a display (neither WAYLAND_DISPLAY nor DISPLAY is set); use --file <path> or - instead", errCaptureUnsupported)
}

// isWSL also checks the kernel release because WSL_DISTRO_NAME and
// WSL_INTEROP are lost under sudo or env -i, while WSLg still sets
// WAYLAND_DISPLAY and DISPLAY.
func (d hostDesktop) isWSL() bool {
	if d.getenv("WSL_DISTRO_NAME") != "" || d.getenv("WSL_INTEROP") != "" {
		return true
	}
	return strings.Contains(strings.ToLower(d.kernelRelease()), "microsoft")
}

func readKernelRelease() string {
	release, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return string(release)
}

func platformName(goos string) string {
	if goos == "darwin" {
		return "macOS"
	}
	return goos
}

func (d hostDesktop) requireTools(tools ...desktopTool) error {
	var missing []string
	for _, tool := range tools {
		if _, err := d.lookPath(tool.name); err != nil {
			missing = append(missing, fmt.Sprintf("%s (package %s) to %s", tool.name, tool.pkg, tool.purpose))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: install %s", errCaptureToolMissing, strings.Join(missing, ", and "))
}

func (d hostDesktop) screenshotTools() (displayServer, []desktopTool, error) {
	server, err := d.display()
	if err != nil {
		return 0, nil, err
	}
	if server == displayWayland {
		return server, []desktopTool{toolGrim, toolSlurp}, nil
	}
	return server, []desktopTool{toolMaim}, nil
}

// checkScreenshot reports, without running anything, why a screenshot cannot
// be taken.
func (d hostDesktop) checkScreenshot() error {
	_, tools, err := d.screenshotTools()
	if err != nil {
		return err
	}
	return d.requireTools(tools...)
}

// captureClipboard returns the host image on the clipboard and the MIME type
// the clipboard advertises for it.
func (d hostDesktop) captureClipboard(ctx context.Context) ([]byte, string, error) {
	server, err := d.display()
	if err != nil {
		return nil, "", err
	}
	tool, listArgs, readArgs := toolWlPaste, []string{"--list-types"}, func(mime string) []string {
		return []string{"--no-newline", "--type", mime}
	}
	if server == displayX11 {
		tool, listArgs, readArgs = toolXclip, []string{"-selection", "clipboard", "-target", "TARGETS", "-out"}, func(mime string) []string {
			return []string{"-selection", "clipboard", "-target", mime, "-out"}
		}
	}
	if err := d.requireTools(tool); err != nil {
		return nil, "", err
	}
	listing, err := d.runCapture(ctx, toolMetadataCap, tool.name, listArgs...)
	if err != nil {
		if isEmptyClipboard(err) {
			return nil, "", errClipboardEmpty
		}
		return nil, "", err
	}
	types := strings.Fields(string(listing))
	if len(types) == 0 {
		return nil, "", errClipboardEmpty
	}
	mime, ok := chooseImageMIME(types)
	if !ok {
		return nil, "", fmt.Errorf("%w (it offers %s); copy an image and retry", errClipboardNoImage, strings.Join(types, ", "))
	}
	data, err := d.runCapture(ctx, model.ImageInboxMaxBytes, tool.name, readArgs(mime)...)
	if err != nil {
		return nil, "", err
	}
	return data, mime, nil
}

// captureScreenshot lets the user select a screen region and returns it as a
// PNG.
func (d hostDesktop) captureScreenshot(ctx context.Context) ([]byte, string, error) {
	server, tools, err := d.screenshotTools()
	if err != nil {
		return nil, "", err
	}
	if err := d.requireTools(tools...); err != nil {
		return nil, "", err
	}
	if server == displayX11 {
		data, err := d.runCapture(ctx, model.ImageInboxMaxBytes, toolMaim.name, "--select", "--format", "png")
		if err != nil {
			return nil, "", classifySelectionError(err)
		}
		return data, "image/png", nil
	}
	geometry, err := d.runCapture(ctx, toolMetadataCap, toolSlurp.name)
	if err != nil {
		return nil, "", classifySelectionError(err)
	}
	region := strings.TrimSpace(string(geometry))
	if region == "" {
		return nil, "", errSelectionCancelled
	}
	data, err := d.runCapture(ctx, model.ImageInboxMaxBytes, toolGrim.name, "-g", region, "-")
	if err != nil {
		return nil, "", err
	}
	return data, "image/png", nil
}

// copyText puts text on the host clipboard.
func (d hostDesktop) copyText(ctx context.Context, text string) error {
	server, err := d.display()
	if err != nil {
		return err
	}
	if server == displayWayland {
		if err := d.requireTools(toolWlCopy); err != nil {
			return err
		}
		return d.runWithStdin(ctx, text, toolWlCopy.name)
	}
	if err := d.requireTools(toolXclip); err != nil {
		return err
	}
	return d.runWithStdin(ctx, text, toolXclip.name, "-selection", "clipboard", "-in")
}

// notify shows a desktop notification. Without notify-send it does nothing.
func (d hostDesktop) notify(ctx context.Context, urgency notifyUrgency, summary string, body string) error {
	if _, err := d.lookPath(toolNotifySend.name); err != nil {
		return nil
	}
	return d.runWithStdin(ctx, "", toolNotifySend.name,
		"--app-name="+model.AppName, "--urgency="+string(urgency), "--", summary, escapeNotificationMarkup(body))
}

// escapeNotificationMarkup keeps a body such as "--session <target>" intact on
// notification servers that render body markup.
func escapeNotificationMarkup(body string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(body)
}

// isEmptyClipboard recognizes how wl-paste ("No selection", "Nothing is
// copied") and xclip ("target TARGETS not available") report a clipboard
// nobody owns.
func isEmptyClipboard(err error) bool {
	var exitErr *toolExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	msg := strings.ToLower(exitErr.stderr)
	return strings.Contains(msg, "no selection") ||
		strings.Contains(msg, "nothing is copied") ||
		strings.Contains(msg, "not available")
}

// classifySelectionError recognizes a region selection the user aborted;
// slurp and maim both say "cancelled".
func classifySelectionError(err error) error {
	var exitErr *toolExitError
	if errors.As(err, &exitErr) && strings.Contains(strings.ToLower(exitErr.stderr), "cancel") {
		return errSelectionCancelled
	}
	return err
}

// chooseImageMIME picks the preferred supported image type from a
// clipboard's type listing, preferring PNG over JPEG.
func chooseImageMIME(types []string) (string, bool) {
	hasJPEG := false
	for _, t := range types {
		switch normalizeMIME(t) {
		case "image/png":
			return "image/png", true
		case "image/jpeg":
			hasJPEG = true
		}
	}
	if hasJPEG {
		return "image/jpeg", true
	}
	return "", false
}

// runToolOutput runs a capture tool, keeping at most maxBytes+1 bytes of its
// stdout so the caller can detect an over-cap payload, and draining the rest
// so the tool can exit.
func runToolOutput(ctx context.Context, maxBytes int64, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- name is a fixed capture tool; args are literals, a MIME type from chooseImageMIME's allowlist, or slurp's region geometry passed without a shell.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("read %s output: %w", name, readErr)
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: %w", name, ctx.Err())
		}
		return nil, &toolExitError{tool: name, stderr: strings.TrimSpace(stderr.String()), err: waitErr}
	}
	return data, nil
}

// runToolInput runs a tool with stdin and without capturing its output.
func runToolInput(ctx context.Context, stdin string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- name is a fixed tool (wl-copy, xclip, notify-send); notify-send's text arguments follow "--" and run without a shell.
	cmd.Stdin = strings.NewReader(stdin)
	// No pipe-backed stdout/stderr: wl-copy and xclip fork a child that keeps
	// serving the selection, and an inherited pipe would make Wait block until
	// that child exits. WaitDelay is a backstop for lingering inherited I/O.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
