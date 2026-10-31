// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tlsstore

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if parsed.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("leaf PublicKeyAlgorithm = %v, want ECDSA", parsed.PublicKeyAlgorithm)
	}
	if parsed.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Fatalf("leaf KeyUsage missing DigitalSignature: %v", parsed.KeyUsage)
	}
	if parsed.KeyUsage&x509.KeyUsageKeyEncipherment != 0 {
		t.Fatalf("leaf KeyUsage has KeyEncipherment, which is meaningless for ECDSA: %v", parsed.KeyUsage)
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

func TestLeafPruningKeepsRequestedCertificate(t *testing.T) {
	store := New(t.TempDir())
	store.MaxLeafCert = 1
	certPath, keyPath, err := store.EnsureLeaf("existing.example")
	if err != nil {
		t.Fatal(err)
	}
	// A touched cache entry can look newer than a fresh file on a filesystem
	// with coarse timestamps, or after the system clock moves backwards.
	future := time.Now().Add(time.Hour)
	for _, path := range []string{certPath, keyPath} {
		if err := os.Chtimes(path, future, future); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Certificate("requested.example"); err != nil {
		t.Fatalf("pruned the certificate being returned: %v", err)
	}
	if _, err := os.Stat(certPath); !os.IsNotExist(err) {
		t.Fatalf("old entry remains in a one-entry cache: %v", err)
	}
}

func TestCertificateRenewsInvalidCachedLeaves(t *testing.T) {
	store := New(t.TempDir())
	for _, age := range []time.Duration{-time.Hour, time.Hour} {
		original, err := store.Certificate("api.example")
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(original.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		leaf.NotAfter = time.Now().Add(age)
		der, err := x509.CreateCertificate(rand.Reader, leaf, store.caCert, leaf.PublicKey, store.caKey)
		if err != nil {
			t.Fatal(err)
		}
		certPath := filepath.Join(store.HostsDir, hostCacheKey("api.example")+".crt")
		if err := writePEMFile(certPath, "CERTIFICATE", der, 0o644); err != nil {
			t.Fatal(err)
		}
		renewed, err := store.Certificate("api.example")
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := x509.ParseCertificate(renewed.Certificate[0])
		if err != nil || time.Until(parsed.NotAfter) < 48*time.Hour {
			t.Fatalf("leaf not renewed: %v", err)
		}
	}
	keyPath := filepath.Join(store.HostsDir, hostCacheKey("api.example")+".key")
	if err := os.WriteFile(keyPath, []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Certificate("api.example"); err != nil {
		t.Fatalf("malformed leaf not repaired: %v", err)
	}
}

func TestConcurrentStoresShareCA(t *testing.T) {
	dir := t.TempDir()
	stores := []*Store{New(dir), New(dir), New(dir)}
	var wg sync.WaitGroup
	for _, store := range stores {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			if err := store.EnsureCA(); err != nil {
				t.Errorf("EnsureCA: %v", err)
			}
		}(store)
	}
	wg.Wait()
	for _, store := range stores[1:] {
		if store.caCert == nil || !store.caCert.Equal(stores[0].caCert) {
			t.Fatal("concurrent stores loaded different CAs")
		}
	}
}

func TestConcurrentCertificatesSurvivePruning(t *testing.T) {
	store := New(t.TempDir())
	store.MaxLeafCert = 1
	var wg sync.WaitGroup
	for _, host := range []string{"one.example", "two.example", "three.example"} {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				pair, err := store.Certificate(host)
				if err != nil {
					t.Errorf("Certificate: %v", err)
					return
				}
				if err := pair.Leaf.VerifyHostname(host); err != nil {
					t.Error(err)
				}
			}
		}(host)
	}
	wg.Wait()
}

func TestEnsureCADoesNotAccessProxyOwnedLeafCache(t *testing.T) {
	store := New(t.TempDir())
	if err := os.Mkdir(store.HostsDir, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(store.HostsDir, 0o700) }()
	if err := store.EnsureCA(); err != nil {
		t.Fatalf("host CA setup accessed proxy-owned cache: %v", err)
	}
}

func TestEnsureCAPreservesIncompletePair(t *testing.T) {
	for _, keep := range []string{"ca.crt", "ca.key"} {
		t.Run(keep, func(t *testing.T) {
			store := New(t.TempDir())
			if err := store.EnsureCA(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.RootDir, keep)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			missing := store.CACertPath
			if keep == "ca.crt" {
				missing = store.CAKeyPath
			}
			if err := os.Remove(missing); err != nil {
				t.Fatal(err)
			}
			err = New(store.RootDir).EnsureCA()
			if err == nil {
				t.Fatal("incomplete CA pair accepted")
			}
			if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), path) {
				t.Fatalf("error does not name both CA files: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("existing trust material changed: %v", err)
			}
			if _, err := os.Stat(missing); !os.IsNotExist(err) {
				t.Fatalf("missing CA file was regenerated: %v", err)
			}
		})
	}
}
