// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// runtimeDir names the directory that holds what exists only while a VM
// runs: sockets, the session's SSH identities and the control exports.
//
// It gives out no path. A session record names the directory for later
// commands, and the directory can be in the temp directory, where a reboot
// or a temp cleaner removes it and someone else can then put their own under
// the same path. So everything in it is reached through open, which fails
// unless the directory is still a private directory of this user. This file
// is the only one that reads the two fields.
type runtimeDir struct {
	location string
	// socketLocation is set when location is too deep for a unix socket
	// address; the sockets then have a directory of their own.
	socketLocation string
}

// runtimeFiles are the paths in a runtime directory that open found to be the
// session's own. They are for immediate use, not for keeping: a later use
// opens the directory again.
type runtimeFiles struct {
	dir     string
	sockets string
}

// socketPathLimit keeps every socket of a runtime directory inside what a
// unix socket address holds (108 bytes on Linux, with the terminator).
const socketPathLimit = 100

// newRuntimeDir creates the runtime directory of a VM under parent. Without a
// parent it lives in the user's own runtime directory, which is private and
// on a tmpfs, or in the system temp directory when there is none.
func newRuntimeDir(parent string) (runtimeDir, error) {
	if parent == "" {
		parent = os.Getenv("XDG_RUNTIME_DIR")
		if !filepath.IsAbs(parent) {
			parent = ""
		}
	} else if err := os.MkdirAll(parent, 0o700); err != nil {
		return runtimeDir{}, fmt.Errorf("vm backend: create runtime directory: %w", err)
	}
	dir, err := os.MkdirTemp(parent, "enclave-vm-")
	if err != nil && parent != "" {
		dir, err = os.MkdirTemp("", "enclave-vm-")
	}
	if err != nil {
		return runtimeDir{}, fmt.Errorf("vm backend: create runtime directory: %w", err)
	}
	// The session record names the directory for commands that run elsewhere.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	rt := runtimeDir{location: dir}
	if !(runtimeFiles{dir: dir}).socketsFit() {
		if rt.socketLocation, err = newSocketDir(dir); err != nil {
			rt.remove()
			return runtimeDir{}, fmt.Errorf("vm backend: the runtime directory %s is too deep for a unix socket address: %w", dir, err)
		}
	}
	files, err := rt.open()
	if err != nil {
		return runtimeDir{}, err
	}
	for _, sub := range []string{files.in(), files.out()} {
		if err := os.Mkdir(sub, 0o700); err != nil {
			rt.remove()
			return runtimeDir{}, fmt.Errorf("vm backend: create control export: %w", err)
		}
	}
	return rt, nil
}

// socketTempDirs are the temp directories tried, in this order, for a socket
// directory.
var socketTempDirs = func() []string { return []string{os.TempDir(), "/tmp"} }

// newSocketDir creates the directory for the sockets of the VM whose runtime
// directory is too deep for them. It is not in the login session's runtime
// directory: that one goes at logout, and a detached VM does not.
//
// A temp directory is shared with other users and cleaned by the system, so
// the directory is one of a parent that only this user can enter, and named
// after the runtime directory. As long as the parent is this user's own,
// nobody else can have put anything there, and no other session has the name.
func newSocketDir(runtime string) (string, error) {
	sum := sha256.Sum256([]byte(runtime))
	name := hex.EncodeToString(sum[:8])
	var problems []error
	for _, tmp := range socketTempDirs() {
		// A relative directory would mean another one to a later command.
		if !filepath.IsAbs(tmp) {
			continue
		}
		parent := filepath.Join(tmp, "enclave-vm-sockets-"+strconv.Itoa(os.Getuid()))
		dir := filepath.Join(parent, name)
		if !(runtimeFiles{dir: dir}).socketsFit() {
			problems = append(problems, fmt.Errorf("%s is too deep as well", tmp))
			continue
		}
		if err := os.Mkdir(parent, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			problems = append(problems, err)
			continue
		}
		if !ownedPrivateDir(parent) {
			problems = append(problems, fmt.Errorf("%s is not a private directory of this user; if it is yours, remove it or set its mode to 0700", parent))
			continue
		}
		// What has the name is left from a session whose runtime directory
		// is gone, or this path would not have been free for a new one.
		_ = os.RemoveAll(dir)                        // #nosec G703 -- a hash name inside a directory only this user can enter.
		if err := os.Mkdir(dir, 0o700); err != nil { // #nosec G703 -- as above.
			problems = append(problems, err)
			continue
		}
		return dir, nil
	}
	return "", fmt.Errorf("no shorter directory for its sockets: %w", errors.Join(problems...))
}

// runtimeDirAt is the runtime directory a session record names.
func runtimeDirAt(dir string, sockets string) runtimeDir {
	return runtimeDir{location: dir, socketLocation: sockets}
}

// name and socketsName are what a session record keeps, and what a message
// calls the directory. They are names, not paths to use.
func (r runtimeDir) name() string        { return r.location }
func (r runtimeDir) socketsName() string { return r.socketLocation }

// lost is the error for a runtime directory that is not the session's own.
func (r runtimeDir) lost() error {
	return fmt.Errorf("vm backend: the runtime directory %s is gone or no longer this session's", r.location)
}

// open gives the paths in the runtime directory if it is still a directory,
// not a link to one, that belongs to this user and that nobody else can
// enter. Only this user can have put such a directory there. Nothing is read
// from, connected to or removed in a runtime directory other than through
// what open returns.
func (r runtimeDir) open() (runtimeFiles, error) {
	if r.location == "" || !ownedPrivateDir(r.location) {
		return runtimeFiles{}, r.lost()
	}
	return runtimeFiles{dir: r.location, sockets: r.socketLocation}, nil
}

// dialMonitor connects to the monitor of the process that started the VM,
// and dialControl to the one other commands use. A socket is dialled only
// where both checks hold, for the runtime directory and for the sockets: who
// answers on a socket in someone else's directory is not the session's VM.
func (r runtimeDir) dialMonitor(ctx context.Context) (*qmpClient, error) {
	return r.dial(ctx, runtimeFiles.qmp)
}

func (r runtimeDir) dialControl(ctx context.Context) (*qmpClient, error) {
	return r.dial(ctx, runtimeFiles.control)
}

func (r runtimeDir) dial(ctx context.Context, socket func(runtimeFiles) string) (*qmpClient, error) {
	files, err := r.open()
	if err != nil {
		return nil, err
	}
	if !files.socketsUsable() {
		return nil, fmt.Errorf("vm backend: the socket directory %s is gone or no longer this session's", r.socketLocation)
	}
	return dialQMP(ctx, socket(files))
}

// readOut reads a file the guest wrote into the out export.
func (r runtimeDir) readOut(name string, limit int64) (content string, ok bool, err error) {
	files, err := r.open()
	if err != nil {
		return "", false, err
	}
	return readGuestFile(files.out(), name, limit)
}

// readResult reads the guest's version of a file mount, which a session
// record names by its path in the out export.
func (r runtimeDir) readResult(path string) (content string, ok bool, err error) {
	files, err := r.open()
	if err != nil {
		return "", false, err
	}
	rel, err := filepath.Rel(files.out(), path)
	if err != nil {
		return "", false, err
	}
	return readGuestFile(files.out(), rel, fileMountLimit)
}

// removePayload removes the launch payload, the command and environment of
// the tool, once the guest has taken it.
func (r runtimeDir) removePayload() error {
	files, err := r.open()
	if err != nil {
		return err
	}
	return os.RemoveAll(files.payload()) // #nosec G703 -- a fixed name inside the session runtime directory.
}

// consoleTail returns the end of the guest's console log for an error
// message.
func (r runtimeDir) consoleTail(lines int) string {
	files, err := r.open()
	if err != nil {
		return ""
	}
	return tailFile(files.console(), lines)
}

// remove removes the runtime directory and the socket directory, each only
// if it is still the session's own.
func (r runtimeDir) remove() {
	if files, err := r.open(); err == nil {
		_ = os.RemoveAll(files.dir) // #nosec G703 -- the directory newRuntimeDir created with MkdirTemp, still this user's own.
	}
	sockets := runtimeFiles{dir: r.location, sockets: r.socketLocation}
	if r.socketLocation != "" && sockets.socketsUsable() {
		_ = os.RemoveAll(r.socketLocation) // #nosec G703 -- the directory newSocketDir created, still this user's own.
	}
}

// socketsUsable reports whether the sockets can be connected to. Where they
// have a directory of their own, it is under the temp directory and can be
// lost while the runtime directory is not: that one and its parent have to
// be this user's own.
func (f runtimeFiles) socketsUsable() bool {
	if f.sockets == "" {
		return true
	}
	return ownedPrivateDir(filepath.Dir(f.sockets)) && ownedPrivateDir(f.sockets)
}

func (f runtimeFiles) socketsFit() bool { return len(f.socket(999)) <= socketPathLimit }

func (f runtimeFiles) socketDir() string {
	if f.sockets != "" {
		return f.sockets
	}
	return f.dir
}

func (f runtimeFiles) in() string         { return filepath.Join(f.dir, "in") }
func (f runtimeFiles) out() string        { return filepath.Join(f.dir, "out") }
func (f runtimeFiles) payload() string    { return filepath.Join(f.in(), "payload") }
func (f runtimeFiles) qmp() string        { return filepath.Join(f.socketDir(), "qmp.sock") }
func (f runtimeFiles) control() string    { return filepath.Join(f.socketDir(), "ctl.sock") }
func (f runtimeFiles) pidFile() string    { return filepath.Join(f.dir, "qemu.pid") }
func (f runtimeFiles) console() string    { return filepath.Join(f.dir, "console.log") }
func (f runtimeFiles) clientKey() string  { return filepath.Join(f.dir, "client_key") }
func (f runtimeFiles) knownHosts() string { return filepath.Join(f.dir, "known_hosts") }
func (f runtimeFiles) hostKey() string    { return filepath.Join(f.in(), "ssh_host_ed25519_key") }
func (f runtimeFiles) socket(i int) string {
	return filepath.Join(f.socketDir(), "fs"+strconv.Itoa(i)+".sock")
}

// exportPID is where the PID of the virtiofsd behind socket i is kept. It is
// in the runtime directory, not with the socket: a socket directory can be
// lost while the VM runs.
func (f runtimeFiles) exportPID(i int) string {
	return filepath.Join(f.dir, "fs"+strconv.Itoa(i)+".sock.pid")
}

func (f runtimeFiles) exportLog(i int) string {
	return filepath.Join(f.dir, "fs"+strconv.Itoa(i)+".log")
}
