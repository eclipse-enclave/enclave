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

	"enclave/internal/config"
	"enclave/internal/envflag"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/mounts"
	"enclave/internal/util"
)

// sensitiveMountsAllowed reports whether the user opted in to mounting
// sensitive host paths, through --allow-sensitive-mounts or
// ENCLAVE_ALLOW_SENSITIVE_MOUNTS. An explicit --allow-sensitive-mounts=false
// wins over the environment. There is deliberately no config key, so neither
// global nor project config can grant it. Global config can only exempt
// specific paths (sensitive_dir_exceptions), which still leaves the
// hard-protected locations refused.
func sensitiveMountsAllowed(flag bool, source model.OptionSource) bool {
	if source == model.SourceCLI {
		return flag
	}
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

// configSensitiveDirExceptions turns the effective sensitive_dir_exceptions
// into exceptions. Unusable entries are dropped with a warning, which fails
// closed: the guard then acts as if they were not there. Project config never
// gets here: its entries are dropped when it is loaded.
func configSensitiveDirExceptions(opts model.Options, home string, projectDir string) ([]mounts.SensitiveDirException, []string) {
	key := "sensitive_dir_exceptions"
	file := formatSource(opts.Sources.SensitiveDirExceptions, projectDir)
	// Project tool overrides are dropped too, so a tool override entry comes
	// from global config.
	if opts.Sources.SensitiveDirExceptions == model.SourceToolOverride {
		key = "tool_overrides." + opts.Tool + "." + key
		file = formatSource(model.SourceGlobal, projectDir)
	}
	realHome := home
	if resolved, err := util.RealPath(home); err == nil {
		realHome = resolved
	}

	var exceptions []mounts.SensitiveDirException
	var warnings []string
	for _, entry := range opts.SensitiveDirExceptions {
		origin := fmt.Sprintf("%s entry %q in %s", key, entry, file)
		exception, unusable := configSensitiveDirException(entry, origin, home, realHome)
		if unusable != "" {
			warnings = append(warnings, "Ignoring "+origin+": "+unusable)
			continue
		}
		exceptions = append(exceptions, exception)
	}
	return exceptions, warnings
}

// configSensitiveDirException resolves one sensitive_dir_exceptions entry, or
// says why it is unusable.
func configSensitiveDirException(entry, origin, home, realHome string) (mounts.SensitiveDirException, string) {
	path := util.ExpandTilde(strings.TrimSpace(entry), home)
	switch {
	case path == "":
		return mounts.SensitiveDirException{}, "the path is empty"
	case !filepath.IsAbs(path):
		return mounts.SensitiveDirException{}, "the path must be absolute or start with ~/"
	}
	exception := mounts.NewSensitiveDirException(path, origin)
	if util.PathWithin(exception.Path, realHome) {
		return mounts.SensitiveDirException{}, "home and its parents cannot be exempted"
	}
	return exception, ""
}

// checkSensitiveMounts refuses to mount a project or additional directory
// that would expose sensitive user data (see mounts.SensitivePaths) unless
// allowed or covered by one of the exceptions. An exempted mount warns on
// every start; no exception lifts a hard-protected location. The paths must
// be symlink-resolved. Additional directories include those from global and
// project config. Devcontainer binds need no check: they must resolve inside
// the project, and nothing inside a project that is not sensitive is
// sensitive.
func checkSensitiveMounts(project model.Project, additional []additionalDirs, exceptions []mounts.SensitiveDirException, home string, allowed bool) error {
	sensitive := mounts.NewSensitivePaths(home, config.HostAppDataDirs(home), config.HostEnclaveRoots(home))
	var sensitiveMounts, exempted, offenders []string
	assess := func(dir string, origin string) {
		a := sensitive.Assess(dir, exceptions)
		mount := fmt.Sprintf("%s (%s): ", dir, origin)
		switch {
		case a.Reason == "":
			return
		case a.Exception == nil:
			offenders = append(offenders, mount+a.Reason)
		case a.HardReason != "":
			offenders = append(offenders, mount+a.HardReason+", which no sensitive-dir exception can cover; mount a narrower directory")
		default:
			exempted = append(exempted, mount+a.Reason+"; exempted by "+a.Exception.Origin)
		}
		sensitiveMounts = append(sensitiveMounts, mount+a.Reason)
	}
	assess(project.RealDir, "project directory")
	for _, list := range additional {
		for _, dir := range list.dirs {
			assess(dir, list.origin(project.Dir))
		}
	}

	if len(offenders) == 0 || allowed {
		warnings := exempted
		if allowed {
			warnings = sensitiveMounts
		}
		for _, mount := range warnings {
			logx.Warnf("Mounting sensitive host path %s", mount)
		}
		return nil
	}
	return fmt.Errorf("refusing to mount sensitive host paths:\n  %s\n"+
		"To mount them anyway, pass them with --add-sensitive-dir or --add-readonly-sensitive-dir\n"+
		"(this works for the project directory too), or list them in sensitive_dir_exceptions in\n%s.\n"+
		"Pass --allow-sensitive-mounts or set %s=1 to mount all sensitive paths anyway",
		strings.Join(offenders, "\n  "), formatSource(model.SourceGlobal, project.Dir), model.EnvAllowSensitiveMounts)
}
