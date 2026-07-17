// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

//go:generate go run -tags=gen ../../scripts/gen_options.go

type OptionGroup string

const (
	OptionGroupGlobal OptionGroup = "global"
	OptionGroupRun    OptionGroup = "run"
	OptionGroupAuth   OptionGroup = "auth"
	OptionGroupBuild  OptionGroup = "build"
)

type OptionKind string

const (
	OptionKindString      OptionKind = "string"
	OptionKindBool        OptionKind = "bool"
	OptionKindStringSlice OptionKind = "string_slice"
	OptionKindYolo        OptionKind = "yolo"
)

type ApplyKind string

const (
	ApplyNone              ApplyKind = "none"
	ApplyBoolPtr           ApplyKind = "bool_ptr"
	ApplyString            ApplyKind = "string"
	ApplySliceIfEmpty      ApplyKind = "slice_if_empty"
	ApplySliceMergeFeature ApplyKind = "slice_merge_feature"
	ApplySliceMergeHost    ApplyKind = "slice_merge_host"
	ApplyYoloDefault       ApplyKind = "yolo_default"
)

type CLIActionKind string

const (
	CLIActionSetBool      CLIActionKind = "set_bool"
	CLIActionSetBoolPtr   CLIActionKind = "set_bool_ptr"
	CLIActionSetString    CLIActionKind = "set_string"
	CLIActionAppendString CLIActionKind = "append_string"
	CLIActionCall         CLIActionKind = "call"
)

type OptionDef struct {
	Name           string
	Group          OptionGroup
	Kind           OptionKind
	OptionField    string
	SourceField    string
	DefaultsField  string
	DefaultRequire bool
	Apply          ApplyKind
	TrimOnApply    bool
	CLIFlags       []CLIFlagDef
}

type CLIFlagDef struct {
	Name                string
	Usage               string
	ValueKind           CLIValueKind
	MissingValueMessage string
	Action              CLIAction
}

type CLIAction struct {
	Kind         CLIActionKind
	OptionField  string
	SourceField  string
	BoolValue    bool
	SetFlagField string
	Call         string
}

func OptionDefs() []OptionDef {
	return []OptionDef{
		{
			Name:           "tool",
			Group:          OptionGroupRun,
			Kind:           OptionKindString,
			OptionField:    "Tool",
			SourceField:    "Tool",
			DefaultsField:  "Tool",
			DefaultRequire: true,
			Apply:          ApplyString,
			TrimOnApply:    true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--tool",
					Usage:               "Tool profile (default: claude)",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--tool requires a value (see --help for available tools)",
					Action: CLIAction{
						Kind:        CLIActionSetString,
						OptionField: "Tool",
						SourceField: "Tool",
					},
				},
			},
		},
		{
			Name:           "backend",
			Group:          OptionGroupRun,
			Kind:           OptionKindString,
			OptionField:    "Backend",
			SourceField:    "Backend",
			DefaultsField:  "Backend",
			DefaultRequire: true,
			Apply:          ApplyString,
			TrimOnApply:    true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--backend",
					Usage:               "Isolation backend: docker|qemu (default: docker)",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--backend requires a value (docker|qemu)",
					Action: CLIAction{
						Kind:        CLIActionSetString,
						OptionField: "Backend",
						SourceField: "Backend",
					},
				},
			},
		},
		{
			Name:          "host_config",
			Group:         OptionGroupRun,
			Kind:          OptionKindString,
			OptionField:   "HostConfig",
			SourceField:   "HostConfig",
			DefaultsField: "HostConfig",
			Apply:         ApplyString,
			TrimOnApply:   true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--host-config",
					Usage:               "Host config reuse: none|passthrough",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--host-config requires a value (none|passthrough)",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyHostConfig",
						SourceField: "HostConfig",
					},
				},
			},
		},
		{
			Name:          "host_config_paths",
			Group:         OptionGroupRun,
			Kind:          OptionKindStringSlice,
			OptionField:   "HostConfigPaths",
			SourceField:   "HostConfigPaths",
			DefaultsField: "HostConfigPaths",
			Apply:         ApplySliceMergeHost,
		},
		{
			Name:          "yolo",
			Group:         OptionGroupRun,
			Kind:          OptionKindYolo,
			DefaultsField: "Yolo",
			SourceField:   "Yolo",
			Apply:         ApplyYoloDefault,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--yolo",
					Usage:     "Enable YOLO mode",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBoolPtr,
						OptionField: "YoloOverride",
						SourceField: "Yolo",
						BoolValue:   true,
					},
				},
				{
					Name:      "--no-yolo",
					Usage:     "Disable YOLO mode",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBoolPtr,
						OptionField: "YoloOverride",
						SourceField: "Yolo",
						BoolValue:   false,
					},
				},
			},
		},
		{
			Name:          "ephemeral",
			Group:         OptionGroupRun,
			Kind:          OptionKindBool,
			OptionField:   "Ephemeral",
			SourceField:   "Ephemeral",
			DefaultsField: "Ephemeral",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--ephemeral",
					Usage:     "Ephemeral session (no auth or env stores)",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "Ephemeral",
						SourceField: "Ephemeral",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:           "auth_scope",
			Group:          OptionGroupAuth,
			Kind:           OptionKindString,
			OptionField:    "AuthScope",
			SourceField:    "AuthScope",
			DefaultsField:  "AuthScope",
			DefaultRequire: true,
			Apply:          ApplyString,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--auth-scope",
					Usage:               "Auth scope: shared|project",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--auth-scope requires a value (shared|project)",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyAuthScope",
						SourceField: "AuthScope",
					},
				},
			},
		},
		{
			Name:          "auth_name",
			Group:         OptionGroupAuth,
			Kind:          OptionKindString,
			OptionField:   "AuthName",
			SourceField:   "AuthName",
			DefaultsField: "AuthName",
			Apply:         ApplyString,
			TrimOnApply:   true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--auth-name",
					Usage:               "Named auth identity: select a separate shared auth store per tool",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--auth-name requires a value (e.g. personal, api)",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyAuthName",
						SourceField: "AuthName",
					},
				},
			},
		},
		{
			Name:          "reset_auth",
			Group:         OptionGroupAuth,
			Kind:          OptionKindBool,
			OptionField:   "ResetAuth",
			SourceField:   "ResetAuth",
			DefaultsField: "ResetAuth",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--reset-auth",
					Usage:     "Clear stored auth files and persisted API keys",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "ResetAuth",
						SourceField: "ResetAuth",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "no_api_key",
			Group:         OptionGroupAuth,
			Kind:          OptionKindBool,
			OptionField:   "NoAPIKey",
			SourceField:   "NoAPIKey",
			DefaultsField: "NoAPIKey",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--no-api-key",
					Usage:     "Disable API key injection",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "NoAPIKey",
						SourceField: "NoAPIKey",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "pass_api_key",
			Group:         OptionGroupAuth,
			Kind:          OptionKindBool,
			OptionField:   "PassAPIKey",
			SourceField:   "PassAPIKey",
			DefaultsField: "PassAPIKey",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--pass-api-key",
					Usage:     "Allow API key injection in --ephemeral mode",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "PassAPIKey",
						SourceField: "PassAPIKey",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:           "secrets_scope",
			Group:          OptionGroupAuth,
			Kind:           OptionKindString,
			OptionField:    "SecretsScope",
			SourceField:    "SecretsScope",
			DefaultsField:  "SecretsScope",
			DefaultRequire: true,
			Apply:          ApplyString,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--secrets-scope",
					Usage:               "Secrets scope: project|global|both",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--secrets-scope requires a value (project|global|both)",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applySecretsScope",
						SourceField: "SecretsScope",
					},
				},
			},
		},
		{
			Name:          "pass_env",
			Group:         OptionGroupAuth,
			Kind:          OptionKindStringSlice,
			OptionField:   "PassEnv",
			SourceField:   "PassEnv",
			DefaultsField: "PassEnv",
			Apply:         ApplySliceIfEmpty,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--pass-env",
					Usage:               "Env vars to pass into the container (comma-separated)",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--pass-env requires a value",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyPassEnv",
						SourceField: "PassEnv",
					},
				},
			},
		},
		{
			Name:          "allow_all_network",
			Group:         OptionGroupRun,
			Kind:          OptionKindBool,
			OptionField:   "AllowAllNetwork",
			SourceField:   "AllowAllNetwork",
			DefaultsField: "AllowAllNetwork",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--allow-all-network",
					Usage:     "Disable network restrictions",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "AllowAllNetwork",
						SourceField: "AllowAllNetwork",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "no_cache",
			Group:         OptionGroupRun,
			Kind:          OptionKindBool,
			OptionField:   "NoCache",
			SourceField:   "NoCache",
			DefaultsField: "NoCache",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--no-cache",
					Usage:     "Disable package caches",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "NoCache",
						SourceField: "NoCache",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "session_monitor",
			Group:         OptionGroupRun,
			Kind:          OptionKindBool,
			OptionField:   "SessionMonitor",
			SourceField:   "SessionMonitor",
			DefaultsField: "SessionMonitor",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--session-monitor",
					Usage:     "Run the agent under the managed tmux session (enables status snapshots)",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "SessionMonitor",
						SourceField: "SessionMonitor",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "no_history",
			Group:         OptionGroupRun,
			Kind:          OptionKindBool,
			OptionField:   "NoHistory",
			SourceField:   "NoHistory",
			DefaultsField: "NoHistory",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--no-history",
					Usage:     "Disable shell history",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "NoHistory",
						SourceField: "NoHistory",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "no_memory",
			Group:         OptionGroupRun,
			Kind:          OptionKindBool,
			OptionField:   "NoMemory",
			SourceField:   "NoMemory",
			DefaultsField: "NoMemory",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--no-memory",
					Usage:     "Disable per-project agent memory",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "NoMemory",
						SourceField: "NoMemory",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "image_inbox",
			Group:         OptionGroupRun,
			Kind:          OptionKindBool,
			OptionField:   "ImageInbox",
			SourceField:   "ImageInbox",
			DefaultsField: "ImageInbox",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--image-inbox",
					Usage:     "Mount a read-only host image inbox at /mnt/host-images",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "ImageInbox",
						SourceField: "ImageInbox",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:        "force_rebuild",
			Group:       OptionGroupBuild,
			Kind:        OptionKindBool,
			OptionField: "ForceRebuild",
			SourceField: "ForceRebuild",
			Apply:       ApplyNone,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--rebuild",
					Usage:     "Force image rebuild",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "ForceRebuild",
						SourceField: "ForceRebuild",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:        "no_rebuild",
			Group:       OptionGroupBuild,
			Kind:        OptionKindBool,
			OptionField: "NoRebuild",
			SourceField: "NoRebuild",
			Apply:       ApplyNone,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--no-rebuild",
					Usage:     "Use existing images and suppress all image builds",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "NoRebuild",
						SourceField: "NoRebuild",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:        "force_base_image",
			Group:       OptionGroupBuild,
			Kind:        OptionKindBool,
			OptionField: "ForceBaseImage",
			SourceField: "ForceBaseImage",
			Apply:       ApplyNone,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--force-base-image",
					Usage:     "Bypass devcontainer base image compatibility checks",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "ForceBaseImage",
						SourceField: "ForceBaseImage",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "base_image",
			Group:         OptionGroupBuild,
			Kind:          OptionKindString,
			OptionField:   "BaseImage",
			SourceField:   "BaseImage",
			DefaultsField: "BaseImage",
			Apply:         ApplyString,
			TrimOnApply:   true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--base-image",
					Usage:               "Override base image",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--base-image requires a value",
					Action: CLIAction{
						Kind:        CLIActionSetString,
						OptionField: "BaseImage",
						SourceField: "BaseImage",
					},
				},
			},
		},
		{
			Name:          "devcontainer",
			Group:         OptionGroupBuild,
			Kind:          OptionKindBool,
			OptionField:   "Devcontainer",
			SourceField:   "Devcontainer",
			DefaultsField: "Devcontainer",
			Apply:         ApplyBoolPtr,
		},
		{
			Name:          "use_remote_user",
			Group:         OptionGroupBuild,
			Kind:          OptionKindBool,
			OptionField:   "UseRemoteUser",
			SourceField:   "UseRemoteUser",
			DefaultsField: "UseRemoteUser",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--use-remote-user",
					Usage:     "Use devcontainer remoteUser",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "UseRemoteUser",
						SourceField: "UseRemoteUser",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "slim",
			Group:         OptionGroupBuild,
			Kind:          OptionKindBool,
			OptionField:   "Slim",
			SourceField:   "Slim",
			DefaultsField: "Slim",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--slim",
					Usage:     "Tools only, no features",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "Slim",
						SourceField: "Slim",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:           "image_name",
			Group:          OptionGroupBuild,
			Kind:           OptionKindString,
			OptionField:    "ImageName",
			SourceField:    "ImageName",
			DefaultsField:  "ImageName",
			DefaultRequire: true,
			Apply:          ApplyString,
			TrimOnApply:    true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--image-name",
					Usage:               "Override image name",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--image-name requires a value",
					Action: CLIAction{
						Kind:         CLIActionSetString,
						OptionField:  "ImageName",
						SourceField:  "ImageName",
						SetFlagField: "ImageNameSet",
					},
				},
			},
		},
		{
			Name:          "features",
			Group:         OptionGroupBuild,
			Kind:          OptionKindStringSlice,
			OptionField:   "Features",
			SourceField:   "Features",
			DefaultsField: "Features",
			Apply:         ApplySliceMergeFeature,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--features",
					Usage:               "Enable features (comma-separated), or use default|all|none. In devcontainer mode, features default to none unless set",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--features requires a value",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyFeatures",
						SourceField: "Features",
					},
				},
			},
		},
		{
			Name:          "cache_from",
			Group:         OptionGroupBuild,
			Kind:          OptionKindStringSlice,
			OptionField:   "CacheFrom",
			SourceField:   "CacheFrom",
			DefaultsField: "CacheFrom",
			Apply:         ApplySliceIfEmpty,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--cache-from",
					Usage:               "Reuse build cache from image",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--cache-from requires a value",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyCacheFrom",
						SourceField: "CacheFrom",
					},
				},
			},
		},
		{
			Name:        "build_uid",
			Group:       OptionGroupBuild,
			Kind:        OptionKindString,
			OptionField: "BuildUID",
			SourceField: "BuildUID",
			Apply:       ApplyNone,
			TrimOnApply: true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--build-uid",
					Usage:               "UID to bake into the runtime image",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--build-uid requires a numeric UID",
					Action: CLIAction{
						Kind:        CLIActionSetString,
						OptionField: "BuildUID",
						SourceField: "BuildUID",
					},
				},
			},
		},
		{
			Name:        "build_gid",
			Group:       OptionGroupBuild,
			Kind:        OptionKindString,
			OptionField: "BuildGID",
			SourceField: "BuildGID",
			Apply:       ApplyNone,
			TrimOnApply: true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--build-gid",
					Usage:               "GID to bake into the runtime image",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--build-gid requires a numeric GID",
					Action: CLIAction{
						Kind:        CLIActionSetString,
						OptionField: "BuildGID",
						SourceField: "BuildGID",
					},
				},
			},
		},
		{
			Name:        "runtime_uid_remap",
			Group:       OptionGroupBuild,
			Kind:        OptionKindBool,
			OptionField: "RuntimeUIDRemap",
			SourceField: "RuntimeUIDRemap",
			Apply:       ApplyNone,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--runtime-uid-remap",
					Usage:     "Run canonical-UID images after remapping agent to the host UID/GID",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "RuntimeUIDRemap",
						SourceField: "RuntimeUIDRemap",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:        "buildx_cache_dir",
			Group:       OptionGroupBuild,
			Kind:        OptionKindString,
			OptionField: "BuildxCacheDir",
			SourceField: "BuildxCacheDir",
			Apply:       ApplyNone,
			TrimOnApply: true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--buildx-cache-dir",
					Usage:               "Local buildx cache directory (mode=max export, import when populated)",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--buildx-cache-dir requires a directory path",
					Action: CLIAction{
						Kind:        CLIActionSetString,
						OptionField: "BuildxCacheDir",
						SourceField: "BuildxCacheDir",
					},
				},
			},
		},
		{
			Name:        "buildx_cache_from",
			Group:       OptionGroupBuild,
			Kind:        OptionKindStringSlice,
			OptionField: "BuildxCacheFrom",
			SourceField: "BuildxCacheFrom",
			Apply:       ApplyNone,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--buildx-cache-from",
					Usage:               "Raw buildx cache import spec (repeatable)",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--buildx-cache-from requires a cache spec",
					Action: CLIAction{
						Kind:        CLIActionAppendString,
						OptionField: "BuildxCacheFrom",
						SourceField: "BuildxCacheFrom",
					},
				},
			},
		},
		{
			Name:        "buildx_cache_to",
			Group:       OptionGroupBuild,
			Kind:        OptionKindStringSlice,
			OptionField: "BuildxCacheTo",
			SourceField: "BuildxCacheTo",
			Apply:       ApplyNone,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--buildx-cache-to",
					Usage:               "Raw buildx cache export spec (repeatable)",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--buildx-cache-to requires a cache spec",
					Action: CLIAction{
						Kind:        CLIActionAppendString,
						OptionField: "BuildxCacheTo",
						SourceField: "BuildxCacheTo",
					},
				},
			},
		},
		{
			Name:           "progress",
			Group:          OptionGroupBuild,
			Kind:           OptionKindString,
			OptionField:    "Progress",
			SourceField:    "Progress",
			DefaultsField:  "Progress",
			DefaultRequire: true,
			Apply:          ApplyString,
			TrimOnApply:    true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--progress",
					Usage:               "Build output: quiet|compact|verbose",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--progress requires a value (quiet|compact|verbose)",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyProgress",
						SourceField: "Progress",
					},
				},
			},
		},
		{
			Name:           "network_log",
			Group:          OptionGroupRun,
			Kind:           OptionKindString,
			OptionField:    "NetworkLog",
			SourceField:    "NetworkLog",
			DefaultsField:  "NetworkLog",
			DefaultRequire: true,
			Apply:          ApplyString,
			TrimOnApply:    true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--network-log",
					Usage:               "Network logging mode: coarse|requests",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--network-log requires a value (coarse|requests)",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyNetworkLog",
						SourceField: "NetworkLog",
					},
				},
			},
		},
		{
			Name:          "verbose",
			Group:         OptionGroupGlobal,
			Kind:          OptionKindBool,
			OptionField:   "Verbose",
			SourceField:   "Verbose",
			DefaultsField: "Verbose",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--verbose",
					Usage:     "Enable verbose logging",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "Verbose",
						SourceField: "Verbose",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "ports",
			Group:         OptionGroupRun,
			Kind:          OptionKindStringSlice,
			OptionField:   "Ports",
			SourceField:   "Ports",
			DefaultsField: "Ports",
			Apply:         ApplySliceIfEmpty,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "-p",
					Usage:               "Forward port to container",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "-p requires a value",
					Action: CLIAction{
						Kind:        CLIActionAppendString,
						OptionField: "Ports",
						SourceField: "Ports",
					},
				},
			},
		},
		{
			Name:        "session_name",
			Group:       OptionGroupRun,
			Kind:        OptionKindString,
			OptionField: "SessionName",
			SourceField: "SessionName",
			Apply:       ApplyNone,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--name",
					Usage:               "Session name (auto-generated if omitted)",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--name requires a value",
					Action: CLIAction{
						Kind:        CLIActionSetString,
						OptionField: "SessionName",
						SourceField: "SessionName",
					},
				},
			},
		},
		{
			Name:          "add_dirs",
			Group:         OptionGroupRun,
			Kind:          OptionKindStringSlice,
			OptionField:   "AddDirs",
			SourceField:   "AddDirs",
			DefaultsField: "AddDirs",
			Apply:         ApplySliceIfEmpty,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--add-dir",
					Usage:               "Mount additional directory",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--add-dir requires a directory path",
					Action: CLIAction{
						Kind:        CLIActionAppendString,
						OptionField: "AddDirs",
						SourceField: "AddDirs",
					},
				},
			},
		},
		{
			Name:          "add_readonly_dirs",
			Group:         OptionGroupRun,
			Kind:          OptionKindStringSlice,
			OptionField:   "AddReadonlyDirs",
			SourceField:   "AddReadonlyDirs",
			DefaultsField: "AddReadonlyDirs",
			Apply:         ApplySliceIfEmpty,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--add-readonly-dir",
					Usage:               "Mount additional directory read-only",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--add-readonly-dir requires a directory path",
					Action: CLIAction{
						Kind:        CLIActionAppendString,
						OptionField: "AddReadonlyDirs",
						SourceField: "AddReadonlyDirs",
					},
				},
			},
		},
		{
			Name:          "project_mount",
			Group:         OptionGroupRun,
			Kind:          OptionKindString,
			OptionField:   "ProjectMount",
			SourceField:   "ProjectMount",
			DefaultsField: "ProjectMount",
			Apply:         ApplyString,
			TrimOnApply:   true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--project-mount",
					Usage:               "Project mount mode: writable|readonly",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--project-mount requires a value (writable|readonly)",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyProjectMount",
						SourceField: "ProjectMount",
					},
				},
			},
		},
		{
			Name:          "worktree_metadata",
			Group:         OptionGroupRun,
			Kind:          OptionKindString,
			OptionField:   "WorktreeMetadata",
			SourceField:   "WorktreeMetadata",
			DefaultsField: "WorktreeMetadata",
			Apply:         ApplyString,
			TrimOnApply:   true,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--worktree-metadata",
					Usage:               "Linked-worktree git metadata mounts: follow|readonly|none",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--worktree-metadata requires a value (follow|readonly|none)",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyWorktreeMetadata",
						SourceField: "WorktreeMetadata",
					},
				},
			},
		},
		{
			Name:          "allow_domains",
			Group:         OptionGroupRun,
			Kind:          OptionKindStringSlice,
			OptionField:   "AllowDomains",
			SourceField:   "AllowDomains",
			DefaultsField: "AllowDomains",
			Apply:         ApplySliceIfEmpty,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--allow-domain",
					Usage:               "Add domain to gateway allowlist for this run only (repeatable)",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--allow-domain requires a domain name",
					Action: CLIAction{
						Kind:        CLIActionAppendString,
						OptionField: "AllowDomains",
						SourceField: "AllowDomains",
					},
				},
			},
		},
		{
			Name:          "playwright_mcp",
			Group:         OptionGroupRun,
			Kind:          OptionKindBool,
			OptionField:   "PlaywrightMCP",
			SourceField:   "PlaywrightMCP",
			DefaultsField: "PlaywrightMCP",
			Apply:         ApplyBoolPtr,
			CLIFlags: []CLIFlagDef{
				{
					Name:      "--playwright-mcp",
					Usage:     "Enable Playwright MCP server for browser automation",
					ValueKind: CLIValueNone,
					Action: CLIAction{
						Kind:        CLIActionSetBool,
						OptionField: "PlaywrightMCP",
						SourceField: "PlaywrightMCP",
						BoolValue:   true,
					},
				},
			},
		},
		{
			Name:          "bridge_ports",
			Group:         OptionGroupRun,
			Kind:          OptionKindStringSlice,
			OptionField:   "BridgePorts",
			SourceField:   "BridgePorts",
			DefaultsField: "BridgePorts",
			Apply:         ApplySliceIfEmpty,
			CLIFlags: []CLIFlagDef{
				{
					Name:                "--bridge-port",
					Usage:               "Bridge host port into container (repeatable, comma-separated)",
					ValueKind:           CLIValueRequired,
					MissingValueMessage: "--bridge-port requires a port number",
					Action: CLIAction{
						Kind:        CLIActionCall,
						Call:        "applyBridgePort",
						SourceField: "BridgePorts",
					},
				},
			},
		},
	}
}
