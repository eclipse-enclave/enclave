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
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

type sshMigrationState uint8

const (
	sshMigrationNone sshMigrationState = iota
	sshMigrationPending
	sshMigrationCompleted
	sshMigrationConflict
	// sshMigrationEmptyLegacy is a conflict in which the legacy directory is
	// empty, so removing it loses nothing.
	sshMigrationEmptyLegacy
)

type sshRenameFunc func(oldPath string, newPath string) error

// errLegacySSHNotDirectory marks a legacy SSH path that is neither a directory
// nor a symbolic link to one. Migration never moves such a path.
var errLegacySSHNotDirectory = errors.New("cannot be migrated automatically")

func setupSSH() error {
	home, err := resolveWritableHome()
	if err != nil {
		return err
	}
	enclaveSSH, err := prepareSSHInitDirectory(home)
	if err != nil {
		return err
	}

	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	if _, err := os.Stat(knownHosts); err == nil {
		copyPath := filepath.Join(enclaveSSH, "known_hosts")
		if err := copyFile(knownHosts, copyPath, 0o600); err != nil {
			return err
		}
		// The mode above only applies to a new file.
		if err := os.Chmod(copyPath, 0o600); err != nil {
			return err
		}
		logx.Infof("Copied known_hosts from ~/.ssh")
	}

	keyPath := filepath.Join(enclaveSSH, "id_ed25519")
	pubPath := keyPath + ".pub"
	if _, err := os.Stat(keyPath); err == nil {
		logx.Infof("%s SSH key already exists", model.AppName)
	} else {
		logx.Infof("Generating dedicated SSH key for %s.", model.AppName)
		cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-C", fmt.Sprintf("%s@%s", model.AppName, hostName()), "-N", "") // #nosec G204 -- command and args are fixed, executed without shell.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		if err := os.Chmod(keyPath, 0o600); err != nil {
			return err
		}
		if err := os.Chmod(pubPath, 0o644); err != nil { // #nosec G302 -- public keys are intentionally readable.
			return err
		}
		logx.Infof("Generated new SSH key: %s", keyPath)
		// #nosec G304 -- pubPath is derived from a trusted keyPath under the resolved home directory.
		if data, err := os.ReadFile(pubPath); err == nil {
			fmt.Println("")
			fmt.Println("Add this public key to your Git provider:")
			fmt.Println("")
			fmt.Print(string(data))
		}
		logx.Infof("Alternatively, replace the keys in %s with your desired keys", enclaveSSH)
	}

	logx.Successf("SSH setup complete. Directory: %s", enclaveSSH)
	logx.Infof("%s will use this directory for all SSH operations.", model.AppName)
	return nil
}

// prepareSSHInitDirectory migrates a legacy SSH directory if there is one and
// creates the current directory. A legacy path that cannot be migrated is left
// in place, so ssh-init is the way out of that state rather than blocked by it.
func prepareSSHInitDirectory(home string) (string, error) {
	current := config.HostSSHDir(home)
	if err := prepareSSHDirectory(home); err != nil {
		if !errors.Is(err, errLegacySSHNotDirectory) {
			return "", err
		}
		logx.Warnf("Leaving legacy SSH path %s unchanged and creating a new SSH directory at %s.", config.HostLegacySSHDir(home), current)
	}

	logx.Infof("Setting up %s SSH directory.", model.AppName)
	if err := os.MkdirAll(current, 0o700); err != nil {
		return "", err
	}
	// The current path may be a symbolic link to a directory, which the walk
	// would skip.
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	if err := hardenSSHDirectory(resolved); err != nil {
		return "", err
	}
	return current, nil
}

func prepareSSHDirectory(home string) error {
	state, err := migrateLegacySSHDir(home)
	if err != nil {
		return err
	}

	current := config.HostSSHDir(home)
	legacy := config.HostLegacySSHDir(home)
	switch state {
	case sshMigrationCompleted:
		logx.Infof("Relocated SSH directory from %s to %s.", legacy, current)
	case sshMigrationConflict:
		logx.Warnf("Both current and legacy SSH paths exist; using %s and leaving %s unchanged.", current, legacy)
	}
	return nil
}

func inspectSSHMigration(home string) (sshMigrationState, error) {
	legacy := config.HostLegacySSHDir(home)
	current := config.HostSSHDir(home)
	if filepath.Clean(legacy) == filepath.Clean(current) {
		return sshMigrationNone, nil
	}

	legacyInfo, err := os.Lstat(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return sshMigrationNone, nil
	}
	if err != nil {
		return sshMigrationNone, fmt.Errorf("inspect legacy SSH path %s: %w", legacy, err)
	}

	currentExists, err := currentSSHDirectoryExists(current)
	if err != nil {
		return sshMigrationNone, err
	}
	legacyIsDirectory := legacyInfo.IsDir()
	// legacyErr says why the legacy path cannot be moved.
	var legacyErr error
	switch {
	case legacyIsDirectory:
	case legacyInfo.Mode()&os.ModeSymlink != 0:
		_, legacyErr = resolveLegacySSHLink(legacy, current)
	default:
		legacyErr = errors.New("is not a directory")
	}
	legacyIsMovable := legacyErr == nil
	if currentExists && legacyIsMovable {
		// An empty current directory would shadow the legacy keys and leave
		// them to be removed with the cache, so the move replaces it.
		replaceable, err := isEmptySSHDirectory(current)
		if err != nil {
			return sshMigrationNone, err
		}
		currentExists = !replaceable
	}
	if currentExists {
		if legacyIsDirectory {
			if err := validateSSHMigrationPaths(legacy, current, true); err != nil {
				return sshMigrationNone, err
			}
			empty, err := isEmptySSHDirectory(legacy)
			if err != nil {
				return sshMigrationNone, err
			}
			if empty {
				return sshMigrationEmptyLegacy, nil
			}
		}
		return sshMigrationConflict, nil
	}
	if !legacyIsMovable {
		return sshMigrationNone, fmt.Errorf("legacy SSH path %s %v and %w; move its keys to %s or run '%s ssh-init' to create a new SSH directory there", legacy, legacyErr, errLegacySSHNotDirectory, current, model.AppName)
	}
	if legacyIsDirectory {
		if err := validateSSHMigrationPaths(legacy, current, false); err != nil {
			return sshMigrationNone, err
		}
	}
	return sshMigrationPending, nil
}

// resolveLegacySSHLink returns the directory a legacy symbolic link leads to,
// or why the link cannot be moved. It is the only check a link has to pass to
// be moved: the target must be a directory that does not overlap the current
// location. Cleanup protects the target itself, see validateSSHCleanupDirs.
func resolveLegacySSHLink(legacy string, current string) (string, error) {
	resolved, err := filepath.EvalSymlinks(legacy)
	if err != nil {
		return "", errors.New("is a link that does not lead to a directory")
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return "", errors.New("is a link that does not lead to a directory")
	}
	if err := validateSSHMigrationPaths(resolved, current, false); err != nil {
		return "", fmt.Errorf("is a link to an unusable location (%w)", err)
	}
	return resolved, nil
}

func currentSSHDirectoryExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect current SSH path %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		targetInfo, err := os.Stat(path)
		if err != nil {
			return false, fmt.Errorf("current SSH path %s is a symbolic link that does not resolve to a directory: %w", path, err)
		}
		if !targetInfo.IsDir() {
			return false, fmt.Errorf("current SSH path %s is a symbolic link that does not resolve to a directory", path)
		}
		return true, nil
	}
	if !info.IsDir() {
		return false, fmt.Errorf("current SSH path %s is not a directory", path)
	}
	return true, nil
}

// isEmptySSHDirectory reports whether path is a real directory, not a symbolic
// link to one, without entries.
func isEmptySSHDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("inspect SSH path %s: %w", path, err)
	}
	if !info.IsDir() {
		return false, nil
	}
	empty, err := directoryIsEmpty(path)
	if err != nil {
		return false, fmt.Errorf("inspect SSH directory %s: %w", path, err)
	}
	return empty, nil
}

func directoryIsEmpty(path string) (bool, error) {
	dir, err := os.Open(path) // #nosec G304 -- path is one of the two fixed SSH directories.
	if err != nil {
		return false, err
	}
	defer func() { _ = dir.Close() }()
	if _, err := dir.Readdirnames(1); err != nil {
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func validateSSHMigrationPaths(legacy string, current string, currentExists bool) error {
	overlapError := func() error {
		return fmt.Errorf("legacy SSH directory %s overlaps current location %s; move it manually", legacy, current)
	}
	if util.PathWithin(legacy, current) || util.PathWithin(current, legacy) {
		return overlapError()
	}
	legacyContainsCurrent, err := util.RealPathWithin(legacy, current)
	if err != nil {
		return fmt.Errorf("resolve SSH migration paths: %w", err)
	}
	if legacyContainsCurrent {
		return overlapError()
	}
	if !currentExists {
		return nil
	}
	currentContainsLegacy, err := util.RealPathWithin(current, legacy)
	if err != nil {
		return fmt.Errorf("resolve SSH migration paths: %w", err)
	}
	if currentContainsLegacy {
		return overlapError()
	}
	return nil
}

func migrateLegacySSHDir(home string) (sshMigrationState, error) {
	return migrateLegacySSHDirWithRename(home, os.Rename)
}

func migrateLegacySSHDirWithRename(home string, rename sshRenameFunc) (sshMigrationState, error) {
	state, err := inspectSSHMigration(home)
	if err != nil || (state != sshMigrationPending && state != sshMigrationEmptyLegacy) {
		return state, err
	}

	legacy := config.HostLegacySSHDir(home)
	current := config.HostSSHDir(home)
	lockPath := config.HostLockPath(home, "ssh-migration.lock")
	err = util.WithFileLock(lockPath, func() error {
		var inspectErr error
		state, inspectErr = inspectSSHMigration(home)
		switch {
		case inspectErr != nil:
			return inspectErr
		case state == sshMigrationEmptyLegacy:
			// Remove fails on a directory that got content in the meantime.
			if err := os.Remove(legacy); err != nil { // #nosec G703 -- legacy is the fixed application cache SSH path.
				return err
			}
			state = sshMigrationNone
		case state == sshMigrationPending:
			if moveErr := moveSSHDirectory(legacy, current, rename); moveErr != nil {
				return moveErr
			}
			state = sshMigrationCompleted
		}
		return nil
	})
	if err != nil {
		return state, fmt.Errorf("relocate legacy SSH directory from %s to %s: %w", legacy, current, err)
	}
	return state, nil
}

// moveSSHDirectory publishes the legacy path at the current location. A
// directory is renamed, so a move across filesystems fails and is left to the
// user. A symbolic link is recreated rather than followed.
func moveSSHDirectory(legacy string, current string, rename sshRenameFunc) error {
	info, err := os.Lstat(legacy)
	if err != nil {
		return err
	}
	isLink := info.Mode()&os.ModeSymlink != 0
	if !isLink {
		if err := hardenSSHDirectory(legacy); err != nil {
			return fmt.Errorf("secure legacy SSH directory: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
		return fmt.Errorf("create SSH config parent: %w", err)
	}
	// os.Rename does not replace a directory. Remove fails on one with content.
	// A failed move puts the empty directory back, so it changes nothing.
	restoreCurrent := func() {}
	if currentInfo, err := os.Lstat(current); err == nil {
		if err := os.Remove(current); err != nil { // #nosec G703 -- current is the fixed SSH directory path.
			return fmt.Errorf("replace empty SSH directory: %w", err)
		}
		restoreCurrent = func() { _ = os.Mkdir(current, currentInfo.Mode().Perm()) } // #nosec G301,G703 -- restores the removed directory with its own mode.
	}
	if isLink {
		err = moveSSHLink(legacy, current)
	} else if err = rename(legacy, current); err != nil {
		err = fmt.Errorf("%w; move it manually", err)
	}
	if err != nil {
		restoreCurrent()
	}
	return err
}

// moveSSHLink recreates the legacy symbolic link at the current location,
// pointing at the directory it resolves to: a relative target would resolve
// differently there. The target itself is left alone.
func moveSSHLink(legacy string, current string) error {
	target, err := resolveLegacySSHLink(legacy, current)
	if err != nil {
		return fmt.Errorf("legacy SSH path %s %w", legacy, err)
	}
	if err := os.Symlink(target, current); err != nil {
		return err
	}
	return os.Remove(legacy) // #nosec G703 -- legacy is the fixed application cache SSH path.
}

// hardenSSHDirectory restricts directories under root to 0700 and private keys
// to 0600. Symlinks are skipped. Swapping an entry for a symlink during the walk
// needs write access to the user's own SSH directory, which no container has,
// so the walk uses plain paths. os.Root cannot replace them before Go 1.25 and
// a descriptor-based chmod would fail on the unreadable entries this repairs.
func hardenSSHDirectory(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o700) // #nosec G122,G302 -- SSH directories need execute permission; see hardenSSHDirectory for the symlink race.
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || strings.HasSuffix(entry.Name(), ".pub") {
			return nil
		}
		private, err := isSSHPrivateKey(path, info.Mode())
		if err != nil {
			return err
		}
		if private {
			return os.Chmod(path, 0o600) // #nosec G122 -- see hardenSSHDirectory for the symlink race.
		}
		return nil
	})
}

func isSSHPrivateKey(path string, mode fs.FileMode) (bool, error) {
	name := filepath.Base(path)
	if strings.HasPrefix(name, "id_") {
		return true, nil
	}
	if mode.Perm()&0o400 == 0 {
		return false, nil
	}

	file, err := os.Open(path) // #nosec G304 -- path is visited under the managed SSH directory without following symlinks.
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return false, err
	}
	return bytes.Contains(data, []byte("PRIVATE KEY-----")), nil
}

func copyFile(src string, dst string, mode os.FileMode) error {
	// #nosec G304 -- src is resolved from known SSH config locations.
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, mode); err != nil { // #nosec G703 -- dst is resolved under the managed SSH directory.
		return err
	}
	return nil
}

func hostName() string {
	name, err := os.Hostname()
	if err != nil {
		return model.AppName
	}
	return name
}
