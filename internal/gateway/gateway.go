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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/config"
	"enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/netlog"
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
	// SessionExists is set when the session container itself exists; it is
	// only checked for an owned gateway.
	SessionExists bool
	// Removed is set when the gateway was removed.
	Removed bool
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
	if cfg.NoRebuild {
		logx.Warnf("Skipping gateway image build due to --no-rebuild.")
		if err := ensureExistingGatewayImageWith(cfg.Profile, docker.ImageExists); err != nil {
			return empty, err
		}
	} else {
		needs, _, err := needsRebuild(cfg.Paths, cfg.Profile, cfg.AllowlistPath)
		if err != nil {
			return empty, err
		}
		if cfg.ForceRebuild || needs {
			resolveBuildPlan := func() (bool, string, error) {
				return needsRebuild(cfg.Paths, cfg.Profile, cfg.AllowlistPath)
			}
			executeBuild := func(buildHash string) error {
				return buildGatewayImage(ctx, cfg.Paths, cfg.Profile, cfg.AllowlistPath, buildHash)
			}
			if err := coordinateGatewayImageBuild(cfg.Home, imageName(cfg.Profile), cfg.ForceRebuild, resolveBuildPlan, executeBuild); err != nil {
				return empty, err
			}
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
	mounts = appendTLSCAMounts(mounts, cfg.TLSRootDir)

	env := []string{}
	if cfg.NetworkLogPath != "" {
		env = append(env, model.EnvNetworkLogFile+"="+gatewayNetworkLogPath)
	}
	if strings.TrimSpace(cfg.NetworkLogMode) != "" {
		env = append(env, model.EnvNetworkLogMode+"="+cfg.NetworkLogMode)
	}
	if session := strings.TrimSpace(cfg.ContainerName); session != "" {
		env = append(env, model.EnvGatewaySession+"="+session)
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
		AutoRemove:   true,
		NetworkMode:  docker.NetworkMode(cfg.NetworkName),
		CapAdd:       []string{"NET_ADMIN", "NET_RAW"},
		Sysctls:      sysctls,
		Binds:        binds,
		Mounts:       remainingMounts,
		PortBindings: cfg.PortBindings,
		ExtraHosts:   extraHosts,
		UserNS:       gatewayUserNS(),
	}

	containerID, err := startGatewayContainer(ctx, config, hostConfig, gatewayContainer)
	if err != nil && docker.IsContainerNameConflict(err) {
		reconciled, reconcileErr := ReconcileStale(ctx, cfg.ContainerName, cfg.ProjectHash)
		if reconcileErr != nil {
			return empty, fmt.Errorf("reconcile conflicting gateway container %s: %w", gatewayContainer, reconcileErr)
		}
		if reconciled.Removed || !reconciled.Exists {
			// Removed here, or gone on its own (an auto-removing gateway
			// finishing its exit): the name is free, run once more.
			containerID, err = startGatewayContainer(ctx, config, hostConfig, gatewayContainer)
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
// A sidecar whose start does not complete is removed again: left running, it
// keeps the session's published ports bound, and the next start of the same
// session name fails its host-port checks before it reaches gateway startup.
func startGatewayContainer(ctx context.Context, config *docker.ContainerConfig, hostConfig *docker.HostConfig, name string) (string, error) {
	startedAt := time.Now().UTC()
	containerID, err := startRunDetached(ctx, config, hostConfig, name)
	if err != nil {
		// A name conflict means the engine created nothing; the container
		// holding the name belongs to someone else and must survive.
		if !docker.IsContainerNameConflict(err) {
			removeFailedGateway(ctx, name, containerID, config)
		}
		return "", fmt.Errorf("failed to start gateway container: %w", err)
	}
	if err := waitForGatewayReady(ctx, name, startedAt); err != nil {
		removeFailedGateway(ctx, name, containerID, config)
		return "", err
	}
	return containerID, nil
}

// removeFailedGateway runs detached from ctx, which is already cancelled when
// the start was interrupted. It removes by container ID when the engine
// reported one so a newer same-name gateway of a concurrent start survives.
// Without an ID (podman prints none on a failed start, and a killed CLI may
// not have printed Docker's) the name is inspected first, and only a container
// that carries this session's gateway labels and is not running is removed, by
// its inspected ID. A running container whose ID was not returned is left
// for the next lock-protected startup to reconcile.
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

// failedGatewayRemoveRef resolves the container holding name to the ID this
// start may remove, or "" when nothing holds the name, the holder belongs to
// another session, or it is running.
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
		result.SessionExists = true
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

func validateStartConfig(cfg StartConfig) error {
	if strings.TrimSpace(cfg.NetworkName) == "" {
		return fmt.Errorf("gateway network name is empty")
	}
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

func waitForGatewayReady(ctx context.Context, containerID string, since time.Time) error {
	deadline := time.Now().Add(gatewayReadyTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("interrupted while waiting for gateway readiness: %w", err)
		}
		logs, err := docker.ContainerLogsSince(ctx, containerID, since)
		if err == nil && HasLogLine(logs, gatewayReadyMarker) {
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
	return fmt.Errorf("gateway image %q does not exist locally; rerun without --no-rebuild, pass --rebuild, or use --allow-all-network to bypass the gateway", image)
}

// StopContainer stops the exact gateway container returned by Start. Using
// its immutable ID prevents delayed error cleanup from stopping a newer
// same-name gateway created by a concurrent session start.
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
}
