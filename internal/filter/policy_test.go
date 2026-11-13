// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testPlaceholder = "ENCLAVE_SECRET_0123456789abcdef0123456789abcdef"

func int64p(v int64) *int64 { return &v }

func intp(v int) *int { return &v }

func validPolicy() *Policy {
	return &Policy{
		SchemaVersion:  PolicySchemaVersion,
		Kind:           PolicyKind,
		Filter:         "gws",
		Preset:         "readonly",
		CredentialType: CredentialOAuth2,
		Hosts:          []string{"gmail.googleapis.com", "www.googleapis.com", "*.example.com"},
		Placeholder:    testPlaceholder,
		Inject:         Inject{Header: "authorization", Format: "Bearer %s"},
		DenyHeaders:    []string{"x-goog-api-key"},
		DenyParams:     []string{"access_token"},
		Capabilities: map[string]Capability{
			"discovery": {Public: true, Enabled: true},
			"mail.read": {Enabled: true},
			"mail.send": {Enabled: true, Constraints: map[string]Constraint{
				"recipients": {Kind: KindEmail, Var: "recipients", Value: ConstraintValue{Values: []string{"@example.com"}}},
			}},
			"files.edit": {Enabled: true, Constraints: map[string]Constraint{
				"resources": {Kind: KindResource, Var: "fileId", Resolver: "driveParents", Value: ConstraintValue{Within: []string{"F"}}},
			}},
			"files.delete": {},
		},
		Counters: map[string]Counter{"mail.read": {Quota: int64p(5)}, "mail.sent": {}},
		Resolvers: map[string]Resolver{
			"driveParents": {URL: "https://www.googleapis.com/drive/v3/files/{id}?fields=parents", Extract: "$.parents[*]", Recursive: true, MaxDepth: 8},
		},
		Routes: []Route{
			{Op: "discovery.get", Capability: "discovery", Methods: []string{"GET"}, Hosts: []string{"www.googleapis.com"}, Path: "/discovery/v1/apis/{api}/{version}/rest", MaxBodyBytes: DefaultMaxBodyBytes, MaxResponseBytes: DefaultMaxResponseBytes},
			{Op: "gmail.messages.get", Capability: "mail.read", Methods: []string{"GET"}, Hosts: []string{"gmail.googleapis.com"}, Path: "/gmail/v1/users/me/messages/{id}",
				Query: map[string]QueryCondition{"format": {Absent: true, Values: []string{"full"}}},
				Count: []Count{{Counter: "mail.read", Key: "id"}}, MaxBodyBytes: DefaultMaxBodyBytes, MaxResponseBytes: DefaultMaxResponseBytes},
			{Op: "gmail.threads.get", Capability: "mail.read", Methods: []string{"GET"}, Hosts: []string{"gmail.googleapis.com"}, Path: "/gmail/v1/users/me/threads/{id}",
				Bind:  map[string][]BindStep{"messageIds": {{Op: StepResponse}, {Op: StepJSON, Arg: "$.messages[*].id"}}},
				Count: []Count{{Counter: "mail.read", Key: "messageIds"}}, MaxBodyBytes: DefaultMaxBodyBytes, MaxResponseBytes: DefaultMaxResponseBytes},
			{Op: "gmail.messages.send", Capability: "mail.send", Methods: []string{"POST"}, Hosts: []string{"gmail.googleapis.com"}, Path: "/gmail/v1/users/me/messages/send",
				Bind:  map[string][]BindStep{"recipients": {{Op: StepBody}, {Op: StepJSON, Arg: "$.raw"}, {Op: StepBase64URL}, {Op: StepRFC822, Arg: RFC822Recipients}}},
				Count: []Count{{Counter: "mail.sent"}}, MaxBodyBytes: DefaultMaxBodyBytes, MaxResponseBytes: DefaultMaxResponseBytes},
			{Op: "drive.files.update", Capability: "files.edit", Methods: []string{"PATCH"}, Hosts: []string{"www.googleapis.com", "*.example.com"}, Path: "/drive/v3/files/{fileId}",
				JSON:         map[string]JSONCondition{"$.trashed": {Absent: true, Values: []json.RawMessage{json.RawMessage("false")}}},
				MaxBodyBytes: DefaultMaxBodyBytes, MaxResponseBytes: DefaultMaxResponseBytes},
		},
		Deny: &DenyTemplate{ContentType: "application/json", Body: `{"error":{"code":{{code}},"message":{{message}}}}`},
	}
}

func TestPolicyRoundTrip(t *testing.T) {
	p := validPolicy()
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	data, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	again, _ := loaded.Marshal()
	if string(again) != string(data) {
		t.Fatalf("round trip changed the policy:\n%s\n%s", data, again)
	}
}

func TestParsePolicyFailsClosed(t *testing.T) {
	data, _ := validPolicy().Marshal()
	var generic map[string]any
	_ = json.Unmarshal(data, &generic)
	generic["futureField"] = true
	withUnknown, _ := json.Marshal(generic)
	tests := map[string][]byte{
		"unknown field": withUnknown,
		"trailing data": append(append([]byte(nil), data...), []byte(" {}")...),
		"not json":      []byte("policy"),
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePolicy(doc); err == nil {
				t.Fatal("ParsePolicy error = nil")
			}
		})
	}
	if _, err := LoadPolicy(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("LoadPolicy of a missing file succeeded")
	}
	if _, err := LoadPolicy(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("LoadPolicy of a directory = %v", err)
	}
}

func TestPolicyValidateErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(p *Policy)
		want   string
	}{
		{"schema", func(p *Policy) { p.SchemaVersion = "2" }, "schemaVersion"},
		{"kind", func(p *Policy) { p.Kind = "x" }, "kind"},
		{"filter name", func(p *Policy) { p.Filter = "Bad" }, "filter"},
		{"preset name", func(p *Policy) { p.Preset = "" }, "preset"},
		{"credential type", func(p *Policy) { p.CredentialType = "basic" }, "credentialType"},
		{"no hosts", func(p *Policy) { p.Hosts = nil }, "hosts: must list"},
		{"bad host", func(p *Policy) { p.Hosts = []string{"https://x"} }, "invalid host"},
		{"unnormalized host", func(p *Policy) { p.Hosts = []string{"Gmail.googleapis.com"} }, "not normalized"},
		{"duplicate host", func(p *Policy) { p.Hosts = append(p.Hosts, "gmail.googleapis.com") }, "duplicate host"},
		{"broad wildcard", func(p *Policy) { p.Hosts = []string{"*.com"} }, "invalid host"},
		{"short placeholder", func(p *Policy) { p.Placeholder = "ENCLAVE_SECRET_x" }, "placeholder"},
		{"placeholder space", func(p *Policy) { p.Placeholder = testPlaceholder + " x" }, "placeholder"},
		{"inject header missing", func(p *Policy) { p.Inject.Header = "" }, "inject: header is required"},
		{"inject header upper", func(p *Policy) { p.Inject.Header = "Authorization" }, "lowercase"},
		{"inject header invalid", func(p *Policy) { p.Inject.Header = "a b" }, "invalid header"},
		{"inject format no verb", func(p *Policy) { p.Inject.Format = "Bearer" }, "%s exactly once"},
		{"inject format two verbs", func(p *Policy) { p.Inject.Format = "%s %s" }, "%s exactly once"},
		{"inject format other verb", func(p *Policy) { p.Inject.Format = "%d %s" }, "%s exactly once"},
		{"inject format newline", func(p *Policy) { p.Inject.Format = "Bearer\n%s" }, "printable"},
		{"deny header is inject", func(p *Policy) { p.DenyHeaders = []string{"authorization"} }, "injection header"},
		{"deny header upper", func(p *Policy) { p.DenyHeaders = []string{"X-Key"} }, "lowercase"},
		{"deny header dup", func(p *Policy) { p.DenyHeaders = []string{"x", "x"} }, "duplicate"},
		{"deny param empty", func(p *Policy) { p.DenyParams = []string{""} }, "invalid parameter"},
		{"deny param dup", func(p *Policy) { p.DenyParams = []string{"k", "k"} }, "duplicate"},
		{"counter name", func(p *Policy) { p.Counters["Bad"] = Counter{} }, "counters"},
		{"negative quota", func(p *Policy) { p.Counters["mail.read"] = Counter{Quota: int64p(-1)} }, "negative"},
		{"resolver name", func(p *Policy) { p.Resolvers["bad name"] = p.Resolvers["driveParents"] }, "resolvers"},
		{"resolver depth", func(p *Policy) {
			r := p.Resolvers["driveParents"]
			r.MaxDepth = 0
			p.Resolvers["driveParents"] = r
		}, "maxDepth"},
		{"resolver depth not recursive", func(p *Policy) {
			r := p.Resolvers["driveParents"]
			r.Recursive = false
			p.Resolvers["driveParents"] = r
		}, "maxDepth must be 1"},
		{"resolver extract", func(p *Policy) {
			r := p.Resolvers["driveParents"]
			r.Extract = "parents"
			p.Resolvers["driveParents"] = r
		}, "extract"},
		{"no capabilities", func(p *Policy) { p.Capabilities = nil }, "capabilities must not be empty"},
		{"public disabled", func(p *Policy) { p.Capabilities["discovery"] = Capability{Public: true} }, "always enabled"},
		{"public constraints", func(p *Policy) {
			p.Capabilities["discovery"] = Capability{Public: true, Enabled: true, Constraints: p.Capabilities["mail.send"].Constraints}
		}, "no constraints"},
		{"disabled constraints", func(p *Policy) {
			p.Capabilities["files.delete"] = Capability{Constraints: p.Capabilities["mail.send"].Constraints}
		}, "disabled capability"},
		{"constraint kind", func(p *Policy) {
			p.Capabilities["mail.send"].Constraints["recipients"] = Constraint{Kind: "x", Var: "recipients", Value: ConstraintValue{Any: true}}
		}, "kind must be one of"},
		{"constraint var", func(p *Policy) {
			p.Capabilities["mail.send"].Constraints["recipients"] = Constraint{Kind: KindEmail, Var: "1x", Value: ConstraintValue{Any: true}}
		}, "var"},
		{"constraint resolver on email", func(p *Policy) {
			p.Capabilities["mail.send"].Constraints["recipients"] = Constraint{Kind: KindEmail, Var: "recipients", Resolver: "driveParents", Value: ConstraintValue{Any: true}}
		}, "only allowed for kind resource"},
		{"constraint unknown resolver", func(p *Policy) {
			p.Capabilities["files.edit"].Constraints["resources"] = Constraint{Kind: KindResource, Var: "fileId", Resolver: "nope", Value: ConstraintValue{Any: true}}
		}, "not declared"},
		{"constraint value", func(p *Policy) {
			p.Capabilities["mail.send"].Constraints["recipients"] = Constraint{Kind: KindEmail, Var: "recipients"}
		}, "value: must not be empty"},
		{"no routes", func(p *Policy) { p.Routes = nil }, "routes must not be empty"},
		{"route op", func(p *Policy) { p.Routes[0].Op = "" }, "op"},
		{"route op duplicate", func(p *Policy) { p.Routes[1].Op = p.Routes[0].Op }, "already used"},
		{"route capability", func(p *Policy) { p.Routes[0].Capability = "nope" }, "not declared in capabilities"},
		{"route method", func(p *Policy) { p.Routes[0].Methods = []string{"CONNECT"} }, "unsupported method"},
		{"route no method", func(p *Policy) { p.Routes[0].Methods = nil }, "at least one method"},
		{"route duplicate method", func(p *Policy) { p.Routes[0].Methods = []string{"GET", "GET"} }, "duplicate method"},
		{"route no host", func(p *Policy) { p.Routes[0].Hosts = nil }, "at least one host"},
		{"route ungoverned host", func(p *Policy) { p.Routes[0].Hosts = []string{"evil.com"} }, "not a governed host"},
		{"route parent of exact host", func(p *Policy) { p.Routes[0].Hosts = []string{"googleapis.com"} }, "not a governed host"},
		{"route wildcard wider", func(p *Policy) { p.Routes[0].Hosts = []string{"*.googleapis.com"} }, "not a governed host"},
		{"route host unnormalized", func(p *Policy) { p.Routes[0].Hosts = []string{"WWW.googleapis.com"} }, "not normalized"},
		{"route path", func(p *Policy) { p.Routes[0].Path = "/a//b" }, "path"},
		{"route query empty", func(p *Policy) { p.Routes[1].Query = map[string]QueryCondition{"format": {}} }, "at least one value"},
		{"route query any mixed", func(p *Policy) { p.Routes[1].Query = map[string]QueryCondition{"format": {Any: true, Absent: true}} }, "cannot be combined"},
		{"route query name", func(p *Policy) { p.Routes[1].Query = map[string]QueryCondition{"": {Absent: true}} }, "must use only letters"},
		{"route query name with $", func(p *Policy) { p.Routes[1].Query = map[string]QueryCondition{"$alt": {Absent: true}} }, "must use only letters"},
		{"route queryMatch name", func(p *Policy) { p.Routes[1].QueryMatch = map[string]string{"a b": "x"} }, "queryMatch: parameter name"},
		{"route queryMatch regex", func(p *Policy) { p.Routes[1].QueryMatch = map[string]string{"q": "("} }, "invalid regular expression"},
		{"route jsonMatch path", func(p *Policy) { p.Routes[1].JSONMatch = map[string]string{"x": "a"} }, "jsonMatch"},
		{"route jsonMatch regex", func(p *Policy) { p.Routes[1].JSONMatch = map[string]string{"$.a": "[a"} }, "invalid regular expression"},
		{"route jsonPresent path", func(p *Policy) { p.Routes[1].JSONPresent = []string{"$..[0]"} }, "jsonPresent[0]"},
		{"route json path", func(p *Policy) { p.Routes[4].JSON = map[string]JSONCondition{"trashed": {Absent: true}} }, "json"},
		{"route json null", func(p *Policy) {
			p.Routes[4].JSON = map[string]JSONCondition{"$.trashed": {Values: []json.RawMessage{json.RawMessage("null")}}}
		}, "null must be expressed as absent"},
		{"route json object", func(p *Policy) {
			p.Routes[4].JSON = map[string]JSONCondition{"$.trashed": {Values: []json.RawMessage{json.RawMessage("{}")}}}
		}, "not a JSON scalar"},
		{"route json empty", func(p *Policy) { p.Routes[4].JSON = map[string]JSONCondition{"$.trashed": {}} }, "at least one value"},
		{"bind var name", func(p *Policy) { p.Routes[3].Bind = map[string][]BindStep{"1x": {{Op: StepBody}}} }, "invalid variable name"},
		{"bind no steps", func(p *Policy) { p.Routes[3].Bind = map[string][]BindStep{"x": nil} }, "at least one step"},
		{"bind unknown step", func(p *Policy) { p.Routes[3].Bind = map[string][]BindStep{"x": {{Op: "xml"}}} }, "unknown step"},
		{"count unknown counter", func(p *Policy) { p.Routes[1].Count = []Count{{Counter: "nope"}} }, "not declared in counters"},
		{"count unbound key", func(p *Policy) { p.Routes[1].Count = []Count{{Counter: "mail.read", Key: "nope"}} }, "neither a path capture nor a bind"},
		{"count keyed and unkeyed", func(p *Policy) { p.Routes[3].Count = []Count{{Counter: "mail.read"}} }, "both with and without a key"},
		{"count on public", func(p *Policy) { p.Routes[0].Count = []Count{{Counter: "mail.sent"}} }, "public capability"},
		{"constraint var from response", func(p *Policy) {
			p.Routes[3].Bind["recipients"] = []BindStep{{Op: StepResponse}, {Op: StepJSON, Arg: "$.to"}}
		}, "reads the response"},
		{"max body zero", func(p *Policy) { p.Routes[0].MaxBodyBytes = 0 }, "maxBodyBytes"},
		{"max body huge", func(p *Policy) { p.Routes[0].MaxBodyBytes = MaxInspectBytes + 1 }, "maxBodyBytes"},
		{"max response huge", func(p *Policy) { p.Routes[0].MaxResponseBytes = MaxInspectBytes + 1 }, "maxResponseBytes"},
		{"deny no content type", func(p *Policy) { p.Deny.ContentType = "" }, "contentType"},
		{"deny no body", func(p *Policy) { p.Deny.Body = "" }, "body is required"},
		{"deny invalid json", func(p *Policy) { p.Deny.Body = `{"message":"{{message}}"}` }, "valid JSON"},
		{"deny status unquoted", func(p *Policy) { p.Deny.Body = `{"status":"{{status}}"}` }, "valid JSON"},
		{"json keys", func(p *Policy) { p.JSONKeys = "snake" }, "jsonKeys"},
		{"action unknown", func(p *Policy) { p.Routes[0].Action = "log" }, "action must be"},
		{"reason on allow", func(p *Policy) { p.Routes[0].Reason = "x" }, "reason is only allowed"},
		{"deny route capability", func(p *Policy) { p.Routes[0].Action = ActionDeny }, "has no capability"},
		{"deny route bind", func(p *Policy) {
			p.Routes[2].Action, p.Routes[2].Capability, p.Routes[2].Count = ActionDeny, "", nil
		}, "no bind or count"},
		{"deny route reason", func(p *Policy) {
			p.Routes[0].Action, p.Routes[0].Capability, p.Routes[0].Reason = ActionDeny, "", "a\nb"
		}, "reason must be"},
		{"multipart parts", func(p *Policy) { p.Routes[0].MultipartParts = -1 }, "multipartParts"},
		{"multipart parts huge", func(p *Policy) { p.Routes[0].MultipartParts = MaxMultipartParts + 1 }, "multipartParts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPolicy()
			tt.mutate(p)
			err := p.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateResolverURL(t *testing.T) {
	governed := []string{"www.googleapis.com", "*.example.com"}
	tests := []struct {
		url  string
		want string
	}{
		{"https://www.googleapis.com/drive/v3/files/{id}?fields=parents", ""},
		{"https://api.example.com/items/{id}", ""},
		{"https://www.googleapis.com/drive/v3/files/x", "exactly once"},
		{"https://www.googleapis.com/{id}/{id}", "exactly once"},
		{"https://www.googleapis.com/files?id={id}", "must be in the path"},
		{"http://www.googleapis.com/files/{id}", "https"},
		{"https://evil.com/files/{id}", "not a governed host"},
		{"https://example.com/files/{id}", ""},
		{"https://sub.api.example.com/files/{id}", ""},
		{"https://sub.www.googleapis.com/files/{id}", ""},
		{"https://googleapis.com/files/{id}", "not a governed host"},
		{"https://example.org/files/{id}", "not a governed host"},
		{"https://user@www.googleapis.com/files/{id}", "user info"},
		{"https://www.googleapis.com:8443/files/{id}", "port"},
		{"https://www.googleapis.com/files/{id}#x", "fragment"},
		{"https://www.googleapis.com/files/{id}/{x}", "braces"},
		{"https://www.googleapis.com/a/../files/{id}", "path"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := ValidateResolverURL(tt.url, governed)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("ValidateResolverURL = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateResolverURL = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMatchHostIsStrict(t *testing.T) {
	tests := []struct {
		host, pattern string
		want          bool
	}{
		{"gmail.googleapis.com", "gmail.googleapis.com", true},
		{"x.gmail.googleapis.com", "gmail.googleapis.com", false},
		{"a.example.com", "*.example.com", true},
		{"a.b.example.com", "*.example.com", true},
		{"example.com", "*.example.com", false},
		{"badexample.com", "*.example.com", false},
	}
	for _, tt := range tests {
		if got := MatchHost(tt.host, tt.pattern); got != tt.want {
			t.Errorf("MatchHost(%q, %q) = %v, want %v", tt.host, tt.pattern, got, tt.want)
		}
	}
}

func TestDenyTemplateRender(t *testing.T) {
	tpl := DenyTemplate{ContentType: "application/json", Body: `{"code":{{code}},"status":{{status}},"message":{{message}},"again":{{message}}}`}
	out := tpl.Render(403, StatusResourceExhausted, "quote \" and {{code}} {{status}} and </script>")
	var decoded struct {
		Code    int    `json:"code"`
		Status  string `json:"status"`
		Message string `json:"message"`
		Again   string `json:"again"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("rendered body is not JSON: %v: %s", err, out)
	}
	if decoded.Code != 403 || decoded.Status != StatusResourceExhausted || decoded.Message != "quote \" and {{code}} {{status}} and </script>" || decoded.Again != decoded.Message {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestSummaryCarriesNoPlaceholder(t *testing.T) {
	p := validPolicy()
	data, err := json.Marshal(p.Summary())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), testPlaceholder) {
		t.Fatal("summary contains the placeholder")
	}
	s := p.Summary()
	if s.Kind != SummaryKind || s.Filter != "gws" || s.Preset != "readonly" || len(s.Hosts) != 3 || s.Confine != nil {
		t.Fatalf("summary = %+v", s)
	}
	p.Confine = []string{"example.com"}
	if s := p.Summary(); len(s.Confine) != 1 || s.Confine[0] != "example.com" {
		t.Fatalf("summary confine = %v", s.Confine)
	}
	p.Confine = nil
	var send *SummaryCapability
	for i := range s.Capabilities {
		if s.Capabilities[i].Name == "mail.send" {
			send = &s.Capabilities[i]
		}
	}
	if send == nil || !send.Enabled || send.Constraints["recipients"].Value.Values[0] != "@example.com" || send.Operations[0] != "gmail.messages.send" {
		t.Fatalf("mail.send summary = %+v", send)
	}
	if len(s.Counters) != 2 || s.Counters[0].Name != "mail.read" || *s.Counters[0].Quota != 5 || s.Counters[1].Quota != nil {
		t.Fatalf("counters = %+v", s.Counters)
	}
	p.Routes = append(p.Routes, Route{Op: "drive.permissions.create", Action: ActionDeny, Methods: []string{"POST"},
		Hosts: []string{"www.googleapis.com"}, Path: "/drive/v3/files/{fileId}/permissions", MaxBodyBytes: 1, MaxResponseBytes: 1})
	if err := p.Validate(); err != nil {
		t.Fatalf("deny route: %v", err)
	}
	if denied := p.Summary().DeniedOperations; len(denied) != 1 || denied[0] != "drive.permissions.create" {
		t.Fatalf("DeniedOperations = %v", denied)
	}
}

func TestValidateBind(t *testing.T) {
	captures := map[string]bool{"id": true}
	tests := []struct {
		name  string
		steps []BindStep
		want  string
	}{
		{"path", []BindStep{{Op: StepPath, Arg: "id"}}, ""},
		{"full pipeline", []BindStep{{Op: StepBody}, {Op: StepJSON, Arg: "$.raw"}, {Op: StepBase64URL}, {Op: StepRFC822, Arg: RFC822Recipients}}, ""},
		{"rfc822 destinations", []BindStep{{Op: StepBody}, {Op: StepRFC822, Arg: RFC822Destinations}}, ""},
		{"multipart", []BindStep{{Op: StepBody}, {Op: StepMultipart, Arg: "application/json"}, {Op: StepJSON, Arg: "$.parents[*]"}}, ""},
		{"response", []BindStep{{Op: StepResponse}, {Op: StepJSON, Arg: "$.id"}}, ""},
		{"split", []BindStep{{Op: StepQuery, Arg: "addParents"}, {Op: StepSplit, Arg: ","}}, ""},
		{"header", []BindStep{{Op: StepHeader, Arg: "X-Goog-Upload-Protocol"}}, ""},
		{"empty", nil, "at least one step"},
		{"unknown", []BindStep{{Op: "xpath"}}, "unknown step"},
		{"transform first", []BindStep{{Op: StepJSON, Arg: "$.a"}}, "cannot start a bind"},
		{"two sources", []BindStep{{Op: StepBody}, {Op: StepQuery, Arg: "a"}}, "only be the first step"},
		{"arg on body", []BindStep{{Op: StepBody, Arg: "x"}}, "takes no argument"},
		{"missing arg", []BindStep{{Op: StepQuery}}, "needs an argument"},
		{"unknown capture", []BindStep{{Op: StepPath, Arg: "other"}}, "not a capture"},
		{"bad header", []BindStep{{Op: StepHeader, Arg: "a b"}}, "invalid header"},
		{"query control", []BindStep{{Op: StepQuery, Arg: "a\nb"}}, "must use only"},
		{"query dollar", []BindStep{{Op: StepQuery, Arg: "$fields"}}, "must use only"},
		{"multipart after json", []BindStep{{Op: StepBody}, {Op: StepJSON, Arg: "$.a"}, {Op: StepMultipart, Arg: "application/json"}}, "directly follow"},
		{"bad json path", []BindStep{{Op: StepBody}, {Op: StepJSON, Arg: "raw"}}, "must start with"},
		{"bad rfc822 mode", []BindStep{{Op: StepBody}, {Op: StepRFC822, Arg: "senders"}}, "rfc822 mode"},
		{"bad split", []BindStep{{Op: StepBody}, {Op: StepSplit, Arg: "\x01"}}, "control"},
		{"multipart index", []BindStep{{Op: StepBody}, {Op: StepMultipart, Index: intp(1)}}, ""},
		{"multipart index and prefix", []BindStep{{Op: StepBody}, {Op: StepMultipart, Arg: "application/json", Index: intp(0)}}, "not both"},
		{"multipart index negative", []BindStep{{Op: StepBody}, {Op: StepMultipart, Index: intp(-1)}}, "between 0 and"},
		{"multipart index huge", []BindStep{{Op: StepBody}, {Op: StepMultipart, Index: intp(MaxMultipartParts)}}, "between 0 and"},
		{"index on json", []BindStep{{Op: StepBody}, {Op: StepJSON, Arg: "$.a", Index: intp(0)}}, "only multipart takes an index"},
		{"multipart without selector", []BindStep{{Op: StepBody}, {Op: StepMultipart}}, "needs an argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBind(tt.steps, captures)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("ValidateBind = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateBind = %v, want %q", err, tt.want)
			}
		})
	}
	if !IsResponseBind([]BindStep{{Op: StepResponse}}) || IsResponseBind([]BindStep{{Op: StepBody}}) || IsResponseBind(nil) {
		t.Fatal("IsResponseBind wrong")
	}
	if !ReadsRequestBody([]BindStep{{Op: StepBody}}) || ReadsRequestBody([]BindStep{{Op: StepPath, Arg: "id"}}) {
		t.Fatal("ReadsRequestBody wrong")
	}
}

func TestNameValidation(t *testing.T) {
	for _, ok := range []string{"gws", "mail.read", "send-any", "a_b", "0x"} {
		if ValidateName(ok) != nil {
			t.Errorf("ValidateName(%q) failed", ok)
		}
	}
	for _, bad := range []string{"", "Gws", ".x", "-x", "a/b", "a b", strings.Repeat("a", maxNameLength+1)} {
		if ValidateName(bad) == nil {
			t.Errorf("ValidateName(%q) passed", bad)
		}
	}
	if ValidateIdent("docs.documents.batchUpdate") != nil || ValidateIdent("a b") == nil || ValidateIdent("") == nil {
		t.Error("ValidateIdent wrong")
	}
	if ValidateVarName("fileId") != nil || ValidateVarName("_x") != nil || ValidateVarName("1x") == nil || ValidateVarName("a-b") == nil {
		t.Error("ValidateVarName wrong")
	}
	if ValidateEnvVarName("GOOGLE_TOKEN") != nil || ValidateEnvVarName("lower") == nil {
		t.Error("ValidateEnvVarName wrong")
	}
}

// A filter governs every name equal to or below its host patterns, while a
// route host keeps matching exactly.
func TestGovernsAndMatchHost(t *testing.T) {
	for _, tc := range []struct {
		pattern, host  string
		governs, route bool
	}{
		{"a.example.com", "a.example.com", true, true},
		{"a.example.com", "x.a.example.com", true, false},
		{"a.example.com", "example.com", false, false},
		{"a.example.com", "xa.example.com", false, false},
		{"*.example.com", "example.com", true, false},
		{"*.example.com", "a.example.com", true, true},
		{"*.example.com", "x.a.example.com", true, true},
		{"*.example.com", "example.org", false, false},
		{"*.example.com", "", false, false},
	} {
		if got := Governs(tc.pattern, tc.host); got != tc.governs {
			t.Errorf("Governs(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.governs)
		}
		if got := MatchHost(tc.host, tc.pattern); got != tc.route {
			t.Errorf("MatchHost(%q, %q) = %v, want %v", tc.host, tc.pattern, got, tc.route)
		}
	}
	governed := []string{"gmail.googleapis.com", "*.example.com"}
	for pattern, want := range map[string]bool{
		"gmail.googleapis.com":    true,
		"x.gmail.googleapis.com":  true,
		"*.gmail.googleapis.com":  true,
		"example.com":             true,
		"*.a.example.com":         true,
		"googleapis.com":          false,
		"*.googleapis.com":        false,
		"www.googleapis.com":      false,
		"example.org":             false,
		"*.xgmail.googleapis.com": false,
	} {
		if got := patternCovered(pattern, governed); got != want {
			t.Errorf("patternCovered(%q) = %v, want %v", pattern, got, want)
		}
	}
}

func TestHostCapabilitiesListsHostsOfEnabledCapabilities(t *testing.T) {
	p := &Policy{
		Capabilities: map[string]Capability{
			"mail.read":   {Enabled: true},
			"mail.send":   {Enabled: true},
			"mail.delete": {},
			"files.edit": {Enabled: true, Constraints: map[string]Constraint{
				"folders": {Kind: KindResource, Var: "parent", Resolver: "parents"},
			}},
		},
		Resolvers: map[string]Resolver{
			"parents": {URL: "https://WWW.googleapis.com/drive/v3/files/{id}?fields=parents"},
		},
		Routes: []Route{
			{Op: "get", Capability: "mail.read", Hosts: []string{"gmail.googleapis.com"}},
			{Op: "send", Capability: "mail.send", Hosts: []string{"gmail.googleapis.com"}},
			{Op: "delete", Capability: "mail.delete", Hosts: []string{"gmail.googleapis.com", "trash.googleapis.com"}},
			{Op: "edit", Capability: "files.edit", Hosts: []string{"*.example.com"}},
			{Op: "token", Action: ActionDeny, Hosts: []string{"oauth2.googleapis.com"}},
		},
	}
	got := p.HostCapabilities()
	want := map[string][]string{
		"gmail.googleapis.com": {"mail.read", "mail.send"},
		"*.example.com":        {"files.edit"},
		"www.googleapis.com":   {"files.edit"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HostCapabilities() = %v, want %v", got, want)
	}
}

func TestValidateConfine(t *testing.T) {
	hosts := []string{"gmail.googleapis.com", "*.drive.googleapis.com"}
	if err := ValidateConfine([]string{"googleapis.com"}, hosts); err != nil {
		t.Fatalf("ValidateConfine: %v", err)
	}
	for confine, want := range map[string][]string{
		"duplicate":      {"googleapis.com", "googleapis.com"},
		"not normalized": {"GoogleAPIs.com"},
		"governed":       {"drive.googleapis.com"},
		"no host below":  {"example.com"},
	} {
		if err := ValidateConfine(want, hosts); err == nil {
			t.Errorf("%s: ValidateConfine(%v) accepted", confine, want)
		}
	}
}
