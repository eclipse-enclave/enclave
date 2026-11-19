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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/placeholderstore"
	"enclave/internal/util"
)

func TestProtectedStopRetainsMountsUntilSuccessfulRemoval(t *testing.T) {
	previousTimeout := endedContainerGoneTimeout
	endedContainerGoneTimeout = 100 * time.Millisecond
	t.Cleanup(func() { endedContainerGoneTimeout = previousTimeout })
	for _, tc := range []struct {
		name                            string
		ephemeral, gatewayFails, reused bool
	}{
		{name: "shared"}, {name: "ephemeral", ephemeral: true},
		{name: "gateway retained", gatewayFails: true},
		{name: "ephemeral gateway retained", ephemeral: true, gatewayFails: true},
		{name: "renamed with same-name reuse", reused: true},
		{name: "ephemeral renamed with same-name reuse", ephemeral: true, reused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			home := t.TempDir()
			paths := model.Paths{ToolsDir: filepath.Join("..", "..", "..", "extensions", "tools")}
			profile, err := config.LoadProfile(paths, "claude")
			if err != nil {
				t.Fatal(err)
			}
			key := backend.StoreKey{Owner: "claude", ProjectHash: "syntheticproject", Suffix: "named"}
			dir, err := hoststore.ResolveDir(home, key, backend.StoreKindConfig)
			if err != nil {
				t.Fatal(err)
			}
			authKey := backend.StoreKey{Owner: "claude"}
			if tc.ephemeral {
				authKey.Suffix = "ephemeral-named"
			}
			native, err := hoststore.ResolveDir(home, authKey, backend.StoreKindAuth)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{dir, native} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			credentials, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "detached-access-synthetic", "refreshToken": "detached-refresh-synthetic", "expiresAt": 4102444800000}})
			if err := os.WriteFile(filepath.Join(native, ".credentials.json"), credentials, 0600); err != nil {
				t.Fatal(err)
			}
			holder := hoststore.ModeHolder{ID: "detached", Session: "session", Tool: "claude", ProjectHash: key.ProjectHash, Protected: true, Ephemeral: tc.ephemeral, GatewayCorrelated: true, Engine: "docker"}
			if err := hoststore.ReserveMode(home, dir, holder, nil); err != nil {
				t.Fatal(err)
			}
			if err := hoststore.AdvanceMode(home, dir, holder.ID, "live", "agent-id", true); err != nil {
				t.Fatal(err)
			}
			views := filepath.Join(config.HostProjectToolDir(home, holder.ProjectHash, holder.Tool), "auth-views")
			view, err := placeholderstore.Project(native, views, holder.ID, profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			metadata, _ := json.Marshal(map[string]any{"oauthAccount": map[string]any{"displayName": "detached-final"}})
			if err := os.WriteFile(filepath.Join(view.Dir, "config.json"), metadata, 0600); err != nil {
				t.Fatal(err)
			}
			agentName := holder.Session
			if tc.reused {
				agentName = "renamed"
			}
			agent := dockercmd.InspectResponse{ID: "agent-id", Name: "/" + agentName, Config: &dockercmd.ContainerConfig{
				Labels: map[string]string{model.LabelModeHolder: holder.ID, model.LabelAgent: holder.Tool, model.LabelHash: holder.ProjectHash},
				Env:    []string{model.EnvToolConfigDir + "=/config", model.EnvAuthDir + "=/view", model.EnvNativeAuthOps + "=protected"},
			}, Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: dir, Destination: "/config"}, {Type: dockercmd.MountTypeBind, Source: view.Dir, Destination: "/view"}}, State: &dockercmd.ContainerState{Running: true}}
			sidecar := dockercmd.InspectResponse{ID: "gateway-id", Name: "/original-gateway", Config: &dockercmd.ContainerConfig{Labels: map[string]string{model.GatewayLabelManaged: "true", model.GatewayLabelModeHolder: holder.ID, model.GatewayLabelContainer: holder.Session, model.GatewayLabelProjectHash: holder.ProjectHash}}, Mounts: []dockercmd.MountPoint{{Type: dockercmd.MountTypeBind, Source: native, Destination: model.GatewayNativeAuthStorePath}}}
			agentJSON, _ := json.Marshal(agent)
			sidecarJSON, _ := json.Marshal(sidecar)
			replacement := agent
			replacement.ID = "replacement-agent"
			replacement.Config = &dockercmd.ContainerConfig{Labels: map[string]string{model.LabelModeHolder: "replacement", model.LabelHash: holder.ProjectHash}}
			replacementJSON, _ := json.Marshal(replacement)
			replacementGateway := sidecar
			replacementGateway.ID = "replacement-gateway"
			replacementGateway.Config = &dockercmd.ContainerConfig{Labels: map[string]string{model.GatewayLabelManaged: "true", model.GatewayLabelModeHolder: "replacement", model.GatewayLabelContainer: holder.Session, model.GatewayLabelProjectHash: holder.ProjectHash}}
			replacementGatewayJSON, _ := json.Marshal(replacementGateway)
			sibling := filepath.Join(views, "replacement-published")
			if tc.reused {
				if err := os.MkdirAll(sibling, 0700); err != nil {
					t.Fatal(err)
				}
				if err := hoststore.ReserveMode(home, dir, hoststore.ModeHolder{ID: "replacement", Session: holder.Session, Tool: holder.Tool, ProjectHash: holder.ProjectHash, Protected: true, Engine: "docker", OwnerPID: os.Getpid()}, nil); err != nil {
					t.Fatal(err)
				}
			}
			bin := t.TempDir()
			gone, fail := filepath.Join(bin, "agent-gone"), filepath.Join(bin, "fail-remove")
			gatewayGone, gatewayFail := filepath.Join(bin, "gateway-gone"), filepath.Join(bin, "gateway-fail")
			replacementGone := filepath.Join(bin, "replacement-gone")
			script := "#!/bin/sh\nif [ \"$1\" = container ]; then shift; fi\nlast=\nfor arg do last=\"$arg\"; done\ncase \"$1\" in\nps) case \"$*\" in *" + model.GatewayLabelModeHolder + "=" + holder.ID + "*" + model.GatewayLabelManaged + "=true*) echo gateway-id;; *) echo 'wrong gateway filters' >&2; exit 1;; esac;;\ninspect)\ncase \"$last\" in\n" + agentName + "|agent-id) if [ -e " + util.ShellQuote(gone) + " ]; then echo 'No such container' >&2; exit 1; fi; printf '%s\\n' " + util.ShellQuote(string(agentJSON)) + ";;\ngateway-id) if [ -e " + util.ShellQuote(gatewayGone) + " ]; then echo 'No such container' >&2; exit 1; fi; printf '%s\\n' " + util.ShellQuote(string(sidecarJSON)) + ";;\n"
			if tc.reused {
				script += "session|replacement-agent) printf '%s\\n' " + util.ShellQuote(string(replacementJSON)) + ";;\nsession-gateway|replacement-gateway) printf '%s\\n' " + util.ShellQuote(string(replacementGatewayJSON)) + ";;\n"
			}
			script += "*) echo 'No such container' >&2; exit 1;;\nesac;;\nstop) if [ \"$last\" = gateway-id ] && [ -e " + util.ShellQuote(gatewayFail) + " ]; then exit 1; fi;;\nrm) case \"$last\" in agent-id) if [ -e " + util.ShellQuote(fail) + " ]; then exit 1; fi; : > " + util.ShellQuote(gone) + ";; gateway-id) if [ -e " + util.ShellQuote(gatewayFail) + " ]; then exit 1; fi; : > " + util.ShellQuote(gatewayGone) + ";; session|session-gateway|replacement-agent|replacement-gateway) : > " + util.ShellQuote(replacementGone) + ";; esac;;\n*) echo 'No such network' >&2; exit 1;;\nesac\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			be := New(Options{Host: model.Host{Home: home}, Paths: paths})
			ref := backend.SessionRef{Name: agentName}
			if err := be.Stop(context.Background(), ref, backend.StopOptions{Finalize: true}); err != nil {
				t.Fatal(err)
			}
			checkRetained := func() {
				t.Helper()
				for _, path := range []string{native, view.Dir} {
					if _, err := os.Stat(path); err != nil {
						t.Fatal("stopped/failed removal lost a mounted store")
					}
				}
				if err := hoststore.WithModeHolders(home, dir, func(holders *[]hoststore.ModeHolder) error {
					for _, current := range *holders {
						if current.ID == holder.ID {
							if current.State != "finalizing" || current.OwnerPID != 0 {
								t.Fatal("protected end did not retain holder and release claim")
							}
							return nil
						}
					}
					t.Fatal("old holder was released early")
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			checkRetained()
			if _, err := os.Stat(filepath.Join(native, "config.json")); !os.IsNotExist(err) {
				t.Fatal("stop harvested before removal")
			}
			if err := os.WriteFile(fail, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := be.RemoveWithoutFinalize(context.Background(), ref); err == nil {
				t.Fatal("remove fault control did not fail")
			}
			checkRetained()
			if err := os.Remove(fail); err != nil {
				t.Fatal(err)
			}
			if tc.gatewayFails {
				if err := os.WriteFile(gatewayFail, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = be.RemoveWithoutFinalize(context.Background(), ref)
			if tc.gatewayFails {
				if err == nil {
					t.Fatal("gateway removal failure was reported as success")
				}
				checkRetained()
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(gatewayGone); err != nil {
				t.Fatal("protected end deleted state while original gateway still exists")
			}
			if _, err := os.Stat(view.Dir); !os.IsNotExist(err) {
				t.Fatal("successful removal kept own view")
			}
			records, err := hoststore.ModeRecords(home)
			if err != nil {
				t.Fatal(err)
			}
			if tc.reused {
				if len(records) != 1 || len(records[0].Holders) != 1 || records[0].Holders[0].ID != "replacement" {
					t.Fatal("same-name replacement holder was changed")
				}
				if _, err := os.Stat(sibling); err != nil {
					t.Fatal("same-name replacement view was deleted")
				}
				if _, err := os.Stat(replacementGone); !os.IsNotExist(err) {
					t.Fatal("same-name replacement agent/gateway was removed")
				}
			} else if len(records) != 0 {
				t.Fatal("successful removal kept holder")
			}
			if tc.ephemeral {
				if _, err := os.Stat(native); !os.IsNotExist(err) {
					t.Fatal("ephemeral identity survived removal")
				}
			} else {
				data, err := os.ReadFile(filepath.Join(native, "config.json"))
				if err != nil || string(data) != string(metadata) {
					t.Fatal("detached metadata was not harvested after removal")
				}
			}
		})
	}
}

func TestProtectedForegroundFinalizationRequiresCorrelatedResourcesGone(t *testing.T) {
	previousTimeout := endedContainerGoneTimeout
	endedContainerGoneTimeout = 50 * time.Millisecond
	t.Cleanup(func() { endedContainerGoneTimeout = previousTimeout })
	for _, tc := range []struct {
		name, agent, gateway                 string
		ordinary, pending, legacy, wantFinal bool
	}{
		{name: "removed", agent: "absent", gateway: "absent", wantFinal: true},
		{name: "stopped agent", agent: "stopped", gateway: "absent"},
		{name: "running agent", agent: "running", gateway: "absent"},
		{name: "gateway remains", agent: "absent", gateway: "running"},
		{name: "gateway unreachable", agent: "absent", gateway: "unreachable"},
		{name: "same-name replacement", agent: "absent", gateway: "replacement", wantFinal: true},
		{name: "pending start empty", agent: "absent", gateway: "absent", pending: true, wantFinal: true},
		{name: "legacy empty uncertain", agent: "absent", gateway: "absent", legacy: true},
		{name: "ordinary stopped unchanged", agent: "stopped", gateway: "running", ordinary: true, wantFinal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			home, bin := t.TempDir(), t.TempDir()
			key := backend.StoreKey{Owner: "claude", ProjectHash: "syntheticproject"}
			dir, err := hoststore.ResolveDir(home, key, backend.StoreKindConfig)
			if err != nil {
				t.Fatal(err)
			}
			holder := hoststore.ModeHolder{ID: "foreground", Session: "session", Tool: key.Owner, ProjectHash: key.ProjectHash, Protected: !tc.ordinary, GatewayCorrelated: !tc.legacy, Engine: "docker", OwnerPID: os.Getpid()}
			if err := hoststore.ReserveMode(home, dir, holder, nil); err != nil {
				t.Fatal(err)
			}
			if !tc.pending {
				if err := hoststore.AdvanceMode(home, dir, holder.ID, "live", "agent-id", false); err != nil {
					t.Fatal(err)
				}
			}
			request := backend.Request{Mode: &backend.SessionMode{ConfigStore: key, HolderID: holder.ID}, Session: backend.SessionMeta{Name: holder.Session, ProjectHash: holder.ProjectHash}}
			if !tc.ordinary {
				request.NativeAuth = &backend.NativeAuthSpec{}
			}
			agentResponse := "echo 'No such container' >&2; exit 1"
			if tc.agent != "absent" {
				info := dockercmd.InspectResponse{ID: "agent-id", Config: &dockercmd.ContainerConfig{Labels: map[string]string{model.LabelModeHolder: holder.ID}}, State: &dockercmd.ContainerState{Running: tc.agent == "running"}}
				data, _ := json.Marshal(info)
				agentResponse = "printf '%s\\n' " + util.ShellQuote(string(data))
			}
			sidecar := dockercmd.InspectResponse{ID: "gateway-old-id", Config: &dockercmd.ContainerConfig{Labels: map[string]string{model.GatewayLabelManaged: "true", model.GatewayLabelModeHolder: holder.ID, model.GatewayLabelContainer: holder.Session, model.GatewayLabelProjectHash: holder.ProjectHash}}, State: &dockercmd.ContainerState{Running: true}}
			gatewayJSON, _ := json.Marshal(sidecar)
			listing := "exit 0"
			switch tc.gateway {
			case "unreachable":
				listing = "echo 'daemon unavailable' >&2; exit 1"
			case "running":
				listing = "echo gateway-old-id"
			}
			log := filepath.Join(bin, "calls")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + util.ShellQuote(log) + "\nif [ \"$1\" = container ]; then shift; fi\nlast=\nfor arg do last=\"$arg\"; done\nif [ \"$1\" = ps ]; then\ncase \"$*\" in *" + model.GatewayLabelModeHolder + "=" + holder.ID + "*" + model.GatewayLabelManaged + "=true*) " + listing + ";; *" + model.LabelModeHolder + "=" + holder.ID + "*) exit 0;; *) echo 'wrong role filters' >&2; exit 1;; esac\nelse\ncase \"$last\" in\nagent-id) " + agentResponse + ";;\ngateway-old-id|session-gateway) printf '%s\\n' " + util.ShellQuote(string(gatewayJSON)) + ";;\n*) echo 'No such container' >&2; exit 1;;\nesac\nfi\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			be := New(Options{Host: model.Host{Home: home}})
			id := "agent-id"
			if tc.pending {
				id = ""
			}
			be.finalizingRequestMode(request, id)
			if err := hoststore.WithModeHolders(home, dir, func(holders *[]hoststore.ModeHolder) error {
				if len(*holders) != 1 || ((*holders)[0].State == "finalizing") != tc.wantFinal {
					t.Fatalf("state=%v wantFinal=%t", *holders, tc.wantFinal)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if tc.gateway == "replacement" {
				calls, err := os.ReadFile(log)
				if err != nil || strings.Contains(string(calls), "session-gateway") {
					t.Fatal("holder-correlated proof inspected a replacement name")
				}
			}
		})
	}
}

func TestGatewayCorrelationLookupAndRemovalFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		count                                          int
		engineError, wrongHash, wrongHolder, wantError bool
	}{
		{name: "none"}, {name: "one", count: 1},
		{name: "multiple", count: 2, wantError: true},
		{name: "unreachable", engineError: true, wantError: true},
		{name: "wrong project", count: 1, wrongHash: true, wantError: true},
		{name: "wrong holder", count: 1, wrongHolder: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			gone, log := filepath.Join(bin, "gone"), filepath.Join(bin, "calls")
			labels := map[string]string{model.GatewayLabelManaged: "true", model.GatewayLabelModeHolder: "original-holder", model.GatewayLabelContainer: "original-session", model.GatewayLabelProjectHash: "scopehash"}
			if tc.wrongHash {
				labels[model.GatewayLabelProjectHash] = "other"
			}
			if tc.wrongHolder {
				labels[model.GatewayLabelModeHolder] = "other"
			}
			info := dockercmd.InspectResponse{ID: "gateway-one", Name: "/renamed-gateway", Config: &dockercmd.ContainerConfig{Labels: labels}, State: &dockercmd.ContainerState{Running: true}}
			first, _ := json.Marshal(info)
			info.ID = "gateway-two"
			second, _ := json.Marshal(info)
			listing := "exit 0"
			if tc.engineError {
				listing = "echo 'daemon unavailable' >&2; exit 1"
			} else if tc.count == 1 {
				listing = "echo gateway-one"
			} else if tc.count == 2 {
				listing = "printf 'gateway-one\\ngateway-two\\n'"
			}
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + util.ShellQuote(log) + "\nif [ \"$1\" = container ]; then shift; fi\nlast=\nfor arg do last=\"$arg\"; done\ncase \"$1\" in\nps) " + listing + ";;\ninspect) if [ -e " + util.ShellQuote(gone) + " ]; then echo 'No such container' >&2; exit 1; fi\ncase \"$last\" in gateway-one) printf '%s\\n' " + util.ShellQuote(string(first)) + ";; gateway-two) printf '%s\\n%s\\n' " + util.ShellQuote(string(first)) + " " + util.ShellQuote(string(second)) + ";; *) echo 'No such container' >&2; exit 1;; esac;;\nstop) exit 0;;\nrm) if [ \"$last\" != gateway-one ]; then echo 'wrong removal target' >&2; exit 1; fi; : > " + util.ShellQuote(gone) + ";;\n*) echo 'No such network' >&2; exit 1;;\nesac\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			found, err := New(Options{}).RemoveModeGateway(context.Background(), "original-holder", "original-session", "scopehash")
			if (err != nil) != tc.wantError || found != (tc.count == 1 && !tc.wantError) {
				t.Fatalf("found=%t err=%v", found, err)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantError && strings.Contains(string(calls), "rm ") {
				t.Fatal("ambiguous gateway ownership allowed removal")
			}
			if !tc.wantError && tc.count == 1 {
				if _, err := os.Stat(gone); err != nil {
					t.Fatal("correlated gateway was not removed by ID")
				}
			}
		})
	}
}
