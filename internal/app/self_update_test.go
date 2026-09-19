// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"enclave/internal/cli"
	"enclave/internal/extinstall"
	"enclave/internal/model"
)

func TestAutomaticSelfUpdateAllowedSkipsMachineOutput(t *testing.T) {
	for _, parsed := range []cli.Result{
		{Action: "config", ConfigView: model.ConfigView{JSON: true}},
		{Action: "ps", Options: model.Options{PSOptions: model.PSOptions{PSJSON: true}}},
		{Action: "status", Options: model.Options{StatusOptions: model.StatusOptions{StatusJSON: true}}},
		{Action: "network-log", NetworkLogView: model.NetworkLogView{JSON: true}},
		{Action: "tools", ExtRequest: &extinstall.Request{JSON: true}},
		{Action: "review-target"},
		{Action: "user-command"},
	} {
		if automaticSelfUpdateAllowed(parsed) {
			t.Errorf("automatic update allowed for machine-oriented action %q", parsed.Action)
		}
	}
	if !automaticSelfUpdateAllowed(cli.Result{Action: "run"}) {
		t.Fatal("automatic update was not allowed for a normal run")
	}
}

func TestSelfUpdateCheckDue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "self-update-check")
	now := time.Now()
	if !selfUpdateCheckDue(path, now) {
		t.Fatal("missing stamp was not due")
	}
	if err := os.WriteFile(path, []byte("checked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if selfUpdateCheckDue(path, now) {
		t.Fatal("one-hour-old stamp was due")
	}
	if err := os.Chtimes(path, now.Add(-25*time.Hour), now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !selfUpdateCheckDue(path, now) {
		t.Fatal("25-hour-old stamp was not due")
	}
}
