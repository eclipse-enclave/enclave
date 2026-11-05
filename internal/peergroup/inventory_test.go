// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func testEntry(t *testing.T, member string) InventoryEntry {
	t.Helper()
	gen, err := NewGeneration()
	if err != nil {
		t.Fatal(err)
	}
	return InventoryEntry{
		Member: member, MemberGeneration: gen, Tool: "claude", ProjectHash: "ab12cd34ef56", ConfigStoreKey: "default",
		StorePath: ContextTableDir(filepath.Join(t.TempDir(), "config-store", "default"), gen),
	}
}

func TestInventoryLifecycle(t *testing.T) {
	state := t.TempDir()
	inv := NewInventory(state)
	w1, w2 := testEntry(t, "worker-1"), testEntry(t, "worker-2")

	if err := inv.Add("review", testGen, w1); err != nil {
		t.Fatal(err)
	}
	if err := inv.Add("review", testGen, w1); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate Add = %v", err)
	}
	if err := inv.Add("review", testGen, w2); err != nil {
		t.Fatal(err)
	}
	file, err := inv.Load("review", testGen)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Entries) != 2 || file.Entries[0].State != InventoryAdmitting || file.Entries[0].WrittenAt == "" || file.SchemaVersion != SchemaVersion {
		t.Fatalf("after Add: %+v", file)
	}

	if err := inv.SetState("review", testGen, "worker-1", w1.MemberGeneration, InventoryMember); err != nil {
		t.Fatal(err)
	}
	if err := inv.SetState("review", testGen, "worker-1", w1.MemberGeneration, InventoryAdmitting); err == nil {
		t.Fatal("SetState back to admitting succeeded")
	}
	if err := inv.SetState("review", testGen, "worker-9", w1.MemberGeneration, InventoryMember); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("SetState of unknown entry = %v", err)
	}
	if err := inv.SetState("review", testGen, "worker-1", w1.MemberGeneration, InventoryEligible); err != nil {
		t.Fatal(err)
	}
	if err := inv.Remove("review", testGen, "worker-2", w2.MemberGeneration); err != nil {
		t.Fatal(err)
	}
	if err := inv.Remove("review", testGen, "worker-2", w2.MemberGeneration); err != nil {
		t.Fatalf("second Remove = %v", err)
	}
	file, _ = inv.Load("review", testGen)
	if len(file.Entries) != 1 || file.Entries[0].State != InventoryEligible || file.Entries[0].EligibleAt == "" {
		t.Fatalf("after SetState and Remove: %+v", file)
	}

	gens, err := inv.Generations("review")
	if err != nil || len(gens) != 1 || gens[0] != testGen {
		t.Fatalf("Generations = %v, %v", gens, err)
	}
}

func TestInventoryRejectsUnscopedStorePath(t *testing.T) {
	inv := NewInventory(t.TempDir())
	e := testEntry(t, "worker-1")
	e.StorePath = filepath.Dir(e.StorePath)
	if err := inv.Add("review", testGen, e); err == nil {
		t.Fatal("Add accepted a store path without the member generation")
	}
	e = testEntry(t, "worker-1")
	e.StorePath = "relative/" + string(e.MemberGeneration)
	if err := inv.Add("review", testGen, e); err == nil {
		t.Fatal("Add accepted a relative store path")
	}
	if err := inv.Add("review", "nothex", testEntry(t, "w")); err == nil {
		t.Fatal("Add accepted an invalid group generation")
	}
}

func TestInventoryDeleteEligible(t *testing.T) {
	store, state := newTestStore(t)
	rec, err := store.Create("review")
	if err != nil {
		t.Fatal(err)
	}
	gen := rec.GroupGeneration
	inv := NewInventory(state)
	ok, failing, active := testEntry(t, "worker-1"), testEntry(t, "worker-2"), testEntry(t, "worker-3")
	for _, e := range []InventoryEntry{ok, failing, active} {
		if err := inv.Add("review", gen, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []InventoryEntry{ok, failing} {
		if err := inv.SetState("review", gen, e.Member, e.MemberGeneration, InventoryEligible); err != nil {
			t.Fatal(err)
		}
	}
	var deleted []string
	del := func(e InventoryEntry) error {
		if e.Member == "worker-2" {
			return errors.New("busy")
		}
		deleted = append(deleted, e.Member)
		return nil
	}
	if err := inv.DeleteEligible(context.Background(), "review", gen, del); err == nil {
		t.Fatal("DeleteEligible hid a failed deletion")
	}
	if len(deleted) != 1 || deleted[0] != "worker-1" {
		t.Fatalf("deleted = %v", deleted)
	}
	file, _ := inv.Load("review", gen)
	if len(file.Entries) != 2 {
		t.Fatalf("entries after partial deletion = %+v", file.Entries)
	}
	for _, e := range file.Entries {
		if e.Member == "worker-2" && (e.LastError != "busy" || e.LastAttempt == "") {
			t.Fatalf("failed entry not annotated: %+v", e)
		}
	}

	// After teardown the inventory alone keeps the group directory.
	if err := inv.SetState("review", gen, "worker-3", active.MemberGeneration, InventoryEligible); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("review", gen); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(GroupDir(state, "review")); err != nil {
		t.Fatalf("group dir with inventory left: %v", err)
	}
	if err := inv.DeleteEligible(context.Background(), "review", gen, func(InventoryEntry) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(InventoryPath(state, "review", gen)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty inventory file kept: %v", err)
	}
	if _, err := os.Stat(GroupDir(state, "review")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("group dir kept after last inventory entry: %v", err)
	}
}

func TestInventoryDeleteEligibleRefusesTamperedPath(t *testing.T) {
	state := t.TempDir()
	inv := NewInventory(state)
	e := testEntry(t, "worker-1")
	if err := inv.Add("review", testGen, e); err != nil {
		t.Fatal(err)
	}
	if err := inv.SetState("review", testGen, e.Member, e.MemberGeneration, InventoryEligible); err != nil {
		t.Fatal(err)
	}
	file, _ := inv.Load("review", testGen)
	file.Entries[0].StorePath = filepath.Dir(file.Entries[0].StorePath)
	if err := writeJSONAtomic(InventoryPath(state, "review", testGen), file, 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err := inv.DeleteEligible(context.Background(), "review", testGen, func(InventoryEntry) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("DeleteEligible on an unscoped path: err=%v called=%v", err, called)
	}
}
