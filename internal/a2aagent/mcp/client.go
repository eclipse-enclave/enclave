// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package mcp is the orchestrator side of A2A: the stdio MCP server, the CLI
// fallback operations, the liveness connection and the durable send journal.
package mcp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
)

// Defaults for zero Config bounds (CR-P3-1, proposal 17).
const (
	DefaultSendWait        = 90 * time.Second
	DefaultMaxRequestBytes = 1 << 20
	// ProgressInterval is the minimum spacing of MCP progress notifications.
	ProgressInterval = 30 * time.Second
	// firstEventGrace bounds the wait for the task ID beyond the send wait.
	firstEventGrace = 30 * time.Second
	recentLimit     = 10
	reopenBackoff   = time.Second
	// unconfirmedBackoff spaces resends of a request the worker answered with -31019.
	unconfirmedBackoff = 500 * time.Millisecond
)

const (
	noteWaitElapsed = "Wait elapsed; the task continues. Use get_task with the handle."
	noteRecent      = "Last known states; get_task with a handle for the current one."
	noteUnconfirmed = "The worker has not confirmed the hub epoch yet (it or the hub just restarted). Nothing was admitted " +
		"and there is no handle; send again unchanged and without retry."
	noteUnconfirmedTask = "The worker has not confirmed the hub epoch yet (it or the hub just restarted); try again shortly."
)

// errUnconfirmed ends follow when the worker refused the send with -31019; nothing was admitted.
var errUnconfirmed = errors.New("a2a client: epoch unconfirmed")

// Options configures a Client.
type Options struct {
	Config     a2aagent.Config
	JournalDir string
	// Log receives connection state changes; nil discards them.
	Log io.Writer
	Now func() time.Time
}

// Client runs the four orchestrator operations against the hub.
type Client struct {
	cfg              a2aagent.Config
	hub              *hubClient
	journal          *Journal
	live             *liveness
	sendWait         time.Duration
	maxRequestBytes  int
	progressInterval time.Duration
	retryBackoff     time.Duration
	logf             func(format string, args ...any)
}

// New validates the orchestrator configuration and opens the journal.
func New(opts Options) (*Client, error) {
	cfg := opts.Config
	if cfg.Role != a2a.RoleOrchestrator {
		return nil, fmt.Errorf("a2a client: role %q, want %q", cfg.Role, a2a.RoleOrchestrator)
	}
	if cfg.HubHost == "" || cfg.HubPort == 0 || cfg.InboundToken == "" || cfg.Member == "" || cfg.MemberGeneration == "" {
		return nil, errors.New("a2a client: hub address, inbound token, member and member generation are required")
	}
	journal, err := OpenJournal(opts.JournalDir, JournalHeader{
		Group:            cfg.Group,
		GroupGeneration:  cfg.GroupGeneration,
		Member:           cfg.Member,
		MemberGeneration: cfg.MemberGeneration,
	}, opts.Now)
	if err != nil {
		return nil, err
	}
	logw := opts.Log
	if logw == nil {
		logw = io.Discard
	}
	c := &Client{
		cfg:              cfg,
		hub:              newHubClient(cfg.HubHost, cfg.HubPort, cfg.InboundToken),
		journal:          journal,
		sendWait:         cfg.SendWait,
		maxRequestBytes:  cfg.MaxRequestBytes,
		progressInterval: ProgressInterval,
		retryBackoff:     unconfirmedBackoff,
		logf:             func(format string, args ...any) { _, _ = fmt.Fprintf(logw, format+"\n", args...) },
	}
	if c.sendWait <= 0 {
		c.sendWait = DefaultSendWait
	}
	if c.maxRequestBytes <= 0 {
		c.maxRequestBytes = DefaultMaxRequestBytes
	}
	c.live = &liveness{hub: c.hub, log: c.logf, idle: 2 * livenessKeepalive}
	return c, nil
}

// SendWait is the configured wait bound that clamps every wait.
func (c *Client) SendWait() time.Duration { return c.sendWait }

// HoldLiveness keeps the orchestrator liveness connection open until ctx ends.
func (c *Client) HoldLiveness(ctx context.Context) { c.live.run(ctx) }

// LivenessConnected reports whether the liveness connection is currently established.
func (c *Client) LivenessConnected() bool { return c.live.connected.Load() }

// Result is the outcome of one operation, rendered as JSON for the model and the CLI.
type Result struct {
	Handle        string        `json:"handle,omitempty"`
	State         string        `json:"state,omitempty"`
	QueuePosition *int          `json:"queuePosition,omitempty"`
	MessageID     string        `json:"messageId,omitempty"`
	Context       string        `json:"context,omitempty"`
	Reason        string        `json:"reason,omitempty"`
	Termination   string        `json:"termination,omitempty"`
	Message       string        `json:"message,omitempty"`
	Error         string        `json:"error,omitempty"`
	Note          string        `json:"note,omitempty"`
	Peers         []Peer        `json:"peers,omitempty"`
	Recent        []RecentEntry `json:"recent,omitempty"`
	Candidates    []RecentEntry `json:"candidates,omitempty"`
}

// Peer is one worker in the list_peers result.
type Peer struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// RecentEntry is a journal entry as shown to the model.
type RecentEntry struct {
	Handle    string `json:"handle,omitempty"`
	Peer      string `json:"peer"`
	State     string `json:"state,omitempty"`
	MessageID string `json:"messageId"`
	Context   string `json:"context,omitempty"`
	SentAt    string `json:"sentAt"`
	Error     string `json:"error,omitempty"`
}

// IsError reports whether the result is a failure the caller should see as an error.
func (r Result) IsError() bool { return r.Error != "" }

// ExitCode maps a result to the exit codes of interfaces.md section 10.
func (r Result) ExitCode() int {
	switch {
	case r.IsError():
		return 1
	case r.State == "" || r.State == string(a2a.TaskStateCompleted):
		return 0
	case a2a.TaskState(r.State).Terminal():
		return 5
	default:
		return 4
	}
}

// ProgressFunc receives a short state line at most every ProgressInterval.
type ProgressFunc func(message string)

// ErrStopped means the caller stopped waiting; the task is never canceled for it.
var ErrStopped = errors.New("a2a client: stopped waiting")

func errorResult(format string, args ...any) Result {
	return Result{Error: fmt.Sprintf(format, args...)}
}

// ListPeers returns the workers of the group.
func (c *Client) ListPeers(ctx context.Context) (Result, error) {
	listing, err := c.hub.listPeers(ctx)
	if ctx.Err() != nil {
		return Result{}, ErrStopped
	}
	if err != nil {
		return describeError(err), nil
	}
	peers := make([]Peer, 0, len(listing.Peers))
	for _, p := range listing.Peers {
		peers = append(peers, Peer{Name: p.Name, Description: p.Description})
	}
	if len(peers) == 0 {
		return Result{Note: "No workers in this peer group."}, nil
	}
	return Result{Peers: peers}, nil
}

// SendRequest holds the send_task arguments.
type SendRequest struct {
	Peer    string
	Text    string
	Context string
	// Wait nil means the configured send wait.
	Wait      *time.Duration
	Retry     bool
	MessageID string
}

// Send journals the send, streams it to the peer and waits up to the clamped wait.
func (c *Client) Send(ctx context.Context, req SendRequest, progress ProgressFunc) (Result, error) {
	if req.Peer == "" || strings.TrimSpace(req.Text) == "" {
		return errorResult("peer and text are required"), nil
	}
	if len(req.Text) > c.maxRequestBytes {
		return errorResult("text is %d bytes; the limit is %d", len(req.Text), c.maxRequestBytes), nil
	}
	if req.MessageID != "" && !req.Retry {
		return errorResult("message_id is only used with retry: true"), nil
	}
	var ctxHandle a2a.ContextHandle
	if req.Context != "" {
		parsed, err := a2a.ParseContextHandle(req.Context)
		if err != nil {
			return errorResult("context must be a ctx: handle from an earlier result"), nil
		}
		if parsed.Worker != req.Peer {
			return errorResult("context %s belongs to peer %s", req.Context, parsed.Worker), nil
		}
		ctxHandle = parsed
	}
	listing, err := c.hub.listPeers(ctx)
	if ctx.Err() != nil {
		return Result{}, ErrStopped
	}
	if err != nil {
		return describeError(err), nil
	}
	var peer *a2a.PeerEntry
	names := make([]string, 0, len(listing.Peers))
	for i := range listing.Peers {
		names = append(names, listing.Peers[i].Name)
		if listing.Peers[i].Name == req.Peer {
			peer = &listing.Peers[i]
		}
	}
	if peer == nil {
		return errorResult("unknown peer %q; peers: %s", req.Peer, strings.Join(names, ", ")), nil
	}
	if req.Context != "" && ctxHandle.Generation != peer.Generation {
		return errorResult("context %s belongs to another generation of worker %s (current %s), which does not know it", req.Context, req.Peer, peer.Generation), nil
	}

	entry, failure, err := c.journalSend(req)
	if err != nil {
		return Result{}, err
	}
	if failure != nil {
		return *failure, nil
	}

	msg := a2a.Message{
		MessageID: entry.MessageID,
		ContextID: ctxHandle.ContextID,
		Role:      a2a.RoleUser,
		Parts:     []a2a.Part{a2a.TextPart(req.Text)},
	}
	if req.Retry {
		msg.Metadata = a2a.WithEnclaveMeta(nil, a2a.EnclaveMeta{Retry: true})
	}
	deadline := time.Now().Add(c.clamp(req.Wait))
	for {
		t := &tracker{peer: req.Peer, generation: peer.Generation, messageID: entry.MessageID}
		reqCtx, cancelReq := context.WithCancel(ctx)
		items, err := c.hub.call(reqCtx, req.Peer, a2a.MethodSendStreamingMessage, a2a.SendMessageRequest{Message: msg})
		if err != nil {
			cancelReq()
			res := c.sendFailure(t, err)
			c.journalRecord(entry.MessageID, "", res)
			return res, nil
		}
		res, err := c.follow(ctx, t, items, cancelReq, max(0, time.Until(deadline)), progress)
		if errors.Is(err, errUnconfirmed) {
			// The identical message is resent: nothing was admitted, so the ID is still unused.
			again, err := c.pauseUnconfirmed(ctx, deadline)
			if err != nil {
				return Result{}, err
			}
			if again {
				continue
			}
			res = unconfirmedResult(t.messageID)
		} else if err != nil {
			return Result{}, err
		}
		c.journalRecord(entry.MessageID, "", res)
		return res, nil
	}
}

// pauseUnconfirmed waits the resend backoff when it fits before deadline; false means give up.
func (c *Client) pauseUnconfirmed(ctx context.Context, deadline time.Time) (bool, error) {
	if time.Until(deadline) < c.retryBackoff {
		return false, nil
	}
	select {
	case <-ctx.Done():
		return false, ErrStopped
	case <-time.After(c.retryBackoff):
		return true, nil
	}
}

// callUnconfirmed runs callOnce, resending while the worker answers -31019 and deadline allows.
func (c *Client) callUnconfirmed(ctx context.Context, deadline time.Time, peer, method string, params any) (a2a.Result, error) {
	for {
		got, err := c.hub.callOnce(ctx, peer, method, params)
		if !isUnconfirmed(err) || ctx.Err() != nil {
			return got, err
		}
		again, perr := c.pauseUnconfirmed(ctx, deadline)
		if perr != nil {
			return got, perr
		}
		if !again {
			return got, err
		}
	}
}

func isUnconfirmed(err error) bool {
	var rpcErr *a2a.Error
	return errors.As(err, &rpcErr) && rpcErr.Code == a2a.CodeEpochUnconfirmed
}

// unconfirmedResult reports a send the worker never admitted, so it carries no handle.
func unconfirmedResult(messageID string) Result {
	return Result{
		MessageID: messageID,
		Error:     fmt.Sprintf("worker epoch not confirmed [%d %s]", a2a.CodeEpochUnconfirmed, a2a.RefusalEpochUnconfirmed),
		Note:      noteUnconfirmed,
	}
}

// Get returns the task's state, waiting up to wait for a terminal state; an empty handle lists recent sends.
func (c *Client) Get(ctx context.Context, handle string, wait *time.Duration, progress ProgressFunc) (Result, error) {
	if handle == "" {
		return c.recent()
	}
	h, failure := parseTaskHandle(handle)
	if failure != nil {
		return *failure, nil
	}
	if res, err := c.checkGeneration(ctx, h, handle); res != nil || err != nil {
		return derefResult(res), err
	}
	t := &tracker{peer: h.Worker, generation: h.Generation, taskID: h.TaskID}
	if entry, ok := c.journalByHandle(handle); ok {
		t.messageID = entry.MessageID
	}
	deadline := time.Now().Add(c.clamp(wait))
	got, err := c.callUnconfirmed(ctx, deadline, h.Worker, a2a.MethodGetTask, a2a.GetTaskRequest{ID: h.TaskID})
	if ctx.Err() != nil || errors.Is(err, ErrStopped) {
		return Result{}, ErrStopped
	}
	if err != nil {
		res := describeError(err)
		res.Handle = handle
		return res, nil
	}
	t.apply(got)
	d := max(0, time.Until(deadline))
	var res Result
	if t.terminal() || d == 0 {
		res = t.result("")
	} else {
		res, err = c.follow(ctx, t, nil, func() {}, d, progress)
		if err != nil {
			return Result{}, err
		}
	}
	c.journalRecord("", handle, res)
	return res, nil
}

// Cancel asks the worker to cancel the task; only this operation ever cancels.
func (c *Client) Cancel(ctx context.Context, handle string) (Result, error) {
	h, failure := parseTaskHandle(handle)
	if failure != nil {
		return *failure, nil
	}
	if res, err := c.checkGeneration(ctx, h, handle); res != nil || err != nil {
		return derefResult(res), err
	}
	t := &tracker{peer: h.Worker, generation: h.Generation, taskID: h.TaskID}
	if entry, ok := c.journalByHandle(handle); ok {
		t.messageID = entry.MessageID
	}
	got, err := c.callUnconfirmed(ctx, time.Now().Add(c.sendWait), h.Worker, a2a.MethodCancelTask, a2a.CancelTaskRequest{ID: h.TaskID})
	if ctx.Err() != nil || errors.Is(err, ErrStopped) {
		return Result{}, ErrStopped
	}
	if err != nil {
		res := describeError(err)
		res.Handle = handle
		return res, nil
	}
	t.apply(got)
	res := t.result("")
	c.journalRecord("", handle, res)
	return res, nil
}

// checkGeneration refuses a handle of another generation of its worker: the
// worker does not check it, and a restarted worker never knew the task.
func (c *Client) checkGeneration(ctx context.Context, h a2a.TaskHandle, handle string) (*Result, error) {
	listing, err := c.hub.listPeers(ctx)
	if ctx.Err() != nil {
		return nil, ErrStopped
	}
	if err != nil {
		res := describeError(err)
		res.Handle = handle
		return &res, nil
	}
	for _, p := range listing.Peers {
		if p.Name != h.Worker {
			continue
		}
		if p.Generation == h.Generation {
			return nil, nil
		}
		res := errorResult("%s belongs to another generation of worker %s (current %s), which does not know the task", handle, h.Worker, p.Generation)
		res.Handle = handle
		return &res, nil
	}
	res := errorResult("%s: worker %s is no longer in the group", handle, h.Worker)
	res.Handle = handle
	return &res, nil
}

func derefResult(res *Result) Result {
	if res == nil {
		return Result{}
	}
	return *res
}

func parseTaskHandle(handle string) (a2a.TaskHandle, *Result) {
	h, err := a2a.ParseTaskHandle(handle)
	if err != nil {
		res := errorResult("%q is not a task handle. Handles are the task:<worker>:<generation>:<id> values from send_task, not Claude task IDs. get_task without a handle lists recent ones.", handle)
		return a2a.TaskHandle{}, &res
	}
	return h, nil
}

func (c *Client) clamp(wait *time.Duration) time.Duration {
	if wait == nil {
		return c.sendWait
	}
	return max(0, min(*wait, c.sendWait))
}

// follow reads task events until a terminal state, the wait bound, or ctx ends.
// A nil items channel starts by subscribing. Dropping a stream never cancels.
func (c *Client) follow(ctx context.Context, t *tracker, items <-chan streamItem, cancelReq context.CancelFunc, wait time.Duration, progress ProgressFunc) (Result, error) {
	defer func() { cancelReq() }()
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	var grace <-chan time.Time
	var tick <-chan time.Time
	if progress != nil {
		ticker := time.NewTicker(c.progressInterval)
		defer ticker.Stop()
		tick = ticker.C
	}
	expired, pause := false, false
	var lastErr error
	for {
		if items == nil {
			if t.terminal() {
				return t.result(""), nil
			}
			if expired {
				note := noteWaitElapsed
				if lastErr != nil {
					note += " Last hub error: " + lastErr.Error()
				}
				return t.result(note), nil
			}
			if pause {
				select {
				case <-ctx.Done():
					return Result{}, ErrStopped
				case <-deadline.C:
					expired = true
					continue
				case <-time.After(reopenBackoff):
				}
			}
			pause = true
			reqCtx, cancel := context.WithCancel(ctx)
			next, err := c.hub.call(reqCtx, t.peer, a2a.MethodSubscribeToTask, a2a.SubscribeToTaskRequest{ID: t.taskID})
			if ctx.Err() != nil {
				cancel()
				return Result{}, ErrStopped
			}
			if err != nil {
				cancel()
				lastErr = err
				continue
			}
			items, cancelReq = next, cancel
			continue
		}
		select {
		case <-ctx.Done():
			return Result{}, ErrStopped
		case <-deadline.C:
			if t.taskID != "" {
				return t.result(noteWaitElapsed), nil
			}
			expired = true
			grace = time.After(firstEventGrace)
		case <-grace:
			return c.sendFailure(t, errors.New("no task ID from the worker in time")), nil
		case <-tick:
			progress(t.progressLine())
		case it, ok := <-items:
			if !ok || it.err != nil || it.rpcErr != nil {
				cancelReq()
				items = nil
				if ctx.Err() != nil {
					return Result{}, ErrStopped
				}
			}
			switch {
			case it.rpcErr != nil && t.taskID == "" && it.rpcErr.Code == a2a.CodeEpochUnconfirmed:
				return Result{}, errUnconfirmed
			case it.rpcErr != nil && t.taskID == "":
				res := describeError(it.rpcErr)
				res.MessageID = t.messageID
				return res, nil
			case it.rpcErr != nil && it.rpcErr.Code == a2a.CodeUnsupportedOperation:
				// SubscribeToTask on a terminal task; GetTask has the outcome.
				if err := c.refresh(ctx, t); err != nil {
					if ctx.Err() != nil {
						return Result{}, ErrStopped
					}
					var rpcErr *a2a.Error
					if errors.As(err, &rpcErr) && !isUnconfirmed(err) {
						return t.resultWithError("", rpcErr), nil
					}
					lastErr = err
				}
				continue
			case it.rpcErr != nil && it.rpcErr.Code == a2a.CodeEpochUnconfirmed:
				// The task exists; the worker's epoch will be confirmed again, so keep subscribing.
				lastErr = it.rpcErr
				continue
			case it.rpcErr != nil:
				return t.resultWithError("", it.rpcErr), nil
			case !ok || it.err != nil:
				if t.taskID == "" && !t.messageOnly {
					err := it.err
					if err == nil {
						err = errors.New("stream ended before the task ID")
					}
					return c.sendFailure(t, err), nil
				}
				if it.err != nil {
					lastErr = it.err
				}
				continue
			}
			pause, lastErr = false, nil
			first := t.taskID == ""
			t.apply(it.result)
			if t.messageOnly || t.terminal() {
				return t.result(""), nil
			}
			if first && t.taskID != "" {
				c.journalRecord(t.messageID, "", t.result(""))
				if expired {
					return t.result(noteWaitElapsed), nil
				}
			}
		}
	}
}

// refresh applies a GetTask snapshot of the tracked task.
func (c *Client) refresh(ctx context.Context, t *tracker) error {
	ctx, cancel := context.WithTimeout(ctx, firstEventGrace)
	defer cancel()
	got, err := c.hub.callOnce(ctx, t.peer, a2a.MethodGetTask, a2a.GetTaskRequest{ID: t.taskID})
	if err != nil {
		return err
	}
	t.apply(got)
	return nil
}

// sendFailure reports a send whose outcome is unknown; the journal keeps the message ID for a flagged retry.
func (c *Client) sendFailure(t *tracker, err error) Result {
	var rpcErr *a2a.Error
	if errors.As(err, &rpcErr) {
		res := describeError(rpcErr)
		res.MessageID = t.messageID
		return res
	}
	return Result{
		MessageID: t.messageID,
		Error:     err.Error(),
		Note:      "Outcome unknown. To retry, send again with the same peer, text and context and retry: true.",
	}
}

// describeError renders hub and adapter errors with their code and reason.
func describeError(err error) Result {
	var rpcErr *a2a.Error
	if !errors.As(err, &rpcErr) {
		return Result{Error: err.Error()}
	}
	text := fmt.Sprintf("%s [%d", rpcErr.Message, rpcErr.Code)
	if info, ok := rpcErr.Info(); ok {
		text += " " + info.Reason
	}
	text += "]"
	res := Result{Error: text}
	switch rpcErr.Code {
	case a2a.CodeTaskNotFound:
		res.Note = "Unknown task. get_task without a handle lists recent handles."
	case a2a.CodeMessageIDConflict:
		res.Note = "This message ID was used with other content; send without retry to start a new task."
	case a2a.CodeRetryAmbiguous:
		res.Note = "The worker no longer knows the original; check its work before sending again."
	case a2a.CodeEpochUnconfirmed:
		res.Note = noteUnconfirmedTask
	}
	return res
}

func newMessageID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
