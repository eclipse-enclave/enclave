// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "fmt"

const SecretPlaceholderMarker = "ENCLAVE_SECRET_" // #nosec G101 -- recognition marker, not a credential.

// SecretPlaceholderConfig describes a synthetic credential, independent of its secret.
type SecretPlaceholderConfig struct {
	Prefix string                  `json:"prefix,omitempty"`
	Suffix string                  `json:"suffix,omitempty"`
	Random SecretPlaceholderRandom `json:"random"`
}

type SecretPlaceholderRandom struct {
	Encoding string `json:"encoding"`
	Length   int    `json:"length"`
}

// ValidatedAlphabet returns the generation alphabet after validating the shape. Lengths
// are character counts; each supported minimum provides at least 128 random bits.
func (c SecretPlaceholderConfig) ValidatedAlphabet() (string, error) {
	var alphabet string
	var minimum int
	switch c.Random.Encoding {
	case "hex":
		alphabet, minimum = "0123456789abcdef", 32
	case "base64url":
		alphabet, minimum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", 22
	case "alphanumeric":
		alphabet, minimum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", 22
	default:
		return "", fmt.Errorf("placeholder.random.encoding must be hex, base64url, or alphanumeric")
	}
	if c.Random.Length < minimum || c.Random.Length > 1024 {
		return "", fmt.Errorf("placeholder.random.length must be between %d and 1024 for %s", minimum, c.Random.Encoding)
	}
	for _, literal := range []string{c.Prefix, c.Suffix} {
		if err := ValidateSecretLiteral(literal, "placeholder prefix and suffix"); err != nil {
			return "", err
		}
	}
	return alphabet, nil
}

// ValidateSecretLiteral checks a literal that is matched or substituted in credential
// values: at most 256 bytes of non-whitespace printable ASCII. The subject names the
// field in error messages.
func ValidateSecretLiteral(literal string, subject string) error {
	if len(literal) > 256 {
		return fmt.Errorf("%s must not exceed 256 bytes", subject)
	}
	for _, b := range []byte(literal) {
		if b < 33 || b > 126 {
			return fmt.Errorf("%s must contain only non-whitespace printable ASCII", subject)
		}
	}
	return nil
}
