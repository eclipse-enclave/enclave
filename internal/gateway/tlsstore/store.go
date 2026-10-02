// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tlsstore

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"enclave/internal/domainpattern"
	"enclave/internal/util"
)

const (
	defaultLeafCacheSize = 256
	defaultCAName        = "Enclave Gateway CA"
	rsaKeyBits           = 2048
)

type Store struct {
	RootDir     string
	HostsDir    string
	CACertPath  string
	CAKeyPath   string
	MaxLeafCert int

	mu     sync.Mutex
	caCert *x509.Certificate
	caKey  any
}

func New(rootDir string) *Store {
	return &Store{
		RootDir:     rootDir,
		HostsDir:    filepath.Join(rootDir, "hosts"),
		CACertPath:  filepath.Join(rootDir, "ca.crt"),
		CAKeyPath:   filepath.Join(rootDir, "ca.key"),
		MaxLeafCert: defaultLeafCacheSize,
	}
}

func (s *Store) withLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return util.WithFileLock(filepath.Join(s.HostsDir, ".cache.lock"), fn)
}

func (s *Store) EnsureCA() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withCALock()
}

// The CA never changes after it is loaded, so cache hits skip the file lock and
// the RSA key parse that would otherwise run on every TLS handshake.
func (s *Store) withCALock() error {
	if s.caCert != nil && s.caKey != nil {
		return nil
	}
	return util.WithFileLock(filepath.Join(s.RootDir, ".ca.lock"), s.ensureCA)
}

func (s *Store) EnsureLeaf(host string) (certPath, keyPath string, err error) {
	err = s.withLock(func() error {
		var inner error
		certPath, keyPath, inner = s.ensureLeaf(host)
		return inner
	})
	return
}

func (s *Store) ensureLeaf(host string) (string, string, error) {
	normalizedHost, err := domainpattern.NormalizeHost(host)
	if err != nil {
		return "", "", err
	}
	if err := s.withCALock(); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(s.HostsDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create hosts dir: %w", err)
	}

	hostKey := hostCacheKey(normalizedHost)
	certPath := filepath.Join(s.HostsDir, hostKey+".crt")
	keyPath := filepath.Join(s.HostsDir, hostKey+".key")
	if s.caCert == nil || s.caKey == nil {
		s.caCert, s.caKey, err = loadCA(s.CACertPath, s.CAKeyPath)
		if err != nil {
			return "", "", err
		}
	}
	if s.validLeaf(certPath, keyPath, normalizedHost) {
		now := time.Now()
		_ = os.Chtimes(certPath, now, now)
		_ = os.Chtimes(keyPath, now, now)
		return certPath, keyPath, nil
	}

	if err := writeLeaf(certPath, keyPath, normalizedHost, s.caCert, s.caKey); err != nil {
		return "", "", err
	}
	if err := s.pruneLeafCache(hostKey); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func (s *Store) validLeaf(certPath, keyPath, host string) bool {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false
	}
	roots := x509.NewCertPool()
	roots.AddCert(s.caCert)
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, CurrentTime: time.Now().Add(24 * time.Hour)})
	return err == nil && !time.Now().Before(leaf.NotBefore)
}

func (s *Store) Certificate(host string) (cert *tls.Certificate, err error) {
	err = s.withLock(func() error {
		certPath, keyPath, err := s.ensureLeaf(host)
		if err != nil {
			return err
		}
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return fmt.Errorf("load host certificate: %w", err)
		}
		cert = &pair
		return nil
	})
	return
}

func (s *Store) ensureCA() error {
	if util.FileExists(s.CACertPath) && util.FileExists(s.CAKeyPath) {
		pair, err := tls.LoadX509KeyPair(s.CACertPath, s.CAKeyPath)
		if err != nil {
			return fmt.Errorf("load trusted CA pair from %s: %w", s.RootDir, err)
		}
		s.caCert, err = x509.ParseCertificate(pair.Certificate[0])
		s.caKey = pair.PrivateKey
		return err
	}
	if util.FileExists(s.CACertPath) || util.FileExists(s.CAKeyPath) {
		present, missing := s.CACertPath, s.CAKeyPath
		if !util.FileExists(present) {
			present, missing = missing, present
		}
		return fmt.Errorf("incomplete trusted CA pair: %s is missing; restore it or remove %s to generate a new CA", missing, present)
	}
	if err := os.MkdirAll(s.RootDir, 0o700); err != nil {
		return fmt.Errorf("create tls dir: %w", err)
	}
	priv, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		return fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return fmt.Errorf("generate CA serial: %w", err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: defaultCAName, Organization: []string{"Enclave"}},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("create CA cert: %w", err)
	}
	if err := writePEMFile(s.CACertPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("encode CA key: %w", err)
	}
	if err := writePEMFile(s.CAKeyPath, "PRIVATE KEY", keyBytes, 0o600); err != nil {
		return err
	}
	return nil
}

func (s *Store) pruneLeafCache(keepKey string) error {
	limit := s.MaxLeafCert
	if limit <= 0 {
		limit = defaultLeafCacheSize
	}
	entries, err := os.ReadDir(s.HostsDir)
	if err != nil {
		return fmt.Errorf("read hosts cache: %w", err)
	}
	type certEntry struct {
		key     string
		modTime time.Time
	}
	var certs []certEntry
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".crt" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		certs = append(certs, certEntry{
			key:     strings.TrimSuffix(entry.Name(), ".crt"),
			modTime: info.ModTime(),
		})
	}
	if len(certs) <= limit {
		return nil
	}
	sort.Slice(certs, func(i, j int) bool {
		return certs[i].modTime.Before(certs[j].modTime)
	})
	removeCount := len(certs) - limit
	for _, entry := range certs {
		// Timestamps alone cannot guarantee the requested certificate is newest.
		if entry.key == keepKey {
			continue
		}
		key := entry.key
		_ = os.Remove(filepath.Join(s.HostsDir, key+".crt"))
		_ = os.Remove(filepath.Join(s.HostsDir, key+".key"))
		removeCount--
		if removeCount == 0 {
			break
		}
	}
	return nil
}

// Leaves use ECDSA P-256 because they are minted under the store mutex during
// the client's TLS handshake; RSA keygen there would serialize cold handshakes.
func writeLeaf(certPath string, keyPath string, host string, caCert *x509.Certificate, caKey any) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return fmt.Errorf("generate leaf serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-1 * time.Hour),
		NotAfter:     now.AddDate(0, 3, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create leaf certificate: %w", err)
	}
	if err := writePEMFile(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("encode leaf key: %w", err)
	}
	if err := writePEMFile(keyPath, "PRIVATE KEY", keyBytes, 0o600); err != nil {
		return err
	}
	return nil
}

func loadCA(certPath string, keyPath string) (*x509.Certificate, any, error) {
	certPEM, err := os.ReadFile(certPath) // #nosec G304 -- path is trusted and resolved by runtime config.
	if err != nil {
		return nil, nil, fmt.Errorf("read CA cert: %w", err)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("parse CA cert PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}

	keyPEM, err := os.ReadFile(keyPath) // #nosec G304 -- path is trusted and resolved by runtime config.
	if err != nil {
		return nil, nil, fmt.Errorf("read CA key: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("parse CA key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	return cert, key, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func writePEMFile(path string, pemType string, contents []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: pemType, Bytes: contents}); err != nil {
		return fmt.Errorf("write PEM %s: %w", path, err)
	}
	if err := util.WriteFileAtomic(path, buf.Bytes(), mode); err != nil {
		return fmt.Errorf("atomic rename for %s: %w", path, err)
	}
	return nil
}

func hostCacheKey(host string) string {
	sum := sha256.Sum256([]byte(host))
	return hex.EncodeToString(sum[:16])
}
