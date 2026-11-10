// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/gateway"
	"enclave/internal/gateway/bundle"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/network"
)

func (b *Backend) startGateway(ctx context.Context, req backend.Request, networkName string) (string, func(), error) {
	if req.Network.Mode == backend.NetworkModeUnrestricted {
		name, cleanup, err := b.startObservingGateway(ctx, req, networkName)
		if err != nil {
			return "", nil, fmt.Errorf("%w\nTo run this unrestricted session without a gateway (and without a network log), pass --disable-gateway", err)
		}
		return name, cleanup, nil
	}
	gatewayConfigDir := config.HostProjectGatewayConfigDir(b.opts.Host.Home, req.Session.ProjectHash, req.Session.Tool)
	policy := network.EffectivePolicy{
		Mode:          model.NetworkModeRestricted,
		Domains:       append([]string(nil), req.Network.Egress.AllowedDomains...),
		DeniedDomains: append([]string(nil), req.Network.Egress.DeniedDomains...),
		Resolvers:     append([]string(nil), req.Network.Egress.Resolvers...),
	}
	if err := bundle.WriteConfigBundle(bundle.BundleWriteConfig{Dir: gatewayConfigDir, Policy: policy, Tool: req.Session.Tool}); err != nil {
		return "", nil, fmt.Errorf("failed to write gateway config bundle: %w", err)
	}

	secretReleaseFile, err := writeSecretReleaseConfig(req.Secrets)
	if err != nil {
		return "", nil, err
	}
	startConfig := b.gatewayStartConfig(req, networkName, gatewayConfigDir)
	startConfig.NetworkMode = model.NetworkModeRestricted
	startConfig.SecretReleaseFile = secretReleaseFile
	if req.NativeAuth != nil {
		dir, err := b.storage.storeDir(req.NativeAuth.Store.Key, req.NativeAuth.Store.Kind)
		uid, uidErr := strconv.Atoi(b.opts.Host.UID)
		gid, gidErr := strconv.Atoi(b.opts.Host.GID)
		if err != nil || uidErr != nil || gidErr != nil {
			cleanupFiles([]string{secretReleaseFile})
			return "", nil, fmt.Errorf("protected auth requires a native store and numeric host identity")
		}
		file, err := writeGatewayConfig("native-auth", req.NativeAuth.Config)
		if err != nil {
			cleanupFiles([]string{secretReleaseFile})
			return "", nil, err
		}
		startConfig.NativeAuth = &gateway.NativeAuthStart{ConfigFile: file, StoreDir: dir,
			LockPath: hoststore.LockPath(b.opts.Host.Home, dir), UID: uid, GID: gid}
	}
	return b.runGateway(ctx, startConfig, "failed to start DNS gateway")
}

// startObservingGateway starts the gateway of an unrestricted session. It
// enforces nothing, so it gets no config bundle and no secret releases: the
// session holds the real values.
func (b *Backend) startObservingGateway(ctx context.Context, req backend.Request, networkName string) (string, func(), error) {
	startConfig := b.gatewayStartConfig(req, networkName, "")
	startConfig.NetworkMode = model.NetworkModeUnrestricted
	startConfig.Hostname = req.Hostname
	return b.runGateway(ctx, startConfig, "failed to start gateway")
}

func (b *Backend) gatewayStartConfig(req backend.Request, networkName string, gatewayConfigDir string) gateway.StartConfig {
	workspaceID := workspaceIDFromSession(req.Session, b.opts.ProjectDir)
	networkLogMode := b.opts.NetworkLogMode
	if req.Network.Mode == backend.NetworkModeUnrestricted && networkLogMode == model.NetworkLogFull {
		networkLogMode = model.NetworkLogRequests
	}
	captureRoot := ""
	if networkLogMode == model.NetworkLogFull {
		captureRoot = config.HostProjectNetworkCaptureDir(b.opts.Host.Home, req.Session.ProjectHash, req.Session.Tool)
	}
	return gateway.StartConfig{
		Paths:             b.opts.Paths,
		Profile:           model.Profile{Name: req.Session.Tool},
		AllowlistPath:     req.Network.Egress.AllowlistPath,
		ContainerName:     req.Session.Name,
		ForceRebuild:      b.opts.ForceRebuild,
		NoRebuild:         b.opts.NoRebuild,
		NetworkLogMode:    networkLogMode,
		NetworkLogPath:    config.HostProjectNetworkLogPath(b.opts.Host.Home, req.Session.ProjectHash, req.Session.Tool),
		NetworkCaptureDir: captureRoot,
		GatewayConfigDir:  gatewayConfigDir,
		NetworkName:       networkName,
		PortBindings:      portMap(req.Ports),
		ExposedPorts:      portSet(req.Ports),
		LoopbackPorts:     append([]string(nil), req.Network.LoopbackPorts...),
		IdeBridgePorts:    append([]string(nil), req.Network.IdeBridgePorts...),
		Home:              b.opts.Host.Home,
		ProjectDir:        req.Session.Worktree,
		WorkspaceID:       workspaceID,
		ProjectHash:       req.Session.ProjectHash,
		TLSRootDir:        config.HostTLSDir(b.opts.Host.Home),
	}
}

func (b *Backend) runGateway(ctx context.Context, startConfig gateway.StartConfig, errPrefix string) (string, func(), error) {
	var tempFiles []string
	if startConfig.SecretReleaseFile != "" {
		tempFiles = append(tempFiles, startConfig.SecretReleaseFile)
	}
	if startConfig.NativeAuth != nil {
		tempFiles = append(tempFiles, startConfig.NativeAuth.ConfigFile)
	}
	result, err := gateway.Start(ctx, startConfig)
	if err != nil {
		cleanupFiles(tempFiles)
		return "", nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	tempFiles = append(tempFiles, result.TempFiles...)
	cleanup := func() {
		gatewayRef := result.ContainerID
		if strings.TrimSpace(gatewayRef) == "" {
			gatewayRef = result.ContainerName
		}
		gateway.StopContainer(gatewayRef)
		cleanupFiles(tempFiles)
	}
	return result.ContainerName, cleanup, nil
}

// RemoveStaleGateway removes the gateway sidecar of a session whose container
// does not exist. A start interrupted between gateway readiness and container
// creation leaves the sidecar running with the session's ports published,
// which would fail the next start's host-port checks. The caller holds the
// session-start lock, so an owned orphan can be removed without a grace period.
func (b *Backend) RemoveStaleGateway(ctx context.Context, name string, projectHash string) error {
	result, err := gateway.ReconcileStale(ctx, name, projectHash)
	if err != nil {
		return err
	}
	gatewayName := gateway.ContainerName(name)
	if result.Removed {
		logx.Warnf("Removed stale gateway container %s left behind by an interrupted start", gatewayName)
	}
	return nil
}

// StopExitedSessionGateways stops gateways of exited sessions and removes aged,
// stopped orphans. ownerPrefix scopes session names (empty matches all).
// It returns the affected gateway names; dryRun only reports them.
func StopExitedSessionGateways(ctx context.Context, ownerPrefix string, dryRun bool) ([]string, error) {
	stopped, stopErr := stopExitedSessionGateways(ctx, "", ownerPrefix, dryRun)
	removed, removeErr := removeStoppedGatewayOrphans(ctx, ownerPrefix, dryRun)
	return append(stopped, removed...), errors.Join(stopErr, removeErr)
}

func (b *Backend) StopExitedSessionGateways(ctx context.Context) error {
	_, err := StopExitedSessionGateways(ctx, "", false)
	return err
}

func stopGatewayIfSessionExited(ctx context.Context, session string) {
	if _, err := stopExitedSessionGateways(ctx, session, "", false); err != nil {
		logx.Debugf("Failed to check the gateway of session %s: %v", session, err)
	}
}

// stopExitedSessionGateways stops gateways by their listed ID, so a same-name
// restart, which removes the exited session and its gateway first, keeps its
// new gateway. An empty session matches all sessions.
func stopExitedSessionGateways(ctx context.Context, session string, ownerPrefix string, dryRun bool) ([]string, error) {
	gateways, err := exitedSessionGateways(ctx, session, ownerPrefix)
	if err != nil {
		logx.Warnf("Failed to check for gateways of exited sessions: %v", err)
		return nil, err
	}
	names := make([]string, 0, len(gateways))
	var stopErr error
	for _, gw := range gateways {
		name := dockercmd.PrimaryContainerName(gw)
		names = append(names, name)
		if !dryRun {
			logx.Debugf("Stopping gateway container %s of an exited session", name)
			// Podman requires the dependent session to be removed first. Leave
			// removal to RemoveSessionContainer while that session still exists.
			timeout := 3 * time.Second
			if err := dockercmd.ContainerStop(ctx, gw.ID, &timeout); err != nil && !dockercmd.IsNotFound(err) {
				logx.Warnf("Failed to stop gateway container %s: %v", name, err)
				stopErr = errors.Join(stopErr, err)
			}
		}
	}
	return names, stopErr
}

// exitedSessionGateways lists running gateways whose background session exists
// and has exited. A starting session has no container yet or one in the "created"
// state, and a foreground session removes its own gateway, so neither matches.
func exitedSessionGateways(ctx context.Context, session string, ownerPrefix string) ([]dockercmd.Summary, error) {
	filters := dockercmd.NewFilters()
	if session != "" {
		filters.Add("label", model.GatewayLabelContainer+"="+session)
	}
	filters.Add("label", model.GatewayLabelManaged+"=true")
	filters.Add("status", "running")
	gateways, err := dockercmd.ContainerList(ctx, dockercmd.ListOptions{Filters: filters})
	if err != nil {
		return nil, err
	}
	gatewaysBySession := map[string]dockercmd.Summary{}
	for _, gw := range gateways {
		owner := gw.Labels[model.GatewayLabelContainer]
		if owner == "" || gw.Labels[model.GatewayLabelProjectHash] == "" || !strings.HasPrefix(owner, ownerPrefix) ||
			dockercmd.PrimaryContainerName(gw) != gateway.ContainerName(owner) {
			continue
		}
		gatewaysBySession[owner] = gw
	}
	if len(gatewaysBySession) == 0 {
		return nil, nil
	}
	sessions, err := dockercmd.ContainerInspectMany(ctx, slices.Sorted(maps.Keys(gatewaysBySession)))
	if err != nil {
		return nil, err
	}
	var exited []dockercmd.Summary
	for _, info := range sessions {
		gw, ok := gatewaysBySession[strings.TrimPrefix(info.Name, "/")]
		if !ok || info.Config == nil || !containerExited(info.State) ||
			info.Config.Labels[model.LabelHash] != gw.Labels[model.GatewayLabelProjectHash] ||
			!strings.EqualFold(info.Config.Labels[model.LabelBackground], "true") {
			continue
		}
		exited = append(exited, gw)
	}
	return exited, nil
}

// Allow a failed startup to collect its final logs before an unrelated session's
// sweep can remove them. This exceeds the 15s readiness and 3s log-read windows.
const stoppedGatewayOrphanGrace = time.Minute

func removeStoppedGatewayOrphans(ctx context.Context, ownerPrefix string, dryRun bool) ([]string, error) {
	filters := dockercmd.NewFilters()
	filters.Add("label", model.GatewayLabelManaged+"=true")
	gateways, err := dockercmd.ContainerList(ctx, dockercmd.ListOptions{All: true, Filters: filters})
	if err != nil {
		logx.Warnf("Failed to list stopped orphan gateway containers: %v", err)
		return nil, err
	}
	var removed []string
	var removeErr error
	for _, candidate := range gateways {
		if !containerExited(&dockercmd.ContainerState{Status: candidate.State}) {
			continue
		}
		owner := candidate.Labels[model.GatewayLabelContainer]
		_, hash, _, managed := dockercmd.ParseManagedName(owner)
		if !managed || !strings.HasPrefix(owner, ownerPrefix) || candidate.ID == "" ||
			hash != candidate.Labels[model.GatewayLabelProjectHash] ||
			dockercmd.PrimaryContainerName(candidate) != gateway.ContainerName(owner) {
			continue
		}
		eligible, err := stoppedGatewayOrphan(ctx, candidate.ID, owner, hash, time.Now())
		if err != nil {
			logx.Warnf("Failed to inspect orphan gateway container %s: %v", dockercmd.PrimaryContainerName(candidate), err)
			removeErr = errors.Join(removeErr, err)
			continue
		}
		if !eligible {
			continue
		}
		if !dryRun {
			// Non-force removal also protects a container restarted after inspection.
			if err := dockercmd.ContainerRemove(ctx, candidate.ID, false, true); err != nil && !dockercmd.IsNotFound(err) {
				logx.Warnf("Failed to remove orphan gateway container %s: %v", dockercmd.PrimaryContainerName(candidate), err)
				removeErr = errors.Join(removeErr, err)
				continue
			}
		}
		removed = append(removed, dockercmd.PrimaryContainerName(candidate))
	}
	return removed, removeErr
}

func stoppedGatewayOrphan(ctx context.Context, id, owner, hash string, now time.Time) (bool, error) {
	info, err := dockercmd.ContainerInspect(ctx, id)
	if dockercmd.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.ID != id || !gateway.ContainerOwnedBy(info, owner, hash) ||
		!containerExited(info.State) || info.State.Running {
		return false, nil
	}
	finished, err := time.Parse(time.RFC3339Nano, info.State.FinishedAt)
	if err != nil || finished.IsZero() || now.Sub(finished) < stoppedGatewayOrphanGrace {
		return false, nil
	}
	_, err = dockercmd.ContainerInspect(ctx, owner)
	if dockercmd.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

func containerExited(state *dockercmd.ContainerState) bool {
	if state == nil {
		return false
	}
	switch state.Status {
	case "exited", "dead", "stopped": // Podman also reports "stopped".
		return true
	}
	return false
}

func workspaceIDFromSession(session backend.SessionMeta, projectDir string) string {
	workspaceID := strings.TrimSpace(session.RealWorktree)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(projectDir)
	}
	if workspaceID == "" {
		workspaceID = session.Worktree
	}
	return workspaceID
}

func writeSecretReleaseConfig(secrets []backend.SecretRelease) (string, error) {
	entries := make([]model.SecretReleaseEntry, 0, len(secrets))
	for _, secret := range secrets {
		if secret.HTTP == nil {
			continue
		}
		entries = append(entries, model.SecretReleaseEntry{
			SecretID:    secret.SecretID,
			Placeholder: secret.Placeholder,
			Value:       secret.Value,
			Hosts:       append([]string(nil), secret.HTTP.Hosts...),
			Header:      secret.HTTP.Header,
			Format:      secret.HTTP.Format,
			ExactHosts:  secret.HTTP.ExactHosts,
		})
	}
	if len(entries) == 0 {
		return "", nil
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("marshal secret release entries: %w", err)
	}
	return writeGatewayConfig("secret-release", data)
}

func writeGatewayConfig(name string, data []byte) (string, error) {
	file, err := os.CreateTemp("", "enclave-"+name+"-*.json")
	if err != nil {
		return "", fmt.Errorf("create %s config: %w", name, err)
	}
	tmpPath := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath is created by os.CreateTemp.
		return "", fmt.Errorf("write %s config: %w", name, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath is created by os.CreateTemp.
		return "", fmt.Errorf("close %s config: %w", name, err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil { // #nosec G302 G703 -- tmpPath is created by os.CreateTemp.
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath is created by os.CreateTemp.
		return "", fmt.Errorf("chmod %s config: %w", name, err)
	}
	return tmpPath, nil
}

func cleanupFiles(paths []string) {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		_ = os.Remove(filepath.Clean(path)) // #nosec G304 G703 -- paths are temp files created by this process or gateway.
	}
}
