// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package spec

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"enclave/internal/filter"
)

const fixtureDir = "../testdata/gws"

const baseSpecYAML = `
schemaVersion: "1"
kind: filter
name: demo
hosts: [api.example.com, "*.files.example.com"]
credential:
  oauth2:
    authUrl: https://auth.example.com/authorize
    tokenUrl: https://auth.example.com/token
    scopePrefix: "https://scopes.example.com/"
    scopeImplies:
      write: [read]
  inject: { header: Authorization, format: "Bearer %s" }
  env: [DEMO_TOKEN]
  denyHeaders: [X-Api-Key]
  denyParams: [access_token]
resolvers:
  parents:
    url: "https://api.example.com/items/{id}?fields=parents"
    extract: "$.parents[*]"
    recursive: true
    scopes: [read]
counters:
  reads: { description: Items read }
capabilities:
  meta: { public: true }
  read:
    scopes: [read]
    constraints:
      items: { kind: resource, var: itemId, resolver: parents, default: "*" }
  send:
    scopes: [send]
    constraints:
      to: { kind: email, var: to }
routes:
  - op: meta.get
    capability: meta
    method: GET
    host: api.example.com
    path: /meta
  - op: items.get
    capability: read
    method: [get]
    host: [api.example.com]
    path: /items/{itemId}
    query: { view: [full, ~] }
    count: [ { counter: reads, key: itemId } ]
  - op: mail.send
    capability: send
    method: POST
    host: api.example.com
    path: /send
    bind:
      to: [ {body: {}}, {json: "$.to[*]"} ]
`

func baseSpec(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(baseSpecYAML), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// node walks a decoded document by map keys and list indexes.
func node(t *testing.T, doc any, path ...any) any {
	t.Helper()
	cur := doc
	for _, step := range path {
		switch k := step.(type) {
		case string:
			cur = cur.(map[string]any)[k]
		case int:
			cur = cur.([]any)[k]
		}
	}
	return cur
}

func setPath(t *testing.T, doc map[string]any, value any, path ...any) {
	t.Helper()
	parent := node(t, doc, path[:len(path)-1]...)
	switch k := path[len(path)-1].(type) {
	case string:
		if value == nil {
			delete(parent.(map[string]any), k)
			return
		}
		parent.(map[string]any)[k] = value
	case int:
		parent.([]any)[k] = value
	}
}

func parseDoc(t *testing.T, doc map[string]any) (*Filter, error) {
	t.Helper()
	data, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return Parse(data, "demo/spec.yaml")
}

func TestParseBaseSpec(t *testing.T) {
	f, err := parseDoc(t, baseSpec(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Credential.Inject.Header != "authorization" || f.Credential.DenyHeaders[0] != "x-api-key" {
		t.Fatalf("credential not normalized: %+v", f.Credential)
	}
	if f.Routes[1].Methods[0] != "GET" || f.Routes[1].MaxBodyBytes != filter.DefaultMaxBodyBytes {
		t.Fatalf("route not normalized: %+v", f.Routes[1])
	}
	if got := f.Capabilities["read"].Scopes; len(got) != 1 || got[0] != "https://scopes.example.com/read" {
		t.Fatalf("scopes = %v", got)
	}
	if f.Resolvers["parents"].MaxDepth != filter.DefaultResolverDepth {
		t.Fatalf("resolver depth = %d", f.Resolvers["parents"].MaxDepth)
	}
	if d := f.Capabilities["read"].Constraints["items"].Default; d == nil || !d.Any {
		t.Fatalf("default = %+v", d)
	}
	if !f.Routes[1].Query["view"].Absent || f.Routes[1].Query["view"].Values[0] != "full" {
		t.Fatalf("query = %+v", f.Routes[1].Query)
	}
}

func TestParseSpecErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, d map[string]any)
		want   string
	}{
		{"schema version", func(t *testing.T, d map[string]any) { d["schemaVersion"] = "2" }, "schemaVersion must be"},
		{"kind", func(t *testing.T, d map[string]any) { d["kind"] = "mixin" }, `kind must be "filter"`},
		{"name", func(t *testing.T, d map[string]any) { d["name"] = "Demo" }, "name: invalid name"},
		{"unknown field", func(t *testing.T, d map[string]any) { d["network"] = map[string]any{} }, `unknown field "network"`},
		{"top-level key case", func(t *testing.T, d map[string]any) { d["Hosts"] = []any{"evil.example.com"} },
			`parse demo/spec.yaml: unknown field "Hosts" (did you mean "hosts"?)`},
		{"route key case", func(t *testing.T, d map[string]any) { setPath(t, d, "send", "routes", 1, "Capability") },
			`parse demo/spec.yaml: routes[1]: unknown field "Capability" (did you mean "capability"?)`},
		{"credential key case", func(t *testing.T, d map[string]any) { setPath(t, d, "x-api-key", "credential", "inject", "Header") },
			`credential.inject: unknown field "Header" (did you mean "header"?)`},
		{"capability key case", func(t *testing.T, d map[string]any) { setPath(t, d, true, "capabilities", "read", "Public") },
			`capabilities.read: unknown field "Public" (did you mean "public"?)`},
		{"constraint key case", func(t *testing.T, d map[string]any) {
			setPath(t, d, "*", "capabilities", "send", "constraints", "to", "Default")
		}, `capabilities.send.constraints.to: unknown field "Default" (did you mean "default"?)`},
		{"placeholder key case", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"random": map[string]any{"Encoding": "hex", "length": 32}}, "credential", "placeholder")
		}, `credential.placeholder.random: unknown field "Encoding" (did you mean "encoding"?)`},
		{"count key case", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"counter": "reads", "Key": "itemId"}}, "routes", 1, "count")
		}, `routes[1].count[0]: unknown field "Key" (did you mean "key"?)`},
		{"multipart selector key case", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"body": map[string]any{}}, map[string]any{"multipart": map[string]any{"Index": 1}}}, "routes", 2, "bind", "to")
		}, `bind.to[1]: multipart takes a content type prefix or {index: n}: unknown field "Index" (did you mean "index"?)`},
		{"constraint default key case", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"IDs": []any{"a"}}, "capabilities", "read", "constraints", "items", "default")
		}, `constraints.items.default: unknown key "IDs", use ids and within`},
		{"reserved env", func(t *testing.T, d map[string]any) { setPath(t, d, []any{"ENCLAVE_TOKEN"}, "credential", "env") },
			"credential: env: ENCLAVE_TOKEN uses the reserved ENCLAVE_ prefix"},
		{"reserved static env", func(t *testing.T, d map[string]any) {
			setPath(t, d, nil, "credential", "oauth2")
			setPath(t, d, map[string]any{"env": []any{"DEMO_HOST_TOKEN", "ENCLAVE_FOO"}}, "credential", "static")
			for _, c := range []string{"read", "send"} {
				setPath(t, d, nil, "capabilities", c, "scopes")
			}
			setPath(t, d, nil, "resolvers", "parents", "scopes")
		}, "credential: static: env: ENCLAVE_FOO uses the reserved ENCLAVE_ prefix"},
		{"default preset name", func(t *testing.T, d map[string]any) { d["defaultPreset"] = "Read" }, "defaultPreset"},
		{"json keys", func(t *testing.T, d map[string]any) { d["jsonKeys"] = "snake" }, "jsonKeys"},
		{"no hosts", func(t *testing.T, d map[string]any) { d["hosts"] = []any{} }, "hosts: must list"},
		{"bad host", func(t *testing.T, d map[string]any) { d["hosts"] = []any{"https://x.com"} }, "hosts: invalid host"},
		{"broad wildcard", func(t *testing.T, d map[string]any) { d["hosts"] = []any{"*.com"} }, "hosts: invalid host"},
		{"no credential", func(t *testing.T, d map[string]any) { delete(d, "credential") }, "credential is required"},
		{"both credentials", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"env": []any{"X"}}, "credential", "static")
		}, "credential: exactly one of oauth2 and static"},
		{"auth url", func(t *testing.T, d map[string]any) {
			setPath(t, d, "http://auth.example.com", "credential", "oauth2", "authUrl")
		}, "credential: oauth2: authUrl"},
		{"token url", func(t *testing.T, d map[string]any) { setPath(t, d, nil, "credential", "oauth2", "tokenUrl") }, "oauth2: tokenUrl"},
		{"http token url", func(t *testing.T, d map[string]any) {
			setPath(t, d, "http://127.0.0.1:8080/token", "credential", "oauth2", "tokenUrl")
		}, "oauth2: tokenUrl: \"http://127.0.0.1:8080/token\" must be an https URL"},
		{"http localhost auth url", func(t *testing.T, d map[string]any) {
			setPath(t, d, "http://localhost/auth", "credential", "oauth2", "authUrl")
		}, "oauth2: authUrl: \"http://localhost/auth\" must be an https URL"},
		{"http revoke url", func(t *testing.T, d map[string]any) {
			setPath(t, d, "http://auth.example.com/revoke", "credential", "oauth2", "revokeUrl")
		}, "oauth2: revokeUrl: \"http://auth.example.com/revoke\" must be an https URL"},
		{"revoke url", func(t *testing.T, d map[string]any) {
			setPath(t, d, "ftp://x", "credential", "oauth2", "revokeUrl")
		}, "oauth2: revokeUrl"},
		{"reserved auth param", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"scope": "x"}, "credential", "oauth2", "authParams")
		}, "authParams.scope: is set by enclave filters login"},
		{"response mode", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"response_mode": "form_post"}, "credential", "oauth2", "authParams")
		}, "authParams.response_mode: is set by enclave filters login"},
		{"auth param space", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"prompt": "a b"}, "credential", "oauth2", "authParams")
		}, "authParams.prompt"},
		{"scope prefix", func(t *testing.T, d map[string]any) {
			setPath(t, d, "scopes/", "credential", "oauth2", "scopePrefix")
		}, "scopePrefix"},
		{"scope implies self", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"write": []any{"write"}}, "credential", "oauth2", "scopeImplies")
		}, "cannot imply itself"},
		{"scope implies cycle", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"write": []any{"read"}, "read": []any{"write"}}, "credential", "oauth2", "scopeImplies")
		}, "scopeImplies: cycle"},
		{"scope implies space", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"write": []any{"re ad"}}, "credential", "oauth2", "scopeImplies")
		}, "scopeImplies.write: invalid scope"},
		{"static without source", func(t *testing.T, d map[string]any) {
			setPath(t, d, nil, "credential", "oauth2")
			setPath(t, d, map[string]any{}, "credential", "static")
			setPath(t, d, nil, "resolvers", "parents", "scopes")
			setPath(t, d, nil, "capabilities", "read", "scopes")
			setPath(t, d, nil, "capabilities", "send", "scopes")
		}, "credential: static: needs env or file"},
		{"static env name", func(t *testing.T, d map[string]any) {
			setPath(t, d, nil, "credential", "oauth2")
			setPath(t, d, map[string]any{"env": []any{"lower"}}, "credential", "static")
		}, "static: env: invalid env var"},
		{"static file path", func(t *testing.T, d map[string]any) {
			setPath(t, d, nil, "credential", "oauth2")
			setPath(t, d, map[string]any{"file": map[string]any{"path": " "}}, "credential", "static")
		}, "static: file.path is required"},
		{"static file parser", func(t *testing.T, d map[string]any) {
			setPath(t, d, nil, "credential", "oauth2")
			setPath(t, d, map[string]any{"file": map[string]any{"path": "~/t", "parser": "/a/b"}}, "credential", "static")
		}, "static: file.parser"},
		{"static priority", func(t *testing.T, d map[string]any) {
			setPath(t, d, nil, "credential", "oauth2")
			setPath(t, d, map[string]any{"env": []any{"T"}, "priority": "last"}, "credential", "static")
		}, "static: priority"},
		{"scopes with static", func(t *testing.T, d map[string]any) {
			setPath(t, d, nil, "credential", "oauth2")
			setPath(t, d, map[string]any{"env": []any{"T"}}, "credential", "static")
			setPath(t, d, nil, "resolvers", "parents", "scopes")
		}, "capabilities.read: scopes: scopes need an oauth2 credential"},
		{"resolver scopes with static", func(t *testing.T, d map[string]any) {
			setPath(t, d, nil, "credential", "oauth2")
			setPath(t, d, map[string]any{"env": []any{"T"}}, "credential", "static")
		}, "resolvers.parents: scopes: scopes need an oauth2 credential"},
		{"no inject", func(t *testing.T, d map[string]any) { setPath(t, d, nil, "credential", "inject") }, "inject is required"},
		{"inject format", func(t *testing.T, d map[string]any) {
			setPath(t, d, "Bearer", "credential", "inject", "format")
		}, "credential: inject: format"},
		{"no env", func(t *testing.T, d map[string]any) { setPath(t, d, []any{}, "credential", "env") }, "env: must list"},
		{"env name", func(t *testing.T, d map[string]any) { setPath(t, d, []any{"demo"}, "credential", "env") }, "env: invalid env var"},
		{"env duplicate", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{"A", "A"}, "credential", "env")
		}, "env: duplicate"},
		{"placeholder", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"random": map[string]any{"encoding": "hex", "length": 8}}, "credential", "placeholder")
		}, "credential: placeholder: placeholder.random.length"},
		{"deny header is inject", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{"authorization"}, "credential", "denyHeaders")
		}, "denyHeaders: header \"authorization\" is the injection header"},
		{"deny param empty", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{""}, "credential", "denyParams")
		}, "denyParams"},
		{"resolver name", func(t *testing.T, d map[string]any) {
			setPath(t, d, node(t, d, "resolvers", "parents"), "resolvers", "bad name")
		}, "resolvers.bad name"},
		{"resolver url host", func(t *testing.T, d map[string]any) {
			setPath(t, d, "https://evil.com/items/{id}", "resolvers", "parents", "url")
		}, "resolvers.parents: url: host \"evil.com\" is not a governed host"},
		{"resolver id in query", func(t *testing.T, d map[string]any) {
			setPath(t, d, "https://api.example.com/items?id={id}", "resolvers", "parents", "url")
		}, "must be in the path"},
		{"resolver extract", func(t *testing.T, d map[string]any) {
			setPath(t, d, "parents", "resolvers", "parents", "extract")
		}, "resolvers.parents: extract"},
		{"resolver depth without recursive", func(t *testing.T, d map[string]any) {
			setPath(t, d, false, "resolvers", "parents", "recursive")
			setPath(t, d, 4, "resolvers", "parents", "maxDepth")
		}, "maxDepth needs recursive"},
		{"resolver depth too deep", func(t *testing.T, d map[string]any) {
			setPath(t, d, 65, "resolvers", "parents", "maxDepth")
		}, "maxDepth must be between"},
		{"counter name", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{}, "counters", "Reads")
		}, "counters: invalid name"},
		{"no capabilities", func(t *testing.T, d map[string]any) { d["capabilities"] = map[string]any{} }, "capabilities must not be empty"},
		{"capability name", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{}, "capabilities", "Read")
		}, "capabilities.Read: invalid name"},
		{"public with scopes", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{"read"}, "capabilities", "meta", "scopes")
		}, "capabilities.meta: scopes: a public capability"},
		{"public with constraints", func(t *testing.T, d map[string]any) {
			setPath(t, d, node(t, d, "capabilities", "send", "constraints"), "capabilities", "meta", "constraints")
		}, "capabilities.meta: constraints: a public capability"},
		{"constraint kind", func(t *testing.T, d map[string]any) {
			setPath(t, d, "mail", "capabilities", "send", "constraints", "to", "kind")
		}, "capabilities.send: constraints.to.kind must be one of"},
		{"constraint var", func(t *testing.T, d map[string]any) {
			setPath(t, d, "to-list", "capabilities", "send", "constraints", "to", "var")
		}, "constraints.to.var: invalid variable name"},
		{"constraint resolver on email", func(t *testing.T, d map[string]any) {
			setPath(t, d, "parents", "capabilities", "send", "constraints", "to", "resolver")
		}, "constraints.to.resolver is only allowed for kind resource"},
		{"constraint unknown resolver", func(t *testing.T, d map[string]any) {
			setPath(t, d, "nope", "capabilities", "read", "constraints", "items", "resolver")
		}, "resolver \"nope\" is not declared"},
		{"default shape", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{"a"}, "capabilities", "read", "constraints", "items", "default")
		}, "constraints.items.default: use \"*\" or {ids"},
		{"default string", func(t *testing.T, d map[string]any) {
			setPath(t, d, "@example.com", "capabilities", "send", "constraints", "to", "default")
		}, "constraints.to.default: the only string value"},
		{"default bad email", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{"example.com"}, "capabilities", "send", "constraints", "to", "default")
		}, "constraints.to.default: email pattern"},
		{"default within without resolver", func(t *testing.T, d map[string]any) {
			setPath(t, d, nil, "capabilities", "read", "constraints", "items", "resolver")
			setPath(t, d, map[string]any{"within": []any{"F"}}, "capabilities", "read", "constraints", "items", "default")
		}, "within needs a resolver"},
		{"default unknown key", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"under": []any{"F"}}, "capabilities", "read", "constraints", "items", "default")
		}, "unknown key \"under\""},
		{"no routes", func(t *testing.T, d map[string]any) { d["routes"] = []any{} }, "routes must not be empty"},
		{"route op", func(t *testing.T, d map[string]any) { setPath(t, d, "bad op", "routes", 0, "op") }, "routes[0] (bad op): op: invalid name"},
		{"route duplicate op", func(t *testing.T, d map[string]any) {
			setPath(t, d, "meta.get", "routes", 1, "op")
		}, "routes[1] (meta.get): op \"meta.get\" is already used by routes[0]"},
		{"route unknown capability", func(t *testing.T, d map[string]any) {
			setPath(t, d, "write", "routes", 1, "capability")
		}, "routes[1] (items.get): capability \"write\" is not declared"},
		{"route method", func(t *testing.T, d map[string]any) {
			setPath(t, d, "TRACE", "routes", 0, "method")
		}, "routes[0] (meta.get): method: unsupported method \"TRACE\""},
		{"route method type", func(t *testing.T, d map[string]any) {
			setPath(t, d, 7, "routes", 0, "method")
		}, "must be a string or a list of strings"},
		{"route host", func(t *testing.T, d map[string]any) {
			setPath(t, d, "evil.com", "routes", 0, "host")
		}, "routes[0] (meta.get): host: host \"evil.com\" is not a governed host"},
		{"route host invalid", func(t *testing.T, d map[string]any) {
			setPath(t, d, "a/b", "routes", 0, "host")
		}, "routes[0] (meta.get): host: invalid host"},
		{"route path", func(t *testing.T, d map[string]any) {
			setPath(t, d, "/items/{itemId", "routes", 1, "path")
		}, "routes[1] (items.get): path:"},
		{"route query empty", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"view": []any{}}, "routes", 1, "query")
		}, "routes[1] (items.get): query.view: must list"},
		{"route query object", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"view": []any{map[string]any{}}}, "routes", 1, "query")
		}, "query.view: values must be"},
		{"route json path", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"trashed": []any{false}}, "routes", 1, "json")
		}, "routes[1] (items.get): json: JSON path"},
		{"route json object", func(t *testing.T, d map[string]any) {
			setPath(t, d, map[string]any{"$.a": []any{map[string]any{}}}, "routes", 1, "json")
		}, "json.$.a: values must be JSON scalars"},
		{"bind step shape", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"body": map[string]any{}, "json": "$.to"}}, "routes", 2, "bind", "to")
		}, "routes[2] (mail.send): bind.to[0]: a step is a map with exactly one key"},
		{"bind unknown step", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"xml": "x"}}, "routes", 2, "bind", "to")
		}, "bind.to[0]: unknown step \"xml\""},
		{"bind body arg", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"body": "x"}}, "routes", 2, "bind", "to")
		}, "bind.to[0]: body takes no argument"},
		{"bind json arg type", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"body": map[string]any{}}, map[string]any{"json": 1}}, "routes", 2, "bind", "to")
		}, "bind.to[1]: json needs a string argument"},
		{"bind multipart selector", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"body": map[string]any{}}, map[string]any{"multipart": map[string]any{"type": "x"}}}, "routes", 2, "bind", "to")
		}, "bind.to[1]: multipart takes a content type prefix or {index: n}"},
		{"bind pipeline", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"json": "$.to"}}, "routes", 2, "bind", "to")
		}, "bind.to[0]: json cannot start a bind"},
		{"bind path capture", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"path": "id"}}, "routes", 2, "bind", "to")
		}, "bind.to[0]: path \"id\" is not a capture"},
		{"count unknown counter", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"counter": "writes"}}, "routes", 1, "count")
		}, "routes[1] (items.get): count[0]: counter \"writes\" is not declared"},
		{"count unbound key", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"counter": "reads", "key": "id"}}, "routes", 1, "count")
		}, "count[0]: key \"id\" is neither"},
		{"count on public", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"counter": "reads"}}, "routes", 0, "count")
		}, "count is not allowed on a route of public capability"},
		{"constraint var from response", func(t *testing.T, d map[string]any) {
			setPath(t, d, []any{map[string]any{"response": map[string]any{}}, map[string]any{"json": "$.to"}}, "routes", 2, "bind", "to")
		}, "bind.to reads the response"},
		{"negative body limit", func(t *testing.T, d map[string]any) {
			setPath(t, d, -1, "routes", 0, "maxBodyBytes")
		}, "must not be negative"},
		{"body limit too large", func(t *testing.T, d map[string]any) {
			setPath(t, d, filter.MaxInspectBytes+1, "routes", 0, "maxBodyBytes")
		}, "maxBodyBytes must be between"},
		{"response limit too large", func(t *testing.T, d map[string]any) {
			setPath(t, d, filter.MaxInspectBytes+1, "routes", 0, "maxResponseBytes")
		}, "maxResponseBytes must be between"},
		{"deny route with capability", func(t *testing.T, d map[string]any) {
			setPath(t, d, "deny", "routes", 0, "action")
		}, "routes[0] (meta.get): a route with action deny has no capability"},
		{"route action", func(t *testing.T, d map[string]any) {
			setPath(t, d, "skip", "routes", 0, "action")
		}, "action must be allow or deny"},
		{"multipart parts", func(t *testing.T, d map[string]any) {
			setPath(t, d, 99, "routes", 0, "multipartParts")
		}, "multipartParts must be between"},
		{"deny template", func(t *testing.T, d map[string]any) {
			d["deny"] = map[string]any{"contentType": "application/json", "body": `{"m":"{{message}}"}`}
		}, "deny: body does not render valid JSON"},
		{"deny template content type", func(t *testing.T, d map[string]any) {
			d["deny"] = map[string]any{"body": "x"}
		}, "deny: contentType is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := baseSpec(t)
			tt.mutate(t, doc)
			_, err := parseDoc(t, doc)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse error = %v\nwant substring %q", err, tt.want)
			}
			if !strings.HasPrefix(err.Error(), "demo/spec.yaml: ") && !strings.HasPrefix(err.Error(), "parse demo/spec.yaml: ") {
				t.Fatalf("error does not name the file: %v", err)
			}
		})
	}
}

func TestParseWarnsAboutUnboundConstraintVars(t *testing.T) {
	doc := baseSpec(t)
	setPath(t, doc, nil, "routes", 2, "bind")
	f, err := parseDoc(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Warnings) != 1 || !strings.Contains(f.Warnings[0], `routes[2] (mail.send): does not bind "to"`) {
		t.Fatalf("Warnings = %v", f.Warnings)
	}
}

func TestParseWildcardOnlyAcknowledgesUnboundConstraint(t *testing.T) {
	doc := baseSpec(t)
	setPath(t, doc, nil, "routes", 2, "bind")
	setPath(t, doc, []any{"to"}, "routes", 2, "wildcardOnly")
	f, err := parseDoc(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none for an acknowledged constraint", f.Warnings)
	}
	if got := f.Routes[2].WildcardOnly; len(got) != 1 || got[0] != "to" {
		t.Fatalf("WildcardOnly = %v", got)
	}
}

func TestParseRejectsInvalidWildcardOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		route int
		value any
		bind  bool
		want  string
	}{
		"unknown constraint": {route: 2, value: []any{"cc"}, want: `wildcardOnly: "cc" is not a constraint of capability send`},
		"bound constraint":   {route: 2, value: []any{"to"}, bind: true, want: `the route binds "to", so constraint to is checked`},
		"duplicate":          {route: 2, value: []any{"to", "to"}, want: `duplicate constraint "to"`},
		"empty":              {route: 2, value: []any{}, want: "wildcardOnly: must list at least one constraint"},
		"public capability":  {route: 0, value: []any{"to"}, want: "is not a constraint of capability meta"},
	} {
		t.Run(name, func(t *testing.T) {
			doc := baseSpec(t)
			if !tc.bind {
				setPath(t, doc, nil, "routes", 2, "bind")
			}
			setPath(t, doc, tc.value, "routes", tc.route, "wildcardOnly")
			_, err := parseDoc(t, doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseMatchConditions(t *testing.T) {
	doc := baseSpec(t)
	routes := node(t, doc, "routes").([]any)
	doc["routes"] = append([]any{map[string]any{
		"op": "items.chart", "action": "deny", "method": "POST", "host": "api.example.com", "path": "/items/{itemId}:batch",
		"jsonPresent": []any{"$..createChart", "$.requests[*].embed"},
		"jsonMatch":   map[string]any{"$..text": "(?i)secret"},
		"queryMatch":  map[string]any{"q": "fullText"},
		"jsonAllowedKeys": map[string]any{
			"$":              []any{"requests"},
			"$.requests[*]":  []any{"insertText", "delete_text"},
			"$.writeControl": []any{},
		},
	}}, routes...)
	f, err := parseDoc(t, doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := f.Routes[0]
	if len(r.JSONPresent) != 2 || r.JSONMatch["$..text"] != "(?i)secret" || r.QueryMatch["q"] != "fullText" {
		t.Fatalf("route = %+v", r)
	}
	if keys := r.JSONAllowedKeys; len(keys) != 3 || len(keys["$.requests[*]"]) != 2 || keys["$.writeControl"] == nil || len(keys["$.writeControl"]) != 0 {
		t.Fatalf("jsonAllowedKeys = %#v", keys)
	}
	for name, mutate := range map[string]struct {
		key   string
		value any
		want  string
	}{
		"bad regex":         {"jsonMatch", map[string]any{"$.a": "("}, "invalid regular expression"},
		"bad query name":    {"queryMatch", map[string]any{"$q": "x"}, `parameter name "$q"`},
		"bad path":          {"jsonPresent", []any{"$..[*]"}, "jsonPresent[0]"},
		"empty presence":    {"jsonPresent", []any{}, "jsonPresent: must list at least one path"},
		"long expression":   {"queryMatch", map[string]any{"q": strings.Repeat("a", 1025)}, "longer than 1024"},
		"allowed keys path": {"jsonAllowedKeys", map[string]any{"requests": []any{"a"}}, "jsonAllowedKeys: JSON path"},
		"allowed key name":  {"jsonAllowedKeys", map[string]any{"$": []any{"a.b"}}, `key "a.b" must use only`},
		"allowed key twice": {"jsonAllowedKeys", map[string]any{"$": []any{"a", "a"}}, `duplicate key "a"`},
		"allowed keys form": {"jsonAllowedKeys", []any{"$"}, "jsonAllowedKeys"},
	} {
		t.Run(name, func(t *testing.T) {
			doc := baseSpec(t)
			setPath(t, doc, mutate.value, "routes", 1, mutate.key)
			if _, err := parseDoc(t, doc); err == nil || !strings.Contains(err.Error(), mutate.want) {
				t.Fatalf("Parse error = %v, want %q", err, mutate.want)
			}
		})
	}
}

func TestParseAcceptsDenyRouteAndMultipartIndex(t *testing.T) {
	doc := baseSpec(t)
	routes := node(t, doc, "routes").([]any)
	routes = append([]any{map[string]any{
		"op": "items.share", "action": "deny", "reason": "sharing is off",
		"method": "POST", "host": "api.example.com", "path": "/items/{itemId}/share",
	}}, routes...)
	doc["routes"] = routes
	setPath(t, doc, []any{map[string]any{"body": nil}, map[string]any{"multipart": map[string]any{"index": 1}}, map[string]any{"json": "$.to[*]"}},
		"routes", 3, "bind", "to")
	f, err := parseDoc(t, doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Routes[0].Action != filter.ActionDeny || f.Routes[0].Reason != "sharing is off" {
		t.Fatalf("deny route = %+v", f.Routes[0])
	}
	if step := f.Routes[3].Bind["to"][1]; step.Op != filter.StepMultipart || step.Index == nil || *step.Index != 1 {
		t.Fatalf("multipart step = %+v", step)
	}
}

func TestLoadFixture(t *testing.T) {
	f, err := Load(fixtureDir, "gws")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Warnings) != 0 {
		t.Fatalf("Warnings = %v", f.Warnings)
	}
	if f.DefaultPreset != "readonly" || f.JSONKeys != filter.JSONKeysProtobuf || f.Credential.Type != filter.CredentialOAuth2 {
		t.Fatalf("filter = %+v", f)
	}
	if _, err := Load(fixtureDir, "other"); err == nil || !strings.Contains(err.Error(), `name must be "other"`) {
		t.Fatalf("Load with wrong name = %v", err)
	}
}

func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "gws")
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReportsIgnoredEntries(t *testing.T) {
	dir := copyDir(t, fixtureDir)
	writeFile(t, filepath.Join(dir, "install.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(dir, "files", "home", "x"), "x")
	writeFile(t, filepath.Join(dir, "presets", "notes.txt"), "x")
	writeFile(t, filepath.Join(dir, "commands", "host", "gws-status"), "#!/bin/sh\n")
	f, err := Load(dir, "gws")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.Warnings, "\n")
	for _, want := range []string{"install.sh is ignored", "files is ignored", "notes.txt is ignored"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings miss %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "commands is ignored") {
		t.Errorf("commands/ reported as ignored:\n%s", joined)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Run("missing spec", func(t *testing.T) {
		if _, err := Load(t.TempDir(), ""); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Load = %v, want not exist", err)
		}
	})
	t.Run("spec is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, SpecFilename), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir, ""); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("spec too large", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, SpecFilename), strings.Repeat("#", maxFileBytes+1))
		if _, err := Load(dir, ""); err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("spec json key case", func(t *testing.T) {
		dir := t.TempDir()
		data, _ := yaml.YAMLToJSON([]byte(strings.Replace(baseSpecYAML, "\n  denyParams:", "\n  DenyParams:", 1)))
		writeFile(t, filepath.Join(dir, SpecFilenameJSON), string(data))
		if _, err := Load(dir, "demo"); err == nil || !strings.Contains(err.Error(), `credential: unknown field "DenyParams" (did you mean "denyParams"?)`) {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("shipped preset key case", func(t *testing.T) {
		dir := copyDir(t, fixtureDir)
		writeFile(t, filepath.Join(dir, PresetsDir, "readonly.yaml"), "allow:\n  mail.read: {}\nAllow:\n  mail.send: { recipients: \"*\" }\n")
		if _, err := Load(dir, "gws"); err == nil || !strings.Contains(err.Error(), `readonly.yaml: unknown field "Allow" (did you mean "allow"?)`) {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("spec json fallback", func(t *testing.T) {
		dir := t.TempDir()
		data, _ := yaml.YAMLToJSON([]byte(baseSpecYAML))
		writeFile(t, filepath.Join(dir, SpecFilenameJSON), string(data))
		if f, err := Load(dir, "demo"); err != nil || f.SpecPath != filepath.Join(dir, SpecFilenameJSON) {
			t.Fatalf("Load = %v, %v", f, err)
		}
	})
	t.Run("presets without default", func(t *testing.T) {
		dir := copyDir(t, fixtureDir)
		spec, _ := os.ReadFile(filepath.Join(dir, SpecFilename))
		writeFile(t, filepath.Join(dir, SpecFilename), strings.Replace(string(spec), "defaultPreset: readonly\n", "", 1))
		if _, err := Load(dir, "gws"); err == nil || !strings.Contains(err.Error(), "defaultPreset: is required") {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("default not shipped", func(t *testing.T) {
		dir := copyDir(t, fixtureDir)
		spec, _ := os.ReadFile(filepath.Join(dir, SpecFilename))
		writeFile(t, filepath.Join(dir, SpecFilename), strings.Replace(string(spec), "defaultPreset: readonly", "defaultPreset: nope", 1))
		if _, err := Load(dir, "gws"); err == nil || !strings.Contains(err.Error(), `defaultPreset: "nope" is not a shipped preset`) {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("broken shipped preset", func(t *testing.T) {
		dir := copyDir(t, fixtureDir)
		writeFile(t, filepath.Join(dir, PresetsDir, "broken.yaml"), "allow:\n  mail.write: {}\n")
		if _, err := Load(dir, "gws"); err == nil || !strings.Contains(err.Error(), "broken.yaml: allow.mail.write: unknown capability") {
			t.Fatalf("Load = %v", err)
		}
	})
}

func TestParseConfine(t *testing.T) {
	doc := baseSpec(t)
	doc["confine"] = []any{"Example.COM.", "example.com"}
	f, err := parseDoc(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"example.com"}; !reflect.DeepEqual(f.Confine, want) {
		t.Fatalf("Confine = %v, want %v", f.Confine, want)
	}
	tests := []struct {
		domain string
		want   string
	}{
		{"api.example.com", `domain "api.example.com" is governed`},
		{"x.api.example.com", `domain "x.api.example.com" is governed`},
		{"files.example.com", `domain "files.example.com" is governed`},
		{"example.org", `domain "example.org" has no governed host below it`},
		{"com", `domain "com" must have at least two labels`},
		{"*.example.com", `domain "*.example.com" must not be a wildcard`},
		{"https://example.com", `invalid domain "https://example.com"`},
	}
	for _, tt := range tests {
		doc := baseSpec(t)
		doc["confine"] = []any{tt.domain}
		_, err := parseDoc(t, doc)
		if err == nil || !strings.Contains(err.Error(), "confine: ") || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("confine %q: err = %v, want %q", tt.domain, err, tt.want)
		}
	}
}
