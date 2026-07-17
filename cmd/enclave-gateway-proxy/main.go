// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"enclave/internal/gateway/bundle"
	"enclave/internal/gateway/mitm"
	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tlsRoot := strings.TrimSpace(os.Getenv(model.EnvGatewayTLSRoot))
	if tlsRoot == "" {
		tlsRoot = model.GatewayTLSRootPath
	}
	configDir := strings.TrimSpace(os.Getenv(model.EnvGatewayConfigDir))
	if configDir == "" {
		configDir = model.GatewayConfigDir
	}
	readyFile := strings.TrimSpace(os.Getenv(model.EnvGatewayProxyReadyFile))

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
	case model.NetworkLogCoarse, model.NetworkLogRequests:
	default:
		log.Fatal("invalid network log mode; expected coarse or requests")
	}

	rules, err := mitm.LoadRules(strings.TrimSpace(os.Getenv(model.EnvSecretReleaseFile)))
	if err != nil {
		log.Fatalf("failed to load secret release rules: %v", err)
	}
	opts := mitm.Options{
		HTTPAddr:       ":8080",
		HTTPSAddr:      ":8443",
		AllowedDomains: allowedDomains,
		DeniedDomains:  deniedDomains,
		SecretRules:    rules,
		AuditLogPath:   strings.TrimSpace(os.Getenv(model.EnvNetworkLogFile)),
		ForceHTTPSMITM: networkLogMode == model.NetworkLogRequests,
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
	if err := mitm.Run(ctx, opts); err != nil {
		log.Fatalf("gateway proxy failed: %v", err)
	}
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
