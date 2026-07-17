// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package backend

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"time"
)

const (
	NameDocker = "docker"
	NameQEMU   = "qemu"
)

type NetworkMode string

const (
	NetworkModeRestricted   NetworkMode = "restricted"
	NetworkModeUnrestricted NetworkMode = "unrestricted"
)

type MountType string

const (
	MountTypeBind   MountType = "bind"
	MountTypeVolume MountType = "volume"
	MountTypeTmpfs  MountType = "tmpfs"
)

type Request struct {
	Session SessionMeta

	Image      string
	Argv       []string
	Entrypoint []string
	Env        []EnvVar
	WorkingDir string
	User       string
	Hostname   string

	Mounts []Mount
	Stores []PersistentStore

	Network NetworkPolicy
	Ports   []PortMapping

	Security SecurityPosture
	Secrets  []SecretRelease

	// AuthSync declares the credential reconciliation to run after a foreground
	// session exits. It must be carried on the request because foreground
	// sessions are removed on exit, leaving nothing to inspect afterwards.
	AuthSync *AuthSyncSpec

	RuntimeUIDRemap bool
	Detached        bool
}

type EnvVar struct {
	Name  string
	Value string
}

type SessionMeta struct {
	Tool         string
	ProjectHash  string
	Worktree     string
	RealWorktree string
	Name         string
	DisplayName  string
	Background   bool
	Yolo         bool
}

type Mount struct {
	Type          MountType
	Source        string
	ContainerPath string
	ReadOnly      bool
}

type StoreKind string

const (
	StoreKindConfig      StoreKind = "config"
	StoreKindAuth        StoreKind = "auth"
	StoreKindFeatureAuth StoreKind = "feature-auth"
	StoreKindEnv         StoreKind = "env"
)

type StoreKey struct {
	Owner       string
	ProjectHash string
	Suffix      string
}

type PersistentStore struct {
	Kind          StoreKind
	Key           StoreKey
	ContainerPath string
	ReadOnly      bool
	// CacheMmap requests mmap-coherent file semantics for this store's mount.
	// Set from the tool profile (qemu_store_cache_mmap) for stores holding
	// SQLite WAL databases; the qemu backend maps it to 9p cache=mmap and
	// backends without special cache modes ignore it.
	CacheMmap bool
}

// StoreRef identifies one persistent store by neutral identity. It is the
// replacement for passing backend-specific store handles (e.g. host store
// directories) between packages.
type StoreRef struct {
	Kind StoreKind
	Key  StoreKey
}

// StorePrep declares which persistent stores a session needs and how to
// prepare them before the session starts. The caller decides policy (which
// stores exist, reset flags, preserve lists); the backend owns the mechanics.
type StorePrep struct {
	Config   *ConfigStorePrep
	Auth     *StorePrepEntry // shared tool auth store
	Env      *EnvStorePrep
	Features []StorePrepEntry // feature auth stores

	// ResetAuthFiles lists store-relative auth files to delete from the config
	// store and (when present) the shared auth store before the session starts.
	ResetAuthFiles []string
}

type ConfigStorePrep struct {
	Key StoreKey
	// Recreate drops any existing store contents and starts fresh (ephemeral
	// sessions). The backend may tag the store for later cleanup.
	Recreate bool
	// LayoutDirs are store-relative directories to create ahead of the session
	// so tools find their expected structure with correct ownership.
	LayoutDirs []string
	Overlay    *ConfigOverlaySpec
}

// ConfigOverlaySpec asks the backend to replace the config store's contents
// with a host-side generated source directory, keeping PreservePaths (session
// state such as auth files) intact across the swap.
type ConfigOverlaySpec struct {
	SourceDir     string
	PreservePaths []string
}

type StorePrepEntry struct {
	Key StoreKey
}

type EnvStorePrep struct {
	Key StoreKey
	// Reset deletes the persisted env file (e.g. on --reset-auth).
	Reset bool
}

// StoreState reports what PrepareStores found while preparing the stores.
type StoreState struct {
	PersistedEnvAvailable bool
}

// AuthSyncSpec is the post-session credential reconciliation intent: which
// auth files flow from the config store to the shared auth store, and which
// feature credentials are copied to feature auth stores.
type AuthSyncSpec struct {
	// AuthFiles are validated store-relative paths reconciled from the config
	// store to the shared auth store.
	AuthFiles []string
	Features  []FeatureAuthSync
}

type FeatureAuthSync struct {
	Feature   string // owner of the feature auth store
	ConfigDir string // subdirectory of the config store holding the feature's config
	AuthFiles []string
}

type NetworkPolicy struct {
	Mode           NetworkMode
	Egress         EgressPolicy
	LoopbackPorts  []string
	IdeBridgePorts []string
}

type EgressPolicy struct {
	AllowedDomains []string
	// DeniedDomains are blackholed by the gateway (dnsmasq address= lines and
	// the proxy's deny-first check) even when a broader allow would otherwise
	// resolve them. Populated from spec.yaml network.deniedDomains; without
	// this field the deny rules would exist only in `network print` output,
	// never in a running session's gateway.
	DeniedDomains []string
	Resolvers     []string
	AllowlistPath string
}

type SecretRelease struct {
	SecretID    string
	Placeholder string
	EnvVars     []string
	Value       string
	HTTP        *HTTPReleaseRule
}

type HTTPReleaseRule struct {
	Hosts  []string
	Header string
	Format string
}

type PortMapping struct {
	HostIP        string
	HostPort      string
	ContainerPort string
	Protocol      string
}

type SecurityPosture struct {
	Admin bool
}

type AttachIO struct {
	In         io.Reader
	Out        io.Writer
	Err        io.Writer
	TTY        bool
	DetachKeys string
	// OnStarted, if set, is invoked once the backend confirms the session is
	// running (e.g. to announce published ports). Backends that cannot observe
	// startup may skip it.
	OnStarted func()
}

type ExitStatus struct {
	Code int
}

type Backend interface {
	Name() string
	Check(ctx context.Context) error
	Capabilities() Capabilities
	Storage() StoreManager

	// PrepareStores realizes the declared persistent stores before a session
	// starts. Callers run auth preparation between PrepareStores and Run/Start,
	// operating on the stores through Storage().
	PrepareStores(ctx context.Context, prep StorePrep) (StoreState, error)

	Run(ctx context.Context, req Request, io AttachIO) (ExitStatus, error)
	Start(ctx context.Context, req Request) (SessionRef, error)

	List(ctx context.Context, filter SessionFilter) ([]Session, error)
	Inspect(ctx context.Context, ref SessionRef) (*Session, error)
	Attach(ctx context.Context, ref SessionRef, io AttachIO) error
	Stop(ctx context.Context, ref SessionRef, opts StopOptions) error
	Remove(ctx context.Context, ref SessionRef) error
}

type Execer interface {
	Exec(ctx context.Context, ref SessionRef, req ExecRequest, io AttachIO) error
}

// OutputExecer runs a command inside a session without attaching the caller's
// terminal and returns the command's stdout verbatim. An empty user runs as
// the container's default exec user.
type OutputExecer interface {
	ExecOutput(ctx context.Context, ref SessionRef, argv []string, user string) (string, error)
}

type LogReader interface {
	Logs(ctx context.Context, ref SessionRef, opts LogOptions) (string, error)
}

// UnfinalizedRemover removes a session without reconciling auth first. Callers
// must know the session's auth state is already durable (for example because
// Stop{Finalize:true} ran) or explicitly accept losing session credentials.
type UnfinalizedRemover interface {
	RemoveWithoutFinalize(ctx context.Context, ref SessionRef) error
}

// ConfigStoreConflictChecker reports whether a running session for the same
// tool, project, and worktree already uses the given config-store key.
// Sessions predating config-key tracking count as using the caller's stable
// key, so callers must pass the stable key they would fall back to.
type ConfigStoreConflictChecker interface {
	ConfigStoreKeyInUse(ctx context.Context, meta SessionMeta, key string) (bool, error)
}

// GatewayInfo is the neutral view of one running network-enforcement gateway.
type GatewayInfo struct {
	ID               string
	Name             string
	Tool             string
	ProjectHash      string
	ProjectDir       string
	WorkspaceID      string
	SessionContainer string
}

func (g GatewayInfo) ShortID() string {
	if len(g.ID) <= 12 {
		return g.ID
	}
	return g.ID[:12]
}

// GatewayFilter scopes a gateway listing; the zero value matches all running
// gateways. Empty fields are not filtered on.
type GatewayFilter struct {
	Tool        string
	ProjectHash string
	WorkspaceID string
}

// GatewayManager is implemented by backends whose restricted-network
// enforcement runs as a reloadable gateway component. It powers `network
// status`/`network apply` without exposing the gateway mechanism.
type GatewayManager interface {
	ListGateways(ctx context.Context, filter GatewayFilter) ([]GatewayInfo, error)
	// VerifyGatewayConfigMount checks that the gateway consumes its network
	// configuration from the expected host directory, so a bundle written
	// there is what a reload will pick up.
	VerifyGatewayConfigMount(ctx context.Context, id string, expectedSourceDir string) error
	// ReloadGatewayNetwork tells the gateway to apply the current config
	// bundle and waits until it confirms the given bundle generation.
	ReloadGatewayNetwork(ctx context.Context, id string, generation string) error
}

type SessionRef struct {
	Name string
	ID   string
}

type StopOptions struct {
	Finalize bool
	Timeout  time.Duration
}

type SessionFilter struct {
	All         bool
	RunningOnly bool
	Tool        string
	ProjectHash string
	SessionName string
	Background  *bool
	NamePrefix  string
	ExactName   string
}

type Session struct {
	Ref         SessionRef
	Tool        string
	ProjectHash string
	Worktree    string
	// ProjectDir is the absolute, symlink-resolved project directory. It may be
	// empty for containers started by an older enclave that did not record the
	// project-dir label.
	ProjectDir string
	Status     string
	CreatedAt  time.Time
	Name       string
	Background bool
	// ImageInbox reports whether the session was started with --image-inbox
	// (i.e. carries the read-only host image inbox mount).
	ImageInbox bool
	Ports      []PortMapping
	Yolo       bool
	// SessionMonitor reports whether the agent runs under the managed tmux
	// session, so a terminal snapshot can be captured via `status`.
	SessionMonitor bool
	// SessionMonitorUser is the user that owns the managed tmux server, so
	// `status` can capture as that user (empty for sessions without the
	// monitor or containers labeled before this was recorded).
	SessionMonitorUser string
}

type ExecRequest struct {
	Argv []string
	User string
	TTY  bool
}

type LogOptions struct {
	Tail int
}

type Capabilities struct {
	RestrictedNetwork bool
	SecretHTTPRelease bool
}

type StoreManager interface {
	Ensure(ctx context.Context, key StoreKey, kind StoreKind, owner string) error
	ReadFile(ctx context.Context, key StoreKey, kind StoreKind, rel string) ([]byte, error)
	WriteFile(ctx context.Context, key StoreKey, kind StoreKind, rel string, data []byte, mode fs.FileMode) error
	Seed(ctx context.Context, key StoreKey, kind StoreKind, items []SeedItem) error
	RemovePath(ctx context.Context, key StoreKey, kind StoreKind, rel string) error
	Remove(ctx context.Context, key StoreKey, kind StoreKind) error
}

// StoreLocker serializes cross-process access to one store. Callers wrap
// read-modify-write sequences of StoreManager operations; single operations do
// not need it.
type StoreLocker interface {
	WithStoreLock(ctx context.Context, key StoreKey, kind StoreKind, fn func() error) error
}

// StoreInspector reports whether a store already exists without creating it.
type StoreInspector interface {
	StoreExists(ctx context.Context, key StoreKey, kind StoreKind) (bool, error)
}

// OwnedFileWriter writes a store file and restores store ownership in one
// backend operation, where the backend can do that more atomically than
// WriteFile followed by Ensure.
type OwnedFileWriter interface {
	WriteFileOwned(ctx context.Context, key StoreKey, kind StoreKind, rel string, data []byte, mode fs.FileMode, owner string) error
}

type SeedItem struct {
	HostPath string
	StoreRel string
	Mode     fs.FileMode
}

// ExitError reports a session process that exited with a non-zero status.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("session exited with status %d", e.Code)
}

func Validate(req Request, caps Capabilities) error {
	if req.Network.Mode == NetworkModeRestricted && !caps.RestrictedNetwork {
		return fmt.Errorf("backend cannot enforce restricted networking; rerun with --allow-all-network to proceed unrestricted")
	}
	if req.Network.Mode == NetworkModeRestricted && !caps.SecretHTTPRelease {
		for _, secret := range req.Secrets {
			if secret.HTTP != nil {
				id := secret.SecretID
				if id == "" {
					id = "<unknown>"
				}
				return fmt.Errorf("backend cannot honor HTTP secret release for %q under restricted networking", id)
			}
		}
	}
	return nil
}
