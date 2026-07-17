// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tlsstore

import (
	"bytes"
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

func (s *Store) EnsureCA() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureCA()
}

func (s *Store) EnsureLeaf(host string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalizedHost, err := domainpattern.NormalizeHost(host)
	if err != nil {
		return "", "", err
	}
	if err := s.ensureCA(); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(s.HostsDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create hosts dir: %w", err)
	}

	hostKey := hostCacheKey(normalizedHost)
	certPath := filepath.Join(s.HostsDir, hostKey+".crt")
	keyPath := filepath.Join(s.HostsDir, hostKey+".key")
	if util.FileExists(certPath) && util.FileExists(keyPath) {
		now := time.Now()
		_ = os.Chtimes(certPath, now, now)
		_ = os.Chtimes(keyPath, now, now)
		return certPath, keyPath, nil
	}

	if s.caCert == nil || s.caKey == nil {
		caCert, caKey, err := loadCA(s.CACertPath, s.CAKeyPath)
		if err != nil {
			return "", "", err
		}
		s.caCert = caCert
		s.caKey = caKey
	}
	if err := writeLeaf(certPath, keyPath, normalizedHost, s.caCert, s.caKey); err != nil {
		return "", "", err
	}
	if err := s.pruneLeafCache(); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func (s *Store) Certificate(host string) (*tls.Certificate, error) {
	certPath, keyPath, err := s.EnsureLeaf(host)
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load host certificate: %w", err)
	}
	return &cert, nil
}

func (s *Store) ensureCA() error {
	if util.FileExists(s.CACertPath) && util.FileExists(s.CAKeyPath) {
		return nil
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

func (s *Store) pruneLeafCache() error {
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
	for i := 0; i < removeCount; i++ {
		key := certs[i].key
		_ = os.Remove(filepath.Join(s.HostsDir, key+".crt"))
		_ = os.Remove(filepath.Join(s.HostsDir, key+".key"))
	}
	return nil
}

func writeLeaf(certPath string, keyPath string, host string, caCert *x509.Certificate, caKey any) error {
	priv, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
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
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
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
