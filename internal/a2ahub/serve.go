// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"time"
)

// Serve runs the proxy on ln until ctx ends. The caller must have registered
// reloads (SIGHUP) before calling, so the first status write never precedes
// the handler (P1-P). Reload errors are reported in the status file only.
func (p *Proxy) Serve(ctx context.Context, ln net.Listener, reloads <-chan os.Signal) error {
	_ = p.Reload()
	srv := &http.Server{
		Handler:           p.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	for {
		select {
		case <-reloads:
			_ = p.Reload()
		case err := <-errc:
			p.Close()
			return err
		case <-ctx.Done():
			p.Close()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := srv.Shutdown(shutdownCtx)
			if errors.Is(err, context.DeadlineExceeded) {
				err = srv.Close()
			}
			return err
		}
	}
}
