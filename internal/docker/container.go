// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ContainerList returns container summaries matching opts. It lists IDs with
// `docker ps` and then batch-inspects them so callers get structured data
// (labels as maps, names as slices, a real creation timestamp) rather than the
// lossy `docker ps` table format.
func ContainerList(ctx context.Context, opts ListOptions) ([]Summary, error) {
	args := []string{"ps", "--no-trunc", "--format", "{{.ID}}"}
	if opts.All {
		args = append(args, "--all")
	}
	args = append(args, opts.Filters.flags()...)
	out, err := capture(ctx, args...)
	if err != nil {
		return nil, err
	}
	ids := splitLines(out)
	if len(ids) == 0 {
		return nil, nil
	}
	inspected, err := inspectContainers(ctx, ids)
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(inspected))
	for _, info := range inspected {
		summaries = append(summaries, summaryFromInspect(info))
	}
	return summaries, nil
}

func ContainerInspectMany(ctx context.Context, ids []string) ([]InspectResponse, error) {
	return inspectContainers(ctx, ids)
}

// ContainerInspect returns the inspect view of a single container.
func ContainerInspect(ctx context.Context, containerID string) (InspectResponse, error) {
	inspected, err := inspectContainers(ctx, []string{containerID})
	if err != nil {
		return InspectResponse{}, err
	}
	if len(inspected) == 0 {
		return InspectResponse{}, &cliError{
			args:   []string{"container", "inspect", containerID},
			stderr: "no such container: " + containerID,
			err:    fmt.Errorf("container not found"),
		}
	}
	return inspected[0], nil
}

// inspectContainers batch-inspects containers, tolerating IDs that vanished
// between listing and inspection (their objects are simply absent from stdout).
func inspectContainers(ctx context.Context, ids []string) ([]InspectResponse, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := append([]string{"container", "inspect", "--format", "{{json .}}"}, ids...)
	out, err := capture(ctx, args...)
	results := decodeInspectResponses(out)
	if err != nil && IsNotFound(err) {
		return results, nil
	}
	if len(results) == 0 && err != nil {
		return nil, err
	}
	return results, nil
}

func decodeInspectResponses(out string) []InspectResponse {
	lines := splitLines(out)
	results := make([]InspectResponse, 0, len(lines))
	for _, line := range lines {
		var info InspectResponse
		if err := json.Unmarshal([]byte(line), &info); err != nil {
			continue
		}
		results = append(results, info)
	}
	return results
}

func summaryFromInspect(info InspectResponse) Summary {
	summary := Summary{ID: info.ID}
	if name := strings.TrimSpace(info.Name); name != "" {
		summary.Names = []string{name}
	}
	if info.Config != nil {
		summary.Image = info.Config.Image
		summary.Labels = info.Config.Labels
	}
	if info.State != nil {
		summary.State = info.State.Status
	}
	if info.NetworkSettings != nil {
		summary.Ports = info.NetworkSettings.Ports
	}
	if t, err := time.Parse(time.RFC3339Nano, info.Created); err == nil {
		summary.Created = t.Unix()
	}
	return summary
}

// ContainerStop stops a container, optionally overriding the kill timeout.
func ContainerStop(ctx context.Context, containerID string, timeout *time.Duration) error {
	args := []string{"stop"}
	if timeout != nil {
		args = append(args, "--time", strconv.Itoa(int(timeout.Seconds())))
	}
	args = append(args, containerID)
	_, err := capture(ctx, args...)
	return err
}

// ContainerRemove removes a container, optionally forcing and removing its
// anonymous volumes.
func ContainerRemove(ctx context.Context, containerID string, force bool, removeVolumes bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "--force")
	}
	if removeVolumes {
		args = append(args, "--volumes")
	}
	args = append(args, containerID)
	_, err := capture(ctx, args...)
	return err
}

// ContainerSignal sends a signal to a container.
func ContainerSignal(ctx context.Context, containerID string, signal string) error {
	_, err := capture(ctx, "kill", "--signal", signal, containerID)
	return err
}

// ContainerLogsSince returns up to the last 200 combined stdout/stderr log
// lines emitted since the given time.
func ContainerLogsSince(ctx context.Context, containerID string, since time.Time) (string, error) {
	return containerLogs(ctx, containerID, "--since", strconv.FormatInt(since.Unix(), 10), "--tail", "200")
}

// ContainerLogsTail returns the last tail combined stdout/stderr log lines.
func ContainerLogsTail(ctx context.Context, containerID string, tail int) (string, error) {
	return containerLogs(ctx, containerID, "--tail", strconv.Itoa(tail))
}

func containerLogs(ctx context.Context, containerID string, extra ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	args := append([]string{"logs"}, extra...)
	args = append(args, containerID)
	// docker logs already demultiplexes stdout/stderr; merge both into one
	// buffer. exec.Cmd routes a shared writer through a single pipe, so this is
	// race-free.
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204 -- args are fixed flags plus a container reference, passed without a shell.
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		code, _ := commandExitCode(err)
		return buf.String(), &cliError{args: args, code: code, stderr: strings.TrimSpace(buf.String()), err: err}
	}
	return buf.String(), nil
}

func splitLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}
