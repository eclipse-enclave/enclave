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
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	"enclave/internal/model"
	"enclave/internal/peergroup"
)

func publishedCredentials(t *testing.T, state, group string) []string {
	t.Helper()
	var m peergroup.Membership
	data, err := os.ReadFile(filepath.Join(peergroup.CurrentLinkPath(state, group), peergroup.MembershipFile))
	if err == nil {
		err = json.Unmarshal(data, &m)
	}
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, c := range m.HostPrincipal.Credentials {
		hashes = append(hashes, c.Hash)
	}
	return hashes
}

func TestHostCredentialIssueAndRevokeCommit(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	f.engine.calls = nil

	token, hash, err := f.p.IssueHostCredential(context.Background(), "review", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || hash != f.p.tokens.Hash(token) {
		t.Fatalf("credential = %q, hash = %q", token, hash)
	}
	rec := f.record()
	if rec.Committed != 2 || rec.Pending != nil || len(rec.Credentials) != 1 || rec.Credentials[0].Hash != hash {
		t.Fatalf("record after issue = %+v", rec)
	}
	if got := publishedCredentials(t, f.state, "review"); !reflect.DeepEqual(got, []string{hash}) {
		t.Fatalf("published credentials = %v", got)
	}
	// One reload per revision, no reconcile work such as member inspection.
	if got := strings.Join(f.engine.calls, ","); got != "ReloadHub" {
		t.Fatalf("engine calls = %s", got)
	}
	f.assertUnlocked()

	if err := f.p.RevokeHostCredential(context.Background(), "review", hash); err != nil {
		t.Fatal(err)
	}
	rec = f.record()
	if rec.Committed != 3 || len(rec.Credentials) != 0 || len(publishedCredentials(t, f.state, "review")) != 0 {
		t.Fatalf("record after revoke = %+v", rec)
	}
	f.engine.calls = nil
	if err := f.p.RevokeHostCredential(context.Background(), "review", hash); err != nil || len(f.engine.calls) != 0 {
		t.Fatalf("revoking a gone credential published: %v, %v", err, f.engine.calls)
	}
	if err := f.p.RevokeHostCredential(context.Background(), "missing", hash); err != nil {
		t.Fatalf("revoke in a missing group: %v", err)
	}
}

func TestHostCredentialIssueFailsWithoutCommit(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	f.p.cfg.CommitTimeout = 200 * time.Millisecond
	f.engine.noAck = true

	if _, _, err := f.p.IssueHostCredential(context.Background(), "review", 2*time.Minute); err == nil {
		t.Fatal("issue succeeded without commit")
	}
	rec := f.record()
	if len(rec.Credentials) != 0 || rec.Pending == nil || !hasObligation(rec, peergroup.ObligationSnapshotCommit) {
		t.Fatalf("record after failed issue = %+v", rec)
	}
	f.assertUnlocked()

	// The next issue replaces the uncommitted revision and settles the obligation.
	f.engine.noAck = false
	_, hash, err := f.p.IssueHostCredential(context.Background(), "review", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rec = f.record()
	if hasObligation(rec, peergroup.ObligationSnapshotCommit) || rec.Pending != nil || len(rec.Credentials) != 1 || rec.Credentials[0].Hash != hash {
		t.Fatalf("record after second issue = %+v", rec)
	}
}

func TestHostCredentialIssueRefusesStoppedHubAndExpires(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	rec := f.record()
	rec.Credentials = []peergroup.InvocationRecord{{Hash: "sha256:old", IssuedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-01-01T00:01:00Z"}}
	if err := f.p.store.Save(rec); err != nil {
		t.Fatal(err)
	}
	_, hash, err := f.p.IssueHostCredential(context.Background(), "review", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := publishedCredentials(t, f.state, "review"); !reflect.DeepEqual(got, []string{hash}) {
		t.Fatalf("expired credential still published: %v", got)
	}

	f.engine.hub.Running = false
	f.engine.calls = nil
	if _, _, err := f.p.IssueHostCredential(context.Background(), "review", time.Minute); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("issue with a stopped hub: %v", err)
	}
	if len(f.engine.calls) != 0 {
		t.Fatalf("stopped hub was signalled: %v", f.engine.calls)
	}
	// Revocation with the hub stopped publishes only; reconcile commits later.
	if err := f.p.RevokeHostCredential(context.Background(), "review", hash); err != nil {
		t.Fatal(err)
	}
	rec = f.record()
	if len(rec.Credentials) != 0 || rec.Pending == nil || len(publishedCredentials(t, f.state, "review")) != 0 {
		t.Fatalf("record after revoke with stopped hub = %+v", rec)
	}
}

func TestHubCallStreamsExecAsProxyUID(t *testing.T) {
	var gotName, gotUser string
	var gotCmd []string
	var gotStdin string
	previous := hubExecStream
	hubExecStream = func(_ context.Context, container, user string, cmd []string, stdin io.Reader) (io.ReadCloser, error) {
		gotName, gotUser, gotCmd = container, user, cmd
		data, _ := io.ReadAll(stdin)
		gotStdin = string(data)
		return io.NopCloser(strings.NewReader("ok")), nil
	}
	t.Cleanup(func() { hubExecStream = previous })
	b := New(Options{Host: model.Host{Home: t.TempDir()}})
	out, err := b.HubCall(context.Background(), backend.HubRef{Group: "review"}, "/peers/w1", strings.NewReader("cred\n{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	if gotName != "enclave-a2a-hub-review" || gotUser != "65532" || !reflect.DeepEqual(gotCmd, []string{a2aHubBinary, "call", "/peers/w1"}) || gotStdin != "cred\n{}" {
		t.Fatalf("exec %s as %s: %v, stdin %q", gotName, gotUser, gotCmd, gotStdin)
	}
	for _, bad := range []string{"peers/w1", "/peers/w 1", ""} {
		if _, err := b.HubCall(context.Background(), backend.HubRef{Group: "review"}, bad, nil); err == nil {
			t.Fatalf("path %q accepted", bad)
		}
	}
}

func TestSessionPeerGroupFromLabels(t *testing.T) {
	if got := sessionPeerGroupFromLabels(map[string]string{}); got != nil {
		t.Fatalf("no group label: %+v", got)
	}
	got := sessionPeerGroupFromLabels(map[string]string{
		peergroup.LabelGroup: "review", peergroup.LabelGroupGeneration: "3f9c", peergroup.LabelMember: "w1",
		peergroup.LabelMemberGeneration: "a1b2", peergroup.LabelRole: "worker",
	})
	want := &backend.SessionPeerGroup{Group: "review", GroupGeneration: "3f9c", Member: "w1", MemberGeneration: "a1b2", Role: a2a.RoleWorker}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identity = %+v", got)
	}
}
