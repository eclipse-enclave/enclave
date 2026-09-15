// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"os"
	"testing"

	"enclave/internal/extinstall"

	"enclave/internal/model"
)

func TestExtensionExitCode(t *testing.T) {
	if code := extensionExitCode([]extinstall.ActionResult{{Action: extinstall.ActionInstalled}}, nil); code != 0 {
		t.Errorf("success exit code = %d", code)
	}
	if code := extensionExitCode([]extinstall.ActionResult{{Action: extinstall.ActionFailed}}, nil); code != 1 {
		t.Errorf("partial failure exit code = %d", code)
	}
	if code := extensionExitCode(nil, errors.New("boom")); code != 1 {
		t.Errorf("error exit code = %d", code)
	}
}

func TestExtensionErrorLine(t *testing.T) {
	cases := []struct {
		name   string
		result extinstall.ActionResult
		want   string
	}{
		{
			name:   "per-extension failure",
			result: extinstall.ActionResult{Name: "foo", Error: "has local modifications; pass --force to discard them"},
			want:   "foo: has local modifications; pass --force to discard them",
		},
		{
			name:   "aborted prompt",
			result: extinstall.ActionResult{Name: "foo", Error: "aborted; nothing installed"},
			want:   "foo: aborted; nothing installed",
		},
		{
			name:   "whole-run failure",
			result: extinstall.ActionResult{Error: "acme/kits contains no feature extension"},
			want:   "acme/kits contains no feature extension",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extensionErrorLine(tc.result); got != tc.want {
				t.Errorf("extensionErrorLine(%+v) = %q, want %q", tc.result, got, tc.want)
			}
		})
	}
}

func TestNarrationWriter(t *testing.T) {
	if w := narrationWriter(extinstall.Request{JSON: true}); w != nil {
		t.Errorf("narrationWriter(JSON=true) = %v, want nil", w)
	}
	if w := narrationWriter(extinstall.Request{JSON: false}); w != os.Stdout {
		t.Errorf("narrationWriter(JSON=false) = %v, want os.Stdout", w)
	}
}

func TestRunExtensionManageRejectsMissingRequest(t *testing.T) {
	if code := runExtensionManage(nil, nil); code != 1 {
		t.Errorf("runExtensionManage(nil request) = %d, want 1", code)
	}
}

// OpList has no installer entry point; listing is served elsewhere.
func TestRunExtensionManageRejectsUnknownOp(t *testing.T) {
	req := &extinstall.Request{Kind: model.KindFeature, Op: extinstall.OpList}
	if code := runExtensionManage(nil, req); code != 1 {
		t.Errorf("runExtensionManage(OpList) = %d, want 1", code)
	}
}

func TestExtensionOpsCoverEveryManagementOp(t *testing.T) {
	for _, op := range []extinstall.Op{extinstall.OpAdd, extinstall.OpUpdate, extinstall.OpRemove} {
		if extensionOps[op] == nil {
			t.Errorf("extensionOps has no handler for %q", op)
		}
	}
}
