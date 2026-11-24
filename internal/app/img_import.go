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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

// clipboardCaptureTimeout bounds a clipboard read, which is not interactive.
const clipboardCaptureTimeout = 60 * time.Second

// screenshotCaptureTimeout bounds a region selection the user never finishes.
const screenshotCaptureTimeout = 10 * time.Minute

// clipboardCopyTimeout bounds handing the container path to the clipboard.
const clipboardCopyTimeout = 10 * time.Second

type inboxTarget struct {
	projectHash  string
	projectLabel string
}

// notifyTimeout bounds showing a desktop notification.
const notifyTimeout = 5 * time.Second

func runImgImport(opts model.Options, projectDir string) int {
	desktop := systemDesktop()
	containerPath, target, err := importHostImage(opts, projectDir, desktop)
	if err != nil {
		reportImportFailure(desktop, err)
		return 1
	}
	reportImport(opts.ImgOptions, desktop, containerPath, target.projectLabel)
	return 0
}

// importHostImage captures, validates, and stores a host image in the target
// project's image inbox and returns the path its sessions see it at.
func importHostImage(opts model.Options, projectDir string, desktop hostDesktop) (string, *inboxTarget, error) {
	img := opts.ImgOptions

	// A screenshot is interactive: find out whether it can be taken and
	// where it goes before the user selects a region.
	var target *inboxTarget
	if img.ImgScreenshot {
		if err := desktop.checkScreenshot(); err != nil {
			return "", nil, err
		}
		var err error
		if target, err = resolveImportTarget(opts, projectDir); err != nil {
			return "", nil, err
		}
	}

	data, advertised, err := acquireHostImage(img, desktop)
	if err != nil {
		return "", nil, err
	}
	// Validated before any engine call: bad input is reported as such even
	// where no engine or session is available.
	ext, err := validateImage(data, advertised, model.ImageInboxMaxBytes)
	if err != nil {
		if img.ImgFile != "" {
			err = fmt.Errorf("%s: %w", img.ImgFile, err)
		}
		return "", nil, err
	}

	if target == nil {
		if target, err = resolveImportTarget(opts, projectDir); err != nil {
			return "", nil, err
		}
	}

	home, err := config.ResolveHostHome()
	if err != nil {
		return "", nil, fmt.Errorf("failed to resolve home directory: %w", err)
	}
	containerPath, err := writeHostImage(home, target.projectHash, data, ext)
	if err != nil {
		return "", nil, err
	}
	return containerPath, target, nil
}

// reportImport prints the bare container path on stdout, so that
// `P=$(enclave img import ...)` captures exactly the path; every status line
// goes to stderr. The desktop notification follows the clipboard copy so that
// it can report a failed one.
func reportImport(img model.ImgOptions, desktop hostDesktop, containerPath string, projectLabel string) {
	fmt.Println(containerPath)
	imported := "Imported host image for " + projectLabel
	logx.SuccessNoticef("%s", imported)
	body := containerPath
	if copiesPathToClipboard(img) {
		if err := copyPathToClipboard(desktop, containerPath); err != nil {
			logx.Warnf("Could not copy the container path to the clipboard: %v", err)
			body += "\nNot copied to the clipboard: " + err.Error()
		} else {
			logx.Noticef("Container path copied to the clipboard; paste it into the session")
		}
	}
	notifyImport(desktop, notifyNormal, imported, body)
}

func copyPathToClipboard(desktop hostDesktop, containerPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardCopyTimeout)
	defer cancel()
	return desktop.copyText(ctx, containerPath)
}

// reportImportFailure reports the error on stderr and as a desktop
// notification. A selection the user cancelled needs no notification.
func reportImportFailure(desktop hostDesktop, err error) {
	logx.Errorf("%v", err)
	if errors.Is(err, errSelectionCancelled) {
		return
	}
	notifyImport(desktop, notifyCritical, "Host image import failed", err.Error())
}

// notifyImport shows a desktop notification, so that an import run from a
// hotkey, with no terminal, has visible output. It is best-effort: stdout and
// stderr remain the canonical output.
func notifyImport(desktop hostDesktop, urgency notifyUrgency, summary string, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	if err := desktop.notify(ctx, urgency, summary, body); err != nil {
		logx.Debugf("desktop notification: %v", err)
	}
}

// resolveImportTarget finds the project whose image inbox receives the import.
func resolveImportTarget(opts model.Options, projectDir string) (*inboxTarget, error) {
	if err := checkDocker(); err != nil {
		return nil, err
	}
	be, err := newListingBackend(opts)
	if err != nil {
		return nil, err
	}
	sessions, err := be.List(context.Background(), backend.SessionFilter{RunningOnly: true})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	if home, err := config.ResolveHostHome(); err == nil {
		sweepImageInboxesQuietly(context.Background(), be, home, "")
	}
	projectHash, err := resolveInboxProject(sessions, sessionTargetProject(projectDir), opts.ImgSession)
	if err != nil {
		return nil, err
	}
	return &inboxTarget{projectHash: projectHash, projectLabel: inboxProjectLabel(sessions, projectHash)}, nil
}

// copiesPathToClipboard reports whether the container path replaces the
// captured host image on the clipboard, so the next paste in the session
// hands the agent the path. A file or stdin import leaves the clipboard alone.
func copiesPathToClipboard(img model.ImgOptions) bool {
	return !img.ImgNoClipboard && img.ImgFile == "" && !img.ImgStdin
}

// acquireHostImage reads the host image from the selected source and returns
// it with the type the source advertises, if any.
func acquireHostImage(img model.ImgOptions, desktop hostDesktop) ([]byte, string, error) {
	switch {
	case img.ImgScreenshot:
		ctx, cancel := context.WithTimeout(context.Background(), screenshotCaptureTimeout)
		defer cancel()
		return desktop.captureScreenshot(ctx)
	case img.ImgFile != "" || img.ImgStdin:
		return readImportSource(img)
	default:
		ctx, cancel := context.WithTimeout(context.Background(), clipboardCaptureTimeout)
		defer cancel()
		return desktop.captureClipboard(ctx)
	}
}

// readImportSource reads the host image from --file or stdin and returns it
// with the type the source advertises (from a file's extension; none for
// stdin).
func readImportSource(img model.ImgOptions) ([]byte, string, error) {
	if img.ImgStdin {
		data, err := readHostImage(os.Stdin, model.ImageInboxMaxBytes)
		if err != nil {
			return nil, "", fmt.Errorf("read stdin: %w", err)
		}
		return data, "", nil
	}
	f, err := os.Open(img.ImgFile)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	data, err := readHostImage(f, model.ImageInboxMaxBytes)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", img.ImgFile, err)
	}
	return data, advertisedMIMEForPath(img.ImgFile), nil
}

// readHostImage reads at most maxBytes, failing instead of truncating when
// the input is larger.
func readHostImage(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("input exceeds the %d byte cap", maxBytes)
	}
	return data, nil
}

// advertisedMIMEForPath maps a PNG or JPEG file extension to its MIME type, so
// that a file whose content contradicts its name is rejected. Other
// extensions advertise nothing and leave the decision to the content sniff.
func advertisedMIMEForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	default:
		return ""
	}
}

// resolveInboxProject picks the project an import goes to; the first match
// wins:
//
//  1. requested, resolved like the session argument of `attach`;
//  2. the working directory's project, if it has a running inbox-enabled
//     session;
//  3. the only project with running inbox-enabled sessions.
//
// Anything else is an error: a wrong guess would show a host image to another
// project's agent.
func resolveInboxProject(running []backend.Session, cwd model.Project, requested string) (string, error) {
	if requested != "" {
		return resolveRequestedInboxSession(running, cwd, requested)
	}
	byProject := map[string][]backend.Session{}
	for _, session := range running {
		if sessionHasImageInbox(session) && strings.TrimSpace(session.ProjectHash) != "" {
			byProject[session.ProjectHash] = append(byProject[session.ProjectHash], session)
		}
	}
	if hash := strings.TrimSpace(cwd.Hash); hash != "" && len(byProject[hash]) > 0 {
		return hash, nil
	}
	switch len(byProject) {
	case 0:
		return "", fmt.Errorf("no running session has an image inbox; start one with `%s run --image-inbox`", model.AppName)
	case 1:
		for hash := range byProject {
			return hash, nil
		}
	}
	candidates := make([]string, 0, len(byProject))
	for _, projectSessions := range byProject {
		names := make([]string, 0, len(projectSessions))
		for _, session := range projectSessions {
			names = append(names, inboxCandidateName(session))
		}
		sort.Strings(names)
		candidates = append(candidates, fmt.Sprintf("%s: %s", projectSessions[0].ProjectDir, strings.Join(names, ", ")))
	}
	sort.Strings(candidates)
	return "", fmt.Errorf("several projects have a running session with an image inbox; pass --session <target> or run the import from one of these project directories:\n  %s",
		strings.Join(candidates, "\n  "))
}

// resolveRequestedInboxSession resolves --session among all running sessions,
// exactly as `attach` would, and only then requires an image inbox. Resolving
// among inbox-enabled sessions alone would let a session name that `attach`
// finds in the working directory's project silently pick another project's
// session of the same name.
func resolveRequestedInboxSession(running []backend.Session, cwd model.Project, requested string) (string, error) {
	session, err := resolveNamedSession(running, requested, sessionTargetQuery{Project: cwd})
	if err != nil {
		return "", err
	}
	if !sessionHasImageInbox(session) || strings.TrimSpace(session.ProjectHash) == "" {
		return "", fmt.Errorf("session %q (%s) has no image inbox; restart it with `%s run --image-inbox`",
			requested, session.Ref.Name, model.AppName)
	}
	return session.ProjectHash, nil
}

// inboxCandidateName shows a session by its session name where it has one,
// with the container name that --session also accepts.
func inboxCandidateName(session backend.Session) string {
	if name := strings.TrimSpace(session.Name); name != "" {
		return fmt.Sprintf("%s (%s)", name, session.Ref.Name)
	}
	return session.Ref.Name
}

func sessionHasImageInbox(session backend.Session) bool {
	return session.ImageInbox
}

func inboxProjectLabel(sessions []backend.Session, projectHash string) string {
	for _, session := range sessions {
		if session.ProjectHash == projectHash && session.ProjectDir != "" {
			return session.ProjectDir
		}
	}
	return projectHash
}

// writeHostImage atomically writes a validated host image into the project's
// image inbox under a fresh random name, readable only by the user. It returns
// the path the project's sessions see it at.
func writeHostImage(home string, projectHash string, data []byte, ext string) (string, error) {
	name, err := newInboxFilename(ext)
	if err != nil {
		return "", fmt.Errorf("generate host image name: %w", err)
	}
	inboxDir, err := config.EnsureHostImageInboxDir(home, projectHash)
	if err != nil {
		return "", fmt.Errorf("create image inbox: %w", err)
	}
	if err := util.WriteFileAtomic(filepath.Join(inboxDir, name), data, 0o600); err != nil {
		return "", fmt.Errorf("write host image: %w", err)
	}
	return model.ContainerImageInboxDir + "/" + name, nil
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

// mimeMatches compares an advertised MIME with a sniffed one, normalizing the
// image/jpg alias to image/jpeg and ignoring parameters.
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

// newInboxFilename builds a collision-resistant name "<utcTs>-<8hex>.<ext>".
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
