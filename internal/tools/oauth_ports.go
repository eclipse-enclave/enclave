// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tools

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"

	"enclave/internal/model"
	"enclave/internal/util"
)

// OAuthPortHints returns OAuth callback ports for providers missing a session.
func OAuthPortHints(ctx model.RunContext) []string {
	ports := map[string]bool{}
	var ordered []string
	for _, provider := range ctx.Profile.Providers {
		if provider.Name == "" {
			continue
		}
		state := ctx.AuthState.Providers[provider.Name]
		for _, cfg := range provider.OAuthPorts {
			port := strings.TrimSpace(cfg.Port)
			if port == "" {
				continue
			}
			autoHint := true
			if cfg.AutoHintWhenNoSession != nil {
				autoHint = *cfg.AutoHintWhenNoSession
			}
			if autoHint && !state.HasSession {
				if !ports[port] {
					ports[port] = true
					ordered = append(ordered, port)
				}
			}
		}
	}
	if len(ordered) == 0 {
		return nil
	}
	return ordered
}

// OAuthPortValidateRun enforces exact port mappings when OAuth is required for a provider.
func OAuthPortValidateRun(ctx model.RunContext) error {
	for _, provider := range ctx.Profile.Providers {
		if provider.Name == "" {
			continue
		}
		state := ctx.AuthState.Providers[provider.Name]
		for _, cfg := range provider.OAuthPorts {
			port := strings.TrimSpace(cfg.Port)
			if port == "" {
				continue
			}
			if !util.IsPortNumber(port) {
				return fmt.Errorf("%s oauth port %q is invalid", provider.Name, port)
			}
			requireMapping := true
			if cfg.RequireMappingWhenNoCredentials != nil {
				requireMapping = *cfg.RequireMappingWhenNoCredentials
			}
			if !requireMapping || state.HasSession || state.HasEnvCredential {
				continue
			}
			hasHost, hasContainer, hasExact := util.PortMappingState(ctx.Run.Ports, port)
			if !hasExact {
				if hasHost || hasContainer {
					return fmt.Errorf("%s oauth requires host port %s mapped to container port %s (use -p %s or -p %s:%s)", provider.Name, port, port, port, port, port)
				}
				return fmt.Errorf("%s oauth requires host port %s to be mapped (use -p %s)", provider.Name, port, port)
			}
			if err := ensureHostPortAvailable(publishedHostIP(ctx.Run.Ports, port), port, provider.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// OAuthLoopbackPorts returns the loopback forwarding ports when the exact mapping exists.
func OAuthLoopbackPorts(ctx model.RunContext) []string {
	ports := map[string]bool{}
	var ordered []string
	for _, provider := range ctx.Profile.Providers {
		if provider.Name == "" {
			continue
		}
		for _, cfg := range provider.OAuthPorts {
			port := strings.TrimSpace(cfg.Port)
			if port == "" {
				continue
			}
			if hasPortMapping(ctx.Run.Ports, port, port) {
				if !ports[port] {
					ports[port] = true
					ordered = append(ordered, port)
				}
			}
		}
	}
	if len(ordered) == 0 {
		return nil
	}
	return ordered
}

func hasPortMapping(ports []string, hostPort string, containerPort string) bool {
	for _, port := range ports {
		host, container, ok := util.SplitPortMapping(port)
		if !ok {
			continue
		}
		if host == hostPort && container == containerPort {
			return true
		}
	}
	return false
}

// publishedHostIP returns the host address of the exact port:port mapping.
func publishedHostIP(ports []string, port string) string {
	for _, spec := range ports {
		hostIP, hostPort, containerPort, ok := util.ParsePortSpec(spec)
		if ok && hostPort == port && containerPort == port {
			return util.NormalizePortHostIP(hostIP)
		}
	}
	return util.DefaultPortHostIP
}

// ensureHostPortAvailable probes the port on the address it is published on
// and on one overlapping address. On macOS a probe fails only while the port
// is held on the probed address itself, so a loopback probe misses a holder
// on the wildcard, and a wildcard probe misses one on loopback, where
// sessions publish by default.
func ensureHostPortAvailable(hostIP string, port string, providerName string) error {
	overlapping := "0.0.0.0"
	if net.ParseIP(hostIP).IsUnspecified() {
		overlapping = util.DefaultPortHostIP
	}
	for _, ip := range []string{hostIP, overlapping} {
		listener, err := net.Listen("tcp", net.JoinHostPort(ip, port))
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("host port %s is already in use on %s; %s oauth requires it", port, ip, providerName)
		}
		if err != nil {
			return fmt.Errorf("%s oauth requires host port %s on %s: %w", providerName, port, hostIP, err)
		}
		_ = listener.Close()
	}
	return nil
}
