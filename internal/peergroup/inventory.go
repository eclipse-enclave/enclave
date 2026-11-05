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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Inventory entry states.
const (
	InventoryAdmitting = "admitting"
	InventoryMember    = "member"
	InventoryEligible  = "eligible"
)

// InventoryFile is inventory/<groupGeneration>.json (proposal 16).
type InventoryFile struct {
	SchemaVersion   string           `json:"schemaVersion"`
	Group           string           `json:"group"`
	GroupGeneration Generation       `json:"groupGeneration"`
	Entries         []InventoryEntry `json:"entries"`
}

// InventoryEntry is one member's A2A store; removed once deletion succeeds.
type InventoryEntry struct {
	Member           string     `json:"member"`
	MemberGeneration Generation `json:"memberGeneration"`
	Tool             string     `json:"tool"`
	ProjectHash      string     `json:"projectHash"`
	ConfigStoreKey   string     `json:"configStoreKey"`
	// StorePath is the absolute host path of <config store>/a2a/<memberGeneration>.
	StorePath   string `json:"storePath"`
	State       string `json:"state"`
	WrittenAt   string `json:"writtenAt"`
	EligibleAt  string `json:"eligibleAt,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	LastAttempt string `json:"lastAttemptAt,omitempty"`
}

// Inventory is the cleanup inventory, written before admission.
type Inventory interface {
	// Add records entry in state InventoryAdmitting; first step of a join.
	Add(group string, groupGeneration Generation, entry InventoryEntry) error
	// SetState moves an entry to InventoryMember or InventoryEligible.
	SetState(group string, groupGeneration Generation, member string, memberGeneration Generation, state string) error
	// Remove drops an entry without deleting its store; join rollback only.
	Remove(group string, groupGeneration Generation, member string, memberGeneration Generation) error
	Load(group string, groupGeneration Generation) (*InventoryFile, error)
	// Generations lists group generations with an inventory file.
	Generations(group string) ([]Generation, error)
	// DeleteEligible calls del for each eligible entry and removes the entries
	// it succeeds for; del also removes the transcripts the table lists.
	DeleteEligible(ctx context.Context, group string, groupGeneration Generation, del func(InventoryEntry) error) error
}

// NewInventory returns the filesystem inventory.
func NewInventory(stateRoot string) Inventory {
	return &fsInventory{stateRoot: stateRoot}
}

// fsInventory relies on the caller holding the group lock.
type fsInventory struct {
	stateRoot string
}

func (inv *fsInventory) Add(group string, groupGeneration Generation, entry InventoryEntry) error {
	if err := checkInventoryKey(group, groupGeneration); err != nil {
		return err
	}
	if entry.Member == "" || !entry.MemberGeneration.Valid() {
		return fmt.Errorf("inventory entry needs a member and a valid member generation, got %q %q", entry.Member, entry.MemberGeneration)
	}
	// The member generation in the store path is what keeps deletion from
	// reaching another generation's data (interfaces.md section 3).
	if !filepath.IsAbs(entry.StorePath) || filepath.Base(entry.StorePath) != string(entry.MemberGeneration) {
		return fmt.Errorf("inventory store path %q must be absolute and end in the member generation %s", entry.StorePath, entry.MemberGeneration)
	}
	file, err := inv.loadOrNew(group, groupGeneration)
	if err != nil {
		return err
	}
	if findEntry(file, entry.Member, entry.MemberGeneration) >= 0 {
		return fmt.Errorf("inventory already lists %s generation %s: %w", entry.Member, entry.MemberGeneration, fs.ErrExist)
	}
	entry.State = InventoryAdmitting
	entry.WrittenAt = FormatTimestamp(time.Now())
	entry.EligibleAt, entry.LastError, entry.LastAttempt = "", "", ""
	file.Entries = append(file.Entries, entry)
	return inv.save(file)
}

func (inv *fsInventory) SetState(group string, groupGeneration Generation, member string, memberGeneration Generation, state string) error {
	if state != InventoryMember && state != InventoryEligible {
		return fmt.Errorf("inventory state %q cannot be set; use Add or Remove", state)
	}
	file, err := inv.Load(group, groupGeneration)
	if err != nil {
		return err
	}
	i := findEntry(file, member, memberGeneration)
	if i < 0 {
		return fmt.Errorf("inventory has no %s generation %s: %w", member, memberGeneration, fs.ErrNotExist)
	}
	file.Entries[i].State = state
	if state == InventoryEligible && file.Entries[i].EligibleAt == "" {
		file.Entries[i].EligibleAt = FormatTimestamp(time.Now())
	}
	return inv.save(file)
}

func (inv *fsInventory) Remove(group string, groupGeneration Generation, member string, memberGeneration Generation) error {
	file, err := inv.Load(group, groupGeneration)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	i := findEntry(file, member, memberGeneration)
	if i < 0 {
		return nil
	}
	file.Entries = append(file.Entries[:i], file.Entries[i+1:]...)
	return inv.save(file)
}

func (inv *fsInventory) Load(group string, groupGeneration Generation) (*InventoryFile, error) {
	if err := checkInventoryKey(group, groupGeneration); err != nil {
		return nil, err
	}
	path := InventoryPath(inv.stateRoot, group, groupGeneration)
	var file InventoryFile
	if err := readJSON(path, &file); err != nil {
		return nil, err
	}
	if file.Group != group || file.GroupGeneration != groupGeneration {
		return nil, fmt.Errorf("inventory %s names group %q generation %q", path, file.Group, file.GroupGeneration)
	}
	return &file, nil
}

func (inv *fsInventory) loadOrNew(group string, groupGeneration Generation) (*InventoryFile, error) {
	file, err := inv.Load(group, groupGeneration)
	if errors.Is(err, fs.ErrNotExist) {
		return &InventoryFile{SchemaVersion: SchemaVersion, Group: group, GroupGeneration: groupGeneration}, nil
	}
	return file, err
}

// save deletes the file once it has no entries, then drops the group
// directory if nothing else keeps it.
func (inv *fsInventory) save(file *InventoryFile) error {
	path := InventoryPath(inv.stateRoot, file.Group, file.GroupGeneration)
	if len(file.Entries) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return removeGroupDirIfUnused(inv.stateRoot, file.Group)
	}
	if err := ensureDir(GroupsDir(inv.stateRoot), privateDirMode); err != nil {
		return err
	}
	if err := ensureDir(GroupDir(inv.stateRoot, file.Group), privateDirMode); err != nil {
		return err
	}
	if err := ensureDir(filepath.Dir(path), privateDirMode); err != nil {
		return err
	}
	return writeJSONAtomic(path, file, privateFileMode)
}

func (inv *fsInventory) Generations(group string) ([]Generation, error) {
	if err := ValidateGroupName(group); err != nil {
		return nil, err
	}
	return inventoryGenerations(inv.stateRoot, group)
}

func (inv *fsInventory) DeleteEligible(ctx context.Context, group string, groupGeneration Generation, del func(InventoryEntry) error) error {
	file, err := inv.Load(group, groupGeneration)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	kept := file.Entries[:0:0]
	for _, entry := range file.Entries {
		if entry.State != InventoryEligible {
			kept = append(kept, entry)
			continue
		}
		if err := ctx.Err(); err != nil {
			kept = append(kept, entry)
			errs = append(errs, err)
			continue
		}
		var delErr error
		if filepath.Base(entry.StorePath) != string(entry.MemberGeneration) || !filepath.IsAbs(entry.StorePath) {
			delErr = fmt.Errorf("store path %q is not scoped to the member generation", entry.StorePath)
		} else {
			delErr = del(entry)
		}
		if delErr == nil {
			continue
		}
		entry.LastError = delErr.Error()
		entry.LastAttempt = FormatTimestamp(time.Now())
		kept = append(kept, entry)
		errs = append(errs, fmt.Errorf("delete A2A store of %s generation %s: %w", entry.Member, entry.MemberGeneration, delErr))
	}
	file.Entries = kept
	if err := inv.save(file); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func checkInventoryKey(group string, groupGeneration Generation) error {
	if err := ValidateGroupName(group); err != nil {
		return err
	}
	if !groupGeneration.Valid() {
		return fmt.Errorf("invalid group generation %q", groupGeneration)
	}
	return nil
}

func findEntry(file *InventoryFile, member string, memberGeneration Generation) int {
	for i, e := range file.Entries {
		if e.Member == member && e.MemberGeneration == memberGeneration {
			return i
		}
	}
	return -1
}
