// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/devcontainer"
	"enclave/internal/gateway/tlsstore"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/mounts"
	"enclave/internal/network"
	"enclave/internal/policy"
	"enclave/internal/util"
)

type Runtime struct {
	paths                 model.Paths
	host                  model.Host
	project               model.Project
	profile               model.Profile
	handler               model.RuntimeHandler
	run                   model.RunOptions
	auth                  model.AuthOptions
	build                 model.BuildOptions
	runSources            model.RunOptionSources
	yoloEnabled           bool
	validatedDirs         []string
	validatedReadonlyDirs []string
	devcontainer          *model.DevcontainerConfig
	features              []model.Extension
	userCommandMount      *model.UserCommandMount
	ideBridgePorts        []string
	projectDir            string
	containerUser         string
	containerHome         string
	remoteUserWarned      bool
	userExistsCache       map[string]bool
	sessionName           string // cached resolved session name
	configSourceDir       string // host path to config-generated dir, set when config-source is active
	configVolSuffix       string // cached config-store suffix for the current execution
	configVolReady        bool   // whether configVolSuffix has been initialized for this execution
	policyResolved        bool
	policyResult          policy.ResolveResult
	policyErr             error
	backend               backend.Backend
}

type ExecutionContext struct {
	ContainerName string
	Mounts        []backend.Mount
	Stores        []backend.PersistentStore
	Env           []string
	Network       backend.NetworkPolicy
	Ports         []backend.PortMapping
	Secrets       []backend.SecretRelease
	AuthSync      *backend.AuthSyncSpec
	Cleanup       func()
	RunCtx        model.RunContext
}

type mountAccumulator struct {
	mounts []backend.Mount
	stores []backend.PersistentStore
	env    []string
}

type preparedVolumes struct {
	Stores        storeSet
	AuthState     model.AuthState
	SecretMapping SecretMapping
	AuthSync      *backend.AuthSyncSpec
	Cleanup       func()
}

type noopRuntimeHandler struct{}

const defaultConfigKey = "default"

func newMountAccumulator(mounts []backend.Mount, env []string) *mountAccumulator {
	return &mountAccumulator{mounts: mounts, env: env}
}

func (m *mountAccumulator) AddMount(mount backend.Mount) {
	m.mounts = append(m.mounts, mount)
}

func (m *mountAccumulator) AddEnv(key string, value string) {
	if key == "" {
		return
	}
	m.env = append(m.env, key+"="+value)
}

func (m *mountAccumulator) AddStore(store backend.PersistentStore) {
	m.stores = append(m.stores, store)
}

func (m *mountAccumulator) Mounts() []backend.Mount {
	return m.mounts
}

func (m *mountAccumulator) Stores() []backend.PersistentStore {
	return m.stores
}

func (m *mountAccumulator) Env() []string {
	return m.env
}

func (m *mountAccumulator) MountsPtr() *[]backend.Mount {
	return &m.mounts
}

func (m *mountAccumulator) EnvPtr() *[]string {
	return &m.env
}

func (noopRuntimeHandler) PortHints(model.RunContext) []string { return nil }

func (noopRuntimeHandler) LoopbackPorts(model.RunContext) []string { return nil }

func (noopRuntimeHandler) ValidateRun(model.RunContext) error { return nil }

func New(cfg model.RuntimeConfig) *Runtime {
	projectDir := cfg.Project.Dir
	if cfg.Devcontainer != nil && strings.TrimSpace(cfg.Devcontainer.WorkspaceFolder) != "" {
		projectDir = strings.TrimSpace(cfg.Devcontainer.WorkspaceFolder)
	}
	return &Runtime{
		paths:                 cfg.Paths,
		host:                  cfg.Host,
		project:               cfg.Project,
		profile:               cfg.Profile,
		handler:               resolveHandler(cfg.Handler),
		run:                   cfg.Run,
		auth:                  cfg.Auth,
		build:                 cfg.Build,
		runSources:            cfg.RunSources,
		yoloEnabled:           cfg.YoloEnabled,
		validatedDirs:         cfg.ValidatedDirs,
		validatedReadonlyDirs: cfg.ValidatedReadonlyDirs,
		devcontainer:          cfg.Devcontainer,
		features:              cfg.Features,
		userCommandMount:      cfg.UserCommandMount,
		projectDir:            projectDir,
		containerUser:         model.ContainerUser,
		containerHome:         model.ContainerHome,
	}
}

func (r *Runtime) SetBackend(be backend.Backend) {
	r.backend = be
}

func resolveHandler(handler model.RuntimeHandler) model.RuntimeHandler {
	if handler != nil {
		return handler
	}
	return noopRuntimeHandler{}
}

func (r *Runtime) Execute() error {
	ctx, err := r.prepareExecution()
	if err != nil {
		return err
	}
	if ctx.Cleanup != nil {
		defer ctx.Cleanup()
	}
	// The backend syncs auth files from the config store to the shared auth
	// store after the container exits, per the request's AuthSync intent.
	return r.runContainer(ctx)
}

func (r *Runtime) prepareExecution() (*ExecutionContext, error) {
	config.WriteProjectMarkers(r.host.Home, r.project.Hash, r.project.RealDir)
	baseContainerName := r.baseContainerName()
	var containerName string
	if r.run.Background || r.run.SessionName != "" {
		sessionContainerName := r.containerName()
		if r.containerExists(sessionContainerName) {
			if r.containerIsRunning(sessionContainerName) {
				return nil, fmt.Errorf("session %s already running; stop it first with: %s stop %s", sessionContainerName, model.AppName, sessionContainerName)
			}
			logx.Infof("Removing stopped container: %s", sessionContainerName)
			r.removeStoppedSession(sessionContainerName)
		}
		containerName = sessionContainerName
	} else {
		if r.containerExists(baseContainerName) {
			containerName = r.containerName()
		} else {
			containerName = baseContainerName
		}
	}
	r.setConfigVolumeSuffix(containerName, baseContainerName)
	r.logContainerStart(containerName, baseContainerName)
	r.warnPostStartInteractive()

	r.resolveContainerUser()
	r.ideBridgePorts = discoverIdeBridgePorts(r.host.Home)
	r.ideBridgePorts = mergeBridgePorts(r.ideBridgePorts, r.run.BridgePorts)
	mountArgs, err := r.prepareMounts()
	if err != nil {
		return nil, err
	}
	prepared, err := r.prepareVolumes(containerName, baseContainerName, mountArgs)
	if err != nil {
		return nil, err
	}
	runCtx := r.prepareRunContext(prepared.AuthState)
	r.applyPortHints(&runCtx)
	r.applyDevcontainerPorts()
	r.applyProfilePorts()
	r.applyFeaturePorts()
	runCtx.Run = r.run
	if err := r.handler.ValidateRun(runCtx); err != nil {
		return nil, err
	}
	networkResult, err := newNetworkManager(r).Prepare(containerName, prepared.AuthState, prepared.SecretMapping)
	if err != nil {
		if prepared.Cleanup != nil {
			prepared.Cleanup()
		}
		return nil, err
	}
	env := append([]string{}, mountArgs.Env()...)
	env = append(env, networkResult.Env...)
	cleanup := mergeCleanup(prepared.Cleanup, networkResult.Cleanup)
	return &ExecutionContext{
		ContainerName: containerName,
		Mounts:        mountArgs.Mounts(),
		Stores:        mountArgs.Stores(),
		Env:           env,
		Network:       networkResult.Network,
		Ports:         networkResult.Ports,
		Secrets:       secretReleases(prepared.SecretMapping),
		AuthSync:      prepared.AuthSync,
		Cleanup:       cleanup,
		RunCtx:        runCtx,
	}, nil
}

// removeStoppedSession removes a stopped session container before restarting a
// session with the same name. Finalize is skipped: the stop that preceded it
// already reconciled auth, and the persistent config store keeps any state the
// container wrote after that.
func (r *Runtime) removeStoppedSession(name string) {
	if r.backend == nil {
		return
	}
	ref := backend.SessionRef{Name: name}
	if remover, ok := r.backend.(backend.UnfinalizedRemover); ok {
		_ = remover.RemoveWithoutFinalize(context.Background(), ref)
		return
	}
	_ = r.backend.Remove(context.Background(), ref)
}

func (r *Runtime) logContainerStart(containerName string, baseContainerName string) {
	if containerName != baseContainerName && !r.run.Background && r.run.SessionName == "" {
		logx.Warnf("Container name %s is already in use; starting new session as %s", baseContainerName, containerName)
	}
	if r.run.Background {
		logx.Successf("Starting background session %q for: %s", r.friendlySessionName(), r.project.Name)
		return
	}
	if r.run.Admin {
		logx.Successf("Starting container for: %s (admin shell)", r.project.Name)
		return
	}
	logx.Successf("Starting container for: %s", r.project.Name)
}

func (r *Runtime) prepareMounts() (*mountAccumulator, error) {
	baseMounts, baseEnv := r.baseMounts()
	mountArgs := newMountAccumulator(baseMounts, baseEnv)
	r.addDevcontainerMounts(mountArgs)
	r.addAdditionalMounts(mountArgs)
	r.addUserCommandMount(mountArgs)
	r.addWorktreeMetadataMounts(mountArgs)
	r.addGitConfigMount(mountArgs)
	r.addSSHMount(mountArgs)
	r.addImageInboxMount(mountArgs)
	r.addSessionMonitorEnv(mountArgs)
	r.addCacheMounts(mountArgs)
	r.addHistoryMounts(mountArgs)
	r.addMemoryMounts(mountArgs)
	r.addToolConfigMounts(mountArgs)
	if err := r.prepareToolConfigSource(); err != nil {
		return nil, err
	}
	if !r.toolConfigSourceHandlesSkills() {
		if err := r.addSkillMounts(mountArgs); err != nil {
			return nil, err
		}
	}
	r.addIdeBridgeMount(mountArgs)
	r.addDevcontainerEnv(mountArgs)
	if r.run.PlaywrightMCP && r.profile.Name == "claude" {
		mountArgs.AddEnv(model.EnvPlaywrightMCP, "1")
		logx.Infof("Playwright MCP server enabled for Claude Code")
	}
	return mountArgs, nil
}

func (r *Runtime) prepareVolumes(containerName string, baseContainerName string, mountArgs *mountAccumulator) (preparedVolumes, error) {
	if r.backend == nil {
		return preparedVolumes{}, fmt.Errorf("runtime backend is not configured")
	}
	volumeSuffix := r.currentConfigVolumeSuffix(containerName, baseContainerName)
	vm := newVolumeManager(r)
	prep, stores := vm.BuildPrep(volumeSuffix)
	cleanup := r.ephemeralVolumeCleanup(stores)
	state, err := r.backend.PrepareStores(context.Background(), prep)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return preparedVolumes{}, err
	}
	stores.PersistedEnvAvailable = state.PersistedEnvAvailable

	r.addConfigMount(mountArgs, stores.Config)
	r.addAuthMount(mountArgs, stores.Auth)
	r.addFeatureAuthMounts(mountArgs, stores.Features)
	r.addEnvStore(mountArgs, stores.Env)

	authState, secretMapping, err := newAuthManager(r).Prepare(mountArgs.EnvPtr(), stores)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return preparedVolumes{}, err
	}

	r.logCredentials(authState)
	if err := r.addGatewayCAMount(mountArgs); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return preparedVolumes{}, err
	}
	r.addDirenvMount(mountArgs)
	if r.configSourceDir == "" {
		r.addToolSettingsTemplate(mountArgs)
	}

	return preparedVolumes{
		Stores:        stores,
		AuthState:     authState,
		SecretMapping: secretMapping,
		AuthSync:      vm.authSyncSpec(stores),
		Cleanup:       cleanup,
	}, nil
}

func (r *Runtime) setConfigVolumeSuffix(containerName string, baseContainerName string) {
	r.configVolSuffix = r.deriveConfigVolumeSuffix(containerName, baseContainerName)
	r.configVolReady = true
}

func (r *Runtime) currentConfigVolumeSuffix(containerName string, baseContainerName string) string {
	if r.configVolReady {
		return r.configVolSuffix
	}
	return r.deriveConfigVolumeSuffix(containerName, baseContainerName)
}

func (r *Runtime) deriveConfigVolumeSuffix(containerName string, baseContainerName string) string {
	if r.run.Ephemeral {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	// Named sessions (background or explicit --name) get a stable
	// store suffix derived from the session name so stores persist across
	// stop/start cycles.
	if r.run.Background || r.run.SessionName != "" {
		return r.resolvedSessionName()
	}
	stableSuffix := r.worktreeConfigVolumeSuffix()
	if containerName == baseContainerName {
		return stableSuffix
	}
	suffix := strings.TrimPrefix(containerName, baseContainerName+"-")
	if suffix == containerName {
		return stableSuffix
	}
	if r.runningConfigKeyExists(r.stableConfigKey()) {
		return suffix
	}
	return stableSuffix
}

func (r *Runtime) stableConfigKey() string {
	if suffix := r.worktreeConfigVolumeSuffix(); suffix != "" {
		return suffix
	}
	return defaultConfigKey
}

// runningConfigKeyExists reports whether a running session for this tool,
// project, and worktree already uses the given config-store key. How that is
// tracked (a Docker label today) is a backend detail behind
// ConfigStoreConflictChecker.
func (r *Runtime) runningConfigKeyExists(key string) bool {
	if strings.TrimSpace(key) == "" || r.backend == nil {
		return false
	}
	checker, ok := r.backend.(backend.ConfigStoreConflictChecker)
	if !ok {
		return false
	}
	inUse, err := checker.ConfigStoreKeyInUse(context.Background(), backend.SessionMeta{
		Tool:        r.profile.Name,
		ProjectHash: r.project.Hash,
		Worktree:    r.project.Dir,
	}, key)
	if err != nil {
		return false
	}
	return inUse
}

func (r *Runtime) worktreeConfigVolumeSuffix() string {
	if !r.shouldMountToolConfigSource() {
		return ""
	}
	currentHash := projectPathHash(r.project)
	if currentHash == "" || currentHash == r.project.Hash {
		return ""
	}
	return currentHash
}

func projectPathHash(project model.Project) string {
	path := strings.TrimSpace(project.RealDir)
	if path == "" {
		path = strings.TrimSpace(project.Dir)
	}
	if path == "" {
		return ""
	}
	return model.ShortHash(util.HashString(path))
}

func (r *Runtime) prepareRunContext(authState model.AuthState) model.RunContext {
	return model.RunContext{
		Host:       r.host,
		Project:    r.project,
		Profile:    r.profile,
		Run:        r.run,
		Auth:       r.auth,
		AuthState:  authState,
		Build:      r.build,
		RunSources: r.runSources,
	}
}

func (r *Runtime) ephemeralVolumeCleanup(stores storeSet) func() {
	if !r.run.Ephemeral || stores.Config == nil {
		return nil
	}
	configStore := *stores.Config
	return func() {
		// Retry loop: AutoRemove containers are torn down asynchronously
		// after WaitConditionNotRunning returns, so the volume may still
		// be in use briefly.
		const maxAttempts = 5
		for i := range maxAttempts {
			err := r.removeConfigStore(configStore)
			if err == nil {
				return
			}
			if i < maxAttempts-1 {
				time.Sleep(200 * time.Millisecond)
			} else {
				logx.Warnf("Failed to remove ephemeral config store: %v", err)
			}
		}
	}
}

func (r *Runtime) removeConfigStore(configStore backend.StoreRef) error {
	if r.backend == nil || r.backend.Storage() == nil {
		return fmt.Errorf("backend storage unavailable")
	}
	return r.backend.Storage().Remove(context.Background(), configStore.Key, configStore.Kind)
}

func mergeCleanup(primary func(), secondary func()) func() {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}
	return func() {
		primary()
		secondary()
	}
}

func backendEnv(env []string) []backend.EnvVar {
	out := make([]backend.EnvVar, 0, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		out = append(out, backend.EnvVar{Name: key, Value: value})
	}
	return out
}

func secretReleases(mapping SecretMapping) []backend.SecretRelease {
	if len(mapping.Entries) == 0 {
		return nil
	}
	secrets := make([]backend.SecretRelease, 0, len(mapping.Entries))
	for _, entry := range mapping.Entries {
		secrets = append(secrets, backend.SecretRelease{
			SecretID:    entry.SecretID,
			Placeholder: entry.Placeholder,
			Value:       entry.Value,
			HTTP: &backend.HTTPReleaseRule{
				Hosts:  append([]string(nil), entry.Hosts...),
				Header: entry.Header,
				Format: entry.Format,
			},
		})
	}
	return secrets
}

func (r *Runtime) addUniquePort(port string, message string, args ...any) {
	hasHost, hasContainer, hasExact := util.PortMappingState(r.run.Ports, port)
	if hasExact || hasHost || hasContainer {
		return
	}
	r.run.Ports = append(r.run.Ports, port)
	logx.Infof(message, args...)
}

func (r *Runtime) applyPortHints(runCtx *model.RunContext) {
	for _, port := range r.handler.PortHints(*runCtx) {
		r.addUniquePort(port, "Auto-mapped port %s for %s", port, util.TitleCase(r.profile.Name))
	}
	runCtx.Run = r.run
}

func (r *Runtime) applyDevcontainerPorts() {
	if r.devcontainer == nil || len(r.devcontainer.ForwardPorts) == 0 {
		return
	}
	for _, port := range r.devcontainer.ForwardPorts {
		r.addUniquePort(port, "Devcontainer port forwarding: %s", port)
	}
}

// applyProfilePorts publishes the ports declared by the tool extension.
// Each entry is injected into the run's port list, so it flows through the
// same resolution (loopback-by-default) and isolation seam (gateway vs tool
// container) as a user-supplied -p. Ports the user already mapped explicitly
// are skipped.
func (r *Runtime) applyProfilePorts() {
	for _, p := range r.profile.Ports {
		if !p.Publish {
			continue
		}
		r.addDeclaredPort(p)
	}
}

// announcePublishedPorts reports port information once the session is
// started: one forwarding line per published port — with auto-assigned host
// ports resolved from the live bindings — and the resolved URL for each
// declared, published port. Announcing only after start keeps the lines
// truthful: a failed bind never prints a forwarding that did not happen.
func (r *Runtime) announcePublishedPorts(containerName string) {
	var bindings []backend.PortMapping
	if hasAutoAssignedPort(r.run.Ports) {
		bindings = r.sessionPortBindings(containerName)
	}
	r.logPortForwardings(bindings)
	r.logDeclaredPortAvailability(bindings)
}

func hasAutoAssignedPort(ports []string) bool {
	for _, spec := range ports {
		if hostPort, _, ok := util.SplitPortMapping(spec); ok && hostPort == "0" {
			return true
		}
	}
	return false
}

// logPortForwardings prints one forwarding line per published port. The
// loopback default mirrors ResolvePorts, so the printed host-IP matches the
// actual binding. Auto-assigned host ports show the daemon-picked value from
// the live bindings, or "<auto>" when the binding cannot be read back.
func (r *Runtime) logPortForwardings(bindings []backend.PortMapping) {
	for _, spec := range r.run.Ports {
		hostIP, hostPort, containerPort, ok := util.ParsePortSpec(spec)
		if !ok {
			continue
		}
		if hostIP == "" {
			hostIP = "127.0.0.1"
		}
		if hostPort == "0" {
			if binding := boundPortMapping(bindings, containerPort); binding != nil {
				hostIP, hostPort = binding.HostIP, binding.HostPort
			} else {
				hostPort = "<auto>"
			}
		}
		logx.Infof("Port forwarding: %s:%s -> %s", hostIP, hostPort, containerPort)
	}
}

// applyFeaturePorts publishes the ports declared by enabled feature mixins.
// It runs after applyProfilePorts, so user -p mappings and tool-declared
// ports win the dedup over feature declarations.
func (r *Runtime) applyFeaturePorts() {
	for _, feature := range r.features {
		for _, p := range feature.Ports {
			if !p.Publish {
				continue
			}
			r.addDeclaredPort(p)
		}
	}
}

// addDeclaredPort injects a declared PortConfig into the run's port list
// unless its container port is already mapped. "auto" entries use the "0"
// host-port sentinel so the daemon assigns a free host port; they dedup on
// the container port only, since their host port cannot conflict.
func (r *Runtime) addDeclaredPort(p model.PortConfig) {
	port := strconv.Itoa(p.Container)
	if p.HostAllocation != model.HostAllocationAuto {
		r.addUniquePort(port, "Publishing declared port %s for %s", port, p.Label)
		return
	}
	_, hasContainer, _ := util.PortMappingState(r.run.Ports, port)
	if hasContainer {
		return
	}
	spec := "0:" + port
	r.run.Ports = append(r.run.Ports, spec)
	logx.Infof("Publishing declared port %s for %s (auto-assigned host port)", port, p.Label)
}

// declaredPortConfigs returns the tool's declared ports followed by those of
// the enabled features.
func (r *Runtime) declaredPortConfigs() []model.PortConfig {
	ports := append([]model.PortConfig(nil), r.profile.Ports...)
	for _, feature := range r.features {
		ports = append(ports, feature.Ports...)
	}
	return ports
}

// logDeclaredPortAvailability tells the user where each declared, published
// port with an openUrl is reachable. The {host_port} placeholder resolves
// from the mapped host binding; auto-assigned host ports use the live
// bindings of the started session, falling back to a hint at enclave ps when
// the binding cannot be read back.
func (r *Runtime) logDeclaredPortAvailability(bindings []backend.PortMapping) {
	for _, p := range r.declaredPortConfigs() {
		if !p.Publish || p.OpenURL == "" {
			continue
		}
		containerPort := strconv.Itoa(p.Container)
		hostPort, ok := hostPortForContainerPort(r.run.Ports, containerPort)
		if !ok {
			continue
		}
		if hostPort == "0" {
			hostPort = boundHostPort(bindings, containerPort)
			if hostPort == "" {
				logx.Infof("%s port %s has an auto-assigned host port; run '%s ps' to see it", p.Label, containerPort, model.AppName)
				continue
			}
		}
		url := strings.ReplaceAll(p.OpenURL, model.PortHostPlaceholder, hostPort)
		logx.Successf("%s available at %s", p.Label, url)
	}
}

// sessionPortBindings reads the live port bindings of a started session so
// auto-assigned host ports can be reported with their real value.
func (r *Runtime) sessionPortBindings(containerName string) []backend.PortMapping {
	if r.backend == nil {
		return nil
	}
	session, err := r.backend.Inspect(context.Background(), backend.SessionRef{Name: containerName})
	if err != nil || session == nil {
		return nil
	}
	return session.Ports
}

func boundPortMapping(bindings []backend.PortMapping, containerPort string) *backend.PortMapping {
	for i := range bindings {
		b := &bindings[i]
		if b.ContainerPort == containerPort && b.HostPort != "" && b.HostPort != "0" {
			return b
		}
	}
	return nil
}

func boundHostPort(bindings []backend.PortMapping, containerPort string) string {
	if b := boundPortMapping(bindings, containerPort); b != nil {
		return b.HostPort
	}
	return ""
}

func (r *Runtime) runContainer(ctx *ExecutionContext) error {
	be := r.backend
	if be == nil {
		return fmt.Errorf("runtime backend is not configured")
	}
	_, err := be.Run(context.Background(), r.backendRequest(ctx, false, true), backend.AttachIO{TTY: true, OnStarted: func() { r.announcePublishedPorts(ctx.ContainerName) }})
	return err
}

func (r *Runtime) backendRequest(ctx *ExecutionContext, detached bool, interactive bool) backend.Request {
	env := r.containerEnv(ctx, interactive)
	user := ""
	r.applyDevcontainerUserIntent(&user, &env)
	r.applyRuntimeUIDRemapIntent(&user, &env)

	req := backend.Request{
		Session: backend.SessionMeta{
			Tool:         r.profile.Name,
			ProjectHash:  r.project.Hash,
			Worktree:     r.project.Dir,
			RealWorktree: r.project.RealDir,
			Name:         ctx.ContainerName,
			DisplayName:  r.sessionDisplayName(ctx.ContainerName, detached),
			Background:   detached,
			Yolo:         r.yoloEnabled,
		},
		Image:           r.build.ImageName,
		Argv:            newCommandBuilder(r).Build(),
		Env:             backendEnv(env),
		WorkingDir:      r.projectDir,
		User:            user,
		Mounts:          append([]backend.Mount(nil), ctx.Mounts...),
		Stores:          append([]backend.PersistentStore(nil), ctx.Stores...),
		Network:         ctx.Network,
		Ports:           append([]backend.PortMapping(nil), ctx.Ports...),
		Security:        backend.SecurityPosture{Admin: r.run.Admin},
		Secrets:         append([]backend.SecretRelease(nil), ctx.Secrets...),
		RuntimeUIDRemap: r.build.RuntimeUIDRemap,
		Detached:        detached,
	}
	if !detached {
		// Post-run sync only applies to foreground sessions; detached sessions
		// reconcile via Stop/Remove finalize while the container still exists.
		req.AuthSync = ctx.AuthSync
	}
	if req.Network.Mode != backend.NetworkModeRestricted {
		req.Hostname = model.HostnamePrefix + r.project.Name
	}
	return req
}

func (r *Runtime) containerEnv(ctx *ExecutionContext, interactive bool) []string {
	env := append([]string{}, ctx.Env...)
	env = append(env, r.envFileValues()...)
	if !interactive {
		return env
	}
	if hostTERM := os.Getenv("TERM"); hostTERM != "" && !r.envVarSet("TERM") {
		env = append(env, "TERM="+hostTERM)
	}
	if val := os.Getenv("COLORTERM"); val != "" && !r.envVarSet("COLORTERM") {
		env = append(env, "COLORTERM="+val)
	}
	return env
}

func (r *Runtime) sessionDisplayName(containerName string, background bool) string {
	if background || r.run.SessionName != "" || containerName != r.baseContainerName() {
		return r.friendlySessionName()
	}
	return ""
}

func (r *Runtime) baseContainerName() string {
	return fmt.Sprintf("%s-%s-%s", model.AppName, r.profile.Name, r.project.Hash)
}

func (r *Runtime) containerName() string {
	return r.baseContainerName() + "-" + r.resolvedSessionName()
}

// resolvedSessionName returns the session name to use, computing and caching it
// on first call so that all callers within a single execution see the same value.
func (r *Runtime) resolvedSessionName() string {
	if r.sessionName != "" {
		return r.sessionName
	}
	if r.run.SessionName != "" {
		r.sessionName = sanitizeSessionName(r.run.SessionName)
	} else {
		r.sessionName = r.nextSessionName()
	}
	return r.sessionName
}

// friendlySessionName returns the user-facing session name: the original
// user-supplied value when set, otherwise the auto-generated name.
func (r *Runtime) friendlySessionName() string {
	if r.run.SessionName != "" {
		return r.run.SessionName
	}
	return r.resolvedSessionName()
}

func hostPortForContainerPort(ports []string, target string) (string, bool) {
	for _, port := range ports {
		hostPort, containerPort, ok := util.SplitPortMapping(port)
		if !ok {
			continue
		}
		if containerPort == target {
			return hostPort, true
		}
	}
	return "", false
}

func (r *Runtime) containerExists(name string) bool {
	if r.backend == nil {
		return false
	}
	sessions, err := r.backend.List(context.Background(), backend.SessionFilter{All: true, ExactName: name})
	if err != nil {
		return false
	}
	return len(sessions) > 0
}

func (r *Runtime) containerIsRunning(name string) bool {
	if r.backend == nil {
		return false
	}
	sessions, err := r.backend.List(context.Background(), backend.SessionFilter{RunningOnly: true, ExactName: name})
	if err != nil {
		return false
	}
	return len(sessions) > 0
}

// withStoreLock serializes cross-process access to a persistent store while fn
// runs. A nil store or a backend without locking support runs fn directly.
func (r *Runtime) withStoreLock(store *backend.StoreRef, fn func() error) error {
	if store == nil || r.backend == nil {
		return fn()
	}
	if locker, ok := r.backend.Storage().(backend.StoreLocker); ok {
		return locker.WithStoreLock(context.Background(), store.Key, store.Kind, fn)
	}
	return fn()
}

func (r *Runtime) baseMounts() ([]backend.Mount, []string) {
	var mounts []backend.Mount
	readOnly := model.ProjectMountIsReadonly(r.run.ProjectMount)
	// Skip the default project mount when the devcontainer defines an explicit
	// workspaceMount — the devcontainer mount handles the project directory and
	// adding both would cause a "Duplicate mount point" error from Docker.
	if r.devcontainer == nil || strings.TrimSpace(r.devcontainer.WorkspaceMount) == "" {
		mounts = append(mounts, bindMount(r.project.Dir, r.project.Dir, readOnly))
		if r.projectDir != r.project.Dir && r.devcontainer != nil {
			mounts = append(mounts, bindMount(r.project.Dir, r.projectDir, readOnly))
		}
	}
	env := []string{
		"PROJECT_DIR=" + r.projectDir,
		"TOOL=" + r.profile.Name,
		model.EnvProjectMount + "=" + model.ProjectMountMode(r.run.ProjectMount),
	}
	env = append(env, r.specEnvVariables()...)
	return mounts, env
}

// validEnvKeyPattern matches POSIX-shell-safe environment variable names.
var validEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// specEnvVariables renders the session's spec environment.variables into
// "KEY=VALUE" entries, in sorted key order for deterministic output. Enabled
// mixins contribute first (in feature order); the tool spec wins on key
// conflicts. Invalid keys and keys that would shadow reserved/prefixed env
// vars are skipped with a warning rather than overriding them.
func (r *Runtime) specEnvVariables() []string {
	merged := map[string]string{}
	for _, feature := range r.features {
		for k, v := range feature.EnvVariables {
			merged[k] = v
		}
	}
	for k, v := range r.profile.EnvVariables {
		merged[k] = v
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys))
	for _, key := range keys {
		if !validEnvKeyPattern.MatchString(key) {
			logx.Warnf("Ignoring invalid environment.variables key %q (must match %s)", key, validEnvKeyPattern.String())
			continue
		}
		if key == "PROJECT_DIR" || key == "TOOL" || strings.HasPrefix(key, model.EnvPrefix) {
			logx.Warnf("Ignoring environment.variables key %q: reserved for enclave internals", key)
			continue
		}
		env = append(env, key+"="+merged[key])
	}
	return env
}

// specProxyManaged unions environment.proxyManaged across the tool spec and
// all enabled mixins, so a mixin-declared credential keeps its placeholder
// selection without relying on the tool spec.
func (r *Runtime) specProxyManaged() []string {
	out := append([]string(nil), r.profile.ProxyManaged...)
	for _, feature := range r.features {
		out = append(out, feature.ProxyManaged...)
	}
	return out
}

// specNetworkDomains unions network.allowedDomains/deniedDomains across the
// tool spec and all enabled mixins, plus every declared HTTP release host
// (serviceDomains/serviceAuth): a host the proxy injects credentials for must
// be resolvable, or the release rule is dead weight. A service mixin's
// declared reachability (and deny rules) apply to the session exactly like
// the tool spec's own.
func (r *Runtime) specNetworkDomains() (allowed []string, denied []string) {
	allowed = append([]string(nil), r.profile.AllowedDomains...)
	denied = append([]string(nil), r.profile.DeniedDomains...)
	allowed = append(allowed, model.ReleaseHosts(r.profile.Secrets)...)
	for _, feature := range r.features {
		allowed = append(allowed, feature.AllowedDomains...)
		denied = append(denied, feature.DeniedDomains...)
		allowed = append(allowed, model.ReleaseHosts(feature.Secrets)...)
	}
	return allowed, denied
}

// addWorktreeMetadataMounts mounts the linked-worktree gitdir/commondir
// according to worktree_metadata: follow ties them to the project mount mode,
// readonly forces them read-only, none skips them entirely.
func (r *Runtime) addWorktreeMetadataMounts(mountArgs *mountAccumulator) {
	mode := model.WorktreeMetadataMode(r.run.WorktreeMetadata)
	if mode == model.WorktreeMetadataNone {
		return
	}
	validated := append([]string{}, r.validatedDirs...)
	validated = append(validated, r.validatedReadonlyDirs...)
	readOnly := mode == model.WorktreeMetadataReadonly || model.ProjectMountIsReadonly(r.run.ProjectMount)
	mounts.AddWorktree(mountArgs.MountsPtr(), r.project, &validated, readOnly)
}

func (r *Runtime) addAdditionalMounts(mountArgs *mountAccumulator) {
	if !model.ProjectMountIsReadonly(r.run.ProjectMount) {
		mounts.AddAdditional(mountArgs.MountsPtr(), r.validatedDirs, false)
		mounts.AddAdditional(mountArgs.MountsPtr(), r.validatedReadonlyDirs, true)
		return
	}

	var writableDirs []string
	readOnlyDirs := append([]string{}, r.validatedReadonlyDirs...)
	projectRoot := strings.TrimSpace(r.project.RealDir)
	if projectRoot == "" {
		projectRoot = strings.TrimSpace(r.project.Dir)
	}
	for _, dir := range r.validatedDirs {
		if projectRoot != "" && util.PathWithin(projectRoot, dir) {
			logx.Warnf("Additional directory %s is inside the project tree and was mounted read-only because project_mount=readonly", dir)
			readOnlyDirs = append(readOnlyDirs, dir)
			continue
		}
		writableDirs = append(writableDirs, dir)
	}
	mounts.AddAdditional(mountArgs.MountsPtr(), writableDirs, false)
	mounts.AddAdditional(mountArgs.MountsPtr(), readOnlyDirs, true)
}

func (r *Runtime) addGitConfigMount(mounts *mountAccumulator) {
	gitconfig := filepath.Join(r.host.Home, ".gitconfig")
	if util.PathExists(gitconfig) {
		mounts.AddMount(bindMount(gitconfig, "/tmp/host_gitconfig", true))
	}
}

func (r *Runtime) addSSHMount(mounts *mountAccumulator) {
	sshDir := config.HostSSHDir(r.host.Home)
	if util.PathExists(sshDir) {
		mounts.AddMount(bindMount(sshDir, r.containerHome+"/.ssh", true))
		logx.Infof("SSH directory mounted read-only")
		return
	}
	logx.Warnf("SSH not configured. Run '%s ssh-init' to enable SSH operations.", model.AppName)
}

// addImageInboxMount grants the container a single read-only bind mount of the
// global host image inbox. The directory is shared by every --image-inbox
// session, so an image imported once is visible everywhere. The agent can only
// read images the user explicitly imported host-side; the mount is read-only
// and the container has no channel to import them itself. The inbox is not
// removed on session teardown (other sessions may share it) — it is cleared by
// `enclave cleanup --all`.
func (r *Runtime) addImageInboxMount(mounts *mountAccumulator) {
	if !r.run.ImageInbox {
		return
	}
	inboxDir := config.HostImageInboxDir(r.host.Home)
	if err := os.MkdirAll(inboxDir, 0o700); err != nil {
		logx.Warnf("Failed to create image inbox directory %s: %v", inboxDir, err)
		return
	}
	mounts.AddMount(bindMount(inboxDir, model.ContainerImageInboxDir, true))
	mounts.AddEnv(model.EnvImageInbox, model.ContainerImageInboxDir)
	logx.Infof("Host image inbox mounted read-only at %s", model.ContainerImageInboxDir)
}

// addUserCommandMount exposes the host session command tree read-only inside
// the container at a fixed neutral path. It intentionally does not mirror the
// host home layout (unlike mounts.AddAdditional) and never references the host/
// command tree, keeping host commands invisible in-container.
func (r *Runtime) addUserCommandMount(mounts *mountAccumulator) {
	if r.userCommandMount == nil {
		return
	}
	mounts.AddMount(bindMount(r.userCommandMount.HostDir, r.userCommandMount.ContainerPath, true))
	logx.Infof("User session commands mounted read-only at %s", r.userCommandMount.ContainerPath)
}

// addSessionMonitorEnv asks the entrypoint to run the agent under the managed
// tmux session so `enclave status` can capture terminal snapshots. The
// backend derives the session-monitor label from this env var. Opt-in via
// --session-monitor.
func (r *Runtime) addSessionMonitorEnv(mounts *mountAccumulator) {
	if !r.run.SessionMonitor {
		return
	}
	mounts.AddEnv(model.EnvSessionMonitor, "1")
	// Record the tmux owner so `status` can capture as that user; under
	// --runtime-uid-remap the container's default exec user is root, not the
	// remapped agent that actually runs tmux.
	mounts.AddEnv(model.EnvSessionMonitorUser, r.containerUser)
}

func (r *Runtime) addCacheMounts(mounts *mountAccumulator) {
	if r.run.NoCache {
		return
	}
	cacheDir := config.HostCacheToolProjectDir(r.host.Home, r.profile.Name, r.project.Hash)

	// Create all cache directories
	cacheDirs := []string{
		"npm", "pip",
		"go", "go-build", "cargo", "pnpm", "uv", "yarn", "bun",
		"nvm",
	}
	for _, dir := range cacheDirs {
		_ = os.MkdirAll(filepath.Join(cacheDir, dir), 0o700)
	}

	mounts.AddMount(bindMount(filepath.Join(cacheDir, "npm"), r.containerHome+"/.npm", false))
	mounts.AddMount(bindMount(filepath.Join(cacheDir, "pip"), r.containerHome+"/.cache/pip", false))
	// Go caches
	mounts.AddMount(bindMount(filepath.Join(cacheDir, "go"), r.containerHome+"/go/pkg/mod", false))
	mounts.AddMount(bindMount(filepath.Join(cacheDir, "go-build"), r.containerHome+"/.cache/go-build", false))
	// Rust/Cargo cache
	mounts.AddMount(bindMount(filepath.Join(cacheDir, "cargo"), r.containerHome+"/.cargo", false))
	// pnpm store
	mounts.AddMount(bindMount(filepath.Join(cacheDir, "pnpm"), r.containerHome+"/.local/share/pnpm", false))
	// uv (Python) cache
	mounts.AddMount(bindMount(filepath.Join(cacheDir, "uv"), r.containerHome+"/.cache/uv", false))
	// Yarn cache
	mounts.AddMount(bindMount(filepath.Join(cacheDir, "yarn"), r.containerHome+"/.cache/yarn", false))
	// Bun cache
	mounts.AddMount(bindMount(filepath.Join(cacheDir, "bun"), r.containerHome+"/.bun", false))
	// nvm installed Node.js versions
	mounts.AddMount(bindMount(filepath.Join(cacheDir, "nvm"), r.containerHome+"/.nvm/versions", false))
}

func (r *Runtime) addHistoryMounts(mounts *mountAccumulator) {
	if r.run.NoHistory {
		return
	}
	projectDataDir := config.HostProjectHistoryDir(r.host.Home, r.project.Hash, r.profile.Name)
	_ = os.MkdirAll(projectDataDir, 0o700)
	mounts.AddMount(bindMount(projectDataDir, r.containerHome+"/.shell_history", false))
	mounts.AddEnv("HISTFILE", r.containerHome+"/.shell_history/bash_history")
}

func (r *Runtime) addMemoryMounts(mounts *mountAccumulator) {
	if r.run.NoMemory {
		return
	}
	// Ephemeral sessions must not persist memory: skip the host bind mount so
	// the agent's writes land in the discarded per-session config store.
	if r.run.Ephemeral {
		return
	}
	if r.profile.MemoryDir == "" {
		return
	}
	memDir := config.HostProjectMemoryDir(r.host.Home, r.project.Hash, r.profile.Name)
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		logx.Warnf("Failed to create memory directory %s: %v", memDir, err)
		return
	}
	containerMemDir := resolveContainerProfilePath(r.containerHome, r.profile.MemoryDir)
	mounts.AddMount(bindMount(memDir, containerMemDir, false))
}

// ensureHostPlaceholderFile touches hostPath if it does not yet exist, so that
// Docker bind-mounts a file rather than creating a directory in its place.
func ensureHostPlaceholderFile(hostPath string) {
	if _, err := os.Stat(hostPath); os.IsNotExist(err) {
		// #nosec G304 -- hostPath is built from a trusted directory plus a trusted basename.
		if f, err := os.Create(hostPath); err == nil {
			if err := f.Close(); err != nil {
				logx.Debugf("Failed to close %s: %v", hostPath, err)
			}
		}
	}
}

func (r *Runtime) addToolConfigMounts(mounts *mountAccumulator) {
	if r.run.NoHistory {
		return
	}
	// Config files created in container that should persist across sessions.
	// Each file is touched (if not exists) to ensure Docker mounts a file, not a directory.
	configFiles := []struct {
		hostName      string // filename in the config directory
		containerPath string // full path in container
	}{
		{"npmrc", r.containerHome + "/.npmrc"},
		{"yarnrc", r.containerHome + "/.yarnrc"},
		{"yarnrc.yml", r.containerHome + "/.yarnrc.yml"},
		{"bunfig.toml", r.containerHome + "/.bunfig.toml"},
		{"node_repl_history", r.containerHome + "/.node_repl_history"},
	}

	configDir := config.HostProjectHomeConfigDir(r.host.Home, r.project.Hash, r.profile.Name)
	_ = os.MkdirAll(configDir, 0o700)

	for _, cf := range configFiles {
		hostPath := filepath.Join(configDir, cf.hostName)
		ensureHostPlaceholderFile(hostPath)
		mounts.AddMount(bindMount(hostPath, cf.containerPath, false))
	}
}

func (r *Runtime) addDevcontainerMounts(mountArgs *mountAccumulator) {
	if r.devcontainer == nil {
		return
	}
	if r.devcontainer.WorkspaceMount != "" {
		if parsed, err := devcontainer.ParseMountSpec(r.devcontainer.WorkspaceMount, r.project.Dir, r.host.Home); err != nil {
			logx.Warnf("Invalid devcontainer workspaceMount: %v", err)
		} else if devcontainer.IsBlockedMount(parsed, r.project.Dir) {
			logx.Warnf("devcontainer workspaceMount source %q is blocked for security; ignoring (only paths inside the project directory may be bind-mounted via devcontainer config)", parsed.Source)
		} else {
			mounts.ApplyProjectMountMode(&parsed, r.run.ProjectMount)
			mountArgs.AddMount(parsed)
		}
	}
	for _, mount := range r.devcontainer.Mounts {
		if strings.TrimSpace(mount) == "" {
			continue
		}
		parsed, err := devcontainer.ParseMountSpec(mount, r.project.Dir, r.host.Home)
		if err != nil {
			logx.Warnf("Invalid devcontainer mount: %v", err)
			continue
		}
		if devcontainer.IsBlockedMount(parsed, r.project.Dir) {
			logx.Warnf("devcontainer mount source %q is blocked for security; ignoring (only paths inside the project directory may be bind-mounted via devcontainer config)", parsed.Source)
			continue
		}
		mounts.ApplyProjectMountMode(&parsed, r.run.ProjectMount)
		mountArgs.AddMount(parsed)
	}
}

func (r *Runtime) addDevcontainerEnv(mounts *mountAccumulator) {
	if r.devcontainer == nil {
		return
	}
	mounts.AddEnv(model.EnvDevcontainer, "1")
	if remoteUser := r.devcontainerRemoteUser(); remoteUser != "" && r.shouldApplyDevcontainerRemoteUser(remoteUser) {
		mounts.AddEnv(model.EnvDevcontainerRemoteUser, remoteUser)
	}
	if r.devcontainer.PostCreateCommand != "" {
		mounts.AddEnv(model.EnvDevcontainerPostCreate, r.devcontainer.PostCreateCommand)
	}
	if r.devcontainer.PostStartCommand != "" {
		mounts.AddEnv(model.EnvDevcontainerPostStart, r.devcontainer.PostStartCommand)
	}
	if len(r.devcontainer.ContainerEnv) > 0 {
		for key, value := range r.devcontainer.ContainerEnv {
			if key == "" || key == "PROJECT_DIR" || key == "TOOL" || strings.HasPrefix(key, model.EnvPrefix) {
				continue
			}
			mounts.AddEnv(key, value)
		}
	}
}

func (r *Runtime) applyDevcontainerUserIntent(user *string, env *[]string) {
	if r.devcontainer == nil {
		return
	}
	remoteUser := r.devcontainerRemoteUser()
	if remoteUser == "" {
		return
	}
	if r.build.UseRemoteUser {
		if r.containerUser != remoteUser {
			return
		}
		*user = remoteUser
		r.appendUserHomeEnvEntries(env, r.containerUser, r.containerHome)
		return
	}
	if r.run.Shell {
		if !r.devcontainerUserExistsCached(remoteUser) {
			logx.Warnf("devcontainer remoteUser %q not found in image; ignoring", remoteUser)
			return
		}
		*user = remoteUser
		r.appendUserHomeEnvEntries(env, remoteUser, devcontainerHome(remoteUser))
		return
	}
	if !r.remoteUserWarned {
		logx.Warnf("devcontainer remoteUser %q is ignored in agent mode; use --use-remote-user to honor remoteUser for agents or use --shell for a shell session as that user", remoteUser)
		r.remoteUserWarned = true
	}
}

func (r *Runtime) appendUserHomeEnvEntries(env *[]string, userVal, homeVal string) {
	if !r.envVarSet("USER") && !envHasKey(*env, "USER") {
		*env = append(*env, "USER="+userVal)
	}
	if !r.envVarSet("HOME") && !envHasKey(*env, "HOME") {
		*env = append(*env, "HOME="+homeVal)
	}
}

func (r *Runtime) applyRuntimeUIDRemapIntent(user *string, env *[]string) {
	if !r.build.RuntimeUIDRemap {
		return
	}
	*user = "root"
	*env = append(*env,
		model.EnvRuntimeUID+"="+r.host.UID,
		model.EnvRuntimeGID+"="+r.host.GID,
		"HOME="+r.containerHome,
		"USER="+r.containerUser,
	)
}

func (r *Runtime) envVarSet(key string) bool {
	if key == "" {
		return false
	}
	if r.devcontainer != nil {
		if _, ok := r.devcontainer.ContainerEnv[key]; ok {
			return true
		}
	}
	return false
}

func (r *Runtime) devcontainerUserExists(user string) bool {
	user = strings.TrimSpace(user)
	if user == "" {
		return false
	}
	if r.build.ImageName == "" {
		return false
	}
	return devcontainer.UserExistsInImage(r.build.ImageName, user)
}

func (r *Runtime) devcontainerUserExistsCached(user string) bool {
	user = strings.TrimSpace(user)
	if user == "" {
		return false
	}
	if r.userExistsCache == nil {
		r.userExistsCache = map[string]bool{}
	}
	if exists, ok := r.userExistsCache[user]; ok {
		return exists
	}
	exists := r.devcontainerUserExists(user)
	r.userExistsCache[user] = exists
	return exists
}

func (r *Runtime) devcontainerRemoteUser() string {
	if r.devcontainer == nil {
		return ""
	}
	return strings.TrimSpace(r.devcontainer.RemoteUser)
}

func (r *Runtime) shouldApplyDevcontainerRemoteUser(remoteUser string) bool {
	if remoteUser == "" {
		return false
	}
	if r.build.UseRemoteUser {
		return r.containerUser == remoteUser
	}
	if r.run.Shell {
		return r.devcontainerUserExistsCached(remoteUser)
	}
	return false
}

func (r *Runtime) resolveContainerUser() {
	r.containerUser = model.ContainerUser
	r.containerHome = model.ContainerHome
	if !r.build.UseRemoteUser || r.devcontainer == nil {
		return
	}
	remoteUser := r.devcontainerRemoteUser()
	if remoteUser == "" {
		logx.Warnf("devcontainer remoteUser is not set; --use-remote-user ignored")
		return
	}
	if !r.devcontainerUserExistsCached(remoteUser) {
		logx.Warnf("devcontainer remoteUser %q not found in image; ignoring", remoteUser)
		return
	}
	r.containerUser = remoteUser
	r.containerHome = devcontainerHome(remoteUser)
}

func devcontainerHome(user string) string {
	if strings.TrimSpace(user) == "" {
		return model.ContainerHome
	}
	if user == "root" {
		return "/root"
	}
	return "/home/" + user
}

func (r *Runtime) addConfigMount(mounts *mountAccumulator, configStore *backend.StoreRef) {
	if r.profile.ConfigDir == "" || configStore == nil {
		return
	}
	containerConfigDir := resolveContainerProfilePath(r.containerHome, r.profile.ConfigDir)
	mounts.AddStore(backend.PersistentStore{Kind: configStore.Kind, Key: configStore.Key, ContainerPath: containerConfigDir, CacheMmap: r.profile.QEMUStoreCacheMmap})
	configEnvVar := strings.ToUpper(r.profile.Name) + "_CONFIG_DIR"
	mounts.AddEnv(configEnvVar, containerConfigDir)
	mounts.AddEnv(model.EnvToolConfigDir, containerConfigDir)
	if settingsTarget := strings.TrimSpace(r.profile.SettingsTarget); settingsTarget != "" {
		mounts.AddEnv(model.EnvToolSettingsTarget, resolveContainerProfilePath(r.containerHome, settingsTarget))
	}
	if skillsDir := strings.TrimSpace(r.profile.SkillsDir); skillsDir != "" {
		mounts.AddEnv(model.EnvToolSkillsDir, resolveContainerProfilePath(r.containerHome, skillsDir))
	}
	if r.yoloEnabled {
		mounts.AddEnv(model.EnvYolo, "1")
	}
	logx.Infof("%s CLI configuration mounted", util.TitleCase(r.profile.Name))
}

func (r *Runtime) addAuthMount(mounts *mountAccumulator, authStore *backend.StoreRef) {
	if authStore == nil {
		return
	}
	authMountPath := r.containerHome + "/" + model.ContainerAuthDir
	mounts.AddStore(backend.PersistentStore{Kind: authStore.Kind, Key: authStore.Key, ContainerPath: authMountPath})
	mounts.AddEnv(model.EnvAuthDir, authMountPath)
	authFiles := r.profile.ProviderAuthFiles()
	if len(authFiles) > 0 {
		mounts.AddEnv(model.EnvAuthFiles, strings.Join(authFiles, ","))
	}
	// When the tool supports a dedicated credential-storage directory (e.g.
	// Claude's CLAUDE_SECURESTORAGE_CONFIG_DIR), point it at the shared auth
	// store. The tool then reads/writes its credential file and refresh lock
	// directly in the shared location, so concurrent sessions coordinate token
	// refreshes natively instead of racing per-session copies. The config-dir
	// symlink remains as a graceful fallback if the variable is unsupported.
	if env := r.profile.ProviderSecurestorageDirEnv(); env != "" {
		mounts.AddEnv(env, authMountPath)
	}
	logx.Infof("%s shared auth mounted", util.TitleCase(r.profile.Name))
}

func (r *Runtime) addFeatureAuthMounts(mounts *mountAccumulator, featureStores map[string]backend.StoreRef) {
	if len(featureStores) == 0 {
		return
	}
	var entries []string
	for _, feat := range r.features {
		store, ok := featureStores[feat.Name]
		if !ok {
			continue
		}
		mountPath := r.containerHome + "/" + model.ContainerFeatureAuthDir + "/" + feat.Name
		mounts.AddStore(backend.PersistentStore{Kind: store.Kind, Key: store.Key, ContainerPath: mountPath})
		entry := feat.Name + ":" + feat.ConfigDir + ":" + strings.Join(feat.AuthFiles, ",")
		entries = append(entries, entry)
		logx.Infof("%s feature auth mounted", util.TitleCase(feat.Name))
	}
	if len(entries) > 0 {
		mounts.AddEnv(model.EnvFeatureAuthMap, strings.Join(entries, "|"))
	}
}

func (r *Runtime) addEnvStore(mounts *mountAccumulator, envStore *backend.StoreRef) {
	if envStore == nil {
		return
	}
	mounts.AddStore(backend.PersistentStore{Kind: envStore.Kind, Key: envStore.Key})
}

func (r *Runtime) addDirenvMount(mounts *mountAccumulator) {
	if !util.PathExists(filepath.Join(r.project.Dir, ".envrc")) {
		return
	}
	mounts.AddEnv("HOST_PROJECT_DIR", r.project.Dir)
	hostDirenvAllow := filepath.Join(r.host.Home, ".local", "share", "direnv", "allow")
	if util.PathExists(hostDirenvAllow) {
		mounts.AddMount(bindMount(hostDirenvAllow, "/tmp/host_direnv_allow", true))
		logx.Infof("Host direnv approvals will be translated to container")
	}
}

func (r *Runtime) addGatewayCAMount(mounts *mountAccumulator) error {
	if !r.shouldUseGateway() {
		return nil
	}
	store := tlsstore.New(config.HostTLSDir(r.host.Home))
	if err := store.EnsureCA(); err != nil {
		return fmt.Errorf("initialize gateway CA: %w", err)
	}
	mounts.AddMount(bindMount(store.CACertPath, model.AgentGatewayCACertPath, true))
	mounts.AddEnv(model.EnvGatewayCACertPath, model.AgentGatewayCACertPath)
	return nil
}

func (r *Runtime) shouldUseGateway() bool {
	resolved, err := r.resolveEffectivePolicy()
	if err != nil {
		logx.Warnf("Failed to resolve effective network policy for gateway decision: %v", err)
		return true
	}
	return resolved.Effective.Mode != model.NetworkModeUnrestricted
}

func (r *Runtime) resolveEffectivePolicy() (policy.ResolveResult, error) {
	if r.policyResolved {
		return r.policyResult, r.policyErr
	}
	r.policyResolved = true

	tool := strings.TrimSpace(r.profile.Name)
	if tool == "" {
		policyPath := network.GlobalPolicyPath(r.host.Home)
		globalPolicy, err := network.LoadPolicy(policyPath)
		if err != nil {
			r.policyErr = fmt.Errorf("load global policy %s: %w", policyPath, err)
			return policy.ResolveResult{}, r.policyErr
		}
		specAllowed, specDenied := r.specNetworkDomains()
		effective := network.Merge(network.MergeConfig{
			GlobalPolicy:       globalPolicy,
			SpecAllowedDomains: specAllowed,
			SpecDeniedDomains:  specDenied,
		})
		if r.run.AllowAllNetwork {
			effective.Mode = model.NetworkModeUnrestricted
			effective.ModeSource = "allow-all-network"
		}
		r.policyResult = policy.ResolveResult{
			GlobalPolicy: globalPolicy,
			Effective:    effective,
		}
		return r.policyResult, nil
	}

	specAllowed, specDenied := r.specNetworkDomains()
	resolver := policy.NewEffectiveResolver(r.paths, r.host.Home)
	resolved, err := resolver.Resolve(policy.ResolveInput{
		ProjectDir:         r.project.Dir,
		ProjectHash:        r.project.Hash,
		Tool:               tool,
		AllowAllNetwork:    r.run.AllowAllNetwork,
		SpecAllowedDomains: nonNilStrings(specAllowed),
		SpecDeniedDomains:  nonNilStrings(specDenied),
	})
	if err != nil {
		r.policyErr = err
		return policy.ResolveResult{}, err
	}
	r.policyResult = resolved
	return resolved, nil
}

// nonNilStrings returns s unchanged when non-nil, or an empty (non-nil) slice
// otherwise. The effective-policy resolver treats a nil SpecAllowedDomains/
// SpecDeniedDomains as "not supplied" and reloads the tool spec from disk;
// r.profile is already loaded, so passing an explicit empty slice avoids the
// redundant per-session spec reparse for tools with no inline network domains.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (r *Runtime) addToolSettingsTemplate(mounts *mountAccumulator) {
	if r.profile.SettingsFile == "" {
		return
	}

	mounts.AddEnv(model.EnvToolSettingsTemplate, "/usr/local/share/enclave/templates/"+r.profile.SettingsFile)
	if r.yoloEnabled {
		mounts.AddEnv(model.EnvYolo, "1")
	}
	if settingsTarget := strings.TrimSpace(r.profile.SettingsTarget); settingsTarget != "" {
		mounts.AddEnv(model.EnvToolSettingsTarget, resolveContainerProfilePath(r.containerHome, settingsTarget))
	} else {
		logx.Warnf("%s settings_file is set but settings_target is empty", util.TitleCase(r.profile.Name))
	}
}

func (r *Runtime) envFileValues() []string {
	projectEnv := filepath.Join(r.project.Dir, ".env")
	if util.PathExists(projectEnv) {
		if keys, err := util.EnvKeysFromFile(projectEnv); err == nil && len(keys) > 0 {
			logx.Warnf(".env file loaded into container (keys: %s)", strings.Join(keys, ", "))
		} else {
			logx.Warnf(".env file loaded into container")
		}
		values, err := util.ParseEnvFile(projectEnv)
		if err != nil {
			logx.Warnf("Failed to read .env file: %v", err)
			return nil
		}
		return values
	}
	return nil
}

func formatEnvContent(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		value := values[key]
		if value == "" {
			continue
		}
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(value)
		builder.WriteString("\n")
	}
	return builder.String()
}

func (r *Runtime) logCredentials(state model.AuthState) {
	hasEnvCredential := state.HasAnyEnvCredential()
	hasSession := state.HasAnySession()
	if hasEnvCredential && hasSession {
		logx.Successf("Credentials: Both env credential and subscription/session are present")
		return
	}
	if hasEnvCredential {
		logx.Successf("Credentials: Env credential is present")
		return
	}
	if hasSession {
		logx.Successf("Credentials: Subscription/session is present")
		return
	}
	logx.Warnf("Credentials: No env credential or session found. You may need to log in.")
}
