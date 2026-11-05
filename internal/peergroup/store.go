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
	"sort"
	"strings"
	"time"

	"enclave/internal/a2a"
)

type fsGroupStore struct {
	stateRoot string
	locksDir  string
}

func (s *fsGroupStore) Lock(ctx context.Context, group string) (func(), error) {
	if err := ValidateGroupName(group); err != nil {
		return nil, err
	}
	return acquireLock(ctx, filepath.Join(s.locksDir, GroupLockName(group)))
}

func (s *fsGroupStore) Load(group string) (*GroupRecord, error) {
	if err := ValidateGroupName(group); err != nil {
		return nil, err
	}
	var rec GroupRecord
	if err := readJSON(GroupRecordPath(s.stateRoot, group), &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrGroupNotFound, group)
		}
		return nil, err
	}
	if rec.Group != group {
		return nil, fmt.Errorf("group record %s names group %q", GroupRecordPath(s.stateRoot, group), rec.Group)
	}
	return &rec, nil
}

func (s *fsGroupStore) Create(group string) (*GroupRecord, error) {
	return s.create(group, nil)
}

func (s *fsGroupStore) CreateOn(group string, engine Engine) (*GroupRecord, error) {
	return s.create(group, &engine)
}

func (s *fsGroupStore) create(group string, engine *Engine) (*GroupRecord, error) {
	if err := ValidateGroupName(group); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(GroupRecordPath(s.stateRoot, group)); err == nil {
		return nil, fmt.Errorf("peer group %s: %w", group, fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := s.resetGroupDirs(group); err != nil {
		return nil, err
	}
	groupGen, err := NewGeneration()
	if err != nil {
		return nil, err
	}
	hostGen, err := NewGeneration()
	if err != nil {
		return nil, err
	}
	rec := &GroupRecord{
		SchemaVersion:   SchemaVersion,
		Group:           group,
		GroupGeneration: groupGen,
		CreatedAt:       FormatTimestamp(time.Now()),
		HubContainer:    HubContainerPrefix + group,
		HostPrincipal:   HostPrincipal{Name: HostMemberName, Generation: hostGen},
		Engine:          engine,
	}
	if err := s.Save(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// resetGroupDirs empties everything a previous group generation published,
// so revision numbers and status acks never span generations; the inventory
// and the audit log are kept.
func (s *fsGroupStore) resetGroupDirs(group string) error {
	if err := ensureDir(GroupsDir(s.stateRoot), privateDirMode); err != nil {
		return err
	}
	if err := ensureDir(GroupDir(s.stateRoot, group), privateDirMode); err != nil {
		return err
	}
	snapshots := SnapshotsDir(s.stateRoot, group)
	if err := os.RemoveAll(snapshots); err != nil {
		return err
	}
	if err := ensureDir(snapshots, publicDirMode); err != nil {
		return err
	}
	status := StatusDir(s.stateRoot, group)
	if err := ensureDir(status, sharedDirMode); err != nil {
		return err
	}
	if err := removeDirEntries(status); err != nil {
		return err
	}
	if err := ensureDir(AuditDir(s.stateRoot, group), sharedDirMode); err != nil {
		return err
	}
	secrets := filepath.Dir(OutboundTokensPath(s.stateRoot, group))
	if err := os.RemoveAll(secrets); err != nil {
		return err
	}
	return ensureDir(secrets, privateDirMode)
}

func (s *fsGroupStore) Save(rec *GroupRecord) error {
	if rec == nil {
		return errors.New("nil group record")
	}
	if err := ValidateGroupName(rec.Group); err != nil {
		return err
	}
	if !rec.GroupGeneration.Valid() {
		return fmt.Errorf("group record %s: invalid group generation %q", rec.Group, rec.GroupGeneration)
	}
	if err := checkSchema(&rec.SchemaVersion); err != nil {
		return err
	}
	existing, err := s.Load(rec.Group)
	switch {
	case errors.Is(err, ErrGroupNotFound):
	case err != nil:
		return err
	case existing.GroupGeneration != rec.GroupGeneration:
		return fmt.Errorf("group record %s: stored generation %s, refusing to overwrite with %s", rec.Group, existing.GroupGeneration, rec.GroupGeneration)
	}
	normalizeRecord(rec)
	if err := ensureDir(GroupDir(s.stateRoot, rec.Group), privateDirMode); err != nil {
		return err
	}
	return writeJSONAtomic(GroupRecordPath(s.stateRoot, rec.Group), rec, privateFileMode)
}

// normalizeRecord writes empty lists as [] rather than null for non-Go readers.
func normalizeRecord(rec *GroupRecord) {
	if rec.Members == nil {
		rec.Members = []GroupMember{}
	}
	if rec.Departures == nil {
		rec.Departures = []Departure{}
	}
	for i := range rec.Departures {
		if rec.Departures[i].Acks == nil {
			rec.Departures[i].Acks = []a2a.DepartureAck{}
		}
	}
	if rec.Obligations == nil {
		rec.Obligations = []Obligation{}
	}
	if rec.Releases == nil {
		rec.Releases = []Release{}
	}
	if rec.Credentials == nil {
		rec.Credentials = []InvocationRecord{}
	}
}

func (s *fsGroupStore) Delete(group string, groupGeneration Generation) error {
	rec, err := s.Load(group)
	if err != nil {
		return err
	}
	if rec.GroupGeneration != groupGeneration {
		return fmt.Errorf("peer group %s has generation %s, not %s", group, rec.GroupGeneration, groupGeneration)
	}
	if err := os.Remove(GroupRecordPath(s.stateRoot, group)); err != nil {
		return err
	}
	if err := syncDir(GroupDir(s.stateRoot, group)); err != nil {
		return err
	}
	for _, dir := range []string{
		SnapshotsDir(s.stateRoot, group),
		StatusDir(s.stateRoot, group),
		filepath.Dir(OutboundTokensPath(s.stateRoot, group)),
	} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return removeGroupDirIfUnused(s.stateRoot, group)
}

func (s *fsGroupStore) List() ([]string, error) {
	entries, err := os.ReadDir(GroupsDir(s.stateRoot))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var groups []string
	for _, e := range entries {
		if !e.IsDir() || ValidateGroupName(e.Name()) != nil {
			continue
		}
		used, err := groupDirInUse(s.stateRoot, e.Name())
		if err != nil {
			return nil, err
		}
		if used {
			groups = append(groups, e.Name())
		}
	}
	sort.Strings(groups)
	return groups, nil
}

// groupDirInUse reports whether group.json or any inventory file remains.
func groupDirInUse(stateRoot, group string) (bool, error) {
	if _, err := os.Lstat(GroupRecordPath(stateRoot, group)); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	gens, err := inventoryGenerations(stateRoot, group)
	if err != nil {
		return false, err
	}
	return len(gens) > 0, nil
}

// removeGroupDirIfUnused applies the rule of interfaces.md section 3: the
// group directory goes once group.json and every inventory file are gone.
func removeGroupDirIfUnused(stateRoot, group string) error {
	used, err := groupDirInUse(stateRoot, group)
	if err != nil || used {
		return err
	}
	if err := os.RemoveAll(GroupDir(stateRoot, group)); err != nil {
		return err
	}
	return syncDir(GroupsDir(stateRoot))
}

func removeDirEntries(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func inventoryGenerations(stateRoot, group string) ([]Generation, error) {
	entries, err := os.ReadDir(inventoryDir(stateRoot, group))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var gens []Generation
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() || !Generation(name).Valid() {
			continue
		}
		gens = append(gens, Generation(name))
	}
	sort.Slice(gens, func(i, j int) bool { return gens[i] < gens[j] })
	return gens, nil
}
