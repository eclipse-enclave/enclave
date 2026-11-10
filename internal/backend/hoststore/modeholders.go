// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/util"
)

// ModeHolder lives outside the agent-writable config store. Container exit does
// not release it: the owning host process may still be reconciling credentials.
type ModeHolder struct {
	ID          string    `json:"id"`
	Session     string    `json:"session"`
	Tool        string    `json:"tool"`
	ProjectHash string    `json:"projectHash"`
	Engine      string    `json:"engine"`
	Protected   bool      `json:"protected"`
	ContainerID string    `json:"containerId,omitempty"`
	State       string    `json:"state"`
	OwnerPID    int       `json:"ownerPid"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ModeRecord groups the holders of one physical config store.
type ModeRecord struct {
	ConfigDir string       `json:"configDir"`
	Holders   []ModeHolder `json:"holders"`
}

// WithModeHolders uses the config store's existing lock, including during
// admission checks. The callback must not acquire that same lock again.
func WithModeHolders(home, dir string, fn func(*[]ModeHolder) error) error {
	return WithLock(home, dir, func() error {
		file := config.HostLockPath(home, "mode-"+util.HashString(dir)+".json")
		record, err := readModeRecord(file, dir)
		if err != nil {
			return err
		}
		before := slices.Clone(record.Holders)
		if err := fn(&record.Holders); err != nil {
			return err
		}
		if slices.Equal(before, record.Holders) {
			return nil
		}
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := util.WriteFileAtomic(file, data, 0o600); err != nil {
			return err
		}
		return util.SyncDir(config.HostLocksDir(home))
	})
}

// ReserveMode runs discover under the lock before publishing a pending holder.
// Protected admission supplies discovery for ordinary pre-ledger containers.
func ReserveMode(home, dir string, holder ModeHolder, discover func([]ModeHolder) error) error {
	return WithModeHolders(home, dir, func(holders *[]ModeHolder) error {
		for _, other := range *holders {
			if other.ID == holder.ID {
				return fmt.Errorf("session mode holder already reserved")
			}
			if other.State != "ended" && other.Protected != holder.Protected {
				return fmt.Errorf("session %s holds this config store in the opposite auth mode (%s); end it first", other.Session, other.State)
			}
		}
		if discover != nil {
			if err := discover(*holders); err != nil {
				return err
			}
		}
		holder.State, holder.UpdatedAt = "pending", time.Now().UTC()
		*holders = append(*holders, holder)
		return nil
	})
}

// AdvanceMode never infers completion from engine status. Only the session's
// finalizer, or explicit stale-owner cleanup, may move finalizing to ended.
func AdvanceMode(home, dir, id, state, containerID string, detached bool) error {
	return WithModeHolders(home, dir, func(holders *[]ModeHolder) error {
		for i := range *holders {
			holder := &(*holders)[i]
			if holder.ID != id {
				continue
			}
			allowed := holder.State == state ||
				holder.State == "pending" && (state == "live" || state == "finalizing") ||
				holder.State == "live" && state == "finalizing" ||
				holder.State == "finalizing" && state == "ended"
			if !validModeState(state) || !allowed {
				return fmt.Errorf("invalid session mode transition %s -> %s", holder.State, state)
			}
			holder.State, holder.UpdatedAt = state, time.Now().UTC()
			if containerID != "" {
				holder.ContainerID = containerID
			}
			if detached && state == "live" {
				holder.OwnerPID = 0
			}
			return nil
		}
		return fmt.Errorf("session mode holder is missing")
	})
}

func validModeState(state string) bool {
	return state == "pending" || state == "live" || state == "finalizing" || state == "ended"
}

// ModeOwnerAlive treats PID reuse and permission/inspection failures as alive.
// A detached handoff has no owning host process (pid zero).
func ModeOwnerAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	defer func() { _ = process.Release() }()
	err = process.Signal(syscall.Signal(0))
	return !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone)
}

// ModeRecords is a snapshot for cleanup enumeration. Mutations must reload the
// record under WithModeHolders, not write this snapshot back.
func ModeRecords(home string) ([]ModeRecord, error) {
	files, err := filepath.Glob(config.HostLockPath(home, "mode-*.json"))
	if err != nil {
		return nil, err
	}
	var records []ModeRecord
	for _, file := range files {
		record, err := readModeRecord(file, "")
		if err != nil {
			return nil, err
		}
		if len(record.Holders) > 0 {
			records = append(records, record)
		}
	}
	return records, nil
}

func readModeRecord(file, dir string) (ModeRecord, error) {
	record := ModeRecord{ConfigDir: dir}
	data, err := os.ReadFile(file) // #nosec G304 -- enclave-owned lock directory and hash-derived filename.
	if errors.Is(err, os.ErrNotExist) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, fmt.Errorf("read session mode holders: %w", err)
	}
	if record.ConfigDir == "" {
		return record, fmt.Errorf("invalid session mode config store")
	}
	ids := make(map[string]bool)
	for _, holder := range record.Holders {
		if holder.ID == "" || holder.Session == "" || holder.OwnerPID < 0 || ids[holder.ID] || !validModeState(holder.State) {
			return record, fmt.Errorf("invalid session mode holder")
		}
		ids[holder.ID] = true
	}
	return record, nil
}

// AdvanceRequestMode applies the lifecycle intent shared by both backends.
func AdvanceRequestMode(home string, req backend.Request, state, id string) {
	if req.Mode == nil {
		return
	}
	dir, err := ResolveDir(home, req.Mode.ConfigStore, backend.StoreKindConfig)
	if err == nil {
		err = AdvanceMode(home, dir, req.Mode.HolderID, state, id, req.Detached)
	}
	if err != nil {
		logx.Warnf("Session auth-mode reservation retained: %v", err)
	}
}
