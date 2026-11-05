// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/peergroup"
)

const (
	// a2aMaxEventBytes bounds one SSE event; results are 256 KiB by default.
	a2aMaxEventBytes = 4 << 20
	a2aMaxBodyBytes  = 4 << 20
	// a2aFirstEventGrace bounds the wait for a task ID after the wait elapsed.
	a2aFirstEventGrace = 30 * time.Second
	a2aJournalFile     = "host-journal.json"
	a2aJournalLimit    = 100
)

var a2aRetryBackoff = time.Second

// a2aHostAccess is how host verbs act as the host principal: invocation
// credentials published through a committed revision, and hub calls through
// engine exec of `enclave-a2a-hub call`.
type a2aHostAccess interface {
	Issue(ctx context.Context, group string, lifetime time.Duration) (peergroup.InvocationCredential, error)
	Revoke(ctx context.Context, group, hash string) error
	// Call returns the response body; Close ends the call.
	Call(ctx context.Context, group, path, credential string, body []byte) (io.ReadCloser, error)
}

// a2aHostCredentials is the part of *backenddocker.PeerGroups host verbs use.
type a2aHostCredentials interface {
	IssueHostCredential(ctx context.Context, group string, lifetime time.Duration) (credential string, hash string, err error)
	RevokeHostCredential(ctx context.Context, group, hash string) error
}

// a2aHubCaller is the part of *backenddocker.Backend host verbs use.
type a2aHubCaller interface {
	HubCall(ctx context.Context, ref backend.HubRef, path string, stdin io.Reader) (io.ReadCloser, error)
}

// engineA2AHostAccess binds host access to the backend's credential and hub call APIs.
type engineA2AHostAccess struct {
	creds a2aHostCredentials
	hub   a2aHubCaller
	state a2aState
}

func newEngineA2AHostAccess(ctx context.Context, opts model.Options, state a2aState) (a2aHostAccess, error) {
	if err := checkDocker(); err != nil {
		return nil, err
	}
	lc, be, err := a2aNewLifecycle(ctx, opts, state.home)
	if err != nil {
		return nil, err
	}
	creds, ok := lc.(a2aHostCredentials)
	if !ok || be == nil {
		return nil, fmt.Errorf("backend %q cannot act as the host principal", opts.Backend)
	}
	return &engineA2AHostAccess{creds: creds, hub: be, state: state}, nil
}

func (a *engineA2AHostAccess) Issue(ctx context.Context, group string, lifetime time.Duration) (peergroup.InvocationCredential, error) {
	issuedAt := a2aNow()
	token, hash, err := a.creds.IssueHostCredential(ctx, group, lifetime)
	if err != nil {
		return peergroup.InvocationCredential{}, err
	}
	cred := peergroup.InvocationCredential{Token: token, Hash: hash, IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(lifetime)}
	if rec, err := a.state.store.Load(group); err == nil {
		for _, c := range rec.Credentials {
			if c.Hash != hash {
				continue
			}
			if at, err := peergroup.ParseTimestamp(c.ExpiresAt); err == nil {
				cred.ExpiresAt = at
			}
		}
	}
	return cred, nil
}

func (a *engineA2AHostAccess) Revoke(ctx context.Context, group, hash string) error {
	return a.creds.RevokeHostCredential(ctx, group, hash)
}

// Call sends the credential as the first stdin line, never on argv.
func (a *engineA2AHostAccess) Call(ctx context.Context, group, path, credential string, body []byte) (io.ReadCloser, error) {
	ref := backend.HubRef{Group: group}
	if rec, err := a.state.store.Load(group); err == nil {
		ref.Name = rec.HubContainer
	}
	stdin := io.MultiReader(strings.NewReader(credential+"\n"), bytes.NewReader(body))
	stream, err := a.hub.HubCall(ctx, ref, path, stdin)
	if err != nil {
		return nil, fmt.Errorf("hub call: %w", err)
	}
	return stream, nil
}

// a2aStreamItem is one decoded response element.
type a2aStreamItem struct {
	result a2a.Result
	rpcErr *a2a.Error
	err    error
}

// a2aHostClient speaks JSON-RPC to workers through the hub as the host principal.
type a2aHostClient struct {
	access     a2aHostAccess
	group      string
	credential string
	nextID     int
}

// call posts one request and delivers results until the channel closes.
func (c *a2aHostClient) call(ctx context.Context, peer, method string, params any) (<-chan a2aStreamItem, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	c.nextID++
	body, err := json.Marshal(a2a.Request{JSONRPC: a2a.JSONRPCVersion, ID: json.RawMessage(strconv.Itoa(c.nextID)), Method: method, Params: raw})
	if err != nil {
		return nil, err
	}
	stream, err := c.access.Call(ctx, c.group, a2a.PeerPathPrefix+peer, c.credential, body)
	if err != nil {
		return nil, err
	}
	items := make(chan a2aStreamItem)
	go func() {
		defer close(items)
		defer func() { _ = stream.Close() }()
		send := func(it a2aStreamItem) bool {
			select {
			case items <- it:
				return true
			case <-ctx.Done():
				return false
			}
		}
		br := bufio.NewReader(stream)
		if isJSONBody(br) {
			data, err := io.ReadAll(io.LimitReader(br, a2aMaxBodyBytes))
			if err != nil {
				send(a2aStreamItem{err: err})
				return
			}
			send(decodeA2AResponse(method, data))
			return
		}
		reader := a2a.NewSSEReader(br, a2aMaxEventBytes)
		for {
			ev, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				send(a2aStreamItem{err: err})
				return
			}
			it := decodeA2AResponse(method, ev.Data)
			if !send(it) || it.rpcErr != nil || it.err != nil {
				return
			}
		}
	}()
	return items, nil
}

// callOnce returns the first result of a non-streaming call.
func (c *a2aHostClient) callOnce(ctx context.Context, peer, method string, params any) (a2a.Result, error) {
	items, err := c.call(ctx, peer, method, params)
	if err != nil {
		return a2a.Result{}, err
	}
	for it := range items {
		switch {
		case it.err != nil:
			return a2a.Result{}, it.err
		case it.rpcErr != nil:
			return a2a.Result{}, it.rpcErr
		default:
			return it.result, nil
		}
	}
	if ctx.Err() != nil {
		return a2a.Result{}, ctx.Err()
	}
	return a2a.Result{}, errors.New("hub call returned no response")
}

// isJSONBody tells a JSON body from an SSE stream by its first byte.
func isJSONBody(br *bufio.Reader) bool {
	for {
		b, err := br.Peek(1)
		if err != nil {
			return false
		}
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			_, _ = br.ReadByte()
		default:
			return b[0] == '{'
		}
	}
}

func decodeA2AResponse(method string, data []byte) a2aStreamItem {
	var resp a2a.Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return a2aStreamItem{err: fmt.Errorf("decode hub response: %w", err)}
	}
	if resp.Error != nil {
		return a2aStreamItem{rpcErr: resp.Error}
	}
	res, err := decodeA2AResult(method, resp.Result)
	if err != nil {
		return a2aStreamItem{err: err}
	}
	return a2aStreamItem{result: res}
}

// decodeA2AResult decodes a result by method: GetTask and CancelTask answer a bare Task, the rest a StreamResponse.
func decodeA2AResult(method string, raw json.RawMessage) (a2a.Result, error) {
	switch method {
	case a2a.MethodGetTask, a2a.MethodCancelTask:
		var t a2a.Task
		if err := json.Unmarshal(raw, &t); err != nil {
			return a2a.Result{}, fmt.Errorf("decode %s result: %w", method, err)
		}
		if t.ID == "" {
			return a2a.Result{}, fmt.Errorf("decode %s result: no task id", method)
		}
		return a2a.Result{Task: &t}, nil
	}
	return a2a.DecodeResult(raw)
}

// a2aTaskOutput is the `--json` object of send and task (kind a2a-task); the
// task fields match the in-session client's result (A11).
type a2aTaskOutput struct {
	SchemaVersion string `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Group         string `json:"group"`
	Peer          string `json:"peer,omitempty"`
	Handle        string `json:"handle,omitempty"`
	State         string `json:"state,omitempty"`
	QueuePosition *int   `json:"queuePosition,omitempty"`
	MessageID     string `json:"messageId,omitempty"`
	Context       string `json:"context,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Termination   string `json:"termination,omitempty"`
	Message       string `json:"message,omitempty"`
	Error         string `json:"error,omitempty"`
	Note          string `json:"note,omitempty"`
}

// exitCode maps a result to section 10; detached sends and cancels succeed
// once the worker answered.
func (o a2aTaskOutput) exitCode(verb string, detached bool) int {
	switch {
	case o.Error != "":
		return 1
	case detached || verb == cli.A2AVerbTaskCancel:
		return 0
	case o.State == "" || o.State == string(a2a.TaskStateCompleted):
		return 0
	case a2a.TaskState(o.State).Terminal():
		return a2aExitTaskNotSuccess
	default:
		return a2aExitWaitElapsed
	}
}

const (
	a2aNoteWaitElapsed = "The wait ended; the task continues. Use `enclave a2a task get <handle>`."
	a2aNoteExpired     = "The invocation credential expired; the task continues. Use `enclave a2a task get <handle>`."
	a2aNoteUnknown     = "Outcome unknown. Run `enclave a2a task retry <messageId>` to resend it flagged as a retry."
)

// a2aTracker folds task snapshots and stream events into the latest state.
type a2aTracker struct {
	peer, generation, messageID string

	taskID, contextID   string
	state               a2a.TaskState
	queuePosition       *int
	reason, termination string
	statusText          string
	artifacts           []string
	messageOnly         bool
}

func (t *a2aTracker) apply(r a2a.Result) {
	switch {
	case r.Task != nil:
		t.taskID = r.Task.ID
		t.setContext(r.Task.ContextID)
		t.applyStatus(r.Task.Status)
		t.applyMeta(r.Task.Metadata)
		if len(r.Task.Artifacts) > 0 {
			t.artifacts = t.artifacts[:0]
			for _, a := range r.Task.Artifacts {
				t.artifacts = append(t.artifacts, a2aPartsText(a.Parts))
			}
		}
	case r.StatusUpdate != nil:
		if t.taskID == "" {
			t.taskID = r.StatusUpdate.TaskID
		}
		t.setContext(r.StatusUpdate.ContextID)
		t.applyStatus(r.StatusUpdate.Status)
		t.applyMeta(r.StatusUpdate.Metadata)
	case r.ArtifactUpdate != nil:
		if t.taskID == "" {
			t.taskID = r.ArtifactUpdate.TaskID
		}
		t.setContext(r.ArtifactUpdate.ContextID)
		text := a2aPartsText(r.ArtifactUpdate.Artifact.Parts)
		if r.ArtifactUpdate.Append && len(t.artifacts) > 0 {
			t.artifacts[len(t.artifacts)-1] += text
		} else {
			t.artifacts = append(t.artifacts, text)
		}
	case r.Message != nil:
		t.messageOnly = true
		t.setContext(r.Message.ContextID)
		t.statusText = a2aPartsText(r.Message.Parts)
	}
}

func (t *a2aTracker) setContext(id string) {
	if id != "" {
		t.contextID = id
	}
}

func (t *a2aTracker) applyStatus(s a2a.TaskStatus) {
	if s.State != "" {
		t.state = s.State
	}
	if s.Message != nil {
		t.statusText = a2aPartsText(s.Message.Parts)
	}
	if t.state != a2a.TaskStateSubmitted {
		t.queuePosition = nil
	}
}

func (t *a2aTracker) applyMeta(metadata map[string]any) {
	meta, ok, err := a2a.EnclaveMetaFrom(metadata)
	if !ok || err != nil {
		return
	}
	if meta.QueuePosition != nil && t.state == a2a.TaskStateSubmitted {
		pos := *meta.QueuePosition
		t.queuePosition = &pos
	}
	if meta.Reason != "" {
		t.reason = string(meta.Reason)
	}
	if meta.Termination != "" {
		t.termination = string(meta.Termination)
	}
}

func a2aPartsText(parts []a2a.Part) string {
	var texts []string
	for _, p := range parts {
		if p.Text != nil {
			texts = append(texts, *p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func (t *a2aTracker) terminal() bool { return t.state.Terminal() }

func (t *a2aTracker) handle() string {
	if t.taskID == "" {
		return ""
	}
	return a2a.TaskHandle{Worker: t.peer, Generation: t.generation, TaskID: t.taskID}.String()
}

func (t *a2aTracker) output(group, note string) a2aTaskOutput {
	out := a2aTaskOutput{
		SchemaVersion: a2aSchemaVersion, Kind: a2aKindTask, Group: group, Peer: t.peer,
		Handle: t.handle(), State: string(t.state), MessageID: t.messageID,
		Reason: t.reason, Termination: t.termination, Note: note,
	}
	if t.contextID != "" {
		out.Context = a2a.ContextHandle{Worker: t.peer, Generation: t.generation, ContextID: t.contextID}.String()
	}
	if t.state == a2a.TaskStateSubmitted && t.queuePosition != nil {
		pos := *t.queuePosition
		out.QueuePosition = &pos
	}
	if t.terminal() || t.messageOnly {
		var texts []string
		for _, a := range t.artifacts {
			if a != "" {
				texts = append(texts, a)
			}
		}
		out.Message = strings.Join(texts, "\n")
		if out.Message == "" {
			out.Message = t.statusText
		}
	}
	return out
}

func (t *a2aTracker) outputWithError(group string, err error) a2aTaskOutput {
	out := t.output(group, "")
	out.Error, out.Note = describeA2AError(err)
	return out
}

// describeA2AError renders hub and adapter errors with their code and reason.
func describeA2AError(err error) (text, note string) {
	var rpcErr *a2a.Error
	if !errors.As(err, &rpcErr) {
		return err.Error(), ""
	}
	text = fmt.Sprintf("%s [%d", rpcErr.Message, rpcErr.Code)
	if info, ok := rpcErr.Info(); ok {
		text += " " + info.Reason
	}
	text += "]"
	switch rpcErr.Code {
	case a2a.CodeTaskNotFound:
		note = "Unknown task: handles authorize nothing, only tasks of this group's host principal are visible."
	case a2a.CodeMessageIDConflict:
		note = "This message ID was used with other content."
	case a2a.CodeRetryAmbiguous:
		note = "The worker no longer knows the original; check its work before sending again."
	}
	return text, note
}

// a2aFollow reads task events until a terminal state, the deadline or ctx.
// A nil items channel starts by subscribing. Dropping a stream never cancels.
// resend, when set, re-posts a send the adapter refused with -31019.
func (c *a2aHostClient) follow(ctx context.Context, t *a2aTracker, items <-chan a2aStreamItem, wait time.Duration, resend func() (<-chan a2aStreamItem, error)) (a2aTaskOutput, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	var grace <-chan time.Time
	expired := false
	var lastErr error
	for {
		if items == nil {
			if t.terminal() {
				return t.output(c.group, ""), nil
			}
			if expired {
				return t.output(c.group, a2aNoteWaitElapsed), nil
			}
			if lastErr != nil {
				select {
				case <-ctx.Done():
					return a2aTaskOutput{}, ctx.Err()
				case <-deadline.C:
					expired = true
					continue
				case <-time.After(a2aRetryBackoff):
				}
			}
			next, err := c.call(ctx, t.peer, a2a.MethodSubscribeToTask, a2a.SubscribeToTaskRequest{ID: t.taskID})
			if ctx.Err() != nil {
				return a2aTaskOutput{}, ctx.Err()
			}
			if err != nil {
				lastErr = err
				continue
			}
			items = next
			continue
		}
		select {
		case <-ctx.Done():
			return a2aTaskOutput{}, ctx.Err()
		case <-deadline.C:
			if t.taskID != "" {
				return t.output(c.group, a2aNoteWaitElapsed), nil
			}
			expired = true
			grace = time.After(a2aFirstEventGrace)
		case <-grace:
			return c.sendFailure(t, errors.New("no task ID from the worker in time")), nil
		case it, ok := <-items:
			if !ok || it.err != nil || it.rpcErr != nil {
				items = nil
				if ctx.Err() != nil {
					return a2aTaskOutput{}, ctx.Err()
				}
			}
			switch {
			case it.rpcErr != nil && t.taskID == "" && it.rpcErr.Code == a2a.CodeEpochUnconfirmed && resend != nil && !expired:
				// Nothing was admitted; resend unchanged (CR-F1-3).
				select {
				case <-ctx.Done():
					return a2aTaskOutput{}, ctx.Err()
				case <-time.After(a2aRetryBackoff):
				}
				next, err := resend()
				if err != nil {
					return c.sendFailure(t, err), nil
				}
				items = next
				continue
			case it.rpcErr != nil && t.taskID == "":
				out := t.outputWithError(c.group, it.rpcErr)
				return out, nil
			case it.rpcErr != nil && it.rpcErr.Code == a2a.CodeUnsupportedOperation:
				// SubscribeToTask on a terminal task; GetTask has the outcome.
				got, err := c.callOnce(ctx, t.peer, a2a.MethodGetTask, a2a.GetTaskRequest{ID: t.taskID})
				if ctx.Err() != nil {
					return a2aTaskOutput{}, ctx.Err()
				}
				if err != nil {
					var rpcErr *a2a.Error
					if errors.As(err, &rpcErr) {
						return t.outputWithError(c.group, rpcErr), nil
					}
					lastErr = err
					continue
				}
				t.apply(got)
				continue
			case it.rpcErr != nil && it.rpcErr.Code == a2a.CodeDeliveryRevoked:
				return t.output(c.group, a2aNoteExpired), nil
			case it.rpcErr != nil:
				return t.outputWithError(c.group, it.rpcErr), nil
			case !ok || it.err != nil:
				if t.taskID == "" && !t.messageOnly {
					err := it.err
					if err == nil {
						err = errors.New("stream ended before the task ID")
					}
					return c.sendFailure(t, err), nil
				}
				lastErr = firstErr(it.err, errors.New("stream ended"))
				continue
			}
			lastErr = nil
			first := t.taskID == ""
			t.apply(it.result)
			if t.messageOnly || t.terminal() {
				return t.output(c.group, ""), nil
			}
			if first && expired && t.taskID != "" {
				return t.output(c.group, a2aNoteWaitElapsed), nil
			}
		}
	}
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *a2aHostClient) sendFailure(t *a2aTracker, err error) a2aTaskOutput {
	out := t.output(c.group, "")
	var rpcErr *a2a.Error
	if errors.As(err, &rpcErr) {
		out.Error, out.Note = describeA2AError(rpcErr)
		return out
	}
	out.Error = err.Error()
	out.Note = a2aNoteUnknown
	return out
}

// a2aHostJournal remembers host sends so `task retry` can resend one with
// its original message ID and content (CR-A13-4). It lives in host-secrets/,
// which goes with the group generation.
type a2aHostJournal struct {
	SchemaVersion   string                `json:"schemaVersion"`
	Group           string                `json:"group"`
	GroupGeneration string                `json:"groupGeneration"`
	Entries         []a2aHostJournalEntry `json:"entries"`
}

type a2aHostJournalEntry struct {
	MessageID      string `json:"messageId"`
	Peer           string `json:"peer"`
	PeerGeneration string `json:"peerGeneration"`
	Context        string `json:"context,omitempty"`
	Text           string `json:"text"`
	Handle         string `json:"handle,omitempty"`
	State          string `json:"state,omitempty"`
	SentAt         string `json:"sentAt"`
	UpdatedAt      string `json:"updatedAt"`
}

func a2aJournalPath(stateRoot, group string) string {
	return filepath.Join(filepath.Dir(peergroup.OutboundTokensPath(stateRoot, group)), a2aJournalFile)
}

// updateJournal applies change to the journal of rec's generation under the group lock.
func (s a2aState) updateJournal(ctx context.Context, group string, change func(*a2aHostJournal)) error {
	unlock, err := s.store.Lock(ctx, group)
	if err != nil {
		return fmt.Errorf("lock peer group %s: %w", group, err)
	}
	defer unlock()
	rec, err := s.store.Load(group)
	if err != nil {
		return err
	}
	j := s.readJournal(group, rec.GroupGeneration)
	change(j)
	if len(j.Entries) > a2aJournalLimit {
		j.Entries = j.Entries[len(j.Entries)-a2aJournalLimit:]
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic0600(a2aJournalPath(s.stateRoot, group), data)
}

// readJournal returns the journal, or an empty one for another group generation.
func (s a2aState) readJournal(group string, generation peergroup.Generation) *a2aHostJournal {
	empty := &a2aHostJournal{SchemaVersion: a2aSchemaVersion, Group: group, GroupGeneration: string(generation)}
	data, err := os.ReadFile(a2aJournalPath(s.stateRoot, group))
	if err != nil {
		return empty
	}
	var j a2aHostJournal
	if json.Unmarshal(data, &j) != nil || j.SchemaVersion != a2aSchemaVersion || j.Group != group || j.GroupGeneration != string(generation) {
		return empty
	}
	return &j
}

func writeFileAtomic0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path) // #nosec G703 -- path is the journal under the host state root.
}

func (j *a2aHostJournal) find(ref string) (a2aHostJournalEntry, bool) {
	for i := len(j.Entries) - 1; i >= 0; i-- {
		if e := j.Entries[i]; e.Handle == ref || e.MessageID == ref {
			return e, true
		}
	}
	return a2aHostJournalEntry{}, false
}

func (j *a2aHostJournal) record(messageID string, out a2aTaskOutput, now string) {
	for i := range j.Entries {
		if j.Entries[i].MessageID != messageID {
			continue
		}
		if out.Handle != "" {
			j.Entries[i].Handle = out.Handle
		}
		if out.State != "" {
			j.Entries[i].State = out.State
		}
		j.Entries[i].UpdatedAt = now
		return
	}
}

func newA2AMessageID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// a2aHostOp is one host verb after its local checks.
type a2aHostOp struct {
	req      *cli.A2ARequest
	group    string
	lifetime time.Duration
	wait     time.Duration
}

// runA2AHostVerb runs send and task verbs as the host principal: issue a
// credential, call the worker through the hub, revoke the credential.
func runA2AHostVerb(ctx context.Context, opts model.Options, req *cli.A2ARequest, state a2aState, group string) int {
	op, err := planA2AHostOp(opts, req, group)
	if err != nil {
		return a2aFail(req, group, err)
	}
	var t *a2aTracker
	var entry a2aHostJournalEntry
	rec, err := state.store.Load(group)
	if errors.Is(err, peergroup.ErrGroupNotFound) {
		err = fmt.Errorf("peer group %s does not exist", group)
	}
	if err != nil {
		return a2aFail(req, group, err)
	}
	switch req.Verb {
	case cli.A2AVerbSend:
		text, err := a2aSendText(req.Text, opts)
		if err != nil {
			return a2aFail(req, group, err)
		}
		gen, err := a2aWorkerGeneration(state, rec, req.Peer)
		if err != nil {
			return a2aFail(req, group, err)
		}
		if req.Context != "" {
			h, err := a2a.ParseContextHandle(req.Context)
			if err != nil {
				return a2aFail(req, group, errors.New("--context must be a ctx: handle from an earlier result"))
			}
			if h.Worker != req.Peer || h.Generation != gen {
				return a2aFail(req, group, fmt.Errorf("context %s does not belong to the current %s", req.Context, req.Peer))
			}
		}
		now := peergroup.FormatTimestamp(a2aNow())
		entry = a2aHostJournalEntry{MessageID: newA2AMessageID(), Peer: req.Peer, PeerGeneration: gen, Context: req.Context, Text: text, SentAt: now, UpdatedAt: now}
		if err := state.updateJournal(ctx, group, func(j *a2aHostJournal) { j.Entries = append(j.Entries, entry) }); err != nil {
			return a2aFail(req, group, fmt.Errorf("record host send: %w", err))
		}
		t = &a2aTracker{peer: req.Peer, generation: gen, messageID: entry.MessageID}
	case cli.A2AVerbTaskRetry:
		found, ok := state.readJournal(group, rec.GroupGeneration).find(req.Handle)
		if !ok {
			return a2aFail(req, group, fmt.Errorf("no host send recorded for %s in this group generation", req.Handle))
		}
		entry = found
		if err := a2aCheckWorkerGeneration(state, rec, entry.Peer, entry.PeerGeneration, req.Handle); err != nil {
			return a2aFail(req, group, err)
		}
		t = &a2aTracker{peer: entry.Peer, generation: entry.PeerGeneration, messageID: entry.MessageID}
	default:
		h, err := a2a.ParseTaskHandle(req.Handle)
		if err != nil {
			return a2aFail(req, group, fmt.Errorf("%q is not a task handle (task:<worker>:<generation>:<id>)", req.Handle))
		}
		if err := a2aCheckWorkerGeneration(state, rec, h.Worker, h.Generation, req.Handle); err != nil {
			return a2aFail(req, group, err)
		}
		t = &a2aTracker{peer: h.Worker, generation: h.Generation, taskID: h.TaskID}
		if e, ok := state.readJournal(group, rec.GroupGeneration).find(req.Handle); ok {
			t.messageID = e.MessageID
		}
	}

	access, err := a2aNewHostAccess(ctx, opts, state)
	if err != nil {
		return a2aFail(req, group, err)
	}
	cred, err := access.Issue(ctx, group, op.lifetime)
	if err != nil {
		return a2aFail(req, group, err)
	}
	defer func() {
		// Revocation must not depend on the interrupted invocation's context.
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := access.Revoke(revokeCtx, group, cred.Hash); err != nil {
			logx.Warnf("revoke host invocation credential: %v (it expires at %s)", err, peergroup.FormatTimestamp(cred.ExpiresAt))
		}
	}()
	client := &a2aHostClient{access: access, group: group, credential: cred.Token}
	// Ending the calls closes streams still open when the verb returns.
	callCtx, cancelCalls := context.WithCancel(ctx)
	out, err := op.run(callCtx, client, t, entry)
	cancelCalls()
	if t.messageID != "" && (req.Verb == cli.A2AVerbSend || req.Verb == cli.A2AVerbTaskRetry || out.Handle != "") {
		journalErr := state.updateJournal(context.WithoutCancel(ctx), group, func(j *a2aHostJournal) {
			j.record(t.messageID, out, peergroup.FormatTimestamp(a2aNow()))
		})
		if journalErr != nil {
			logx.Debugf("update host send journal: %v", journalErr)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			if h := t.handle(); h != "" {
				logx.Infof("Interrupted; the task continues: %s", h)
			}
			return backend.ExitCodeInterrupted
		}
		return a2aFail(req, group, err)
	}
	if err := writeA2ATask(a2aStdout, out, req.JSON); err != nil {
		logx.Errorf("write result: %v", err)
		return 1
	}
	if out.Error != "" && !req.JSON {
		logx.Errorf("%s", out.Error)
	}
	return out.exitCode(req.Verb, req.Detach)
}

// planA2AHostOp derives the wait and the credential lifetime: the operation
// bound plus a minute, capped at a2a_credential_lifetime (CR-P3-3).
func planA2AHostOp(opts model.Options, req *cli.A2ARequest, group string) (a2aHostOp, error) {
	capLifetime, err := config.ParseA2ADuration(opts.A2ACredentialLifetime)
	if err != nil {
		return a2aHostOp{}, fmt.Errorf("a2a_credential_lifetime: %w", err)
	}
	capLifetime = min(capLifetime, peergroup.MaxInvocationLifetime)
	op := a2aHostOp{req: req, group: group}
	blocking := false
	switch req.Verb {
	case cli.A2AVerbSend, cli.A2AVerbTaskRetry:
		if !req.Detach {
			blocking = true
			wait, err := config.ParseA2ADuration(opts.A2AHostSendWait)
			if err != nil {
				return a2aHostOp{}, fmt.Errorf("a2a_host_send_wait: %w", err)
			}
			if req.Wait != nil {
				wait = *req.Wait
			}
			op.wait = wait
		}
	case cli.A2AVerbTaskGet:
		if req.Wait != nil && *req.Wait > 0 {
			blocking = true
			op.wait = *req.Wait
		}
	}
	if !blocking {
		op.lifetime = min(a2aNonBlockingLifetime, capLifetime)
		return op, nil
	}
	if op.wait > capLifetime-config.A2ACredentialSlack {
		return a2aHostOp{}, fmt.Errorf("wait %s exceeds a2a_credential_lifetime %s minus %s", op.wait, capLifetime, config.A2ACredentialSlack)
	}
	op.lifetime = op.wait + config.A2ACredentialSlack
	return op, nil
}

func (op a2aHostOp) run(ctx context.Context, c *a2aHostClient, t *a2aTracker, entry a2aHostJournalEntry) (a2aTaskOutput, error) {
	switch op.req.Verb {
	case cli.A2AVerbSend, cli.A2AVerbTaskRetry:
		return op.send(ctx, c, t, entry)
	case cli.A2AVerbTaskGet:
		got, err := c.callOnce(ctx, t.peer, a2a.MethodGetTask, a2a.GetTaskRequest{ID: t.taskID})
		if err != nil {
			return a2aRPCOutcome(c, t, err)
		}
		t.apply(got)
		if t.terminal() || op.wait == 0 {
			return t.output(c.group, ""), nil
		}
		return c.follow(ctx, t, nil, op.wait, nil)
	case cli.A2AVerbTaskCancel:
		got, err := c.callOnce(ctx, t.peer, a2a.MethodCancelTask, a2a.CancelTaskRequest{ID: t.taskID})
		if err != nil {
			return a2aRPCOutcome(c, t, err)
		}
		t.apply(got)
		return t.output(c.group, ""), nil
	}
	return a2aTaskOutput{}, fmt.Errorf("unsupported verb %q", op.req.Verb)
}

// a2aRPCOutcome turns a JSON-RPC error into a result and keeps other errors.
func a2aRPCOutcome(c *a2aHostClient, t *a2aTracker, err error) (a2aTaskOutput, error) {
	var rpcErr *a2a.Error
	if errors.As(err, &rpcErr) {
		return t.outputWithError(c.group, rpcErr), nil
	}
	return a2aTaskOutput{}, err
}

func (op a2aHostOp) send(ctx context.Context, c *a2aHostClient, t *a2aTracker, entry a2aHostJournalEntry) (a2aTaskOutput, error) {
	msg := a2a.Message{MessageID: entry.MessageID, Role: a2a.RoleUser, Parts: []a2a.Part{a2a.TextPart(entry.Text)}}
	if entry.Context != "" {
		h, err := a2a.ParseContextHandle(entry.Context)
		if err != nil {
			return a2aTaskOutput{}, err
		}
		msg.ContextID = h.ContextID
	}
	if op.req.Verb == cli.A2AVerbTaskRetry {
		msg.Metadata = a2a.WithEnclaveMeta(nil, a2a.EnclaveMeta{Retry: true})
	}
	if op.req.Detach {
		return op.sendDetached(ctx, c, t, msg)
	}
	post := func() (<-chan a2aStreamItem, error) {
		return c.call(ctx, t.peer, a2a.MethodSendStreamingMessage, a2a.SendMessageRequest{Message: msg})
	}
	items, err := post()
	if err != nil {
		if ctx.Err() != nil {
			return a2aTaskOutput{}, ctx.Err()
		}
		return c.sendFailure(t, err), nil
	}
	return c.follow(ctx, t, items, op.wait, post)
}

// sendDetached returns once the worker admitted the task, resending while
// the adapter answers -31019 (nothing admitted).
func (op a2aHostOp) sendDetached(ctx context.Context, c *a2aHostClient, t *a2aTracker, msg a2a.Message) (a2aTaskOutput, error) {
	req := a2a.SendMessageRequest{Message: msg, Configuration: &a2a.SendMessageConfiguration{ReturnImmediately: true}}
	until := a2aNow().Add(op.lifetime - config.A2ACredentialSlack/2)
	for {
		got, err := c.callOnce(ctx, t.peer, a2a.MethodSendMessage, req)
		if ctx.Err() != nil {
			return a2aTaskOutput{}, ctx.Err()
		}
		var rpcErr *a2a.Error
		if errors.As(err, &rpcErr) && rpcErr.Code == a2a.CodeEpochUnconfirmed && a2aNow().Before(until) {
			select {
			case <-ctx.Done():
				return a2aTaskOutput{}, ctx.Err()
			case <-time.After(a2aRetryBackoff):
			}
			continue
		}
		if err != nil {
			return c.sendFailure(t, err), nil
		}
		t.apply(got)
		return t.output(c.group, ""), nil
	}
}

// a2aSendText returns the task text; "-" reads stdin.
func a2aSendText(text string, opts model.Options) (string, error) {
	if text == "-" {
		data, err := io.ReadAll(io.LimitReader(a2aStdin, a2aMaxBodyBytes+1))
		if err != nil {
			return "", fmt.Errorf("read task text from stdin: %w", err)
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("task text is empty")
	}
	limit := int64(1 << 20)
	if n, err := config.ParseA2ASize(opts.A2AMaxRequestSize); err == nil {
		limit = n
	}
	if int64(len(text)) > limit {
		return "", fmt.Errorf("task text is %d bytes; a2a_max_request_size is %d", len(text), limit)
	}
	return text, nil
}

// a2aWorkerGeneration returns the generation of an active worker peer.
func a2aWorkerGeneration(state a2aState, rec *peergroup.GroupRecord, peer string) (string, error) {
	var names []string
	for _, m := range rec.Members {
		if m.Role != a2a.RoleWorker {
			continue
		}
		names = append(names, m.Name)
		if m.Name == peer {
			if m.State != peergroup.MemberActive {
				return "", fmt.Errorf("worker %s is %s", peer, m.State)
			}
			return string(m.Generation), nil
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("peer group %s has no workers", rec.Group)
	}
	return "", fmt.Errorf("unknown worker %q; workers: %s", peer, strings.Join(names, ", "))
}

// a2aCheckWorkerGeneration refuses a handle of another generation of the
// worker: a restarted worker never knew its tasks.
func a2aCheckWorkerGeneration(state a2aState, rec *peergroup.GroupRecord, peer, generation, handle string) error {
	current, err := a2aWorkerGeneration(state, rec, peer)
	if err != nil {
		return fmt.Errorf("%s: %w", handle, err)
	}
	if generation != current {
		return fmt.Errorf("%s belongs to another generation of worker %s (current %s), which does not know the task", handle, peer, current)
	}
	return nil
}

func writeA2ATask(w io.Writer, out a2aTaskOutput, asJSON bool) error {
	if asJSON {
		return writeA2AJSON(w, out)
	}
	var b strings.Builder
	line := func(key, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s: %s\n", key, value)
		}
	}
	line("handle", out.Handle)
	line("state", out.State)
	if out.QueuePosition != nil {
		line("queue position", strconv.Itoa(*out.QueuePosition))
	}
	line("context", out.Context)
	line("message id", out.MessageID)
	line("reason", out.Reason)
	line("termination", out.Termination)
	line("note", out.Note)
	if out.Message != "" {
		b.WriteString("\n" + strings.TrimRight(out.Message, "\n") + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
