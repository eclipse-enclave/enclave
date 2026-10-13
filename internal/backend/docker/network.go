// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
)

const (
	sessionNetworkDynamicSubnet  = "0.0.0.0/28"
	sessionNetworkGCGracePeriod  = time.Hour
	sessionNetworkEnsureAttempts = 4
	// Podman removes --rm containers asynchronously; retry while this
	// session's containers are still present.
	sessionNetworkRemoveRetries = 40
	hostBindingIPv4Option       = "com.docker.network.bridge.host_binding_ipv4"
	// netavark isolates bridge networks from each other only when asked
	// (netavark 2 / podman 6 made strict the default); Docker isolates
	// user-defined bridges unconditionally and rejects the option.
	podmanIsolateOption        = "isolate"
	podmanIsolateStrict        = "strict"
	podmanIsolateOptedInOnly   = "true"
	netavarkStrictIsolateMajor = 1
	netavarkStrictIsolateMinor = 7
)

var (
	sessionNetworkRemoveRetryWait   = 250 * time.Millisecond
	sessionNetworkRemoveNoticeAfter = time.Second
	dockerInfo                      = dockercmd.Info
	networkCreate                   = dockercmd.NetworkCreate
	networkInspect                  = dockercmd.NetworkInspect
	networkList                     = dockercmd.NetworkList
	networkRemove                   = dockercmd.NetworkRemove
	networkAttachedContainers       = dockercmd.NetworkAttachedContainers
	sessionContainerList            = dockercmd.ContainerList
	sessionRuntimeExists            = dockerSessionRuntimeExists
)

type sessionNetworkRef struct {
	Name        string
	ID          string
	Container   string
	ProjectHash string
}

type sessionNetworkAction int

const (
	sessionNetworkReuse sessionNetworkAction = iota
	sessionNetworkConflict
	sessionNetworkAttached
)

func sessionNetworkHasEndpoints(ctx context.Context, info dockercmd.NetworkInspectResponse) (bool, error) {
	attached, err := sessionNetworkEndpoints(ctx, info)
	if err != nil {
		return false, err
	}
	return len(attached) > 0, nil
}

// Podman omits the inspect endpoint map and skips its in-use check on
// removal by ID, so query attached containers, including stopped ones.
func sessionNetworkEndpoints(ctx context.Context, info dockercmd.NetworkInspectResponse) ([]string, error) {
	if len(info.Containers) > 0 {
		names := make([]string, 0, len(info.Containers))
		for _, endpoint := range info.Containers {
			names = append(names, strings.TrimSpace(endpoint.Name))
		}
		return names, nil
	}
	if !dockercmd.IsPodman() {
		return nil, nil
	}
	return networkAttachedContainers(ctx, strings.TrimSpace(info.Name))
}

// Wait only for this session's containers; foreign endpoints may persist.
func sessionOwnsEndpoints(names []string, container string) bool {
	container = strings.TrimSpace(container)
	for _, name := range names {
		if name = strings.TrimSpace(name); name != container && name != container+model.GatewayContainerSuffix {
			return false
		}
	}
	return true
}

func sessionNetworkName(containerName string) string {
	return strings.TrimSpace(containerName) + model.SessionNetworkSuffix
}

func sessionNetworkLabels(meta backend.SessionMeta) map[string]string {
	return map[string]string{
		model.NetworkLabelManaged:     "true",
		model.NetworkLabelContainer:   strings.TrimSpace(meta.Name),
		model.NetworkLabelProjectHash: strings.TrimSpace(meta.ProjectHash),
	}
}

func sessionNetworkOwnedBy(info dockercmd.NetworkInspectResponse, container string, projectHash string) bool {
	return strings.EqualFold(strings.TrimSpace(info.Labels[model.NetworkLabelManaged]), "true") &&
		strings.TrimSpace(info.Labels[model.NetworkLabelContainer]) == strings.TrimSpace(container) &&
		strings.TrimSpace(info.Labels[model.NetworkLabelProjectHash]) == strings.TrimSpace(projectHash)
}

// Discovery has no independently known owner; require complete labels and the
// canonical network name before using them to select a cleanup candidate.
func sessionNetworkOwner(info dockercmd.NetworkInspectResponse) (string, string, bool) {
	owner := strings.TrimSpace(info.Labels[model.NetworkLabelContainer])
	projectHash := strings.TrimSpace(info.Labels[model.NetworkLabelProjectHash])
	return owner, projectHash, owner != "" && projectHash != "" &&
		info.Name == sessionNetworkName(owner) && sessionNetworkOwnedBy(info, owner, projectHash)
}

func sessionNetworkActionFor(info dockercmd.NetworkInspectResponse, meta backend.SessionMeta, hasEndpoints bool) sessionNetworkAction {
	if !sessionNetworkOwnedBy(info, meta.Name, meta.ProjectHash) {
		return sessionNetworkConflict
	}
	if hasEndpoints {
		return sessionNetworkAttached
	}
	return sessionNetworkReuse
}

func (b *Backend) ensureSessionNetwork(ctx context.Context, meta backend.SessionMeta, sysInfo dockercmd.SystemInfo) (sessionNetworkRef, error) {
	name := sessionNetworkName(meta.Name)
	if strings.TrimSpace(meta.Name) == "" {
		return sessionNetworkRef{}, fmt.Errorf("cannot create a per-session network without a container name")
	}

	for attempt := 0; attempt < sessionNetworkEnsureAttempts; attempt++ {
		info, err := networkInspect(ctx, name)
		if err == nil {
			attached, endpointErr := sessionNetworkEndpoints(ctx, info)
			if endpointErr != nil {
				return sessionNetworkRef{}, fmt.Errorf("list containers attached to per-session network %q: %w", name, endpointErr)
			}
			action := sessionNetworkActionFor(info, meta, len(attached) > 0)
			switch action {
			case sessionNetworkReuse:
				return newSessionNetworkRef(name, info.ID, meta.Name, meta.ProjectHash)
			case sessionNetworkConflict:
				return sessionNetworkRef{}, fmt.Errorf("docker network %q already exists and is not owned by this enclave session", name)
			case sessionNetworkAttached:
				return sessionNetworkRef{}, attachedSessionNetworkError(meta.Name, name, attached)
			}
		} else if !dockercmd.IsNotFound(err) {
			return sessionNetworkRef{}, fmt.Errorf("inspect per-session network %q: %w", name, err)
		}

		id, err := createSessionNetwork(ctx, meta, sysInfo)
		if err != nil {
			if dockercmd.IsAlreadyExists(err) {
				continue
			}
			if dockercmd.IsAddressPoolExhausted(err) {
				return sessionNetworkRef{}, fmt.Errorf("create per-session network %q: Docker address pools are exhausted; upgrade the Docker daemon to 29.0.0 or newer, or configure daemon default-address-pools: %w", name, err)
			}
			return sessionNetworkRef{}, fmt.Errorf("create per-session network %q: %w", name, err)
		}
		return newSessionNetworkRef(name, id, meta.Name, meta.ProjectHash)
	}
	return sessionNetworkRef{}, fmt.Errorf("create per-session network %q: repeated concurrent changes prevented ownership verification", name)
}

// netavark before 1.7 rejects strict at container attachment, not creation,
// so select the value from its version before creating the network.
func podmanIsolateValue(info dockercmd.SystemInfo) (string, error) {
	if !dockercmd.IsPodman() {
		return "", nil
	}
	if !strings.EqualFold(strings.TrimSpace(info.NetworkBackend), "netavark") {
		backend := strings.TrimSpace(info.NetworkBackend)
		if backend == "" {
			backend = "an unreported network backend"
		}
		return "", fmt.Errorf("podman uses %s; per-session network isolation requires netavark", backend)
	}
	if netavarkVersionAtLeast(info.NetworkBackendVersion, netavarkStrictIsolateMajor, netavarkStrictIsolateMinor) {
		return podmanIsolateStrict, nil
	}
	logx.Warnf("This podman installs %s, which has no isolate=strict; session networks are isolated from other enclave sessions but not from non-isolated podman networks. Upgrade netavark to %d.%d.0 or newer for full bridge isolation.", netavarkVersionLabel(info.NetworkBackendVersion), netavarkStrictIsolateMajor, netavarkStrictIsolateMinor)
	return podmanIsolateOptedInOnly, nil
}

func netavarkVersionLabel(version string) string {
	if version = strings.TrimSpace(version); version != "" {
		return version
	}
	return "a network backend of unreported version"
}

// Podman reports the backend version as "netavark 1.4.0".
func netavarkVersionAtLeast(version string, wantMajor int, wantMinor int) bool {
	fields := strings.Fields(strings.TrimSpace(version))
	if len(fields) == 0 {
		return false
	}
	return dockerVersionAtLeast(fields[len(fields)-1], wantMajor, wantMinor)
}

// Each engine rejects the other's bridge option keys.
func sessionNetworkDriverOptions(podman bool, isolate string) map[string]string {
	if podman {
		return map[string]string{podmanIsolateOption: isolate}
	}
	// Published ports already bind 127.0.0.1 explicitly; this keeps a port
	// published without a host address off the external interfaces as well.
	return map[string]string{hostBindingIPv4Option: "127.0.0.1"}
}

func createSessionNetwork(ctx context.Context, meta backend.SessionMeta, info dockercmd.SystemInfo) (string, error) {
	podman := dockercmd.IsPodman()
	isolate, err := podmanIsolateValue(info)
	if err != nil {
		return "", err
	}
	ipv6 := false
	opts := dockercmd.NetworkCreateOptions{
		Name:       sessionNetworkName(meta.Name),
		Driver:     "bridge",
		EnableIPv6: &ipv6,
		Labels:     sessionNetworkLabels(meta),
		Options:    sessionNetworkDriverOptions(podman, isolate),
	}
	// ServerVersion is Docker's daemon version; Podman uses its own /24
	// IPAM pool and needs no small-prefix request.
	if !podman && dockerVersionAtLeast(info.ServerVersion, 29, 0) {
		opts.Subnet = sessionNetworkDynamicSubnet
	}

	id, err := networkCreate(ctx, opts)
	if err == nil || dockercmd.IsAlreadyExists(err) {
		return id, err
	}
	if opts.Subnet != "" {
		// Even newer daemons or CLIs can reject the dynamic-prefix request;
		// retry with default IPAM.
		opts.Subnet = ""
		id, retryErr := networkCreate(ctx, opts)
		if retryErr != nil {
			return id, fmt.Errorf("create network with subnet %q: %v; retry with default IPAM: %w", sessionNetworkDynamicSubnet, err, retryErr)
		}
		return id, nil
	}
	if podman && opts.Options[podmanIsolateOption] == podmanIsolateStrict && dockercmd.IsUnsupportedIsolateValue(err) {
		// Older Podman CLIs parse isolate as a boolean during network creation,
		// even with a netavark version that supports strict.
		logx.Warnf("podman rejected isolate=strict for the per-session network; using isolate=true, which does not isolate the session from non-isolated podman networks: %v", err)
		opts.Options = sessionNetworkDriverOptions(podman, podmanIsolateOptedInOnly)
		return networkCreate(ctx, opts)
	}
	return id, err
}

func dockerVersionAtLeast(version string, wantMajor int, wantMinor int) bool {
	version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version), "v"))
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(numericPrefix(parts[0]))
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(numericPrefix(parts[1]))
	if err != nil {
		return false
	}
	return major > wantMajor || major == wantMajor && minor >= wantMinor
}

func numericPrefix(value string) string {
	for i, r := range value {
		if r < '0' || r > '9' {
			return value[:i]
		}
	}
	return value
}

func sessionNetworkPastGracePeriod(info dockercmd.NetworkInspectResponse, now time.Time) bool {
	created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(info.Created))
	return err == nil && !created.After(now.Add(-sessionNetworkGCGracePeriod))
}

func newSessionNetworkRef(name string, id string, container string, projectHash string) (sessionNetworkRef, error) {
	name = strings.TrimSpace(name)
	id = strings.TrimSpace(id)
	if id == "" {
		return sessionNetworkRef{}, fmt.Errorf("per-session network %q has an empty network ID", name)
	}
	return sessionNetworkRef{
		Name:        name,
		ID:          id,
		Container:   strings.TrimSpace(container),
		ProjectHash: strings.TrimSpace(projectHash),
	}, nil
}

func attachedSessionNetworkError(containerName string, networkName string, attached []string) error {
	if sessionOwnsEndpoints(attached, containerName) {
		return fmt.Errorf("session %s already running or requires stale-resource cleanup: Docker network %q has attached endpoints; run 'enclave stop %s' and retry", containerName, networkName, containerName)
	}
	containerName = strings.TrimSpace(containerName)
	var peers []string
	for _, name := range attached {
		name = strings.TrimSpace(name)
		if name == containerName || name == containerName+model.GatewayContainerSuffix {
			continue
		}
		if name == "" {
			name = "<unknown>"
		}
		peers = append(peers, name)
	}
	sort.Strings(peers)
	return fmt.Errorf("per-session network %q has foreign attached containers: %s; disconnect them from this network and retry", networkName, strings.Join(peers, ", "))
}

func cleanupSessionNetwork(ref sessionNetworkRef) {
	if strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.ID) == "" || strings.TrimSpace(ref.Container) == "" {
		return
	}
	ctx := context.Background()
	if err := removeOwnedSessionNetwork(ctx, ref, sessionNetworkRemoveRetries); err != nil {
		logx.Warnf("Failed to remove per-session network for %s: %v", ref.Container, err)
	}
}

func removeOwnedSessionNetwork(ctx context.Context, ref sessionNetworkRef, retries int) error {
	started := time.Now()
	noticed := false
	wait := func(reason string) {
		time.Sleep(sessionNetworkRemoveRetryWait)
		if !noticed && time.Since(started) >= sessionNetworkRemoveNoticeAfter {
			noticed = true
			logx.Infof("Waiting to remove per-session network %s: %s (up to %d retries)", ref.Name, reason, retries)
		}
	}
	giveUp := func(reason string) {
		logx.Warnf("Leaving per-session network %s in place after %s: %s; a later session start or `enclave cleanup --ephemeral` removes it once its containers are gone", ref.Name, time.Since(started).Round(time.Second), reason)
	}
	for attempt := 0; attempt <= retries; attempt++ {
		info, err := networkInspect(ctx, ref.Name)
		if err != nil {
			if dockercmd.IsNotFound(err) {
				return nil
			}
			return err
		}
		if strings.TrimSpace(info.ID) != strings.TrimSpace(ref.ID) ||
			!sessionNetworkOwnedBy(info, ref.Container, ref.ProjectHash) {
			return nil
		}
		attached, err := sessionNetworkEndpoints(ctx, info)
		if err != nil {
			return err
		}
		if len(attached) > 0 {
			reason := "session containers still attached: " + strings.Join(attached, ", ")
			if !sessionOwnsEndpoints(attached, ref.Container) {
				logx.Debugf("Leaving per-session network %s in place because it still has foreign attached endpoints: %s", ref.Name, strings.Join(attached, ", "))
				return nil
			}
			if attempt < retries {
				wait(reason)
				continue
			}
			giveUp(reason)
			return nil
		}
		exists, err := sessionRuntimeExists(ctx, ref.Container)
		if err != nil {
			return err
		}
		if exists {
			reason := "session or gateway container still exists"
			if attempt < retries {
				wait(reason)
				continue
			}
			giveUp(reason)
			return nil
		}
		removeRef, err := inspectedNetworkRemoveRef(info)
		if err != nil {
			return err
		}
		if err := networkRemove(ctx, removeRef); err != nil {
			if dockercmd.IsNotFound(err) {
				return nil
			}
			if dockercmd.IsActiveEndpoints(err) && attempt < retries {
				wait("engine reports active endpoints")
				continue
			}
			return err
		}
		if noticed {
			logx.Infof("Removed per-session network %s after %s", ref.Name, time.Since(started).Round(100*time.Millisecond))
		}
		return nil
	}
	return nil
}

func inspectedNetworkRemoveRef(info dockercmd.NetworkInspectResponse) (string, error) {
	if id := strings.TrimSpace(info.ID); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("docker network %q has no inspect ID; refusing name-based removal", info.Name)
}

func dockerSessionRuntimeExists(ctx context.Context, containerName string) (bool, error) {
	for _, name := range []string{containerName, strings.TrimSpace(containerName) + model.GatewayContainerSuffix} {
		exists, err := dockerSessionContainerExists(ctx, name)
		if err != nil || exists {
			return exists, err
		}
	}
	return false, nil
}

func captureSessionNetworkRef(ctx context.Context, containerName string, projectHash string) (sessionNetworkRef, bool) {
	if strings.TrimSpace(projectHash) == "" {
		return sessionNetworkRef{}, false
	}
	name := sessionNetworkName(containerName)
	info, err := networkInspect(ctx, name)
	if err != nil {
		if !dockercmd.IsNotFound(err) {
			logx.Debugf("Failed to capture per-session network %s before teardown: %v", name, err)
		}
		return sessionNetworkRef{}, false
	}
	if info.Name != name ||
		!sessionNetworkOwnedBy(info, containerName, projectHash) {
		return sessionNetworkRef{}, false
	}
	ref, err := newSessionNetworkRef(name, info.ID, containerName, projectHash)
	return ref, err == nil
}

func (b *Backend) gcSessionNetworks(ctx context.Context, now time.Time) {
	stale, err := staleSessionNetworks(ctx, now)
	if err != nil {
		logx.Debugf("Failed to list stale per-session networks: %v", err)
		return
	}
	for _, ref := range stale {
		if err := removeOwnedSessionNetwork(ctx, ref, 0); err != nil {
			logx.Debugf("Failed to remove stale per-session network %s: %v", ref.Name, err)
		}
	}
}

// PruneStaleSessionNetworks removes endpoint-free session networks past the
// grace period whose session and gateway containers are gone, returning their names.
// ownerPrefix restricts owning container names (empty matches all); dryRun
// reports candidates without removing them.
func PruneStaleSessionNetworks(ctx context.Context, now time.Time, ownerPrefix string, dryRun bool) ([]string, error) {
	stale, err := staleSessionNetworks(ctx, now)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(stale))
	for _, ref := range stale {
		if !strings.HasPrefix(ref.Container, ownerPrefix) {
			continue
		}
		names = append(names, ref.Name)
		if dryRun {
			continue
		}
		if err := removeOwnedSessionNetwork(ctx, ref, 0); err != nil {
			logx.Warnf("Failed to remove stale per-session network %s: %v", ref.Name, err)
		}
	}
	return names, nil
}

func staleSessionNetworks(ctx context.Context, now time.Time) ([]sessionNetworkRef, error) {
	filters := dockercmd.NewFilters()
	filters.Add("label", model.NetworkLabelManaged+"=true")
	networks, err := networkList(ctx, filters)
	if err != nil {
		return nil, err
	}
	var candidates []dockercmd.NetworkInspectResponse
	for _, info := range networks {
		if sessionNetworkEligibleForGC(info, now) && strings.TrimSpace(info.ID) != "" {
			candidates = append(candidates, info)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	// Include stopped and unlabelled containers: any holder reserves the name.
	// Removal rechecks existence after this snapshot before deleting anything.
	containers, err := sessionContainerList(ctx, dockercmd.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	reserved := make(map[string]bool, len(containers))
	for _, container := range containers {
		for _, name := range container.Names {
			reserved[strings.TrimPrefix(name, "/")] = true
		}
	}
	var stale []sessionNetworkRef
	for _, info := range candidates {
		owner, projectHash, _ := sessionNetworkOwner(info)
		if reserved[owner] || reserved[owner+model.GatewayContainerSuffix] {
			continue
		}
		hasEndpoints, err := sessionNetworkHasEndpoints(ctx, info)
		if err != nil || hasEndpoints {
			continue
		}
		ref, err := newSessionNetworkRef(info.Name, info.ID, owner, projectHash)
		if err == nil {
			stale = append(stale, ref)
		}
	}
	return stale, nil
}

func dockerSessionContainerExists(ctx context.Context, name string) (bool, error) {
	_, err := dockercmd.ContainerInspect(ctx, name)
	if err == nil {
		return true, nil
	}
	if dockercmd.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

func sessionNetworkEligibleForGC(info dockercmd.NetworkInspectResponse, now time.Time) bool {
	_, _, ok := sessionNetworkOwner(info)
	return ok && len(info.Containers) == 0 && sessionNetworkPastGracePeriod(info, now)
}

func fillSessionNetwork(ctx context.Context, session *backend.Session) {
	if session == nil || strings.TrimSpace(session.Ref.Name) == "" {
		return
	}
	info, err := networkInspect(ctx, sessionNetworkName(session.Ref.Name))
	if err != nil {
		return
	}
	if !sessionNetworkOwnedBy(info, session.Ref.Name, session.ProjectHash) {
		return
	}
	session.Network = sessionNetworkFromInspect(info)
}

func fillSessionNetworks(ctx context.Context, sessions []backend.Session) {
	if len(sessions) == 0 {
		return
	}
	filters := dockercmd.NewFilters()
	filters.Add("label", model.NetworkLabelManaged+"=true")
	networks, err := networkList(ctx, filters)
	if err != nil {
		return
	}
	byOwner := make(map[string]dockercmd.NetworkInspectResponse, len(networks))
	for _, info := range networks {
		owner, _, ok := sessionNetworkOwner(info)
		if !ok {
			continue
		}
		byOwner[owner] = info
	}
	for i := range sessions {
		info, ok := byOwner[sessions[i].Ref.Name]
		if !ok {
			continue
		}
		if !sessionNetworkOwnedBy(info, sessions[i].Ref.Name, sessions[i].ProjectHash) {
			continue
		}
		sessions[i].Network = sessionNetworkFromInspect(info)
	}
}

func sessionNetworkFromInspect(info dockercmd.NetworkInspectResponse) *backend.SessionNetwork {
	subnet := ""
	for _, cfg := range info.SubnetConfigs() {
		candidate := strings.TrimSpace(cfg.Subnet)
		if candidate != "" && !strings.Contains(candidate, ":") {
			subnet = candidate
			break
		}
	}
	return &backend.SessionNetwork{Name: info.Name, Subnet: subnet}
}
