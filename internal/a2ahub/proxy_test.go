// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

func TestReloadWithoutSnapshotWritesStatus(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.proxy.Reload(); err == nil {
		t.Fatal("reload without a snapshot succeeded")
	}
	s := f.status()
	if s.AppliedRevision != 0 || s.RejectedRevision != 0 || s.Error == "" {
		t.Fatalf("status %+v", s)
	}
	if s.Epoch != f.proxy.Epoch() || s.GroupGeneration != testGroupGen || s.Reader != peergroup.ReaderProxy {
		t.Fatalf("status identity %+v", s)
	}
}

func TestReloadAppliesRevision(t *testing.T) {
	f := newFixture(t, nil)
	adapter, _ := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	rev := f.publishAndReload()
	s := f.status()
	if s.AppliedRevision != rev || s.RejectedRevision != 0 || s.Error != "" {
		t.Fatalf("status %+v", s)
	}
	// A nudge without a new revision keeps it and refreshes the status.
	if err := f.proxy.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := f.status().AppliedRevision; got != rev {
		t.Fatalf("applied %d after a no-op reload", got)
	}
}

func TestProxyAdoptsSupervisorEpoch(t *testing.T) {
	const epoch = "abcdef0123456789"
	f := newFixture(t, func(c *Config) { c.Epoch = epoch })
	adapter, rec := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()
	if f.proxy.Epoch() != epoch || f.status().Epoch != epoch {
		t.Fatalf("epoch %s, status %s", f.proxy.Epoch(), f.status().Epoch)
	}
	_ = f.rpc(f.tokens["orch"], "worker-1", sendBody(1, "hi"))
	if got := rec.last().header.Get(a2a.HeaderEpoch); got != epoch {
		t.Fatalf("forwarded epoch %s", got)
	}
	if _, err := New(Config{GroupGeneration: testGroupGen, Epoch: "nope"}, nil); err == nil {
		t.Fatal("invalid epoch accepted")
	}
}

func TestReloadRefusesMixedSnapshotAndKeepsLastRevision(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"secrets hash mismatch": func(t *testing.T, dir string) {
			rewrite(t, filepath.Join(dir, peergroup.SecretsFile), func(s string) string { return strings.Replace(s, `"outbound"`, ` "outbound"`, 1) })
		},
		"membership from another revision": func(t *testing.T, dir string) {
			rewriteManifested(t, dir, peergroup.MembershipFile, func(s string) string {
				return strings.Replace(s, `"revision": 2`, `"revision": 1`, 1)
			})
		},
		"worker without outbound token": func(t *testing.T, dir string) {
			rewriteManifested(t, dir, peergroup.SecretsFile, func(s string) string {
				var sec peergroup.Secrets
				if err := json.Unmarshal([]byte(s), &sec); err != nil {
					t.Fatal(err)
				}
				sec.Outbound = sec.Outbound[:0]
				out, _ := json.Marshal(sec)
				return string(out)
			})
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			adapter, rec := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
			f.addWorker("worker-1", adapter)
			f.addOrchestrator("orch")
			first := f.publishAndReload()
			link := peergroup.CurrentLinkPath(f.stateRoot, testGroup)
			firstTarget, err := os.Readlink(link)
			if err != nil {
				t.Fatal(err)
			}
			f.addWorker("worker-2", adapter)
			second := f.publish()
			corrupt(t, peergroup.SnapshotDir(f.stateRoot, testGroup, second))
			if err := f.proxy.Reload(); err == nil {
				t.Fatal("corrupt revision applied")
			}
			s := f.status()
			if s.AppliedRevision != first || s.RejectedRevision != second || s.Error == "" {
				t.Fatalf("status %+v", s)
			}
			// Routing still follows the first revision.
			wantCode(t, f.rpc(f.tokens["orch"], "worker-2", sendBody(1, "hi")), a2a.CodeDeliveryUnknownMember, a2a.DeliveryUnknownMember)
			if resp := f.rpc(f.tokens["orch"], "worker-1", sendBody(2, "hi")); resp.Error != nil {
				t.Fatalf("worker-1 refused after rejected revision: %+v", resp.Error)
			}
			if rec.count() != 1 {
				t.Fatalf("adapter saw %d requests", rec.count())
			}
			// Withdrawing the refused revision clears the rejection (I2 B10).
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(firstTarget, link); err != nil {
				t.Fatal(err)
			}
			if err := f.proxy.Reload(); err != nil {
				t.Fatalf("reload of the applied revision: %v", err)
			}
			if s := f.status(); s.AppliedRevision != first || s.RejectedRevision != 0 || s.Error != "" {
				t.Fatalf("status after withdrawal %+v", s)
			}
		})
	}
}

func TestReloadRefusesOtherGroupGeneration(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.GroupGeneration = "ffffffffffffffff" })
	adapter, _ := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	rev := f.publish()
	if err := f.proxy.Reload(); err == nil {
		t.Fatal("snapshot of another group generation applied")
	}
	if s := f.status(); s.AppliedRevision != 0 || s.RejectedRevision != rev {
		t.Fatalf("status %+v", s)
	}
}

// rewrite replaces a published file in place, keeping its mode.
func rewrite(t *testing.T, path string, edit func(string) string) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(edit(string(data))), 0o600); err != nil {
		t.Fatal(err)
	}
}

// rewriteManifested edits a file and fixes its manifest hash, so only the
// cross-file checks can catch the change.
func rewriteManifested(t *testing.T, dir, name string, edit func(string) string) {
	t.Helper()
	rewrite(t, filepath.Join(dir, name), edit)
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	rewrite(t, filepath.Join(dir, peergroup.ManifestFile), func(s string) string {
		var man peergroup.Manifest
		if err := json.Unmarshal([]byte(s), &man); err != nil {
			t.Fatal(err)
		}
		man.Files[name] = hashToken(string(data))
		out, _ := json.Marshal(man)
		return string(out)
	})
}

func TestForwardStripsHeadersInjectsOutboundAndPrincipal(t *testing.T) {
	f := newFixture(t, nil)
	adapter, rec := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	outbound := f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	rev := f.publishAndReload()

	body := sendBody(7, "review the diff")
	req := f.request(http.MethodPost, "/peers/worker-1", f.tokens["orch"], body)
	req.Header.Set(a2a.HeaderMember, "worker-1")
	req.Header.Set(a2a.HeaderRole, "host")
	req.Header.Set("X-Enclave-A2A-Anything", "forged")
	req.Header.Set("Cookie", "session=1")
	resp := f.do(req)
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var out a2a.Response
	if err := json.Unmarshal(data, &out); err != nil || out.Error != nil {
		t.Fatalf("response %s", data)
	}
	if string(out.ID) != "7" {
		t.Fatalf("id %s", out.ID)
	}

	got := rec.last()
	if !bytes.Equal(got.body, []byte(body)) {
		t.Fatalf("body rewritten:\n%s\n%s", got.body, body)
	}
	if got.path != a2a.AdapterRPCPath {
		t.Fatalf("path %s", got.path)
	}
	want := map[string]string{
		a2a.HeaderAuthorization:   a2a.BearerPrefix + outbound,
		a2a.HeaderGroup:           testGroup,
		a2a.HeaderGroupGeneration: string(testGroupGen),
		a2a.HeaderMember:          "orch",
		a2a.HeaderRole:            string(a2a.RoleOrchestrator),
		a2a.HeaderGeneration:      string(f.generation("orch")),
		a2a.HeaderEpoch:           f.proxy.Epoch(),
		a2a.HeaderRevision:        strconv.FormatUint(uint64(rev), 10),
		a2a.HeaderVersion:         a2a.ProtocolVersion,
		a2a.HeaderExtensions:      a2a.ExtensionURI,
	}
	for h, v := range want {
		if vals := got.header.Values(h); len(vals) != 1 || vals[0] != v {
			t.Errorf("header %s = %q, want %q", h, vals, v)
		}
	}
	for h := range got.header {
		if strings.HasPrefix(h, a2a.HeaderPrefix) && want[h] == "" {
			t.Errorf("forwarded identity header %s", h)
		}
	}
	if got.header.Get("Cookie") != "" {
		t.Error("forwarded an unlisted header")
	}
}

func TestHostCredentialForwardsHostPrincipal(t *testing.T) {
	f := newFixture(t, nil)
	adapter, rec := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	cred := f.addHostCredential(2 * time.Minute)
	f.publishAndReload()
	if resp := f.rpc(cred, "worker-1", sendBody(1, "hi")); resp.Error != nil {
		t.Fatalf("host send refused: %+v", resp.Error)
	}
	h := rec.last().header
	if h.Get(a2a.HeaderMember) != peergroup.HostMemberName || h.Get(a2a.HeaderRole) != string(a2a.RoleHost) || h.Get(a2a.HeaderGeneration) != string(testHostGen) {
		t.Fatalf("host principal headers %v", h)
	}
	f.advance(3 * time.Minute)
	wantCode(t, f.rpc(cred, "worker-1", sendBody(2, "hi")), a2a.CodeDeliveryRevoked, a2a.DeliveryRevoked)
}

func TestAuthenticationAndRevocation(t *testing.T) {
	f := newFixture(t, nil)
	adapter, rec := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()

	wantCode(t, f.rpc("", "worker-1", sendBody(1, "hi")), a2a.CodeDeliveryUnauthenticated, a2a.DeliveryUnauthenticated)
	wantCode(t, f.rpc(newToken(t), "worker-1", sendBody(2, "hi")), a2a.CodeDeliveryUnauthenticated, a2a.DeliveryUnauthenticated)
	old := f.tokens["orch"]
	f.remove("orch")
	f.publishAndReload()
	wantCode(t, f.rpc(old, "worker-1", sendBody(3, "hi")), a2a.CodeDeliveryRevoked, a2a.DeliveryRevoked)
	if rec.count() != 0 {
		t.Fatalf("adapter reached %d times", rec.count())
	}
}

func TestRoleChecks(t *testing.T) {
	f := newFixture(t, nil)
	adapter, rec := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	f.addWorker("worker-2", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()

	// Workers cannot send, not even to each other.
	wantCode(t, f.rpc(f.tokens["worker-1"], "worker-2", sendBody(1, "hi")), a2a.CodeDeliveryRoleForbidden, a2a.DeliveryRoleForbidden)
	// Only workers are targets.
	wantCode(t, f.rpc(f.tokens["orch"], "orch", sendBody(2, "hi")), a2a.CodeDeliveryRoleForbidden, a2a.DeliveryRoleForbidden)
	wantCode(t, f.rpc(f.tokens["orch"], "nobody", sendBody(3, "hi")), a2a.CodeDeliveryUnknownMember, a2a.DeliveryUnknownMember)
	wantCode(t, f.rpc(f.tokens["orch"], "Not_A_Name", sendBody(4, "hi")), a2a.CodeDeliveryUnknownMember, a2a.DeliveryUnknownMember)
	f.remove("worker-2")
	f.publishAndReload()
	wantCode(t, f.rpc(f.tokens["orch"], "worker-2", sendBody(5, "hi")), a2a.CodeDeliveryRevoked, a2a.DeliveryRevoked)

	// Only the orchestrator holds liveness, only workers the control stream.
	for _, tc := range []struct{ path, token string }{
		{a2a.HubLivenessPath, f.tokens["worker-1"]},
		{a2a.HubControlPath, f.tokens["orch"]},
		{a2a.HubPeersPath, f.tokens["worker-1"]},
		{"/peers/worker-1" + a2a.CardPath, f.tokens["worker-1"]},
	} {
		resp := f.do(f.request(http.MethodGet, tc.path, tc.token, ""))
		var out a2a.Response
		_ = json.NewDecoder(resp.Body).Decode(&out)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d", tc.path, resp.StatusCode)
		}
		wantCode(t, out, a2a.CodeDeliveryRoleForbidden, a2a.DeliveryRoleForbidden)
	}
	if rec.count() != 0 {
		t.Fatalf("adapter reached %d times", rec.count())
	}
}

func TestVersionAndExtensionHeaders(t *testing.T) {
	f := newFixture(t, nil)
	adapter, rec := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()

	decode := func(req *http.Request) a2a.Response {
		resp := f.do(req)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		var out a2a.Response
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	req := f.request(http.MethodPost, "/peers/worker-1", f.tokens["orch"], sendBody(1, "hi"))
	req.Header.Del(a2a.HeaderVersion)
	wantCode(t, decode(req), a2a.CodeVersionNotSupported, "")
	req = f.request(http.MethodPost, "/peers/worker-1", f.tokens["orch"], sendBody(2, "hi"))
	req.Header.Set(a2a.HeaderVersion, "0.3")
	wantCode(t, decode(req), a2a.CodeVersionNotSupported, "")
	req = f.request(http.MethodPost, "/peers/worker-1", f.tokens["orch"], sendBody(3, "hi"))
	req.Header.Del(a2a.HeaderExtensions)
	out := decode(req)
	wantCode(t, out, a2a.CodeExtensionSupportRequired, "")
	if string(out.ID) != "3" || out.Error.Data != nil {
		t.Fatalf("A2A-range error carries id %s data %s", out.ID, out.Error.Data)
	}
	if rec.count() != 0 {
		t.Fatalf("adapter reached %d times", rec.count())
	}
}

func TestAdapterUnreachable(t *testing.T) {
	f := newFixture(t, nil)
	adapter, _ := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()
	adapter.Close()
	out := f.rpc(f.tokens["orch"], "worker-1", sendBody(9, "hi"))
	wantCode(t, out, a2a.CodeDeliveryAdapterUnreachable, a2a.DeliveryAdapterUnreachable)
	if info, _ := out.Error.Info(); info.Metadata["member"] != "worker-1" || string(out.ID) != "9" {
		t.Fatalf("error %+v id %s", info, out.ID)
	}
}

func TestAdapterHTTPErrorIsDeliveryError(t *testing.T) {
	f := newFixture(t, nil)
	adapter, _ := newAdapter(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		http.Error(w, "no", http.StatusUnauthorized)
	})
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()
	out := f.rpc(f.tokens["orch"], "worker-1", sendBody(1, "hi"))
	wantCode(t, out, a2a.CodeDeliveryAdapterUnreachable, a2a.DeliveryAdapterUnreachable)
	if info, _ := out.Error.Info(); info.Metadata["httpStatus"] != "401" {
		t.Fatalf("metadata %+v", info.Metadata)
	}
}

func TestRequestTooLarge(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxRequestBytes = 1024 })
	adapter, rec := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()
	out := f.rpc(f.tokens["orch"], "worker-1", sendBody(1, strings.Repeat("x", 1024+requestEnvelopeBytes)))
	wantCode(t, out, a2a.CodeRequestTooLarge, a2a.DeliveryRequestTooLarge)
	if rec.count() != 0 {
		t.Fatal("oversized request forwarded")
	}
}

func TestOwnerCacheRefusesForeignPrincipal(t *testing.T) {
	f := newFixture(t, nil)
	adapter, rec := newAdapter(t, taskResult("t1", a2a.TaskStateWorking))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	cred := f.addHostCredential(time.Hour)
	f.publishAndReload()

	if out := f.rpc(f.tokens["orch"], "worker-1", sendBody(1, "hi")); out.Error != nil {
		t.Fatal(out.Error)
	}
	wantCode(t, f.rpc(cred, "worker-1", getTaskBody(2, "t1")), a2a.CodeTaskNotFound, "")
	if rec.count() != 1 {
		t.Fatalf("foreign GetTask forwarded (%d requests)", rec.count())
	}
	if out := f.rpc(f.tokens["orch"], "worker-1", getTaskBody(3, "t1")); out.Error != nil {
		t.Fatal(out.Error)
	}
	// Unknown to the cache: forwarded, the worker decides.
	if out := f.rpc(cred, "worker-1", getTaskBody(4, "other")); out.Error != nil {
		t.Fatal(out.Error)
	}
	if rec.count() != 3 {
		t.Fatalf("adapter saw %d requests", rec.count())
	}
}

func TestAuditIsStatusOnly(t *testing.T) {
	for _, mode := range []string{LogCoarse, LogFull} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, func(c *Config) { c.LogMode = mode })
			adapter, _ := newAdapter(t, func(w http.ResponseWriter, _ *http.Request, body []byte) {
				req, _ := a2a.DecodeRequest(body)
				meta := a2a.WithEnclaveMeta(nil, a2a.EnclaveMeta{Termination: a2a.TerminationConfirmed, Reason: a2a.ReasonDeadline})
				task := a2a.Task{ID: "t9", ContextID: "c9", Status: a2a.TaskStatus{State: a2a.TaskStateFailed}, Metadata: meta,
					Artifacts: []a2a.Artifact{{ArtifactID: "a", Parts: []a2a.Part{a2a.TextPart("secret result")}}}}
				result, _ := a2a.EncodeResult(a2a.Result{Task: &task})
				writeJSON(w, http.StatusOK, a2a.Response{JSONRPC: "2.0", ID: req.ID, Result: result})
			})
			f.addWorker("worker-1", adapter)
			f.addOrchestrator("orch")
			rev := f.publishAndReload()
			_ = f.rpc(f.tokens["orch"], "worker-1", sendBody(1, "secret prompt"))
			_ = f.rpc(newToken(t), "worker-1", sendBody(2, "secret prompt"))

			entries := f.auditEntries()
			if len(entries) != 2 {
				t.Fatalf("entries %+v", entries)
			}
			e := entries[0]
			if e.Principal != "orch" || e.Role != "orchestrator" || e.Target != "worker-1" || e.Method != a2a.MethodSendMessage ||
				e.TaskID != "t9" || e.ContextID != "c9" || e.Outcome != 0 || e.Reason != "deadline" || e.Termination != "confirmed" ||
				e.Revision != uint64(rev) || e.Epoch != f.proxy.Epoch() || e.Group != testGroup || e.Generation != string(f.generation("orch")) {
				t.Fatalf("entry %+v", e)
			}
			if entries[1].Outcome != a2a.CodeDeliveryUnauthenticated || entries[1].Reason != a2a.DeliveryUnauthenticated || entries[1].Principal != "" {
				t.Fatalf("refusal entry %+v", entries[1])
			}
			raw, _ := os.ReadFile(filepath.Join(f.cfg.AuditDir, peergroup.AuditLogFile))
			hasContent := bytes.Contains(raw, []byte("secret"))
			if mode == LogCoarse && hasContent {
				t.Fatalf("coarse audit logged content: %s", raw)
			}
			if mode == LogFull {
				if e.Content == nil || e.Content.Request != "secret prompt" || e.Content.Response != "secret result" {
					t.Fatalf("full content %+v", e.Content)
				}
				if entries[1].Content == nil || entries[1].Content.Request != "" {
					t.Fatalf("unauthenticated request content logged: %+v", entries[1].Content)
				}
			}
		})
	}
}

func TestRoutingRejectsBadPaths(t *testing.T) {
	f := newFixture(t, nil)
	f.addOrchestrator("orch")
	f.publishAndReload()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/peers/worker-1", http.StatusMethodNotAllowed},
		{http.MethodPost, "/peers/worker-1/extra", http.StatusNotFound},
		{http.MethodPost, "/peers/", http.StatusNotFound},
		{http.MethodGet, "/elsewhere", http.StatusNotFound},
		{http.MethodPost, a2a.HubPeersPath, http.StatusMethodNotAllowed},
	} {
		resp := f.do(f.request(tc.method, tc.path, f.tokens["orch"], ""))
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, resp.StatusCode, tc.status)
		}
	}
}

func TestServeReloadsOnSignal(t *testing.T) {
	f := newFixture(t, nil)
	f.addOrchestrator("orch")
	first := f.publish()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reloads := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() { done <- f.proxy.Serve(ctx, ln, reloads) }()
	waitFor(t, func() bool { return f.appliedOnDisk() == first })
	f.addOrchestrator("orch-2")
	f.remove("orch")
	second := f.publish()
	reloads <- os.Interrupt
	waitFor(t, func() bool { return f.appliedOnDisk() == second })
	cancel()
	if err := <-done; err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func (f *fixture) appliedOnDisk() peergroup.Revision {
	_, proxy, err := peergroup.NewStatusWaiter(f.stateRoot).Read(testGroup)
	if err != nil || proxy == nil {
		return 0
	}
	return proxy.AppliedRevision
}
