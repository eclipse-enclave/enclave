// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

// capabilities is what an extension will be able to do once installed.
type capabilities struct {
	// Spec is config's projection of the staged spec document, carried whole so
	// a capability the schema gains reaches this summary without a second copy
	// here. The fields below are the ones only the staged tree can answer.
	Spec config.SpecSummary

	InstallScript  bool
	UpdateProbe    bool
	StartupScripts []string
	// AllowDomains folds the spec's allowedDomains together with the domains a
	// staged gateway-allowlist.conf widens reachability to; that file's other
	// directives are reported verbatim in AllowlistDirectives.
	AllowDomains        []string
	AllowlistDirectives []string
	Skills              []string
	WorkspaceFiles      []string
	HomeFiles           []string
	ShadowsBuiltin      bool
	IgnoredGoDir        bool
	Files               int
	Bytes               int64
}

// yoloActive reports whether the extension arranges for the agent's launch
// command to skip per-action approval: a yolo flag is declared and it is not
// explicitly disabled (sandbox.yoloEnabled defaults to true when unset).
func (c capabilities) yoloActive() bool {
	return c.Spec.YoloFlag != "" && c.Spec.YoloEnabled
}

// entrypointDir is the startup-script directory this kind's extensions ship.
// The rule is entrypoint.sh's, not config's: it sources entrypoint.d from the
// selected tool's directory alone, and feature-entrypoint.d from each enabled
// feature's, so neither kind runs the other's directory.
func entrypointDir(kind model.ExtensionKind) string {
	if kind == model.KindTool {
		return model.ToolEntrypointDir
	}
	return model.FeatureEntrypointDir
}

// inspect summarizes a staged extension directory.
func inspect(dir string, kind model.ExtensionKind) (capabilities, error) {
	summary, err := config.SummarizeSpecDir(dir)
	if err != nil {
		return capabilities{}, err
	}

	tree, err := scanExtensionTree(dir, kind)
	if err != nil {
		return capabilities{}, err
	}
	domains := append([]string{}, summary.AllowedDomains...)
	allowlistDomains, allowlistDirectives, err := parseAllowlistFile(filepath.Join(dir, model.AllowlistFilename))
	if err != nil {
		return capabilities{}, err
	}

	return capabilities{
		Spec:                summary,
		InstallScript:       tree.InstallScript,
		UpdateProbe:         tree.UpdateProbe && config.UpdateProbeApplies(kind.SpecKind()),
		StartupScripts:      tree.StartupScripts,
		AllowDomains:        dedupeSorted(append(domains, allowlistDomains...)),
		AllowlistDirectives: dedupeSorted(allowlistDirectives),
		Skills:              tree.Skills,
		WorkspaceFiles:      tree.WorkspaceFiles,
		HomeFiles:           tree.HomeFiles,
		IgnoredGoDir:        tree.GoDir,
		Files:               tree.Files,
		Bytes:               tree.Bytes,
	}, nil
}

// extensionTree is what one walk of a staged or installed extension directory
// answers about it.
type extensionTree struct {
	Files          int
	Bytes          int64
	InstallScript  bool
	UpdateProbe    bool
	GoDir          bool
	StartupScripts []string
	Skills         []string
	WorkspaceFiles []string
	HomeFiles      []string
}

// scanExtensionTree answers every filesystem-derived capability in one walk of
// dir. The provenance sidecar counts towards nothing.
func scanExtensionTree(dir string, kind model.ExtensionKind) (extensionTree, error) {
	var (
		skillsPrefix    = model.SkillsDirName + "/"
		workspacePrefix = model.ExtensionFilesDir + "/" + model.ExtensionFilesWorkspaceDir + "/"
		homePrefix      = model.ExtensionFilesDir + "/" + model.ExtensionFilesHomeDir + "/"
	)
	tree := extensionTree{}
	entrypoint := entrypointDir(kind)

	err := walkExtensionEntries(dir, func(rel string, _ string, entry fs.DirEntry) error {
		if entry.IsDir() {
			if rel == model.ExtensionGoDir {
				tree.GoDir = true
			}
			if name, ok := immediateChild(rel, skillsPrefix); ok {
				tree.Skills = append(tree.Skills, name)
			}
			return nil
		}
		if rel == model.ExtensionSourceFilename {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		tree.Files++
		tree.Bytes += info.Size()

		switch rel {
		case model.InstallScriptFilename:
			tree.InstallScript = true
		case model.CheckUpdateScriptFilename:
			tree.UpdateProbe = true
		}
		if name, ok := immediateChild(rel, skillsPrefix); ok {
			tree.Skills = append(tree.Skills, name)
		}
		// entrypoint.sh globs "*.sh", so anything else in the directory — a
		// README, a helper data file — is never sourced.
		if name, ok := immediateChild(rel, entrypoint+"/"); ok && strings.HasSuffix(name, ".sh") {
			tree.StartupScripts = append(tree.StartupScripts, entrypoint+"/"+name)
		}
		if name, ok := strings.CutPrefix(rel, workspacePrefix); ok {
			tree.WorkspaceFiles = append(tree.WorkspaceFiles, name)
		}
		if name, ok := strings.CutPrefix(rel, homePrefix); ok {
			tree.HomeFiles = append(tree.HomeFiles, name)
		}
		return nil
	})
	if err != nil {
		return extensionTree{}, err
	}

	// entrypoint.sh sources the scripts in glob order, so they are listed
	// sorted like everything else.
	sort.Strings(tree.StartupScripts)
	sort.Strings(tree.Skills)
	sort.Strings(tree.WorkspaceFiles)
	sort.Strings(tree.HomeFiles)
	return tree, nil
}

// immediateChild reports rel's name when rel is a direct child of prefix (which
// ends in a slash), and false for anything nested deeper.
func immediateChild(rel string, prefix string) (string, bool) {
	name, ok := strings.CutPrefix(rel, prefix)
	if !ok || name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

// leadingVar returns the substituted variable an initFiles path starts with,
// accepting both $VAR and ${VAR}. Only the variables kit-init.sh hands to
// envsubst count; any other name is left literal in the resolved path and so
// behaves like an ordinary path segment.
func leadingVar(path string) string {
	for _, name := range model.KitInitSubstitutedVars {
		if strings.HasPrefix(path, "${"+name+"}") {
			return name
		}
		// Unbraced: envsubst reads the longest identifier, so $HOMEBREW is the
		// variable HOMEBREW and not HOME followed by text.
		if rest, ok := strings.CutPrefix(path, "$"+name); ok && !startsWithIdentChar(rest) {
			return name
		}
	}
	return ""
}

func startsWithIdentChar(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// writesIntoProject reports whether an initFiles path lands in the user's real
// project directory. kit-init.sh resolves the path with envsubst, so WORKDIR
// expands to the project directory and HOME to an absolute path outside it;
// anything else resolves relative to the project. Only a leading "/" escapes
// it — a leading "~" does not, since the resolved path is used quoted and no
// shell ever expands it.
func writesIntoProject(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	switch leadingVar(trimmed) {
	case model.KitInitWorkdirVar:
		return true
	case model.KitInitHomeVar:
		return false
	}
	return !strings.HasPrefix(trimmed, "/")
}

// parseAllowlistFile extracts the hosts a gateway allowlist fragment names.
// dnsmasq fragments use server=/<domain>/<ip> and address=/<domain>/<ip> to
// widen reachability; any other directive (for example conf-file=, which
// pulls in a whole other fragment) is returned verbatim as a directive so it
// is never silently hidden from the capability summary.
func parseAllowlistFile(path string) (domains []string, directives []string, err error) {
	// #nosec G304 -- path is inside the caller's staging directory.
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rest, ok := strings.CutPrefix(line, "server=/")
		if !ok {
			rest, ok = strings.CutPrefix(line, "address=/")
		}
		if !ok {
			directives = append(directives, line)
			continue
		}
		if domain, _, _ := strings.Cut(rest, "/"); domain != "" {
			domains = append(domains, domain)
		}
	}
	return domains, directives, scanner.Err()
}

func dedupeSorted(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	result := util.Dedupe(trimmed)
	sort.Strings(result)
	return result
}

// allPorts folds provider OAuth callback ports into the container ports
// declared under `ports:`. A declared port is only bound on the host when it
// sets publish, so that is carried into the rendering rather than left to the
// port number, which says nothing about it.
func (c capabilities) allPorts() []string {
	ports := make([]string, 0, len(c.Spec.Ports))
	for _, port := range c.Spec.Ports {
		text := strconv.Itoa(port.Container)
		if port.Publish {
			text += " (published on the host)"
		}
		ports = append(ports, text)
	}
	for _, provider := range c.Spec.Providers {
		ports = append(ports, provider.OAuthPorts...)
	}
	return dedupeSorted(ports)
}

func describeProvider(p config.SpecProviderSummary) string {
	parts := []string{p.Name}
	if len(p.AuthFiles) > 0 {
		parts = append(parts, "authFiles="+strings.Join(p.AuthFiles, "+"))
	}
	if p.AuthSession {
		mode := p.AuthSessionMode
		if mode == "" {
			mode = "any"
		}
		parts = append(parts, "authSession="+mode)
	}
	if len(p.OAuthPorts) > 0 {
		parts = append(parts, "oauthPorts="+strings.Join(p.OAuthPorts, "+"))
	}
	if p.SecurestorageDirEnv != "" {
		parts = append(parts, "securestorageDirEnv="+p.SecurestorageDirEnv)
	}
	return strings.Join(parts, " ")
}

func describeCredentialSource(c config.SpecCredentialSource) string {
	parts := []string{c.ID}
	if len(c.Env) > 0 {
		parts = append(parts, "env="+strings.Join(c.Env, "+"))
	}
	if c.FilePath != "" {
		parts = append(parts, "file="+c.FilePath)
	}
	if c.FileParser != "" {
		parts = append(parts, "parser="+c.FileParser)
	}
	if c.ReleaseHeader != "" {
		parts = append(parts, "header="+c.ReleaseHeader)
	}
	if len(c.ReleaseHosts) > 0 {
		parts = append(parts, "hosts="+strings.Join(c.ReleaseHosts, "+"))
	}
	return strings.Join(parts, " ")
}

func describeInitFile(n config.SpecInitFileSummary) string {
	parts := []string{n.Path}
	if n.Mode != "" {
		parts = append(parts, "mode="+n.Mode)
	}
	if n.OnlyIfMissing {
		parts = append(parts, "onlyIfMissing")
	}
	if writesIntoProject(n.Path) {
		parts = append(parts, "writesProject")
	}
	// The content is written on every start, so a rewrite behind an unchanged
	// path is a change to what the extension does. The digest reports it
	// without pulling an arbitrarily long file into the summary.
	if n.ContentDigest != "" {
		parts = append(parts, "content="+n.ContentDigest)
	}
	return strings.Join(parts, " ")
}

func mapDescribe[T any](items []T, describe func(T) string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, describe(item))
	}
	return out
}

func joinWithPlus(values []string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, "+"+value)
	}
	return strings.Join(parts, ", ")
}
