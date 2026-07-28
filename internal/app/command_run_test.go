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
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestExecutionRequiresDocker(t *testing.T) {
	if !executionRequiresDocker("run", model.Options{}) {
		t.Fatal("docker backend should require Docker")
	}
	qemuPrebuilt := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NameQEMU},
		BuildOptions: model.BuildOptions{NoRebuild: true, ImageNameSet: true},
	}
	if executionRequiresDocker("run", qemuPrebuilt) {
		t.Fatal("prebuilt qemu bundle run should not require Docker")
	}
	qemuNoRebuild := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NameQEMU},
		BuildOptions: model.BuildOptions{NoRebuild: true},
	}
	if executionRequiresDocker("run", qemuNoRebuild) {
		t.Fatal("qemu --no-rebuild reuse run should not require Docker")
	}
	qemuImageName := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NameQEMU},
		BuildOptions: model.BuildOptions{ImageNameSet: true},
	}
	if executionRequiresDocker("run", qemuImageName) {
		t.Fatal("qemu --image-name run should not require Docker")
	}
	qemuBuild := model.Options{RunOptions: model.RunOptions{Backend: backend.NameQEMU}}
	if !executionRequiresDocker("run", qemuBuild) {
		t.Fatal("qemu bundle builds should require Docker packaging helper")
	}
	if executionRequiresDocker("exec", qemuBuild) {
		t.Fatal("qemu unsupported exec path should not fail at Docker preflight")
	}
}

func TestAutoBackgroundForIDE(t *testing.T) {
	ideProfile := model.Profile{Name: "theia", PostStart: &model.PostStartActions{OpenIDE: "theia"}}

	cases := []struct {
		name    string
		action  string
		opts    model.Options
		profile model.Profile
		want    bool
	}{
		{"bare run of IDE profile is forced detached", "run", model.Options{}, ideProfile, true},
		{"already background is left alone", "run", model.Options{RunOptions: model.RunOptions{Background: true}}, ideProfile, false},
		{"explicit shell keeps the container shell", "shell", model.Options{RunOptions: model.RunOptions{Shell: true}}, ideProfile, false},
		{"exec is never auto-backgrounded", "exec", model.Options{}, ideProfile, false},
		{"non-IDE profile is untouched", "run", model.Options{}, model.Profile{Name: "claude"}, false},
		{"unsupported open_ide value is untouched", "run", model.Options{}, model.Profile{Name: "x", PostStart: &model.PostStartActions{OpenIDE: "vscode"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoBackgroundForIDE(tc.action, tc.opts, tc.profile); got != tc.want {
				t.Fatalf("autoBackgroundForIDE = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEnsureExistingRuntimeImageWith(t *testing.T) {
	t.Run("returns nil when image exists", func(t *testing.T) {
		err := ensureExistingRuntimeImageWith("enclave:latest", func(context.Context, string) (bool, error) {
			return true, nil
		})
		if err != nil {
			t.Fatalf("ensureExistingRuntimeImageWith returned error: %v", err)
		}
	})

	t.Run("returns actionable error when image is missing", func(t *testing.T) {
		err := ensureExistingRuntimeImageWith("enclave:latest", func(context.Context, string) (bool, error) {
			return false, nil
		})
		if err == nil {
			t.Fatal("expected missing-image error, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "does not exist locally") || !strings.Contains(msg, "--no-rebuild") || !strings.Contains(msg, "--rebuild") {
			t.Fatalf("unexpected error message: %q", msg)
		}
	})

	t.Run("propagates inspect errors", func(t *testing.T) {
		wantErr := errors.New("inspect failed")
		err := ensureExistingRuntimeImageWith("enclave:latest", func(context.Context, string) (bool, error) {
			return false, wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected wrapped inspect error %v, got %v", wantErr, err)
		}
	})
}
