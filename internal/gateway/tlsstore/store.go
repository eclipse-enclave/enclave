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

	mu      sync.Mutex
	caCert  *x509.Certificate
	caKey   any
	caRoots *x509.CertPool
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

func (s *Store) EnsureCA() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureCA()
}

// The CA never changes after it is loaded, so cache hits skip the file lock and
// the RSA key parse that would otherwise run on every TLS handshake. The file
// lock serializes CA creation across concurrent host enclave processes.
func (s *Store) ensureCA() error {
	if s.caCert != nil {
		return nil
	}
	return util.WithFileLock(filepath.Join(s.RootDir, ".ca.lock"), s.loadOrCreateCA)
}

func (s *Store) EnsureLeaf(host string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	certPath, keyPath, _, err := s.ensureLeaf(host)
	return certPath, keyPath, err
}

func (s *Store) Certificate(host string) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, pair, err := s.ensureLeaf(host)
	return pair, err
}

func (s *Store) ensureLeaf(host string) (string, string, *tls.Certificate, error) {
	normalizedHost, err := domainpattern.NormalizeHost(host)
	if err != nil {
		return "", "", nil, err
	}
	if err := s.ensureCA(); err != nil {
		return "", "", nil, err
	}
	if err := os.MkdirAll(s.HostsDir, 0o700); err != nil {
		return "", "", nil, fmt.Errorf("create hosts dir: %w", err)
	}

	hostKey := hostCacheKey(normalizedHost)
	certPath := filepath.Join(s.HostsDir, hostKey+".crt")
	keyPath := filepath.Join(s.HostsDir, hostKey+".key")
	if pair := s.cachedLeaf(certPath, keyPath, normalizedHost); pair != nil {
		now := time.Now()
		_ = os.Chtimes(certPath, now, now)
		_ = os.Chtimes(keyPath, now, now)
		return certPath, keyPath, pair, nil
	}

	pair, err := writeLeaf(certPath, keyPath, normalizedHost, s.caCert, s.caKey)
	if err != nil {
		return "", "", nil, err
	}
	if err := s.pruneLeafCache(hostKey); err != nil {
		return "", "", nil, err
	}
	return certPath, keyPath, pair, nil
}

// cachedLeaf returns nil unless the cached pair is signed by the current CA,
// covers host, and stays valid for at least another day.
func (s *Store) cachedLeaf(certPath, keyPath, host string) *tls.Certificate {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil
	}
	now := time.Now()
	_, err = pair.Leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: s.caRoots, CurrentTime: now.Add(24 * time.Hour)})
	if err != nil || now.Before(pair.Leaf.NotBefore) {
		return nil
	}
	return &pair
}

func (s *Store) setCA(cert *x509.Certificate, key any) {
	s.caCert = cert
	s.caKey = key
	s.caRoots = x509.NewCertPool()
	s.caRoots.AddCert(cert)
}

func (s *Store) loadOrCreateCA() error {
	if util.FileExists(s.CACertPath) && util.FileExists(s.CAKeyPath) {
		pair, err := tls.LoadX509KeyPair(s.CACertPath, s.CAKeyPath)
		if err != nil {
			return fmt.Errorf("load trusted CA pair from %s: %w", s.RootDir, err)
		}
		s.setCA(pair.Leaf, pair.PrivateKey)
		return nil
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
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
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
	s.setCA(cert, priv)
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
func writeLeaf(certPath string, keyPath string, host string, caCert *x509.Certificate, caKey any) (*tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("generate leaf serial: %w", err)
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
		return nil, fmt.Errorf("create leaf certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse leaf certificate: %w", err)
	}
	if err := writePEMFile(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return nil, err
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("encode leaf key: %w", err)
	}
	if err := writePEMFile(keyPath, "PRIVATE KEY", keyBytes, 0o600); err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}, nil
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
