// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	dockercmd "enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

var authSyncMountPath = regexp.MustCompile(`(^|[[:space:]:'"])/(config|auth)/`)

// runRequestAuthSync reconciles credentials after a foreground session ends,
// driven by the request's declared intent. The container may already be gone
// (foreground sessions auto-remove), so nothing can be inspected; the store
// identities come from the request instead.
func (b *Backend) runRequestAuthSync(req backend.Request) {
	spec := req.AuthSync
	if spec == nil {
		return
	}
	// Detached from the caller's context on purpose: this runs as a deferred
	// post-run step, so a Ctrl-C cancelling the run context must not abort the
	// reconcile and silently drop credentials.
	ctx := context.Background()
	configDir := ""
	authDir := ""
	for _, store := range req.Stores {
		switch store.Kind {
		case backend.StoreKindConfig:
			configDir = b.resolveStoreDir(store.Key, store.Kind)
		case backend.StoreKindAuth:
			authDir = b.resolveStoreDir(store.Key, store.Kind)
		}
	}
	if configDir == "" {
		return
	}
	placeholders := backend.SecretPlaceholders(req.Secrets)
	if len(spec.AuthFiles) > 0 && authDir != "" {
		if err := b.syncSharedAuthStores(ctx, req.Image, req.Session.Tool, spec.AuthFiles, configDir, authDir, placeholders...); err != nil {
			logx.Warnf("Failed to sync shared auth files: %v", err)
		}
	}
	for _, sync := range spec.Features {
		if err := b.syncFeatureAuthStore(ctx, configDir, sync, placeholders...); err != nil {
			logx.Warnf("Failed to sync %s feature auth files: %v", sync.Feature, err)
		}
	}
}

// resolveStoreDir resolves a store's host directory, returning "" when the
// store key is incomplete or malformed.
func (b *Backend) resolveStoreDir(key backend.StoreKey, kind backend.StoreKind) string {
	dir, err := b.storage.storeDir(key, kind)
	if err != nil {
		return ""
	}
	return dir
}

// syncAfterExec makes session credentials durable immediately after an exec,
// mirroring the post-foreground sync: shared-auth reconcile plus feature-auth
// additive sync, both derived from the (still running) target container.
func (b *Backend) syncAfterExec(containerName string) {
	// Detached from the exec context on purpose: cancelling the exec must not
	// abort the credential reconcile and silently drop credentials.
	ctx := context.Background()
	if err := b.finalizeManagedContainerAuth(ctx, containerName); err != nil {
		logx.Warnf("Failed to sync shared auth files: %v", err)
	}
	b.syncFeatureAuthFromContainer(ctx, containerName)
}

// finalizeManagedContainerAuth reconciles auth files for a managed container
// after its tool process has stopped (or, for exec, gone quiescent). It
// discovers the config and auth store directories from the container's bind
// mounts and no-ops for unmanaged containers and sessions without a shared
// auth store.
func (b *Backend) finalizeManagedContainerAuth(ctx context.Context, containerName string) error {
	name := strings.TrimSpace(containerName)
	if name == "" {
		return nil
	}

	info, err := dockercmd.ContainerInspect(ctx, name)
	if err != nil {
		if dockercmd.IsNotFound(err) {
			return nil
		}
		return err
	}
	if info.Config == nil || strings.TrimSpace(info.Config.Labels[model.LabelAgent]) == "" {
		return nil
	}

	tool := strings.TrimSpace(info.Config.Labels[model.LabelAgent])
	configDest := containerEnvValue(info.Config, model.EnvToolConfigDir)
	authDest := containerEnvValue(info.Config, model.EnvAuthDir)
	authFilesValue := containerEnvValue(info.Config, model.EnvAuthFiles)
	if tool == "" || configDest == "" || authDest == "" || authFilesValue == "" {
		return nil
	}

	configDir := mountedSourceDir(info, configDest)
	authDir := mountedSourceDir(info, authDest)
	if configDir == "" || authDir == "" || configDir == authDir {
		return nil
	}

	authFiles := splitAuthFiles(authFilesValue)
	return b.syncSharedAuthStores(ctx, info.Config.Image, tool, authFiles, configDir, authDir, backend.DecodeSecretPlaceholders(containerEnvValue(info.Config, model.EnvSecretPlaceholders))...)
}

// syncSharedAuthStores reconciles auth files from the project-specific config
// store to the shared auth store via the auth-reconcile script, run in a helper
// container so it shares semantics with the session entrypoint. The config and
// auth store directories are bind-mounted into the helper. Most tools use
// conservative additive sync; Claude credentials use an expiresAt comparison to
// recover from atomic symlink replacement.
//
// For Claude this is now a fallback/migration safety net: with
// CLAUDE_SECURESTORAGE_CONFIG_DIR pointed at the shared auth store, Claude
// writes .credentials.json there directly, so the config-store source is a
// dangling symlink and this reconcile no-ops on the healthy path.
func (b *Backend) syncSharedAuthStores(ctx context.Context, helperImage string, tool string, authFiles []string, configDir string, authDir string, placeholders ...string) error {
	if configDir == "" || authDir == "" || configDir == authDir {
		return nil
	}
	validated, err := backend.ValidateAuthFilePaths(authFiles)
	if err != nil {
		return err
	}
	if len(validated) == 0 {
		return nil
	}
	if b.reconcileScriptErr != nil {
		return b.reconcileScriptErr
	}
	if strings.TrimSpace(b.reconcileScript) == "" {
		return fmt.Errorf("auth reconcile script is empty")
	}

	image := model.AlpineImage
	if tool == "claude" {
		image = strings.TrimSpace(helperImage)
		if image == "" {
			image = model.ImageName
		}
	}

	// Ensure the bind sources exist as the invoking user; otherwise Docker
	// would create them as root on first mount.
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return err
	}

	cmd := sharedAuthSyncCommand(b.reconcileScript, tool, validated, b.chownSpec(), "/config", "/auth", placeholders...)
	hostConfig := sharedAuthSyncHostConfig(configDir, authDir, util.IsSELinuxEnforcing())
	applyHostUserNamespace(hostConfig)

	return hoststore.WithLock(b.opts.Host.Home, authDir, func() error {
		var stderr bytes.Buffer
		err := dockercmd.RunWithIOAndStartHook(ctx, &dockercmd.ContainerConfig{
			Image:      image,
			Entrypoint: []string{"sh", "-c"},
			Cmd:        []string{cmd},
			User:       b.authSyncHelperUser(),
		}, hostConfig, "", nil, io.Discard, &stderr, false, nil)
		message := strings.TrimSpace(mapAuthSyncPaths(stderr.String(), configDir, authDir))
		if err != nil {
			if message != "" {
				return fmt.Errorf("%w: %s", err, message)
			}
			return err
		}
		for _, line := range strings.Split(message, "\n") {
			if warning, ok := strings.CutPrefix(line, "Warning: "); ok {
				logx.Warnf("%s", warning)
			}
		}
		return nil
	})
}

func mapAuthSyncPaths(message, configDir, authDir string) string {
	return authSyncMountPath.ReplaceAllStringFunc(message, func(match string) string {
		if strings.HasSuffix(match, "/config/") {
			return strings.TrimSuffix(match, "/config/") + configDir + string(os.PathSeparator)
		}
		return strings.TrimSuffix(match, "/auth/") + authDir + string(os.PathSeparator)
	})
}

// authSyncHelperUser is the user the reconcile helper runs as. Docker runs it
// as root; under rootless podman with keep-id, container root maps to a
// subordinate host UID, so anything it created would be undeletable for the
// host user. There the helper runs as the host user, which owns both stores.
func (b *Backend) authSyncHelperUser() string {
	if dockercmd.IsPodman() {
		return b.chownSpec()
	}
	return "root"
}

// sharedAuthSyncHostConfig builds the reconcile helper's host config: the
// config store read-only and the shared auth store writable. The reconcile
// script is deliberately not mounted; its content travels in the helper
// command. Bind mounts are relabeled the same way session-container mounts are
// when SELinux is enforcing; without the relabel the helper cannot read the
// stores ("Permission denied").
func sharedAuthSyncHostConfig(configDir string, authDir string, selinuxEnforcing bool) *dockercmd.HostConfig {
	hostConfig := &dockercmd.HostConfig{
		AutoRemove: true,
		Mounts: []dockercmd.Mount{
			{
				Type:     dockercmd.MountTypeBind,
				Source:   configDir,
				Target:   "/config",
				ReadOnly: true,
			},
			{
				Type:   dockercmd.MountTypeBind,
				Source: authDir,
				Target: "/auth",
			},
		},
	}
	applySELinuxMounts(hostConfig, selinuxEnforcing)
	return hostConfig
}

// sharedAuthSyncCommand assembles the helper container's shell command: the
// inlined reconcile script (a POSIX function library) followed by the
// enclave_sync_shared_auth invocation.
func sharedAuthSyncCommand(script string, tool string, authFiles []string, chown string, configRoot string, authRoot string, placeholders ...string) string {
	var cmd strings.Builder
	cmd.WriteString("set -e\n")
	fmt.Fprintf(&cmd, "%s=%s\n", model.EnvSecretPlaceholders, util.ShellQuote(backend.EncodeSecretPlaceholders(placeholders)))
	cmd.WriteString(script)
	cmd.WriteString("\nenclave_sync_shared_auth")
	for _, arg := range append([]string{tool, configRoot, authRoot, chown, "0"}, authFiles...) {
		fmt.Fprintf(&cmd, " %s", util.ShellQuote(arg))
	}
	cmd.WriteString("\n")
	return cmd.String()
}

// syncFeatureAuthFromContainer copies feature auth files from the running
// container's config store to the feature auth stores declared in its
// feature-auth map env var.
func (b *Backend) syncFeatureAuthFromContainer(ctx context.Context, containerName string) {
	name := strings.TrimSpace(containerName)
	if name == "" {
		return
	}
	info, err := dockercmd.ContainerInspect(ctx, name)
	if err != nil {
		if !dockercmd.IsNotFound(err) {
			logx.Debugf("feature auth sync: inspect container %s: %v", name, err)
		}
		return
	}
	if info.Config == nil || strings.TrimSpace(info.Config.Labels[model.LabelAgent]) == "" {
		return
	}
	featureMap := containerEnvValue(info.Config, model.EnvFeatureAuthMap)
	configDir := mountedSourceDir(info, containerEnvValue(info.Config, model.EnvToolConfigDir))
	if featureMap == "" || configDir == "" {
		return
	}
	placeholders := backend.DecodeSecretPlaceholders(containerEnvValue(info.Config, model.EnvSecretPlaceholders))
	for _, sync := range parseFeatureAuthMap(featureMap) {
		if err := b.syncFeatureAuthStore(ctx, configDir, sync, placeholders...); err != nil {
			logx.Warnf("Failed to sync %s feature auth files: %v", sync.Feature, err)
		}
	}
}

// syncFeatureAuthStore copies auth files from the config store directory into a
// feature auth store directory, additively: only when the source is a non-empty
// regular file and the destination is still empty. This is a plain host-side
// copy; no helper container is involved.
func (b *Backend) syncFeatureAuthStore(ctx context.Context, configDir string, sync backend.FeatureAuthSync, placeholders ...string) error {
	if configDir == "" || strings.TrimSpace(sync.Feature) == "" {
		return nil
	}
	validated, err := backend.ValidateAuthFilePaths(sync.AuthFiles)
	if err != nil {
		return err
	}
	if len(validated) == 0 {
		return nil
	}
	key := backend.StoreKey{Owner: sync.Feature}
	authDir, err := b.storage.storeDir(key, backend.StoreKindFeatureAuth)
	if err != nil {
		return err
	}
	return b.storage.WithStoreLock(ctx, key, backend.StoreKindFeatureAuth, func() error {
		if err := os.MkdirAll(authDir, 0o700); err != nil {
			return err
		}
		for _, authFile := range validated {
			src, err := storeFilePath(configDir, path.Join(sync.ConfigDir, authFile), true)
			if err != nil {
				return err
			}
			dst, err := storeFilePath(authDir, authFile, true)
			if err != nil {
				return err
			}
			if err := copyFeatureAuthFileIfMissing(src, dst, placeholders...); err != nil {
				return err
			}
		}
		return nil
	})
}

// copyFeatureAuthFileIfMissing copies src to dst only when src is a non-empty
// regular file and dst is absent or empty, preserving the additive feature-auth
// semantics (never overwrite an existing credential).
func copyFeatureAuthFileIfMissing(src string, dst string, placeholders ...string) error {
	si, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !si.Mode().IsRegular() || si.Size() == 0 {
		return nil
	}
	data, err := os.ReadFile(src) // #nosec G304 -- src is validated within the config store by storeFilePath.
	if err != nil {
		return err
	}
	if backend.ContainsSecretPlaceholder(data, placeholders) {
		logx.Warnf("Not syncing auth file %s: contains a gateway placeholder.", src)
		return nil
	}
	if di, err := os.Stat(dst); err == nil {
		if di.Size() > 0 {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600) // #nosec G703 -- dst is validated within the auth store by storeFilePath.
}

// parseFeatureAuthMap parses the feature-auth env map written by the runtime:
// "<feature>:<configDir>:<file1,file2>|<feature2>:...".
func parseFeatureAuthMap(value string) []backend.FeatureAuthSync {
	entries := strings.Split(value, "|")
	syncs := make([]backend.FeatureAuthSync, 0, len(entries))
	for _, entry := range entries {
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) != 3 {
			continue
		}
		feature := strings.TrimSpace(parts[0])
		files := splitAuthFiles(parts[2])
		if feature == "" || len(files) == 0 {
			continue
		}
		syncs = append(syncs, backend.FeatureAuthSync{
			Feature:   feature,
			ConfigDir: strings.TrimSpace(parts[1]),
			AuthFiles: files,
		})
	}
	return syncs
}

func containerEnvValue(config *dockercmd.ContainerConfig, key string) string {
	if config == nil || key == "" {
		return ""
	}
	prefix := key + "="
	for _, entry := range config.Env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

// mountedSourceDir resolves the host source directory of the bind mount whose
// container destination matches. Store mounts are bind mounts, so their host
// path is reported as the mount Source.
func mountedSourceDir(info dockercmd.InspectResponse, destination string) string {
	want := cleanContainerPath(destination)
	if want == "" {
		return ""
	}
	for _, mounted := range info.Mounts {
		if mounted.Type != dockercmd.MountTypeBind {
			continue
		}
		if cleanContainerPath(mounted.Destination) == want && strings.TrimSpace(mounted.Source) != "" {
			return strings.TrimSpace(mounted.Source)
		}
	}
	return ""
}

func cleanContainerPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return path.Clean(value)
}

func splitAuthFiles(value string) []string {
	parts := strings.Split(value, ",")
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			files = append(files, part)
		}
	}
	return files
}
