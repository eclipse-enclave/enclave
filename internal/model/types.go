// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "sort"

type RunOptions struct {
	Tool              string
	Backend           string
	HostConfig        string
	HostConfigPaths   []string
	YoloOverride      *bool
	ConfigDefaultYolo *bool
	Persist           bool
	Ephemeral         bool
	AllowAllNetwork   bool
	Ports             []string
	AddDirs           []string
	AddReadonlyDirs   []string
	ProjectMount      string
	WorktreeMetadata  string
	AllowDomains      []string
	BridgePorts       []string
	Shell             bool
	Admin             bool
	ImageInbox        bool
	SessionMonitor    bool
	CmdArgs           []string
	NoHistory         bool
	NoMemory          bool
	NoCache           bool
	NetworkLog        string
	Verbose           bool
	Background        bool
	SessionName       string
	PlaywrightMCP     bool
	AllRunning        bool
	NoApply           bool
	Force             bool
}

type AuthOptions struct {
	ResetAuth    bool
	NoAPIKey     bool
	PassAPIKey   bool
	PassEnv      []string
	AuthScope    string
	AuthName     string
	SecretsScope string
}

type BuildOptions struct {
	ForceRebuild    bool
	NoRebuild       bool
	ForceBaseImage  bool
	BaseImage       string
	Devcontainer    bool
	ImageName       string
	ImageNameSet    bool
	Slim            bool
	Features        []string
	UseRemoteUser   bool
	CacheFrom       []string
	BuildUID        string
	BuildGID        string
	RuntimeUIDRemap bool
	BuildxCacheDir  string
	BuildxCacheFrom []string
	BuildxCacheTo   []string
	Progress        string
}

type CleanupOptions struct {
	CleanupAll        bool
	CleanupEphemeral  bool
	CleanupKeepCache  bool
	CleanupKeepHist   bool
	CleanupKeepMemory bool
	CleanupKeepAuth   bool
	CleanupBuildCache bool
	CleanupDryRun     bool
}

// ImgOptions carries arguments for the host-side `img import` command.
type ImgOptions struct {
	// ImgScreenshot captures a region screenshot instead of reading the clipboard.
	ImgScreenshot bool
	// ImgNoCopy suppresses copying the resulting container path to the host clipboard.
	ImgNoCopy bool
}

// StatusOptions carries arguments for the `status` command.
type StatusOptions struct {
	// StatusJSON emits machine-readable snapshot objects instead of a table.
	StatusJSON bool
	// StatusAll reports sessions from all projects instead of only the
	// project resolved from the working directory.
	StatusAll bool
}

// DefaultStatusLines is the agreed screen-snapshot height: enough bottom rows
// for state detection without shipping whole scrollback-sized panes.
const DefaultStatusLines = 24

// PSOptions carries arguments for the host-side `ps` container-listing command.
type PSOptions struct {
	// PSAll includes stopped containers in the listing, not just running ones.
	PSAll bool
	// PSJSON emits a JSON array instead of the human-readable table.
	PSJSON bool
}

// UpdateOptions carries arguments for the build-only `update` command.
type UpdateOptions struct {
	// UpdateTools is the list of tools whose images should be rebuilt with a
	// forced agent CLI update. Empty means the resolved default tool.
	UpdateTools []string
}

type Options struct {
	RunOptions
	AuthOptions
	BuildOptions
	CleanupOptions
	UpdateOptions
	ImgOptions
	StatusOptions
	PSOptions
	Sources OptionSources
}

type RuntimeConfig struct {
	Paths                 Paths
	Host                  Host
	Project               Project
	Profile               Profile
	Run                   RunOptions
	Auth                  AuthOptions
	Build                 BuildOptions
	RunSources            RunOptionSources
	Handler               RuntimeHandler
	Devcontainer          *DevcontainerConfig
	ValidatedDirs         []string
	ValidatedReadonlyDirs []string
	YoloEnabled           bool
	Features              []Extension
	UserCommandMount      *UserCommandMount
}

// UserCommandMount describes the read-only bind mount that exposes the host
// session command tree (~/.config/enclave/commands/session) inside the container at
// a fixed neutral path (UserCommandsContainerDir), independent of the host home
// layout. It is populated only for `enclave <name>` session commands.
type UserCommandMount struct {
	HostDir       string
	ContainerPath string
}

type RuntimeHandler interface {
	PortHints(ctx RunContext) []string
	LoopbackPorts(ctx RunContext) []string
	ValidateRun(ctx RunContext) error
}

type RunContext struct {
	Host       Host
	Project    Project
	Profile    Profile
	Run        RunOptions
	Auth       AuthOptions
	AuthState  AuthState
	Build      BuildOptions
	RunSources RunOptionSources
}

type ConfigView struct {
	// Mode selects the config output view: "matrix" (default), "effective",
	// "diff", or "source".
	Mode string
	JSON bool
}

type Profile struct {
	Name                string                  `json:"name"`
	Command             string                  `json:"command"`
	ContinueArgs        []string                `json:"continue_args,omitempty"`
	ResumeArgs          []string                `json:"resume_args,omitempty"`
	YoloFlag            string                  `json:"yolo_flag"`
	YoloEnabled         *bool                   `json:"yolo_enabled"`
	ConfigDir           string                  `json:"config_dir"`
	SkillsDir           string                  `json:"skills_dir,omitempty"`
	MemoryDir           string                  `json:"memory_dir,omitempty"`
	SettingsFile        string                  `json:"settings_file"`
	SettingsTarget      string                  `json:"settings_target"`
	PassthroughPaths    []string                `json:"passthrough_paths,omitempty"`
	QEMUMinMemoryMiB    int                     `json:"qemu_min_memory_mib,omitempty"`
	QEMUStoreCacheMmap  bool                    `json:"qemu_store_cache_mmap,omitempty"`
	Ports               []PortConfig            `json:"ports,omitempty"`
	Providers           []ProviderConfig        `json:"providers"`
	Secrets             map[string]SecretConfig `json:"secrets,omitempty"`
	HostConfigDir       string                  `json:"host_config_dir"`
	HostCredentialsFile string                  `json:"host_credentials_file"`
	HostOAuthJSON       string                  `json:"host_oauth_json"`
	PostStart           *PostStartActions       `json:"post_start,omitempty"`
	// AllowedDomains and EnvVariables are populated from spec.yaml's
	// network.allowedDomains / environment.variables for runtime use.
	AllowedDomains []string `json:"allowedDomains,omitempty"`
	// DeniedDomains is populated from spec.yaml's network.deniedDomains. A denied
	// domain is blackholed even when a broader allow (e.g. a parent wildcard)
	// would otherwise resolve it; deny wins via dnsmasq's most-specific match.
	// Internal-only, like AllowedDomains.
	DeniedDomains []string          `json:"deniedDomains,omitempty"`
	EnvVariables  map[string]string `json:"environment,omitempty"`
	// ProxyManaged lists container env-var names that must be injected as
	// secret-release placeholders (populated from spec.yaml's
	// environment.proxyManaged) so the network proxy swaps them for the real
	// secret at request time. When a release-eligible secret has any of its
	// aliases listed here, only those aliases carry the placeholder; the rest
	// receive the raw value. Internal-only, like AllowedDomains/EnvVariables.
	ProxyManaged []string `json:"proxyManaged,omitempty"`
}

// PostStartActions describes side effects to perform once the container is
// running. Currently used to auto-launch a host IDE attached to the container
// (e.g. Theia / Theia-Next).
type PostStartActions struct {
	// OpenIDE names the host IDE to launch. Supported values: "theia",
	// "theia-next". Empty means no IDE launch.
	OpenIDE string `json:"open_ide,omitempty"`
}

type SecretConfig struct {
	EnvVars []string             `json:"env_vars"`
	Release *SecretReleaseConfig `json:"release,omitempty"`
	APIKey  *bool                `json:"api_key,omitempty"`
	// File names an optional host file the secret can be sourced from, in
	// addition to EnvVars. Priority (env-first | file-first) orders the env
	// aliases against the file; empty means env-first.
	File     *SecretFileSource `json:"file,omitempty"`
	Priority string            `json:"priority,omitempty"`
}

// SecretFileSource sources a secret from a host file rather than an env var.
// Parser is "" (raw trimmed contents) or "json:<dot.path>" (JSON extraction).
type SecretFileSource struct {
	Path   string `json:"path"`
	Parser string `json:"parser,omitempty"`
}

type SecretReleaseConfig struct {
	HTTP *HTTPSecretReleaseConfig `json:"http,omitempty"`
}

type HTTPSecretReleaseConfig struct {
	Hosts  []string `json:"hosts"`
	Header string   `json:"header"`
	Format string   `json:"format,omitempty"`
}

// ReleaseHosts returns the sorted, deduplicated HTTP release hosts declared
// across secrets. These are the hosts the gateway proxy injects credentials
// for (spec.yaml network.serviceDomains / serviceAuth), so a session must be
// able to resolve them: callers union them into the allow set, otherwise a
// service declared only via serviceDomains would carry a release rule for a
// host the sandbox can never reach.
func ReleaseHosts(secrets map[string]SecretConfig) []string {
	seen := map[string]struct{}{}
	hosts := make([]string, 0)
	for _, sc := range secrets {
		if sc.Release == nil || sc.Release.HTTP == nil {
			continue
		}
		for _, host := range sc.Release.HTTP.Hosts {
			if _, ok := seen[host]; ok {
				continue
			}
			seen[host] = struct{}{}
			hosts = append(hosts, host)
		}
	}
	sort.Strings(hosts)
	return hosts
}

type AuthSessionConfig struct {
	Mode   string             `json:"mode"`
	Checks []AuthSessionCheck `json:"checks"`
}

type AuthSessionCheck struct {
	File    string `json:"file"`
	Type    string `json:"type"`
	Pointer string `json:"pointer,omitempty"`
}

type ProviderConfig struct {
	Name              string             `json:"name"`
	CredentialSecrets []string           `json:"credential_secrets,omitempty"`
	AuthFiles         []string           `json:"auth_files,omitempty"`
	AuthSession       *AuthSessionConfig `json:"auth_session,omitempty"`
	OAuthPorts        []OAuthPortConfig  `json:"oauth_ports,omitempty"`
	// SecurestorageDirEnv, when set, names an environment variable the tool
	// reads to locate its credential-storage directory independently of its
	// main config directory. enclave points it at the shared auth store so
	// the tool reads/writes its credential file (and any refresh lock) directly
	// in the shared location, enabling native cross-session credential
	// coordination instead of per-session copies. Used by Claude's
	// CLAUDE_SECURESTORAGE_CONFIG_DIR.
	SecurestorageDirEnv string `json:"securestorage_dir_env,omitempty"`
}

type OAuthPortConfig struct {
	Port                            string `json:"port"`
	AutoHintWhenNoSession           *bool  `json:"auto_hint_when_no_session,omitempty"`
	RequireMappingWhenNoCredentials *bool  `json:"require_mapping_when_no_credentials,omitempty"`
}

// PortConfig declares a container port a tool or feature publishes to the
// host. Entries with Publish true are bound on the run path (loopback by
// default; on the gateway container under network isolation, the tool
// container otherwise). HostAllocation selects how the host port is chosen:
// "fixed" (the default) mirrors the container port, while "auto" requests an
// OS-assigned host port (Docker's "0" sentinel) so concurrent sessions do not
// contend for the same host port.
type PortConfig struct {
	Container      int    `json:"container"`
	HostAllocation string `json:"host_allocation,omitempty"`
	Publish        bool   `json:"publish,omitempty"`
	Label          string `json:"label,omitempty"`
	OpenURL        string `json:"open_url,omitempty"`
}

// Host-port allocation modes for PortConfig.HostAllocation. An empty value is
// treated as HostAllocationFixed.
const (
	HostAllocationFixed = "fixed"
	HostAllocationAuto  = "auto"
)

// PortHostPlaceholder is substituted with the resolved host port when rendering
// a PortConfig.OpenURL.
const PortHostPlaceholder = "{host_port}"

func (p Profile) YoloEnabledValue() bool {
	if p.YoloEnabled == nil {
		return true
	}
	return *p.YoloEnabled
}

type Extension struct {
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	AptPackages []string `json:"apt_packages,omitempty"`
	NeedsRoot   bool     `json:"needs_root,omitempty"`
	// InstallCommandUsers is the user field of each commands.install entry, in
	// order. Its presence marks a mixin as using declarative install steps; a
	// root entry forces the root build phase.
	InstallCommandUsers []string                `json:"install_command_users,omitempty"`
	DefaultEnabled      bool                    `json:"default_enabled,omitempty"`
	DefaultIncluded     bool                    `json:"default_included,omitempty"`
	Priority            int                     `json:"priority,omitempty"`
	ConfigDir           string                  `json:"config_dir,omitempty"`
	AuthFiles           []string                `json:"auth_files,omitempty"`
	Secrets             map[string]SecretConfig `json:"secrets,omitempty"`
	// AllowedDomains/DeniedDomains/EnvVariables/ProxyManaged mirror the
	// same-named Profile fields for mixins: populated from a mixin spec.yaml's
	// network/environment blocks and consumed by the session runtime (env
	// injection, network policy merge, placeholder selection), so a service
	// mixin can declare its own reachability and env without relying on the
	// tool spec. Internal-only, like the Profile counterparts.
	AllowedDomains []string          `json:"allowedDomains,omitempty"`
	DeniedDomains  []string          `json:"deniedDomains,omitempty"`
	EnvVariables   map[string]string `json:"environment,omitempty"`
	ProxyManaged   []string          `json:"proxyManaged,omitempty"`
	// Ports mirrors Profile.Ports for mixins: declared entries with Publish
	// true are bound for sessions that enable the feature, flowing through the
	// same resolution as a user-supplied -p.
	Ports []PortConfig `json:"ports,omitempty"`
}

func (e Extension) IsMixin() bool   { return e.Type == ExtensionKindMixin }
func (e Extension) IsSandbox() bool { return e.Type == ExtensionKindSandbox || e.Type == "" }

type Paths struct {
	AppRoot           string
	Dockerfile        string
	Entrypoint        string
	GatewayDockerfile string
	GatewayEntrypoint string
	ExtensionsDir     string
	ToolsDir          string // extensions/tools/
	FeaturesDir       string // extensions/features/
	UserExtensionsDir string // ~/.config/enclave/extensions/
	UserToolsDir      string // ~/.config/enclave/extensions/tools/
	UserFeaturesDir   string // ~/.config/enclave/extensions/features/
	AllowlistsDir     string // For gateway allowlist fragments (shared across extensions)
	BuildScriptsDir   string // Docker weaving scripts copied into image during build
}

type Project struct {
	Dir     string
	RealDir string
	Hash    string
	Name    string
}

type Host struct {
	Home string
	UID  string
	GID  string
}

type DevcontainerConfig struct {
	WorkspaceFolder    string
	WorkspaceMount     string
	RemoteUser         string
	ContainerEnv       map[string]string
	RunArgs            []string
	Mounts             []string
	PostCreateCommand  string
	PostStartCommand   string
	UpdateRemoteUserID *bool
	ForwardPorts       []string
}

const (
	AppName = "enclave"
	// AppID is the reverse-DNS application identifier. It names the app-specific
	// host directories on platforms that follow that convention (macOS
	// Library/Application Support and Library/Caches).
	AppID                   = "org.eclipse." + AppName
	Version                 = "1.0.0"
	HostnamePrefix          = AppName + "-"
	ContainerUser           = "agent"
	ContainerHome           = "/home/" + ContainerUser
	ContainerAuthDir        = "." + AppName + "-auth"
	ContainerFeatureAuthDir = "." + AppName + "-feature-auth"
	// UserCommandsContainerDir is the fixed, home-layout-neutral path where the
	// host session command tree is mounted read-only inside the container.
	UserCommandsContainerDir = "/opt/" + AppName + "/commands"
	ContainerImageInboxDir   = "/mnt/host-images"
	// DefaultImageInboxMaxBytes caps an imported image at 10 MiB: large enough
	// for real screenshots and pasted images, small enough to reject clipboard
	// payloads that are clearly not the intended content.
	DefaultImageInboxMaxBytes = 10 << 20
	// SessionMonitorTmuxSocket / SessionMonitorTmuxSession name the managed
	// tmux session the entrypoint creates when the session monitor is enabled.
	// They are a contract with entrypoint.sh; `status` uses them to capture
	// terminal snapshots via `tmux capture-pane` inside the container.
	SessionMonitorTmuxSocket  = AppName
	SessionMonitorTmuxSession = "main"
	ImageName                 = AppName + ":latest"
	AlpineImage               = "alpine@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659"
	HashLength                = 12
	DetachKeysDefault         = `ctrl-\`
)

const (
	LabelHash       = AppName + ".hash"
	LabelVersion    = AppName + ".version"
	LabelBuilt      = AppName + ".built"
	LabelAgent      = AppName + ".agent"
	LabelEphemeral  = AppName + ".ephemeral"
	LabelBackground = AppName + ".background"
	LabelSession    = AppName + ".session"
	LabelWorktree   = AppName + ".worktree"
	// LabelProjectDir records the absolute, symlink-resolved project directory
	// for a session. Unlike LabelWorktree (which carries the directory as it was
	// passed on the command line), this always holds the canonical path. It is
	// currently surfaced only to `ps --json` consumers; internal matching (e.g.
	// exec.go) still keys off LabelWorktree.
	LabelProjectDir = AppName + ".project_dir"
	LabelConfigKey  = AppName + ".config_key"
	// LabelImageInbox marks a container started with --image-inbox (its value is
	// "true"). The inbox directory is global rather than session-derived; this
	// label only lets `img import` detect inbox-enabled sessions.
	LabelImageInbox = AppName + ".image_inbox"
	LabelYolo       = AppName + ".yolo"
	// LabelSessionMonitor marks a container whose agent runs under the managed
	// tmux session (its value is "true"), so `status` knows a terminal snapshot
	// can be captured via tmux inside the container.
	LabelSessionMonitor = AppName + ".session_monitor"
	// LabelSessionMonitorUser records the user that owns the managed tmux
	// server, so `status` captures via `docker exec --user <user>` and reaches
	// the right per-user tmux socket even when the container's default exec
	// user differs (e.g. root under --runtime-uid-remap).
	LabelSessionMonitorUser = AppName + ".session_monitor_user"
)

const (
	TemplatesDir           = "templates"
	HomeConfigDirName      = "home-config"
	SkillsDirName          = "skills"
	GeneratedSkillsDirName = "skills-generated"
	GeneratedConfigDirName = "config-generated"
)

const (
	DevcontainerDir      = ".devcontainer"
	DevcontainerFilename = "devcontainer.json"
)

const (
	AuthScopeShared  = "shared"
	AuthScopeProject = "project"
)

const (
	HostConfigNone        = "none"
	HostConfigPassthrough = "passthrough"
)

const (
	ProjectMountWritable = "writable"
	ProjectMountReadonly = "readonly"
)

const (
	WorktreeMetadataFollow   = "follow"
	WorktreeMetadataReadonly = "readonly"
	WorktreeMetadataNone     = "none"
)

const (
	NetworkModeRestricted   = "restricted"
	NetworkModeUnrestricted = "unrestricted"
	NetworkLogCoarse        = "coarse"
	NetworkLogRequests      = "requests"
)

const (
	SecretsScopeProject = "project"
	SecretsScopeGlobal  = "global"
	SecretsScopeBoth    = "both"
)

const (
	BuildProgressQuiet   = "quiet"
	BuildProgressCompact = "compact"
	BuildProgressVerbose = "verbose"
)

const (
	AuthSessionModeAny = "any"
	AuthSessionModeAll = "all"
)

// Credential-source priority: orders a secret's env aliases against its file
// source. env-first (the default) consults env aliases before the file;
// file-first consults the file first.
const (
	SecretPriorityEnvFirst  = "env-first"
	SecretPriorityFileFirst = "file-first"
)

// SelectionDefault is the keyword for "all default-enabled" features.
// FeatureSelectionAll ("all") means literally every feature.
// Images are always per-tool; there is no image-mode selector.
const (
	SelectionDefault    = "default"
	FeatureSelectionAll = "all"
)

// Extension kind discriminator values, matching the public spec.yaml
// `kind: sandbox|mixin` tokens. These are the canonical (and only accepted)
// values for Extension.Type.
const (
	ExtensionKindSandbox = "sandbox"
	ExtensionKindMixin   = "mixin"
)

const (
	InstallScriptFilename     = "install.sh"
	CheckUpdateScriptFilename = "check-update.sh"
	AllowlistFilename         = "gateway-allowlist.conf"
	DefaultExtensionPriority  = 100
)

const (
	GatewayImageTagLatest        = "latest"
	GatewayImagePrefix           = AppName + "-gateway-"
	GatewayContainerSuffix       = "-gateway"
	GatewayLabelManaged          = AppName + ".gateway"
	GatewayLabelHash             = AppName + ".gateway.hash"
	GatewayLabelAgent            = LabelAgent
	GatewayLabelProjectHash      = AppName + ".gateway.project_hash"
	GatewayLabelProjectDir       = AppName + ".gateway.project_dir"
	GatewayLabelWorkspaceHash    = AppName + ".gateway.workspace_hash"
	GatewayLabelContainer        = AppName + ".gateway.container"
	GatewayAllowlistsDir         = "/etc/dnsmasq.allowlists"
	DNSMasqConfigDir             = "/etc/dnsmasq.d"
	GatewayDomainsConfigExt      = ".conf"
	GatewayAllowlistFilename     = AllowlistFilename
	GatewayAllowlistOverridePath = DNSMasqConfigDir + "/allowlist.conf"
	GatewayConfigDir             = "/etc/enclave-gateway-config"
	GatewayConfigDNSMasqPath     = GatewayConfigDir + "/dnsmasq.conf"
	GatewayConfigDomainsPath     = GatewayConfigDir + "/domains.txt"
	GatewayConfigDeniedPath      = GatewayConfigDir + "/denied.txt"
	GatewayConfigMetaPath        = GatewayConfigDir + "/meta.json"
	GatewaySecretReleasePath     = "/etc/enclave/secret-releases.json" // #nosec G101 -- configuration file path, not a credential.
	GatewayTLSRootPath           = "/tls"
	GatewayTLSCACertPath         = GatewayTLSRootPath + "/ca.crt"
	GatewayTLSCAKeyPath          = GatewayTLSRootPath + "/ca.key"
	GatewayTLSHostsPath          = GatewayTLSRootPath + "/hosts"
	AgentGatewayCACertPath       = "/usr/local/share/ca-certificates/enclave-gateway.crt"
)
