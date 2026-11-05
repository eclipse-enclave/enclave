// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

import (
	"encoding/json"
	"fmt"
)

// EnclaveMetaFrom reads and validates metadata[ExtensionURI]; ok is false when the key is absent.
func EnclaveMetaFrom(metadata map[string]any) (meta EnclaveMeta, ok bool, err error) {
	value, ok := metadata[ExtensionURI]
	if !ok {
		return EnclaveMeta{}, false, nil
	}
	switch v := value.(type) {
	case EnclaveMeta:
		meta = v
	case *EnclaveMeta:
		if v == nil {
			return EnclaveMeta{}, true, fmt.Errorf("a2a: metadata[%q] is null", ExtensionURI)
		}
		meta = *v
	case map[string]any:
		// Decoded JSON arrives as a generic map; round-trip it into the struct.
		data, err := json.Marshal(v)
		if err != nil {
			return EnclaveMeta{}, true, fmt.Errorf("a2a: metadata[%q]: %w", ExtensionURI, err)
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return EnclaveMeta{}, true, fmt.Errorf("a2a: metadata[%q]: %w", ExtensionURI, err)
		}
	default:
		return EnclaveMeta{}, true, fmt.Errorf("a2a: metadata[%q] is not an object", ExtensionURI)
	}
	if err := meta.Validate(); err != nil {
		return EnclaveMeta{}, true, err
	}
	return meta, true, nil
}

// WithEnclaveMeta sets metadata[ExtensionURI] = meta, allocating metadata if nil.
func WithEnclaveMeta(metadata map[string]any, meta EnclaveMeta) map[string]any {
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	metadata[ExtensionURI] = meta
	return metadata
}

// Validate rejects termination and reason values unknown to the extension.
func (m EnclaveMeta) Validate() error {
	switch m.Termination {
	case "", TerminationConfirmed, TerminationUnconfirmed, TerminationNotApplicable:
	default:
		return fmt.Errorf("a2a: unknown termination %q", m.Termination)
	}
	switch m.Reason {
	case "", ReasonCancel, ReasonDeadline, ReasonHubLost, ReasonEpochChanged, ReasonOrchestratorLost, ReasonOutcomeUnknown:
	default:
		return fmt.Errorf("a2a: unknown ending reason %q", m.Reason)
	}
	if m.QueuePosition != nil && *m.QueuePosition < 0 {
		return fmt.Errorf("a2a: negative queue position %d", *m.QueuePosition)
	}
	return nil
}
