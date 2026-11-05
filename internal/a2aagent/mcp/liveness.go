// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"enclave/internal/a2a"
)

// Liveness timing: the hub sends keepalives every 15 s and two missed ones count as loss (section 11).
const (
	livenessKeepalive  = 15 * time.Second
	livenessBackoffMin = time.Second
	livenessBackoffMax = 10 * time.Second
)

type liveness struct {
	hub       *hubClient
	log       func(format string, args ...any)
	idle      time.Duration
	connected atomic.Bool
	// connects counts established connections, for tests.
	connects atomic.Int64
}

// run holds the orchestrator liveness connection and reconnects until ctx ends.
func (l *liveness) run(ctx context.Context) {
	backoff := livenessBackoffMin
	for ctx.Err() == nil {
		start := time.Now()
		err := l.hold(ctx)
		l.connected.Store(false)
		if ctx.Err() != nil {
			return
		}
		l.log("a2a: liveness connection lost: %v", err)
		if time.Since(start) > 2*livenessKeepalive {
			backoff = livenessBackoffMin
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, livenessBackoffMax)
	}
}

func (l *liveness) hold(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := l.hub.newRequest(ctx, http.MethodGet, a2a.HubLivenessPath, nil, a2a.SSEContentType)
	if err != nil {
		return err
	}
	resp, err := l.hub.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body := &watchedReader{r: resp.Body}
	body.touch()
	go func() {
		ticker := time.NewTicker(l.idle / 4)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if body.idleFor() > l.idle {
					cancel()
					return
				}
			}
		}
	}()
	reader := a2a.NewSSEReader(body, maxEventBytes)
	ev, err := reader.Next()
	if err != nil {
		return err
	}
	if ev.Event != a2a.ControlEventHello {
		// A hub refusal arrives as one JSON-RPC error body.
		var resp a2a.Response
		if json.Unmarshal(ev.Data, &resp) == nil && resp.Error != nil {
			return resp.Error
		}
		return fmt.Errorf("first liveness event %q, want %q", ev.Event, a2a.ControlEventHello)
	}
	var hello a2a.ControlHello
	if err := json.Unmarshal(ev.Data, &hello); err != nil {
		return fmt.Errorf("decode hello: %w", err)
	}
	l.connected.Store(true)
	l.connects.Add(1)
	l.log("a2a: liveness connected (epoch=%s revision=%d)", hello.Epoch, hello.Revision)
	for {
		if _, err := reader.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("closed by hub")
			}
			if ctx.Err() != nil && body.idleFor() > l.idle {
				return errors.New("keepalive missed")
			}
			return err
		}
	}
}

// watchedReader records the time of the last read so silent loss is detected even between events.
type watchedReader struct {
	r    io.Reader
	last atomic.Int64
}

func (w *watchedReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 {
		w.touch()
	}
	return n, err
}

func (w *watchedReader) touch() { w.last.Store(time.Now().UnixNano()) }

func (w *watchedReader) idleFor() time.Duration {
	return time.Since(time.Unix(0, w.last.Load()))
}
