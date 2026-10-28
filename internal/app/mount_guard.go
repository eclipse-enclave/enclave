// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"enclave/internal/config"
	"enclave/internal/envflag"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/mounts"
)

// sensitiveMountsAllowed reports whether the user opted in to mounting
// sensitive host paths, through --allow-sensitive-mounts or
// ENCLAVE_ALLOW_SENSITIVE_MOUNTS. An explicit --allow-sensitive-mounts=false
// wins over the environment. There is deliberately no config key, so neither
// global nor project config can grant it.
func sensitiveMountsAllowed(flag bool, source model.OptionSource) bool {
	if source == model.SourceCLI {
		return flag
	}
	return flag || envflag.Truthy(os.LookupEnv(model.EnvAllowSensitiveMounts))
}

// additionalDir is an additional directory to mount, symlink-resolved, and
// the flag or config key that set it, so a refusal points at what to change.
type additionalDir struct {
	path   string
	origin string
}

// validateAdditionalDirs resolves the add_dirs and add_readonly_dirs entries
// one at a time, so each validated directory and each error keeps the origin
// of its entry. A read-only entry already mounted writable is skipped.
func validateAdditionalDirs(opts model.Options, projectReal string, home string) (writable []additionalDir, readonly []additionalDir, err error) {
	var seen []string
	var errs []string
	resolve := func(option string, entries []string) []additionalDir {
		var dirs []additionalDir
		for _, entry := range entries {
			origin := config.ListEntryOriginLabel(option, opts.Origins, entry)
			validated, err := mounts.ValidateExtraDirsWithExisting([]string{entry}, seen, projectReal, home)
			if err != nil {
				errs = append(errs, origin+": "+err.Error())
				continue
			}
			for _, path := range validated {
				seen = append(seen, path)
				dirs = append(dirs, additionalDir{path: path, origin: origin})
			}
		}
		return dirs
	}
	writable = resolve("add_dirs", opts.AddDirs)
	readonly = resolve("add_readonly_dirs", opts.AddReadonlyDirs)
	if len(errs) > 0 {
		return nil, nil, errors.New(strings.Join(errs, "\n"))
	}
	return writable, readonly, nil
}

func additionalDirPaths(dirs []additionalDir) []string {
	paths := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		paths = append(paths, dir.path)
	}
	return paths
}

// checkSensitiveMounts refuses to mount a project or additional directory
// that would expose sensitive user data (see mounts.SensitivePaths) unless
// allowed. The paths must be symlink-resolved. Additional directories include
// those from global and project config. Devcontainer binds need no check:
// they must resolve inside the project, and nothing inside a project that is
// not sensitive is sensitive.
func checkSensitiveMounts(project model.Project, additional []additionalDir, home string, allowed bool) error {
	sensitive := mounts.NewSensitivePaths(home, config.HostAppDataDirs(home))
	var offenders []string
	if reason := sensitive.Reason(project.RealDir); reason != "" {
		offenders = append(offenders, fmt.Sprintf("%s (project directory): %s", project.RealDir, reason))
	}
	for _, dir := range additional {
		if reason := sensitive.Reason(dir.path); reason != "" {
			offenders = append(offenders, fmt.Sprintf("%s (%s): %s", dir.path, dir.origin, reason))
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	if allowed {
		for _, offender := range offenders {
			logx.Warnf("Mounting sensitive host path %s", offender)
		}
		return nil
	}
	return fmt.Errorf("refusing to mount sensitive host paths:\n  %s\n"+
		"Run enclave from a project directory, or narrow the additional directories where they are set.\n"+
		"Pass --allow-sensitive-mounts or set %s=1 to mount them anyway",
		strings.Join(offenders, "\n  "), model.EnvAllowSensitiveMounts)
}
