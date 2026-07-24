// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/logx"
)

const maxAppRootModeIssues = 5

func warnAppRootModeIssues(appRoot string) {
	issues, err := collectAppRootModeIssues(appRoot)
	if err != nil {
		logx.Warnf("Could not inspect installed asset permissions under %s: %v", appRoot, err)
		return
	}
	if len(issues) == 0 {
		return
	}

	shown := issues
	more := ""
	if len(shown) > maxAppRootModeIssues {
		shown = shown[:maxAppRootModeIssues]
		more = fmt.Sprintf("; and %d more", len(issues)-maxAppRootModeIssues)
	}
	logx.Warnf("Enclave assets have restrictive permissions that can break image builds: %s%s. Reinstall the package or restore the source checkout's asset modes under %s.", strings.Join(shown, "; "), more, appRoot)
}

func collectAppRootModeIssues(appRoot string) ([]string, error) {
	if strings.TrimSpace(appRoot) == "" {
		return nil, nil
	}

	var issues []string
	for _, rel := range []string{"entrypoint.sh", "gateway-entrypoint.sh"} {
		if err := checkAppRootFileMode(appRoot, rel, true, &issues); err != nil {
			return nil, err
		}
	}
	for _, rel := range []string{
		filepath.ToSlash(filepath.Join("runtime-assets", "auth-reconcile.sh")),
		filepath.ToSlash(filepath.Join("runtime-assets", "net.sh")),
		filepath.ToSlash(filepath.Join("runtime-assets", "tmux-session.conf")),
	} {
		if err := checkAppRootFileMode(appRoot, rel, false, &issues); err != nil {
			return nil, err
		}
	}
	for _, rel := range []string{
		filepath.ToSlash(filepath.Join("runtime-assets", "microvm", "alpine", "build-bundle.sh")),
		filepath.ToSlash(filepath.Join("runtime-assets", "microvm", "alpine", "init")),
	} {
		if err := checkAppRootFileMode(appRoot, rel, true, &issues); err != nil {
			return nil, err
		}
	}
	if err := walkAppRootModeTree(appRoot, filepath.ToSlash(filepath.Join("runtime-assets", "build-scripts")), buildScriptNeedsExecute, &issues); err != nil {
		return nil, err
	}
	if err := walkAppRootModeTree(appRoot, "extensions", extensionFileNeedsExecute, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

func checkAppRootFileMode(appRoot string, rel string, executable bool, issues *[]string) error {
	path := filepath.Join(appRoot, filepath.FromSlash(rel))
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	appendAppRootModeIssue(appRoot, path, info, executable, issues)
	return nil
}

func walkAppRootModeTree(appRoot string, relRoot string, executable func(string) bool, issues *[]string) error {
	root := filepath.Join(appRoot, filepath.FromSlash(relRoot))
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(appRoot, path)
		if err != nil {
			return err
		}
		appendAppRootModeIssue(appRoot, path, info, executable(filepath.ToSlash(rel)), issues)
		return nil
	})
}

func appendAppRootModeIssue(appRoot string, path string, info os.FileInfo, executable bool, issues *[]string) {
	mode := info.Mode().Perm()
	var missing []string
	if info.IsDir() {
		if mode&0o004 == 0 {
			missing = append(missing, "read")
		}
		if mode&0o001 == 0 {
			missing = append(missing, "execute")
		}
	} else {
		if mode&0o004 == 0 {
			missing = append(missing, "read")
		}
		if executable && mode&0o001 == 0 {
			missing = append(missing, "execute")
		}
	}
	if len(missing) == 0 {
		return
	}

	rel, err := filepath.Rel(appRoot, path)
	if err != nil {
		rel = path
	}
	*issues = append(*issues, fmt.Sprintf("%s lacks world %s permission (mode %#o)", filepath.ToSlash(rel), strings.Join(missing, "/"), mode))
}

// These predicates mirror the executable-asset normalization rules in
// Dockerfile, debian/rules, internal/appassets, and dockerfile_gen.go.
func buildScriptNeedsExecute(rel string) bool {
	base := filepath.Base(filepath.FromSlash(rel))
	return strings.HasSuffix(base, ".sh") || strings.Contains(rel, "/bin/")
}

func extensionFileNeedsExecute(rel string) bool {
	return filepath.Base(filepath.FromSlash(rel)) == "install.sh"
}
