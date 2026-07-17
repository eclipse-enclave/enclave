// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func HashFile(path string) (hash string, err error) {
	// #nosec G304 -- path is provided by trusted internal callers for local file hashing.
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "none", nil
		}
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func HashString(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

func ExpandTilde(path string, home string) string {
	if path == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}

func HasPathTraversal(path string) bool {
	clean := filepath.Clean(path)
	parts := strings.Split(clean, string(os.PathSeparator))
	for _, part := range parts {
		if part == ".." {
			return true
		}
	}
	return false
}

// PathWithin reports whether path is equal to or inside root after lexical
// cleaning. Inputs are not symlink-resolved; use RealPathWithin for paths that
// come from project-controlled config or mount specs.
func PathWithin(root string, path string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(path) == "" {
		return false
	}
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

// PathStrictlyWithin reports whether path is inside root, excluding root itself.
func PathStrictlyWithin(root string, path string) bool {
	if !PathWithin(root, path) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != "."
}

// RealPathWithin resolves root and path to absolute paths and resolves symlinks
// where possible before applying PathWithin. A non-existent path falls back to
// its absolute cleaned form so callers can validate Docker create-on-mount
// semantics; other symlink resolution errors are returned.
func RealPathWithin(root string, path string) (bool, error) {
	resolvedRoot, err := realPathForContainment(root, false)
	if err != nil {
		return false, err
	}
	resolvedPath, err := realPathForContainment(path, true)
	if err != nil {
		return false, err
	}
	return PathWithin(resolvedRoot, resolvedPath), nil
}

func realPathForContainment(path string, allowMissing bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fs.ErrInvalid
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !allowMissing || !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return resolveExistingParent(abs)
}

func resolveExistingParent(abs string) (string, error) {
	missing := []string{}
	current := abs
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			parts := append([]string{filepath.Clean(resolved)}, missing...)
			return filepath.Join(parts...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs, nil
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}

func HasPathPrefix(path string, prefix string) bool {
	return PathWithin(prefix, path)
}

func IsCriticalSystemDir(path string) bool {
	return HasPathPrefix(path, "/bin") || HasPathPrefix(path, "/sbin") ||
		HasPathPrefix(path, "/boot") || HasPathPrefix(path, "/lib") ||
		HasPathPrefix(path, "/lib64") || HasPathPrefix(path, "/run") ||
		HasPathPrefix(path, "/var/run") || HasPathPrefix(path, "/root")
}

func IsSystemDir(path string) bool {
	return HasPathPrefix(path, "/etc") || HasPathPrefix(path, "/var") ||
		HasPathPrefix(path, "/usr") || HasPathPrefix(path, "/sys") ||
		HasPathPrefix(path, "/proc") || HasPathPrefix(path, "/dev")
}

func IsPortNumber(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(value)
	return err == nil && n >= 1 && n <= 65535
}

type EnvPair struct {
	Key   string
	Value string
}

func ParseEnvPairs(r io.Reader) ([]EnvPair, error) {
	// Read the whole stream rather than using bufio.Scanner: .env values can be
	// arbitrarily long (inlined certs, base64 service-account JSON, long tokens)
	// and a scanner's 64KB line cap would otherwise make a single oversized line
	// fail and silently drop the entire file for callers like envFileValues.
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	pairs := []EnvPair{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, "\"'")
		pairs = append(pairs, EnvPair{Key: key, Value: value})
	}
	return pairs, nil
}

func ParseEnv(r io.Reader) (map[string]string, error) {
	pairs, err := ParseEnvPairs(r)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		env[pair.Key] = pair.Value
	}
	return env, nil
}

func ParseEnvFile(path string) ([]string, error) {
	// #nosec G304 -- path is provided by trusted local config/devcontainer callers.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	pairs, err := ParseEnvPairs(file)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		out = append(out, pair.Key+"="+pair.Value)
	}
	return out, nil
}

func EnvKeysFromFile(path string) ([]string, error) {
	// #nosec G304 -- path is provided by trusted internal callers for env parsing.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	pairs, err := ParseEnvPairs(file)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		keys = append(keys, pair.Key)
	}
	return keys, nil
}

func RedactSecret(value string) string {
	if value == "" {
		return "<empty>"
	}
	if len(value) > 8 {
		return value[:4] + "..." + value[len(value)-4:]
	}
	return "****"
}

func TitleCase(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func PathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func DirHasFiles(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func Dedupe[T comparable](values []T) []T {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[T]struct{}, len(values))
	out := make([]T, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// ShellQuote wraps value in single quotes so it is safe to embed verbatim in a
// POSIX shell command, escaping any embedded single quotes. An empty value
// yields "”".
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// WorkspaceIdentityHash returns a stable hash for gateway workspace identity.
// It prefers primaryPath and falls back to fallbackPath when primary is empty.
func WorkspaceIdentityHash(primaryPath string, fallbackPath string) string {
	workspace := strings.TrimSpace(primaryPath)
	if workspace == "" {
		workspace = strings.TrimSpace(fallbackPath)
	}
	if workspace == "" {
		return ""
	}
	return HashString(filepath.Clean(workspace))
}
