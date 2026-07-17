// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tlsstore

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureCAIsIdempotent(t *testing.T) {
	store := New(t.TempDir())
	if err := store.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA() error = %v", err)
	}
	firstCert, err := os.ReadFile(store.CACertPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", store.CACertPath, err)
	}
	if err := store.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA() second call error = %v", err)
	}
	secondCert, err := os.ReadFile(store.CACertPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", store.CACertPath, err)
	}
	if string(firstCert) != string(secondCert) {
		t.Fatalf("CA cert changed across EnsureCA() calls")
	}
	block, _ := pem.Decode(firstCert)
	if block == nil {
		t.Fatalf("failed to decode CA cert PEM")
	}
	certBytes := []byte(nil)
	if block != nil {
		certBytes = block.Bytes
	}
	cert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}
	if !cert.IsCA {
		t.Fatalf("cert.IsCA = false, want true")
	}
	if cert.PublicKeyAlgorithm != x509.RSA {
		t.Fatalf("cert.PublicKeyAlgorithm = %v, want RSA", cert.PublicKeyAlgorithm)
	}
	if _, ok := cert.PublicKey.(*rsa.PublicKey); !ok {
		t.Fatalf("cert public key type = %T, want *rsa.PublicKey", cert.PublicKey)
	}
}

func TestEnsureLeafCreatesCertificateForHost(t *testing.T) {
	store := New(t.TempDir())
	certPath, keyPath, err := store.EnsureLeaf("api.example.com")
	if err != nil {
		t.Fatalf("EnsureLeaf() error = %v", err)
	}
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("Stat(%q) error = %v", certPath, err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("Stat(%q) error = %v", keyPath, err)
	}
	pair, err := store.Certificate("api.example.com")
	if err != nil {
		t.Fatalf("Certificate() error = %v", err)
	}
	if len(pair.Certificate) == 0 {
		t.Fatalf("Certificate() returned empty cert chain")
	}
	parsed, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}
	if len(parsed.DNSNames) != 1 || parsed.DNSNames[0] != "api.example.com" {
		t.Fatalf("DNSNames = %v, want [api.example.com]", parsed.DNSNames)
	}
	if parsed.PublicKeyAlgorithm != x509.RSA {
		t.Fatalf("leaf PublicKeyAlgorithm = %v, want RSA", parsed.PublicKeyAlgorithm)
	}
	if parsed.KeyUsage&x509.KeyUsageKeyEncipherment == 0 {
		t.Fatalf("leaf KeyUsage missing KeyEncipherment: %v", parsed.KeyUsage)
	}
}

func TestCertificateRefreshesLeafMtimeOnRepeatedUse(t *testing.T) {
	store := New(t.TempDir())
	certPath, _, err := store.EnsureLeaf("api.example.com")
	if err != nil {
		t.Fatalf("EnsureLeaf() error = %v", err)
	}

	if _, err := store.Certificate("api.example.com"); err != nil {
		t.Fatalf("Certificate() first call error = %v", err)
	}
	infoBefore, err := os.Stat(certPath)
	if err != nil {
		t.Fatalf("Stat(%q) after first Certificate() error = %v", certPath, err)
	}

	time.Sleep(20 * time.Millisecond)

	if _, err := store.Certificate("api.example.com"); err != nil {
		t.Fatalf("Certificate() second call error = %v", err)
	}
	infoAfter, err := os.Stat(certPath)
	if err != nil {
		t.Fatalf("Stat(%q) after second Certificate() error = %v", certPath, err)
	}
	if !infoAfter.ModTime().After(infoBefore.ModTime()) {
		t.Fatalf("cert mtime did not advance: before=%s after=%s", infoBefore.ModTime(), infoAfter.ModTime())
	}
}

func TestEnsureLeafPrunesOldEntries(t *testing.T) {
	store := New(t.TempDir())
	store.MaxLeafCert = 2

	if _, _, err := store.EnsureLeaf("one.example.com"); err != nil {
		t.Fatalf("EnsureLeaf(one) error = %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, _, err := store.EnsureLeaf("two.example.com"); err != nil {
		t.Fatalf("EnsureLeaf(two) error = %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, _, err := store.EnsureLeaf("three.example.com"); err != nil {
		t.Fatalf("EnsureLeaf(three) error = %v", err)
	}

	oneKey := hostCacheKey("one.example.com")
	oneCert := filepath.Join(store.HostsDir, oneKey+".crt")
	if _, err := os.Stat(oneCert); !os.IsNotExist(err) {
		t.Fatalf("oldest cert %q still exists; prune did not run", oneCert)
	}
}
