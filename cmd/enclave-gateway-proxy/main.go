// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"enclave/internal/filter/engine"
	"enclave/internal/gateway/bundle"
	"enclave/internal/gateway/dnsaudit"
	"enclave/internal/gateway/mitm"
	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
)

func main() {
	// The gateway runs this binary twice: once as the MITM proxy, and once as
	// the DNS audit translator. The translator is a separate process because
	// DNS denials must be recorded even when the proxy is disabled.
	dnsAuditLog := flag.String("dns-audit", "", "Translate this dnsmasq log into network audit events instead of serving as a proxy")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if path := strings.TrimSpace(*dnsAuditLog); path != "" {
		if err := dnsaudit.Run(ctx, dnsaudit.Options{
			DNSMasqLogPath: path,
			NetworkLogPath: strings.TrimSpace(os.Getenv(model.EnvNetworkLogFile)),
			Session:        strings.TrimSpace(os.Getenv(model.EnvGatewaySession)),
		}); err != nil {
			log.Fatalf("gateway dns audit failed: %v", err)
		}
		return
	}

	// Registered before the proxy reports ready, so the entrypoint's reload
	// signal can never hit the default action, which would end the process.
	hangups := make(chan os.Signal, 1)
	signal.Notify(hangups, syscall.SIGHUP)

	tlsRoot := strings.TrimSpace(os.Getenv(model.EnvGatewayTLSRoot))
	if tlsRoot == "" {
		tlsRoot = model.GatewayTLSRootPath
	}
	configDir := strings.TrimSpace(os.Getenv(model.EnvGatewayConfigDir))
	if configDir == "" {
		configDir = model.GatewayConfigDir
	}
	readyFile := strings.TrimSpace(os.Getenv(model.EnvGatewayProxyReadyFile))

	// The entrypoint passes on the mode the host set on the container. An
	// unrestricted gateway has no policy files: it has nothing to enforce.
	networkMode := strings.TrimSpace(os.Getenv(model.EnvGatewayNetworkMode))
	restricted := networkMode != model.NetworkModeUnrestricted
	var allowlist mitm.Allowlist
	if restricted {
		var err error
		allowlist, err = readAllowlist(configDir)
		if err != nil {
			log.Fatal(err)
		}
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
	filters, err := loadFilters(strings.TrimSpace(os.Getenv(model.EnvFilterDir)), restricted)
	if err != nil {
		log.Fatal(err)
	}
	opts := mitm.Options{
		AllowedDomains: allowlist.Allowed,
		DeniedDomains:  allowlist.Denied,
		SecretRules:    rules,
		Filters:        filters,
		AuditLogPath:   strings.TrimSpace(os.Getenv(model.EnvNetworkLogFile)),
		CaptureDir:     captureDir,
		Session:        strings.TrimSpace(os.Getenv(model.EnvGatewaySession)),
		NetworkMode:    networkMode,
		ForceHTTPSMITM: networkLogMode == model.NetworkLogRequests || networkLogMode == model.NetworkLogFull,
		// The host runtime pre-provisions CA key material under tlsRoot.
		TLSStore: tlsstore.New(tlsRoot),
	}
	if readyFile != "" {
		opts.OnReady = func() {
			if err := markProxyReady(readyFile); err != nil {
				log.Printf("failed to write proxy ready file %q: %q", readyFile, err.Error()) // #nosec G706 -- quoted values cannot inject log lines.
			}
		}
	}
	if restricted {
		reloads := make(chan mitm.Allowlist)
		opts.Reloads = reloads
		go reloadOnHangup(ctx, hangups, configDir, reloads)
	}
	if err := mitm.Run(ctx, opts); err != nil {
		log.Fatalf("gateway proxy failed: %v", err)
	}
}

// readAllowlist reads the allowlist the entrypoint installed from the config
// bundle.
func readAllowlist(configDir string) (mitm.Allowlist, error) {
	allowed, err := bundle.ReadAllowedDomains(filepath.Join(configDir, filepath.Base(model.GatewayConfigDomainsPath)))
	if err != nil {
		return mitm.Allowlist{}, fmt.Errorf("failed to load gateway allowed domains: %w", err)
	}
	denied, err := bundle.ReadDeniedDomains(filepath.Join(configDir, filepath.Base(model.GatewayConfigDeniedPath)))
	if err != nil {
		return mitm.Allowlist{}, fmt.Errorf("failed to load gateway denied domains: %w", err)
	}
	return mitm.Allowlist{Allowed: allowed, Denied: denied}, nil
}

// reloadOnHangup hands the proxy a fresh allowlist on every SIGHUP. The
// entrypoint sends it after installing a new config bundle, instead of
// restarting the proxy, so request filter quotas survive the reload. An
// unreadable allowlist ends the process, which takes the gateway down.
func reloadOnHangup(ctx context.Context, hangups <-chan os.Signal, configDir string, reloads chan<- mitm.Allowlist) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-hangups:
		}
		allowlist, err := readAllowlist(configDir)
		if err != nil {
			log.Fatalf("reload: %v", err)
		}
		select {
		case reloads <- allowlist:
			log.Printf("reloaded the allowlist (%d allowed, %d denied)", len(allowlist.Allowed), len(allowlist.Denied))
		case <-ctx.Done():
			return
		}
	}
}

// loadFilters loads the request filters the host handed over in dir. Only a
// restricted gateway enforces them, and any error must stop the gateway: it
// never runs without a filter the session selected.
func loadFilters(dir string, restricted bool) (*engine.Set, error) {
	if dir == "" {
		return nil, nil
	}
	if !restricted {
		return nil, fmt.Errorf("request filters need a restricted gateway")
	}
	filters, err := mitm.LoadFilters(dir, mitm.NewFilterClient())
	if err != nil {
		return nil, fmt.Errorf("failed to load request filters: %w", err)
	}
	for _, e := range filters.Engines() {
		log.Printf("request filter %q active: preset %q", e.Filter(), e.Preset()) // #nosec G706 -- quoted values cannot inject log lines.
	}
	return filters, nil
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
