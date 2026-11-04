// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
	mountutil "enclave/internal/mounts"
)

func TestWorktreeMountCannotInjectAnotherSource(t *testing.T) {
	project := t.TempDir()
	pointer := "x,source=" + t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, pointer), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".git"), []byte("gitdir: "+pointer+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var neutral []backend.Mount
	var seen []string
	mountutil.AddWorktree(&neutral, model.Project{Dir: project, RealDir: project}, &seen, false, mountutil.NewSensitivePaths(t.TempDir(), nil, nil))
	if len(neutral) != 1 || !strings.Contains(neutral[0].Source, ",source=") {
		t.Fatalf("test did not reach the serialization boundary: %v", neutral)
	}
	m := neutral[0]
	if args, err := mountFlags([]Mount{{Type: MountTypeBind, Source: m.Source, Target: m.ContainerPath}}); err == nil || len(args) != 0 {
		t.Fatalf("injected mount rendered: args=%v, err=%v", args, err)
	}
}

func TestMountFieldsRejectEngineDelimiters(t *testing.T) {
	for _, value := range []string{"/path,source=/outside", "/path\"quoted", "/path\nnext", "/path\rnext"} {
		for _, target := range []bool{false, true} {
			m := Mount{Type: MountTypeBind, Source: "/source", Target: "/target"}
			if target {
				m.Target = value
			} else {
				m.Source = value
			}
			args, err := mountFlags([]Mount{m})
			if err == nil || len(args) != 0 {
				t.Fatalf("accepted unsafe --mount field: args=%v, err=%v", args, err)
			}
			if !strings.Contains(err.Error(), strconv.Quote(value)) {
				t.Fatalf("error does not name the path %q: %v", value, err)
			}
		}
	}
	for _, target := range []bool{false, true} {
		m := Mount{Type: MountTypeBind, Source: "/source", Target: "/target", CreateSourceDir: true}
		if target {
			m.Target += ":other"
		} else {
			m.Source += ":other"
		}
		if _, err := mountFlags([]Mount{m}); err == nil || !strings.Contains(err.Error(), ":other") {
			t.Fatalf("source-creating bind with a colon: %v", err)
		}
		if _, _, err := SplitMountsForSELinuxWith([]Mount{m}, true); err == nil || !strings.Contains(err.Error(), ":other") {
			t.Fatalf("SELinux bind with a colon: %v", err)
		}
	}
}

func TestInvalidMountNeverInvokesEngine(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "engine-called")
	t.Setenv("MOUNT_CLI_LOG", marker)
	installNetworkDockerStub(t, `printf called > "$MOUNT_CLI_LOG"`)
	config := &ContainerConfig{Image: "synthetic"}
	host := &HostConfig{Mounts: []Mount{{Type: MountTypeBind, Source: "/project/x,source=/outside", Target: "/work"}}}
	ctx := context.Background()
	runs := map[string]func() error{
		"run": func() error { return Run(ctx, config, host, "") },
		"streams": func() error {
			_, err := RunWithIOAndStartHook(ctx, config, host, "", nil, io.Discard, io.Discard, false, "", nil)
			return err
		},
		"capture":              func() error { _, err := RunCapture(ctx, config, host, ""); return err },
		"interactive":          func() error { return RunInteractive(ctx, config, host, "") },
		"detached":             func() error { _, err := RunDetached(ctx, config, host, ""); return err },
		"detached interactive": func() error { _, err := RunDetachedInteractive(ctx, config, host, ""); return err },
		"create":               func() error { _, err := ContainerCreate(ctx, config, host, ""); return err },
	}
	for name, run := range runs {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil || !strings.Contains(err.Error(), `"/project/x,source=/outside" contains a comma`) {
				t.Fatalf("invalid mount did not fail before engine invocation: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("engine was invoked: %v", err)
			}
		})
	}
}
