// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package exchange

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"enclave/internal/model"
	"enclave/internal/placeholderstore"
	"enclave/internal/util"
)

const CapabilityFile = "native-auth-ready.json"
const capabilityVersion = "native-auth-v2"

type capability struct {
	Version      string `json:"version"`
	ConfigDigest string `json:"configDigest"`
	Session      string `json:"session"`
}

func ReadConfig(path string) (Config, string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- trusted host-produced, read-only gateway configuration.
	if err != nil || len(data) > 1<<20 {
		return Config{}, "", fmt.Errorf("cannot read native auth config")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var config Config
	if err := d.Decode(&config); err != nil {
		return config, "", fmt.Errorf("invalid native auth config")
	}
	if _, err := d.Token(); err != io.EOF {
		return config, "", fmt.Errorf("trailing native auth config data")
	}
	hash := sha256.Sum256(data)
	return config, hex.EncodeToString(hash[:]), nil
}

// Load proves the configured native files and canonical lock are accessible to
// the actual proxy identity. No native values are included in the attestation.
func Load(ctx context.Context, path, dir, lock, session string, env []model.SecretReleaseEntry) (*Store, string, error) {
	config, digest, err := ReadConfig(path)
	if err != nil {
		return nil, "", err
	}
	store, err := New(config, dir, lock, session, env)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, store.timeout)
	defer cancel()
	release, err := store.lock(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("native auth lock unavailable")
	}
	defer release()
	if _, err := placeholderstore.LoadBindings(dir); err != nil {
		return nil, "", fmt.Errorf("native auth bindings unavailable")
	}
	for _, provider := range config.Providers {
		for _, file := range provider.AuthSecrets {
			if file.CheckOnly {
				continue
			}
			_, err := placeholderstore.ReadNative(dir, file)
			if err != nil && !isMissing(err) {
				return nil, "", fmt.Errorf("native auth file unavailable")
			}
		}
	}
	probe, err := os.CreateTemp(dir, ".native-probe-*")
	if err != nil {
		return nil, "", fmt.Errorf("native auth store not writable")
	}
	name := probe.Name()
	_ = probe.Close()
	if err := os.Remove(name); err != nil {
		return nil, "", err
	}
	return store, digest, nil
}

// Called only after the proxy has installed the exchange handler and opened its
// listeners. digest is derived from the configuration Load actually parsed.
func WriteCapability(dir, digest, session string) error {
	data, _ := json.Marshal(capability{capabilityVersion, digest, session})
	return util.WriteFileAtomic(filepath.Join(dir, CapabilityFile), data, 0o600)
}

func CheckCapability(dir, digest, session string) error {
	data, err := os.ReadFile(filepath.Join(dir, CapabilityFile)) // #nosec G304 -- fresh host-owned runtime dir, never mounted into the workload.
	if err != nil {
		return fmt.Errorf("gateway does not support protected native auth")
	}
	var marker capability
	if json.Unmarshal(data, &marker) != nil || marker.Version != capabilityVersion || marker.ConfigDigest != digest || marker.Session != session {
		return fmt.Errorf("gateway native auth capability does not match this session and configuration")
	}
	return nil
}
