// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/backend/vmhost"
	"enclave/internal/model"
	"enclave/internal/util"
)

const (
	// Files the guest writes into the out export.
	guestBootFile     = "boot"
	guestStartedFile  = "started"
	guestExitCodeFile = "exit-code"

	guestStatusLimit = 4 << 10
	// fileMountLimit bounds what the host copies back for one file mount.
	fileMountLimit = 64 << 20

	defaultEntrypoint = "/usr/local/bin/entrypoint.sh"
)

// writeControlIn fills the in export: the scripts the guest runs, what it
// mounts, and how the session is launched. The scripts travel with every boot
// so that a disk created by an older enclave still speaks this host's
// protocol; the base image only knows how to find them.
func writeControlIn(rt runtimeFiles, assetsDir string, shares []share, files []vmhost.FileMount, req backend.Request, user string) error {
	for _, script := range []string{"boot", "session"} {
		data, err := os.ReadFile(filepath.Join(assetsDir, script)) // #nosec G304 -- script ships with enclave under the app root.
		if err != nil {
			return fmt.Errorf("vm backend: read guest script: %w", err)
		}
		if err := os.WriteFile(filepath.Join(rt.in(), script), data, 0o600); err != nil { // #nosec G703 -- a fixed name inside the session runtime directory.
			return err
		}
	}
	mounts, err := mountsManifest(shares)
	if err != nil {
		return err
	}
	fileMounts, err := filesManifest(files)
	if err != nil {
		return err
	}
	authorized, err := os.ReadFile(rt.clientKey() + ".pub")
	if err != nil {
		return fmt.Errorf("vm backend: read session public key: %w", err)
	}
	entries := map[string]string{
		"mounts.tsv":      mounts,
		"files.tsv":       fileMounts,
		"authorized_keys": string(authorized),
		"hostname":        req.Hostname + "\n",
	}
	for name, content := range entries {
		if err := os.WriteFile(filepath.Join(rt.in(), name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	return writePayload(rt, req, user)
}

// writePayload stores how to launch the tool. The environment can carry
// secrets, so it never appears on a command line or in the guest's
// persistent disk: the guest reads these files once and the host removes them
// as soon as the session has started.
func writePayload(rt runtimeFiles, req backend.Request, user string) error {
	if len(req.Argv) == 0 {
		return fmt.Errorf("vm backend: command argv is empty")
	}
	argv := append([]string(nil), req.Entrypoint...)
	if len(argv) == 0 {
		argv = []string{defaultEntrypoint}
	}
	argv = append(argv, req.Argv...)
	env := make([]string, 0, len(req.Env))
	for _, entry := range req.Env {
		if entry.Name == "" {
			continue
		}
		env = append(env, entry.Name+"="+entry.Value)
	}
	for _, value := range append(append([]string(nil), argv...), env...) {
		if strings.Contains(value, "\x00") {
			return fmt.Errorf("vm backend: command or environment contains a NUL byte")
		}
	}
	if err := rejectManifestField("working directory", req.WorkingDir); err != nil {
		return err
	}
	if err := os.Mkdir(rt.payload(), 0o700); err != nil {
		return err
	}
	entries := map[string]string{
		"argv":           nulJoined(argv),
		"env":            nulJoined(env),
		"workdir":        req.WorkingDir + "\n",
		"user":           user + "\n",
		"loopback-ports": strings.Join(req.Network.LoopbackPorts, "\n") + "\n",
	}
	for name, content := range entries {
		if err := os.WriteFile(filepath.Join(rt.payload(), name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// nulJoined terminates each value with NUL, the one byte neither an argument
// nor an environment entry can contain.
func nulJoined(values []string) string {
	var out strings.Builder
	for _, value := range values {
		out.WriteString(value)
		out.WriteByte(0)
	}
	return out.String()
}

// guestUser maps the request's user to the account the launcher runs the
// tool as.
func guestUser(req backend.Request) (string, error) {
	switch strings.TrimSpace(req.User) {
	case "", model.ContainerUser:
		if req.Security.Admin {
			return "root", nil
		}
		return model.ContainerUser, nil
	case "root":
		return "root", nil
	default:
		return "", fmt.Errorf("vm backend: only default, root, or %s user execution is supported", model.ContainerUser)
	}
}

// readGuestFile reads a file the guest wrote into an export. The guest
// controls that directory, so the read stays inside it, follows no link,
// cannot block on a FIFO, accepts only what turns out to be a regular file
// once opened, and stops at limit. ok is false while the file is missing.
func readGuestFile(dir string, name string, limit int64) (content string, ok bool, err error) {
	root, err := hoststore.OpenFiles(dir)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFileLimit(name, limit)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("vm backend: read guest file %s: %w", name, err)
	}
	return string(data), true, nil
}

// readGuestExitCode returns the tool's exit status as the guest launcher
// reported it.
func readGuestExitCode(rt runtimeDir) (int, bool, error) {
	text, ok, err := rt.readOut(guestExitCodeFile, guestStatusLimit)
	if err != nil || !ok {
		return 0, false, err
	}
	code, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || code < 0 || code > 255 {
		return 0, false, fmt.Errorf("vm backend: guest reported an invalid exit code %q", strings.TrimSpace(text))
	}
	return code, true, nil
}

// fileRecord is a writable file mount with what the host knows about its
// source when the session starts. Write-back is allowed only onto that very
// file in that very state.
type fileRecord struct {
	Target string `json:"target"`
	// ResultPath is the staged file the guest writes through.
	ResultPath string `json:"resultPath"`
	// Path is the source with its links resolved, so that replacing the file
	// does not replace a link to it.
	Path string `json:"path"`
	ID   string `json:"id"`
	Sum  string `json:"sum"`
	Mode uint32 `json:"mode"`
}

func contentSum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// recordFileMounts captures the origin of every writable file mount, before
// the guest can touch its staged copy.
func recordFileMounts(files []vmhost.FileMount) ([]fileRecord, error) {
	var records []fileRecord
	for _, file := range files {
		if file.ReadOnly {
			continue
		}
		path, err := filepath.EvalSymlinks(file.Source)
		if err != nil {
			return nil, fmt.Errorf("vm backend: resolve file mount source %q: %w", file.Source, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("vm backend: inspect file mount source %q: %w", file.Source, err)
		}
		staged, err := os.ReadFile(file.ResultPath) // #nosec G304 -- the copy this process staged in its runtime directory.
		if err != nil {
			return nil, fmt.Errorf("vm backend: read staged file mount: %w", err)
		}
		records = append(records, fileRecord{
			Target:     file.Target,
			ResultPath: file.ResultPath,
			Path:       path,
			ID:         fileID(info),
			Sum:        contentSum(staged),
			Mode:       uint32(info.Mode().Perm()),
		})
	}
	return records, nil
}

// persistFileMounts writes the guest's final content of each writable file
// mount back to its host source. The source path is not trusted to still
// name that file: it may lie in a directory the guest can write, and the
// user may have edited it meanwhile. So nothing is written unless the guest
// changed the content, the path still opens the file recorded at start, and
// that file still has the content the guest was given. The new content then
// replaces the file in one rename, so a write that fails leaves the old one.
func persistFileMounts(rt runtimeDir, files []fileRecord) []error {
	var errs []error
	for _, file := range files {
		if err := persistFileMount(rt, file); err != nil {
			errs = append(errs, fmt.Errorf("write back file mount %s: %w", file.Target, err))
		}
	}
	return errs
}

func persistFileMount(rt runtimeDir, file fileRecord) error {
	content, ok, err := rt.readResult(file.ResultPath)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("the guest removed its copy")
	}
	if contentSum([]byte(content)) == file.Sum {
		return nil
	}
	if err := checkFileOrigin(file); err != nil {
		return err
	}
	return util.WriteFileAtomic(file.Path, []byte(content), fs.FileMode(file.Mode))
}

// checkFileOrigin reports whether the path of a file mount's source still
// names the file recorded at session start, with its content of then.
func checkFileOrigin(file fileRecord) error {
	current, err := os.OpenFile(file.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- the opened file is checked against the mount's recorded source below.
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	info, err := current.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || file.ID == "" || fileID(info) != file.ID {
		return fmt.Errorf("%s is no longer the file that was mounted; the guest's changes are not written", file.Path)
	}
	data, err := io.ReadAll(io.LimitReader(current, fileMountLimit+1))
	if err != nil {
		return err
	}
	if contentSum(data) != file.Sum {
		return fmt.Errorf("%s changed on the host during the session; the guest's changes are not written over it", file.Path)
	}
	return nil
}
