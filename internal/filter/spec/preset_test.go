// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package spec

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"enclave/internal/filter"
)

func loadFixture(t *testing.T) *Filter {
	t.Helper()
	f, err := Load(fixtureDir, "gws")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return f
}

func TestResolveShippedPresets(t *testing.T) {
	f := loadFixture(t)
	p, err := f.ResolvePreset("send-eclipsesource", "")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Shipped || p.Description != "Read, and send mail only to eclipsesource.com." || len(p.Chain) != 2 {
		t.Fatalf("preset = %+v", p)
	}
	if got := p.Capabilities["mail.send"]["recipients"].Values; !reflect.DeepEqual(got, []string{"@eclipsesource.com"}) {
		t.Fatalf("recipients = %v", got)
	}
	// Inherited from readonly, with the files.read default applied.
	if _, ok := p.Capabilities["mail.read"]; !ok || !p.Capabilities["files.read"]["resources"].Any {
		t.Fatalf("capabilities = %v", p.Capabilities)
	}
	if !reflect.DeepEqual(p.Quotas, map[string]int64{"mail.read": 200, "mail.sent": 20}) {
		t.Fatalf("quotas = %v", p.Quotas)
	}

	editAll, err := f.ResolvePreset("edit-all", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := editAll.Capabilities["files.read"]; ok {
		t.Fatal("deny did not drop the inherited files.read")
	}
	if !editAll.Capabilities["labels.modify"]["labels"].Any {
		t.Fatalf("labels default missing: %v", editAll.Capabilities["labels.modify"])
	}
}

func TestUserPresetShadowsShipped(t *testing.T) {
	f := loadFixture(t)
	user := t.TempDir()
	writeFile(t, filepath.Join(user, "readonly.yaml"), "description: Mine.\nallow:\n  mail.read: {}\nquotas:\n  mail.read: 5\n")
	p, err := f.ResolvePreset("readonly", user)
	if err != nil {
		t.Fatal(err)
	}
	if p.Shipped || p.Description != "Mine." || p.Quotas["mail.read"] != 5 {
		t.Fatalf("preset = %+v", p)
	}
	if _, ok := p.Capabilities["files.read"]; ok {
		t.Fatal("shadowed preset still inherits from the shipped one")
	}
	// A shipped preset extending readonly picks up the user's readonly.
	send, err := f.ResolvePreset("send-eclipsesource", user)
	if err != nil {
		t.Fatal(err)
	}
	if send.Quotas["mail.read"] != 5 {
		t.Fatalf("extends did not use the user preset: %v", send.Quotas)
	}
}

func TestUserPresetExtendingItsOwnNameExtendsShipped(t *testing.T) {
	f := loadFixture(t)
	user := t.TempDir()
	writeFile(t, filepath.Join(user, "readonly.yaml"), "extends: readonly\nquotas:\n  mail.read: 10\n")
	p, err := f.ResolvePreset("readonly", user)
	if err != nil {
		t.Fatal(err)
	}
	if p.Quotas["mail.read"] != 10 || !p.Capabilities["files.read"]["resources"].Any || len(p.Chain) != 2 {
		t.Fatalf("preset = %+v", p)
	}
	want := []PresetLink{
		{Name: "readonly", Path: filepath.Join(user, "readonly.yaml")},
		{Name: "readonly", Path: filepath.Join(fixtureDir, PresetsDir, "readonly.yaml"), Shipped: true},
	}
	if !reflect.DeepEqual(p.Chain, want) {
		t.Fatalf("chain = %+v, want %+v", p.Chain, want)
	}
}

func TestUnknownPresetListsAvailablePresets(t *testing.T) {
	f := loadFixture(t)
	user := t.TempDir()
	writeFile(t, filepath.Join(user, "mine.yaml"), "extends: readonly\n")
	_, err := f.ResolvePreset("nope", user)
	if err == nil || err.Error() != `filter gws has no preset "nope" (available: edit-all, folder-edit, mine, readonly, send-eclipsesource)` {
		t.Fatalf("ResolvePreset = %v", err)
	}
	writeFile(t, filepath.Join(user, "self.yaml"), "extends: self\n")
	if _, err := f.ResolvePreset("self", user); err == nil || !strings.HasSuffix(err.Error(), `ships no preset "self" (available: edit-all, folder-edit, readonly, send-eclipsesource)`) {
		t.Fatalf("ResolvePreset = %v", err)
	}
}

func TestPresetExtendsErrors(t *testing.T) {
	f := loadFixture(t)
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"cycle", map[string]string{"a": "extends: b\n", "b": "extends: a\n"}, "extends cycle"},
		{"self", map[string]string{"a": "extends: a\n"}, "extends: filter gws ships no preset \"a\""},
		{"missing parent", map[string]string{"a": "extends: nope\n"}, "extends: filter gws has no preset \"nope\""},
		{"too deep", map[string]string{
			"p1": "extends: p2\n", "p2": "extends: p3\n", "p3": "extends: p4\n", "p4": "extends: p5\n",
			"p5": "extends: p6\n", "p6": "extends: p7\n", "p7": "extends: p8\n", "p8": "extends: p9\n", "p9": "allow:\n  mail.read: {}\n",
		}, "longer than 8 presets"},
		{"cycle through shadow", map[string]string{"readonly": "extends: readonly\n", "send-eclipsesource": "extends: readonly\n"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := t.TempDir()
			first := ""
			for name, content := range tt.files {
				writeFile(t, filepath.Join(user, name+".yaml"), content)
			}
			for _, candidate := range []string{"a", "p1", "send-eclipsesource"} {
				if _, ok := tt.files[candidate]; ok {
					first = candidate
				}
			}
			_, err := f.ResolvePreset(first, user)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("ResolvePreset = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ResolvePreset = %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := f.ResolvePreset("Bad", ""); err == nil {
		t.Fatal("invalid preset name accepted")
	}
	if _, err := f.ResolvePreset("missing", t.TempDir()); err == nil || !strings.Contains(err.Error(), `no preset "missing"`) {
		t.Fatalf("missing preset = %v", err)
	}
}

func TestShippedPresetCannotExtendItself(t *testing.T) {
	dir := copyDir(t, fixtureDir)
	writeFile(t, filepath.Join(dir, PresetsDir, "loop.yaml"), "extends: loop\n")
	if _, err := Load(dir, "gws"); err == nil || !strings.Contains(err.Error(), "loop.yaml: extends: a preset cannot extend itself") {
		t.Fatalf("Load = %v", err)
	}
}

func TestPresetMaxChainAllowed(t *testing.T) {
	f := loadFixture(t)
	user := t.TempDir()
	for i := 1; i < MaxPresetChain; i++ {
		writeFile(t, filepath.Join(user, fmt.Sprintf("p%d.yaml", i)), fmt.Sprintf("extends: p%d\n", i+1))
	}
	writeFile(t, filepath.Join(user, fmt.Sprintf("p%d.yaml", MaxPresetChain)), "allow:\n  mail.read: {}\n")
	p, err := f.ResolvePreset("p1", user)
	if err != nil || len(p.Chain) != MaxPresetChain {
		t.Fatalf("ResolvePreset = %v, %v", p, err)
	}
}

func TestPresetValidationErrors(t *testing.T) {
	f := loadFixture(t)
	tests := []struct {
		name string
		body string
		want string
	}{
		{"unknown field", "allows: {}\n", "unknown field \"allows\""},
		{"key case", "allow:\n  mail.read: {}\nAllow:\n  mail.send: { recipients: \"*\" }\n", "unknown field \"Allow\" (did you mean \"allow\"?)"},
		{"quotas key case", "Quotas:\n  mail.read: 1\n", "unknown field \"Quotas\" (did you mean \"quotas\"?)"},
		{"constraint name case", "allow:\n  mail.send: { Recipients: \"*\" }\n", "allow.mail.send: unknown constraint \"Recipients\" (did you mean \"recipients\"?)"},
		{"resource key case", "allow:\n  files.edit: { resources: { IDs: [x] } }\n", "resources: unknown key \"IDs\", use ids and within"},
		{"unknown capability", "allow:\n  mail.write: {}\n", "allow.mail.write: unknown capability"},
		{"public in allow", "allow:\n  discovery: {}\n", "allow.discovery: discovery is public"},
		{"public in deny", "deny: [discovery]\n", "deny: discovery is public"},
		{"unknown in deny", "deny: [mail.write]\n", "deny: unknown capability"},
		{"allow and deny", "allow:\n  mail.read: {}\ndeny: [mail.read]\n", "deny: mail.read is also in allow"},
		{"duplicate deny", "deny: [mail.read, mail.read]\n", "deny: duplicate"},
		{"false", "allow:\n  mail.read: false\n", "allow.mail.read: false is not allowed"},
		{"null", "allow:\n  mail.read:\n", "allow.mail.read: use {}, true"},
		{"list", "allow:\n  mail.read: [x]\n", "allow.mail.read: use {}, true"},
		{"unknown constraint", "allow:\n  mail.send: { to: \"*\" }\n", "allow.mail.send: unknown constraint \"to\""},
		{"email shape", "allow:\n  mail.send: { recipients: { ids: [x] } }\n", "allow.mail.send: recipients: use \"*\" or a list"},
		{"email value", "allow:\n  mail.send: { recipients: [\"eclipsesource.com\"] }\n", "recipients: email pattern"},
		{"email not string", "allow:\n  mail.send: { recipients: [1] }\n", "recipients: list elements must be strings"},
		{"resource shape", "allow:\n  files.edit: { resources: [\"x\"] }\n", "resources: use \"*\" or {ids"},
		{"resource empty", "allow:\n  files.edit: { resources: {} }\n", "resources: must not be empty"},
		{"resource ids type", "allow:\n  files.edit: { resources: { ids: x } }\n", "ids must be a list"},
		{"value shape", "allow:\n  labels.modify: { labels: { ids: [x] } }\n", "labels: use \"*\" or a list of strings"},
		{"value empty", "allow:\n  labels.modify: { labels: [] }\n", "labels: must not be empty"},
		{"missing constraint value", "allow:\n  mail.send: {}\n", "enables mail.send without a value for constraint recipients"},
		{"unknown counter", "quotas:\n  mail.write: 1\n", "quotas: unknown counter"},
		{"negative quota", "quotas:\n  mail.read: -1\n", "quotas.mail.read: must not be negative"},
		{"fraction quota", "quotas:\n  mail.read: 1.5\n", "parse"},
		{"bad extends", "extends: Read\n", "extends: invalid name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := t.TempDir()
			path := filepath.Join(user, "custom.yaml")
			writeFile(t, path, tt.body)
			_, err := f.ResolvePreset("custom", user)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ResolvePreset = %v, want %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("error does not name the preset file: %v", err)
			}
		})
	}
}

func TestPresetsListing(t *testing.T) {
	f := loadFixture(t)
	user := t.TempDir()
	writeFile(t, filepath.Join(user, "readonly.yaml"), "description: Mine.\n")
	writeFile(t, filepath.Join(user, "mine.yaml"), "description: Custom.\n")
	writeFile(t, filepath.Join(user, "Bad.yaml"), "description: skipped\n")
	list, err := f.Presets(user)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list {
		names = append(names, fmt.Sprintf("%s:%v:%v", p.Name, p.Shipped, p.Shadows))
	}
	want := []string{"edit-all:true:false", "folder-edit:true:false", "mine:false:false", "readonly:false:true", "send-eclipsesource:true:false"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("Presets = %v, want %v", names, want)
	}
}

func TestScopes(t *testing.T) {
	f := loadFixture(t)
	const prefix = "https://www.googleapis.com/auth/"
	if got := f.FullScope("drive"); got != prefix+"drive" {
		t.Fatalf("FullScope = %q", got)
	}
	if got := f.FullScope("https://mail.google.com/"); got != "https://mail.google.com/" {
		t.Fatalf("FullScope kept URL = %q", got)
	}
	expanded := f.ExpandScopes([]string{"drive"})
	for _, s := range []string{"drive", "drive.readonly", "drive.metadata.readonly", "drive.file"} {
		if !expanded[prefix+s] {
			t.Errorf("ExpandScopes(drive) misses %s", s)
		}
	}
	if missing := f.MissingScopes([]string{prefix + "drive"}, []string{"drive.readonly", "gmail.readonly"}); !reflect.DeepEqual(missing, []string{prefix + "gmail.readonly"}) {
		t.Fatalf("MissingScopes = %v", missing)
	}
	if !f.Covers([]string{prefix + "gmail.modify"}, []string{prefix + "gmail.readonly", prefix + "gmail.send"}) {
		t.Fatal("gmail.modify does not cover readonly and send")
	}
	if f.Covers([]string{prefix + "drive.readonly"}, []string{prefix + "drive"}) {
		t.Fatal("narrow scope covers a broad one")
	}
	if got := f.MinimizeScopes([]string{"drive.readonly", "drive", "gmail.readonly", "drive.metadata.readonly"}); !reflect.DeepEqual(got, []string{prefix + "drive", prefix + "gmail.readonly"}) {
		t.Fatalf("MinimizeScopes = %v", got)
	}
}

func TestRequiredScopes(t *testing.T) {
	f := loadFixture(t)
	const prefix = "https://www.googleapis.com/auth/"
	tests := map[string][]string{
		"readonly":           {prefix + "drive.readonly", prefix + "gmail.readonly"},
		"send-eclipsesource": {prefix + "drive.readonly", prefix + "gmail.readonly", prefix + "gmail.send"},
		// within uses the driveParents resolver, which needs drive.metadata.readonly.
		"folder-edit": {prefix + "drive", prefix + "drive.file", prefix + "drive.metadata.readonly", prefix + "drive.readonly", prefix + "gmail.modify", prefix + "gmail.readonly"},
		// "*" constraints never call the resolver.
		"edit-all": {prefix + "drive", prefix + "drive.file", prefix + "gmail.modify", prefix + "gmail.readonly"},
	}
	for name, want := range tests {
		p, err := f.ResolvePreset(name, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := f.RequiredScopes(p); !reflect.DeepEqual(got, want) {
			t.Errorf("RequiredScopes(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestSelectGrant(t *testing.T) {
	f := loadFixture(t)
	const prefix = "https://www.googleapis.com/auth/"
	now := mustTime(t, "2026-11-01T00:00:00Z")
	grants := []GrantCandidate{
		{ID: "wide", Scopes: []string{prefix + "drive", prefix + "gmail.modify", prefix + "calendar"}, CreatedAt: now},
		{ID: "old", Scopes: []string{prefix + "drive.readonly", prefix + "gmail.readonly"}, CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "new", Scopes: []string{prefix + "drive.readonly", prefix + "gmail.readonly"}, CreatedAt: now},
		{ID: "narrow", Scopes: []string{prefix + "gmail.readonly"}, CreatedAt: now},
	}
	required := []string{prefix + "drive.readonly", prefix + "gmail.readonly"}
	got, ok := f.SelectGrant(grants, required)
	if !ok || got.ID != "new" {
		t.Fatalf("SelectGrant = %v, %v, want new", got.ID, ok)
	}
	if _, ok := f.SelectGrant(grants[3:], required); ok {
		t.Fatal("SelectGrant found a grant that does not cover")
	}
}

func TestCompileFixture(t *testing.T) {
	f := loadFixture(t)
	p, err := f.ResolvePreset("folder-edit", "")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := Compile(f, p, "ENCLAVE_SECRET_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if pol.Preset != "folder-edit" || pol.JSONKeys != filter.JSONKeysProtobuf || len(pol.Routes) != len(f.Routes) {
		t.Fatalf("policy = %+v", pol)
	}
	if c := pol.Capabilities["files.edit"]; !c.Enabled || c.Constraints["resources"].Value.Within[0] != "FOLDER1" || c.Constraints["resources"].Resolver != "driveParents" {
		t.Fatalf("files.edit = %+v", c)
	}
	if c := pol.Capabilities["mail.send"]; c.Enabled || len(c.Constraints) != 0 {
		t.Fatalf("mail.send = %+v", c)
	}
	if c := pol.Capabilities["discovery"]; !c.Enabled || !c.Public {
		t.Fatalf("discovery = %+v", c)
	}
	if q := pol.Counters["files.written"].Quota; q == nil || *q != 50 {
		t.Fatalf("files.written quota = %v", q)
	}
	if q := pol.Counters["mail.sent"].Quota; q != nil {
		t.Fatalf("mail.sent quota = %v", *q)
	}
	data, err := pol.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := filter.ParsePolicy(data); err != nil {
		t.Fatalf("compiled policy does not load: %v", err)
	}
	if _, err := Compile(f, p, "short"); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("Compile with a short placeholder = %v", err)
	}
	if _, err := Compile(f, nil, "x"); err == nil {
		t.Fatal("Compile without preset succeeded")
	}
}

func TestNewCredentials(t *testing.T) {
	f := loadFixture(t)
	cred, err := f.NewOAuth2Credential("id", "secret", "refresh", "readonly", []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	if cred.OAuth2.TokenURL != "https://oauth2.googleapis.com/token" || cred.Filter != "gws" {
		t.Fatalf("credential = %+v", cred.OAuth2.TokenURL)
	}
	if _, err := f.NewOAuth2Credential("id", "secret", "", "g", nil); err == nil {
		t.Fatal("empty refresh token accepted")
	}
	if _, err := f.NewStaticCredential("x"); err == nil {
		t.Fatal("static credential for an oauth2 filter accepted")
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestCompileWildcardOnlyOnDisabledCapability(t *testing.T) {
	f := loadFixture(t)
	idx := -1
	for i, r := range f.Routes {
		if r.Op == "gmail.messages.send" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("fixture has no gmail.messages.send route")
	}
	f.Routes[idx].Bind = nil
	f.Routes[idx].WildcardOnly = []string{"recipients"}
	for _, preset := range []string{"readonly", "send-eclipsesource"} {
		p, err := f.ResolvePreset(preset, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Compile(f, p, "ENCLAVE_SECRET_0123456789abcdef0123456789abcdef"); err != nil {
			t.Fatalf("Compile(%s): %v", preset, err)
		}
	}
}
