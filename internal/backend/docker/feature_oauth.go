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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	"enclave/internal/gateway"
	"enclave/internal/model"
	"enclave/internal/util"
)

func (b *Backend) PrepareFeatureAuthSessions(ctx context.Context, parent *backend.AuthSession, session string, names []string, protected bool) (*backend.AuthSession, error) {
	if len(names) == 0 {
		return parent, nil
	}
	release, err := hoststore.AcquireAuthMountAdmission(b.opts.Host.Home)
	if err != nil {
		return parent, err
	}
	defer release()
	ref := parent
	if ref == nil {
		if protected {
			return nil, errors.New("protected feature authentication requires a protected tool session")
		}
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		ref = &backend.AuthSession{Session: session, Engine: b.Name(), Incarnation: hex.EncodeToString(nonce[:])}
		startRelease, err := util.AcquireFileLock(b.authStartLock(ref.Incarnation), nil)
		if err != nil {
			return nil, err
		}
		b.authStarts.Store(ref.Incarnation, startRelease)
	} else if ref.Protected != protected || ref.Session != session || ref.Engine != b.Name() {
		return ref, errors.New("feature auth session does not match tool admission")
	}
	if err := b.validateAuthReservation(ref); err != nil {
		return ref, err
	}
	names = slices.Clone(names)
	slices.Sort(names)
	names = slices.Compact(names)
	for _, name := range names {
		if protected && name != "github-cli" {
			return ref, fmt.Errorf("protected native authentication is unsupported for feature %s", name)
		}
		if _, err := hoststore.ResolveDir(b.opts.Host.Home, backend.StoreKey{Owner: name}, backend.StoreKindFeatureAuth); err != nil {
			return ref, err
		}
	}
	ref.Features = names
	// Record intent before reserving any feature so a partial admission is
	// recoverable without container labels carrying trusted feature state.
	if err := b.writeAuthSessionRecord(ref); err != nil {
		return ref, err
	}
	check := b.inspectAuth(ctx)
	for _, name := range names {
		dir, err := hoststore.ResolveDir(b.opts.Host.Home, backend.StoreKey{Owner: name}, backend.StoreKindFeatureAuth)
		if err != nil {
			return ref, err
		}
		if err := hoststore.AdmitNativeIdentity(b.opts.Host.Home, dir, authReservation(ref),
			func(owners []hoststore.IdentityReservation) (bool, error) {
				return check.available(dir, owners, name, b.featureRuntimeDir(ref, name))
			},
			check.reconcileOwner); err != nil {
			return ref, fmt.Errorf("admit %s feature authentication: %w", name, err)
		}
		if protected {
			for _, path := range []string{dir, b.featureViewDir(ref, name), b.featureRuntimeDir(ref, name)} {
				if err := b.ensureAuthSessionDirectory(path); err != nil {
					return ref, err
				}
			}
		}
	}
	return ref, nil
}

func (b *Backend) featureViewDir(ref *backend.AuthSession, name string) string {
	return filepath.Join(b.authSessionDir(ref), "features", name, "view")
}

func (b *Backend) featureRuntimeDir(ref *backend.AuthSession, name string) string {
	return filepath.Join(b.authSessionDir(ref), "features", name, "runtime")
}

func (b *Backend) ensureAuthSessionDirectory(dir string) error {
	if err := hoststore.EnsureNoSymlinkChain(config.HostStateRootDir(b.opts.Host.Home), dir, true); err != nil {
		return err
	}
	var missing []string
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Stat(current); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !util.PathOwnedBy(dir, b.opts.Host.UID) || info.Mode().Perm()&0o077 != 0 {
		return errors.New("auth session directories must be private and owned by the host user")
	}
	for _, created := range missing {
		if err := util.SyncDir(filepath.Dir(created)); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) writeAuthSessionRecord(ref *backend.AuthSession) error {
	if err := b.ensureAuthSessionDirectory(b.authSessionDir(ref)); err != nil {
		return err
	}
	data, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	if err := util.WriteFileAtomic(filepath.Join(b.authSessionDir(ref), "session.json"), data, 0o600); err != nil {
		return err
	}
	dir, err := os.Open(b.authSessionDir(ref)) // #nosec G304 -- private host session directory checked above.
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func (b *Backend) recoverAuthSessionFeatures(ref *backend.AuthSession) error {
	path := filepath.Join(b.authSessionDir(ref), "session.json")
	if err := hoststore.EnsureNoSymlinkChain(config.HostStateRootDir(b.opts.Host.Home), path, true); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 65536 || !util.PathOwnedBy(path, b.opts.Host.UID) {
		return errors.New("invalid host auth session record")
	}
	for _, dir := range []string{b.authSessionDir(ref), filepath.Dir(b.authSessionDir(ref))} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !util.PathOwnedBy(dir, b.opts.Host.UID) {
			return errors.New("host auth session record has an untrusted parent")
		}
	}
	data, err := os.ReadFile(path) // #nosec G304 -- private, bounded, symlink-checked host-only session record.
	if err != nil {
		return err
	}
	var saved backend.AuthSession
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return errors.New("invalid host auth session record")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || saved.Key != ref.Key || saved.Session != ref.Session || saved.Engine != ref.Engine || saved.Incarnation != ref.Incarnation || saved.Protected != ref.Protected {
		return errors.New("host auth session record does not match session")
	}
	if len(ref.Features) != 0 && !slices.Equal(ref.Features, saved.Features) {
		return errors.New("feature auth admission differs from host session record")
	}
	for _, name := range saved.Features {
		if _, err := hoststore.ResolveDir(b.opts.Host.Home, backend.StoreKey{Owner: name}, backend.StoreKindFeatureAuth); err != nil {
			return err
		}
		if saved.Protected && name != "github-cli" {
			return errors.New("unsupported protected feature in host session record")
		}
	}
	ref.Features = slices.Clone(saved.Features)
	return nil
}

func (b *Backend) protectedFeatureGatewayConfig(ref *backend.AuthSession, name string) (gateway.FeatureOAuthConfig, error) {
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, backend.StoreKey{Owner: name}, backend.StoreKindFeatureAuth)
	if err != nil {
		return gateway.FeatureOAuthConfig{}, err
	}
	return gateway.FeatureOAuthConfig{Name: name, StoreDir: dir, RuntimeDir: b.featureRuntimeDir(ref, name), ViewDir: b.featureViewDir(ref, name), LockPath: hoststore.LockPath(b.opts.Host.Home, dir), Identity: util.HashString(dir)}, nil
}

func (b *Backend) ResetFeatureAuthIdentity(ctx context.Context, name string) error {
	dir, err := b.idleFeatureIdentity(ctx, name)
	if err != nil {
		return err
	}
	return hoststore.WithIdleProtectedIdentity(b.opts.Host.Home, dir, func() (bool, error) { return b.identityUnused(ctx, dir) }, func() error {
		return resetProtectedFiles(dir, "hosts.yml", "config.yml", "github-grant.json")
	})
}

func (b *Backend) DowngradeFeatureAuthIdentity(ctx context.Context, name string) error {
	dir, err := b.idleFeatureIdentity(ctx, name)
	if err != nil {
		return err
	}
	return hoststore.DowngradeIdentity(b.opts.Host.Home, dir, func() (bool, error) { return b.identityUnused(ctx, dir) })
}

func (b *Backend) idleFeatureIdentity(ctx context.Context, name string) (string, error) {
	if name != "github-cli" {
		return "", errors.New("protected native authentication supports github-cli only")
	}
	dir, err := hoststore.ResolveDir(b.opts.Host.Home, backend.StoreKey{Owner: name}, backend.StoreKindFeatureAuth)
	if err != nil {
		return "", err
	}
	if err := hoststore.ReconcileIdentityReservations(b.opts.Host.Home, dir, b.inspectAuth(ctx).reconcileOwner); err != nil {
		return "", err
	}
	return dir, nil
}

func (b *Backend) validateProtectedFeatureMounts(req backend.Request, spec runSpec) error {
	if !slices.Contains(req.AuthSession.Features, "github-cli") {
		return nil
	}
	target := "/home/agent/.config/gh"
	view, err := canonicalAuthPath(b.featureViewDir(req.AuthSession, "github-cli"))
	if err != nil {
		return err
	}
	mounted := false
	for _, mount := range spec.hostConfig.Mounts {
		if !pathsOverlap(path.Clean(mount.Target), target) {
			continue
		}
		if mounted || mount.Type != "bind" || mount.Source != view || path.Clean(mount.Target) != target {
			return errors.New("protected GitHub config mount overlaps another workload mount")
		}
		mounted = true
	}
	if !mounted {
		return errors.New("protected GitHub feature requires its native handle view")
	}
	required := map[string]string{"GH_CONFIG_DIR": target, "GH_HOST": "github.com", model.EnvProtectedGitHub: "1"}
	for _, assignment := range req.Env {
		if want, ok := required[assignment.Name]; ok && assignment.Value == want {
			delete(required, assignment.Name)
		}
		switch assignment.Name {
		case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN":
			if assignment.Value != "" {
				return fmt.Errorf("protected GitHub conflicts with environment setting %s", assignment.Name)
			}
		case "GH_HOST":
			if assignment.Value != "github.com" {
				return errors.New("protected GitHub supports github.com only")
			}
		case "GH_CONFIG_DIR":
			if assignment.Value != target {
				return errors.New("protected GitHub requires its native config view")
			}
		case model.EnvProtectedGitHub:
			if assignment.Value != "1" {
				return errors.New("protected GitHub launcher cannot be disabled")
			}
		}
	}
	if len(required) != 0 {
		return errors.New("protected GitHub requires pinned launcher and native host/config bindings")
	}
	for _, file := range []string{"hosts.yml", "config.yml"} {
		if _, err := os.Lstat(filepath.Join(view, file)); err == nil || !errors.Is(err, os.ErrNotExist) {
			return errors.New("protected GitHub requires a fresh native handle view")
		}
	}
	return nil
}
