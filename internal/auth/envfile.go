// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"enclave/internal/config"
	"enclave/internal/util"
)

// EditEnvContent drops every assignment to a key in remove and appends
// key=value for each key in set. Comments, blank lines, and other keys are kept
// in place. It reports the removed keys in file order, without duplicates, and
// returns content unchanged when nothing is removed or set.
func EditEnvContent(content string, remove []string, set []string, value string) (string, []string) {
	var kept []string
	var removed []string
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		if pair, ok := util.ParseEnvLine(line); ok && slices.Contains(remove, pair.Key) {
			if !slices.Contains(removed, pair.Key) {
				removed = append(removed, pair.Key)
			}
			continue
		}
		kept = append(kept, line)
	}
	if len(removed) == 0 && len(set) == 0 {
		return content, nil
	}
	for _, key := range set {
		kept = append(kept, key+"="+value)
	}
	if strings.TrimSpace(strings.Join(kept, "")) == "" {
		return "", removed
	}
	return strings.Join(kept, "\n") + "\n", removed
}

// ParseEnvContent parses .env content with the secrets-file rules.
func ParseEnvContent(content string) (map[string]string, error) {
	return parseEnvLines(strings.NewReader(content))
}

// EditEnvFile applies EditEnvContent to a host secrets file. A symlinked
// file is edited at its target, under a lock keyed by that target, so two
// links to one file cannot drop each other's changes. The file and its
// directory are created private when a key is set. A file left without
// assignments or comments is deleted, unless a symlink points to it: the
// target is then emptied so the link keeps working. Writes replace the file
// atomically and are synced; a request that changes nothing writes nothing.
// It returns the removed assignments, read under the same lock as the edit.
func EditEnvFile(home string, path string, remove []string, set []string, value string) ([]util.EnvPair, error) {
	target, linked, err := resolveEnvFileTarget(path)
	if err != nil {
		return nil, err
	}
	var removed []util.EnvPair
	lockPath := config.HostLockPath(home, "secrets-"+util.HashString(canonicalLockTarget(target))+".lock")
	err = util.WithFileLock(lockPath, func() error {
		// #nosec G304 -- path is resolved from trusted secrets file locations.
		data, err := os.ReadFile(target)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		exists := err == nil
		previous, err := ParseEnvContent(string(data))
		if err != nil {
			return err
		}
		content, removedKeys := EditEnvContent(string(data), remove, set, value)
		for _, key := range removedKeys {
			removed = append(removed, util.EnvPair{Key: key, Value: previous[key]})
		}
		switch {
		case content == string(data):
			return nil
		case content == "" && !linked:
			if exists {
				return os.Remove(target)
			}
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := util.WriteFileAtomic(target, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
	return removed, err
}

// canonicalLockTarget names a secrets file by its real directory, so paths
// that reach one file through symlinked directories share a lock. A directory
// that does not exist yet has no aliases, and the cleaned path is used.
func canonicalLockTarget(target string) string {
	dir, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return filepath.Clean(target)
	}
	return filepath.Join(dir, filepath.Base(target))
}

// resolveEnvFileTarget follows a symlinked secrets file (for example one kept
// in a dotfiles repository) so an edit does not replace the link.
// A dangling link resolves to the file it names, which the edit creates.
func resolveEnvFileTarget(path string) (target string, linked bool, err error) {
	target = path
	for range 40 {
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			return target, linked, nil
		}
		if err != nil {
			return "", false, err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return target, linked, nil
		}
		next, err := os.Readlink(target)
		if err != nil {
			return "", false, err
		}
		if !filepath.IsAbs(next) {
			next = filepath.Join(filepath.Dir(target), next)
		}
		target, linked = next, true
	}
	return "", false, fmt.Errorf("resolve symlinked secrets file %s: too many links", path)
}
