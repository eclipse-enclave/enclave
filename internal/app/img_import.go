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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

// imgImportTimeout bounds the clipboard-capture path so a stuck, non-interactive
// helper (wl-paste/xclip) cannot hang the command indefinitely.
const imgImportTimeout = 60 * time.Second

// imgScreenshotTimeout is the (more generous) ceiling for the --screenshot path,
// which runs an interactive region selector (slurp/maim --select) the user drags
// by hand. It still bounds a selector the user never completes.
const imgScreenshotTimeout = 10 * time.Minute

// typeListCap bounds the clipboard type-listing output; it is metadata, not the
// image itself, so a small cap is plenty.
const typeListCap = 64 << 10

func runImgImport(input *CommandInput) int {
	opts := input.Options

	home, err := config.ResolveHostHome()
	if err != nil {
		logx.Errorf("Failed to resolve home directory: %v", err)
		return 1
	}
	inboxDir := config.HostImageInboxDir(home)

	maxBytes := int64(model.DefaultImageInboxMaxBytes)

	// The screenshot path is interactive (the user drags a region selection), so
	// it gets a more generous timeout than the fast clipboard read.
	timeout := imgImportTimeout
	if opts.ImgScreenshot {
		timeout = imgScreenshotTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	data, mime, err := acquireImage(ctx, opts.ImgScreenshot, maxBytes)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	ext, err := validateImage(data, mime, maxBytes)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	name, err := newInboxFilename(ext)
	if err != nil {
		logx.Errorf("Failed to generate image name: %v", err)
		return 1
	}
	if err := writeInboxImage(inboxDir, name, data); err != nil {
		logx.Errorf("Failed to write image: %v", err)
		return 1
	}

	containerPath := model.ContainerImageInboxDir + "/" + name
	// Print the bare container path to stdout as the machine-readable result
	// (the UI's Import button parses this); the status lines are cosmetic.
	fmt.Println(containerPath)
	logx.Successf("Imported image to the host inbox: %s", containerPath)
	if !hasRunningInboxSession(opts) {
		logx.Warnf("No --image-inbox session is running yet; start one with `%s run --image-inbox` and it will see this image.", model.AppName)
	}
	if !opts.ImgNoCopy {
		if err := copyTextToClipboard(ctx, containerPath); err != nil {
			logx.Warnf("Could not copy path to clipboard: %v", err)
		} else {
			logx.Infof("Path copied to clipboard; paste it into the session (Ctrl+Shift+V in most terminals)")
		}
	}
	return 0
}

// hasRunningInboxSession best-effort reports whether at least one --image-inbox
// session is currently running, so import can warn when the image would not yet
// be visible to any agent. Errors (e.g. Docker unavailable) are not treated as
// significant — the import itself only touches the host filesystem — so we
// suppress the warning rather than mislead.
func hasRunningInboxSession(opts model.Options) bool {
	be, err := newListingBackend(opts)
	if err != nil {
		return true
	}
	sessions, err := be.List(context.Background(), backend.SessionFilter{RunningOnly: true})
	if err != nil {
		return true
	}
	for _, s := range sessions {
		if s.ImageInbox {
			return true
		}
	}
	return false
}

// acquireImage returns raw image bytes and the advertised MIME type (empty when
// the source cannot advertise one, e.g. screenshots).
func acquireImage(ctx context.Context, screenshot bool, maxBytes int64) ([]byte, string, error) {
	if screenshot {
		return acquireScreenshot(ctx, maxBytes)
	}
	return acquireClipboardImage(ctx, maxBytes)
}

func acquireClipboardImage(ctx context.Context, maxBytes int64) ([]byte, string, error) {
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		types, err := captureCommand(ctx, typeListCap, "wl-paste", "--list-types")
		if err != nil {
			return nil, "", wrapMissingTool(err, "wl-paste", "wl-clipboard")
		}
		mime, ok := chooseImageMIME(strings.Fields(string(types)))
		if !ok {
			return nil, "", errors.New("clipboard has no PNG/JPEG image; copy one and retry")
		}
		data, err := captureCommand(ctx, maxBytes, "wl-paste", "--no-newline", "--type", mime)
		if err != nil {
			return nil, "", wrapMissingTool(err, "wl-paste", "wl-clipboard")
		}
		return data, mime, nil
	case os.Getenv("DISPLAY") != "":
		types, err := captureCommand(ctx, typeListCap, "xclip", "-selection", "clipboard", "-target", "TARGETS", "-out")
		if err != nil {
			return nil, "", wrapMissingTool(err, "xclip", "xclip")
		}
		mime, ok := chooseImageMIME(strings.Fields(string(types)))
		if !ok {
			return nil, "", errors.New("clipboard has no PNG/JPEG image; copy one and retry")
		}
		data, err := captureCommand(ctx, maxBytes, "xclip", "-selection", "clipboard", "-target", mime, "-out")
		if err != nil {
			return nil, "", wrapMissingTool(err, "xclip", "xclip")
		}
		return data, mime, nil
	default:
		return nil, "", errors.New("no display detected: set WAYLAND_DISPLAY or DISPLAY, or use --screenshot")
	}
}

func acquireScreenshot(ctx context.Context, maxBytes int64) ([]byte, string, error) {
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		// grim writes the region selected by slurp to stdout.
		data, err := captureCommand(ctx, maxBytes, "sh", "-c", "grim -g \"$(slurp)\" -")
		if err != nil {
			return nil, "", fmt.Errorf("screenshot failed (needs grim and slurp): %w", err)
		}
		return data, "image/png", nil
	case os.Getenv("DISPLAY") != "":
		data, err := captureCommand(ctx, maxBytes, "maim", "--select")
		if err != nil {
			return nil, "", wrapMissingTool(err, "maim", "maim")
		}
		return data, "image/png", nil
	default:
		return nil, "", errors.New("no display detected for --screenshot; use your desktop screenshot tool with \"copy to clipboard\", then run import without --screenshot")
	}
}

// chooseImageMIME picks the preferred supported image MIME from a clipboard's
// advertised target list, preferring PNG over JPEG.
func chooseImageMIME(types []string) (string, bool) {
	hasJPEG := false
	for _, t := range types {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "image/png":
			return "image/png", true
		case "image/jpeg", "image/jpg":
			hasJPEG = true
		}
	}
	if hasJPEG {
		return "image/jpeg", true
	}
	return "", false
}

// captureCommand runs an external command and returns its stdout, capping the
// captured bytes at maxBytes+1 (so the caller can detect an over-cap payload)
// and always draining the remainder so the child can exit.
func captureCommand(ctx context.Context, maxBytes int64, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- name and the shell snippet are fixed literals from callers (wl-paste, xclip, maim, sh); the only non-literal arg is a MIME type selected from chooseImageMIME's supported allowlist (image/png, image/jpeg), never raw user input.
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
	over := int64(len(data)) > maxBytes
	// Drain any remaining output (constant memory) so cmd.Wait does not block on
	// a full pipe when the payload exceeds the cap.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("read %s output: %w", name, readErr)
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s failed: %s", name, msg)
		}
		return nil, fmt.Errorf("%s failed: %w", name, waitErr)
	}
	if over {
		return nil, fmt.Errorf("output exceeds the %d byte cap", maxBytes)
	}
	return data, nil
}

func wrapMissingTool(err error, tool string, pkg string) error {
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
		return fmt.Errorf("%s not found; install it (package %q) to import images", tool, pkg)
	}
	return err
}

// validateImage enforces the size cap, verifies the magic bytes identify a
// supported image, checks the sniffed type agrees with any advertised MIME, and
// returns the file extension to use.
func validateImage(data []byte, advertisedMIME string, maxBytes int64) (string, error) {
	if len(data) == 0 {
		return "", errors.New("no image data received")
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("image is %d bytes, exceeds the %d byte cap", len(data), maxBytes)
	}
	mime, ext, ok := sniffImageType(data)
	if !ok {
		return "", errors.New("data is not a PNG or JPEG image")
	}
	if advertisedMIME != "" && !mimeMatches(advertisedMIME, mime) {
		return "", fmt.Errorf("advertised type %q does not match detected type %q", advertisedMIME, mime)
	}
	return ext, nil
}

// sniffImageType identifies PNG/JPEG from the leading magic bytes.
func sniffImageType(data []byte) (mime string, ext string, ok bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png", "png", true
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg", "jpg", true
	default:
		return "", "", false
	}
}

// mimeMatches compares an advertised clipboard MIME with a sniffed one,
// normalizing the image/jpg alias to image/jpeg and ignoring parameters.
func mimeMatches(advertised string, detected string) bool {
	return normalizeMIME(advertised) == normalizeMIME(detected)
}

func normalizeMIME(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	if m == "image/jpg" {
		return "image/jpeg"
	}
	return m
}

// newInboxFilename builds a collision-resistant filename "<utcTs>-<8hex>.<ext>".
func newInboxFilename(ext string) (string, error) {
	rnd := make([]byte, 4)
	if _, err := rand.Read(rnd); err != nil {
		return "", err
	}
	return inboxFilename(time.Now().UTC(), rnd, ext), nil
}

func inboxFilename(ts time.Time, rnd []byte, ext string) string {
	return fmt.Sprintf("%s-%s.%s", ts.Format("20060102T150405Z"), hex.EncodeToString(rnd), ext)
}

// writeInboxImage atomically writes data to dir/name at mode 0600: it creates an
// exclusive temp file, fsyncs it, then renames it into place.
func writeInboxImage(dir string, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return util.WriteFileAtomic(filepath.Join(dir, name), data, 0o600)
}

// copyTextToClipboard places text on the host clipboard so the user's next
// paste in the agent yields the imported image's container path.
func copyTextToClipboard(ctx context.Context, text string) error {
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		return runWithStdin(ctx, text, "wl-copy")
	case os.Getenv("DISPLAY") != "":
		return runWithStdin(ctx, text, "xclip", "-selection", "clipboard", "-in")
	default:
		return errors.New("no display detected")
	}
}

func runWithStdin(ctx context.Context, stdin string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- name and args are fixed literals from callers (wl-copy, xclip), never user input.
	cmd.Stdin = strings.NewReader(stdin)
	// Do NOT attach pipe-backed stdout/stderr here. Clipboard-write tools
	// (`xclip -i`, `wl-copy`) fork and keep running to serve the selection; a
	// daemonized child would inherit the pipe's write end and make cmd.Wait block
	// forever waiting for an EOF that never comes. Leaving Stdout/Stderr nil
	// routes them to the null device (no pipe, no goroutine), so Wait returns as
	// soon as the foreground process forks away. WaitDelay bounds any lingering
	// inherited I/O as a backstop. We lose the tool's stderr text, but a failed
	// path-to-clipboard copy is non-fatal (the caller only warns).
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
