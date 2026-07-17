// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tools

import (
	"strings"
	"sync"

	"enclave/internal/model"
)

var (
	handlersMu sync.RWMutex
	handlers   = map[string]model.RuntimeHandler{}
)

func RegisterHandler(name string, handler model.RuntimeHandler) {
	key := strings.TrimSpace(strings.ToLower(name))
	if key == "" || handler == nil {
		return
	}
	handlersMu.Lock()
	handlers[key] = handler
	handlersMu.Unlock()
}

func Resolve(name string) model.RuntimeHandler {
	key := strings.TrimSpace(strings.ToLower(name))
	handlersMu.RLock()
	handler := handlers[key]
	handlersMu.RUnlock()
	if handler != nil {
		return handler
	}
	return BaseHandler{}
}
