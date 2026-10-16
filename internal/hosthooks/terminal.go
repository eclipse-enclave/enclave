// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hosthooks

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type enteredProvider struct {
	provider Provider
	request  Request
}
type Scope struct {
	mu      sync.Mutex
	once    sync.Once
	effects []Effect
	entered []enteredProvider
	writer  io.Writer
	runner  Runner
	env     []string
	closed  bool
	unwatch func()
}

// Begin owns terminal cleanup before invoking any provider or writing enter bytes.
func (r Runner) Begin(ctx context.Context, providers []Provider, req Request, env []string, out io.Writer, handled ...os.Signal) *Scope {
	s := &Scope{writer: out, runner: r, env: env}
	if !req.Terminal.StdoutIsTTY || req.Background || len(providers) == 0 {
		return s
	}
	s.unwatch = watchInterrupts(interceptedSignals(handled), s.reset)
	phase, done := context.WithTimeout(ctx, 2*time.Second)
	defer done()
	claims := map[string]bool{}
	for _, p := range providers {
		if _, ok := p.Phases[Enter]; !ok {
			continue
		}
		if phase.Err() != nil {
			r.warn(p.ID, "terminal-enter phase budget exhausted")
			break
		}
		child, cancel := context.WithTimeout(phase, 500*time.Millisecond)
		response, err := r.invoke(child, p, req, Enter, env)
		cancel()
		if err != nil {
			r.warn(p.ID, err)
			continue
		}
		stateReq := req
		stateReq.State = response.State
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			break
		}
		s.entered = append(s.entered, enteredProvider{p, stateReq})
		for _, effect := range response.TerminalEffects {
			if claims[effect.Resource] {
				r.warn(p.ID, "terminal resource already claimed: "+effect.Resource)
				continue
			}
			claims[effect.Resource] = true
			s.effects = append(s.effects, effect)
			_, _ = fmt.Fprint(out, effect.Enter)
		}
		s.mu.Unlock()
	}
	return s
}
func (s *Scope) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for i := len(s.effects) - 1; i >= 0; i-- {
		_, _ = fmt.Fprint(s.writer, s.effects[i].Leave)
	}
}
func (s *Scope) Close(outcome Outcome) {
	s.once.Do(func() {
		if s.unwatch != nil {
			s.unwatch()
		}
		s.reset()
		ctx, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		for i := len(s.entered) - 1; i >= 0; i-- {
			item := s.entered[i]
			if _, ok := item.provider.Phases[Leave]; !ok {
				continue
			}
			if ctx.Err() != nil {
				s.runner.warn(item.provider.ID, "terminal-leave phase budget exhausted")
				break
			}
			child, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			item.request.Outcome = &outcome
			_, err := s.runner.invoke(child, item.provider, item.request, Leave, s.env)
			cancel()
			if err != nil {
				s.runner.warn(item.provider.ID, err)
			}
		}
	})
}
