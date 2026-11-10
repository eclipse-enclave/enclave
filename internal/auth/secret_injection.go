// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"fmt"
	"strings"

	"enclave/internal/model"
)

type PlaceholderResolver struct {
	bySecretID map[string]string
}

func NewPlaceholderResolver() *PlaceholderResolver {
	return &PlaceholderResolver{
		bySecretID: map[string]string{},
	}
}

func (r *PlaceholderResolver) ResolvePlaceholder(secretID string, shape *model.SecretPlaceholderConfig) (string, error) {
	if r == nil {
		return "", fmt.Errorf("placeholder resolver is nil")
	}
	name := strings.TrimSpace(secretID)
	if name == "" {
		return "", fmt.Errorf("secret ID is required")
	}
	if placeholder, ok := r.bySecretID[name]; ok {
		return placeholder, nil
	}
	placeholder, err := model.NewSecretPlaceholder(shape)
	if err != nil {
		return "", err
	}
	r.bySecretID[name] = placeholder
	return placeholder, nil
}
