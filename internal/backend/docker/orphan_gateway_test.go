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
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

func TestGatewaySweepsContinueAfterErrors(t *testing.T) {
	for _, failure := range []string{"stop", "inspect", "remove", "all"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("STUB_FAILURE", failure)
			script := `case "$1" in
ps)
	case " $* " in
	*' status=running '*) printf '%s\n' running1 running2 ;;
	*) printf '%s\n' orphan1 orphan2 orphan3 ;;
	esac
	;;
stop)
	case "$STUB_FAILURE:$*" in
	stop:*running1|all:*running1) echo 'stop failed' >&2; exit 1 ;;
	esac
	;;
rm)
	case "$STUB_FAILURE:$*" in
	remove:*orphan2|all:*orphan2) echo 'remove failed' >&2; exit 1 ;;
	esac
	;;
container)
	# Only the single-container recheck fails, not the initial batch listing.
	if [ "$#" = 5 ] && [ "$5" = orphan1 ]; then
		case "$STUB_FAILURE" in
		inspect|all) echo 'inspect failed' >&2; exit 1 ;;
		esac
	fi
	for id in "$@"; do
		case "$id" in
`
			var runningNames, orphanNames []string
			for i, id := range []string{"running1", "running2", "orphan1", "orphan2", "orphan3"} {
				owner := "enclave-pi-abc123abc123-" + id
				name := owner + model.GatewayContainerSuffix
				status := "exited"
				if i < 2 {
					status = "running"
					runningNames = append(runningNames, name)
				} else {
					orphanNames = append(orphanNames, name)
				}
				info := dockercmd.InspectResponse{
					ID: id, Name: "/" + name,
					Config: &dockercmd.ContainerConfig{Labels: map[string]string{
						model.GatewayLabelManaged: "true", model.GatewayLabelContainer: owner,
						model.GatewayLabelProjectHash: "abc123abc123",
					}},
					State: &dockercmd.ContainerState{Status: status, Running: i < 2,
						FinishedAt: time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)},
				}
				raw, err := json.Marshal(info)
				if err != nil {
					t.Fatal(err)
				}
				script += fmt.Sprintf("%s) printf '%%s\\n' '%s' ;;\n", id, raw)
				if i < 2 {
					info.ID, info.Name = "session-"+id, "/"+owner
					info.Config.Labels = map[string]string{model.LabelHash: "abc123abc123", model.LabelBackground: "true"}
					info.State = &dockercmd.ContainerState{Status: "exited"}
					raw, err = json.Marshal(info)
					if err != nil {
						t.Fatal(err)
					}
					script += fmt.Sprintf("%s) printf '%%s\\n' '%s' ;;\n", owner, raw)
				}
			}
			script += "esac\ndone\n;;\nesac\n"
			logPath := stubCLI(t, "docker", script)
			names, err := StopExitedSessionGateways(context.Background(), "", false)
			if err == nil {
				t.Fatal("expected sweep error")
			}
			for _, operation := range []string{"stop", "inspect", "remove"} {
				if (failure == operation || failure == "all") && !strings.Contains(err.Error(), operation+" failed") {
					t.Errorf("missing %s error in %v", operation, err)
				}
			}
			want := append(runningNames, orphanNames...)
			if failure == "inspect" || failure == "all" {
				want = slices.Delete(want, 2, 3)
			}
			if failure == "remove" || failure == "all" {
				want = slices.DeleteFunc(want, func(name string) bool { return name == orphanNames[1] })
			}
			if !slices.Equal(names, want) {
				t.Fatalf("names = %v, want %v", names, want)
			}
			calls := stubCalls(t, logPath)
			if !slices.Contains(calls, "stop --time 3 running2") || !slices.Contains(calls, "rm --volumes orphan3") {
				t.Fatalf("must process later gateways and the orphan sweep: %v", calls)
			}
		})
	}
}

func TestStopExitedSessionGatewaysReclaimsStoppedOrphans(t *testing.T) {
	for _, status := range []string{"exited", "dead", "stopped"} {
		for _, dryRun := range []bool{false, true} {
			name := status
			if dryRun {
				name += "/dry-run"
			}
			t.Run(name, func(t *testing.T) {
				logPath := stubExitedSessionGateway(t, "missing", "abc123abc123")
				t.Setenv("STUB_GATEWAY_STATUS", status)
				names, err := StopExitedSessionGateways(context.Background(), "enclave-pi-abc123abc123-", dryRun)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(names, []string{exitedSession + model.GatewayContainerSuffix}) {
					t.Fatalf("unexpected orphan list %v", names)
				}
				calls := stubCalls(t, logPath)
				if hasCallWithPrefix(calls, "stop ") || hasCallWithPrefix(calls, "rm ") == dryRun {
					t.Fatalf("unexpected lifecycle calls: %v", calls)
				}
				if !dryRun && !slices.Contains(calls, "rm --volumes gwid") {
					t.Fatalf("orphan must be removed without force and by immutable ID: %v", calls)
				}
			})
		}
	}
}

func TestStopExitedSessionGatewaysProtectsOtherGateways(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       map[string]string
		prefix    string
		wantError bool
	}{
		{name: "recent exit", env: map[string]string{"STUB_FINISHED_AT": time.Now().UTC().Format(time.RFC3339Nano)}},
		{name: "future exit", env: map[string]string{"STUB_FINISHED_AT": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}},
		{name: "missing timestamp", env: map[string]string{"STUB_FINISHED_AT": ""}},
		{name: "invalid timestamp", env: map[string]string{"STUB_FINISHED_AT": "invalid"}},
		{name: "zero timestamp", env: map[string]string{"STUB_FINISHED_AT": "0001-01-01T00:00:00Z"}},
		{name: "running orphan", env: map[string]string{"STUB_GATEWAY_STATUS": "running"}},
		{name: "created orphan", env: map[string]string{"STUB_GATEWAY_STATUS": "created"}},
		{name: "paused orphan", env: map[string]string{"STUB_GATEWAY_STATUS": "paused"}},
		{name: "existing session", env: map[string]string{"STUB_SESSION_STATUS": "exited"}},
		{name: "running session", env: map[string]string{"STUB_SESSION_STATUS": "running"}},
		{name: "foreign session", env: map[string]string{"STUB_SESSION_STATUS": "exited", "STUB_SESSION_HASH": "other"}},
		{name: "session inspection failed", env: map[string]string{"STUB_SESSION_STATUS": "inspect-error"}, wantError: true},
		{name: "unmanaged gateway", env: map[string]string{"STUB_GATEWAY_MANAGED": "false"}},
		{name: "foreign hash", env: map[string]string{"STUB_GATEWAY_HASH": "other"}},
		{name: "missing hash", env: map[string]string{"STUB_GATEWAY_HASH": ""}},
		{name: "foreign name", env: map[string]string{"STUB_GATEWAY_NAME": "foreign-gateway"}},
		{name: "different project scope", prefix: "enclave-pi-other-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := stubExitedSessionGateway(t, "missing", "abc123abc123")
			t.Setenv("STUB_GATEWAY_STATUS", "exited")
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			names, err := StopExitedSessionGateways(context.Background(), tc.prefix, false)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(names) != 0 {
				t.Fatalf("protected gateway must not be listed: %v", names)
			}
			calls := stubCalls(t, logPath)
			if hasCallWithPrefix(calls, "stop ") || hasCallWithPrefix(calls, "rm ") {
				t.Fatalf("must leave gateway untouched: %v", calls)
			}
		})
	}
}
