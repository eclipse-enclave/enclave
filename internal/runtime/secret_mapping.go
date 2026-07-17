// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import "enclave/internal/model"

type SecretMapping struct {
	Entries []model.SecretReleaseEntry
}

func (m SecretMapping) HasEntries() bool {
	return len(m.Entries) > 0
}
