// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package oauthbroker implements the experimental Claude OAuth custody mode.
// Real-client and container compatibility still require manual verification.
package oauthbroker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"enclave/internal/protectedauth"
	"enclave/internal/util"
)

const grantFile = "oauth-grant.json"
const registryFile = "oauth-session.json"
const accessHandlePrefix = "sk-ant-oat01-"
const refreshHandlePrefix = "sk-ant-ort01-"

var (
	ErrDenied    = errors.New("protected OAuth request denied")
	ErrState     = errors.New("protected OAuth state unavailable")
	ErrUncertain = errors.New("protected OAuth outcome uncertain; reauthentication required")
	ErrResponse  = errors.New("unsupported protected OAuth response")
)

// Config is trusted runtime input, never supplied by workload HTTP requests.
// StoreDir and RuntimeDir must be separate private directories, neither mounted
// in the workload. LockPath must name the existing hoststore lock object, explicitly
// mounted into the gateway; deriving it from a container path is not safe.
// The caller owns admission, UID mapping, reset, and session-end cleanup.
type Config struct {
	GitHub *GitHubConfig
	// Tool selects the logins the gateway captures and the native view
	// format; see internal/protectedauth.
	Tool        string
	StoreDir    string
	RuntimeDir  string
	LockPath    string
	Identity    string
	Session     string
	Incarnation string
}

type registry struct {
	Schema          int    `json:"schema"`
	Identity        string `json:"identity"`
	Session         string `json:"session"`
	Incarnation     string `json:"incarnation"`
	Access          string `json:"access"`
	Refresh         string `json:"refresh"`
	ViewInitialized bool   `json:"viewInitialized,omitempty"`
}

type grant struct {
	Schema      int           `json:"schema"`
	Identity    string        `json:"identity"`
	Issuer      string        `json:"issuer"`
	Generation  uint64        `json:"generation"`
	Status      string        `json:"status"`
	CommittedAt time.Time     `json:"committedAt"`
	ExpiresAt   int64         `json:"expiresAt"`
	Token       tokenResponse `json:"token"`
}

// adapter captures one provider's logins and releases its handles. It
// reports whether it handled the request.
type adapter interface {
	roundTrip(req *http.Request, transport, exchange http.RoundTripper) (*http.Response, bool, error)
}

type Broker struct {
	github   *githubBroker
	adapters []adapter
	// anthropic enables Claude subscription capture and release.
	anthropic bool
	// openai holds the ChatGPT sign-in handles when the tool uses it.
	openai   *openaiRegistry
	config   Config
	registry registry
	mu       sync.Mutex
	now      func() time.Time
	write    func(string, any) error
	failed   bool
}

// Open recovers a proxy reload without rotating handles. A new logical session
// must use a fresh RuntimeDir and incarnation even if its container name repeats.
func Open(config Config) (*Broker, error) {
	tool, known := protectedauth.Lookup(config.Tool)
	if !known || config.Identity == "" || config.Session == "" || config.Incarnation == "" || !filepath.IsAbs(config.LockPath) {
		return nil, ErrState
	}
	// Admission on the host creates this file before mounting it. Missing or
	// symlinked locks must not silently become a gateway-local lock object.
	info, err := os.Lstat(config.LockPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrState
	}
	for _, dir := range []string{config.StoreDir, config.RuntimeDir} {
		if !filepath.IsAbs(dir) || privateDirectory(dir) != nil {
			return nil, ErrState
		}
	}
	store, err := filepath.EvalSymlinks(config.StoreDir)
	if err != nil {
		return nil, ErrState
	}
	runtime, err := filepath.EvalSymlinks(config.RuntimeDir)
	if err != nil || within(store, runtime) || within(runtime, store) {
		return nil, ErrState
	}
	b := &Broker{config: config, now: time.Now, write: durableJSON, anthropic: tool.Uses(protectedauth.AdapterAnthropic)}
	err = b.locked(func() error {
		path := filepath.Join(config.RuntimeDir, registryFile)
		if err := readPrivateJSON(path, &b.registry); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return ErrState
			}
			access, err := randomHandle(accessHandlePrefix)
			if err != nil {
				return ErrState
			}
			refresh, err := randomHandle(refreshHandlePrefix)
			if err != nil {
				return ErrState
			}
			b.registry = registry{Schema: 1, Identity: config.Identity, Session: config.Session, Incarnation: config.Incarnation, Access: access, Refresh: refresh}
			// Registration is durable BEFORE any upstream exchange or grant commit.
			// A crash here leaves only unused handles; it cannot acknowledge a grant
			// whose registration is missing. There is no two-rename transaction.
			if err := b.write(path, b.registry); err != nil {
				return ErrState
			}
		}
		r := b.registry
		if r.Schema != 1 || r.Identity != config.Identity || r.Session != config.Session || r.Incarnation != config.Incarnation || !validHandle(r.Access, accessHandlePrefix) || !validHandle(r.Refresh, refreshHandlePrefix) || r.Access == r.Refresh {
			return ErrState
		}
		// An earlier process may have failed directory fsync after rename. Fence
		// both recovered files before acknowledging anything from their visible
		// contents. Rewriting the registry first retains registration-before-grant.
		if err := b.write(path, b.registry); err != nil {
			return ErrState
		}
		g, err := b.readGrant()
		if err != nil {
			return err
		}
		if g != nil {
			if err := b.writeGrant(g); err != nil {
				return err
			}
		}
		if tool.Uses(protectedauth.AdapterOpenAI) {
			return b.openOpenAI()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if b.openai != nil {
		b.adapters = append(b.adapters, openaiAdapter{b: b})
	}
	if config.GitHub != nil {
		b.github, err = openGitHub(*config.GitHub)
		if err != nil {
			return nil, err
		}
	}
	return b, nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func privateDirectory(path string) error {
	// The host provisions these directories. The proxy retains its distinct UID
	// and preserves their ownership when replacing files.
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return ErrState
	}
	return nil
}

func randomHandle(prefix string) (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func validHandle(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == 32
}

func (b *Broker) locked(fn func() error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed {
		return ErrState
	}
	release, err := util.AcquireFileLock(b.config.LockPath, nil)
	if err != nil {
		return ErrState
	}
	defer release()
	return fn()
}

func (b *Broker) readGrant() (*grant, error) {
	var g grant
	if err := readPrivateJSON(filepath.Join(b.config.StoreDir, grantFile), &g); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, ErrState
	}
	if g.Schema != 1 || g.Identity != b.config.Identity || g.Issuer != issuer || (g.Status != "ready" && g.Status != "uncertain") {
		return nil, ErrState
	}
	if g.Status == "ready" && (g.Generation == 0 || g.CommittedAt.Unix() <= 0 || g.ExpiresAt <= g.CommittedAt.Unix() || g.ExpiresAt-g.CommittedAt.Unix() != g.Token.Expires || validateToken(g.Token, true) != nil) {
		return nil, ErrState
	}
	// Another live gateway may have renamed a grant but lost its directory
	// fsync acknowledgement. Fence shared state before releasing any credential.
	if err := util.SyncDir(b.config.StoreDir); err != nil {
		return nil, ErrState
	}
	return &g, nil
}

func readPrivateJSON(path string, dst any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxDocument {
		return ErrState
	}
	data, err := os.ReadFile(path) // #nosec G304 -- fixed filename in trusted gateway-only directory.
	if err != nil {
		return ErrState
	}
	if err := strictJSON(data, dst); err != nil {
		return ErrState
	}
	return nil
}

func durableJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrState
	}
	dir := filepath.Dir(path)
	if err := privateDirectory(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return ErrState
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	f, err := os.CreateTemp(dir, ".oauth-*")
	if err != nil {
		return ErrState
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }() // #nosec G703 -- generated temporary path within the trusted private store.
	if err := inheritFileOwner(f, dir); err != nil {
		return ErrState
	}
	if _, err := f.Write(data); err != nil {
		return ErrState
	}
	if err := f.Sync(); err != nil {
		return ErrState
	}
	if err := f.Close(); err != nil {
		return ErrState
	}
	if err := os.Rename(f.Name(), path); err != nil { // #nosec G703 -- fixed destination in the trusted private store; no workload path input.
		return ErrState
	}
	d, err := os.Open(dir) // #nosec G304 -- trusted gateway-only state directory.
	if err != nil {
		return ErrState
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return ErrState
	}
	return nil
}
