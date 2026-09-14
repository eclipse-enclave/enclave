// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func TestSessionNetworkNameAndLabels(t *testing.T) {
	meta := backend.SessionMeta{Name: "enclave-codex-abc123abc123-main", ProjectHash: "abc123abc123"}
	if got, want := sessionNetworkName(meta.Name), meta.Name+model.SessionNetworkSuffix; got != want {
		t.Fatalf("sessionNetworkName() = %q, want %q", got, want)
	}
	labels := sessionNetworkLabels(meta)
	for key, want := range map[string]string{
		model.NetworkLabelManaged:     "true",
		model.NetworkLabelContainer:   meta.Name,
		model.NetworkLabelProjectHash: meta.ProjectHash,
	} {
		if got := labels[key]; got != want {
			t.Fatalf("label %s = %q, want %q", key, got, want)
		}
	}
}

func TestSessionNetworkActionForDecisionTable(t *testing.T) {
	meta := backend.SessionMeta{Name: "session", ProjectHash: "project"}
	owned := sessionNetworkLabels(meta)
	for _, tc := range []struct {
		name         string
		info         dockercmd.NetworkInspectResponse
		hasEndpoints bool
		want         sessionNetworkAction
	}{
		{name: "unowned", info: dockercmd.NetworkInspectResponse{}, want: sessionNetworkConflict},
		{name: "wrong owner", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: copyLabels(owned, model.NetworkLabelContainer, "other")}, want: sessionNetworkConflict},
		{name: "wrong project", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: copyLabels(owned, model.NetworkLabelProjectHash, "other")}, want: sessionNetworkConflict},
		{name: "attached", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: owned, Containers: map[string]dockercmd.NetworkEndpoint{"id": {Name: "gateway"}}}, hasEndpoints: true, want: sessionNetworkAttached},
		// podman reports no endpoint map at all, so the attachment is only
		// visible through the engine's container list.
		{name: "attached without an endpoint map", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: owned}, hasEndpoints: true, want: sessionNetworkAttached},
		{name: "reusable", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: owned}, want: sessionNetworkReuse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionNetworkActionFor(tc.info, meta, tc.hasEndpoints); got != tc.want {
				t.Fatalf("sessionNetworkActionFor() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCreateSessionNetworkSubnetPolicy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		version     string
		wantSubnets []string
		firstErr    error
	}{
		{name: "docker 29 dynamic prefix", version: "29.0.0", wantSubnets: []string{sessionNetworkDynamicSubnet}},
		{name: "newer docker dynamic prefix", version: "30.1.0-beta.1", wantSubnets: []string{sessionNetworkDynamicSubnet}},
		{name: "older docker default allocation", version: "28.5.1", wantSubnets: []string{""}},
		{name: "dynamic prefix fallback", version: "29.0.0", wantSubnets: []string{sessionNetworkDynamicSubnet, ""}, firstErr: errors.New("unsupported subnet request")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreNetworkGlobals(t)
			var got []dockercmd.NetworkCreateOptions
			networkCreate = func(_ context.Context, opts dockercmd.NetworkCreateOptions) (string, error) {
				got = append(got, opts)
				if len(got) == 1 && tc.firstErr != nil {
					return "", tc.firstErr
				}
				return "network-id", nil
			}
			meta := backend.SessionMeta{Name: "session", ProjectHash: "project"}
			if _, err := createSessionNetwork(context.Background(), meta, dockercmd.SystemInfo{ServerVersion: tc.version}); err != nil {
				t.Fatalf("createSessionNetwork() error = %v", err)
			}
			var subnets []string
			for _, opts := range got {
				subnets = append(subnets, opts.Subnet)
				if opts.Driver != "bridge" || opts.EnableIPv6 == nil || *opts.EnableIPv6 {
					t.Fatalf("unexpected bridge options: %+v", opts)
				}
				if opts.Options[hostBindingIPv4Option] != "127.0.0.1" {
					t.Fatalf("host binding option = %q", opts.Options[hostBindingIPv4Option])
				}
			}
			if !reflect.DeepEqual(subnets, tc.wantSubnets) {
				t.Fatalf("subnet requests = %v, want %v", subnets, tc.wantSubnets)
			}
		})
	}
}

func TestEnsureSessionNetworkReuseAndConflict(t *testing.T) {
	meta := backend.SessionMeta{Name: "session", ProjectHash: "project"}
	for _, tc := range []struct {
		name       string
		info       dockercmd.NetworkInspectResponse
		wantErr    bool
		wantCreate int
		wantRemove int
	}{
		{name: "reuse", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: sessionNetworkLabels(meta)}},
		{name: "reuse expired after GC leaves it", info: dockercmd.NetworkInspectResponse{ID: "network-id", Created: time.Now().Add(-2 * sessionNetworkGCGracePeriod).Format(time.RFC3339Nano), Labels: sessionNetworkLabels(meta)}},
		{name: "conflict", info: dockercmd.NetworkInspectResponse{}, wantErr: true},
		{name: "attached", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: sessionNetworkLabels(meta), Containers: map[string]dockercmd.NetworkEndpoint{"id": {Name: "peer"}}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreNetworkGlobals(t)
			networkInspect = func(context.Context, string) (dockercmd.NetworkInspectResponse, error) { return tc.info, nil }
			var creates int
			var removedRefs []string
			networkCreate = func(context.Context, dockercmd.NetworkCreateOptions) (string, error) {
				creates++
				return "id", nil
			}
			networkRemove = func(_ context.Context, ref string) error {
				removedRefs = append(removedRefs, ref)
				return nil
			}
			dockerInfo = func(context.Context) (dockercmd.SystemInfo, error) { return dockercmd.SystemInfo{}, nil }
			_, err := New(Options{}).ensureSessionNetwork(context.Background(), meta, dockercmd.SystemInfo{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("ensureSessionNetwork() error = %v, wantErr %v", err, tc.wantErr)
			}
			if creates != tc.wantCreate || len(removedRefs) != tc.wantRemove {
				t.Fatalf("creates/removes = %d/%d, want %d/%d", creates, len(removedRefs), tc.wantCreate, tc.wantRemove)
			}
			if tc.wantRemove > 0 && !reflect.DeepEqual(removedRefs, []string{"network-id"}) {
				t.Fatalf("removed references = %v, want immutable network ID", removedRefs)
			}
		})
	}
}

func TestRemoveOwnedSessionNetworkIsConservative(t *testing.T) {
	owner := "session"
	for _, tc := range []struct {
		name       string
		info       dockercmd.NetworkInspectResponse
		refID      string
		runtime    bool
		wantRemove bool
	}{
		{name: "unowned", refID: "network-id", info: dockercmd.NetworkInspectResponse{}},
		{name: "replacement ID", refID: "old-network-id", info: dockercmd.NetworkInspectResponse{ID: "new-network-id", Labels: map[string]string{model.NetworkLabelManaged: "true", model.NetworkLabelContainer: owner, model.NetworkLabelProjectHash: "project"}}},
		{name: "attached", refID: "network-id", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: map[string]string{model.NetworkLabelManaged: "true", model.NetworkLabelContainer: owner, model.NetworkLabelProjectHash: "project"}, Containers: map[string]dockercmd.NetworkEndpoint{"id": {Name: owner}}}},
		{name: "reserved container", refID: "network-id", runtime: true, info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: map[string]string{model.NetworkLabelManaged: "true", model.NetworkLabelContainer: owner, model.NetworkLabelProjectHash: "project"}}},
		{name: "endpoint free", refID: "network-id", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: map[string]string{model.NetworkLabelManaged: "true", model.NetworkLabelContainer: owner, model.NetworkLabelProjectHash: "project"}}, wantRemove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreNetworkGlobals(t)
			networkInspect = func(context.Context, string) (dockercmd.NetworkInspectResponse, error) { return tc.info, nil }
			sessionRuntimeExists = func(context.Context, string) (bool, error) { return tc.runtime, nil }
			removed := false
			networkRemove = func(context.Context, string) error { removed = true; return nil }
			ref := sessionNetworkRef{Name: sessionNetworkName(owner), ID: tc.refID, Container: owner, ProjectHash: "project"}
			if err := removeOwnedSessionNetwork(context.Background(), ref, 0); err != nil {
				t.Fatalf("removeOwnedSessionNetwork() error = %v", err)
			}
			if removed != tc.wantRemove {
				t.Fatalf("removed = %v, want %v", removed, tc.wantRemove)
			}
		})
	}
}

func TestStaleSessionNetworkOwnerSelection(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	base := dockercmd.NetworkInspectResponse{
		ID:      "network-id",
		Name:    sessionNetworkName("session"),
		Created: now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
		Labels: map[string]string{
			model.NetworkLabelManaged:     "true",
			model.NetworkLabelContainer:   "session",
			model.NetworkLabelProjectHash: "project",
		},
	}
	for _, tc := range []struct {
		name string
		info dockercmd.NetworkInspectResponse
		want bool
	}{
		{name: "stale", info: base, want: true},
		{name: "within grace", info: copyNetwork(base, func(info *dockercmd.NetworkInspectResponse) {
			info.Created = now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
		})},
		{name: "attached", info: copyNetwork(base, func(info *dockercmd.NetworkInspectResponse) {
			info.Containers = map[string]dockercmd.NetworkEndpoint{"id": {Name: "session"}}
		})},
		{name: "missing owner", info: copyNetwork(base, func(info *dockercmd.NetworkInspectResponse) {
			info.Labels = copyLabels(info.Labels, model.NetworkLabelContainer, "")
		})},
		{name: "unowned", info: copyNetwork(base, func(info *dockercmd.NetworkInspectResponse) { info.Labels = nil })},
		{name: "invalid created", info: copyNetwork(base, func(info *dockercmd.NetworkInspectResponse) { info.Created = "invalid" })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got := staleSessionNetworkOwner(tc.info, now)
			if got != tc.want {
				t.Fatalf("staleSessionNetworkOwner() eligible = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGCSessionNetworksRequiresMissingOwnerContainer(t *testing.T) {
	restoreNetworkGlobals(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	makeNetwork := func(owner string) dockercmd.NetworkInspectResponse {
		return dockercmd.NetworkInspectResponse{
			ID:      owner + "-network-id",
			Name:    sessionNetworkName(owner),
			Created: now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
			Labels: map[string]string{
				model.NetworkLabelManaged:     "true",
				model.NetworkLabelContainer:   owner,
				model.NetworkLabelProjectHash: "project",
			},
		}
	}
	networks := []dockercmd.NetworkInspectResponse{makeNetwork("gone"), makeNetwork("exists")}
	networkList = func(context.Context, dockercmd.Filters) ([]dockercmd.NetworkInspectResponse, error) {
		return networks, nil
	}
	sessionContainerExists = func(_ context.Context, owner string) (bool, error) { return owner == "exists", nil }
	sessionRuntimeExists = func(context.Context, string) (bool, error) { return false, nil }
	networkInspect = func(_ context.Context, name string) (dockercmd.NetworkInspectResponse, error) {
		for _, info := range networks {
			if info.Name == name {
				return info, nil
			}
		}
		return dockercmd.NetworkInspectResponse{}, errors.New("unexpected network")
	}
	var removed []string
	networkRemove = func(_ context.Context, name string) error { removed = append(removed, name); return nil }

	New(Options{}).gcSessionNetworks(context.Background(), now)
	if !reflect.DeepEqual(removed, []string{"gone-network-id"}) {
		t.Fatalf("removed networks = %v", removed)
	}
}

func TestGCSessionNetworksSkipsReplacementNetwork(t *testing.T) {
	restoreNetworkGlobals(t)
	now := time.Now().UTC()
	owner := "session"
	oldNetwork := dockercmd.NetworkInspectResponse{
		ID:      "old-network-id",
		Name:    sessionNetworkName(owner),
		Created: now.Add(-2 * sessionNetworkGCGracePeriod).Format(time.RFC3339Nano),
		Labels: map[string]string{
			model.NetworkLabelManaged:     "true",
			model.NetworkLabelContainer:   owner,
			model.NetworkLabelProjectHash: "project",
		},
	}
	newNetwork := oldNetwork
	newNetwork.ID = "new-network-id"
	newNetwork.Created = now.Format(time.RFC3339Nano)
	networkList = func(context.Context, dockercmd.Filters) ([]dockercmd.NetworkInspectResponse, error) {
		return []dockercmd.NetworkInspectResponse{oldNetwork}, nil
	}
	networkInspect = func(context.Context, string) (dockercmd.NetworkInspectResponse, error) {
		return newNetwork, nil
	}
	sessionContainerExists = func(context.Context, string) (bool, error) { return false, nil }
	sessionRuntimeExists = func(context.Context, string) (bool, error) { return false, nil }
	removed := false
	networkRemove = func(context.Context, string) error { removed = true; return nil }

	New(Options{}).gcSessionNetworks(context.Background(), now)
	if removed {
		t.Fatal("GC removed a replacement network with a different immutable ID")
	}
}

func TestCreateSessionNetworkPodmanOptions(t *testing.T) {
	restoreNetworkGlobals(t)
	usePodmanCLI(t)
	var got []dockercmd.NetworkCreateOptions
	networkCreate = func(_ context.Context, opts dockercmd.NetworkCreateOptions) (string, error) {
		got = append(got, opts)
		return "network-id", nil
	}
	meta := backend.SessionMeta{Name: "session", ProjectHash: "project"}
	sysInfo := dockercmd.SystemInfo{ServerVersion: "29.0.0", NetworkBackend: "netavark", NetworkBackendVersion: "netavark 1.7.0"}
	if _, err := createSessionNetwork(context.Background(), meta, sysInfo); err != nil {
		t.Fatalf("createSessionNetwork() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("network create calls = %d, want 1", len(got))
	}
	want := map[string]string{podmanIsolateOption: podmanIsolateStrict}
	if !reflect.DeepEqual(got[0].Options, want) {
		t.Fatalf("podman bridge options = %v, want %v (netavark rejects Docker's option keys)", got[0].Options, want)
	}
	if got[0].Subnet != "" {
		t.Fatalf("podman network requested Docker's dynamic prefix %q", got[0].Subnet)
	}
	if got[0].Driver != "bridge" || got[0].EnableIPv6 == nil || *got[0].EnableIPv6 {
		t.Fatalf("unexpected bridge options: %+v", got[0])
	}
}

func TestCreateSessionNetworkRejectsPodmanCNI(t *testing.T) {
	restoreNetworkGlobals(t)
	usePodmanCLI(t)
	networkCreate = func(context.Context, dockercmd.NetworkCreateOptions) (string, error) {
		t.Fatal("network create must not run for an unsupported podman backend")
		return "", nil
	}

	_, err := createSessionNetwork(context.Background(), backend.SessionMeta{Name: "session"}, dockercmd.SystemInfo{NetworkBackend: "cni"})
	if err == nil || !strings.Contains(err.Error(), "requires netavark") {
		t.Fatalf("createSessionNetwork() error = %v, want netavark requirement", err)
	}
}

// netavark parses the isolate value when a container attaches, not when the
// network is created, so an unsupported isolate=strict yields a network that
// creates cleanly and then fails every session that attaches to it. The value
// has to be chosen from the backend version up front.
func TestCreateSessionNetworkPicksIsolateByNetavarkVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		want    string
	}{
		{name: "netavark 1.4 predates strict", version: "netavark 1.4.0", want: podmanIsolateOptedInOnly},
		{name: "netavark 1.6 predates strict", version: "netavark 1.6.9", want: podmanIsolateOptedInOnly},
		{name: "netavark 1.7 added strict", version: "netavark 1.7.0", want: podmanIsolateStrict},
		{name: "netavark 2.x keeps strict", version: "netavark 2.1.0", want: podmanIsolateStrict},
		{name: "unknown version stays safe", version: "", want: podmanIsolateOptedInOnly},
		{name: "unparsable version stays safe", version: "netavark unknown", want: podmanIsolateOptedInOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreNetworkGlobals(t)
			usePodmanCLI(t)
			var got []dockercmd.NetworkCreateOptions
			networkCreate = func(_ context.Context, opts dockercmd.NetworkCreateOptions) (string, error) {
				got = append(got, opts)
				return "network-id", nil
			}
			meta := backend.SessionMeta{Name: "session", ProjectHash: "project"}
			if _, err := createSessionNetwork(context.Background(), meta, dockercmd.SystemInfo{NetworkBackend: "netavark", NetworkBackendVersion: tc.version}); err != nil {
				t.Fatalf("createSessionNetwork() error = %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("network create calls = %d, want 1", len(got))
			}
			if isolate := got[0].Options[podmanIsolateOption]; isolate != tc.want {
				t.Fatalf("isolate for %q = %q, want %q", tc.version, isolate, tc.want)
			}
		})
	}
}

func TestCreateSessionNetworkFallsBackToOptedInIsolation(t *testing.T) {
	restoreNetworkGlobals(t)
	// podman answers a create with the network's name, so the create path
	// inspects it afterwards to learn the real ID.
	logPath := stubCLI(t, "podman", `case "$*" in
*"network inspect"*)
	printf '%s\n' '{"name":"session-net","id":"network-id","labels":{}}'
	;;
*"isolate=strict"*)
	printf '%s\n' 'Error: strconv.ParseBool: parsing "strict": invalid syntax' >&2
	exit 125
	;;
*)
	printf '%s\n' 'session-net'
	;;
esac
`)
	networkCreate = dockercmd.NetworkCreate

	// A netavark that reports strict support but still rejects it at creation
	// must fall back rather than fail the session.
	sysInfo := dockercmd.SystemInfo{NetworkBackend: "netavark", NetworkBackendVersion: "netavark 1.7.0"}
	id, err := createSessionNetwork(context.Background(), backend.SessionMeta{Name: "session", ProjectHash: "project"}, sysInfo)
	if err != nil || id != "network-id" {
		t.Fatalf("createSessionNetwork() = %q, %v", id, err)
	}
	calls := stubCalls(t, logPath)
	var creates []string
	for _, call := range calls {
		if strings.Contains(call, "network create") {
			creates = append(creates, call)
		}
	}
	if len(creates) != 2 || !strings.Contains(creates[0], "--opt isolate=strict") || !strings.Contains(creates[1], "--opt isolate=true") {
		t.Fatalf("expected a strict attempt followed by an isolate=true retry, got %v", calls)
	}
	for _, call := range calls {
		if strings.Contains(call, hostBindingIPv4Option) || strings.Contains(call, "--subnet") {
			t.Fatalf("podman network create carried a Docker-only argument: %s", call)
		}
	}
}

func TestPruneStaleSessionNetworks(t *testing.T) {
	restoreNetworkGlobals(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	network := dockercmd.NetworkInspectResponse{
		ID:      "gone-network-id",
		Name:    sessionNetworkName("gone"),
		Created: now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
		Labels: map[string]string{
			model.NetworkLabelManaged:     "true",
			model.NetworkLabelContainer:   "gone",
			model.NetworkLabelProjectHash: "project",
		},
	}
	networkList = func(context.Context, dockercmd.Filters) ([]dockercmd.NetworkInspectResponse, error) {
		return []dockercmd.NetworkInspectResponse{network}, nil
	}
	networkInspect = func(context.Context, string) (dockercmd.NetworkInspectResponse, error) { return network, nil }
	sessionContainerExists = func(context.Context, string) (bool, error) { return false, nil }
	sessionRuntimeExists = func(context.Context, string) (bool, error) { return false, nil }
	var removed []string
	networkRemove = func(_ context.Context, ref string) error { removed = append(removed, ref); return nil }

	names, err := PruneStaleSessionNetworks(context.Background(), now, true)
	if err != nil || !reflect.DeepEqual(names, []string{network.Name}) || len(removed) != 0 {
		t.Fatalf("dry run = %v, %v (removed %v)", names, err, removed)
	}
	names, err = PruneStaleSessionNetworks(context.Background(), now, false)
	if err != nil || !reflect.DeepEqual(names, []string{network.Name}) || !reflect.DeepEqual(removed, []string{"gone-network-id"}) {
		t.Fatalf("prune = %v, %v (removed %v)", names, err, removed)
	}
}

func TestFillSessionNetworksExposesNameAndIPv4Subnet(t *testing.T) {
	restoreNetworkGlobals(t)
	owner := "session"
	networkList = func(context.Context, dockercmd.Filters) ([]dockercmd.NetworkInspectResponse, error) {
		return []dockercmd.NetworkInspectResponse{{
			Name:   sessionNetworkName(owner),
			Labels: map[string]string{model.NetworkLabelManaged: "true", model.NetworkLabelContainer: owner},
			IPAM: dockercmd.NetworkIPAM{Config: []dockercmd.NetworkIPAMConfig{
				{Subnet: "fd00::/64"},
				{Subnet: "172.30.0.0/28"},
			}},
		}}, nil
	}
	sessions := []backend.Session{{Ref: backend.SessionRef{Name: owner}}}
	fillSessionNetworks(context.Background(), sessions)
	if sessions[0].Network == nil || sessions[0].Network.Name != sessionNetworkName(owner) || sessions[0].Network.Subnet != "172.30.0.0/28" {
		t.Fatalf("unexpected structured network: %+v", sessions[0].Network)
	}
}

func TestFillSessionNetworksReadsPodmanSubnets(t *testing.T) {
	restoreNetworkGlobals(t)
	owner := "session"
	networkList = func(context.Context, dockercmd.Filters) ([]dockercmd.NetworkInspectResponse, error) {
		return []dockercmd.NetworkInspectResponse{{
			Name:    sessionNetworkName(owner),
			Labels:  map[string]string{model.NetworkLabelManaged: "true", model.NetworkLabelContainer: owner},
			Subnets: []dockercmd.NetworkIPAMConfig{{Subnet: "10.89.3.0/24"}},
		}}, nil
	}
	sessions := []backend.Session{{Ref: backend.SessionRef{Name: owner}}}
	fillSessionNetworks(context.Background(), sessions)
	if sessions[0].Network == nil || sessions[0].Network.Subnet != "10.89.3.0/24" {
		t.Fatalf("podman subnet not exposed: %+v", sessions[0].Network)
	}
}

func TestFillSessionNetworkInspectsOnlyRequestedNetwork(t *testing.T) {
	restoreNetworkGlobals(t)
	session := backend.Session{Ref: backend.SessionRef{Name: "session"}, ProjectHash: "project"}
	inspected := ""
	networkInspect = func(_ context.Context, name string) (dockercmd.NetworkInspectResponse, error) {
		inspected = name
		return dockercmd.NetworkInspectResponse{
			Name: name,
			Labels: map[string]string{
				model.NetworkLabelManaged:     "true",
				model.NetworkLabelContainer:   session.Ref.Name,
				model.NetworkLabelProjectHash: session.ProjectHash,
			},
			IPAM: dockercmd.NetworkIPAM{Config: []dockercmd.NetworkIPAMConfig{{Subnet: "172.30.0.0/28"}}},
		}, nil
	}

	fillSessionNetwork(context.Background(), &session)
	if inspected != sessionNetworkName(session.Ref.Name) || session.Network == nil || session.Network.Subnet != "172.30.0.0/28" {
		t.Fatalf("unexpected direct network lookup: inspected=%q network=%+v", inspected, session.Network)
	}
}

func TestDockerVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{version: "29.0.0", want: true},
		{version: "v29.1.2", want: true},
		{version: "30.0.0-beta.1", want: true},
		{version: "28.9.9"},
		{version: "invalid"},
	} {
		if got := dockerVersionAtLeast(tc.version, 29, 0); got != tc.want {
			t.Fatalf("dockerVersionAtLeast(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestEnsureSessionNetworkReportsAddressPoolRemedies(t *testing.T) {
	restoreNetworkGlobals(t)
	dockerInfo = dockercmd.Info
	networkCreate = dockercmd.NetworkCreate
	networkInspect = dockercmd.NetworkInspect
	networkRemove = dockercmd.NetworkRemove

	dir := t.TempDir()
	stub := filepath.Join(dir, "docker")
	script := `#!/bin/sh
if [ "$1" = "info" ]; then
  printf '%s\n' '{"ServerVersion":"28.5.0"}'
  exit 0
fi
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then
  printf '%s\n' 'Error response from daemon: network session-net not found' >&2
  exit 1
fi
if [ "$1" = "network" ] && [ "$2" = "create" ]; then
  printf '%s\n' 'could not find an available, non-overlapping IPv4 address pool among the defaults to assign to the network' >&2
  exit 1
fi
exit 2
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write Docker stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := New(Options{}).ensureSessionNetwork(context.Background(), backend.SessionMeta{Name: "session", ProjectHash: "project"}, dockercmd.SystemInfo{ServerVersion: "28.5.0"})
	if err == nil || !strings.Contains(err.Error(), "29.0.0") || !strings.Contains(err.Error(), "default-address-pools") {
		t.Fatalf("expected both address-pool remedies, got %v", err)
	}
}

// podman's network inspect carries no endpoint map, and removal by ID skips
// podman's own in-use check, so without the container-list query a network is
// removed out from under a container that is still attached to it.
func TestRemoveOwnedSessionNetworkKeepsPodmanNetworkWithAttachedPeer(t *testing.T) {
	restoreNetworkGlobals(t)
	usePodmanCLI(t)
	ref := sessionNetworkRef{Name: "session-net", ID: "network-id", Container: "session", ProjectHash: "project"}
	// The inspect payload is what podman actually returns: labels, no
	// Containers map, whatever is attached.
	networkInspect = func(_ context.Context, _ string) (dockercmd.NetworkInspectResponse, error) {
		return dockercmd.NetworkInspectResponse{
			Name:   "session-net",
			ID:     "network-id",
			Labels: sessionNetworkLabels(backend.SessionMeta{Name: "session", ProjectHash: "project"}),
		}, nil
	}
	networkAttachedContainers = func(_ context.Context, name string) ([]string, error) {
		if name != "session-net" {
			t.Fatalf("queried attachments for %q, want session-net", name)
		}
		return []string{"peer"}, nil
	}
	sessionRuntimeExists = func(context.Context, string) (bool, error) { return false, nil }
	removed := false
	networkRemove = func(context.Context, string) error {
		removed = true
		return nil
	}

	if err := removeOwnedSessionNetwork(context.Background(), ref, 0); err != nil {
		t.Fatalf("removeOwnedSessionNetwork() error = %v", err)
	}
	if removed {
		t.Fatal("removed a per-session network that still had an attached container")
	}
}

// A failed attachment query must not read as "no endpoints": that would make
// an unreachable engine look like a collectable network.
func TestSessionNetworkEndpointQueryFailurePropagates(t *testing.T) {
	restoreNetworkGlobals(t)
	usePodmanCLI(t)
	networkAttachedContainers = func(context.Context, string) ([]string, error) {
		return nil, errors.New("engine unavailable")
	}
	hasEndpoints, err := sessionNetworkHasEndpoints(context.Background(), dockercmd.NetworkInspectResponse{Name: "session-net"})
	if err == nil {
		t.Fatal("expected the query failure to surface")
	}
	if hasEndpoints {
		t.Fatal("a failed query must not claim endpoints exist")
	}
}

// podman's --rm teardown outlives the CLI that returned, so removal has to
// wait for this session's own containers instead of giving up and leaking the
// network on every ordinary foreground session.
func TestRemoveOwnedSessionNetworkWaitsOutPodmanTeardown(t *testing.T) {
	restoreNetworkGlobals(t)
	usePodmanCLI(t)
	ref := sessionNetworkRef{Name: "session-net", ID: "network-id", Container: "session"}
	networkInspect = func(context.Context, string) (dockercmd.NetworkInspectResponse, error) {
		return dockercmd.NetworkInspectResponse{
			Name:   "session-net",
			ID:     "network-id",
			Labels: sessionNetworkLabels(backend.SessionMeta{Name: "session"}),
		}, nil
	}
	// The gateway lingers for the first few polls, then podman finishes.
	polls := 0
	networkAttachedContainers = func(context.Context, string) ([]string, error) {
		polls++
		if polls <= 3 {
			return []string{"session-gateway"}, nil
		}
		return nil, nil
	}
	sessionRuntimeExists = func(context.Context, string) (bool, error) { return false, nil }
	removed := false
	networkRemove = func(context.Context, string) error {
		removed = true
		return nil
	}

	if err := removeOwnedSessionNetwork(context.Background(), ref, sessionNetworkRemoveRetries); err != nil {
		t.Fatalf("removeOwnedSessionNetwork() error = %v", err)
	}
	if !removed {
		t.Fatalf("network leaked after podman finished its teardown (polled %d times)", polls)
	}
}

// A container attached from outside the session is not going to disappear, so
// removal must not burn the whole settling budget on it.
func TestRemoveOwnedSessionNetworkDoesNotWaitOutForeignEndpoints(t *testing.T) {
	restoreNetworkGlobals(t)
	usePodmanCLI(t)
	ref := sessionNetworkRef{Name: "session-net", ID: "network-id", Container: "session"}
	networkInspect = func(context.Context, string) (dockercmd.NetworkInspectResponse, error) {
		return dockercmd.NetworkInspectResponse{
			Name:   "session-net",
			ID:     "network-id",
			Labels: sessionNetworkLabels(backend.SessionMeta{Name: "session"}),
		}, nil
	}
	polls := 0
	networkAttachedContainers = func(context.Context, string) ([]string, error) {
		polls++
		return []string{"peer"}, nil
	}
	networkRemove = func(context.Context, string) error {
		t.Fatal("removed a network with a foreign container attached")
		return nil
	}

	if err := removeOwnedSessionNetwork(context.Background(), ref, sessionNetworkRemoveRetries); err != nil {
		t.Fatalf("removeOwnedSessionNetwork() error = %v", err)
	}
	if polls != 1 {
		t.Fatalf("polled %d times for a foreign endpoint, want 1", polls)
	}
}

// Falling back to isolate=true weakens a security guarantee the CLI reference
// promises enclave warns about, so the notice must reach a default run rather
// than hide behind --verbose.
func TestPodmanIsolateFallbackWarnsWithoutVerbose(t *testing.T) {
	usePodmanCLI(t)
	logx.SetLevel("info")

	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = writer
	value, isolateErr := podmanIsolateValue(dockercmd.SystemInfo{NetworkBackend: "netavark", NetworkBackendVersion: "netavark 1.4.0"})
	os.Stderr = original
	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	if isolateErr != nil {
		t.Fatalf("podmanIsolateValue() error = %v", isolateErr)
	}
	logged, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	if value != podmanIsolateOptedInOnly {
		t.Fatalf("isolate = %q, want %q", value, podmanIsolateOptedInOnly)
	}
	out := string(logged)
	if !strings.Contains(out, "warn") {
		t.Fatalf("degraded isolation was not warned about at default log level: %q", out)
	}
	if !strings.Contains(out, "netavark 1.4.0") {
		t.Fatalf("warning does not name the backend version: %q", out)
	}
}

func TestSessionOwnsEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
		want  bool
	}{
		{name: "session container", names: []string{"session"}, want: true},
		{name: "gateway sidecar", names: []string{"session-gateway"}, want: true},
		{name: "both", names: []string{"session", "session-gateway"}, want: true},
		{name: "foreign peer", names: []string{"peer"}, want: false},
		{name: "session plus peer", names: []string{"session", "peer"}, want: false},
		{name: "prefix lookalike", names: []string{"session-other"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionOwnsEndpoints(tc.names, "session"); got != tc.want {
				t.Fatalf("sessionOwnsEndpoints(%v) = %v, want %v", tc.names, got, tc.want)
			}
		})
	}
}

func restoreNetworkGlobals(t *testing.T) {
	t.Helper()
	origInfo := dockerInfo
	origCreate := networkCreate
	origInspect := networkInspect
	origList := networkList
	origRemove := networkRemove
	origAttached := networkAttachedContainers
	origContainerExists := sessionContainerExists
	origRuntimeExists := sessionRuntimeExists
	t.Cleanup(func() {
		dockerInfo = origInfo
		networkCreate = origCreate
		networkInspect = origInspect
		networkList = origList
		networkRemove = origRemove
		networkAttachedContainers = origAttached
		sessionContainerExists = origContainerExists
		sessionRuntimeExists = origRuntimeExists
	})
}

func copyLabels(labels map[string]string, key string, value string) map[string]string {
	copy := maps.Clone(labels)
	copy[key] = value
	return copy
}

func copyNetwork(info dockercmd.NetworkInspectResponse, mutate func(*dockercmd.NetworkInspectResponse)) dockercmd.NetworkInspectResponse {
	info.Labels = maps.Clone(info.Labels)
	mutate(&info)
	return info
}
