// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

const modeStartTimeout = 5 * time.Minute

func cleanupModeHolders(run model.RunOptions, cleanup model.CleanupOptions, home string, project model.Project, paths model.Paths) error {
	records, err := hoststore.ModeRecords(home)
	if err != nil || len(records) == 0 {
		return err
	}
	host, err := resolveHost()
	if err != nil {
		return err
	}
	be, err := selectBackend(model.Options{RunOptions: run}, dockerBackendOptions(host, paths, model.BuildOptions{}, run))
	if err != nil {
		return err
	}
	for _, record := range records {
		first := record.Holders[0]
		if !cleanup.CleanupAll && (first.Tool != run.Tool || first.ProjectHash != project.Hash) {
			continue
		}
		// The order matches runtime start: start lock, then config-store lock.
		// Keep the start lock through Remove so an exited holder's name cannot
		// be reused between inspection and its credential finalizer.
		lock := config.HostLockPath(home, config.SessionStartLockName(first.Tool, first.ProjectHash))
		release, err := util.AcquireFileLock(lock, nil)
		if err != nil {
			return err
		}
		err = cleanupModeStore(home, record.ConfigDir, be, cleanup.CleanupDryRun)
		release()
		if err != nil {
			return err
		}
	}
	return nil
}

func cleanupModeStore(home, dir string, be backend.Backend, dryRun bool) error {
	var exited []backend.SessionRef
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := hoststore.WithModeHolders(home, dir, func(holders *[]hoststore.ModeHolder) error {
		kept := make([]hoststore.ModeHolder, 0, len(*holders))
		for _, holder := range *holders {
			remove := holder.State == "ended"
			if holder.Engine != be.Name() {
				logx.Infof("Keeping auth-mode holder %s; use --backend %s to clean it", holder.Session, holder.Engine)
				remove = false
			} else if !remove && !hoststore.ModeOwnerAlive(holder.OwnerPID) && time.Since(holder.UpdatedAt) >= modeStartTimeout {
				inspector, ok := be.(backend.SessionModeInspector)
				if !ok {
					return fmt.Errorf("backend %s cannot inspect auth-mode holders", be.Name())
				}
				ref, running, err := inspector.InspectModeSession(ctx, holder.ID, holder.ContainerID)
				if err != nil {
					return fmt.Errorf("session %s (%s): %w", holder.Session, holder.Engine, err)
				}
				remove = ref == nil
				if ref != nil && !running {
					exited = append(exited, *ref)
				}
			}
			if remove {
				if dryRun {
					logx.Infof("Would remove auth-mode holder: %s (%s)", holder.Session, holder.State)
				} else {
					logx.Infof("Removing auth-mode holder: %s (%s)", holder.Session, holder.State)
					continue
				}
			}
			kept = append(kept, holder)
		}
		*holders = kept
		return nil
	})
	if err != nil {
		return err
	}
	for _, ref := range exited {
		if dryRun {
			logx.Infof("Would finalize and remove exited auth-mode session: %s", ref.Name)
			continue
		}
		if err := be.Remove(ctx, ref); err != nil {
			return err
		}
	}
	if len(exited) > 0 && !dryRun {
		return hoststore.WithModeHolders(home, dir, func(holders *[]hoststore.ModeHolder) error {
			*holders = slices.DeleteFunc(*holders, func(holder hoststore.ModeHolder) bool {
				return holder.Engine == be.Name() && holder.State == "ended"
			})
			return nil
		})
	}
	return nil
}
