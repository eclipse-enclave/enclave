// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	"enclave/internal/gateway/oauthbroker"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/protectedauth"
	"enclave/internal/util"
)

const (
	authOwnerLabel       = "enclave.auth-owner"
	authIdentityLabel    = "enclave.auth-identity"
	authIncarnationLabel = "enclave.auth-incarnation"
	authProtectedLabel   = "enclave.auth-protected"
)

func (b *Backend) PrepareAuthSession(ctx context.Context, key backend.StoreKey, session string, protected, reset bool) (*backend.AuthSession, error) {
	releaseAdmission, err := hoststore.AcquireAuthMountAdmission(b.opts.Host.Home)
	if err != nil {
		return nil, err
	}
	defer releaseAdmission()
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err != nil {
		return nil, err
	}
	saved, err := hoststore.IdentityProtected(b.opts.Host.Home, dir)
	if err != nil {
		return nil, err
	}
	if saved && !protected {
		return nil, errors.New("saved auth identity is protected; rerun with --protect-auth or explicitly downgrade it while idle")
	}
	if _, known := protectedauth.Lookup(key.Owner); protected && !known {
		return nil, fmt.Errorf("protected auth does not support tool %s", key.Owner)
	}
	if protected {
		uid, uidErr := strconv.Atoi(b.opts.Host.UID)
		gid, gidErr := strconv.Atoi(b.opts.Host.GID)
		if uidErr != nil || gidErr != nil || uid <= 0 || gid < 0 {
			return nil, errors.New("protected OAuth requires a non-root invoking host user")
		}
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	key.Owner = strings.TrimSpace(key.Owner)
	key.Suffix = strings.TrimSpace(key.Suffix)
	ref := &backend.AuthSession{Key: key, Session: session, Engine: b.Name(), Incarnation: hex.EncodeToString(nonce[:]), Protected: protected}
	check := b.inspectAuth(ctx)
	if err := b.sweepAuthLeftovers(check); err != nil {
		logx.Warnf("Retaining stale auth session state: %v", err)
	}
	release, err := util.AcquireFileLock(b.authStartLock(ref.Incarnation), nil)
	if err != nil {
		return nil, err
	}
	b.authStarts.Store(ref.Incarnation, release)
	admitted, reserved := false, false
	defer func() {
		if admitted {
			return
		}
		b.releaseAuthStart(ref)
		if reserved {
			b.rollBackAuthAdmission(dir, ref)
		}
	}()
	err = hoststore.AdmitIdentity(b.opts.Host.Home, dir, authReservation(ref), reset,
		func(owners []hoststore.IdentityReservation) (bool, error) {
			return check.available(dir, owners, "", b.authRuntimeDir(ref))
		},
		check.reconcileOwner,
		func() error { return resetProtectedGrant(dir) })
	if err != nil {
		return nil, err
	}
	reserved = true
	if protected {
		for _, path := range []string{dir, b.authRuntimeDir(ref), b.authViewDir(ref)} {
			if err := hoststore.EnsureNoSymlinkChain(config.HostStateRootDir(b.opts.Host.Home), path, true); err != nil {
				return nil, err
			}
			if err := os.MkdirAll(path, 0o700); err != nil {
				return nil, err
			}
			if !util.PathOwnedBy(path, b.opts.Host.UID) {
				return nil, fmt.Errorf("protected auth directory %s is not owned by the host user", path)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm()&0o077 != 0 {
				return nil, errors.New("protected auth directories must have private permissions")
			}
		}
		ref.Logins = readyLogins(dir, key.Owner)
	}
	admitted = true
	return ref, nil
}

// readyLogins lists the tool's adapters whose grant in dir is ready.
func readyLogins(dir, tool string) []string {
	description, _ := protectedauth.Lookup(tool)
	var ready []string
	for _, adapter := range description.Adapters {
		data, err := os.ReadFile(filepath.Join(dir, protectedauth.GrantFile(adapter))) // #nosec G304 -- fixed grant name in the admitted identity directory.
		var grant struct {
			Status string `json:"status"`
		}
		if err == nil && json.Unmarshal(data, &grant) == nil && grant.Status == "ready" {
			ready = append(ready, adapter)
		}
	}
	return ready
}

// rollBackAuthAdmission releases a reservation whose preparation failed. This
// process created it and never started a container for it, so its end needs no
// engine verification. Callers hold mount admission.
func (b *Backend) rollBackAuthAdmission(dir string, ref *backend.AuthSession) {
	never := func(hoststore.IdentityReservation) (bool, error) { return true, nil }
	if err := hoststore.ReleaseIdentity(b.opts.Host.Home, dir, authReservation(ref), never); err != nil {
		logx.Warnf("Retaining auth session reservation after failed preparation: %v", err)
		return
	}
	if err := b.removeAuthSessionState(ref.Incarnation); err != nil {
		logx.Warnf("Retaining auth session state after failed preparation: %v", err)
	}
}

// removeAuthSessionState removes an ended incarnation's host session tree and
// start lock. Callers hold mount admission, under which every owner check
// runs, and have verified the owner ended: it never reacquires its start lock.
func (b *Backend) removeAuthSessionState(incarnation string) error {
	path := b.authSessionDir(&backend.AuthSession{Incarnation: incarnation})
	if err := hoststore.EnsureNoSymlinkChain(config.HostStateRootDir(b.opts.Host.Home), path, true); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return retireStartLock(b.authStartLock(incarnation))
}

func retireStartLock(path string) error {
	release, err := util.TryAcquireFileLock(path)
	if err != nil || release == nil {
		return err
	}
	defer release()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// removeEphemeralIdentity deletes a per-session protected identity whose
// sessions have ended. A container that can still mount it, such as a gateway
// awaiting removal, defers deletion to a later sweep. Callers hold mount
// admission.
func (b *Backend) removeEphemeralIdentity(check *authInspection, key backend.StoreKey) error {
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err != nil {
		return err
	}
	return hoststore.RemoveIdleIdentity(b.opts.Host.Home, dir, check.reconcileOwner, func() (bool, error) {
		return check.available(dir, nil, "")
	})
}

// sweepAuthLeftovers retires session trees and start locks that no identity
// reserves and no preparing owner holds, such as those left by a crash between
// releasing a reservation and removing its state, and per-session identities
// whose sessions ended. Callers hold mount admission, which serializes
// reservation changes and owner checks.
func (b *Backend) sweepAuthLeftovers(check *authInspection) error {
	home := b.opts.Host.Home
	if home == "" {
		return nil
	}
	tools, err := os.ReadDir(config.HostStoreAuthRootDir(home))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, tool := range tools {
		if _, known := protectedauth.Lookup(tool.Name()); !known || !tool.IsDir() {
			continue
		}
		identities, err := os.ReadDir(config.HostStoreAuthTreeDir(home, tool.Name()))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, identity := range identities {
			key := backend.StoreKey{Owner: tool.Name(), Suffix: identity.Name()}
			if !identity.IsDir() || !backend.IsProtectedEphemeralIdentity(key) {
				continue
			}
			if err := b.removeEphemeralIdentity(check, key); err != nil {
				logx.Debugf("Keeping ended session's protected identity for a later sweep: %v", err)
			}
		}
	}
	reserved, err := hoststore.ReservedIncarnations(home)
	if err != nil {
		return err
	}
	root := filepath.Join(config.HostStateRootDir(home), "oauth-sessions")
	if err := hoststore.EnsureNoSymlinkChain(config.HostStateRootDir(home), root, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		incarnation := entry.Name()
		if reserved[incarnation] || validateAuthSession(&backend.AuthSession{Session: "sweep", Engine: backend.NameDocker, Incarnation: incarnation}) != nil {
			continue
		}
		release, err := util.TryAcquireFileLock(b.authStartLock(incarnation))
		if err != nil {
			return err
		}
		if release == nil {
			continue
		}
		release()
		if err := b.removeAuthSessionState(incarnation); err != nil {
			return err
		}
	}
	reservedLocks := map[string]bool{}
	for incarnation := range reserved {
		reservedLocks[filepath.Base(b.authStartLock(incarnation))] = true
	}
	locks, err := os.ReadDir(config.HostLocksDir(home))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, lock := range locks {
		name := lock.Name()
		if !strings.HasPrefix(name, "auth-start-") || !strings.HasSuffix(name, ".lock") || reservedLocks[name] || !lock.Type().IsRegular() {
			continue
		}
		if err := retireStartLock(filepath.Join(config.HostLocksDir(home), name)); err != nil {
			return err
		}
	}
	return nil
}

func authReservation(ref *backend.AuthSession) hoststore.IdentityReservation {
	return hoststore.IdentityReservation{Engine: ref.Engine, Session: ref.Session, Incarnation: ref.Incarnation, Protected: ref.Protected}
}

func (b *Backend) authStartLock(incarnation string) string {
	return config.HostLockPath(b.opts.Host.Home, "auth-start-"+util.HashString(incarnation)+".lock")
}

func (b *Backend) authSessionDir(ref *backend.AuthSession) string {
	return filepath.Join(config.HostStateRootDir(b.opts.Host.Home), "oauth-sessions", ref.Incarnation)
}
func (b *Backend) authRuntimeDir(ref *backend.AuthSession) string {
	return filepath.Join(b.authSessionDir(ref), "runtime")
}
func (b *Backend) authViewDir(ref *backend.AuthSession) string {
	return filepath.Join(b.authSessionDir(ref), "view")
}

func (b *Backend) releaseAuthStart(ref *backend.AuthSession) {
	if ref == nil {
		return
	}
	if release, ok := b.authStarts.LoadAndDelete(ref.Incarnation); ok {
		release.(func())()
	}
}

func validateAuthSession(ref *backend.AuthSession) error {
	if ref == nil {
		return errors.New("auth session is required")
	}
	value, err := hex.DecodeString(ref.Incarnation)
	if err != nil || len(value) != 32 || ref.Session == "" {
		return errors.New("invalid auth session incarnation")
	}
	if ref.Engine != backend.NameDocker && ref.Engine != backend.NamePodman {
		return errors.New("invalid auth session engine")
	}
	return nil
}

func (b *Backend) validateAuthReservation(ref *backend.AuthSession) error {
	if err := validateAuthSession(ref); err != nil {
		return err
	}
	if err := b.recoverAuthSessionFeatures(ref); err != nil {
		return err
	}
	if ref.Key.Owner != "" {
		dir, err := hoststore.ResolveDir(b.opts.Host.Home, ref.Key, backend.StoreKindAuth)
		if err != nil {
			return err
		}
		if err := hoststore.RequireIdentityReservation(b.opts.Host.Home, dir, authReservation(ref)); err != nil {
			return err
		}
	}
	for _, name := range ref.Features {
		dir, err := hoststore.ResolveDir(b.opts.Host.Home, backend.StoreKey{Owner: name}, backend.StoreKindFeatureAuth)
		if err != nil {
			return err
		}
		if err := hoststore.RequireIdentityReservation(b.opts.Host.Home, dir, authReservation(ref)); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) ReleaseAuthSession(ctx context.Context, ref *backend.AuthSession) error {
	if ref == nil {
		return nil
	}
	if err := validateAuthSession(ref); err != nil {
		return err
	}
	releaseAdmission, err := hoststore.AcquireAuthMountAdmission(b.opts.Host.Home)
	if err != nil {
		return err
	}
	defer releaseAdmission()
	b.releaseAuthStart(ref)
	if err := b.recoverAuthSessionFeatures(ref); err != nil {
		return err
	}
	check := b.inspectAuth(ctx)
	ended, err := check.ownerEnded(authReservation(ref))
	if err != nil || !ended {
		return err
	}
	for _, name := range ref.Features {
		dir, err := hoststore.ResolveDir(b.opts.Host.Home, backend.StoreKey{Owner: name}, backend.StoreKindFeatureAuth)
		if err != nil {
			return err
		}
		if err := hoststore.ReleaseIdentity(b.opts.Host.Home, dir, authReservation(ref), check.ownerEnded); err != nil {
			return err
		}
	}
	if ref.Key.Owner != "" {
		dir, err := hoststore.ResolveDir(b.opts.Host.Home, ref.Key, backend.StoreKindAuth)
		if err != nil {
			return err
		}
		if err := hoststore.ReleaseIdentity(b.opts.Host.Home, dir, authReservation(ref), check.ownerEnded); err != nil {
			return err
		}
	}
	if err := b.removeAuthSessionState(ref.Incarnation); err != nil {
		return err
	}
	if ref.Protected && backend.IsProtectedEphemeralIdentity(ref.Key) {
		if err := b.removeEphemeralIdentity(check, ref.Key); err != nil {
			logx.Debugf("Keeping ended session's protected identity for a later sweep: %v", err)
		}
	}
	return nil
}

func (b *Backend) ResetAuthIdentity(ctx context.Context, key backend.StoreKey) error {
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err != nil {
		return err
	}
	if err := hoststore.ReconcileIdentityReservations(b.opts.Host.Home, dir, b.inspectAuth(ctx).reconcileOwner); err != nil {
		return err
	}
	return hoststore.WithIdleProtectedIdentity(b.opts.Host.Home, dir, func() (bool, error) { return b.identityUnused(ctx, dir) }, func() error { return resetProtectedGrant(dir) })
}

func (b *Backend) DowngradeAuthIdentity(ctx context.Context, key backend.StoreKey) error {
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, key, backend.StoreKindAuth)
	if err != nil {
		return err
	}
	if err := hoststore.ReconcileIdentityReservations(b.opts.Host.Home, dir, b.inspectAuth(ctx).reconcileOwner); err != nil {
		return err
	}
	return hoststore.DowngradeIdentity(b.opts.Host.Home, dir, func() (bool, error) { return b.identityUnused(ctx, dir) })
}

func resetProtectedGrant(dir string) error {
	return resetProtectedFiles(dir, "oauth-grant.json")
}

// Interrupted durableJSON writes can leave credential-bearing .oauth-* files.
// Remove only broker-owned names, without traversing symlinks or directories.
// The caller holds the idle identity lock through this durable reset.
func resetProtectedFiles(dir string, names ...string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !slices.Contains(names, entry.Name()) && !strings.HasPrefix(entry.Name(), ".oauth-") {
			continue
		}
		if entry.IsDir() {
			return errors.New("protected grant file is a directory")
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return util.SyncDir(dir)
}

// engineContainers is independent of the process-wide selected engine: identity
// admission must inspect both engines without switching another operation's CLI.
var engineContainers = inspectAuthEngineContainers

// A snapshot is scoped to one mount-admission critical section. Workload starts
// cannot race it; never retain it across releasing that lock.
type authInspection struct {
	backend *Backend
	ctx     context.Context
	engines map[string]authEngineResult
}

type authEngineResult struct {
	containers []dockercmd.InspectResponse
	err        error
}

func (b *Backend) inspectAuth(ctx context.Context) *authInspection {
	return &authInspection{backend: b, ctx: ctx, engines: map[string]authEngineResult{}}
}

func (check *authInspection) containers(engine string) ([]dockercmd.InspectResponse, error) {
	if result, ok := check.engines[engine]; ok {
		return result.containers, result.err
	}
	containers, err := engineContainers(check.ctx, engine)
	check.engines[engine] = authEngineResult{containers: containers, err: err}
	return containers, err
}

func inspectAuthEngineContainers(ctx context.Context, engine string) ([]dockercmd.InspectResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	binary, err := exec.LookPath(engine)
	if err != nil {
		return nil, err
	}
	list := func() ([]string, error) {
		output, err := exec.CommandContext(ctx, binary, "ps", "--all", "--quiet", "--no-trunc").Output() // #nosec G204 -- fixed engine and flags.
		if err != nil {
			return nil, fmt.Errorf("cannot verify auth sessions on %s: %w", engine, err)
		}
		return strings.Fields(string(output)), nil
	}
	ids, err := list()
	if err != nil {
		return nil, err
	}
	const maxAttempts = 3
	for range maxAttempts {
		if len(ids) == 0 {
			return nil, nil
		}
		args := append([]string{"container", "inspect"}, ids...)
		output, err := exec.CommandContext(ctx, binary, args...).Output() // #nosec G204 -- engine-returned IDs, no shell.
		if err == nil {
			return decodeAuthEngineContainers(engine, ids, output)
		}
		// Bulk inspect fails when any auto-removed container disappears.
		// Re-list to prove disappearance, not by matching an error string.
		next, listErr := list()
		if listErr != nil {
			return nil, listErr
		}
		vanished := slices.ContainsFunc(ids, func(id string) bool { return !slices.Contains(next, id) })
		if !vanished {
			return nil, fmt.Errorf("cannot inspect auth sessions on %s: %w", engine, err)
		}
		ids = next
	}
	return nil, fmt.Errorf("containers on %s kept disappearing during auth inspection; retry the operation", engine)
}

func decodeAuthEngineContainers(engine string, ids []string, output []byte) ([]dockercmd.InspectResponse, error) {
	var containers []dockercmd.InspectResponse
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	if err := decoder.Decode(&containers); err != nil {
		return nil, fmt.Errorf("decode %s auth session inspection: %w", engine, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("unexpected auth session inspection data")
	}
	if len(containers) != len(ids) {
		return nil, errors.New("incomplete auth session inspection")
	}
	seen := map[string]bool{}
	for _, info := range containers {
		if !slices.Contains(ids, info.ID) || seen[info.ID] || info.Name == "" || info.Config == nil || info.State == nil || info.State.Status == "" {
			return nil, errors.New("incomplete auth session inspection")
		}
		seen[info.ID] = true
	}
	return containers, nil
}

func (check *authInspection) ownerEnded(owner hoststore.IdentityReservation) (bool, error) {
	b := check.backend
	release, _, err := util.TryAcquireFileLock(b.authStartLock(owner.Incarnation))
	if err != nil {
		return false, err
	}
	if release == nil {
		return false, nil
	}
	defer release()
	containers, err := check.containers(owner.Engine)
	if err != nil {
		return false, fmt.Errorf("cannot verify %s auth session %q ended; restore engine access before retrying: %w", owner.Engine, owner.Session, err)
	}
	for _, info := range containers {
		name := strings.TrimPrefix(info.Name, "/")
		if name != owner.Session && name != gateway.ContainerName(owner.Session) {
			continue
		}
		if info.State == nil {
			return false, errors.New("cannot determine auth owner container state")
		}
		if info.State.Status != "exited" && info.State.Status != "dead" {
			return false, nil
		}
	}
	return true, nil
}

func (check *authInspection) reconcileOwner(owner hoststore.IdentityReservation) (bool, error) {
	b := check.backend
	ended, err := check.ownerEnded(owner)
	if err != nil || !ended {
		return ended, err
	}
	ref := &backend.AuthSession{Session: owner.Session, Engine: owner.Engine, Incarnation: owner.Incarnation, Protected: true}
	if err := validateAuthSession(ref); err != nil {
		return false, err
	}
	if err := b.removeAuthSessionState(owner.Incarnation); err != nil {
		return false, err
	}
	return true, nil
}

func (b *Backend) identityUnused(ctx context.Context, dir string, extraPaths ...string) (bool, error) {
	return b.inspectAuth(ctx).available(dir, nil, "", extraPaths...)
}

// Only admitted, current-version gateways may share exact grant/lock binds.
// Raw workload binds, parents, and unreserved or older gateways still conflict.
func (check *authInspection) available(dir string, owners []hoststore.IdentityReservation, feature string, extraPaths ...string) (bool, error) {
	b := check.backend
	if err := hoststore.RequireNoQEMUSessions(check.ctx, b.opts.Host.Home); err != nil {
		return false, err
	}
	paths := append([]string{dir, hoststore.LockPath(b.opts.Host.Home, dir), hoststore.IdentityStatePath(b.opts.Host.Home, dir)}, extraPaths...)
	for i, path := range paths {
		canonical, err := canonicalAuthPath(path)
		if err != nil {
			return false, err
		}
		paths[i] = canonical
	}
	for _, engine := range []string{backend.NameDocker, backend.NamePodman} {
		containers, err := check.containers(engine)
		if errors.Is(err, exec.ErrNotFound) && engine != b.Name() {
			continue
		}
		if err != nil {
			return false, err
		}
		for _, info := range containers {
			shared := b.registeredAuthGateway(info, engine, owners, dir, feature)
			grantTarget, lockTarget := model.GatewayOAuthGrantPath, model.GatewayOAuthLockPath
			if feature == "github-cli" {
				grantTarget, lockTarget = model.GatewayOAuthGitHubGrantPath, model.GatewayOAuthGitHubLockPath
			}
			for _, mount := range info.Mounts {
				if mount.Type != dockercmd.MountTypeBind {
					continue
				}
				source, err := canonicalAuthPath(mount.Source)
				if err != nil {
					return false, fmt.Errorf("cannot verify %s container %q bind source %q: %w", engine, info.Name, mount.Source, err)
				}
				if shared && ((source == paths[0] && mount.Destination == grantTarget) || (source == paths[1] && mount.Destination == lockTarget)) {
					continue
				}
				for _, trusted := range paths {
					if pathsOverlap(source, trusted) {
						// Stopped containers can restart with their existing binds.
						return false, nil
					}
				}
			}
		}
	}
	return true, nil
}

func (b *Backend) registeredAuthGateway(info dockercmd.InspectResponse, engine string, owners []hoststore.IdentityReservation, dir, feature string) bool {
	if info.Config == nil {
		return false
	}
	for _, owner := range owners {
		if !owner.Protected || owner.Engine != engine || strings.TrimPrefix(info.Name, "/") != gateway.ContainerName(owner.Session) {
			continue
		}
		env := map[string]string{}
		for _, assignment := range info.Config.Env {
			key, value, _ := strings.Cut(assignment, "=")
			if _, duplicate := env[key]; duplicate {
				return false
			}
			env[key] = value
		}
		if _, known := protectedauth.Lookup(env[model.EnvOAuthBroker]); !known || env[model.EnvOAuthIncarnation] != owner.Incarnation {
			return false
		}
		ref := &backend.AuthSession{Incarnation: owner.Incarnation}
		runtime := b.authRuntimeDir(ref)
		if err := hoststore.EnsureNoSymlinkChain(config.HostStateRootDir(b.opts.Host.Home), runtime, false); err != nil {
			return false
		}
		expected := oauthbroker.Config{Tool: env[model.EnvOAuthBroker], RuntimeDir: runtime, Identity: env[model.EnvOAuthIdentity], Session: owner.Session, Incarnation: owner.Incarnation}
		if env[model.EnvOAuthGitHub] == "1" {
			expected.GitHub = &oauthbroker.GitHubConfig{Identity: env[model.EnvOAuthGitHubIdentity]}
		}
		if feature == "" {
			if expected.Identity != util.HashString(dir) {
				return false
			}
		} else if feature != "github-cli" || expected.GitHub == nil || expected.GitHub.Identity != util.HashString(dir) || !hasAuthRuntimeMount(info, b.featureRuntimeDir(ref, feature), model.GatewayOAuthGitHubRuntimePath) {
			return false
		}
		// Private readiness binds the inspected gateway to this incarnation and
		// fences older brokers that do not support concurrent grant recovery.
		return hasAuthRuntimeMount(info, runtime, model.GatewayOAuthRuntimePath) && oauthbroker.VerifyReadiness(expected) == nil
	}
	return false
}

func hasAuthRuntimeMount(info dockercmd.InspectResponse, source, target string) bool {
	want, err := canonicalAuthPath(source)
	if err != nil {
		return false
	}
	matches := 0
	for _, mount := range info.Mounts {
		if mount.Destination != target {
			continue
		}
		actual, err := canonicalAuthPath(mount.Source)
		if err != nil || mount.Type != dockercmd.MountTypeBind || actual != want {
			return false
		}
		matches++
	}
	return matches == 1
}

func authSessionFromInspect(info dockercmd.InspectResponse, engine string) *backend.AuthSession {
	if info.Config == nil {
		return nil
	}
	labels := info.Config.Labels
	if labels[authIncarnationLabel] == "" {
		return nil
	}
	return &backend.AuthSession{Key: backend.StoreKey{Owner: labels[authOwnerLabel], Suffix: labels[authIdentityLabel]}, Session: strings.TrimPrefix(info.Name, "/"), Engine: engine, Incarnation: labels[authIncarnationLabel], Protected: labels[authProtectedLabel] == "true"}
}

func addAuthSessionLabels(labels map[string]string, ref *backend.AuthSession) {
	if ref == nil {
		return
	}
	labels[authOwnerLabel] = ref.Key.Owner
	labels[authIdentityLabel] = ref.Key.Suffix
	labels[authIncarnationLabel] = ref.Incarnation
	if ref.Protected {
		labels[authProtectedLabel] = "true"
	}
}

func (b *Backend) protectedGatewayConfig(ref *backend.AuthSession) (*gateway.OAuthConfig, error) {
	if ref == nil || !ref.Protected {
		return nil, nil
	}
	if err := b.validateAuthReservation(ref); err != nil {
		return nil, err
	}
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, ref.Key, backend.StoreKindAuth)
	if err != nil {
		return nil, err
	}
	cfg := &gateway.OAuthConfig{StoreDir: dir, RuntimeDir: b.authRuntimeDir(ref), ViewDir: b.authViewDir(ref), LockPath: hoststore.LockPath(b.opts.Host.Home, dir), Identity: util.HashString(dir), Incarnation: ref.Incarnation}
	for _, name := range ref.Features {
		feature, err := b.protectedFeatureGatewayConfig(ref, name)
		if err != nil {
			return nil, err
		}
		cfg.Features = append(cfg.Features, feature)
	}
	return cfg, nil
}

func (b *Backend) validateProtectedMounts(req backend.Request, spec runSpec) error {
	if err := b.validateWorkloadAuthMounts(req, spec); err != nil {
		return err
	}
	ref := req.AuthSession
	if ref == nil || !ref.Protected {
		return nil
	}
	cfg, err := b.protectedGatewayConfig(ref)
	if err != nil {
		return err
	}
	if err := rejectNativeAuthSource(cfg.ViewDir); err != nil {
		return err
	}
	for _, store := range req.Stores {
		if store.Kind == backend.StoreKindFeatureAuth {
			if !slices.Contains(ref.Features, store.Key.Owner) || store.ContainerPath != "/home/agent/.config/gh" || store.Key != (backend.StoreKey{Owner: "github-cli"}) {
				return errors.New("protected feature store does not match admitted native view")
			}
		}
		if (store.Kind == backend.StoreKindConfig && store.Key != ref.ConfigKey) || (store.Kind == backend.StoreKindAuth && store.Key != ref.Key) {
			return errors.New("protected session references another config or auth identity")
		}
	}
	if req.AuthSync != nil && len(req.AuthSync.Features) != 0 {
		return errors.New("protected OAuth does not support native feature auth synchronization")
	}
	for _, entry := range req.Env {
		if entry.Name == model.EnvFeatureAuthMap {
			return errors.New("protected OAuth does not support native feature auth mappings")
		}
	}
	if err := b.validateProtectedFeatureMounts(req, spec); err != nil {
		return err
	}
	uid, uidErr := strconv.Atoi(b.opts.Host.UID)
	gid, gidErr := strconv.Atoi(b.opts.Host.GID)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid < 0 || req.User != b.opts.Host.UID+":"+b.opts.Host.GID {
		return errors.New("protected OAuth workload must explicitly run as the non-root host UID and GID")
	}
	if len(b.opts.DevcontainerRunArgs) != 0 || req.Security.Admin || req.RuntimeUIDRemap {
		return errors.New("protected OAuth requires an unprivileged workload without runtime UID remapping or devcontainer runtime arguments")
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	return util.PathWithin(first, second) || util.PathWithin(second, first)
}

// The caller holds the cross-engine admission lock until container start.
func (b *Backend) validateWorkloadAuthMounts(req backend.Request, spec runSpec) error {
	var views []string
	if req.AuthSession != nil && req.AuthSession.Protected {
		if err := b.validateAuthReservation(req.AuthSession); err != nil {
			return err
		}
		view, err := canonicalAuthPath(b.authViewDir(req.AuthSession))
		if err != nil {
			return err
		}
		views = append(views, view)
		for _, name := range req.AuthSession.Features {
			view, err := canonicalAuthPath(b.featureViewDir(req.AuthSession, name))
			if err != nil {
				return err
			}
			views = append(views, view)
		}
	}

	var sources []string
	for _, mount := range spec.hostConfig.Mounts {
		if mount.Type == dockercmd.MountTypeBind {
			sources = append(sources, mount.Source)
		}
	}
	checked, err := hoststore.ValidateAuthMounts(b.opts.Host.Home, sources, views)
	if err != nil {
		return err
	}
	next := 0
	for i := range spec.hostConfig.Mounts {
		if spec.hostConfig.Mounts[i].Type == dockercmd.MountTypeBind {
			spec.hostConfig.Mounts[i].Source = checked[next]
			next++
		}
	}
	return nil
}

func rejectNativeAuthSource(dir string) error {
	if dir == "" {
		return nil
	}
	for _, rel := range []string{".credentials.json", "auth.json", ".claude.json", ".config.json", "config.json"} {
		if _, err := os.Lstat(filepath.Join(dir, rel)); err == nil {
			return fmt.Errorf("protected OAuth refuses native auth source %s", rel)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func canonicalAuthPath(path string) (string, error) {
	return hoststore.CanonicalAuthPath(path)
}
