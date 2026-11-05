// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"enclave/internal/config"
	"enclave/internal/docker"
	"enclave/internal/gateway/oauthbroker"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/netcapture"
	"enclave/internal/netlog"
	"enclave/internal/network"
	"enclave/internal/protectedauth"
	"enclave/internal/util"
)

func imageName(profile model.Profile) string {
	return model.GatewayImagePrefix + profile.Name + ":" + model.GatewayImageTagLatest
}

const gatewayNetworkLogPath = "/var/log/enclave/network.log"

const gatewayNetworkCaptureDir = "/var/log/enclave/capture"

const (
	gatewayReadyMarker       = "Gateway ready"
	gatewayReadyTimeout      = 15 * time.Second
	gatewayReadyPollInterval = 100 * time.Millisecond
)

var (
	startContainerInspect = docker.ContainerInspect
	startContainerRemove  = docker.ContainerRemove
	startRunDetached      = docker.RunDetached
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

// gatewayBuildErrorLines is how many trailing lines of engine output a failed
// gateway build reports; the build otherwise runs silently.
const gatewayBuildErrorLines = 20

// buildOutputTail renders the last n non-empty lines of build output as an
// indented block for an error message, or nothing when there is no output.
func buildOutputTail(output string, n int) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "\nbuild output:\n  " + strings.Join(lines, "\n  ")
}

func buildGatewayImage(ctx context.Context, paths model.Paths, profile model.Profile, allowlistPath string, buildHash string) error {
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
	var output bytes.Buffer
	if err := docker.Build(ctx, req, &output); err != nil {
		// Some Docker BuildKit setups fail DNS resolution in the default build
		// network for Alpine index fetches. Retry once with host build network.
		req.NetworkMode = "host"
		var retryOutput bytes.Buffer
		if retryErr := docker.Build(ctx, req, &retryOutput); retryErr != nil {
			return fmt.Errorf("failed to build gateway image: %w (retry with host build network failed: %v)%s",
				err, retryErr, buildOutputTail(retryOutput.String(), gatewayBuildErrorLines))
		}
		logx.Warnf("Gateway build failed on default build network; retry with host build network succeeded")
	}

	logx.Successf("Gateway image built")
	return nil
}

func coordinateGatewayImageBuild(home string, image string, forceRebuild bool, resolveBuildPlan func() (bool, string, error), executeBuild func(string) error) error {
	release, err := config.AcquireImageBuildLock(home, image)
	if err != nil {
		return err
	}
	defer release()
	needs, buildHash, err := resolveBuildPlan()
	if err != nil {
		return err
	}
	if !forceRebuild && !needs {
		return nil
	}
	return executeBuild(buildHash)
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

// ContainerName returns the name of the gateway sidecar for a session container.
func ContainerName(containerName string) string {
	return containerName + model.GatewayContainerSuffix
}

// OAuthConfig contains host-owned broker mounts. Only ViewDir is shared with
// the workload; the other paths must never appear in its mount namespace.
type OAuthConfig struct {
	StoreDir    string
	RuntimeDir  string
	ViewDir     string
	LockPath    string
	Identity    string
	Incarnation string
	Features    []FeatureOAuthConfig
	// Instances are self-hosted hosts the broker mediates like a provider.
	Instances []model.ProviderInstance
}

type FeatureOAuthConfig struct {
	Name       string
	StoreDir   string
	RuntimeDir string
	ViewDir    string
	LockPath   string
	Identity   string
}

type StartConfig struct {
	OAuth             *OAuthConfig
	Paths             model.Paths
	Profile           model.Profile
	AllowlistPath     string
	ContainerName     string
	ForceRebuild      bool
	NoRebuild         bool
	NetworkLogMode    string
	NetworkLogPath    string
	NetworkCaptureDir string
	GatewayConfigDir  string
	NetworkName       string
	PortBindings      docker.PortMap
	ExposedPorts      docker.PortSet
	LoopbackPorts     []string
	IdeBridgePorts    []string
	Home              string
	ProjectDir        string
	WorkspaceID       string
	ProjectHash       string
	SecretReleaseFile string
	// ImagePrepared skips the image step a caller already ran with
	// PrepareImage.
	ImagePrepared bool
	TLSRootDir    string
}

func appendReadOnlyMount(mounts []docker.Mount, hostPath string, containerPath string) []docker.Mount {
	return append(mounts, docker.Mount{
		Type:     docker.MountTypeBind,
		Source:   hostPath,
		Target:   containerPath,
		ReadOnly: true,
	})
}

// Only the CA is shared with the host. A host-side leaf cache cannot be shared
// safely because container UIDs map differently under docker and rootless podman.
func appendTLSCAMounts(mounts []docker.Mount, tlsRootDir string) []docker.Mount {
	if tlsRootDir == "" {
		return mounts
	}
	mounts = appendReadOnlyMount(mounts, filepath.Join(tlsRootDir, "ca.crt"), model.GatewayTLSCACertPath)
	return appendReadOnlyMount(mounts, filepath.Join(tlsRootDir, "ca.key"), model.GatewayTLSCAKeyPath)
}

// StartResult holds the output of a successful gateway start.
type StartResult struct {
	ContainerName string
	ContainerID   string
	TempFiles     []string
}

// StaleResult reports what ReconcileStale found for a session's gateway.
type StaleResult struct {
	// Exists is set when a container with the gateway name exists.
	Exists bool
	// Owned is set when that container carries this session's gateway labels.
	Owned bool
	// Removed is set when the gateway was removed.
	Removed bool
}

// PrepareImage builds the gateway image when it is missing or stale, or
// checks that it exists under --no-rebuild. Callers run it before taking locks
// other sessions wait on, then start with ImagePrepared.
func PrepareImage(ctx context.Context, cfg StartConfig) error {
	if cfg.NoRebuild {
		logx.Warnf("Skipping gateway image build (--no-rebuild or %s=1).", model.EnvNoRebuild)
		return ensureExistingGatewayImageWith(cfg.Profile, docker.ImageExists)
	}
	needs, _, err := needsRebuild(cfg.Paths, cfg.Profile, cfg.AllowlistPath)
	if err != nil {
		return err
	}
	if !cfg.ForceRebuild && !needs {
		return nil
	}
	resolveBuildPlan := func() (bool, string, error) {
		return needsRebuild(cfg.Paths, cfg.Profile, cfg.AllowlistPath)
	}
	executeBuild := func(buildHash string) error {
		return buildGatewayImage(ctx, cfg.Paths, cfg.Profile, cfg.AllowlistPath, buildHash)
	}
	return coordinateGatewayImageBuild(cfg.Home, imageName(cfg.Profile), cfg.ForceRebuild, resolveBuildPlan, executeBuild)
}

// Start builds the gateway image if needed, then runs the gateway sidecar and
// waits for it to report readiness. Cancelling ctx aborts the start and
// removes a sidecar that was already created. The caller holds the
// session-start lock until the session container is running.
func Start(ctx context.Context, cfg StartConfig) (StartResult, error) {
	var empty StartResult
	if err := validateStartConfig(cfg); err != nil {
		return empty, err
	}
	if !cfg.ImagePrepared {
		if err := PrepareImage(ctx, cfg); err != nil {
			return empty, err
		}
	}

	gatewayContainer := ContainerName(cfg.ContainerName)
	reconciled, err := ReconcileStale(ctx, cfg.ContainerName, cfg.ProjectHash)
	if err != nil {
		return empty, fmt.Errorf("inspect existing gateway container %s: %w", gatewayContainer, err)
	}
	if reconciled.Exists && !reconciled.Removed {
		if reconciled.Owned {
			return empty, fmt.Errorf("gateway container %q is already running or starting for session %s; run 'enclave stop %s' and retry", gatewayContainer, cfg.ContainerName, cfg.ContainerName)
		}
		return empty, fmt.Errorf("gateway container %q already exists and is not owned by this enclave session", gatewayContainer)
	}

	if cfg.NetworkLogPath != "" {
		if err := prepareNetworkLog(cfg); err != nil {
			return empty, err
		}
	}
	captureDir := ""
	if cfg.NetworkCaptureDir != "" {
		if captureDir, err = prepareNetworkCapture(cfg); err != nil {
			return empty, err
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
	if captureDir != "" {
		mounts = append(mounts, docker.Mount{
			Type:   docker.MountTypeBind,
			Source: captureDir,
			Target: gatewayNetworkCaptureDir,
		})
	}
	if cfg.SecretReleaseFile != "" {
		mounts = appendReadOnlyMount(mounts, cfg.SecretReleaseFile, model.GatewaySecretReleasePath)
	}
	mounts = appendTLSCAMounts(mounts, cfg.TLSRootDir)
	if cfg.OAuth != nil {
		mounts = append(mounts, []docker.Mount{
			{Type: docker.MountTypeBind, Source: cfg.OAuth.StoreDir, Target: model.GatewayOAuthGrantPath},
			{Type: docker.MountTypeBind, Source: cfg.OAuth.RuntimeDir, Target: model.GatewayOAuthRuntimePath},
			{Type: docker.MountTypeBind, Source: cfg.OAuth.ViewDir, Target: model.GatewayOAuthViewPath},
			{Type: docker.MountTypeBind, Source: cfg.OAuth.LockPath, Target: model.GatewayOAuthLockPath},
		}...)
		for _, feature := range cfg.OAuth.Features {
			mounts = append(mounts, []docker.Mount{
				{Type: docker.MountTypeBind, Source: feature.StoreDir, Target: model.GatewayOAuthGitHubGrantPath},
				{Type: docker.MountTypeBind, Source: feature.RuntimeDir, Target: model.GatewayOAuthGitHubRuntimePath},
				{Type: docker.MountTypeBind, Source: feature.ViewDir, Target: model.GatewayOAuthGitHubViewPath},
				{Type: docker.MountTypeBind, Source: feature.LockPath, Target: model.GatewayOAuthGitHubLockPath},
			}...)
		}
	}

	env := []string{}
	if cfg.OAuth != nil {
		env = append(env, model.EnvOAuthBroker+"="+cfg.Profile.Name, model.EnvOAuthIdentity+"="+cfg.OAuth.Identity, model.EnvOAuthIncarnation+"="+cfg.OAuth.Incarnation)
		for _, feature := range cfg.OAuth.Features {
			env = append(env, model.EnvOAuthGitHub+"=1", model.EnvOAuthGitHubIdentity+"="+feature.Identity)
		}
		if len(cfg.OAuth.Instances) != 0 {
			// Host names only; no credential is part of an instance.
			instances, err := json.Marshal(cfg.OAuth.Instances)
			if err != nil {
				return empty, fmt.Errorf("encode protected provider instances: %w", err)
			}
			env = append(env, model.EnvOAuthInstances+"="+string(instances))
		}
	}

	if cfg.NetworkLogPath != "" {
		env = append(env, model.EnvNetworkLogFile+"="+gatewayNetworkLogPath)
	}
	if strings.TrimSpace(cfg.NetworkLogMode) != "" {
		env = append(env, model.EnvNetworkLogMode+"="+cfg.NetworkLogMode)
	}
	if captureDir != "" {
		env = append(env, model.EnvNetworkCaptureDir+"="+gatewayNetworkCaptureDir)
	}
	if session := strings.TrimSpace(cfg.ContainerName); session != "" {
		env = append(env, model.EnvGatewaySession+"="+session)
	}
	if cfg.GatewayConfigDir != "" {
		env = append(env, model.EnvGatewayConfigDir+"="+model.GatewayConfigDir)
	}
	env = append(env, model.EnvGatewayPublishedPorts+"="+gatewayPublishedPorts(cfg.PortBindings))
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
		User:         gatewayUser(),
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
		// Keep even an immediately exited gateway until its startup logs are read.
		// Failed starts and StopContainer remove it explicitly by ID.
		AutoRemove:   false,
		NetworkMode:  docker.NetworkMode(cfg.NetworkName),
		CapAdd:       []string{"NET_ADMIN", "NET_RAW"},
		Sysctls:      sysctls,
		Binds:        binds,
		Mounts:       remainingMounts,
		PortBindings: cfg.PortBindings,
		ExtraHosts:   extraHosts,
		UserNS:       gatewayUserNS(),
	}

	if cfg.OAuth != nil {
		hostConfig.CapAdd = append(hostConfig.CapAdd, "DAC_OVERRIDE", "CHOWN")
	}
	var verifyReady func() error
	if cfg.OAuth != nil {
		if err := oauthbroker.ClearReadiness(cfg.OAuth.RuntimeDir); err != nil {
			return empty, fmt.Errorf("clear prior protected OAuth readiness: %w", err)
		}
		expected := oauthbroker.Config{Tool: cfg.Profile.Name, RuntimeDir: cfg.OAuth.RuntimeDir, Identity: cfg.OAuth.Identity, Session: cfg.ContainerName, Incarnation: cfg.OAuth.Incarnation}
		if len(cfg.OAuth.Features) != 0 {
			expected.GitHub = &oauthbroker.GitHubConfig{Identity: cfg.OAuth.Features[0].Identity}
		}
		verifyReady = func() error {
			if err := oauthbroker.VerifyReadiness(expected); err != nil {
				err = fmt.Errorf("gateway did not attest the requested protected OAuth adapters: %w", err)
				if cfg.NoRebuild {
					return fmt.Errorf("%w; rebuild the gateway without --no-rebuild or %s if its adapters are outdated", err, model.EnvNoRebuild)
				}
				return err
			}
			return nil
		}
	}
	containerID, err := startGatewayContainer(ctx, config, hostConfig, gatewayContainer, verifyReady)
	if err != nil && docker.IsContainerNameConflict(err) {
		reconciled, reconcileErr := ReconcileStale(ctx, cfg.ContainerName, cfg.ProjectHash)
		if reconcileErr != nil {
			return empty, fmt.Errorf("reconcile conflicting gateway container %s: %w", gatewayContainer, reconcileErr)
		}
		if reconciled.Removed || !reconciled.Exists {
			containerID, err = startGatewayContainer(ctx, config, hostConfig, gatewayContainer, verifyReady)
		} else if reconciled.Exists && reconciled.Owned {
			return empty, fmt.Errorf("gateway container %q is already running or starting for session %s; run 'enclave stop %s' and retry: %w", gatewayContainer, cfg.ContainerName, cfg.ContainerName, err)
		}
	}
	if err != nil {
		return empty, err
	}

	return StartResult{
		ContainerName: gatewayContainer,
		ContainerID:   containerID,
	}, nil
}

// prepareNetworkLog rotates the audit log when it has outgrown netlog.MaxLogBytes,
// makes sure the file the gateway binds exists, and records the session
// boundary. Rotation happens here, at session start, so a session never rotates
// its own log: a single long-running session can grow past the cap. Rotation
// truncates in place rather than renaming, so a session already running keeps
// appending to the file it bind-mounted.
func prepareNetworkLog(cfg StartConfig) error {
	logDir := filepath.Dir(cfg.NetworkLogPath)
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return fmt.Errorf("failed to create network log dir: %w", err)
	}

	rotated, err := netlog.RotateIfLarger(cfg.NetworkLogPath, netlog.MaxLogBytes)
	if err != nil {
		return fmt.Errorf("failed to rotate network log: %w", err)
	}
	if rotated {
		logx.Debugf("Rotated network log to %s", netlog.RotatedPath(cfg.NetworkLogPath))
	}

	// The marker is written host side so it is recorded even when the proxy is
	// disabled, and it gives the reader session boundaries plus the anchor
	// `--since session` resolves against. It also creates the file the gateway
	// bind-mounts, which is why nothing here has to touch it first.
	marker := netlog.SessionStartEvent(cfg.ContainerName, time.Now())
	if err := netlog.AppendEvent(cfg.NetworkLogPath, marker); err != nil {
		return fmt.Errorf("failed to record network log session marker: %w", err)
	}
	return nil
}

// prepareNetworkCapture creates the session's capture directory and prunes the
// store of the project and tool to its budget. Like log rotation this happens
// on the host at session start. Pruning removes whole captures, oldest first,
// from every session including running ones, but never a directory, so a
// running gateway keeps writing to the directory it bind-mounted.
func prepareNetworkCapture(cfg StartConfig) (string, error) {
	dir, err := netcapture.PrepareSessionDir(cfg.NetworkCaptureDir, cfg.ContainerName)
	if err != nil {
		return "", fmt.Errorf("failed to create network capture dir: %w", err)
	}
	removed, err := netcapture.PruneStore(cfg.NetworkCaptureDir, netcapture.MaxStoreBytes)
	if err != nil {
		return "", fmt.Errorf("failed to prune network capture store: %w", err)
	}
	if removed > 0 {
		logx.Debugf("Pruned %d network capture(s) from %s", removed, cfg.NetworkCaptureDir)
	}
	return dir, nil
}

// gatewayUserNS puts the gateway into the same keep-id user namespace the
// session container uses under rootless podman. The session joins the gateway's
// network namespace, and a container may only mount sysfs in a network
// namespace owned by its own user namespace: with the gateway in podman's
// default rootless mapping and the session in keep-id, runc fails the session's
// /sys mount with EPERM (crun masks this by bind-mounting the host /sys). The
// session therefore joins this user namespace as well; see the docker backend.
func gatewayUserNS() string {
	if docker.IsPodman() {
		return "keep-id"
	}
	return ""
}

// gatewayUser pins the entrypoint to root where keep-id would otherwise make
// podman start it as the host user; it needs root to set up its runtime
// directories and firewall before it drops to the dnsmasq and proxy users.
func gatewayUser() string {
	if gatewayUserNS() != "" {
		return "0:0"
	}
	return ""
}

// startGatewayContainer runs the gateway and waits until it reports readiness.
// Failed starts must release published ports before the next session's
// host-port checks.
func startGatewayContainer(ctx context.Context, config *docker.ContainerConfig, hostConfig *docker.HostConfig, name string, verifyReady func() error) (string, error) {
	startedAt := time.Now().UTC()
	containerID, err := startRunDetached(ctx, config, hostConfig, name)
	if err != nil {
		// A name conflict means the engine created nothing; the container
		// holding the name belongs to someone else and must survive.
		if !docker.IsContainerNameConflict(err) {
			err = gatewayStartupError(ctx, containerID, err)
			removeFailedGateway(ctx, name, containerID, config)
		}
		return "", fmt.Errorf("failed to start gateway container: %w", err)
	}
	if err := waitForGatewayReady(ctx, containerID, startedAt, verifyReady); err != nil {
		err = gatewayStartupError(ctx, containerID, err)
		removeFailedGateway(ctx, name, containerID, config)
		return "", err
	}
	return containerID, nil
}

// gatewayStartupError reads the retained container's final logs before cleanup,
// including output emitted after the last readiness poll. Never read by name:
// an unsuccessful run may not have created the container holding that name.
// Explicit cancellation skips diagnostics; an expired deadline still permits
// a bounded final read.
func gatewayStartupError(ctx context.Context, containerID string, cause error) error {
	if strings.TrimSpace(containerID) == "" || errors.Is(cause, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return cause
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	logs, err := docker.ContainerLogsTail(logCtx, containerID, 40)
	if err != nil {
		logx.Debugf("Could not read failed gateway startup logs: %v", err)
		return cause
	}
	logs = strings.TrimSpace(logs)
	if logs == "" {
		return cause
	}
	// Bound even a single very long line in the returned CLI error.
	const maxLogBytes = 16 * 1024
	if len(logs) > maxLogBytes {
		logs = "[truncated]\n" + logs[len(logs)-maxLogBytes:]
	}
	return fmt.Errorf("%w\ngateway output:\n  %s", cause, strings.ReplaceAll(logs, "\n", "\n  "))
}

// removeFailedGateway ignores cancellation and removes only by immutable ID.
// Without an engine-reported ID, only an owned, non-running gateway is eligible;
// leave running orphans for the next lock-protected startup to reconcile.
func removeFailedGateway(ctx context.Context, name string, containerID string, config *docker.ContainerConfig) {
	ctx = context.WithoutCancel(ctx)
	removeRef := strings.TrimSpace(containerID)
	if removeRef == "" {
		removeRef = failedGatewayRemoveRef(ctx, name, config)
		if removeRef == "" {
			return
		}
	}
	err := startContainerRemove(ctx, removeRef, true, true)
	if err != nil && !docker.IsNotFound(err) {
		logx.Warnf("Failed to remove gateway container %s after its start failed: %v", name, err)
	}
}

func failedGatewayRemoveRef(ctx context.Context, name string, config *docker.ContainerConfig) string {
	info, err := startContainerInspect(ctx, name)
	if err != nil {
		if !docker.IsNotFound(err) {
			logx.Warnf("Failed to inspect gateway container %s after its start failed: %v", name, err)
		}
		return ""
	}
	var labels map[string]string
	if config != nil {
		labels = config.Labels
	}
	if !ContainerOwnedBy(info, labels[model.GatewayLabelContainer], labels[model.GatewayLabelProjectHash]) {
		logx.Debugf("Leaving gateway container %s in place after a failed start: it does not belong to this session", name)
		return ""
	}
	if info.State != nil && info.State.Running {
		logx.Debugf("Leaving running gateway container %s in place after a failed start without its ID; the next start will reconcile it", name)
		return ""
	}
	return strings.TrimSpace(info.ID)
}

// ReconcileStale removes an owned gateway when its session container is absent.
// The caller must hold the session-start lock: it makes even a newly created
// orphan safe to remove after an interrupted start. Removal uses the immutable
// ID so a replacement with the same name survives.
func ReconcileStale(ctx context.Context, containerName string, projectHash string) (StaleResult, error) {
	name := ContainerName(containerName)
	info, err := startContainerInspect(ctx, name)
	if err != nil {
		if docker.IsNotFound(err) {
			return StaleResult{}, nil
		}
		return StaleResult{}, err
	}
	result := StaleResult{Exists: true, Owned: ContainerOwnedBy(info, containerName, projectHash)}
	if !result.Owned {
		return result, nil
	}

	_, err = startContainerInspect(ctx, containerName)
	if err == nil {
		return result, nil
	}
	if !docker.IsNotFound(err) {
		return result, err
	}

	removeRef := strings.TrimSpace(info.ID)
	if removeRef == "" {
		return result, fmt.Errorf("gateway container %q has no inspect ID", name)
	}
	if err := startContainerRemove(ctx, removeRef, true, true); err != nil && !docker.IsNotFound(err) {
		return result, err
	}
	result.Removed = true
	return result, nil
}

// ContainerOwnedBy verifies the gateway labels against the expected session.
func ContainerOwnedBy(info docker.InspectResponse, containerName string, projectHash string) bool {
	if info.Config == nil {
		return false
	}
	labels := info.Config.Labels
	return strings.EqualFold(strings.TrimSpace(labels[model.GatewayLabelManaged]), "true") &&
		strings.TrimSpace(labels[model.GatewayLabelContainer]) == strings.TrimSpace(containerName) &&
		strings.TrimSpace(labels[model.GatewayLabelProjectHash]) == strings.TrimSpace(projectHash)
}

func gatewayPublishedPorts(bindings docker.PortMap) string {
	ports := make([]string, 0, len(bindings))
	for port, hosts := range bindings {
		if len(hosts) > 0 {
			ports = append(ports, port.Num()+"/"+port.Proto())
		}
	}
	sort.Strings(ports)
	return strings.Join(ports, ",")
}

func validateStartConfig(cfg StartConfig) error {
	if strings.TrimSpace(cfg.NetworkName) == "" {
		return fmt.Errorf("gateway network name is empty")
	}
	if cfg.OAuth != nil {
		if _, known := protectedauth.Lookup(cfg.Profile.Name); !known || cfg.OAuth.Identity == "" || cfg.OAuth.Incarnation == "" || cfg.ContainerName == "" || cfg.TLSRootDir == "" {
			return fmt.Errorf("protected OAuth gateway requires a supported tool identity, session, incarnation and TLS CA")
		}
		paths := []string{cfg.OAuth.StoreDir, cfg.OAuth.RuntimeDir, cfg.OAuth.ViewDir}
		locks := []string{cfg.OAuth.LockPath}
		if len(cfg.OAuth.Features) > 1 {
			return errors.New("protected OAuth gateway supports one native feature")
		}
		for _, feature := range cfg.OAuth.Features {
			if feature.Name != "github-cli" || feature.Identity == "" {
				return errors.New("protected OAuth gateway supports github-cli native authentication only")
			}
			paths = append(paths, feature.StoreDir, feature.RuntimeDir, feature.ViewDir)
			locks = append(locks, feature.LockPath)
		}
		for _, path := range paths {
			info, err := os.Lstat(path)
			if !filepath.IsAbs(path) || err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
				return fmt.Errorf("protected OAuth gateway requires private real directories")
			}
		}
		for i, a := range paths {
			for j, b := range paths {
				if i != j && (util.PathWithin(a, b) || util.PathWithin(b, a)) {
					return fmt.Errorf("protected OAuth gateway directories must be disjoint")
				}
			}
		}
		for _, lock := range locks {
			info, err := os.Lstat(lock)
			if !filepath.IsAbs(lock) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
				return fmt.Errorf("protected OAuth gateway requires the existing private identity lock")
			}
		}
	}
	if cfg.GatewayConfigDir != "" && !util.PathExists(cfg.GatewayConfigDir) {
		return fmt.Errorf("gateway config dir does not exist: %s", cfg.GatewayConfigDir)
	}
	switch mode := strings.TrimSpace(cfg.NetworkLogMode); mode {
	case "", model.NetworkLogCoarse, model.NetworkLogRequests:
	case model.NetworkLogFull:
		if strings.TrimSpace(cfg.NetworkCaptureDir) == "" {
			return fmt.Errorf("network log mode %s needs a capture directory", mode)
		}
	default:
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

func waitForGatewayReady(ctx context.Context, containerID string, since time.Time, verifyReady func() error) error {
	deadline := time.Now().Add(gatewayReadyTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("interrupted while waiting for gateway readiness: %w", err)
		}
		logs, err := docker.ContainerLogsSince(ctx, containerID, since)
		if err == nil && HasLogLine(logs, gatewayReadyMarker) {
			if verifyReady != nil {
				return verifyReady()
			}
			return nil
		}
		if err != nil && !docker.IsNotFound(err) {
			return fmt.Errorf("read gateway startup logs: %w", err)
		}

		inspect, inspectErr := docker.ContainerInspect(ctx, containerID)
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

		select {
		case <-ctx.Done():
		case <-time.After(gatewayReadyPollInterval):
		}
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
	return fmt.Errorf("gateway image %q does not exist locally and builds are disabled (--no-rebuild or %s); re-enable builds to create it", image, model.EnvNoRebuild)
}

// StopContainer stops and removes the exact gateway returned by Start. Using
// its immutable ID prevents delayed error cleanup from stopping a newer
// same-name gateway created by a concurrent session start.
// RemoveContainer stops the gateway and waits until it is gone, so a protected
// session's reservation can be released right after.
func RemoveContainer(container string) {
	StopContainer(container)
	if strings.TrimSpace(container) == "" {
		return
	}
	if err := docker.ContainerRemoveAndWait(context.Background(), container, true, 10*time.Second); err != nil {
		logx.Warnf("Failed to remove gateway container %s: %v", container, err)
	}
}

func StopContainer(container string) {
	if strings.TrimSpace(container) == "" {
		return
	}
	timeout := 3 * time.Second
	if err := docker.ContainerStop(context.Background(), container, &timeout); err != nil {
		if docker.IsNotFound(err) {
			logx.Debugf("Gateway container %s is already gone", container)
			return
		}
		logx.Warnf("Failed to stop gateway container %s: %v", container, err)
	}
	if err := docker.ContainerRemove(context.Background(), container, true, true); err != nil && !docker.IsNotFound(err) {
		logx.Warnf("Failed to remove gateway container %s: %v", container, err)
	}
}
