// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package builtin

import (
	"context"
	"io"
	"os"

	"enclave/internal/hosthooks"
	"enclave/internal/logx"
)

var out io.Writer = os.Stdout
var isTerminal = func() bool { return true }

type options struct{ handled []os.Signal }
type Option func(*options)

func CallerHandles(signals ...os.Signal) Option { return func(o *options) { o.handled = signals } }
func Begin(color string, opts ...Option) func() {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	p := Tint()
	p.Config = hosthooks.Config{Settings: map[string]any{"color": color}}
	scope := (hosthooks.Runner{}).Begin(context.Background(), []hosthooks.Provider{p}, hosthooks.Request{Terminal: hosthooks.Terminal{StdoutIsTTY: isTerminal(), ColorSuppressed: logx.ColorSuppressedByEnv()}}, os.Environ(), out, o.handled...)
	return func() { scope.Close(hosthooks.Outcome{Status: "unknown"}) }
}
