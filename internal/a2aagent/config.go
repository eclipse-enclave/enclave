// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2aagent

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"enclave/internal/a2a"
)

var (
	groupNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	generationPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// LoadConfig parses environ ("KEY=value" entries) and validates the fields the role needs.
func LoadConfig(environ []string) (Config, error) {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "ENCLAVE_A2A_") {
			env[k] = v
		}
	}
	p := &envParser{env: env}
	cfg := Config{
		Role:             a2a.Role(env[a2a.EnvRole]),
		Group:            env[a2a.EnvGroup],
		GroupGeneration:  env[a2a.EnvGroupGeneration],
		Member:           env[a2a.EnvMember],
		MemberGeneration: env[a2a.EnvMemberGeneration],
		HubHost:          env[a2a.EnvHubHost],
		InboundToken:     env[a2a.EnvInboundToken],
		OutboundToken:    env[a2a.EnvOutboundToken],
		TableDir:         env[a2a.EnvTableDir],
		Description:      env[a2a.EnvDescription],
	}
	cfg.HubPort = p.port(a2a.EnvHubPort)
	cfg.AdapterPort = p.port(a2a.EnvAdapterPort)
	cfg.QueueDepth = p.count(a2a.EnvQueueDepth)
	cfg.MaxRequestBytes = p.count(a2a.EnvMaxRequestBytes)
	cfg.MaxResultBytes = p.count(a2a.EnvMaxResultBytes)
	cfg.RecordLimit = p.count(a2a.EnvRecordLimit)
	cfg.TaskDeadline = p.duration(a2a.EnvTaskDeadline)
	cfg.SendWait = p.duration(a2a.EnvSendWait)
	cfg.RecordMaxAge = p.duration(a2a.EnvRecordMaxAge)
	cfg.ReconnectWindow = p.duration(a2a.EnvReconnectWindow)
	cfg.ContextTTL = p.duration(a2a.EnvContextTTL)
	cfg.TerminationTimeout = p.duration(a2a.EnvTerminationTimeout)
	cfg.HeartbeatInterval = p.duration(a2a.EnvHeartbeatInterval)

	switch cfg.Role {
	case a2a.RoleOrchestrator, a2a.RoleWorker:
	case "":
		p.missing(a2a.EnvRole)
	default:
		p.fail(a2a.EnvRole, "must be orchestrator or worker")
	}
	if cfg.Group == "" {
		p.missing(a2a.EnvGroup)
	} else if !groupNamePattern.MatchString(cfg.Group) {
		p.fail(a2a.EnvGroup, "is not a valid group name")
	}
	p.generation(a2a.EnvGroupGeneration, cfg.GroupGeneration)
	p.generation(a2a.EnvMemberGeneration, cfg.MemberGeneration)
	p.require(a2a.EnvMember, cfg.Member)
	p.require(a2a.EnvHubHost, cfg.HubHost)
	p.require(a2a.EnvInboundToken, cfg.InboundToken)
	if cfg.HubPort == 0 {
		p.missing(a2a.EnvHubPort)
	}
	if cfg.Role == a2a.RoleWorker {
		p.require(a2a.EnvOutboundToken, cfg.OutboundToken)
		p.require(a2a.EnvTableDir, cfg.TableDir)
		if cfg.AdapterPort == 0 {
			p.missing(a2a.EnvAdapterPort)
		}
	}
	if len(p.errs) > 0 {
		return Config{}, fmt.Errorf("a2a config: %w", errors.Join(p.errs...))
	}
	return cfg, nil
}

type envParser struct {
	env  map[string]string
	errs []error
}

func (p *envParser) fail(name, msg string) {
	p.errs = append(p.errs, fmt.Errorf("%s %s", name, msg))
}

func (p *envParser) missing(name string) { p.fail(name, "is required") }

func (p *envParser) require(name, value string) {
	if value == "" {
		p.missing(name)
	}
}

func (p *envParser) generation(name, value string) {
	if value == "" {
		p.missing(name)
	} else if !generationPattern.MatchString(value) {
		p.fail(name, "must be 16 lowercase hex characters")
	}
}

func (p *envParser) count(name string) int {
	raw, ok := p.env[name]
	if !ok || raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		p.fail(name, "must be a non-negative decimal integer")
		return 0
	}
	return n
}

func (p *envParser) port(name string) int {
	n := p.count(name)
	if n > 65535 {
		p.fail(name, "must be a TCP port")
		return 0
	}
	return n
}

func (p *envParser) duration(name string) time.Duration {
	raw, ok := p.env[name]
	if !ok || raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		p.fail(name, "must be a non-negative Go duration")
		return 0
	}
	return d
}
