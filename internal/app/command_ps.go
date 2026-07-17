// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

var (
	psCheckDocker  = checkDocker
	psSessionList  = listPSSessions
	psNow          = time.Now
	psProfilePorts = profilePublishedPorts
)

type psRow struct {
	Name        string
	Tool        string
	Directory   string
	Status      string
	Uptime      string
	Ports       string
	SessionName string
}

// psJSONEntry is the structured shape emitted by `ps --json`. Field names and
// their JSON tags are a consumer-facing contract; keep them stable.
type psJSONEntry struct {
	Name        string       `json:"name"`
	Tool        string       `json:"tool"`
	ProjectDir  string       `json:"projectDir"`
	ProjectHash string       `json:"projectHash"`
	Worktree    string       `json:"worktree"`
	Status      string       `json:"status"`
	CreatedAt   string       `json:"createdAt"`
	SessionName string       `json:"sessionName"`
	Background  bool         `json:"background"`
	Ports       []psJSONPort `json:"ports"`
}

// psJSONPort is one published port binding in the ps --json output. Consumers
// get the raw binding rather than the table's formatted string so they can
// build their own URLs.
type psJSONPort struct {
	ContainerPort string `json:"containerPort"`
	HostPort      string `json:"hostPort"`
	HostIP        string `json:"hostIP"`
	Protocol      string `json:"protocol"`
}

func runPS(opts model.Options) int {
	if err := psCheckDocker(); err != nil {
		logx.Errorf(err.Error())
		return 1
	}

	sessions, err := psSessionList(context.Background(), opts)
	if err != nil {
		logx.Errorf("list sessions: %v", err)
		return 1
	}

	if opts.PSJSON {
		if err := renderPSJSON(os.Stdout, sessions); err != nil {
			logx.Errorf("render container json: %v", err)
			return 1
		}
		return 0
	}

	rows := buildPSRowsFromSessions(sessions, psNow(), cachedProfilePorts())
	if len(rows) == 0 {
		if opts.PSAll {
			fmt.Println("No enclave containers found")
		} else {
			fmt.Println("No running enclave containers found")
		}
		return 0
	}

	if err := renderPSTable(os.Stdout, rows); err != nil {
		logx.Errorf("render container table: %v", err)
		return 1
	}
	return 0
}

func listPSSessions(ctx context.Context, opts model.Options) ([]backend.Session, error) {
	be, err := newListingBackend(opts)
	if err != nil {
		return nil, err
	}
	return be.List(ctx, psSessionFilterFor(opts))
}

// psSessionFilterFor builds the backend listing filter from the parsed ps
// options. By default only running containers are listed; --all includes
// stopped ones as well.
func psSessionFilterFor(opts model.Options) backend.SessionFilter {
	filter := backend.SessionFilter{Tool: psToolFilter(opts), SessionName: psSessionFilter(opts)}
	if opts.PSAll {
		filter.All = true
	} else {
		filter.RunningOnly = true
	}
	return filter
}

func psToolFilter(opts model.Options) string {
	if opts.Sources.Tool == model.SourceCLI {
		return strings.TrimSpace(opts.Tool)
	}
	return ""
}

func psSessionFilter(opts model.Options) string {
	if opts.Sources.SessionName == model.SourceCLI {
		return strings.TrimSpace(opts.SessionName)
	}
	return ""
}

func buildPSRowsFromSessions(sessions []backend.Session, now time.Time, portsForTool func(string) []model.PortConfig) []psRow {
	rows := make([]psRow, 0, len(sessions))
	for _, session := range sessions {
		status := strings.TrimSpace(session.Status)
		if status == "" {
			status = "running"
		}
		var declared []model.PortConfig
		if portsForTool != nil {
			declared = portsForTool(session.Tool)
		}
		// Uptime is age-since-creation, which is meaningless for a container that
		// has already exited (it would keep growing). Show a dash instead.
		uptime := "-"
		if status == "running" {
			uptime = formatPSUptime(session.CreatedAt, now)
		}
		rows = append(rows, psRow{
			Name:        session.Ref.Name,
			Tool:        session.Tool,
			Directory:   directoryDisplayName(session.Worktree, session.ProjectHash),
			Status:      status,
			Uptime:      uptime,
			Ports:       formatPSPorts(session.Ports, declared),
			SessionName: session.Name,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

func cachedProfilePorts() func(string) []model.PortConfig {
	cache := map[string][]model.PortConfig{}
	return func(tool string) []model.PortConfig {
		if ports, ok := cache[tool]; ok {
			return ports
		}
		ports := psProfilePorts(tool)
		cache[tool] = ports
		return ports
	}
}

func profilePublishedPorts(tool string) []model.PortConfig {
	paths, err := config.ResolvePaths()
	if err != nil {
		return nil
	}
	profile, err := config.LoadProfile(paths, tool)
	if err != nil {
		return nil
	}
	var ports []model.PortConfig
	for _, p := range profile.Ports {
		if p.Publish {
			ports = append(ports, p)
		}
	}
	return ports
}

func formatPSPorts(bindings []backend.PortMapping, declared []model.PortConfig) string {
	entries := make([]string, 0, len(bindings))
	seen := map[string]bool{}
	for _, binding := range bindings {
		key := binding.ContainerPort + "/" + binding.Protocol + "->" + binding.HostPort
		if seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, formatPSPortEntry(binding, declared))
	}
	if len(entries) == 0 {
		return "-"
	}
	return strings.Join(entries, ", ")
}

func formatPSPortEntry(binding backend.PortMapping, declared []model.PortConfig) string {
	if url, ok := declaredOpenURL(binding, declared); ok {
		return url
	}
	host := binding.HostIP
	if host == "" {
		host = "0.0.0.0"
	}
	return host + ":" + binding.HostPort
}

func declaredOpenURL(binding backend.PortMapping, declared []model.PortConfig) (string, bool) {
	if binding.Protocol != "" && binding.Protocol != "tcp" {
		return "", false
	}
	for _, p := range declared {
		if strconv.Itoa(p.Container) != binding.ContainerPort || p.OpenURL == "" {
			continue
		}
		return strings.ReplaceAll(p.OpenURL, model.PortHostPlaceholder, binding.HostPort), true
	}
	return "", false
}

func buildPSJSONEntries(sessions []backend.Session) []psJSONEntry {
	entries := make([]psJSONEntry, 0, len(sessions))
	for _, session := range sessions {
		status := strings.TrimSpace(session.Status)
		if status == "" {
			status = "running"
		}
		createdAt := ""
		if !session.CreatedAt.IsZero() {
			createdAt = session.CreatedAt.UTC().Format(time.RFC3339)
		}
		entries = append(entries, psJSONEntry{
			Name:        session.Ref.Name,
			Tool:        session.Tool,
			ProjectDir:  session.ProjectDir,
			ProjectHash: session.ProjectHash,
			Worktree:    session.Worktree,
			Status:      status,
			CreatedAt:   createdAt,
			SessionName: session.Name,
			Background:  session.Background,
			Ports:       buildPSJSONPorts(session.Ports),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func buildPSJSONPorts(bindings []backend.PortMapping) []psJSONPort {
	ports := make([]psJSONPort, 0, len(bindings))
	for _, binding := range bindings {
		ports = append(ports, psJSONPort{
			ContainerPort: binding.ContainerPort,
			HostPort:      binding.HostPort,
			HostIP:        binding.HostIP,
			Protocol:      binding.Protocol,
		})
	}
	return ports
}

func renderPSJSON(w io.Writer, sessions []backend.Session) error {
	entries := buildPSJSONEntries(sessions)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

func directoryDisplayName(worktreePath string, projectHash string) string {
	if worktreePath != "" {
		clean := filepath.Clean(worktreePath)
		if clean != "" && clean != "." {
			return clean
		}
	}
	if projectHash != "" {
		return projectHash
	}
	return "-"
}

func formatPSUptime(createdAt time.Time, now time.Time) string {
	if createdAt.IsZero() || !createdAt.Before(now) {
		return "<1m"
	}

	age := now.Sub(createdAt)
	switch {
	case age < time.Minute:
		return "<1m"
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		hours := int(age.Hours())
		minutes := int(age.Minutes()) % 60
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		days := int(age.Hours()) / 24
		hours := int(age.Hours()) % 24
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, hours)
	}
}

func renderPSTable(w io.Writer, rows []psRow) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tTOOL\tDIR\tSTATUS\tUPTIME\tPORTS"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Name,
			row.Tool,
			row.Directory,
			row.Status,
			row.Uptime,
			row.Ports,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}
