// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"fmt"
	"strings"
)

// This file defines the small set of local types that replace the Docker Go
// SDK structs (container.Config, container.HostConfig, mount.Mount, the nat
// port types, the inspect/list responses, and so on). They carry only the
// fields the codebase actually uses, and the docker package translates them to
// `docker` CLI flags at the boundary (see cli.go).

// MountType identifies the kind of a mount for CLI flag translation.
type MountType string

const (
	MountTypeBind   MountType = "bind"
	MountTypeVolume MountType = "volume"
	MountTypeTmpfs  MountType = "tmpfs"
)

// Mount describes a bind, volume, or tmpfs mount to attach to a container.
type Mount struct {
	Type     MountType
	Source   string
	Target   string
	ReadOnly bool
}

// ContainerConfig is the subset of container configuration we translate to
// `docker run`/`docker create` flags. It is also the decode target for the
// `.Config` object returned by `docker inspect`, so its JSON tags match the
// Docker inspect schema.
type ContainerConfig struct {
	Image        string            `json:"Image,omitempty"`
	Cmd          []string          `json:"Cmd,omitempty"`
	Entrypoint   []string          `json:"Entrypoint,omitempty"`
	Env          []string          `json:"Env,omitempty"`
	WorkingDir   string            `json:"WorkingDir,omitempty"`
	User         string            `json:"User,omitempty"`
	Hostname     string            `json:"Hostname,omitempty"`
	Labels       map[string]string `json:"Labels,omitempty"`
	ExposedPorts PortSet           `json:"ExposedPorts,omitempty"`
}

// HostConfig is the subset of host configuration we translate to flags.
type HostConfig struct {
	AutoRemove   bool
	Privileged   bool
	NetworkMode  NetworkMode
	Binds        []string
	Mounts       []Mount
	PortBindings PortMap
	ExtraHosts   []string
	Init         *bool
	SecurityOpt  []string
	Tmpfs        map[string]string
	CapAdd       []string
	CapDrop      []string
	Sysctls      map[string]string
}

// NetworkMode is the container network mode (for example "" for the default
// bridge or "container:<name>" to share another container's stack).
type NetworkMode string

// IsEmpty reports whether no explicit network mode is set.
func (n NetworkMode) IsEmpty() bool {
	return strings.TrimSpace(string(n)) == ""
}

// Port is a "<port>/<proto>" identifier (for example "8080/tcp"), mirroring the
// go-connections nat.Port string so it can be used as a map key.
type Port string

// PortSet is the set of exposed ports.
type PortSet map[Port]struct{}

// PortBinding maps a container port to a host interface/port.
type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// PortMap maps each published container port to its host bindings.
type PortMap map[Port][]PortBinding

// NewPort builds a Port from a protocol and port number. The signature mirrors
// nat.NewPort so call sites stay unchanged.
func NewPort(proto string, port string) (Port, error) {
	if strings.TrimSpace(port) == "" {
		return "", fmt.Errorf("port is empty")
	}
	if proto == "" {
		proto = "tcp"
	}
	return Port(port + "/" + proto), nil
}

// Num returns the numeric portion of the port ("8080" for "8080/tcp").
func (p Port) Num() string {
	s := string(p)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}

// Proto returns the protocol portion of the port ("tcp" when unspecified).
func (p Port) Proto() string {
	s := string(p)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return "tcp"
}

// Summary is the listing view of a container, derived from `docker inspect`.
type Summary struct {
	ID      string
	Names   []string
	Image   string
	Labels  map[string]string
	State   string
	Created int64
	Ports   PortMap
}

// InspectResponse is the subset of `docker inspect` output we consume.
type InspectResponse struct {
	ID              string           `json:"Id"`
	Name            string           `json:"Name"`
	Created         string           `json:"Created"`
	Config          *ContainerConfig `json:"Config"`
	State           *ContainerState  `json:"State"`
	Mounts          []MountPoint     `json:"Mounts"`
	NetworkSettings *NetworkSettings `json:"NetworkSettings"`
}

type NetworkSettings struct {
	Ports PortMap `json:"Ports"`
}

// ContainerState is the subset of a container's runtime state we consume.
type ContainerState struct {
	Status  string `json:"Status"`
	Running bool   `json:"Running"`
	Error   string `json:"Error"`
}

// MountPoint is an entry in `docker inspect`'s Mounts array.
type MountPoint struct {
	Type        MountType `json:"Type"`
	Name        string    `json:"Name"`
	Source      string    `json:"Source"`
	Destination string    `json:"Destination"`
}

// ImageInspectResponse is the subset of `docker image inspect` output we consume.
type ImageInspectResponse struct {
	Config *ImageConfig `json:"Config"`
}

// ImageConfig holds image-level configuration from `docker image inspect`.
type ImageConfig struct {
	Labels map[string]string `json:"Labels"`
}

// SystemInfo is the subset of `docker info` output we consume.
type SystemInfo struct {
	DockerRootDir   string   `json:"DockerRootDir"`
	SecurityOptions []string `json:"SecurityOptions"`
}

// PruneReport is the result of an image prune.
type PruneReport struct {
	SpaceReclaimed uint64
}

// BuildCachePruneReport is the result of a build-cache prune.
type BuildCachePruneReport struct {
	SpaceReclaimed uint64
}

// ListOptions controls a container list.
type ListOptions struct {
	All     bool
	Filters Filters
}

// Filters accumulates `docker ... --filter key=value` constraints, mirroring
// the SDK's filters.Args builder so call sites stay unchanged.
type Filters struct {
	pairs [][2]string
}

// NewFilters returns an empty filter set.
func NewFilters() Filters {
	return Filters{}
}

// Add appends a key=value filter.
func (f *Filters) Add(key string, value string) {
	f.pairs = append(f.pairs, [2]string{key, value})
}

// flags renders the filters as repeated `--filter key=value` arguments.
func (f Filters) flags() []string {
	out := make([]string, 0, len(f.pairs)*2)
	for _, pair := range f.pairs {
		out = append(out, "--filter", pair[0]+"="+pair[1])
	}
	return out
}
