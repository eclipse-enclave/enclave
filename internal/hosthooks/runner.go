// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hosthooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Runner struct{ Warn func(string) }

func (r Runner) warn(id string, err any) {
	if r.Warn != nil {
		r.Warn(fmt.Sprintf("host hook %s: %v", id, err))
	}
}

func readBounded(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if len(data) > limit {
		return nil, fmt.Errorf("payload exceeds %d bytes", limit)
	}
	return data, err
}
func decodeObject(data []byte, value any) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return fmt.Errorf("expected one JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing protocol output")
	}
	return nil
}
func decodeResponse(data []byte, phase string) (Response, error) {
	var response Response
	if len(bytes.TrimSpace(data)) == 0 {
		response.SchemaVersion = Version
		return response, nil
	}
	if err := decodeObject(data, &response); err != nil {
		return response, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for _, key := range []string{"schemaVersion", "hostEnvironment", "restartHostProcess", "terminalEffects", "state"} {
		if v, ok := fields[key]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return Response{}, fmt.Errorf("%s must not be null", key)
		}
	}
	if response.SchemaVersion != Version {
		return Response{}, fmt.Errorf("unsupported response schemaVersion")
	}
	_, env := fields["hostEnvironment"]
	_, restart := fields["restartHostProcess"]
	_, effects := fields["terminalEffects"]
	if phase != Prepare && (env || restart) || phase != Enter && effects {
		return Response{}, fmt.Errorf("effects are not legal in %s", phase)
	}
	if len(response.State) > MaxPayload {
		return Response{}, fmt.Errorf("state exceeds limit")
	}
	if response.State != nil {
		var state map[string]any
		if err := json.Unmarshal(response.State, &state); err != nil || state == nil {
			return Response{}, fmt.Errorf("state must be an object")
		}
	}
	for _, effect := range response.TerminalEffects {
		if effect.Resource == "" || len(effect.Resource) > MaxPayload || len(effect.Enter) > MaxPayload || len(effect.Leave) > MaxPayload {
			return Response{}, fmt.Errorf("invalid terminal effect or size")
		}
	}
	return response, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	n := len(data)
	remaining := b.limit - b.Len()
	if len(data) > remaining {
		b.exceeded = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return n, nil
}

func (r Runner) invoke(ctx context.Context, p Provider, req Request, phase string, env []string) (Response, error) {
	req = configRequest(p, req, phase)
	data, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if len(data) > MaxMessage {
		return Response{}, fmt.Errorf("request exceeds limit")
	}
	if p.Builtin != nil {
		return p.Builtin(ctx, req)
	}
	// Revalidate immediately before execution, including changed or removed symlinks.
	current, err := Load(p.ID, p.Dir)
	if err != nil {
		return Response{}, err
	}
	path, ok := current.Phases[phase]
	if !ok {
		return Response{}, fmt.Errorf("phase no longer exists")
	}
	cmd := exec.CommandContext(ctx, "./"+path) // #nosec G204 -- explicitly enabled, validated host provider.
	cmd.Dir = p.Dir
	cmd.Env = ChildEnvironment(env)
	cmd.Stdin = bytes.NewReader(data)
	out := &boundedBuffer{limit: MaxMessage}
	diagnostics := &boundedBuffer{limit: MaxDiagnostics}
	cmd.Stdout = out
	cmd.Stderr = diagnostics
	cmd.WaitDelay = 50 * time.Millisecond
	configureProcess(cmd)
	err = cmd.Run()
	killProcessGroup(cmd)
	if diagnostics.Len() > 0 {
		r.warn(p.ID, strings.TrimSpace(diagnostics.String()))
	}
	if err != nil {
		return Response{}, err
	}
	if out.exceeded {
		return Response{}, fmt.Errorf("response exceeds limit")
	}
	return decodeResponse(out.Bytes(), phase)
}

func ChildEnvironment(env []string) []string {
	values := EnvironmentMap(env)
	for key := range values {
		if strings.HasPrefix(key, "ENCLAVE_INTERNAL_") {
			delete(values, key)
		}
	}
	values[ChildEnv] = "1"
	return EnvironmentList(values)
}
func EnvironmentMap(env []string) map[string]string {
	result := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}
func EnvironmentList(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func environmentChanges(patch *Environment, env map[string]string) (map[string]*string, error) {
	changes := map[string]*string{}
	seen := map[string]bool{}
	add := func(key, value string, kind int) error {
		if !envName.MatchString(key) || strings.HasPrefix(key, "ENCLAVE_") || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("invalid or reserved environment key/value %q", key)
		}
		if seen[key] {
			return fmt.Errorf("overlapping environment operations for %s", key)
		}
		seen[key] = true
		old, present := env[key]
		if kind == 1 && present {
			return nil
		}
		if kind == 2 {
			changes[key] = nil
			if !present {
				delete(changes, key)
			}
			return nil
		}
		if !present || old != value {
			v := value
			changes[key] = &v
		}
		return nil
	}
	if patch == nil {
		return changes, nil
	}
	for k, v := range patch.Set {
		if err := add(k, v, 0); err != nil {
			return nil, err
		}
	}
	for k, v := range patch.SetIfAbsent {
		if err := add(k, v, 1); err != nil {
			return nil, err
		}
	}
	for _, k := range patch.Unset {
		if err := add(k, "", 2); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

// Prepare returns a deduplicated candidate environment; the caller owns applying or replaying it.
func (r Runner) Prepare(ctx context.Context, providers []Provider, req Request, env []string) ([]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	effective := EnvironmentMap(env)
	claimed := map[string]bool{}
	restart := false
	for _, p := range providers {
		if _, ok := p.Phases[Prepare]; !ok {
			continue
		}
		if ctx.Err() != nil {
			r.warn(p.ID, "prepare phase budget exhausted")
			break
		}
		child, stop := context.WithTimeout(ctx, 2*time.Second)
		response, err := r.invoke(child, p, req, Prepare, EnvironmentList(effective))
		stop()
		if err != nil {
			r.warn(p.ID, err)
			continue
		}
		changes, err := environmentChanges(response.HostEnvironment, effective)
		if err == nil && response.RestartHostProcess && len(changes) == 0 {
			err = fmt.Errorf("restart requires an effective environment change")
		}
		if err == nil {
			// Explicit writes conflict even when the later requested value already matches.
			if response.HostEnvironment != nil {
				for k := range response.HostEnvironment.Set {
					if claimed[k] {
						err = fmt.Errorf("environment conflict for %s", k)
					}
				}
				for _, k := range response.HostEnvironment.Unset {
					if claimed[k] {
						err = fmt.Errorf("environment conflict for %s", k)
					}
				}
			}
			for k := range changes {
				if claimed[k] {
					err = fmt.Errorf("environment conflict for %s", k)
				}
			}
		}
		if err != nil {
			r.warn(p.ID, err)
			continue
		}
		if response.HostEnvironment != nil {
			for k := range response.HostEnvironment.Set {
				claimed[k] = true
			}
			for _, k := range response.HostEnvironment.Unset {
				claimed[k] = true
			}
		}
		for k, v := range changes {
			claimed[k] = true
			if v == nil {
				delete(effective, k)
			} else {
				effective[k] = *v
			}
		}
		restart = restart || response.RestartHostProcess
	}
	return EnvironmentList(effective), restart
}
