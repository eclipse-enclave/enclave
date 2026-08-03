// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"testing"

	"enclave/internal/config"
)

func TestParseProjectTagSet(t *testing.T) {
	res, err := Parse([]string{"project", "tag", "set", "enclave", "--yes"}, config.DefaultOptions())
	if err != nil {
		t.Fatalf("parse project tag set: %v", err)
	}
	if res.Action != "project-tag-set" || res.ProjectTag != "enclave" || !res.ProjectYes {
		t.Fatalf("unexpected project tag set result: %+v", res)
	}
	if res.ProjectTagNew || res.ProjectTagExisting {
		t.Fatalf("intent flags set without being passed: %+v", res)
	}
}

func TestParseProjectTagSetIntentFlags(t *testing.T) {
	res, err := Parse([]string{"project", "tag", "set", "enclave", "--existing"}, config.DefaultOptions())
	if err != nil {
		t.Fatalf("parse project tag set --existing: %v", err)
	}
	if !res.ProjectTagExisting || res.ProjectTagNew {
		t.Fatalf("unexpected intent flags: %+v", res)
	}

	if _, err := Parse([]string{"project", "tag", "set", "enclave", "--new", "--existing"}, config.DefaultOptions()); err == nil {
		t.Fatal("expected --new and --existing to be mutually exclusive")
	}
}

func TestParseProjectTagList(t *testing.T) {
	res, err := Parse([]string{"project", "tag", "list", "--json"}, config.DefaultOptions())
	if err != nil {
		t.Fatalf("parse project tag list: %v", err)
	}
	if res.Action != "project-tag-list" || !res.ProjectJSON {
		t.Fatalf("unexpected project tag list result: %+v", res)
	}
}

func TestParseProjectShowJSON(t *testing.T) {
	res, err := Parse([]string{"project", "show", "--json"}, config.DefaultOptions())
	if err != nil {
		t.Fatalf("parse project show: %v", err)
	}
	if res.Action != "project-show" || !res.ProjectJSON {
		t.Fatalf("unexpected project show result: %+v", res)
	}
}

func TestParseProjectTagUnsetPath(t *testing.T) {
	res, err := Parse([]string{"project", "tag", "unset", "/tmp/old-project", "--yes"}, config.DefaultOptions())
	if err != nil {
		t.Fatalf("parse project tag unset: %v", err)
	}
	if res.Action != "project-tag-unset" || res.ProjectPath != "/tmp/old-project" || !res.ProjectYes {
		t.Fatalf("unexpected project tag unset result: %+v", res)
	}
}

// The pre-release grammar (`project tag <name>`, `project untag`) must not
// survive as silent aliases; a tag can never be named like a subcommand.
func TestParseProjectRejectsRetiredGrammar(t *testing.T) {
	for _, args := range [][]string{
		{"project", "tag", "enclave"},
		{"project", "untag"},
		{"project", "untag", "/tmp/old-project"},
	} {
		if _, err := Parse(args, config.DefaultOptions()); err == nil {
			t.Fatalf("expected %v to be rejected", args)
		}
	}
}
