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
	// podman removes an --rm container asynchronously, and its session and
	// gateway containers regularly outlive the CLI that returned. The budget
	// covers that settling time with room for a loaded host; it is only ever
	// spent while this session's own containers are still present.
	sessionNetworkRemoveRetries   = 40
	sessionNetworkRemoveRetryWait = 250 * time.Millisecond
	hostBindingIPv4Option         = "com.docker.network.bridge.host_binding_ipv4"
	// netavark isolates bridge networks from each other only when asked
	// (netavark 2 / podman 6 made strict the default); Docker isolates
	// user-defined bridges unconditionally and rejects the option.
	podmanIsolateOption      = "isolate"
	podmanIsolateStrict      = "strict"
	podmanIsolateOptedInOnly = "true"
	// netavark 1.7.0 added isolate=strict; older releases reject the value
	// when a container attaches to the network.
	netavarkStrictIsolateMajor = 1
	netavarkStrictIsolateMinor = 7
)

var (
	dockerInfo                = dockercmd.Info
	networkCreate             = dockercmd.NetworkCreate
	networkInspect            = dockercmd.NetworkInspect
	networkList               = dockercmd.NetworkList
	networkRemove             = dockercmd.NetworkRemove
	networkAttachedContainers = dockercmd.NetworkAttachedContainers
	sessionContainerList      = dockercmd.ContainerList
	sessionRuntimeExists      = dockerSessionRuntimeExists
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

// sessionNetworkHasEndpoints reports whether any container is attached to the
// network. Docker's inspect carries the endpoint map, but podman omits it
// entirely, so there the engine is asked for the container list instead.
// Removal addresses networks by ID, which skips podman's own in-use check, so
// this is the only guard standing between a live endpoint and a removal.
func sessionNetworkHasEndpoints(ctx context.Context, info dockercmd.NetworkInspectResponse) (bool, error) {
	attached, err := sessionNetworkEndpoints(ctx, info)
	if err != nil {
		return false, err
	}
	return len(attached) > 0, nil
}

// sessionNetworkEndpoints names the containers attached to the network.
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

// sessionOwnsEndpoints reports whether every attached container belongs to this
// session. Those disappear on their own, so removal is worth waiting out;
// anything else was attached from outside and the network stays put instead.
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
			hasEndpoints, endpointErr := sessionNetworkHasEndpoints(ctx, info)
			if endpointErr != nil {
				return sessionNetworkRef{}, fmt.Errorf("list containers attached to per-session network %q: %w", name, endpointErr)
			}
			action := sessionNetworkActionFor(info, meta, hasEndpoints)
			switch action {
			case sessionNetworkReuse:
				ref, err := inspectedSessionNetworkRef(info, meta)
				if err != nil {
					return sessionNetworkRef{}, err
				}
				return ref, nil
			case sessionNetworkConflict:
				return sessionNetworkRef{}, fmt.Errorf("docker network %q already exists and is not owned by this enclave session", name)
			case sessionNetworkAttached:
				return sessionNetworkRef{}, attachedSessionNetworkError(meta.Name, name)
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
		if strings.TrimSpace(id) == "" {
			return sessionNetworkRef{}, fmt.Errorf("create per-session network %q: Docker returned an empty network ID", name)
		}
		return sessionNetworkRef{Name: name, ID: id, Container: meta.Name, ProjectHash: meta.ProjectHash}, nil
	}
	return sessionNetworkRef{}, fmt.Errorf("create per-session network %q: repeated concurrent changes prevented ownership verification", name)
}

// podmanIsolateValue picks the bridge isolation netavark actually accepts.
// netavark parses the value when a container attaches, not when the network is
// created. Gate "strict" on its version to avoid attach-time failures. Older
// Podman versions also validate the value during creation; the fallback below
// handles those CLIs paired with a newer netavark.
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
	// isolate=true blocks traffic only between networks that also opt in, which
	// every session network does; containers on podman's default network keep a
	// route into the session. That is a weaker guarantee than the one this
	// feature advertises, so it is stated unconditionally rather than logged at
	// debug level.
	logx.Warnf("This podman installs %s, which has no isolate=strict; the session is isolated from other enclave sessions but reachable from containers on non-isolated podman networks. Upgrade netavark to %d.%d.0 or newer for full isolation.", netavarkVersionLabel(info.NetworkBackendVersion), netavarkStrictIsolateMajor, netavarkStrictIsolateMinor)
	return podmanIsolateOptedInOnly, nil
}

// netavarkVersionLabel names the network backend for the degraded-isolation
// warning, which has to stay readable when podman reports no version at all.
func netavarkVersionLabel(version string) string {
	if version = strings.TrimSpace(version); version != "" {
		return version
	}
	return "a network backend of unreported version"
}

// netavarkVersionAtLeast parses podman's "netavark 1.4.0" backend version. An
// unrecognized or absent value reports false, keeping the safe isolate=true.
func netavarkVersionAtLeast(version string, wantMajor int, wantMinor int) bool {
	fields := strings.Fields(strings.TrimSpace(version))
	if len(fields) == 0 {
		return false
	}
	return dockerVersionAtLeast(fields[len(fields)-1], wantMajor, wantMinor)
}

// sessionNetworkDriverOptions returns the bridge options for the engine in
// use. Each engine rejects the other's keys, so nothing is shared.
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
	// info.ServerVersion is the Docker daemon's; podman leaves it empty and its
	// IPAM hands out a /24 per network from a pool large enough not to need
	// the small-prefix request.
	if !podman && dockerVersionAtLeast(info.ServerVersion, 29, 0) {
		opts.Subnet = sessionNetworkDynamicSubnet
	}

	id, err := networkCreate(ctx, opts)
	if err == nil || dockercmd.IsAlreadyExists(err) {
		return id, err
	}
	if opts.Subnet != "" {
		// A daemon or CLI that reports a new-enough version can still reject
		// the unspecified-address subnet request. Fall back to Docker's
		// default IPAM.
		opts.Subnet = ""
		return networkCreate(ctx, opts)
	}
	if podman && opts.Options[podmanIsolateOption] == podmanIsolateStrict && dockercmd.IsUnsupportedIsolateValue(err) {
		// Older Podman CLIs parse isolate as a boolean at network creation,
		// even when the installed netavark supports strict. isolate=true blocks
		// traffic only between networks that also opt in, which every session does;
		// containers on podman's default network keep a route into the session.
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

func inspectedSessionNetworkRef(info dockercmd.NetworkInspectResponse, meta backend.SessionMeta) (sessionNetworkRef, error) {
	id := strings.TrimSpace(info.ID)
	if id == "" {
		return sessionNetworkRef{}, fmt.Errorf("docker network %q has no inspect ID", info.Name)
	}
	return sessionNetworkRef{
		Name:        sessionNetworkName(meta.Name),
		ID:          id,
		Container:   strings.TrimSpace(meta.Name),
		ProjectHash: strings.TrimSpace(meta.ProjectHash),
	}, nil
}

func attachedSessionNetworkError(containerName string, networkName string) error {
	return fmt.Errorf("session %s already running or requires stale-resource cleanup: Docker network %q has attached endpoints; run 'enclave stop %s' and retry", containerName, networkName, containerName)
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
			if attempt < retries && sessionOwnsEndpoints(attached, ref.Container) {
				time.Sleep(sessionNetworkRemoveRetryWait)
				continue
			}
			logx.Debugf("Leaving per-session network %s in place because it still has attached endpoints: %s", ref.Name, strings.Join(attached, ", "))
			return nil
		}
		exists, err := sessionRuntimeExists(ctx, ref.Container)
		if err != nil {
			return err
		}
		if exists {
			if attempt < retries {
				time.Sleep(sessionNetworkRemoveRetryWait)
				continue
			}
			logx.Debugf("Leaving per-session network %s in place because a session or gateway container still reserves its name", ref.Name)
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
				time.Sleep(sessionNetworkRemoveRetryWait)
				continue
			}
			return err
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
	if strings.TrimSpace(info.ID) == "" || info.Name != name ||
		!sessionNetworkOwnedBy(info, containerName, projectHash) {
		return sessionNetworkRef{}, false
	}
	return sessionNetworkRef{
		Name:        name,
		ID:          strings.TrimSpace(info.ID),
		Container:   strings.TrimSpace(containerName),
		ProjectHash: strings.TrimSpace(projectHash),
	}, true
}

// gcSessionNetworks quietly removes leaked per-session networks at session
// start; `enclave cleanup --ephemeral` runs the same selection through
// PruneStaleSessionNetworks.
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

// PruneStaleSessionNetworks removes the per-session networks whose session
// container is gone, has no endpoints, and is past the grace period, and
// returns their names. ownerPrefix limits it to networks whose owning
// container name starts with the prefix (empty selects every project and
// tool). With dryRun it only reports them.
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
		if _, ok := staleSessionNetworkOwner(info, now); ok && strings.TrimSpace(info.ID) != "" {
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
		// The label and grace checks are cheap, so the engine is only asked
		// about endpoints for networks that are otherwise collectable.
		hasEndpoints, err := sessionNetworkHasEndpoints(ctx, info)
		if err != nil || hasEndpoints {
			continue
		}
		stale = append(stale, sessionNetworkRef{
			Name:        info.Name,
			ID:          info.ID,
			Container:   owner,
			ProjectHash: projectHash,
		})
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

func staleSessionNetworkOwner(info dockercmd.NetworkInspectResponse, now time.Time) (string, bool) {
	owner, _, ok := sessionNetworkOwner(info)
	if !ok || len(info.Containers) > 0 {
		return "", false
	}
	if !sessionNetworkPastGracePeriod(info, now) {
		return "", false
	}
	return owner, true
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
