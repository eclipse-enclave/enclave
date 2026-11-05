// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package a2ahub is the A2A hub proxy: it loads the host-published snapshot,
// authenticates members, forwards JSON-RPC to workers by name and holds the
// control and liveness streams (proposal 6, interfaces.md sections 1, 2, 11).
package a2ahub

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

// Defaults for bounds the hub environment may leave unset (proposal 17).
const (
	DefaultRetainedResults   = 100
	DefaultMaxRequestBytes   = 1 << 20
	DefaultReconnectWindow   = 60 * time.Second
	DefaultKeepaliveInterval = 15 * time.Second
	// DefaultUserTimeout is TCP_USER_TIMEOUT on hub sockets (CR-P1D-3).
	DefaultUserTimeout = 10 * time.Second
	DefaultDialTimeout = 5 * time.Second
)

// Log modes of ENCLAVE_A2A_LOG.
const (
	LogCoarse = "coarse"
	LogFull   = "full"
)

// requestEnvelopeBytes is the JSON-RPC overhead allowed on top of the text bound.
const requestEnvelopeBytes = 64 << 10

// Config is the proxy configuration, normally read from the hub environment.
type Config struct {
	Group           string
	GroupGeneration peergroup.Generation
	// Epoch is the supervisor's hub epoch (CR-A4-2); New generates one when empty.
	Epoch           string
	Port            int
	SnapshotDir     string
	StatusDir       string
	AuditDir        string
	LogMode         string
	RetainedResults int
	MaxRequestBytes int64
	ReconnectWindow time.Duration
	// ProxyUID is the uid the proxy must run as; -1 skips the check.
	ProxyUID          int
	KeepaliveInterval time.Duration
	UserTimeout       time.Duration
	DialTimeout       time.Duration
	Now               func() time.Time
}

// ConfigFromEnv reads the hub environment (interfaces.md section 6).
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		Group:             strings.TrimSpace(getenv(a2a.EnvGroup)),
		GroupGeneration:   peergroup.Generation(strings.TrimSpace(getenv(a2a.EnvGroupGeneration))),
		Epoch:             strings.TrimSpace(getenv(a2a.EnvHubEpoch)),
		Port:              peergroup.HubPort,
		SnapshotDir:       envOr(getenv, a2a.EnvHubSnapshotDir, peergroup.HubSnapshotMountPath),
		StatusDir:         envOr(getenv, a2a.EnvHubStatusDir, peergroup.HubStatusMountPath),
		AuditDir:          envOr(getenv, a2a.EnvHubAuditDir, peergroup.HubAuditMountPath),
		LogMode:           envOr(getenv, a2a.EnvHubLogMode, LogCoarse),
		RetainedResults:   DefaultRetainedResults,
		MaxRequestBytes:   DefaultMaxRequestBytes,
		ReconnectWindow:   DefaultReconnectWindow,
		ProxyUID:          -1,
		KeepaliveInterval: DefaultKeepaliveInterval,
		UserTimeout:       DefaultUserTimeout,
		DialTimeout:       DefaultDialTimeout,
	}
	if err := peergroup.ValidateGroupName(cfg.Group); err != nil {
		return Config{}, fmt.Errorf("%s: %w", a2a.EnvGroup, err)
	}
	if !cfg.GroupGeneration.Valid() {
		return Config{}, fmt.Errorf("%s %q is not a group generation", a2a.EnvGroupGeneration, cfg.GroupGeneration)
	}
	if cfg.Epoch != "" && !peergroup.Generation(cfg.Epoch).Valid() {
		return Config{}, fmt.Errorf("%s %q is not an epoch", a2a.EnvHubEpoch, cfg.Epoch)
	}
	if cfg.LogMode != LogCoarse && cfg.LogMode != LogFull {
		return Config{}, fmt.Errorf("%s %q is not %s or %s", a2a.EnvHubLogMode, cfg.LogMode, LogCoarse, LogFull)
	}
	var err error
	if cfg.Port, err = envInt(getenv, a2a.EnvHubPort, cfg.Port, 1, 65535); err != nil {
		return Config{}, err
	}
	if cfg.RetainedResults, err = envInt(getenv, a2a.EnvHubRetainedResults, cfg.RetainedResults, 1, 1<<20); err != nil {
		return Config{}, err
	}
	maxReq, err := envInt(getenv, a2a.EnvMaxRequestBytes, int(cfg.MaxRequestBytes), 1, 1<<30)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxRequestBytes = int64(maxReq)
	if cfg.ProxyUID, err = envInt(getenv, a2a.EnvHubProxyUID, cfg.ProxyUID, 0, 1<<31-1); err != nil {
		return Config{}, err
	}
	if v := strings.TrimSpace(getenv(a2a.EnvReconnectWindow)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("%s %q is not a positive duration", a2a.EnvReconnectWindow, v)
		}
		cfg.ReconnectWindow = d
	}
	cfg.withDefaults()
	return cfg, nil
}

func envOr(getenv func(string) string, name, fallback string) string {
	if v := strings.TrimSpace(getenv(name)); v != "" {
		return v
	}
	return fallback
}

func envInt(getenv func(string) string, name string, fallback, lo, hi int) (int, error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s %q is not an integer in [%d, %d]", name, v, lo, hi)
	}
	return n, nil
}

func (c *Config) withDefaults() {
	if c.Port == 0 {
		c.Port = peergroup.HubPort
	}
	if c.LogMode == "" {
		c.LogMode = LogCoarse
	}
	if c.RetainedResults <= 0 {
		c.RetainedResults = DefaultRetainedResults
	}
	if c.MaxRequestBytes <= 0 {
		c.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if c.ReconnectWindow <= 0 {
		c.ReconnectWindow = DefaultReconnectWindow
	}
	if c.KeepaliveInterval <= 0 {
		c.KeepaliveInterval = DefaultKeepaliveInterval
	}
	if c.UserTimeout <= 0 {
		c.UserTimeout = DefaultUserTimeout
	}
	if c.UserTimeout >= c.ReconnectWindow {
		// Silent loss must surface well inside the reconnect window.
		c.UserTimeout = c.ReconnectWindow / 2
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = DefaultDialTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}
