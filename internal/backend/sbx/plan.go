// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package sbx

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"enclave/internal/backend"
)

// guestFileStageDir is where writable file mounts are staged inside the
// sandbox. SBX binds only directories read-write, so each such file is copied
// to its target before the session and back after it.
const guestFileStageDir = "/run/enclave-sbx/files"

// ImageConfig is the part of an image's configuration a session needs.
type ImageConfig struct {
	ID         string
	User       string
	Entrypoint []string
	Cmd        []string
}

// storeSourceFunc resolves the host directory backing a persistent store.
type storeSourceFunc func(key backend.StoreKey, kind backend.StoreKind) (string, error)

// stagedFile is a writable single-file mount realized by copying.
type stagedFile struct {
	Source      string // host file the session reads and writes
	HostStaged  string // host copy inside the staging directory
	GuestStaged string // the same copy as seen inside the sandbox
	Target      string // path the session expects
}

type sessionPlan struct {
	Name        string
	KitSpec     []byte
	CreateArgs  []string
	ExecArgs    []string
	EnvFile     []byte
	StagedFiles []stagedFile
}

type planInput struct {
	Request     backend.Request
	Image       ImageConfig
	StoreSource storeSourceFunc
	KitDir      string
	EnvFilePath string
	StageDir    string
	TTY         bool
}

func buildPlan(in planInput) (sessionPlan, error) {
	req := in.Request
	name, err := sandboxName(req.Session.Name)
	if err != nil {
		return sessionPlan{}, err
	}
	argv, err := sessionArgv(req, in.Image)
	if err != nil {
		return sessionPlan{}, err
	}
	kit, err := kitSpec(req, argv)
	if err != nil {
		return sessionPlan{}, err
	}
	envFile, err := envFileContent(req.Env)
	if err != nil {
		return sessionPlan{}, err
	}
	workspaces, staged, err := workspaceArgs(req, in.StoreSource, in.StageDir)
	if err != nil {
		return sessionPlan{}, err
	}

	create := []string{"create", "--name", name, "--pull", "never", "--skills", "off", "--no-progress"}
	for _, port := range req.Ports {
		spec, err := portSpec(port)
		if err != nil {
			return sessionPlan{}, err
		}
		create = append(create, "--publish", spec)
	}
	create = append(create, in.KitDir)
	create = append(create, workspaces...)

	execArgs := []string{"exec", "-i"}
	if in.TTY {
		execArgs = append(execArgs, "-t")
	}
	execArgs = append(execArgs, "-u", sessionUser(req, in.Image))
	if req.WorkingDir != "" {
		execArgs = append(execArgs, "-w", req.WorkingDir)
	}
	execArgs = append(execArgs, "--env-file", in.EnvFilePath, name)
	execArgs = append(execArgs, argv...)

	return sessionPlan{
		Name:        name,
		KitSpec:     kit,
		CreateArgs:  create,
		ExecArgs:    execArgs,
		EnvFile:     envFile,
		StagedFiles: staged,
	}, nil
}

const maxSandboxNameLength = 63

// sandboxName maps an Enclave container name onto SBX's name rules: 2-63
// letters, digits, hyphens or periods, each dot-separated part starting and
// ending with a letter or digit. Enclave's managed names already comply, so
// the mapping is the identity for them and listing can match names directly.
// Names over the limit are rejected rather than truncated: a shortened name
// would no longer match the session name that stop and ps look up, and two
// long names could collide.
func sandboxName(name string) (string, error) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > maxSandboxNameLength {
		return "", fmt.Errorf("sbx backend: session name %q is %d characters, over the %d-character sandbox name limit; use a shorter --name or tool name", name, len(out), maxSandboxNameLength)
	}
	parts := strings.Split(out, ".")
	kept := parts[:0]
	for _, part := range parts {
		if part = strings.Trim(part, "-"); part != "" {
			kept = append(kept, part)
		}
	}
	out = strings.Join(kept, ".")
	if len(out) < 2 {
		return "", fmt.Errorf("sbx backend: session name %q cannot be mapped to a sandbox name", name)
	}
	return out, nil
}

// sessionArgv follows `docker run` semantics: an explicit entrypoint replaces
// the image's and drops the image's default command.
func sessionArgv(req backend.Request, img ImageConfig) ([]string, error) {
	var argv []string
	if len(req.Entrypoint) > 0 {
		argv = append(argv, req.Entrypoint...)
		argv = append(argv, req.Argv...)
	} else {
		argv = append(argv, img.Entrypoint...)
		if len(req.Argv) > 0 {
			argv = append(argv, req.Argv...)
		} else {
			argv = append(argv, img.Cmd...)
		}
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("sbx backend: image %s defines no entrypoint or command and the session supplies none", req.Image)
	}
	return argv, nil
}

func sessionUser(req backend.Request, img ImageConfig) string {
	if user := strings.TrimSpace(req.User); user != "" {
		return user
	}
	if user := strings.TrimSpace(img.User); user != "" {
		return user
	}
	return "root"
}

type kitDocument struct {
	SchemaVersion string     `json:"schemaVersion"`
	Kind          string     `json:"kind"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Sandbox       kitSandbox `json:"sandbox"`
}

type kitSandbox struct {
	Image      string   `json:"image"`
	Entrypoint []string `json:"entrypoint"`
}

// kitSpec renders a minimal schema-v2 sandbox kit whose image is the Enclave
// runtime image. Its entrypoint only matters if someone later runs the
// sandbox by hand; Enclave starts the session itself through exec.
func kitSpec(req backend.Request, argv []string) ([]byte, error) {
	doc := kitDocument{
		SchemaVersion: "2",
		Kind:          "sandbox",
		Name:          kitName(req.Session.Tool),
		Description:   "Enclave session workload",
		Sandbox:       kitSandbox{Image: req.Image, Entrypoint: argv},
	}
	return yaml.Marshal(doc)
}

func kitName(tool string) string {
	var b strings.Builder
	b.WriteString("enclave")
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool != "" {
		b.WriteByte('-')
	}
	for _, r := range tool {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// envFileContent renders the session env for `sbx exec --env-file`, which
// keeps values off the command line. The format is Docker's: one KEY=VALUE
// per line, without quoting, so values cannot span lines.
func envFileContent(env []backend.EnvVar) ([]byte, error) {
	var b strings.Builder
	for _, entry := range env {
		if entry.Name == "" {
			continue
		}
		if strings.ContainsAny(entry.Name, "= \t\r\n\x00") || strings.HasPrefix(entry.Name, "#") {
			return nil, fmt.Errorf("sbx backend: invalid environment variable name %q", entry.Name)
		}
		if strings.ContainsAny(entry.Value, "\r\n\x00") {
			return nil, fmt.Errorf("sbx backend: environment variable %s has a multi-line value, which sbx env files cannot carry", entry.Name)
		}
		b.WriteString(entry.Name)
		b.WriteByte('=')
		b.WriteString(entry.Value)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// workspaceArgs turns mounts and stores into `sbx create` workspace
// arguments. SBX requires the first one to be a writable directory bound at
// its own host path, so the project mount leads.
func workspaceArgs(req backend.Request, storeSource storeSourceFunc, stageDir string) ([]string, []stagedFile, error) {
	primary := primaryMountIndex(req)
	if primary < 0 {
		return nil, nil, fmt.Errorf("sbx backend: no writable project directory mount at its host path; sbx needs one as the primary workspace (read-only project mounts are not supported yet)")
	}
	args := []string{filepath.Clean(req.Mounts[primary].Source)}
	var staged []stagedFile
	for i, mount := range req.Mounts {
		if i == primary {
			continue
		}
		if mount.Type != "" && mount.Type != backend.MountTypeBind {
			return nil, nil, fmt.Errorf("sbx backend: unsupported mount type %q for %s", mount.Type, mount.ContainerPath)
		}
		if mount.CreateSourceDir {
			if err := os.MkdirAll(mount.Source, 0o700); err != nil {
				return nil, nil, fmt.Errorf("sbx backend: create cache directory %s: %w", mount.Source, err)
			}
		}
		info, err := os.Stat(mount.Source)
		if err != nil {
			return nil, nil, fmt.Errorf("sbx backend: inspect mount source %q: %w", mount.Source, err)
		}
		if !info.IsDir() && !mount.ReadOnly {
			if !info.Mode().IsRegular() {
				return nil, nil, fmt.Errorf("sbx backend: mount source %q must be a directory or regular file", mount.Source)
			}
			file, err := stageFile(mount, len(staged), stageDir)
			if err != nil {
				return nil, nil, err
			}
			staged = append(staged, file)
			continue
		}
		arg, err := workspaceArg(mount.Source, mount.ContainerPath, mount.ReadOnly)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, arg)
	}
	for _, store := range req.Stores {
		if strings.TrimSpace(store.ContainerPath) == "" {
			continue
		}
		source, err := storeSource(store.Key, store.Kind)
		if err != nil {
			return nil, nil, fmt.Errorf("sbx backend: resolve %s store: %w", store.Kind, err)
		}
		if err := os.MkdirAll(source, 0o700); err != nil {
			return nil, nil, fmt.Errorf("sbx backend: create %s store: %w", store.Kind, err)
		}
		arg, err := workspaceArg(source, store.ContainerPath, store.ReadOnly)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, arg)
	}
	if len(staged) > 0 {
		arg, err := workspaceArg(stageDir, guestFileStageDir, false)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, arg)
	}
	return args, staged, nil
}

func primaryMountIndex(req backend.Request) int {
	fallback := -1
	for i, mount := range req.Mounts {
		if mount.ReadOnly || (mount.Type != "" && mount.Type != backend.MountTypeBind) {
			continue
		}
		source := filepath.Clean(mount.Source)
		if source != path.Clean(mount.ContainerPath) {
			continue
		}
		if info, err := os.Stat(source); err != nil || !info.IsDir() {
			continue
		}
		for _, project := range []string{req.Session.Worktree, req.Session.RealWorktree} {
			if project != "" && filepath.Clean(project) == source {
				return i
			}
		}
		if fallback < 0 {
			fallback = i
		}
	}
	return fallback
}

// workspaceArg renders PATH:TARGET[:ro]. SBX splits on the last colon that
// precedes a slash, so neither part may contain a colon.
func workspaceArg(source string, target string, readOnly bool) (string, error) {
	source = filepath.Clean(source)
	if strings.Contains(source, ":") {
		return "", fmt.Errorf("sbx backend: mount source %q contains ':', which sbx workspace arguments cannot express", source)
	}
	if !path.IsAbs(target) || strings.Contains(target, ":") {
		return "", fmt.Errorf("sbx backend: mount target %q must be an absolute path without ':'", target)
	}
	arg := source + ":" + path.Clean(target)
	if readOnly {
		arg += ":ro"
	}
	return arg, nil
}

func stageFile(mount backend.Mount, index int, stageDir string) (stagedFile, error) {
	if !path.IsAbs(mount.ContainerPath) {
		return stagedFile{}, fmt.Errorf("sbx backend: file mount target %q must be absolute", mount.ContainerPath)
	}
	entry := fmt.Sprintf("%02d", index)
	base := filepath.Base(mount.Source)
	hostDir := filepath.Join(stageDir, entry)
	if err := os.MkdirAll(hostDir, 0o700); err != nil {
		return stagedFile{}, fmt.Errorf("sbx backend: create file staging directory: %w", err)
	}
	hostStaged := filepath.Join(hostDir, base)
	if err := copyFile(mount.Source, hostStaged); err != nil {
		return stagedFile{}, fmt.Errorf("sbx backend: stage file mount %s: %w", mount.ContainerPath, err)
	}
	return stagedFile{
		Source:      mount.Source,
		HostStaged:  hostStaged,
		GuestStaged: path.Join(guestFileStageDir, entry, base),
		Target:      path.Clean(mount.ContainerPath),
	}, nil
}

func copyFile(src string, dst string) error {
	data, err := os.ReadFile(src) // #nosec G304 -- src is a validated mount source.
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600) // #nosec G703 -- dst is generated under the session staging directory.
}

func portSpec(port backend.PortMapping) (string, error) {
	container := strings.TrimSpace(port.ContainerPort)
	if container == "" {
		return "", fmt.Errorf("sbx backend: port mapping without a container port")
	}
	spec := container
	if hostPort := strings.TrimSpace(port.HostPort); hostPort != "" {
		spec = hostPort + ":" + spec
		if hostIP := strings.TrimSpace(port.HostIP); hostIP != "" {
			spec = hostIP + ":" + spec
		}
	}
	if protocol := strings.ToLower(strings.TrimSpace(port.Protocol)); protocol != "" {
		spec += "/" + protocol
	}
	return spec, nil
}

// copyInScript runs as root before the session: it places each staged file at
// its target with the ownership of the target's directory.
const copyInScript = `set -e
while [ "$#" -gt 1 ]; do
  src=$1; dst=$2; shift 2
  dir=$(dirname "$dst")
  mkdir -p "$dir"
  cp "$src" "$dst"
  chown "$(stat -c %u:%g "$dir")" "$dst"
done`

// copyOutScript runs as root after the session and copies targets back.
const copyOutScript = `while [ "$#" -gt 1 ]; do
  if [ -f "$2" ]; then cp "$2" "$1"; fi
  shift 2
done
true`

// copyArgs builds an `sbx exec` that runs script with (staged, target) pairs.
func copyArgs(name string, script string, files []stagedFile) []string {
	args := []string{"exec", "-u", "root", name, "sh", "-c", script, "sh"}
	for _, file := range files {
		args = append(args, file.GuestStaged, file.Target)
	}
	return args
}
