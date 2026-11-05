// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/gateway"
	"enclave/internal/peergroup"
	"enclave/internal/util"
)

var _ backend.PeerGroupBackend = (*Backend)(nil)

// Hub hardening (CR-P1P-2): the supervisor needs NET_ADMIN and NET_RAW for
// rules, conntrack and gratuitous ARP, SETUID/SETGID to drop to the proxy uid,
// KILL to signal it and DAC_READ_SEARCH to hash the proxy-owned secrets file.
var hubCapabilities = []string{"NET_ADMIN", "NET_RAW", "SETUID", "SETGID", "KILL", "DAC_READ_SEARCH"}

const (
	hubStopTimeout   = 5 * time.Second
	a2aAgentBinary   = "enclave-a2a-agent"
	a2aHubBinary     = "/usr/local/bin/enclave-a2a-hub"
	hubTrueLabel     = "true"
	secretsHelperDir = "/staging"
)

var (
	hubReadyTimeout      = 20 * time.Second
	hubReadyPollInterval = 100 * time.Millisecond

	hubNetworkConnect    = dockercmd.NetworkConnect
	hubNetworkDisconnect = dockercmd.NetworkDisconnect
	hubEndpoints         = dockercmd.ContainerNetworkEndpoints
	hubContainerInspect  = dockercmd.ContainerInspect
	hubContainerList     = dockercmd.ContainerList
	hubSignal            = dockercmd.SignalContainer
	hubRunDetached       = dockercmd.RunDetached
	hubContainerStart    = dockercmd.ContainerStart
	hubContainerRemove   = dockercmd.ContainerRemove
	workerExec           = dockercmd.ExecWithInput
	hubExecStream        = dockercmd.ExecStream
	gatewayOverlayReload = gateway.ReloadOverlay
)

// errHubNotCreated: a missing hub can only be created on a first bridge (CR-A6-1).
var errHubNotCreated = errors.New("A2A hub does not exist and HubSpec.Initial names no bridge to create it on")

// HubContainerName is the hub container of group (interfaces.md section 7).
func HubContainerName(group string) string {
	return peergroup.HubContainerPrefix + group
}

// hubState is a hub container as inspected.
type hubState struct {
	Exists          bool
	Running         bool
	Ref             backend.HubRef
	GroupGeneration string
	// Endpoints maps network name to the engine's endpoint view.
	Endpoints map[string]dockercmd.ContainerNetworkEndpoint
}

func (b *Backend) inspectHub(ctx context.Context, group string) (hubState, error) {
	name := HubContainerName(group)
	info, err := hubContainerInspect(ctx, name)
	if err != nil {
		if dockercmd.IsNotFound(err) {
			return hubState{}, nil
		}
		return hubState{}, fmt.Errorf("inspect A2A hub %s: %w", name, err)
	}
	if info.Config == nil || info.Config.Labels[peergroup.LabelHub] != hubTrueLabel || info.Config.Labels[peergroup.LabelGroup] != group {
		return hubState{}, fmt.Errorf("container %s exists but is not the A2A hub of group %s", name, group)
	}
	endpoints, err := hubEndpoints(ctx, name)
	if err != nil {
		return hubState{}, fmt.Errorf("inspect A2A hub %s networks: %w", name, err)
	}
	return hubState{
		Exists:          true,
		Running:         info.State != nil && info.State.Running,
		Ref:             backend.HubRef{Group: group, ID: info.ID, Name: name},
		GroupGeneration: info.Config.Labels[peergroup.LabelGroupGeneration],
		Endpoints:       endpoints,
	}, nil
}

// StartHub creates the hub on spec.Initial when it is missing, starts a
// stopped hub, or returns the running one.
func (b *Backend) StartHub(ctx context.Context, spec backend.HubSpec) (backend.HubRef, error) {
	if err := peergroup.ValidateGroupName(spec.Group); err != nil {
		return backend.HubRef{}, err
	}
	state, err := b.inspectHub(ctx, spec.Group)
	if err != nil {
		return backend.HubRef{}, err
	}
	if !state.Exists {
		if spec.Initial == nil {
			return backend.HubRef{}, errHubNotCreated
		}
		return b.createHub(ctx, spec, *spec.Initial)
	}
	if state.GroupGeneration != spec.GroupGeneration {
		return backend.HubRef{}, fmt.Errorf("A2A hub %s belongs to group generation %q, not %q", state.Ref.Name, state.GroupGeneration, spec.GroupGeneration)
	}
	if state.Running {
		return state.Ref, nil
	}
	if err := b.restartHub(ctx, spec, state); err != nil {
		return backend.HubRef{}, err
	}
	return state.Ref, nil
}

// restartHub starts a stopped hub and waits for a new epoch in both status files.
func (b *Backend) restartHub(ctx context.Context, spec backend.HubSpec, state hubState) error {
	if err := dropMissingHubNetworks(ctx, state); err != nil {
		return err
	}
	before := readHubEpochs(spec.StatusDir)
	if err := hubContainerStart(ctx, state.Ref.Name); err != nil {
		return fmt.Errorf("start A2A hub %s: %w", state.Ref.Name, err)
	}
	return waitHubReady(ctx, state.Ref.Name, spec.StatusDir, spec.GroupGeneration, before)
}

// dropMissingHubNetworks disconnects a stopped hub from networks that no
// longer exist; Docker otherwise refuses to start it (P1-D finding 5).
func dropMissingHubNetworks(ctx context.Context, state hubState) error {
	for name, endpoint := range state.Endpoints {
		exists := true
		if _, err := networkInspect(ctx, name); err != nil {
			if !dockercmd.IsNotFound(err) {
				return fmt.Errorf("inspect network %s of A2A hub %s: %w", name, state.Ref.Name, err)
			}
			exists = false
		}
		if exists && strings.TrimSpace(endpoint.NetworkID) != "" {
			continue
		}
		if err := hubNetworkDisconnect(ctx, name, state.Ref.Name, false); err != nil {
			return fmt.Errorf("disconnect A2A hub %s from missing network %s: %w", state.Ref.Name, name, err)
		}
	}
	return nil
}

// createHub creates and starts the hub on its first member's bridge with a
// static address (CR-A6-1), then waits until both readers report an epoch.
func (b *Backend) createHub(ctx context.Context, spec backend.HubSpec, att backend.HubAttachment) (backend.HubRef, error) {
	config, hostConfig, err := hubRunConfig(spec, att, util.IsSELinuxEnforcing())
	if err != nil {
		return backend.HubRef{}, err
	}
	name := HubContainerName(spec.Group)
	before := readHubEpochs(spec.StatusDir)
	id, err := hubRunDetached(ctx, config, hostConfig, name)
	if err != nil {
		// A container created before the start failed would block the next join.
		_ = hubContainerRemove(context.WithoutCancel(ctx), name, true, false)
		return backend.HubRef{}, fmt.Errorf("create A2A hub %s: %w", name, err)
	}
	ref := backend.HubRef{Group: spec.Group, ID: firstLine(id), Name: name}
	if err := waitHubReady(ctx, name, spec.StatusDir, spec.GroupGeneration, before); err != nil {
		return ref, err
	}
	return ref, nil
}

func hubRunConfig(spec backend.HubSpec, att backend.HubAttachment, selinux bool) (*dockercmd.ContainerConfig, *dockercmd.HostConfig, error) {
	if err := peergroup.ValidateGroupName(spec.Group); err != nil {
		return nil, nil, err
	}
	if !peergroup.Generation(spec.GroupGeneration).Valid() {
		return nil, nil, fmt.Errorf("invalid group generation %q", spec.GroupGeneration)
	}
	if strings.TrimSpace(spec.Image) == "" {
		return nil, nil, fmt.Errorf("A2A hub image is not set")
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(att.IPv4))
	if err != nil || !addr.Is4() || strings.TrimSpace(att.Network) == "" {
		return nil, nil, fmt.Errorf("A2A hub needs a first attachment with a network and a static IPv4 address, got %q on %q", att.IPv4, att.Network)
	}
	binds, mounts, err := bindMounts([]dockercmd.Mount{
		{Type: dockercmd.MountTypeBind, Source: spec.SnapshotDir, Target: peergroup.HubSnapshotMountPath, ReadOnly: true},
		{Type: dockercmd.MountTypeBind, Source: spec.StatusDir, Target: peergroup.HubStatusMountPath},
		{Type: dockercmd.MountTypeBind, Source: spec.AuditDir, Target: peergroup.HubAuditMountPath},
	}, selinux)
	if err != nil {
		return nil, nil, err
	}
	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels[peergroup.LabelHub] = hubTrueLabel
	labels[peergroup.LabelGroup] = spec.Group
	labels[peergroup.LabelGroupGeneration] = spec.GroupGeneration
	env := []backend.EnvVar{
		{Name: a2a.EnvGroup, Value: spec.Group},
		{Name: a2a.EnvGroupGeneration, Value: spec.GroupGeneration},
		{Name: a2a.EnvHubPort, Value: strconv.Itoa(peergroup.HubPort)},
		{Name: a2a.EnvHubProxyUID, Value: strconv.Itoa(peergroup.HubProxyUID)},
		{Name: a2a.EnvHubSnapshotDir, Value: peergroup.HubSnapshotMountPath},
		{Name: a2a.EnvHubStatusDir, Value: peergroup.HubStatusMountPath},
		{Name: a2a.EnvHubAuditDir, Value: peergroup.HubAuditMountPath},
	}
	fixed := map[string]bool{}
	for _, entry := range env {
		fixed[entry.Name] = true
	}
	for _, entry := range spec.Env {
		if entry.Name == "" || fixed[entry.Name] {
			continue
		}
		env = append(env, entry)
	}
	config := &dockercmd.ContainerConfig{Image: spec.Image, Labels: labels}
	for _, entry := range env {
		config.Env = append(config.Env, entry.Name+"="+entry.Value)
	}
	hostConfig := &dockercmd.HostConfig{
		NetworkMode:    dockercmd.NetworkMode(att.Network),
		IPv4Address:    addr.String(),
		CapDrop:        []string{"ALL"},
		CapAdd:         append([]string(nil), hubCapabilities...),
		ReadonlyRootfs: true,
		SecurityOpt:    []string{"no-new-privileges"},
		Sysctls:        map[string]string{"net.ipv4.ip_forward": "0"},
		// iptables needs /run/xtables.lock; nothing else in the image is writable.
		Tmpfs:  map[string]string{"/run": "", "/tmp": ""},
		Binds:  binds,
		Mounts: mounts,
	}
	return config, hostConfig, nil
}

// bindMounts validates bind sources and splits them for SELinux relabeling.
func bindMounts(mounts []dockercmd.Mount, selinux bool) ([]string, []dockercmd.Mount, error) {
	for _, m := range mounts {
		if !filepath.IsAbs(m.Source) || strings.ContainsAny(m.Source, ",:") {
			return nil, nil, fmt.Errorf("A2A hub mount source %q must be an absolute path without ',' or ':'", m.Source)
		}
	}
	binds, remaining := dockercmd.SplitMountsForSELinuxWith(mounts, selinux)
	return binds, remaining, nil
}

func firstLine(out string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(line)
}

// readHubEpochs returns the epochs in the status files before a (re)start;
// status files survive a hub restart with the old epoch (P1-P).
func readHubEpochs(statusDir string) map[string]bool {
	epochs := map[string]bool{}
	for _, name := range []string{peergroup.SupervisorStatus, peergroup.ProxyStatus} {
		if status, err := readHubStatus(filepath.Join(statusDir, name)); err == nil && status.Epoch != "" {
			epochs[status.Epoch] = true
		}
	}
	return epochs
}

func readHubStatus(path string) (peergroup.StatusFile, error) {
	var status peergroup.StatusFile
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return status, err
	}
	if status.SchemaVersion != peergroup.SchemaVersion {
		return status, fmt.Errorf("status file %s has schemaVersion %q", path, status.SchemaVersion)
	}
	return status, nil
}

// waitHubReady waits for both readers to report a new epoch of this group
// generation; only then has the supervisor installed its SIGHUP handling.
func waitHubReady(ctx context.Context, name, statusDir, groupGeneration string, before map[string]bool) error {
	deadline := time.Now().Add(hubReadyTimeout)
	for {
		ready := true
		for _, file := range []string{peergroup.SupervisorStatus, peergroup.ProxyStatus} {
			status, err := readHubStatus(filepath.Join(statusDir, file))
			if err != nil || status.Epoch == "" || before[status.Epoch] || string(status.GroupGeneration) != groupGeneration {
				ready = false
				break
			}
		}
		if ready {
			return nil
		}
		info, err := hubContainerInspect(ctx, name)
		if err != nil {
			return fmt.Errorf("inspect A2A hub %s while waiting for readiness: %w", name, err)
		}
		if info.State == nil || !info.State.Running {
			return fmt.Errorf("A2A hub %s exited before both readers reported a new epoch", name)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s waiting for A2A hub %s to report a new epoch", hubReadyTimeout, name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(hubReadyPollInterval):
		}
	}
}

// StopHub stops and removes the hub; every attachment must already be gone.
func (b *Backend) StopHub(ctx context.Context, ref backend.HubRef) error {
	name := hubRefName(ref)
	endpoints, err := hubEndpoints(ctx, name)
	if err != nil {
		if isEngineNoSuchContainer(err) {
			return nil
		}
		return fmt.Errorf("inspect A2A hub %s networks: %w", name, err)
	}
	if len(endpoints) > 0 {
		networks := make([]string, 0, len(endpoints))
		for network := range endpoints {
			networks = append(networks, network)
		}
		sort.Strings(networks)
		return fmt.Errorf("A2A hub %s is still attached to %s", name, strings.Join(networks, ", "))
	}
	return removeHubContainer(ctx, name)
}

func removeHubContainer(ctx context.Context, name string) error {
	timeout := hubStopTimeout
	if err := dockercmd.ContainerStop(ctx, name, &timeout); err != nil && !dockercmd.IsNotFound(err) {
		return fmt.Errorf("stop A2A hub %s: %w", name, err)
	}
	if err := dockercmd.ContainerRemove(ctx, name, true, false); err != nil && !dockercmd.IsNotFound(err) {
		return fmt.Errorf("remove A2A hub %s: %w", name, err)
	}
	return nil
}

// ReloadHub nudges both hub readers; commit is decided by the status files.
func (b *Backend) ReloadHub(ctx context.Context, ref backend.HubRef) error {
	return hubSignal(ctx, hubRefName(ref), dockercmd.SignalReload)
}

// ConnectHub attaches the hub at a static address. A failed connect always
// gets a disconnect and an inspect check, because both engines keep endpoint
// state after a failed attach (CR-P1P-3).
func (b *Backend) ConnectHub(ctx context.Context, ref backend.HubRef, att backend.HubAttachment) error {
	name := hubRefName(ref)
	addr, err := netip.ParseAddr(strings.TrimSpace(att.IPv4))
	if err != nil || !addr.Is4() {
		return fmt.Errorf("A2A hub attachment needs a static IPv4 address, got %q", att.IPv4)
	}
	used, err := usedNetworkAddresses(ctx, att.Network, name)
	if err != nil {
		return err
	}
	if holder, taken := used[addr]; taken {
		return fmt.Errorf("address %s on network %s is held by %s", addr, att.Network, holder)
	}
	connectErr := hubNetworkConnect(ctx, att.Network, name, addr.String())
	if connectErr == nil {
		return nil
	}
	var conflict *dockercmd.EndpointConflictError
	if errors.As(connectErr, &conflict) {
		return connectErr
	}
	ctx = context.WithoutCancel(ctx)
	_ = hubNetworkDisconnect(ctx, att.Network, name, false)
	stale, verifyErr := hubAttached(ctx, name, att.Network)
	if verifyErr != nil || !stale {
		return connectErr
	}
	if err := clearStaleHubEndpoint(ctx, name, att.Network, addr); err != nil {
		if !errors.Is(connectErr, dockercmd.ErrStaleEndpoint) {
			connectErr = errors.Join(connectErr, dockercmd.ErrStaleEndpoint)
		}
		return errors.Join(connectErr, err)
	}
	return connectErr
}

// clearStaleHubEndpoint removes the endpoint config Docker keeps after a
// failed connect on a running container (A6): a successful connect at
// another free address overwrites it and can then be disconnected, without
// stopping a hub other members use.
func clearStaleHubEndpoint(ctx context.Context, hub, network string, failed netip.Addr) error {
	used, err := usedNetworkAddresses(ctx, network, hub)
	if err != nil {
		return err
	}
	used[failed] = "failed connect"
	info, err := networkInspect(ctx, network)
	if err != nil {
		return err
	}
	alternate, err := hubAddressFor(sessionNetworkFromInspect(info).Subnet, used)
	if err != nil {
		return err
	}
	if err := hubNetworkConnect(ctx, network, hub, alternate.String()); err != nil {
		return fmt.Errorf("overwrite stale endpoint at %s: %w", alternate, err)
	}
	if err := hubNetworkDisconnect(ctx, network, hub, false); err != nil {
		return err
	}
	if attached, err := hubAttached(ctx, hub, network); err != nil || attached {
		return fmt.Errorf("A2A hub %s still lists network %s after clearing a stale endpoint", hub, network)
	}
	return nil
}

// DisconnectHub detaches the hub and verifies the endpoint is gone.
func (b *Backend) DisconnectHub(ctx context.Context, ref backend.HubRef, network string) error {
	name := hubRefName(ref)
	if err := hubNetworkDisconnect(ctx, network, name, false); err != nil {
		return fmt.Errorf("disconnect A2A hub %s from %s: %w", name, network, err)
	}
	attached, err := hubAttached(ctx, name, network)
	if err != nil {
		if isEngineNoSuchContainer(err) {
			return nil
		}
		return fmt.Errorf("verify A2A hub %s left %s: %w", name, network, err)
	}
	if !attached {
		return nil
	}
	if err := hubNetworkDisconnect(ctx, network, name, true); err != nil {
		return fmt.Errorf("force-disconnect A2A hub %s from %s: %w", name, network, err)
	}
	if attached, err = hubAttached(ctx, name, network); err == nil && attached {
		return fmt.Errorf("A2A hub %s still lists network %s after disconnect: %w", name, network, dockercmd.ErrStaleEndpoint)
	}
	return nil
}

func hubAttached(ctx context.Context, container, network string) (bool, error) {
	endpoints, err := hubEndpoints(ctx, container)
	if err != nil {
		return false, err
	}
	_, ok := findHubEndpoint(endpoints, network)
	return ok, nil
}

func findHubEndpoint(endpoints map[string]dockercmd.ContainerNetworkEndpoint, network string) (dockercmd.ContainerNetworkEndpoint, bool) {
	if endpoint, ok := endpoints[network]; ok {
		return endpoint, true
	}
	for _, endpoint := range endpoints {
		if endpoint.NetworkID != "" && endpoint.NetworkID == network {
			return endpoint, true
		}
	}
	return dockercmd.ContainerNetworkEndpoint{}, false
}

// usedNetworkAddresses maps the IPv4 addresses on network to their holders,
// except exclude (the hub itself, which may hold a stale entry).
var usedNetworkAddresses = func(ctx context.Context, network, exclude string) (map[netip.Addr]string, error) {
	names, err := networkAttachedContainers(ctx, network)
	if err != nil {
		return nil, fmt.Errorf("list containers on network %s: %w", network, err)
	}
	used := map[netip.Addr]string{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || name == exclude {
			continue
		}
		endpoints, err := hubEndpoints(ctx, name)
		if err != nil {
			if isEngineNoSuchContainer(err) {
				continue
			}
			return nil, fmt.Errorf("inspect networks of %s: %w", name, err)
		}
		endpoint, ok := findHubEndpoint(endpoints, network)
		if !ok {
			continue
		}
		for _, value := range []string{endpoint.IPAddress, endpoint.StaticIPv4()} {
			if addr, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil {
				used[addr] = name
			}
		}
	}
	return used, nil
}

// SignalWorker runs "enclave-a2a-agent ctl" in the worker; the request goes
// on stdin, never on argv.
func (b *Backend) SignalWorker(ctx context.Context, ref backend.SessionRef, req a2a.CtlRequest) (backend.WorkerSignalResult, error) {
	name := sessionRefName(ref)
	if name == "" {
		return backend.WorkerSignalResult{}, fmt.Errorf("signal worker: no session")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return backend.WorkerSignalResult{}, err
	}
	out, runErr := workerExec(ctx, name, []string{a2aAgentBinary, "ctl"}, payload)
	var resp a2a.CtlResponse
	if decodeErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); decodeErr != nil {
		if runErr != nil {
			return backend.WorkerSignalResult{}, fmt.Errorf("worker %s ctl %s: %w", name, req.Op, runErr)
		}
		return backend.WorkerSignalResult{}, fmt.Errorf("worker %s ctl %s: decode response: %w", name, req.Op, decodeErr)
	}
	result := backend.WorkerSignalResult{Response: resp}
	if runErr != nil || !resp.OK {
		reason := resp.Error
		if reason == "" && runErr != nil {
			reason = runErr.Error()
		}
		return result, fmt.Errorf("worker %s ctl %s failed: %s", name, req.Op, reason)
	}
	return result, nil
}

// HubCall runs `enclave-a2a-hub call <path>` in the hub as the proxy uid
// (interfaces.md section 11) and streams its stdout. stdin carries the
// invocation credential line and the request body; Close ends the call.
func (b *Backend) HubCall(ctx context.Context, ref backend.HubRef, path string, stdin io.Reader) (io.ReadCloser, error) {
	if !strings.HasPrefix(path, "/") || strings.ContainsFunc(path, unicode.IsSpace) {
		return nil, fmt.Errorf("hub call path %q", path)
	}
	return hubExecStream(ctx, hubRefName(ref), strconv.Itoa(peergroup.HubProxyUID), []string{a2aHubBinary, "call", path}, stdin)
}

// ReloadGatewayOverlay signals the session's gateway and waits for the overlay
// marker of generation.
func (b *Backend) ReloadGatewayOverlay(ctx context.Context, ref backend.SessionRef, generation uint64) error {
	return gatewayOverlayReload(ctx, gateway.ContainerName(sessionRefName(ref)), generation)
}

// ListHubs returns every A2A hub on the engine, running or not.
func (b *Backend) ListHubs(ctx context.Context) ([]backend.HubInfo, error) {
	filters := dockercmd.NewFilters()
	filters.Add("label", peergroup.LabelHub+"="+hubTrueLabel)
	summaries, err := hubContainerList(ctx, dockercmd.ListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, err
	}
	var hubs []backend.HubInfo
	for _, summary := range summaries {
		group := summary.Labels[peergroup.LabelGroup]
		name := dockercmd.PrimaryContainerName(summary)
		if summary.Labels[peergroup.LabelHub] != hubTrueLabel || peergroup.ValidateGroupName(group) != nil || name != HubContainerName(group) {
			continue
		}
		endpoints, err := hubEndpoints(ctx, name)
		if err != nil {
			if isEngineNoSuchContainer(err) {
				continue
			}
			return nil, fmt.Errorf("inspect A2A hub %s networks: %w", name, err)
		}
		info := backend.HubInfo{
			Ref:             backend.HubRef{Group: group, ID: summary.ID, Name: name},
			GroupGeneration: summary.Labels[peergroup.LabelGroupGeneration],
			Running:         summary.State == "running",
		}
		for _, network := range sortedEndpointNames(endpoints) {
			endpoint := endpoints[network]
			addr := endpoint.StaticIPv4()
			if addr == "" {
				addr = endpoint.IPAddress
			}
			info.Attachments = append(info.Attachments, backend.HubAttachment{Network: network, IPv4: addr})
		}
		hubs = append(hubs, info)
	}
	sort.Slice(hubs, func(i, j int) bool { return hubs[i].Ref.Group < hubs[j].Ref.Group })
	return hubs, nil
}

func sortedEndpointNames(endpoints map[string]dockercmd.ContainerNetworkEndpoint) []string {
	names := make([]string, 0, len(endpoints))
	for name := range endpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func hubRefName(ref backend.HubRef) string {
	if name := strings.TrimSpace(ref.Name); name != "" {
		return name
	}
	if id := strings.TrimSpace(ref.ID); id != "" {
		return id
	}
	return HubContainerName(ref.Group)
}

// hubAddressFor returns the hub's static address on a session subnet: the
// last usable IPv4 address not held by another endpoint (Q4 refined).
func hubAddressFor(subnet string, used map[netip.Addr]string) (netip.Addr, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(subnet))
	if err != nil || !prefix.Addr().Is4() {
		return netip.Addr{}, fmt.Errorf("session network has no IPv4 subnet (%q); --peer-group needs one", subnet)
	}
	prefix = prefix.Masked()
	if prefix.Bits() > 29 {
		return netip.Addr{}, fmt.Errorf("session subnet %s is too small for an A2A hub address", prefix)
	}
	base := prefix.Addr().As4()
	first := binary.BigEndian.Uint32(base[:])
	broadcast := first | (1<<(32-prefix.Bits()) - 1)
	// first+1 is the bridge gateway on both engines.
	for candidate := broadcast - 1; candidate > first+1; candidate-- {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], candidate)
		addr := netip.AddrFrom4(b)
		if _, taken := used[addr]; !taken {
			return addr, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("no free address for the A2A hub in %s", prefix)
}
