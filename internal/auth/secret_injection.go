// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

const placeholderPrefix = "ENCLAVE_SECRET_"

type PlaceholderResolver struct {
	byEnvVar map[string]string
}

func NewPlaceholderResolver() *PlaceholderResolver {
	return &PlaceholderResolver{
		byEnvVar: map[string]string{},
	}
}

func (r *PlaceholderResolver) ResolvePlaceholder(envVar string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("placeholder resolver is nil")
	}
	name := strings.TrimSpace(envVar)
	if name == "" {
		return "", fmt.Errorf("env var is required")
	}
	if placeholder, ok := r.byEnvVar[name]; ok {
		return placeholder, nil
	}
	placeholder, err := newPlaceholder()
	if err != nil {
		return "", err
	}
	r.byEnvVar[name] = placeholder
	return placeholder, nil
}

func newPlaceholder() (string, error) {
	entropy := make([]byte, 24)
	if _, err := rand.Read(entropy); err != nil {
		return "", fmt.Errorf("generate placeholder entropy: %w", err)
	}
	return placeholderPrefix + hex.EncodeToString(entropy), nil
}
