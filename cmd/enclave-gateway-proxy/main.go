// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"enclave/internal/gateway/bundle"
	"enclave/internal/gateway/dnsaudit"
	"enclave/internal/gateway/mitm"
	"enclave/internal/gateway/oauthbroker"
	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
)

func main() {
	// The gateway runs this binary twice: once as the MITM proxy, and once as
	// the DNS audit translator. The translator is a separate process because
	// DNS denials must be recorded even when the proxy is disabled.
	dnsAuditLog := flag.String("dns-audit", "", "Translate this dnsmasq log into network audit events instead of serving as a proxy")
	networkModePath := flag.String("network-mode", "", "Print the validated gateway bundle network mode")
	networkState := flag.Bool("network-state", false, "Print the active gateway policy state and bundle hash as JSON")
	flag.Parse()
	if *networkState {
		state, err := bundle.ReadActiveState(model.GatewayActiveMetaPath, model.GatewayActiveDNSMasqPath, model.GatewayActiveDomainsPath)
		if err != nil {
			log.Fatal("invalid gateway network state")
		}
		if err := json.NewEncoder(os.Stdout).Encode(state); err != nil {
			log.Fatal("failed to write gateway network state")
		}
		return
	}
	if *networkModePath != "" {
		mode, err := bundle.ReadMode(*networkModePath)
		if err != nil {
			log.Fatal("invalid gateway network mode")
		}
		fmt.Println(mode)
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if path := strings.TrimSpace(*dnsAuditLog); path != "" {
		if strings.TrimSpace(os.Getenv(model.EnvOAuthBroker)) != "" {
			log.Fatal("raw DNS auditing is disabled for protected OAuth sessions")
		}
		if err := dnsaudit.Run(ctx, dnsaudit.Options{
			DNSMasqLogPath: path,
			NetworkLogPath: strings.TrimSpace(os.Getenv(model.EnvNetworkLogFile)),
			Session:        strings.TrimSpace(os.Getenv(model.EnvGatewaySession)),
		}); err != nil {
			log.Fatalf("gateway dns audit failed: %v", err)
		}
		return
	}

	tlsRoot := strings.TrimSpace(os.Getenv(model.EnvGatewayTLSRoot))
	if tlsRoot == "" {
		tlsRoot = model.GatewayTLSRootPath
	}
	configDir := strings.TrimSpace(os.Getenv(model.EnvGatewayConfigDir))
	if configDir == "" {
		configDir = model.GatewayConfigDir
	}
	readyFile := strings.TrimSpace(os.Getenv(model.EnvGatewayProxyReadyFile))
	networkMode, err := bundle.ReadMode(filepath.Join(configDir, filepath.Base(model.GatewayConfigMetaPath)))
	if err != nil {
		log.Fatal("failed to load gateway network mode")
	}

	allowedDomains, err := bundle.ReadAllowedDomains(filepath.Join(configDir, filepath.Base(model.GatewayConfigDomainsPath)))
	if err != nil {
		log.Fatalf("failed to load gateway allowed domains: %v", err)
	}
	deniedDomains, err := bundle.ReadDeniedDomains(filepath.Join(configDir, filepath.Base(model.GatewayConfigDeniedPath)))
	if err != nil {
		log.Fatalf("failed to load gateway denied domains: %v", err)
	}
	networkLogMode := strings.TrimSpace(strings.ToLower(os.Getenv(model.EnvNetworkLogMode)))
	if networkLogMode == "" {
		networkLogMode = model.NetworkLogCoarse
	}
	switch networkLogMode {
	case model.NetworkLogCoarse, model.NetworkLogRequests, model.NetworkLogFull:
	default:
		log.Fatal("invalid network log mode; expected coarse, requests or full")
	}
	captureDir := ""
	if networkLogMode == model.NetworkLogFull {
		captureDir = strings.TrimSpace(os.Getenv(model.EnvNetworkCaptureDir))
		if captureDir == "" {
			log.Fatal("full network capture requires a capture directory")
		}
	}

	rules, err := mitm.LoadRules(strings.TrimSpace(os.Getenv(model.EnvSecretReleaseFile)))
	if err != nil {
		log.Fatalf("failed to load secret release rules: %v", err)
	}
	broker, err := loadOAuthBroker(oauthbroker.Config{
		StoreDir: model.GatewayOAuthGrantPath, RuntimeDir: model.GatewayOAuthRuntimePath, LockPath: model.GatewayOAuthLockPath,
	}, model.GatewayOAuthViewPath)
	if err != nil {
		log.Fatal("protected OAuth initialization failed; inspect the selected identity or start a fresh session")
	}
	opts := mitm.Options{
		HTTPAddr:             ":8080",
		HTTPSAddr:            ":8443",
		AllowedDomains:       allowedDomains,
		DeniedDomains:        deniedDomains,
		AllowAllDestinations: networkMode == model.NetworkModeUnrestricted,
		OAuthBroker:          broker,
		SecretRules:          rules,
		AuditLogPath:         strings.TrimSpace(os.Getenv(model.EnvNetworkLogFile)),
		CaptureDir:           captureDir,
		Session:              strings.TrimSpace(os.Getenv(model.EnvGatewaySession)),
		ForceHTTPSMITM:       networkLogMode == model.NetworkLogRequests || networkLogMode == model.NetworkLogFull,
		// The host runtime pre-provisions CA key material under tlsRoot.
		TLSStore: tlsstore.New(tlsRoot),
	}
	if readyFile != "" || broker != nil {
		opts.OnReady = func() error {
			if broker != nil {
				if err := broker.MarkReady(); err != nil {
					return fmt.Errorf("protected OAuth readiness attestation failed: %w", err)
				}
			}
			if readyFile != "" {
				return markProxyReady(readyFile)
			}
			return nil
		}
	}
	if err := mitm.Run(ctx, opts); err != nil {
		log.Fatalf("gateway proxy failed: %v", err)
	}
}

func loadOAuthBroker(config oauthbroker.Config, viewDir string) (*oauthbroker.Broker, error) {
	mode := strings.TrimSpace(os.Getenv(model.EnvOAuthBroker))
	config.Identity = strings.TrimSpace(os.Getenv(model.EnvOAuthIdentity))
	config.Session = strings.TrimSpace(os.Getenv(model.EnvGatewaySession))
	config.Incarnation = strings.TrimSpace(os.Getenv(model.EnvOAuthIncarnation))
	githubMode := strings.TrimSpace(os.Getenv(model.EnvOAuthGitHub))
	githubIdentity := strings.TrimSpace(os.Getenv(model.EnvOAuthGitHubIdentity))
	if mode == "" && config.Identity == "" && config.Incarnation == "" && githubMode == "" && githubIdentity == "" && config.GitHub == nil {
		return nil, nil
	}
	if mode != "claude" || config.Identity == "" || config.Session == "" || config.Incarnation == "" {
		return nil, oauthbroker.ErrState
	}
	if githubMode != "" || githubIdentity != "" || config.GitHub != nil {
		if githubMode != "1" || githubIdentity == "" {
			return nil, oauthbroker.ErrState
		}
		if config.GitHub == nil {
			config.GitHub = &oauthbroker.GitHubConfig{
				StoreDir: model.GatewayOAuthGitHubGrantPath, RuntimeDir: model.GatewayOAuthGitHubRuntimePath,
				ViewDir: model.GatewayOAuthGitHubViewPath, LockPath: model.GatewayOAuthGitHubLockPath,
			}
		}
		config.GitHub.Identity = githubIdentity
		config.GitHub.Session = config.Session
		config.GitHub.Incarnation = config.Incarnation
	}
	broker, err := oauthbroker.Open(config)
	if err != nil {
		return nil, err
	}
	if err := broker.InitializeView(viewDir); err != nil {
		return nil, err
	}
	return broker, nil
}

func markProxyReady(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil { // #nosec G703 -- path is supplied by the trusted gateway runtime.
		return fmt.Errorf("create ready dir %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte("ready\n"), 0o600); err != nil { // #nosec G703 -- path is supplied by the trusted gateway runtime.
		return fmt.Errorf("write ready file %s: %w", path, err)
	}
	return nil
}
