// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"enclave/internal/util"
)

const githubGrantFile = "github-grant.json"
const githubRegistryFile = "github-session.json"
const githubHandlePrefix = "gho_enclave_"
const githubMaxAccounts = 16

// GitHubConfig identifies trusted, separately mounted feature storage. Only
// ViewDir is exposed to the workload. Session admission is the host's job.
type GitHubConfig struct {
	StoreDir, RuntimeDir, ViewDir, LockPath string
	Identity, Session, Incarnation          string
}

type githubAccount struct {
	Token string `json:"token"` // #nosec G117 -- canonical provider credential lives only in the gateway-private grant.
	Scope string `json:"scope"`
}

type githubGrant struct {
	Schema   int                      `json:"schema"`
	Identity string                   `json:"identity"`
	Active   string                   `json:"active"`
	Accounts map[string]githubAccount `json:"accounts"`
	Config   map[string]any           `json:"config"`
	Devices  map[string]githubDevice  `json:"devices"`
}

type githubDevice struct {
	Session     string `json:"session"`
	Incarnation string `json:"incarnation"`
	User        string `json:"user"`
}

type githubRegistry struct {
	Schema          int               `json:"schema"`
	Identity        string            `json:"identity"`
	Session         string            `json:"session"`
	Incarnation     string            `json:"incarnation"`
	Handles         map[string]string `json:"handles"`
	ViewInitialized bool              `json:"viewInitialized"`
}

type githubBroker struct {
	config   GitHubConfig
	registry githubRegistry
	mu       sync.Mutex
	write    func(string, any) error
	failed   bool
}

func openGitHub(config GitHubConfig) (*githubBroker, error) {
	if config.Identity == "" || config.Session == "" || config.Incarnation == "" || !filepath.IsAbs(config.LockPath) {
		return nil, ErrState
	}
	info, err := os.Lstat(config.LockPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrState
	}
	dirs := []string{config.StoreDir, config.RuntimeDir, config.ViewDir}
	for i, dir := range dirs {
		if !filepath.IsAbs(dir) || privateDirectory(dir) != nil {
			return nil, ErrState
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, ErrState
		}
		dirs[i] = resolved
	}
	for i, a := range dirs {
		for j, other := range dirs {
			if i != j && within(a, other) {
				return nil, ErrState
			}
		}
	}
	lock, err := filepath.EvalSymlinks(config.LockPath)
	if err != nil || within(dirs[2], lock) {
		return nil, ErrState
	}
	b := &githubBroker{config: config, write: durableJSON}
	err = b.locked(func() error {
		g, err := b.readGrant()
		if errors.Is(err, os.ErrNotExist) {
			g, err = importGitHub(config)
		}
		if err != nil {
			return err
		}
		path := filepath.Join(config.RuntimeDir, githubRegistryFile)
		if err := readPrivateJSON(path, &b.registry); errors.Is(err, os.ErrNotExist) {
			b.registry = githubRegistry{Schema: 1, Identity: config.Identity, Session: config.Session, Incarnation: config.Incarnation, Handles: map[string]string{}}
		} else if err != nil {
			return ErrState
		}
		r := b.registry
		if r.Schema != 1 || r.Identity != config.Identity || r.Session != config.Session || r.Incarnation != config.Incarnation || r.Handles == nil || len(r.Handles) > githubMaxAccounts {
			return ErrState
		}
		seen := map[string]bool{}
		for user, handle := range r.Handles {
			if !githubLogin(user) || !validHandle(handle, githubHandlePrefix) || seen[handle] {
				return ErrState
			}
			seen[handle] = true
		}
		for user := range g.Accounts {
			if err := b.ensureHandle(user); err != nil {
				return err
			}
		}
		// Registration precedes grant durability, including recovery after a
		// previous process failed a directory fsync after a visible rename.
		if err := b.saveRegistry(); err != nil {
			return err
		}
		return b.saveGrant(g)
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (b *githubBroker) locked(fn func() error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed {
		return ErrState
	}
	return util.WithFileLock(b.config.LockPath, fn)
}

func (b *githubBroker) readGrant() (*githubGrant, error) {
	var g githubGrant
	if err := readPrivateJSON(filepath.Join(b.config.StoreDir, githubGrantFile), &g); err != nil {
		return nil, err
	}
	if g.Schema != 1 || g.Identity != b.config.Identity || g.Accounts == nil || len(g.Accounts) > githubMaxAccounts || validateGitHubPreferences(g.Config) != nil {
		return nil, ErrState
	}
	for user, account := range g.Accounts {
		if !githubLogin(user) || !githubToken(account.Token) || !githubScopes(account.Scope, ",") {
			return nil, ErrState
		}
	}
	if (len(g.Accounts) > 0 && g.Accounts[g.Active].Token == "") || (len(g.Accounts) == 0 && g.Active != "") {
		return nil, ErrState
	}
	if g.Devices == nil || len(g.Devices) > 64 {
		return nil, ErrState
	}
	for hash, device := range g.Devices {
		if !validHandle(hash, "") || device.Session == "" || device.Incarnation == "" || (device.User != "" && g.Accounts[device.User].Token == "") {
			return nil, ErrState
		}
	}
	// A sibling gateway may have lost the acknowledgement after rename.
	// Readers must fence shared commits even without a proxy reload.
	if err := util.SyncDir(b.config.StoreDir); err != nil {
		return nil, ErrState
	}
	return &g, nil
}

func (b *githubBroker) save(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > maxDocument {
		b.failed = true
		return ErrState
	}
	if err := b.write(path, value); err != nil {
		b.failed = true
		return ErrState
	}
	return nil
}

func (b *githubBroker) saveRegistry() error {
	return b.save(filepath.Join(b.config.RuntimeDir, githubRegistryFile), b.registry)
}

func (b *githubBroker) saveGrant(g *githubGrant) error {
	return b.save(filepath.Join(b.config.StoreDir, githubGrantFile), g)
}

func (b *githubBroker) ensureHandle(user string) error {
	if b.registry.Handles[user] != "" {
		return nil
	}
	if len(b.registry.Handles) >= githubMaxAccounts {
		return ErrState
	}
	handle, err := randomHandle(githubHandlePrefix)
	if err != nil {
		return err
	}
	for _, existing := range b.registry.Handles {
		if existing == handle {
			return ErrState
		}
	}
	b.registry.Handles[user] = handle
	return nil
}
