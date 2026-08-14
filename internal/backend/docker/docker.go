// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/gateway"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

type Options struct {
	Host                model.Host
	Paths               model.Paths
	ReconcileScriptPath string
	ForceRebuild        bool
	NoRebuild           bool
	NetworkLogMode      string
	DevcontainerRunArgs []string
	ProjectDir          string
	ProjectMount        string
}

type Backend struct {
	opts    Options
	storage *StoreManager
}

func New(opts Options) *Backend {
	b := &Backend{opts: opts}
	b.storage = &StoreManager{host: opts.Host}
	return b
}

func (b *Backend) Name() string { return backend.NameDocker }

func (b *Backend) Check(ctx context.Context) error {
	if err := dockercmd.Ping(ctx); err != nil {
		return fmt.Errorf("docker daemon is not running: %w", err)
	}
	return nil
}

func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{RestrictedNetwork: true, SecretHTTPRelease: true}
}

func (b *Backend) Storage() backend.StoreManager { return b.storage }

func (b *Backend) Run(ctx context.Context, req backend.Request, attach backend.AttachIO) (backend.ExitStatus, error) {
	// Post-run credential sync runs on every foreground outcome (success,
	// tool failure, even setup failure), matching the previous runtime
	// behavior. Registered first so it runs after the gateway teardown.
	if req.AuthSync != nil {
		defer b.runRequestAuthSync(req)
	}
	spec, err := b.prepareRun(ctx, req)
	if err != nil {
		return backend.ExitStatus{}, err
	}
	if spec.cleanup != nil {
		defer spec.cleanup()
	}
	err = runForeground(ctx, spec.config, spec.hostConfig, spec.name, attach)
	return exitStatus(err), neutralizeExitError(err)
}

func (b *Backend) Start(ctx context.Context, req backend.Request) (backend.SessionRef, error) {
	spec, err := b.prepareRun(ctx, req)
	if err != nil {
		return backend.SessionRef{}, err
	}
	id, err := dockercmd.RunDetachedInteractive(ctx, spec.config, spec.hostConfig, spec.name)
	if err != nil {
		if spec.cleanup != nil {
			spec.cleanup()
		}
		return backend.SessionRef{}, err
	}
	return backend.SessionRef{Name: spec.name, ID: id}, nil
}

func (b *Backend) List(ctx context.Context, filter backend.SessionFilter) ([]backend.Session, error) {
	filters := dockercmd.NewFilters()
	for _, pair := range sessionListFilterPairs(filter) {
		filters.Add(pair[0], pair[1])
	}
	containers, err := dockercmd.ContainerList(ctx, dockercmd.ListOptions{All: filter.All, Filters: filters})
	if err != nil {
		return nil, err
	}
	sessions := make([]backend.Session, 0, len(containers))
	for _, c := range containers {
		session, ok := sessionFromSummary(c)
		if ok {
			sessions = append(sessions, session)
		}
	}
	fillGatewayPorts(ctx, sessions)
	return sessions, nil
}

func (b *Backend) Inspect(ctx context.Context, ref backend.SessionRef) (*backend.Session, error) {
	name := sessionRefName(ref)
	info, err := dockercmd.ContainerInspect(ctx, name)
	if err != nil {
		return nil, err
	}
	summary := dockercmd.Summary{ID: info.ID, Labels: map[string]string{}}
	if info.Name != "" {
		summary.Names = []string{info.Name}
	}
	if info.Config != nil {
		summary.Image = info.Config.Image
		summary.Labels = info.Config.Labels
	}
	if info.State != nil {
		summary.State = info.State.Status
	}
	if info.NetworkSettings != nil {
		summary.Ports = info.NetworkSettings.Ports
	}
	if info.Created != "" {
		if created, parseErr := time.Parse(time.RFC3339Nano, info.Created); parseErr == nil {
			summary.Created = created.Unix()
		}
	}
	session, ok := sessionFromSummary(summary)
	if !ok {
		return nil, fmt.Errorf("session %q is not managed by %s", name, model.AppName)
	}
	sessions := []backend.Session{session}
	fillGatewayPorts(ctx, sessions)
	return &sessions[0], nil
}

func (b *Backend) Attach(ctx context.Context, ref backend.SessionRef, attach backend.AttachIO) error {
	detachKeys := strings.TrimSpace(attach.DetachKeys)
	if detachKeys == "" {
		detachKeys = model.DetachKeysDefault
	}
	return dockercmd.AttachInteractive(ctx, sessionRefName(ref), detachKeys)
}

func (b *Backend) Stop(ctx context.Context, ref backend.SessionRef, opts backend.StopOptions) error {
	name := sessionRefName(ref)
	timeout := opts.Timeout
	var timeoutPtr *time.Duration
	if timeout > 0 {
		timeoutPtr = &timeout
	}
	stopErr := dockercmd.ContainerStop(ctx, name, timeoutPtr)
	if opts.Finalize {
		if err := b.finalizeManagedContainerAuth(ctx, name); err != nil && stopErr == nil {
			stopErr = err
		}
	}
	gateway.Stop(name)
	return stopErr
}

func (b *Backend) Remove(ctx context.Context, ref backend.SessionRef) error {
	name := sessionRefName(ref)
	if err := b.finalizeManagedContainerAuth(ctx, name); err != nil {
		return fmt.Errorf("finalize auth before removing container %s: %w", name, err)
	}
	return dockercmd.ContainerRemove(ctx, name, true, false)
}

func (b *Backend) RemoveWithoutFinalize(ctx context.Context, ref backend.SessionRef) error {
	return dockercmd.ContainerRemove(ctx, sessionRefName(ref), true, false)
}

var execInteractive = dockercmd.ExecInteractive

func (b *Backend) Exec(ctx context.Context, ref backend.SessionRef, req backend.ExecRequest, attach backend.AttachIO) error {
	execErr := execInteractive(ctx, sessionRefName(ref), req.Argv, req.User, req.TTY)
	// Sync credentials from the session's config store on every exec outcome so
	// they are durable immediately, not only at a later stop/remove.
	b.syncAfterExec(sessionRefName(ref))
	return neutralizeExitError(execErr)
}

func (b *Backend) ExecOutput(ctx context.Context, ref backend.SessionRef, argv []string, user string) (string, error) {
	return dockercmd.ExecCapture(ctx, sessionRefName(ref), argv, user)
}

func (b *Backend) Logs(ctx context.Context, ref backend.SessionRef, opts backend.LogOptions) (string, error) {
	tail := opts.Tail
	if tail <= 0 {
		tail = 200
	}
	return dockercmd.ContainerLogsTail(ctx, sessionRefName(ref), tail)
}

type runSpec struct {
	config     *dockercmd.ContainerConfig
	hostConfig *dockercmd.HostConfig
	name       string
	cleanup    func()
}

func (b *Backend) prepareRun(ctx context.Context, req backend.Request) (runSpec, error) {
	if err := backend.Validate(req, b.Capabilities()); err != nil {
		return runSpec{}, err
	}
	b.warnInsecureDockerConfig(ctx)
	spec := b.dockerConfig(req)
	if !req.Detached && len(b.opts.DevcontainerRunArgs) > 0 {
		var runtimeUIDRemapEnv []string
		if req.RuntimeUIDRemap {
			runtimeUIDRemapEnv = runtimeUIDRemapEnvEntries(spec.config.Env)
		}
		b.applyDevcontainerRunArgs(spec.config, spec.hostConfig)
		spec.config.Env = append(spec.config.Env, runtimeUIDRemapEnv...)
	}
	if req.Network.Mode == backend.NetworkModeRestricted {
		gatewayName, cleanup, err := b.startGateway(ctx, req)
		if err != nil {
			return runSpec{}, err
		}
		spec.hostConfig.NetworkMode = dockercmd.NetworkMode("container:" + gatewayName)
		spec.cleanup = cleanup
	} else {
		spec.config.ExposedPorts = portSet(req.Ports)
		spec.hostConfig.PortBindings = portMap(req.Ports)
		if len(req.Network.IdeBridgePorts) > 0 {
			spec.hostConfig.ExtraHosts = append(spec.hostConfig.ExtraHosts, "host.docker.internal:host-gateway")
		}
	}
	applySELinuxMounts(spec.hostConfig, util.IsSELinuxEnforcing())
	return spec, nil
}

func (b *Backend) dockerConfig(req backend.Request) runSpec {
	env := make([]string, 0, len(req.Env))
	for _, entry := range req.Env {
		if entry.Name == "" {
			continue
		}
		env = append(env, entry.Name+"="+entry.Value)
	}
	config := &dockercmd.ContainerConfig{
		Image:      req.Image,
		Cmd:        append([]string(nil), req.Argv...),
		Entrypoint: append([]string(nil), req.Entrypoint...),
		Env:        env,
		WorkingDir: req.WorkingDir,
		User:       req.User,
		Hostname:   req.Hostname,
		Labels:     labelsFromRequest(req),
	}
	mounts := make([]dockercmd.Mount, 0, len(req.Mounts)+len(req.Stores))
	for _, m := range req.Mounts {
		mounts = append(mounts, dockerMount(m))
	}
	for _, store := range req.Stores {
		if strings.TrimSpace(store.ContainerPath) == "" {
			continue
		}
		dir, err := b.storage.storeDir(store.Key, store.Kind)
		if err != nil {
			logx.Warnf("Skipping store mount %s: %v", store.ContainerPath, err)
			continue
		}
		// Ensure the bind source exists as the invoking user; otherwise Docker
		// would create it as root on first mount.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			logx.Warnf("Failed to ensure store dir %s: %v", dir, err)
		}
		mounts = append(mounts, dockercmd.Mount{Type: dockercmd.MountTypeBind, Source: dir, Target: store.ContainerPath, ReadOnly: store.ReadOnly})
	}
	init := true
	hostConfig := &dockercmd.HostConfig{
		AutoRemove: !req.Detached,
		Mounts:     mounts,
		Init:       &init,
	}
	if !req.Security.Admin {
		applyContainerHardening(hostConfig)
	}
	return runSpec{config: config, hostConfig: hostConfig, name: req.Session.Name}
}

func labelsFromRequest(req backend.Request) map[string]string {
	meta := req.Session
	labels := map[string]string{
		model.LabelConfigKey: "default",
	}
	if meta.Tool != "" {
		labels[model.LabelAgent] = meta.Tool
	}
	if meta.ProjectHash != "" {
		labels[model.LabelHash] = meta.ProjectHash
	}
	if meta.Worktree != "" {
		labels[model.LabelWorktree] = meta.Worktree
	}
	if meta.RealWorktree != "" {
		labels[model.LabelProjectDir] = meta.RealWorktree
	}
	if meta.DisplayName != "" {
		labels[model.LabelSession] = meta.DisplayName
	} else if _, _, session, ok := dockercmd.ParseManagedName(meta.Name); ok && session != "" {
		labels[model.LabelSession] = session
	}
	if meta.Background {
		labels[model.LabelBackground] = "true"
	}
	if meta.Yolo {
		labels[model.LabelYolo] = "1"
	}
	if configKey := configKeyFromStores(req.Stores); configKey != "" {
		labels[model.LabelConfigKey] = configKey
	}
	// Mark inbox-enabled sessions so `img import` can find them. The runtime
	// signals intent by adding the read-only inbox mount.
	for _, m := range req.Mounts {
		if m.ContainerPath == model.ContainerImageInboxDir {
			labels[model.LabelImageInbox] = "true"
			break
		}
	}
	// Mark session-monitor sessions so `status` knows a tmux snapshot can be
	// captured, and record the tmux owner so it captures as the right user.
	// The runtime signals both via entrypoint env vars.
	for _, e := range req.Env {
		switch e.Name {
		case model.EnvSessionMonitor:
			if e.Value == "1" {
				labels[model.LabelSessionMonitor] = "true"
			}
		case model.EnvSessionMonitorUser:
			if e.Value != "" {
				labels[model.LabelSessionMonitorUser] = e.Value
			}
		}
	}
	return labels
}

func runtimeUIDRemapEnvEntries(env []string) []string {
	values := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case model.EnvRuntimeUID, model.EnvRuntimeGID, "HOME", "USER":
			values[key] = value
		}
	}
	keys := []string{model.EnvRuntimeUID, model.EnvRuntimeGID, "HOME", "USER"}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := values[key]; ok {
			out = append(out, key+"="+value)
		}
	}
	return out
}

func configKeyFromStores(stores []backend.PersistentStore) string {
	for _, store := range stores {
		if store.Kind != backend.StoreKindConfig {
			continue
		}
		if suffix := strings.TrimSpace(store.Key.Suffix); suffix != "" {
			return suffix
		}
		return "default"
	}
	return ""
}

func sessionListFilterPairs(filter backend.SessionFilter) [][2]string {
	var pairs [][2]string
	exactName := strings.TrimSpace(filter.ExactName)
	namePrefix := strings.TrimSpace(filter.NamePrefix)
	if exactName != "" {
		pairs = append(pairs, [2]string{"name", "^/" + exactName + "$"})
	} else if namePrefix != "" {
		pairs = append(pairs, [2]string{"name", namePrefix})
	} else {
		pairs = append(pairs, [2]string{"label", model.LabelAgent})
	}
	if filter.RunningOnly {
		pairs = append(pairs, [2]string{"status", "running"})
	}
	if filter.Tool != "" {
		pairs = append(pairs, [2]string{"label", model.LabelAgent + "=" + filter.Tool})
	}
	if filter.ProjectHash != "" {
		pairs = append(pairs, [2]string{"label", model.LabelHash + "=" + filter.ProjectHash})
	}
	if filter.SessionName != "" {
		pairs = append(pairs, [2]string{"label", model.LabelSession + "=" + filter.SessionName})
	}
	if filter.Background != nil {
		pairs = append(pairs, [2]string{"label", model.LabelBackground + "=" + strconv.FormatBool(*filter.Background)})
	}
	return pairs
}

func sessionFromSummary(c dockercmd.Summary) (backend.Session, bool) {
	name := dockercmd.PrimaryContainerName(c)
	if isGatewaySummary(c, name) {
		return backend.Session{}, false
	}
	parsedTool, parsedHash, parsedSession, parsedOK := dockercmd.ParseManagedName(name)
	labels := c.Labels
	tool := strings.TrimSpace(labels[model.LabelAgent])
	projectHash := strings.TrimSpace(labels[model.LabelHash])
	sessionName := strings.TrimSpace(labels[model.LabelSession])
	if tool == "" && parsedOK {
		tool = parsedTool
	}
	if projectHash == "" && parsedOK {
		projectHash = parsedHash
	}
	if sessionName == "" && parsedOK {
		sessionName = parsedSession
	}
	if tool == "" {
		return backend.Session{}, false
	}
	id := c.ID
	if len(id) > 12 {
		id = id[:12]
	}
	var createdAt time.Time
	if c.Created > 0 {
		createdAt = time.Unix(c.Created, 0)
	}
	worktree := strings.TrimSpace(labels[model.LabelWorktree])
	projectDir := strings.TrimSpace(labels[model.LabelProjectDir])
	if projectDir == "" {
		// Containers started before the project-dir label existed only carry the
		// worktree; fall back to it so consumers still get a directory.
		projectDir = worktree
	}
	return backend.Session{
		Ref:                backend.SessionRef{Name: name, ID: id},
		Tool:               tool,
		ProjectHash:        projectHash,
		Worktree:           worktree,
		ProjectDir:         projectDir,
		Status:             c.State,
		CreatedAt:          createdAt,
		Name:               sessionName,
		Background:         strings.EqualFold(labels[model.LabelBackground], "true"),
		ImageInbox:         strings.EqualFold(labels[model.LabelImageInbox], "true"),
		Ports:              sessionPortMappings(c.Ports),
		Yolo:               strings.TrimSpace(labels[model.LabelYolo]) == "1",
		SessionMonitor:     strings.EqualFold(labels[model.LabelSessionMonitor], "true"),
		SessionMonitorUser: strings.TrimSpace(labels[model.LabelSessionMonitorUser]),
	}, true
}

func sessionPortMappings(ports dockercmd.PortMap) []backend.PortMapping {
	if len(ports) == 0 {
		return nil
	}
	var mappings []backend.PortMapping
	for port, bindings := range ports {
		for _, binding := range bindings {
			if strings.TrimSpace(binding.HostPort) == "" {
				continue
			}
			mappings = append(mappings, backend.PortMapping{
				HostIP:        binding.HostIP,
				HostPort:      binding.HostPort,
				ContainerPort: port.Num(),
				Protocol:      port.Proto(),
			})
		}
	}
	sort.Slice(mappings, func(i, j int) bool {
		if mappings[i].ContainerPort != mappings[j].ContainerPort {
			pi, erri := strconv.Atoi(mappings[i].ContainerPort)
			pj, errj := strconv.Atoi(mappings[j].ContainerPort)
			if erri == nil && errj == nil && pi != pj {
				return pi < pj
			}
			return mappings[i].ContainerPort < mappings[j].ContainerPort
		}
		if mappings[i].Protocol != mappings[j].Protocol {
			return mappings[i].Protocol < mappings[j].Protocol
		}
		if mappings[i].HostIP != mappings[j].HostIP {
			return mappings[i].HostIP < mappings[j].HostIP
		}
		return mappings[i].HostPort < mappings[j].HostPort
	})
	return mappings
}

var containerInspectMany = dockercmd.ContainerInspectMany

func fillGatewayPorts(ctx context.Context, sessions []backend.Session) {
	gatewayIndex := map[string]int{}
	names := make([]string, 0, len(sessions))
	for i := range sessions {
		if len(sessions[i].Ports) > 0 || sessions[i].Status != "running" {
			continue
		}
		name := sessions[i].Ref.Name + model.GatewayContainerSuffix
		names = append(names, name)
		gatewayIndex[name] = i
	}
	if len(names) == 0 {
		return
	}
	infos, err := containerInspectMany(ctx, names)
	if err != nil {
		return
	}
	for _, info := range infos {
		if info.NetworkSettings == nil {
			continue
		}
		name := strings.TrimPrefix(strings.TrimSpace(info.Name), "/")
		i, ok := gatewayIndex[name]
		if !ok {
			continue
		}
		sessions[i].Ports = sessionPortMappings(info.NetworkSettings.Ports)
	}
}

func isGatewaySummary(c dockercmd.Summary, name string) bool {
	if strings.EqualFold(strings.TrimSpace(c.Labels[model.GatewayLabelManaged]), "true") {
		return true
	}
	return strings.HasSuffix(name, model.GatewayContainerSuffix) && strings.HasPrefix(c.Image, model.GatewayImagePrefix)
}

func runForeground(ctx context.Context, config *dockercmd.ContainerConfig, hostConfig *dockercmd.HostConfig, name string, attach backend.AttachIO) error {
	if attach.In == nil && attach.Out == nil && attach.Err == nil {
		if attach.TTY {
			return dockercmd.RunInteractiveWithStartHook(ctx, config, hostConfig, name, attach.OnStarted)
		}
		return dockercmd.Run(ctx, config, hostConfig, name)
	}
	in := attach.In
	out := attach.Out
	errOut := attach.Err
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	if attach.TTY {
		return dockercmd.RunWithIOAndTTY(ctx, config, hostConfig, name, in, out, errOut)
	}
	return dockercmd.RunWithIO(ctx, config, hostConfig, name, in, out, errOut)
}

func applyContainerHardening(hostConfig *dockercmd.HostConfig) {
	hostConfig.SecurityOpt = append(hostConfig.SecurityOpt, "no-new-privileges")
	if hostConfig.Tmpfs == nil {
		hostConfig.Tmpfs = map[string]string{}
	}
	hostConfig.Tmpfs["/etc/sudoers.d"] = "rw,mode=755"
}

func applySELinuxMounts(hostConfig *dockercmd.HostConfig, selinuxEnforcing bool) {
	binds, remainingMounts := dockercmd.SplitMountsForSELinuxWith(hostConfig.Mounts, selinuxEnforcing)
	hostConfig.Binds = append(hostConfig.Binds, binds...)
	hostConfig.Mounts = remainingMounts
}

func portSet(ports []backend.PortMapping) dockercmd.PortSet {
	set := dockercmd.PortSet{}
	for _, p := range ports {
		port, err := dockercmd.NewPort(protocol(p.Protocol), p.ContainerPort)
		if err == nil {
			set[port] = struct{}{}
		}
	}
	return set
}

func portMap(ports []backend.PortMapping) dockercmd.PortMap {
	bindings := dockercmd.PortMap{}
	for _, p := range ports {
		if strings.TrimSpace(p.HostPort) == "" {
			continue
		}
		port, err := dockercmd.NewPort(protocol(p.Protocol), p.ContainerPort)
		if err != nil {
			continue
		}
		bindings[port] = append(bindings[port], dockercmd.PortBinding{HostIP: p.HostIP, HostPort: p.HostPort})
	}
	return bindings
}

func protocol(value string) string {
	if strings.TrimSpace(value) == "" {
		return "tcp"
	}
	return value
}

func sessionRefName(ref backend.SessionRef) string {
	if strings.TrimSpace(ref.Name) != "" {
		return strings.TrimSpace(ref.Name)
	}
	return strings.TrimSpace(ref.ID)
}

// ConfigStoreKeyInUse reports whether a running session for the same tool,
// project, and worktree already uses the given config-store key. The key is
// tracked as a Docker label; sessions predating that label used the stable
// key, which is what callers pass, so an unset label counts as a match.
func (b *Backend) ConfigStoreKeyInUse(ctx context.Context, meta backend.SessionMeta, key string) (bool, error) {
	if strings.TrimSpace(key) == "" {
		return false, nil
	}
	filters := dockercmd.NewFilters()
	filters.Add("status", "running")
	filters.Add("label", model.LabelAgent+"="+meta.Tool)
	filters.Add("label", model.LabelHash+"="+meta.ProjectHash)
	containers, err := dockercmd.ContainerList(ctx, dockercmd.ListOptions{Filters: filters})
	if err != nil {
		return false, err
	}
	for _, summary := range containers {
		worktree := strings.TrimSpace(summary.Labels[model.LabelWorktree])
		if worktree != strings.TrimSpace(meta.Worktree) {
			continue
		}
		existingKey := strings.TrimSpace(summary.Labels[model.LabelConfigKey])
		if existingKey == "" {
			existingKey = key
		}
		if existingKey == key {
			return true, nil
		}
	}
	return false, nil
}

func (b *Backend) warnInsecureDockerConfig(ctx context.Context) {
	info, err := dockercmd.Info(ctx)
	if err != nil {
		logx.Debugf("Failed to read Docker info: %v", err)
		return
	}
	for _, option := range info.SecurityOptions {
		if strings.Contains(option, "name=rootless") {
			return
		}
		if strings.Contains(option, "name=userns") {
			return
		}
	}
	logx.Warnf("Docker is running without userns-remap or rootless mode; container root maps to host root. See docs/security/host-hardening.md.")
}

func exitStatus(err error) backend.ExitStatus {
	if err == nil {
		return backend.ExitStatus{Code: 0}
	}
	var exitErr *dockercmd.ExitError
	if errors.As(err, &exitErr) {
		return backend.ExitStatus{Code: exitErr.Code}
	}
	return backend.ExitStatus{Code: 1}
}

// neutralizeExitError converts a Docker exit error into the backend-neutral
// form so callers above the seam never unwrap Docker types.
func neutralizeExitError(err error) error {
	var exitErr *dockercmd.ExitError
	if errors.As(err, &exitErr) {
		return &backend.ExitError{Code: exitErr.Code}
	}
	return err
}
