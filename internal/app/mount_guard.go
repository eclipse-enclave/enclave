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
	"strings"

	"enclave/internal/config"
	"enclave/internal/envflag"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/mounts"
)

// sensitiveMountsAllowed reports whether the user opted in to mounting
// sensitive host paths, through --allow-sensitive-mounts or
// ENCLAVE_ALLOW_SENSITIVE_MOUNTS. There is deliberately no config key, so
// neither global nor project config can grant it.
func sensitiveMountsAllowed(flag bool) bool {
	return flag || envflag.Truthy(os.LookupEnv(model.EnvAllowSensitiveMounts))
}

// additionalDirs is a list of additional directories to mount and the option
// that set it. Config fills a list only when nothing of higher precedence set
// it, so the whole list has one source.
type additionalDirs struct {
	dirs   []string
	flag   string
	key    string
	source model.OptionSource
}

// origin names the flag or config file that set the list, so a refusal points
// at what to change.
func (a additionalDirs) origin(projectDir string) string {
	if a.source == model.SourceCLI {
		return a.flag
	}
	if src := formatSource(a.source, projectDir); src != "" {
		return a.key + " from " + src
	}
	return a.key
}

// checkSensitiveMounts refuses to mount a project or additional directory
// that would expose sensitive user data (see mounts.SensitivePaths) unless
// allowed. The paths must be symlink-resolved. Additional directories include
// those from global and project config. Devcontainer binds need no check:
// they must resolve inside the project, and nothing inside a project that is
// not sensitive is sensitive.
func checkSensitiveMounts(project model.Project, additional []additionalDirs, home string, allowed bool) error {
	sensitive := mounts.NewSensitivePaths(home, config.HostAppDataDirs(home))
	var offenders []string
	if reason := sensitive.Reason(project.RealDir); reason != "" {
		offenders = append(offenders, fmt.Sprintf("%s (project directory): %s", project.RealDir, reason))
	}
	for _, list := range additional {
		for _, dir := range list.dirs {
			if reason := sensitive.Reason(dir); reason != "" {
				offenders = append(offenders, fmt.Sprintf("%s (%s): %s", dir, list.origin(project.Dir), reason))
			}
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
