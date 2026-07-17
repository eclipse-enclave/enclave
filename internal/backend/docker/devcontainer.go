// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"fmt"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/devcontainer"
	dockercmd "enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/mounts"
	"enclave/internal/util"
)

var blockedCapabilities = map[string]struct{}{
	"SYS_ADMIN":    {},
	"SYS_PTRACE":   {},
	"NET_ADMIN":    {},
	"DAC_OVERRIDE": {},
	"SYS_RAWIO":    {},
	"SYS_MODULE":   {},
	"ALL":          {},
}

type devcontainerMountParser func(string, string, string) (backend.Mount, error)

type devcontainerRunArgHandler struct {
	long  string
	short string
	apply func(b *Backend, arg string, value string, config *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig)
}

func (b *Backend) applyDevcontainerRunArgs(config *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig) {
	args := b.opts.DevcontainerRunArgs
	skipNext := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if skipNext {
			skipNext = false
			continue
		}
		switch {
		case arg == "--user" || arg == "-u":
			logx.Warnf("devcontainer runArgs user override ignored")
			skipNext = true
			continue
		case strings.HasPrefix(arg, "--user="):
			logx.Warnf("devcontainer runArgs user override ignored")
			continue
		case strings.HasPrefix(arg, "--net="):
			logx.Warnf("devcontainer runArgs --net %q is blocked: network mode is managed by enclave and cannot be overridden by devcontainer (use --allow-risky-devcontainer to override)", strings.TrimPrefix(arg, "--net="))
			continue
		case arg == "--privileged" || strings.HasPrefix(arg, "--privileged="):
			logx.Warnf("devcontainer runArgs flag %q is blocked for security; ignoring (use --allow-risky-devcontainer to override)", arg)
			continue
		case arg == "--init":
			init := true
			hostConfig.Init = &init
			continue
		}
		if b.applyDevcontainerValueArg(arg, args, &i, &skipNext, config, hostConfig) {
			continue
		}
		logx.Warnf("devcontainer runArgs flag %q is not supported; ignoring", arg)
	}
}

func (b *Backend) applyDevcontainerValueArg(arg string, args []string, i *int, skipNext *bool, config *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig) bool {
	for _, h := range devcontainerRunArgHandlers {
		if value, ok := consumeValue(arg, h.long, h.short, args, i, skipNext); ok {
			h.apply(b, arg, value, config, hostConfig)
			return true
		}
	}
	return false
}

var devcontainerRunArgHandlers = []devcontainerRunArgHandler{
	{long: "--env", short: "-e", apply: func(_ *Backend, arg, value string, config *dockercmd.ContainerConfig, _ *dockercmd.HostConfig) {
		if requireRunArgValue(arg, value) {
			config.Env = append(config.Env, value)
		}
	}},
	{long: "--env-file", apply: func(_ *Backend, arg, value string, config *dockercmd.ContainerConfig, _ *dockercmd.HostConfig) {
		if !requireRunArgValue(arg, value) {
			return
		}
		if envValues, err := util.ParseEnvFile(value); err != nil {
			logx.Warnf("Failed to read devcontainer env file %s: %v", value, err)
		} else {
			config.Env = append(config.Env, envValues...)
		}
	}},
	{long: "--volume", short: "-v", apply: func(b *Backend, arg, value string, _ *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig) {
		b.applyDevcontainerRunMount(hostConfig, arg, "--volume", value, parseVolumeSpec)
	}},
	{long: "--mount", apply: func(b *Backend, arg, value string, _ *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig) {
		b.applyDevcontainerRunMount(hostConfig, arg, "--mount", value, devcontainer.ParseMountSpec)
	}},
	{long: "--tmpfs", apply: func(_ *Backend, arg, value string, _ *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig) {
		if !requireRunArgValue(arg, value) {
			return
		}
		if hostConfig.Tmpfs == nil {
			hostConfig.Tmpfs = map[string]string{}
		}
		path, opts := splitTmpfs(value)
		if path == "" {
			logx.Warnf("Invalid devcontainer tmpfs spec %q", value)
			return
		}
		hostConfig.Tmpfs[path] = opts
	}},
	{long: "--security-opt", apply: func(_ *Backend, arg, value string, _ *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig) {
		if !requireRunArgValue(arg, value) {
			return
		}
		if isBlockedSecurityOpt(strings.ToLower(strings.TrimSpace(value))) {
			logx.Warnf("devcontainer runArgs --security-opt %q is blocked for security; ignoring (use --allow-risky-devcontainer to override)", value)
			return
		}
		hostConfig.SecurityOpt = append(hostConfig.SecurityOpt, value)
	}},
	{long: "--cap-add", apply: func(_ *Backend, arg, value string, _ *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig) {
		if !requireRunArgValue(arg, value) {
			return
		}
		normalized := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(value)), "CAP_")
		if _, blocked := blockedCapabilities[normalized]; blocked {
			logx.Warnf("devcontainer runArgs --cap-add %q is blocked for security; ignoring (use --allow-risky-devcontainer to override)", value)
			return
		}
		hostConfig.CapAdd = append(hostConfig.CapAdd, value)
	}},
	{long: "--cap-drop", apply: func(_ *Backend, arg, value string, _ *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig) {
		if requireRunArgValue(arg, value) {
			hostConfig.CapDrop = append(hostConfig.CapDrop, value)
		}
	}},
	{long: "--network", short: "--net", apply: func(_ *Backend, arg, value string, _ *dockercmd.ContainerConfig, _ *dockercmd.HostConfig) {
		if !requireRunArgValue(arg, value) {
			return
		}
		flag := "--network"
		if arg == "--net" {
			flag = "--net"
		}
		logx.Warnf("devcontainer runArgs %s %q is blocked: network mode is managed by enclave and cannot be overridden by devcontainer (use --allow-risky-devcontainer to override)", flag, value)
	}},
	{long: "--add-host", apply: func(_ *Backend, arg, value string, _ *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig) {
		if requireRunArgValue(arg, value) {
			hostConfig.ExtraHosts = append(hostConfig.ExtraHosts, value)
		}
	}},
	{long: "--hostname", apply: func(_ *Backend, arg, value string, config *dockercmd.ContainerConfig, _ *dockercmd.HostConfig) {
		if requireRunArgValue(arg, value) {
			config.Hostname = value
		}
	}},
	{long: "--workdir", short: "-w", apply: func(_ *Backend, arg, value string, config *dockercmd.ContainerConfig, _ *dockercmd.HostConfig) {
		if requireRunArgValue(arg, value) {
			config.WorkingDir = value
		}
	}},
	{long: "--entrypoint", apply: func(_ *Backend, _ string, _ string, _ *dockercmd.ContainerConfig, _ *dockercmd.HostConfig) {
		logx.Warnf(`devcontainer runArgs flag "--entrypoint" is blocked for security; ignoring (use --allow-risky-devcontainer to override)`)
	}},
}

func (b *Backend) applyDevcontainerRunMount(hostConfig *dockercmd.HostConfig, arg string, label string, value string, parse devcontainerMountParser) {
	if !requireRunArgValue(arg, value) {
		return
	}
	parsed, err := parse(value, b.projectDir(), b.opts.Host.Home)
	if err != nil {
		logx.Warnf("Invalid devcontainer %s spec %q: %v", strings.TrimPrefix(label, "--"), value, err)
		return
	}
	if devcontainer.IsBlockedMount(parsed, b.projectDir()) {
		logx.Warnf("devcontainer runArgs %s source %q is blocked for security; ignoring (only paths inside the project directory may be bind-mounted via devcontainer config)", label, parsed.Source)
		return
	}
	mounts.ApplyProjectMountMode(&parsed, b.opts.ProjectMount)
	hostConfig.Mounts = append(hostConfig.Mounts, dockerMount(parsed))
}

func (b *Backend) projectDir() string {
	return strings.TrimSpace(b.opts.ProjectDir)
}

func requireRunArgValue(arg, value string) bool {
	if value == "" {
		logx.Warnf("devcontainer runArgs missing value for %s", arg)
		return false
	}
	return true
}

func parseVolumeSpec(spec string, projectDir string, home string) (backend.Mount, error) {
	if spec == "" {
		return backend.Mount{}, fmt.Errorf("volume spec is empty")
	}
	parts := strings.Split(spec, ":")
	if len(parts) < 2 {
		return backend.Mount{}, fmt.Errorf("volume spec must include source and target")
	}
	source := strings.TrimSpace(parts[0])
	target := strings.TrimSpace(parts[1])
	if source == "" || target == "" {
		return backend.Mount{}, fmt.Errorf("volume spec missing source or target")
	}
	opts := ""
	if len(parts) > 2 {
		opts = strings.Join(parts[2:], ":")
	}
	readOnly := hasMountFlag(opts, "ro")
	source = util.ExpandTilde(source, home)
	if filepath.IsAbs(source) || strings.HasPrefix(source, ".") || strings.Contains(source, "/") {
		if !filepath.IsAbs(source) {
			source = filepath.Join(projectDir, source)
		}
		return backend.Mount{Type: backend.MountTypeBind, Source: source, ContainerPath: target, ReadOnly: readOnly}, nil
	}
	return backend.Mount{Type: backend.MountTypeVolume, Source: source, ContainerPath: target, ReadOnly: readOnly}, nil
}

// dockerMount converts a neutral backend.Mount into the Docker CLI mount type.
func dockerMount(m backend.Mount) dockercmd.Mount {
	return dockercmd.Mount{Type: dockercmd.MountType(m.Type), Source: m.Source, Target: m.ContainerPath, ReadOnly: m.ReadOnly}
}

func splitTmpfs(value string) (string, string) {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return strings.TrimSpace(parts[0]), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func consumeValue(arg string, long string, short string, args []string, index *int, skipNext *bool) (string, bool) {
	if arg == long || (short != "" && arg == short) {
		if *index+1 >= len(args) {
			return "", true
		}
		*skipNext = true
		return args[*index+1], true
	}
	prefix := long + "="
	if strings.HasPrefix(arg, prefix) {
		return strings.TrimPrefix(arg, prefix), true
	}
	return "", false
}

func hasMountFlag(options string, flag string) bool {
	if options == "" {
		return false
	}
	for _, opt := range strings.Split(options, ",") {
		if strings.TrimSpace(opt) == flag {
			return true
		}
	}
	return false
}

func isBlockedSecurityOpt(norm string) bool {
	switch norm {
	case "seccomp=unconfined",
		"apparmor=unconfined",
		"no-new-privileges=false",
		"no-new-privileges=0",
		"label=disable":
		return true
	}
	return false
}
