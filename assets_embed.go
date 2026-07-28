// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !enclave_no_embed

// Package assets contains the runtime files embedded in the Enclave CLI.
package assets

import (
	"embed"
	"io/fs"

	"enclave/internal/appassets"
)

// Keep this inventory aligned with the runtime assets installed by
// debian/rules. The all prefix includes any dotfiles added below these trees in
// future. The repository-root .dockerignore must be listed directly.
//
//go:embed .dockerignore Dockerfile Dockerfile.gateway entrypoint.sh gateway-entrypoint.sh LICENSE.md NOTICE.md
//go:embed all:docs all:extensions all:runtime-assets
//go:embed go.mod go.sum all:cmd/enclave-gateway-proxy
//go:embed all:internal/appassets all:internal/config all:internal/domainpattern
//go:embed all:internal/gateway/bundle all:internal/gateway/mitm all:internal/gateway/tlsstore
//go:embed all:internal/git all:internal/logx all:internal/model all:internal/network all:internal/secretfile all:internal/util
var files embed.FS

func init() {
	appassets.Register(files)
}

// FS returns the embedded asset filesystem rooted at the repository layout.
func FS() fs.FS {
	return files
}
