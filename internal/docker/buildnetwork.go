// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"fmt"
	"os"
	"strings"
)

// BuildNetworkEnv selects the network for image builds. Some Docker BuildKit
// setups cannot resolve names on the default build network; setting it to
// "host" is the explicit remedy. It is never applied automatically: build
// output comes partly from extension install scripts, which must not be able
// to widen the build's network reach by printing a DNS error.
const BuildNetworkEnv = "ENCLAVE_BUILD_NETWORK"

// BuildNetworkModeFromEnv returns the build network mode requested through
// BuildNetworkEnv: "" for the engine default or "host".
func BuildNetworkModeFromEnv() (string, error) {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(BuildNetworkEnv)))
	switch value {
	case "", "default":
		return "", nil
	case buildNetworkHost:
		return buildNetworkHost, nil
	default:
		return "", fmt.Errorf("%s must be empty or \"host\", got %q", BuildNetworkEnv, value)
	}
}

// BuildNetworkDNSHint explains a DNS failure inside a Docker build and how to
// work around a build network that cannot resolve names.
func BuildNetworkDNSHint() string {
	return fmt.Sprintf("name resolution failed inside the build; if the host resolves names fine, the Docker build network may be the cause: rerun with %s=host", BuildNetworkEnv)
}
