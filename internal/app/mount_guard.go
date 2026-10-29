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
	"path/filepath"
	"slices"
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
// global nor project config can grant it. Config can only exempt specific
// paths (sensitive_dir_exceptions), which still leaves the
// hard-protected locations refused.
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

// validateAdditionalDirs resolves the add_dirs and add_readonly_dirs entries,
// and the resolved --add-sensitive-dir and --add-readonly-sensitive-dir
// directories, one at a time, so each validated directory and each error
// keeps the origin of its entry. Read-write lists go first, so a read-only
// entry already mounted writable is skipped.
func validateAdditionalDirs(opts model.Options, sensitiveDirs, readonlySensitiveDirs []string, projectReal string, home string) (writable []additionalDir, readonly []additionalDir, err error) {
	var seen []string
	var errs []string
	resolve := func(entries []string, origin func(entry string) string) []additionalDir {
		var dirs []additionalDir
		for _, entry := range entries {
			origin := origin(entry)
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
	configured := func(option string) func(string) string {
		return func(entry string) string { return config.ListEntryOriginLabel(option, opts.Origins, entry) }
	}
	flag := func(name string) func(string) string {
		return func(string) string { return name }
	}
	writable = slices.Concat(resolve(opts.AddDirs, configured("add_dirs")), resolve(sensitiveDirs, flag(addSensitiveDirFlag)))
	readonly = slices.Concat(resolve(opts.AddReadonlyDirs, configured("add_readonly_dirs")), resolve(readonlySensitiveDirs, flag(addReadonlySensitiveDirFlag)))
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

// configSensitiveDirExceptions turns the effective sensitive_dir_exceptions
// into exceptions. Unusable entries are dropped with a warning, which fails
// closed: the guard then acts as if they were not there.
func configSensitiveDirExceptions(opts model.Options, home string) ([]mounts.SensitiveDirException, []string) {
	realHome := home
	if resolved, err := util.RealPath(home); err == nil {
		realHome = resolved
	}

	var exceptions []mounts.SensitiveDirException
	var warnings []string
	for _, entry := range opts.SensitiveDirExceptions {
		origin := sensitiveDirExceptionOrigin(opts.Origins, entry)
		exception, unusable := configSensitiveDirException(entry, origin, home, realHome)
		if unusable != "" {
			warnings = append(warnings, "Ignoring "+origin+": "+unusable)
			continue
		}
		exceptions = append(exceptions, exception)
	}
	return exceptions, warnings
}

// sensitiveDirExceptionOrigin names a sensitive_dir_exceptions entry and the
// config file that set it.
func sensitiveDirExceptionOrigin(origins model.ListOrigins, entry string) string {
	key, file, found := strings.Cut(config.ListEntryOriginLabel("sensitive_dir_exceptions", origins, entry), " in ")
	if !found {
		return fmt.Sprintf("%s entry %q", key, entry)
	}
	return fmt.Sprintf("%s entry %q in %s", key, entry, file)
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
func checkSensitiveMounts(project model.Project, additional []additionalDir, exceptions []mounts.SensitiveDirException, home string, allowed bool) error {
	sensitive := mounts.NewSensitivePaths(home, config.HostAppDataDirs(home), config.HostEnclaveRoots(home))
	var sensitiveMounts, exempted, offenders []string
	exemptable, hardProtected := false, false
	assess := func(dir string, origin string) {
		a := sensitive.Assess(dir, exceptions)
		mount := fmt.Sprintf("%s (%s): ", dir, origin)
		switch {
		case a.Reason == "":
			return
		case a.HardReason != "":
			offenders = append(offenders, mount+a.HardReason+", which no sensitive-dir exception can cover; mount a narrower directory")
			hardProtected = true
		case a.Exception == nil:
			offenders = append(offenders, mount+a.Reason)
			exemptable = true
		default:
			exempted = append(exempted, mount+a.Reason+"; exempted by "+a.Exception.Origin)
		}
		reason := a.Reason
		if a.HardReason != "" {
			reason = a.HardReason
		}
		sensitiveMounts = append(sensitiveMounts, mount+reason)
	}
	assess(project.RealDir, "project directory")
	for _, dir := range additional {
		assess(dir.path, dir.origin)
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
	hint := "Run enclave from a project directory, or narrow the additional directories where they are set.\n"
	if exemptable {
		exemptableMounts := "them"
		if hardProtected {
			exemptableMounts = "the others"
		}
		hint = "To mount " + exemptableMounts + " anyway, pass them with --add-sensitive-dir or --add-readonly-sensitive-dir\n" +
			"(this works for the project directory too), or list them in sensitive_dir_exceptions in\n" +
			formatSource(model.SourceGlobal, project.Dir) + ".\n"
	}
	return fmt.Errorf("refusing to mount sensitive host paths:\n  %s\n%s"+
		"Pass --allow-sensitive-mounts or set %s=1 to mount all sensitive paths anyway",
		strings.Join(offenders, "\n  "), hint, model.EnvAllowSensitiveMounts)
}
