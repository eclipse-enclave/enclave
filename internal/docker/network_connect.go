// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// ErrStaleEndpoint marks a failed connect whose endpoint config survived the
// rollback (Docker 29, running container) and would be re-applied on restart.
var ErrStaleEndpoint = errors.New("engine kept the failed endpoint configuration")

// EndpointConflictError reports an existing attachment with another or an
// unverifiable (empty Have) IPv4 address.
type EndpointConflictError struct {
	Network   string
	Container string
	Want      string
	Have      string
}

func (e *EndpointConflictError) Error() string {
	have := e.Have
	if have == "" {
		have = "an unknown address"
	}
	return fmt.Sprintf("container %s is already connected to network %s with %s, want %s", e.Container, e.Network, have, e.Want)
}

// ContainerNetworkEndpoint is one entry of a container's inspected networks.
type ContainerNetworkEndpoint struct {
	NetworkID  string                `json:"NetworkID"`
	IPAddress  string                `json:"IPAddress"`
	IPAMConfig *EndpointIPAMSettings `json:"IPAMConfig"`
	Aliases    []string              `json:"Aliases"`
}

// EndpointIPAMSettings is the user-requested address of an endpoint.
type EndpointIPAMSettings struct {
	IPv4Address string `json:"IPv4Address"`
}

// StaticIPv4 returns the static address; Podman leaves IPAMConfig empty, so it
// falls back to the live address there (empty while stopped).
func (e ContainerNetworkEndpoint) StaticIPv4() string {
	if e.IPAMConfig != nil && e.IPAMConfig.IPv4Address != "" {
		return e.IPAMConfig.IPv4Address
	}
	if IsPodman() {
		return e.IPAddress
	}
	return ""
}

// NetworkConnect attaches container to network at a static IPv4 address; an
// existing attachment at that address is success, any other address an
// *EndpointConflictError.
func NetworkConnect(ctx context.Context, network, container, ipv4 string, aliases ...string) error {
	args, err := networkConnectArgs(network, container, ipv4, aliases)
	if err != nil {
		return err
	}
	_, err = capture(ctx, args...)
	if err == nil {
		return nil
	}
	if isAlreadyConnected(err) {
		return verifyExistingEndpoint(ctx, network, container, ipv4)
	}
	if IsNotFound(err) || IsNetworkNotFound(err) {
		return err
	}
	// Both engines keep the endpoint config after an IPAM failure, so roll back.
	_, _ = capture(ctx, networkDisconnectArgs(network, container, false)...)
	if endpoints, inspectErr := ContainerNetworkEndpoints(ctx, container); inspectErr == nil {
		if _, stale := findEndpoint(endpoints, network); stale {
			return errors.Join(err, ErrStaleEndpoint)
		}
	}
	return err
}

func networkConnectArgs(network, container, ipv4 string, aliases []string) ([]string, error) {
	if strings.TrimSpace(network) == "" || strings.TrimSpace(container) == "" {
		return nil, fmt.Errorf("network connect requires a network and a container")
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(ipv4))
	if err != nil || !addr.Is4() {
		return nil, fmt.Errorf("network connect requires a static IPv4 address, got %q", ipv4)
	}
	args := []string{"network", "connect", "--ip", addr.String()}
	for _, alias := range aliases {
		if alias = strings.TrimSpace(alias); alias != "" {
			args = append(args, "--alias", alias)
		}
	}
	return append(args, network, container), nil
}

func verifyExistingEndpoint(ctx context.Context, network, container, ipv4 string) error {
	endpoints, err := ContainerNetworkEndpoints(ctx, container)
	if err != nil {
		return err
	}
	endpoint, ok := findEndpoint(endpoints, network)
	if !ok {
		return fmt.Errorf("engine reports container %s connected to network %s, but inspect does not list it", container, network)
	}
	want, _ := netip.ParseAddr(strings.TrimSpace(ipv4))
	have, err := netip.ParseAddr(endpoint.StaticIPv4())
	if err == nil && have == want {
		return nil
	}
	return &EndpointConflictError{Network: network, Container: container, Want: want.String(), Have: endpoint.StaticIPv4()}
}

// findEndpoint matches by name key, or by ID because Docker keys by name only.
func findEndpoint(endpoints map[string]ContainerNetworkEndpoint, network string) (ContainerNetworkEndpoint, bool) {
	if endpoint, ok := endpoints[network]; ok {
		return endpoint, true
	}
	for _, endpoint := range endpoints {
		if endpoint.NetworkID == network || (len(network) >= 12 && strings.HasPrefix(endpoint.NetworkID, network)) {
			return endpoint, true
		}
	}
	return ContainerNetworkEndpoint{}, false
}

// ContainerNetworkEndpoints returns a container's network attachments keyed
// by network name.
func ContainerNetworkEndpoints(ctx context.Context, container string) (map[string]ContainerNetworkEndpoint, error) {
	out, err := capture(ctx, "container", "inspect", "--format", "{{json .NetworkSettings.Networks}}", container)
	if err != nil {
		return nil, err
	}
	return decodeContainerNetworkEndpoints(out)
}

func decodeContainerNetworkEndpoints(out string) (map[string]ContainerNetworkEndpoint, error) {
	endpoints := map[string]ContainerNetworkEndpoint{}
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return endpoints, nil
	}
	if err := json.Unmarshal([]byte(out), &endpoints); err != nil {
		return nil, fmt.Errorf("decode container network settings: %w", err)
	}
	return endpoints, nil
}

// NetworkDisconnect detaches container from network. A container that is not
// attached, a missing container, and a missing network all count as success.
func NetworkDisconnect(ctx context.Context, network, container string, force bool) error {
	if strings.TrimSpace(network) == "" || strings.TrimSpace(container) == "" {
		return fmt.Errorf("network disconnect requires a network and a container")
	}
	_, err := capture(ctx, networkDisconnectArgs(network, container, force)...)
	if err == nil || isNotConnected(err) || IsNotFound(err) || IsNetworkNotFound(err) {
		return nil
	}
	return err
}

func networkDisconnectArgs(network, container string, force bool) []string {
	args := []string{"network", "disconnect"}
	if force {
		args = append(args, "--force")
	}
	return append(args, network, container)
}

func cliStderr(err error) (string, bool) {
	var ce *cliError
	if !errors.As(err, &ce) {
		return "", false
	}
	return strings.ToLower(ce.stderr), true
}

// IsNetworkNotFound reports whether a network command failed because the
// named network does not exist.
func IsNetworkNotFound(err error) bool {
	s, ok := cliStderr(err)
	if !ok {
		return false
	}
	if strings.Contains(s, "no such network") || strings.Contains(s, "unable to find network with name or id") {
		return true
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "error response from daemon: ")
		if strings.HasPrefix(line, "network ") && strings.HasSuffix(line, " not found") {
			return true
		}
	}
	return false
}

func isAlreadyConnected(err error) bool {
	s, ok := cliStderr(err)
	if !ok {
		return false
	}
	return strings.Contains(s, "already exists in network") || strings.Contains(s, "network is already connected")
}

// Docker reports "endpoint <container> not found" for a forced disconnect of
// a missing container.
func isNotConnected(err error) bool {
	s, ok := cliStderr(err)
	if !ok {
		return false
	}
	return strings.Contains(s, "is not connected to") ||
		(strings.Contains(s, "endpoint ") && strings.Contains(s, " not found"))
}

// IsAddressInUse reports whether the requested static address is held by
// another endpoint on the network.
func IsAddressInUse(err error) bool {
	s, ok := cliStderr(err)
	if !ok {
		return false
	}
	return strings.Contains(s, "address already in use") || strings.Contains(s, "is already allocated to container")
}

// IsStaticAddressRejected reports whether the engine refused the requested
// static address for the network: outside every subnet, or the network does
// not accept user-specified addresses at all.
func IsStaticAddressRejected(err error) bool {
	s, ok := cliStderr(err)
	if !ok {
		return false
	}
	return strings.Contains(s, "no configured subnet contains ip address") ||
		strings.Contains(s, "not in any subnet on network") ||
		strings.Contains(s, "user-specified ip address is supported") ||
		strings.Contains(s, "user specified ip address is supported")
}

// IsUnattachableNetworkMode reports whether the container's network mode
// (none, host, slirp4netns, pasta) forbids attaching further networks.
func IsUnattachableNetworkMode(err error) bool {
	s, ok := cliStderr(err)
	if !ok {
		return false
	}
	return strings.Contains(s, "cannot be connected to multiple networks") ||
		strings.Contains(s, "invalid network mode")
}
