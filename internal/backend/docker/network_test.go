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
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
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
		name string
		info dockercmd.NetworkInspectResponse
		want sessionNetworkAction
	}{
		{name: "unowned", info: dockercmd.NetworkInspectResponse{}, want: sessionNetworkConflict},
		{name: "wrong owner", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: copyLabels(owned, model.NetworkLabelContainer, "other")}, want: sessionNetworkConflict},
		{name: "wrong project", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: copyLabels(owned, model.NetworkLabelProjectHash, "other")}, want: sessionNetworkConflict},
		{name: "attached", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: owned, Containers: map[string]dockercmd.NetworkEndpoint{"id": {Name: "gateway"}}}, want: sessionNetworkAttached},
		{name: "reusable", info: dockercmd.NetworkInspectResponse{ID: "network-id", Labels: owned}, want: sessionNetworkReuse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionNetworkActionFor(tc.info, meta); got != tc.want {
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
			if _, err := createSessionNetwork(context.Background(), meta, tc.version); err != nil {
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
			_, err := New(Options{}).ensureSessionNetwork(context.Background(), meta, "")
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

	_, err := New(Options{}).ensureSessionNetwork(context.Background(), backend.SessionMeta{Name: "session", ProjectHash: "project"}, "28.5.0")
	if err == nil || !strings.Contains(err.Error(), "29.0.0") || !strings.Contains(err.Error(), "default-address-pools") {
		t.Fatalf("expected both address-pool remedies, got %v", err)
	}
}

func restoreNetworkGlobals(t *testing.T) {
	t.Helper()
	origInfo := dockerInfo
	origCreate := networkCreate
	origInspect := networkInspect
	origList := networkList
	origRemove := networkRemove
	origContainerExists := sessionContainerExists
	origRuntimeExists := sessionRuntimeExists
	t.Cleanup(func() {
		dockerInfo = origInfo
		networkCreate = origCreate
		networkInspect = origInspect
		networkList = origList
		networkRemove = origRemove
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
