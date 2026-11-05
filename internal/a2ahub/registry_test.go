// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"enclave/internal/a2a"
)

func TestCardRewrittenToHubFacingURL(t *testing.T) {
	f := newFixture(t, nil)
	adapterCard, err := os.ReadFile("../a2a/testdata/enclave-agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(adapterCard, &raw); err != nil {
		t.Fatal(err)
	}
	raw["name"] = "spoofed"
	raw["supportedInterfaces"] = []map[string]string{{"url": "http://10.0.0.1:41821/", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}}
	raw["provider"] = map[string]string{"organization": "kept"}
	adapter, rec := newAdapter(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeJSON(w, http.StatusOK, raw) })
	outbound := f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()

	resp := f.do(f.request(http.MethodGet, "/peers/worker-1"+a2a.CardPath, f.tokens["orch"], ""))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "max-age=0" {
		t.Fatalf("status %d cache %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	var card a2a.AgentCard
	var generic map[string]any
	body := json.NewDecoder(resp.Body)
	if err := body.Decode(&generic); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(generic)
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}
	wantIface := []a2a.AgentInterface{{URL: "http://a2a.enclave:41820/peers/worker-1", ProtocolBinding: "JSONRPC", ProtocolVersion: "1.0"}}
	if !reflect.DeepEqual(card.SupportedInterfaces, wantIface) {
		t.Fatalf("interfaces %+v", card.SupportedInterfaces)
	}
	if card.Name != "worker-1" || card.Description != "worker-1 reviews diffs" || len(card.Skills) != 1 || card.Skills[0].ID != "review" {
		t.Fatalf("registry fields %+v", card)
	}
	if card.SecuritySchemes["bearer"].HTTPAuth == nil || card.SecuritySchemes["bearer"].HTTPAuth.Scheme != "Bearer" ||
		len(card.SecurityRequirements) != 1 || card.SecurityRequirements[0].Schemes["bearer"].List == nil {
		t.Fatalf("security %+v %+v", card.SecuritySchemes, card.SecurityRequirements)
	}
	ext := card.Capabilities.Extensions
	if len(ext) != 1 || ext[0].URI != a2a.ExtensionURI || !ext[0].Required || ext[0].Params["queueDepth"] != float64(4) {
		t.Fatalf("capabilities not kept: %+v", card.Capabilities)
	}
	if generic["provider"] == nil {
		t.Fatal("unknown card member dropped")
	}
	got := rec.last()
	if got.path != a2a.CardPath || got.header.Get(a2a.HeaderAuthorization) != a2a.BearerPrefix+outbound || got.header.Get(a2a.HeaderMember) != "orch" {
		t.Fatalf("card fetch %s %v", got.path, got.header)
	}
}

func TestCardAdapterUnreachable(t *testing.T) {
	f := newFixture(t, nil)
	adapter, _ := newAdapter(t, nil)
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()
	adapter.Close()
	resp := f.do(f.request(http.MethodGet, "/peers/worker-1"+a2a.CardPath, f.tokens["orch"], ""))
	defer func() { _ = resp.Body.Close() }()
	var out a2a.Response
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", resp.StatusCode)
	}
	wantCode(t, out, a2a.CodeDeliveryAdapterUnreachable, a2a.DeliveryAdapterUnreachable)
}

func TestPeerListing(t *testing.T) {
	f := newFixture(t, nil)
	adapter, rec := newAdapter(t, nil)
	f.addWorker("worker-2", adapter)
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	cred := f.addHostCredential(time.Minute)
	rev := f.publishAndReload()
	for _, token := range []string{f.tokens["orch"], cred} {
		resp := f.do(f.request(http.MethodGet, a2a.HubPeersPath, token, ""))
		var listing a2a.PeerListing
		if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if listing.Group != testGroup || listing.GroupGeneration != string(testGroupGen) || listing.Revision != uint64(rev) || listing.Epoch != f.proxy.Epoch() {
			t.Fatalf("listing header %+v", listing)
		}
		want := []a2a.PeerEntry{
			{Name: "worker-1", Generation: string(f.generation("worker-1")), Description: "worker-1 reviews diffs",
				URL: "http://a2a.enclave:41820/peers/worker-1", CardURL: "http://a2a.enclave:41820/peers/worker-1/.well-known/agent-card.json"},
			{Name: "worker-2", Generation: string(f.generation("worker-2")), Description: "worker-2 reviews diffs",
				URL: "http://a2a.enclave:41820/peers/worker-2", CardURL: "http://a2a.enclave:41820/peers/worker-2/.well-known/agent-card.json"},
		}
		if !reflect.DeepEqual(listing.Peers, want) {
			t.Fatalf("peers %+v", listing.Peers)
		}
	}
	if rec.count() != 0 {
		t.Fatal("listing contacted an adapter")
	}
}
