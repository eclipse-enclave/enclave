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
	"math/big"
	"strings"

	"enclave/internal/model"
)

const placeholderPrefix = "ENCLAVE_SECRET_"

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
	placeholder, err := newPlaceholder(shape)
	if err != nil {
		return "", err
	}
	r.bySecretID[name] = placeholder
	return placeholder, nil
}

func newPlaceholder(shape *model.SecretPlaceholderConfig) (string, error) {
	if shape != nil {
		alphabet, err := shape.ValidatedAlphabet()
		if err != nil {
			return "", err
		}
		body := make([]byte, shape.Random.Length)
		bound := big.NewInt(int64(len(alphabet)))
		for i := range body {
			n, err := rand.Int(rand.Reader, bound)
			if err != nil {
				return "", fmt.Errorf("generate placeholder entropy: %w", err)
			}
			body[i] = alphabet[n.Int64()]
		}
		return shape.Prefix + string(body) + shape.Suffix, nil
	}
	entropy := make([]byte, 24)
	if _, err := rand.Read(entropy); err != nil {
		return "", fmt.Errorf("generate placeholder entropy: %w", err)
	}
	return placeholderPrefix + hex.EncodeToString(entropy), nil
}
