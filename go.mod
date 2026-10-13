// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

module enclave

go 1.24.0

toolchain go1.26.9

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/moby/patternmatcher v0.6.1
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	github.com/tidwall/jsonc v0.3.2
	golang.org/x/crypto v0.48.0
	golang.org/x/sys v0.41.0
	golang.org/x/term v0.40.0
	sigs.k8s.io/yaml v1.6.0
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
)
