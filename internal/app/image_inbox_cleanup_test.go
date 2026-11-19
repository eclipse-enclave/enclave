// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
)

func TestOrphanedImageInboxes(t *testing.T) {
	stopped := func(s backend.Session, status string) backend.Session {
		s.Status = status
		return s
	}
	tests := []struct {
		name     string
		inboxes  []string
		sessions []backend.Session
		keep     string
		want     []string
	}{
		{
			name:    "no sessions orphans every inbox",
			inboxes: []string{"hashB", "hashA"},
			want:    []string{"hashA", "hashB"},
		},
		{
			name:     "a running inbox-enabled session keeps its project's inbox",
			inboxes:  []string{"hashA", "hashB"},
			sessions: []backend.Session{inboxSession("a", "hashA", "/src/a", true)},
			want:     []string{"hashB"},
		},
		{
			name:    "one of two sessions is enough to keep the inbox",
			inboxes: []string{"hashA"},
			sessions: []backend.Session{
				stopped(inboxSession("a1", "hashA", "/src/a", true), "exited"),
				inboxSession("a2", "hashA", "/src/a", true),
			},
			want: nil,
		},
		{
			name:     "a session without an inbox does not keep it",
			inboxes:  []string{"hashA"},
			sessions: []backend.Session{inboxSession("a", "hashA", "/src/a", false)},
			want:     []string{"hashA"},
		},
		{
			name:    "stopped but unremoved containers count as not running",
			inboxes: []string{"hashA", "hashB", "hashC", "hashD"},
			sessions: []backend.Session{
				stopped(inboxSession("a", "hashA", "/src/a", true), "exited"),
				stopped(inboxSession("b", "hashB", "/src/b", true), "dead"),
				stopped(inboxSession("c", "hashC", "/src/c", true), "stopped"),
				stopped(inboxSession("d", "hashD", "/src/d", true), "removing"),
			},
			want: []string{"hashA", "hashB", "hashC", "hashD"},
		},
		{
			name:    "a session that is created but not yet started keeps the inbox",
			inboxes: []string{"hashA", "hashB"},
			sessions: []backend.Session{
				stopped(inboxSession("a", "hashA", "/src/a", true), "created"),
				stopped(inboxSession("b", "hashB", "/src/b", true), "paused"),
			},
			want: nil,
		},
		{
			name:    "the project whose session is starting is kept",
			inboxes: []string{"hashA", "hashB"},
			keep:    "hashA",
			want:    []string{"hashB"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := orphanedImageInboxes(tt.inboxes, tt.sessions, tt.keep)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("orphanedImageInboxes() = %v, want %v", got, tt.want)
			}
		})
	}
}

type inboxListingBackend struct {
	stopTestBackend
	name     string
	listed   int
	filter   backend.SessionFilter
	sessions []backend.Session
	err      error
}

func (b *inboxListingBackend) Name() string { return b.name }

func (b *inboxListingBackend) List(_ context.Context, filter backend.SessionFilter) ([]backend.Session, error) {
	b.listed++
	b.filter = filter
	return b.sessions, b.err
}

func makeInboxes(t *testing.T, home string, hashes ...string) {
	t.Helper()
	for _, hash := range hashes {
		if _, err := config.EnsureHostImageInboxDir(home, hash); err != nil {
			t.Fatal(err)
		}
	}
}

func inboxExists(home string, hash string) bool {
	_, err := os.Stat(config.HostImageInboxDir(home, hash))
	return err == nil
}

func TestPlanImageInboxSweepListsAllSessions(t *testing.T) {
	home := t.TempDir()
	makeInboxes(t, home, "hashA", "hashB")
	be := &inboxListingBackend{name: backend.NameDocker, sessions: []backend.Session{inboxSession("a", "hashA", "/src/a", true)}}

	dirs, err := planImageInboxSweep(context.Background(), be, home, "")
	if err != nil {
		t.Fatal(err)
	}
	if !be.filter.All {
		t.Fatalf("sweep must see created and stopped sessions, listed with %+v", be.filter)
	}
	want := []cleanupDir{{Kind: imageInboxKind, Path: config.HostImageInboxDir(home, "hashB")}}
	if !reflect.DeepEqual(dirs, want) {
		t.Fatalf("planImageInboxSweep() = %v, want %v", dirs, want)
	}
}

func TestPlanImageInboxSweepSkipsListingWithoutInboxes(t *testing.T) {
	be := &inboxListingBackend{name: backend.NameDocker, err: errors.New("engine down")}

	dirs, err := planImageInboxSweep(context.Background(), be, t.TempDir(), "")
	if err != nil || dirs != nil {
		t.Fatalf("planImageInboxSweep() = %v, %v; want nothing", dirs, err)
	}
	if be.listed != 0 {
		t.Fatal("listed sessions although no inbox exists")
	}
}

func TestPlanImageInboxSweepKeepsEverythingWhenListingFails(t *testing.T) {
	home := t.TempDir()
	makeInboxes(t, home, "hashA")
	be := &inboxListingBackend{name: backend.NameDocker, err: errors.New("engine down")}

	dirs, err := planImageInboxSweep(context.Background(), be, home, "")
	if err == nil || dirs != nil {
		t.Fatalf("planImageInboxSweep() = %v, %v; want an error and nothing to remove", dirs, err)
	}
}

func TestPlanImageInboxSweepIgnoresBackendsWithoutInboxes(t *testing.T) {
	home := t.TempDir()
	makeInboxes(t, home, "hashA")
	be := &inboxListingBackend{name: backend.NameQEMU}

	dirs, err := planImageInboxSweep(context.Background(), be, home, "")
	if err != nil || dirs != nil {
		t.Fatalf("planImageInboxSweep() = %v, %v; want nothing", dirs, err)
	}
	if be.listed != 0 {
		t.Fatal("a backend that cannot run inbox-enabled sessions decided their inboxes")
	}
}

func TestSweepImageInboxesRemovesOrphans(t *testing.T) {
	home := t.TempDir()
	makeInboxes(t, home, "hashA", "hashB", "hashC")
	be := &inboxListingBackend{name: backend.NamePodman, sessions: []backend.Session{inboxSession("a", "hashA", "/src/a", true)}}

	sweepImageInboxes(context.Background(), be, home, "hashC")

	if !inboxExists(home, "hashA") || !inboxExists(home, "hashC") {
		t.Fatal("removed an inbox in use")
	}
	if inboxExists(home, "hashB") {
		t.Fatal("kept an orphaned inbox")
	}
}

// sessionLifecycleBackend removes a session from its listing once stopped,
// like `enclave stop` does with the container.
type sessionLifecycleBackend struct {
	inboxListingBackend
}

func (b *sessionLifecycleBackend) Stop(_ context.Context, ref backend.SessionRef, _ backend.StopOptions) error {
	kept := b.sessions[:0]
	for _, session := range b.sessions {
		if session.Ref.Name != ref.Name {
			kept = append(kept, session)
		}
	}
	b.sessions = kept
	return nil
}

func TestStopRemovesTheInboxOfTheLastInboxEnabledSession(t *testing.T) {
	projectDir := t.TempDir()
	project, err := config.ResolveProjectFromDir(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	first := inboxSession("enclave-claude-"+project.Hash+"-1", project.Hash, projectDir, true)
	first.Name = "1"
	second := inboxSession("enclave-claude-"+project.Hash+"-2", project.Hash, projectDir, true)
	second.Name = "2"
	home := t.TempDir()
	makeInboxes(t, home, project.Hash)
	be := &sessionLifecycleBackend{inboxListingBackend{name: backend.NameDocker, sessions: []backend.Session{first, second}}}

	if code := stopSessionsAndSweepInboxes(context.Background(), be, stopOptionsWithArg("1"), projectDir, home); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	if !inboxExists(home, project.Hash) {
		t.Fatal("stopping one of two sessions removed the inbox")
	}

	if code := stopSessionsAndSweepInboxes(context.Background(), be, stopOptionsWithArg("2"), projectDir, home); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	if inboxExists(home, project.Hash) {
		t.Fatal("stopping the last inbox-enabled session kept the inbox")
	}
}

func TestRunHoldingImageInbox(t *testing.T) {
	tests := []struct {
		name       string
		foreground bool
		others     []backend.Session
		wantKept   bool
	}{
		{name: "the last foreground session removes the inbox on exit", foreground: true},
		{
			name:       "a foreground session exiting next to another keeps the inbox",
			foreground: true,
			others:     []backend.Session{inboxSession("other", "hashA", "/src/a", true)},
			wantKept:   true,
		},
		{name: "a background session keeps the inbox after it detaches", wantKept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			makeInboxes(t, home, "hashA", "orphan")
			be := &inboxListingBackend{name: backend.NameDocker, sessions: tt.others}

			code := runHoldingImageInbox(context.Background(), be, home, "hashA", tt.foreground, func() int {
				if inboxExists(home, "orphan") {
					t.Error("another project's orphaned inbox survived the session start")
				}
				if !inboxExists(home, "hashA") {
					t.Error("the starting session's inbox was removed")
				}
				return 7
			})

			if code != 7 {
				t.Fatalf("exit code %d, want the session's 7", code)
			}
			if got := inboxExists(home, "hashA"); got != tt.wantKept {
				t.Fatalf("inbox kept = %v, want %v", got, tt.wantKept)
			}
		})
	}
}
