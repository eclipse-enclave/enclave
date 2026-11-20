// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/util"
)

const (
	statusRunning = "running"
	statusExited  = "exited"
)

// sessionRecord is what the host keeps of a VM session from its start until
// it is removed: what `ps` shows, how to reach the VM, and what finalization
// needs once the VM is gone. It holds no environment and no secret.
type sessionRecord struct {
	// Instance tells this session from an earlier or later one of the same
	// name. Names are reused; what one command did to a session must not be
	// continued on its successor.
	Instance    string                `json:"instance"`
	Name        string                `json:"name"`
	Tool        string                `json:"tool"`
	ProjectHash string                `json:"projectHash"`
	Worktree    string                `json:"worktree"`
	ProjectDir  string                `json:"projectDir"`
	DisplayName string                `json:"displayName"`
	Background  bool                  `json:"background"`
	Yolo        bool                  `json:"yolo"`
	Provenance  map[string]any        `json:"provenance,omitempty"`
	CreatedAt   time.Time             `json:"createdAt"`
	Ports       []backend.PortMapping `json:"ports,omitempty"`

	// RuntimeDir holds the VM's control socket, the session's SSH identity
	// and the exports. It is gone after a reboot of the host, and so is the VM.
	RuntimeDir string `json:"runtimeDir"`
	// SocketDir holds the VM's sockets when RuntimeDir is too deep for a
	// unix socket address.
	SocketDir string `json:"socketDir,omitempty"`
	SSHPort   int    `json:"sshPort"`
	// Started is false while the VM boots. The record exists from before
	// QEMU takes the disk, so that a VM whose enclave process died during the
	// boot can still be listed and stopped.
	Started bool `json:"started"`

	// Files, Stores, AuthSync and Placeholders are the finalization the
	// session owes: file mounts to write back and credentials to reconcile.
	Files        []fileRecord              `json:"files,omitempty"`
	Stores       []backend.PersistentStore `json:"stores,omitempty"`
	AuthSync     *backend.AuthSyncSpec     `json:"authSync,omitempty"`
	Placeholders []string                  `json:"placeholders,omitempty"`
	Finalized    bool                      `json:"finalized"`
}

func newSessionRecord(req backend.Request, rt runtimeDir, port int, files []fileRecord) sessionRecord {
	projectDir := req.Session.RealWorktree
	if projectDir == "" {
		projectDir = req.Session.Worktree
	}
	var stores []backend.PersistentStore
	for _, store := range req.Stores {
		if store.Kind == backend.StoreKindConfig || store.Kind == backend.StoreKindAuth {
			stores = append(stores, backend.PersistentStore{Kind: store.Kind, Key: store.Key})
		}
	}
	return sessionRecord{
		Instance:     filepath.Base(rt.name()),
		Name:         sessionName(req),
		Tool:         req.Session.Tool,
		ProjectHash:  req.Session.ProjectHash,
		Worktree:     req.Session.Worktree,
		ProjectDir:   projectDir,
		DisplayName:  req.Session.DisplayName,
		Background:   req.Session.Background,
		Yolo:         req.Session.Yolo,
		Provenance:   req.Session.Provenance,
		CreatedAt:    time.Now().UTC(),
		Ports:        req.Ports,
		RuntimeDir:   rt.name(),
		SocketDir:    rt.socketsName(),
		SSHPort:      port,
		Files:        files,
		Stores:       stores,
		AuthSync:     req.AuthSync,
		Placeholders: backend.SecretPlaceholders(req.Secrets),
	}
}

func (r sessionRecord) runtime() runtimeDir {
	return runtimeDirAt(r.RuntimeDir, r.SocketDir)
}

// authSyncRequest rebuilds the part of the session's request that auth sync
// reads.
func (r sessionRecord) authSyncRequest() backend.Request {
	req := backend.Request{Stores: r.Stores, AuthSync: r.AuthSync}
	for _, placeholder := range r.Placeholders {
		req.Secrets = append(req.Secrets, backend.SecretRelease{Placeholder: placeholder})
	}
	return req
}

func (r sessionRecord) session(status string) backend.Session {
	return backend.Session{
		Ref:         backend.SessionRef{Name: r.Name, ID: r.Instance},
		Tool:        r.Tool,
		ProjectHash: r.ProjectHash,
		Worktree:    r.Worktree,
		ProjectDir:  r.ProjectDir,
		Status:      status,
		CreatedAt:   r.CreatedAt,
		Name:        r.DisplayName,
		Background:  r.Background,
		Ports:       r.Ports,
		Yolo:        r.Yolo,
		Provenance:  r.Provenance,
	}
}

func (b *Backend) sessionsDir() string {
	return filepath.Join(config.HostVMDir(b.home()), "sessions")
}

// recordPath is where the record of a session instance lives. Records are
// kept by instance, not by name: a name is reused by the next session, and a
// record that is written or removed must be exactly the one that was meant.
func (b *Backend) recordPath(instance string) (string, error) {
	if instance == "" || instance != filepath.Base(instance) || strings.HasPrefix(instance, ".") {
		return "", fmt.Errorf("vm backend: invalid session instance %q", instance)
	}
	return filepath.Join(b.sessionsDir(), instance+".json"), nil
}

// writeRecord, removeRecord, finalize and finalizeLeftovers change session
// records; their callers hold the sessions lock (Backend.locked).
func (b *Backend) writeRecord(record sessionRecord) error {
	path, err := b.recordPath(record.Instance)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return util.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

// readInstance returns the record of a session instance; ok is false when it
// is gone.
func (b *Backend) readInstance(instance string) (record sessionRecord, ok bool, err error) {
	path, err := b.recordPath(instance)
	if err != nil {
		return record, false, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a validated name inside the enclave-managed sessions directory.
	if errors.Is(err, fs.ErrNotExist) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	if err := json.Unmarshal(data, &record); err != nil || record.Instance != instance {
		return record, false, fmt.Errorf("session record %s is not valid", path)
	}
	return record, true, nil
}

// listRecords returns every session record that can be read, and an error
// naming the ones that cannot. A listing may show what it got. A decision
// that depends on there being no pending session must not go ahead on a
// partial answer: an unreadable record may be exactly the pending one.
func (b *Backend) listRecords() ([]sessionRecord, error) {
	entries, err := os.ReadDir(b.sessionsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []sessionRecord
	var unreadable []error
	for _, entry := range entries {
		instance, isRecord := strings.CutSuffix(entry.Name(), ".json")
		if !isRecord || entry.IsDir() {
			continue
		}
		record, ok, err := b.readInstance(instance)
		if err != nil {
			unreadable = append(unreadable, err)
			continue
		}
		if ok {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return records, errors.Join(unreadable...)
}

// namedRecords returns the record each session name stands for: the instance
// whose VM runs, else the newest. An earlier instance of a name exists only
// while it still owes a finalization, which the next holder of the project's
// disk retries; it is not what the name refers to any more.
func (b *Backend) namedRecords() ([]sessionRecord, error) {
	records, err := b.listRecords()
	if err != nil {
		// Listing and addressing sessions work with what can be read.
		logx.Debugf("vm backend: skipping session records: %v", err)
	}
	var named []sessionRecord
	for _, record := range records {
		if last := len(named) - 1; last >= 0 && named[last].Name == record.Name {
			if b.status(named[last]) != statusRunning {
				named[last] = record
			}
			continue
		}
		named = append(named, record)
	}
	return named, nil
}

// readRecord returns the record a session name stands for; ok is false when
// there is none.
func (b *Backend) readRecord(name string) (record sessionRecord, ok bool, err error) {
	records, err := b.namedRecords()
	if err != nil {
		return record, false, err
	}
	for _, record := range records {
		if record.Name == name {
			return record, true, nil
		}
	}
	return record, false, nil
}

// removeRecord forgets a session instance and what was left of its runtime
// directory.
func (b *Backend) removeRecord(record sessionRecord) {
	if path, err := b.recordPath(record.Instance); err == nil {
		_ = os.Remove(path)
	}
	if record.RuntimeDir != "" {
		record.runtime().remove()
	}
}

// status reports whether a session's VM runs, from its process. The monitor
// is not asked: it serves one client at a time, and a foreground session's
// own enclave process is that client for as long as the session runs.
func (b *Backend) status(record sessionRecord) string {
	if record.RuntimeDir == "" {
		return statusExited
	}
	if _, running := b.process(record.runtime()); running {
		return statusRunning
	}
	return statusExited
}

// connect opens the control monitor of a session's VM for a command. It
// succeeds only for the VM that answers to the session's name.
//
// A socket is used only from a directory that is still this user's own.
func (b *Backend) connect(ctx context.Context, record sessionRecord) (*qmpClient, bool) {
	qmp, err := record.runtime().dialControl(ctx)
	if err != nil {
		return nil, false
	}
	reply, err := qmp.execute(ctx, "query-name")
	var named struct {
		Name string `json:"name"`
	}
	if err != nil || json.Unmarshal(reply, &named) != nil || named.Name != record.Name {
		qmp.close()
		return nil, false
	}
	return qmp, true
}

// finalize settles what a session whose VM is gone still owes: its writable
// file mounts and its credentials. It runs once per session, whoever gets to
// it first: the foreground run itself, `stop`, a removal, or the next session
// of the project.
//
// A file mount that cannot be written back is not lost with the runtime
// directory: the guest's version moves to a directory that stays, and the
// error says where. Only if that fails too does the session stay unfinalized,
// runtime directory and all, for the next attempt.
func (b *Backend) finalize(record *sessionRecord) error {
	if record.Finalized {
		return nil
	}
	var problems []error
	var unsettled []fileRecord
	if len(record.Files) > 0 {
		files, err := record.runtime().open()
		if err == nil {
			_, err = os.Stat(files.out())
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("the runtime directory %s is gone or no longer this session's, and with it the changes to %d file mounts", record.RuntimeDir, len(record.Files)))
		} else {
			for _, file := range record.Files {
				err := persistFileMount(record.runtime(), file)
				if err == nil {
					continue
				}
				kept, keepErr := b.keepGuestFile(*record, file)
				if keepErr != nil {
					unsettled = append(unsettled, file)
					problems = append(problems, fmt.Errorf("file mount %s was not written back (%w) and its guest version could not be kept (%w)", file.Target, err, keepErr))
					continue
				}
				problems = append(problems, fmt.Errorf("file mount %s was not written back: %w; the guest's version is kept in %s", file.Target, err, kept))
			}
		}
	}
	record.Files = unsettled
	if len(unsettled) == 0 {
		if record.AuthSync != nil {
			b.storage.SyncRequestAuth(record.authSyncRequest())
		}
		record.Finalized = true
	}
	if err := b.writeRecord(*record); err != nil {
		problems = append(problems, fmt.Errorf("record the finalization: %w", err))
	}
	if len(problems) > 0 {
		return fmt.Errorf("vm session %s: %w", record.Name, errors.Join(problems...))
	}
	return nil
}

// keepGuestFile moves the guest's version of a file mount out of the runtime
// directory to where it outlasts the session.
func (b *Backend) keepGuestFile(record sessionRecord, file fileRecord) (string, error) {
	content, ok, err := record.runtime().readResult(file.ResultPath)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("the guest removed its copy")
	}
	dir := filepath.Join(config.HostVMDir(b.home()), "kept", record.Name+"-"+record.Instance)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	kept := filepath.Join(dir, filepath.Base(filepath.Dir(file.ResultPath))+"-"+filepath.Base(file.Path))
	if err := util.WriteFileAtomic(kept, []byte(content), 0o600); err != nil {
		return "", err
	}
	return kept, nil
}

// finalizeLeftovers settles the earlier sessions of a project and tool whose
// VM ended without anyone finalizing them: a tool that exited in a detached
// session, or a foreground run whose enclave process was killed. The caller
// holds the disk, so none of their VMs can be running. It returns what could
// not be settled.
func (b *Backend) finalizeLeftovers(projectHash string, tool string) error {
	// What can be read is settled; what cannot is reported with the rest.
	records, err := b.listRecords()
	var problems []error
	if err != nil {
		problems = append(problems, fmt.Errorf("vm backend: read earlier sessions: %w", err))
	}
	for i := range records {
		record := &records[i]
		if record.ProjectHash != projectHash || record.Tool != tool {
			continue
		}
		if !record.Finalized {
			logx.Infof("Finalizing the earlier vm session %s.", record.Name)
			reapExports(record.runtime())
			if err := b.finalize(record); err != nil {
				problems = append(problems, err)
			}
		}
		// A settled session stays only as what `ps --all` shows for its name:
		// not a foreground one, which nobody removes, and not one whose name
		// a later session has taken.
		superseded := i+1 < len(records) && records[i+1].Name == record.Name
		if record.Finalized && (!record.Background || superseded) {
			b.removeRecord(*record)
		}
	}
	return errors.Join(problems...)
}

// unsettled returns the sessions of a project and tool that still owe a
// finalization. It fails if any record cannot be read: that record might be
// one of them.
func (b *Backend) unsettled(projectHash string, tool string) ([]sessionRecord, error) {
	records, err := b.listRecords()
	if err != nil {
		return nil, err
	}
	var owing []sessionRecord
	for _, record := range records {
		if record.ProjectHash == projectHash && record.Tool == tool && !record.Finalized {
			owing = append(owing, record)
		}
	}
	return owing, nil
}

func matchesFilter(session backend.Session, filter backend.SessionFilter) bool {
	switch {
	case session.Status != statusRunning && (filter.RunningOnly || !filter.All):
		return false
	case filter.Tool != "" && session.Tool != filter.Tool:
		return false
	case filter.ProjectHash != "" && session.ProjectHash != filter.ProjectHash:
		return false
	case filter.Background != nil && session.Background != *filter.Background:
		return false
	case filter.NamePrefix != "" && !strings.HasPrefix(session.Ref.Name, filter.NamePrefix):
		return false
	case filter.ExactName != "" && session.Ref.Name != filter.ExactName:
		return false
	}
	return true
}
