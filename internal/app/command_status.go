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
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

// Snapshot is the per-session terminal snapshot emitted by `status`. The
// agent/session_id/timestamp/screen/osc_title/osc_progress fields follow the
// snapshot interface external orchestrators consume: screen text and OSC
// title are raw detection inputs; deriving a session state (idle, working,
// blocked) from them happens outside enclave.
type Snapshot struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Timestamp int64  `json:"timestamp"`
	Screen    string `json:"screen"`
	OSCTitle  string `json:"osc_title"`
	// OSCProgress is always null: tmux does not track OSC 9;4 progress, and
	// consumers treat the field as optional.
	OSCProgress *string `json:"osc_progress"`

	// enclave extras, ignored by snapshot consumers.
	SessionName string `json:"session_name,omitempty"`
	Status      string `json:"status,omitempty"`
	// Capture reports how the snapshot was obtained: "tmux", or "unavailable"
	// when the session does not run under the managed tmux session or the
	// capture failed (see Error).
	Capture string `json:"capture"`
	Error   string `json:"error,omitempty"`
}

const (
	captureTmux        = "tmux"
	captureUnavailable = "unavailable"
)

var (
	statusCheckDocker    = checkDocker
	statusBackend        = statusSelectBackend
	statusResolveProject = config.ResolveProject
	statusNow            = time.Now
)

func runStatus(opts model.Options) int {
	if err := statusCheckDocker(); err != nil {
		logx.Errorf(err.Error())
		return 1
	}

	// Like exec, status targets the project resolved from the working
	// directory by default; --all widens it to every project on the host.
	projectHash := ""
	if !opts.StatusAll {
		project, err := statusResolveProject()
		if err != nil {
			logx.Errorf("resolve project: %v", err)
			return 1
		}
		projectHash = project.Hash
	}

	be, err := statusBackend(opts)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	execer, ok := be.(backend.OutputExecer)
	if !ok {
		logx.Errorf("backend %s does not support command output capture", be.Name())
		return 1
	}

	ctx := context.Background()
	sessions, err := be.List(ctx, backend.SessionFilter{
		RunningOnly: true,
		Tool:        psToolFilter(opts),
		SessionName: psSessionFilter(opts),
		ProjectHash: projectHash,
	})
	if err != nil {
		logx.Errorf("list sessions: %v", err)
		return 1
	}

	snapshots := collectSnapshots(ctx, execer, sessions, model.DefaultStatusLines, statusNow)

	if opts.StatusJSON {
		out, err := json.MarshalIndent(snapshots, "", "  ")
		if err != nil {
			logx.Errorf("encode snapshots: %v", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}

	if len(snapshots) == 0 {
		if projectHash != "" {
			fmt.Println("No running enclave containers found for this project (use --all for all projects)")
		} else {
			fmt.Println("No running enclave containers found")
		}
		return 0
	}
	if err := renderStatusTable(os.Stdout, snapshots); err != nil {
		logx.Errorf("render status table: %v", err)
		return 1
	}
	return 0
}

func statusSelectBackend(opts model.Options) (backend.Backend, error) {
	return newListingBackend(opts)
}

// collectSnapshots captures a terminal snapshot for every session. Sessions
// not running under the managed tmux session (pre-existing containers,
// sessions without --session-monitor, images without tmux) yield a degraded entry with
// capture "unavailable" instead of failing the whole command.
func collectSnapshots(ctx context.Context, execer backend.OutputExecer, sessions []backend.Session, screenLines int, now func() time.Time) []Snapshot {
	// Each capture is two docker exec calls; run them concurrently (bounded, so
	// `--all` on a busy host doesn't spawn one goroutine per session) and write
	// each result into its own slot so no locking is needed.
	snapshots := make([]Snapshot, len(sessions))
	sem := make(chan struct{}, statusCaptureConcurrency)
	var wg sync.WaitGroup
	for i, session := range sessions {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, session backend.Session) {
			defer wg.Done()
			defer func() { <-sem }()
			snapshots[i] = captureSnapshot(ctx, execer, session, screenLines, now)
		}(i, session)
	}
	wg.Wait()
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].SessionID < snapshots[j].SessionID })
	return snapshots
}

// statusCaptureConcurrency bounds the number of in-flight snapshot captures.
const statusCaptureConcurrency = 8

// statusCaptureTimeout bounds the two docker exec calls of a single capture.
const statusCaptureTimeout = 5 * time.Second

func captureSnapshot(ctx context.Context, execer backend.OutputExecer, session backend.Session, screenLines int, now func() time.Time) Snapshot {
	snap := Snapshot{
		Agent:       session.Tool,
		SessionID:   session.Ref.Name,
		SessionName: session.Name,
		Status:      session.Status,
		Capture:     captureUnavailable,
	}
	if session.SessionMonitor && execer != nil {
		// status is built to be polled, so bound each capture: a single wedged
		// container degrades to an error entry instead of hanging the command
		// (and pinning its concurrency slot) forever.
		ctx, cancel := context.WithTimeout(ctx, statusCaptureTimeout)
		defer cancel()
		// Capture as the tmux owner: under --runtime-uid-remap the container's
		// default exec user is root, but tmux runs as the remapped agent, so a
		// default exec would miss the per-user socket.
		user := session.SessionMonitorUser
		screen, err := execer.ExecOutput(ctx, session.Ref, tmuxCommand("capture-pane", "-p", "-t", model.SessionMonitorTmuxSession), user)
		if err == nil {
			var title string
			title, err = execer.ExecOutput(ctx, session.Ref, tmuxCommand("display-message", "-p", "-t", model.SessionMonitorTmuxSession, "#{pane_title}"), user)
			if err == nil {
				snap.Screen = tailLines(screen, screenLines)
				snap.OSCTitle = strings.TrimRight(title, "\r\n")
				snap.Capture = captureTmux
			}
		}
		if err != nil {
			snap.Error = err.Error()
		}
	}
	// Stamp after the capture so the timestamp reflects the screen content.
	snap.Timestamp = now().UnixMilli()
	return snap
}

// tmuxCommand builds the in-container tmux invocation targeting the managed
// session's dedicated socket (a contract with entrypoint.sh).
func tmuxCommand(args ...string) []string {
	return append([]string{"tmux", "-L", model.SessionMonitorTmuxSocket}, args...)
}

func renderStatusTable(w io.Writer, snapshots []Snapshot) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tTOOL\tCAPTURE\tTITLE\tLAST LINE"); err != nil {
		return err
	}
	for _, snap := range snapshots {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			snap.SessionID,
			snap.Agent,
			snap.Capture,
			tableCell(snap.OSCTitle),
			tableCell(lastScreenLine(snap.Screen)),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// tailLines returns at most n trailing lines of s, preserving a trailing
// newline if present. n <= 0 returns s unchanged (full visible screen).
func tailLines(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	trailingNL := strings.HasSuffix(s, "\n")
	body := strings.TrimSuffix(s, "\n")
	lines := strings.Split(body, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := strings.Join(lines, "\n")
	if trailingNL {
		out += "\n"
	}
	return out
}

// lastScreenLine returns the bottommost non-blank screen row as a one-line
// summary for the human-readable table.
func lastScreenLine(screen string) string {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

func tableCell(value string) string {
	if value == "" {
		return "-"
	}
	// The OSC title is agent-controlled; tabs or newlines would split or wrap
	// the row and misalign the tabwriter columns, so collapse them to spaces.
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(value)
}
