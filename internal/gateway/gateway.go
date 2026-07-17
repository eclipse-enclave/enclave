// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/config"
	"enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/network"
	"enclave/internal/util"
)

func imageName(profile model.Profile) string {
	return model.GatewayImagePrefix + profile.Name + ":" + model.GatewayImageTagLatest
}

const gatewayNetworkLogPath = "/var/log/enclave/network.log"

const (
	gatewayReadyMarker       = "Gateway ready"
	gatewayReadyTimeout      = 15 * time.Second
	gatewayReadyPollInterval = 100 * time.Millisecond
)

func calculateAllowlistHash(allowlistPath string, allowlistsDir string) (string, error) {
	if _, err := os.Stat(allowlistPath); err != nil {
		return "none", nil
	}

	files := []string{allowlistPath}
	// #nosec G304 -- allowlistPath is selected from trusted built-in or resolved override files.
	data, err := os.ReadFile(allowlistPath)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "conf-file=") {
			includePath := strings.TrimPrefix(line, "conf-file=")
			fullPath, ok := network.ResolveAllowlistIncludePath(allowlistsDir, includePath)
			if !ok {
				continue
			}
			if _, err := os.Stat(fullPath); err == nil { // #nosec G703 -- fullPath is constrained to the allowlists directory above.
				files = append(files, fullPath)
			}
		}
	}

	var combined strings.Builder
	for _, file := range files {
		fileHash, err := util.HashFile(file)
		if err != nil {
			return "", err
		}
		combined.WriteString(fileHash)
		combined.WriteString("\n")
	}

	return util.HashString(combined.String()), nil
}

func calculateGatewayProxyBuildInputsHash(appRoot string) (string, error) {
	var combined strings.Builder
	for _, relPath := range gatewayProxyBuildInputs {
		fullPath := filepath.Join(appRoot, filepath.FromSlash(relPath))
		if err := appendGatewayProxyBuildInputHash(&combined, appRoot, fullPath); err != nil {
			return "", fmt.Errorf("hash gateway build input %s: %w", relPath, err)
		}
	}
	return util.HashString(combined.String()), nil
}

func appendGatewayProxyBuildInputHash(combined *strings.Builder, appRoot string, fullPath string) error {
	info, err := os.Stat(fullPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return appendGatewayFileHash(combined, appRoot, fullPath)
	}
	return filepath.WalkDir(fullPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		return appendGatewayFileHash(combined, appRoot, path)
	})
}

func appendGatewayFileHash(combined *strings.Builder, appRoot string, fullPath string) error {
	if !util.PathWithin(appRoot, fullPath) {
		return fmt.Errorf("gateway build input %s is outside app root %s", fullPath, appRoot)
	}
	relPath, err := filepath.Rel(appRoot, fullPath)
	if err != nil {
		return err
	}
	fileHash, err := util.HashFile(fullPath)
	if err != nil {
		return err
	}
	combined.WriteString(filepath.ToSlash(relPath))
	combined.WriteString(":")
	combined.WriteString(fileHash)
	combined.WriteString("\n")
	return nil
}

func needsRebuild(paths model.Paths, profile model.Profile, allowlistPath string) (needsRebuild bool, buildHash string, err error) {
	dockerfileHash, err := util.HashFile(paths.GatewayDockerfile)
	if err != nil {
		return false, "", err
	}
	entrypointHash, err := util.HashFile(paths.GatewayEntrypoint)
	if err != nil {
		return false, "", err
	}
	netHelperHash, err := util.HashFile(gatewayNetHelperPath(paths.AppRoot))
	if err != nil {
		return false, "", err
	}
	allowlistHash, err := calculateAllowlistHash(allowlistPath, paths.AllowlistsDir)
	if err != nil {
		return false, "", err
	}
	proxySourceHash, err := calculateGatewayProxyBuildInputsHash(paths.AppRoot)
	if err != nil {
		return false, "", err
	}
	buildHash = fmt.Sprintf("%s-%s-%s-%s-%s", dockerfileHash, entrypointHash, netHelperHash, allowlistHash, proxySourceHash)
	image := imageName(profile)

	inspect, err := docker.ImageInspect(context.Background(), image)
	if err != nil {
		return true, buildHash, nil
	}
	storedHash := ""
	if inspect.Config != nil && inspect.Config.Labels != nil {
		storedHash = inspect.Config.Labels[model.GatewayLabelHash]
	}

	return storedHash != buildHash, buildHash, nil
}

func buildGatewayImage(paths model.Paths, profile model.Profile, allowlistPath string, buildHash string) error {
	logx.Infof("Building gateway image for %s.", profile.Name)

	contextDir, allowlistRel, cleanup, err := prepareGatewayContext(paths, allowlistPath)
	if err != nil {
		return err
	}
	defer cleanup()

	dockerfilePath := paths.GatewayDockerfile
	if contextDir != paths.AppRoot {
		dockerfilePath = filepath.Join(contextDir, filepath.Base(paths.GatewayDockerfile))
	}

	req := docker.BuildRequest{
		ContextDir: contextDir,
		Dockerfile: dockerfilePath,
		Tags:       []string{imageName(profile)},
		BuildArgs: map[string]string{
			"GATEWAY_ALLOWLIST_FILENAME": allowlistRel,
		},
		Labels: map[string]string{
			model.GatewayLabelHash:  buildHash,
			model.GatewayLabelAgent: profile.Name,
		},
	}
	if err := docker.Build(context.Background(), req, io.Discard); err != nil {
		// Some Docker BuildKit setups fail DNS resolution in the default build
		// network for Alpine index fetches. Retry once with host build network.
		req.NetworkMode = "host"
		if retryErr := docker.Build(context.Background(), req, io.Discard); retryErr != nil {
			return fmt.Errorf("failed to build gateway image: %w (retry with host build network failed: %v)", err, retryErr)
		}
		logx.Warnf("Gateway build failed on default build network; retry with host build network succeeded")
	}

	logx.Successf("Gateway image built")
	return nil
}

func prepareGatewayContext(paths model.Paths, allowlistPath string) (contextDir string, allowlistRel string, cleanup func(), err error) {
	if rel, ok := relativePathWithin(paths.AppRoot, allowlistPath); ok {
		return paths.AppRoot, rel, func() {}, nil
	}

	stagingDir, err := os.MkdirTemp("", "enclave-gateway-context-*")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(stagingDir) } // #nosec G301 -- stagingDir is created via os.MkdirTemp.
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	dockerfileDst := filepath.Join(stagingDir, filepath.Base(paths.GatewayDockerfile))
	if err := util.CopyTree(paths.GatewayDockerfile, dockerfileDst, copyGatewayFile); err != nil {
		return "", "", nil, fmt.Errorf("copy gateway dockerfile: %w", err)
	}
	entrypointDst := filepath.Join(stagingDir, filepath.Base(paths.GatewayEntrypoint))
	if err := util.CopyTree(paths.GatewayEntrypoint, entrypointDst, copyGatewayFile); err != nil {
		return "", "", nil, fmt.Errorf("copy gateway entrypoint: %w", err)
	}
	if err := copyGatewayNetHelper(paths.AppRoot, stagingDir); err != nil {
		return "", "", nil, err
	}
	if err := copyGatewayProxyBuildInputs(paths.AppRoot, stagingDir); err != nil {
		return "", "", nil, err
	}

	allowlistsDst := filepath.Join(stagingDir, "runtime-assets", "gateway-allowlists")
	if err := util.CopyTree(paths.AllowlistsDir, allowlistsDst, copyGatewayFile); err != nil {
		return "", "", nil, fmt.Errorf("copy built-in allowlists: %w", err)
	}

	allowlistRel = filepath.ToSlash(filepath.Join("runtime-assets", "gateway-allowlists", "__user_allowlist.conf"))
	userAllowlistDst := filepath.Join(stagingDir, filepath.FromSlash(allowlistRel))
	if err := util.CopyTree(allowlistPath, userAllowlistDst, copyGatewayFile); err != nil {
		return "", "", nil, fmt.Errorf("copy user allowlist: %w", err)
	}

	return stagingDir, allowlistRel, cleanup, nil
}

func gatewayNetHelperPath(appRoot string) string {
	return filepath.Join(appRoot, "runtime-assets", "net.sh")
}

func copyGatewayNetHelper(appRoot string, stagingDir string) error {
	src := gatewayNetHelperPath(appRoot)
	dst := filepath.Join(stagingDir, "runtime-assets", "net.sh")
	if err := util.CopyTree(src, dst, copyGatewayFile); err != nil {
		return fmt.Errorf("copy gateway net helper: %w", err)
	}
	return nil
}

func copyGatewayProxyBuildInputs(appRoot string, stagingDir string) error {
	for _, relPath := range gatewayProxyBuildInputs {
		src := filepath.Join(appRoot, filepath.FromSlash(relPath))
		dst := filepath.Join(stagingDir, filepath.FromSlash(relPath))
		if err := util.CopyTree(src, dst, copyGatewayFile); err != nil {
			return fmt.Errorf("copy gateway build input %s: %w", relPath, err)
		}
	}
	return nil
}

func copyGatewayFile(src string, dst string, mode os.FileMode) error {
	// #nosec G304 -- src is a trusted gateway build input resolved by the app.
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode) // #nosec G703 -- dst is a trusted gateway staging path.
}

func relativePathWithin(base string, target string) (string, bool) {
	if !util.PathWithin(base, target) {
		return "", false
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(filepath.Clean(rel)), true
}

func gatewayContainerName(containerName string) string {
	return containerName + model.GatewayContainerSuffix
}

type StartConfig struct {
	Paths             model.Paths
	Profile           model.Profile
	AllowlistPath     string
	ContainerName     string
	ForceRebuild      bool
	NoRebuild         bool
	NetworkLogMode    string
	NetworkLogPath    string
	GatewayConfigDir  string
	PortBindings      docker.PortMap
	ExposedPorts      docker.PortSet
	LoopbackPorts     []string
	IdeBridgePorts    []string
	Home              string
	ProjectDir        string
	WorkspaceID       string
	ProjectHash       string
	SecretReleaseFile string
	TLSRootDir        string
}

func appendReadOnlyMount(mounts []docker.Mount, hostPath string, containerPath string) []docker.Mount {
	return append(mounts, docker.Mount{
		Type:     docker.MountTypeBind,
		Source:   hostPath,
		Target:   containerPath,
		ReadOnly: true,
	})
}

// StartResult holds the output of a successful gateway start.
type StartResult struct {
	ContainerName string
	TempFiles     []string
}

func Start(cfg StartConfig) (StartResult, error) {
	var empty StartResult
	if err := validateStartConfig(cfg); err != nil {
		return empty, err
	}
	if cfg.NoRebuild {
		logx.Warnf("Skipping gateway image build due to --no-rebuild.")
		if err := ensureExistingGatewayImageWith(cfg.Profile, docker.ImageExists); err != nil {
			return empty, err
		}
	} else {
		needs, buildHash, err := needsRebuild(cfg.Paths, cfg.Profile, cfg.AllowlistPath)
		if err != nil {
			return empty, err
		}
		if cfg.ForceRebuild || needs {
			if err := buildGatewayImage(cfg.Paths, cfg.Profile, cfg.AllowlistPath, buildHash); err != nil {
				return empty, err
			}
		}
	}

	gatewayContainer := gatewayContainerName(cfg.ContainerName)
	if err := docker.ContainerRemove(context.Background(), gatewayContainer, true, true); err != nil && !docker.IsNotFound(err) {
		logx.Warnf("Failed to remove existing gateway container %s: %v", gatewayContainer, err)
	}

	if cfg.NetworkLogPath != "" {
		logDir := filepath.Dir(cfg.NetworkLogPath)
		if err := os.MkdirAll(logDir, 0o750); err != nil {
			return empty, fmt.Errorf("failed to create network log dir: %w", err)
		}
		if _, err := os.Stat(cfg.NetworkLogPath); err != nil {
			if !os.IsNotExist(err) {
				return empty, fmt.Errorf("failed to stat network log path: %w", err)
			}
			file, err := os.OpenFile(cfg.NetworkLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return empty, fmt.Errorf("failed to create network log file: %w", err)
			}
			if err := file.Close(); err != nil {
				return empty, fmt.Errorf("failed to close network log file: %w", err)
			}
		}
	}

	mounts := []docker.Mount{}
	if overridePath, scope := config.GatewayAllowlistOverridePath(cfg.Profile, cfg.Home, cfg.ProjectHash); overridePath != "" {
		mounts = appendReadOnlyMount(mounts, overridePath, model.GatewayAllowlistOverridePath)
		logx.Infof("Using %s gateway allowlist override", scope)
	}
	if cfg.GatewayConfigDir != "" {
		mounts = appendReadOnlyMount(mounts, cfg.GatewayConfigDir, model.GatewayConfigDir)
	}

	if cfg.NetworkLogPath != "" {
		mounts = append(mounts, docker.Mount{
			Type:   docker.MountTypeBind,
			Source: cfg.NetworkLogPath,
			Target: gatewayNetworkLogPath,
		})
	}
	if cfg.SecretReleaseFile != "" {
		mounts = appendReadOnlyMount(mounts, cfg.SecretReleaseFile, model.GatewaySecretReleasePath)
	}
	if cfg.TLSRootDir != "" {
		caCertPath := filepath.Join(cfg.TLSRootDir, "ca.crt")
		caKeyPath := filepath.Join(cfg.TLSRootDir, "ca.key")
		hostsPath := filepath.Join(cfg.TLSRootDir, "hosts")
		if err := os.MkdirAll(hostsPath, 0o700); err != nil {
			return empty, fmt.Errorf("failed to create gateway TLS hosts dir: %w", err)
		}
		mounts = appendReadOnlyMount(mounts, caCertPath, model.GatewayTLSCACertPath)
		mounts = appendReadOnlyMount(mounts, caKeyPath, model.GatewayTLSCAKeyPath)
		mounts = append(mounts, docker.Mount{
			Type:   docker.MountTypeBind,
			Source: hostsPath,
			Target: model.GatewayTLSHostsPath,
		})
	}

	env := []string{}
	if cfg.NetworkLogPath != "" {
		env = append(env, model.EnvNetworkLogFile+"="+gatewayNetworkLogPath)
	}
	if strings.TrimSpace(cfg.NetworkLogMode) != "" {
		env = append(env, model.EnvNetworkLogMode+"="+cfg.NetworkLogMode)
	}
	if cfg.GatewayConfigDir != "" {
		env = append(env, model.EnvGatewayConfigDir+"="+model.GatewayConfigDir)
	}
	if len(cfg.LoopbackPorts) > 0 {
		env = append(env, model.EnvLoopbackPorts+"="+strings.Join(cfg.LoopbackPorts, ","))
	}
	if len(cfg.IdeBridgePorts) > 0 {
		env = append(env, model.EnvIdeBridgePorts+"="+strings.Join(cfg.IdeBridgePorts, ","))
	}
	if cfg.SecretReleaseFile != "" {
		env = append(env, model.EnvSecretReleaseFile+"="+model.GatewaySecretReleasePath)
	}
	if cfg.TLSRootDir != "" {
		env = append(env, model.EnvGatewayTLSRoot+"="+model.GatewayTLSRootPath)
	}
	config := &docker.ContainerConfig{
		Image:        imageName(cfg.Profile),
		Env:          env,
		ExposedPorts: cfg.ExposedPorts,
		Labels:       gatewayLabels(cfg),
	}
	var extraHosts []string
	if len(cfg.IdeBridgePorts) > 0 {
		extraHosts = append(extraHosts, "host.docker.internal:host-gateway")
	}
	var sysctls map[string]string
	if len(cfg.IdeBridgePorts) > 0 {
		sysctls = map[string]string{
			"net.ipv4.conf.all.route_localnet": "1",
		}
	}
	binds, remainingMounts := docker.SplitMountsForSELinux(mounts)
	hostConfig := &docker.HostConfig{
		AutoRemove:   true,
		CapAdd:       []string{"NET_ADMIN", "NET_RAW"},
		Sysctls:      sysctls,
		Binds:        binds,
		Mounts:       remainingMounts,
		PortBindings: cfg.PortBindings,
		ExtraHosts:   extraHosts,
	}

	startedAt := time.Now().UTC()
	if _, err := docker.RunDetached(context.Background(), config, hostConfig, gatewayContainer); err != nil {
		return empty, fmt.Errorf("failed to start gateway container: %w", err)
	}

	if err := waitForGatewayReady(gatewayContainer, startedAt); err != nil {
		return empty, err
	}

	return StartResult{
		ContainerName: gatewayContainer,
	}, nil
}

func validateStartConfig(cfg StartConfig) error {
	if cfg.GatewayConfigDir != "" && !util.PathExists(cfg.GatewayConfigDir) {
		return fmt.Errorf("gateway config dir does not exist: %s", cfg.GatewayConfigDir)
	}
	if mode := strings.TrimSpace(cfg.NetworkLogMode); mode != "" && mode != model.NetworkLogCoarse && mode != model.NetworkLogRequests {
		return fmt.Errorf("invalid network log mode: %s", cfg.NetworkLogMode)
	}

	if cfg.NetworkLogPath != "" {
		info, err := os.Stat(cfg.NetworkLogPath)
		if err == nil && info.IsDir() {
			return fmt.Errorf("network log path is a directory: %s", cfg.NetworkLogPath)
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to stat network log path: %w", err)
		}
	}

	if cfg.TLSRootDir == "" {
		return nil
	}

	caCertPath := filepath.Join(cfg.TLSRootDir, "ca.crt")
	if _, err := os.Stat(caCertPath); err != nil {
		return fmt.Errorf("gateway CA cert missing at %s: %w", caCertPath, err)
	}
	caKeyPath := filepath.Join(cfg.TLSRootDir, "ca.key")
	if _, err := os.Stat(caKeyPath); err != nil {
		return fmt.Errorf("gateway CA key missing at %s: %w", caKeyPath, err)
	}
	return nil
}

func waitForGatewayReady(containerID string, since time.Time) error {
	deadline := time.Now().Add(gatewayReadyTimeout)
	for time.Now().Before(deadline) {
		logs, err := docker.ContainerLogsSince(context.Background(), containerID, since)
		if err == nil && HasLogLine(logs, gatewayReadyMarker) {
			return nil
		}
		if err != nil && !docker.IsNotFound(err) {
			return fmt.Errorf("read gateway startup logs: %w", err)
		}

		inspect, inspectErr := docker.ContainerInspect(context.Background(), containerID)
		if inspectErr != nil {
			if docker.IsNotFound(inspectErr) {
				return fmt.Errorf("gateway exited during startup")
			}
			return fmt.Errorf("inspect gateway startup state: %w", inspectErr)
		}
		if inspect.State == nil || !inspect.State.Running {
			exitReason := "exited during startup"
			if inspect.State != nil {
				if stateErr := strings.TrimSpace(inspect.State.Error); stateErr != "" {
					exitReason = stateErr
				} else if status := strings.TrimSpace(inspect.State.Status); status != "" {
					exitReason = status
				}
			}
			return fmt.Errorf("gateway exited during startup (%s)", exitReason)
		}

		time.Sleep(gatewayReadyPollInterval)
	}
	return fmt.Errorf("timed out waiting for gateway readiness")
}

// HasLogLine reports whether logs contain an exact line (or prefixed log line)
// ending with marker text.
func HasLogLine(logs string, marker string) bool {
	for _, line := range strings.Split(logs, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == marker || strings.HasSuffix(trimmed, marker) {
			return true
		}
	}
	return false
}

func gatewayLabels(cfg StartConfig) map[string]string {
	labels := map[string]string{
		model.GatewayLabelManaged:   "true",
		model.GatewayLabelAgent:     cfg.Profile.Name,
		model.GatewayLabelContainer: cfg.ContainerName,
	}
	if cfg.ProjectHash != "" {
		labels[model.GatewayLabelProjectHash] = cfg.ProjectHash
	}
	if cfg.ProjectDir != "" {
		labels[model.GatewayLabelProjectDir] = cfg.ProjectDir
	}
	workspaceID := util.WorkspaceIdentityHash(cfg.WorkspaceID, cfg.ProjectDir)
	if workspaceID != "" {
		labels[model.GatewayLabelWorkspaceHash] = workspaceID
	}
	return labels
}

func ensureExistingGatewayImageWith(profile model.Profile, exists func(context.Context, string) (bool, error)) error {
	image := imageName(profile)
	ok, err := exists(context.Background(), image)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return fmt.Errorf("gateway image %q does not exist locally; rerun without --no-rebuild, pass --rebuild, or use --allow-all-network to bypass the gateway", image)
}

func Stop(containerName string) {
	container := gatewayContainerName(containerName)
	timeout := 3 * time.Second
	if err := docker.ContainerStop(context.Background(), container, &timeout); err != nil {
		logx.Warnf("Failed to stop gateway container %s: %v", container, err)
	}
}
