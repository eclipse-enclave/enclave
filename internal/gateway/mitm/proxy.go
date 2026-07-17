// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mitm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/cryptobyte"

	"enclave/internal/domainpattern"
	"enclave/internal/gateway/tlsstore"
	"enclave/internal/model"
	"enclave/internal/util"
)

type Options struct {
	HTTPAddr       string
	HTTPSAddr      string
	AllowedDomains []string
	DeniedDomains  []string
	SecretRules    []model.SecretReleaseEntry
	AuditLogPath   string
	ForceHTTPSMITM bool
	TLSStore       *tlsstore.Store
	OnReady        func()
}

const serverShutdownTimeout = 1 * time.Second

const maxTLSRecordsBeforeClientHelloLength = 16

type auditEvent struct {
	Timestamp    string `json:"ts"`
	Type         string `json:"type"`
	Method       string `json:"method,omitempty"`
	Domain       string `json:"domain,omitempty"`
	Path         string `json:"path,omitempty"`
	Port         int    `json:"port,omitempty"`
	Status       int    `json:"status,omitempty"`
	RequestSize  int64  `json:"req_bytes,omitempty"`
	ResponseSize int64  `json:"resp_bytes,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	Verdict      string `json:"verdict"`
	Rule         string `json:"rule,omitempty"`
}

type auditLogger struct {
	mu   sync.Mutex
	file *os.File
}

func newAuditLogger(path string) (*auditLogger, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return &auditLogger{}, nil
	}
	file, err := os.OpenFile(trimmed, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- path comes from trusted runtime wiring.
	if err != nil {
		return nil, err
	}
	return &auditLogger{file: file}, nil
}

func (l *auditLogger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

func (l *auditLogger) Log(event auditEvent) {
	if l == nil || l.file == nil {
		return
	}
	if strings.TrimSpace(event.Timestamp) == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	payload = append(payload, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.Write(payload)
}

func LoadRules(path string) ([]model.SecretReleaseEntry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- path comes from controlled runtime env.
	if err != nil {
		return nil, fmt.Errorf("read secret release rules: %w", err)
	}
	var rules []model.SecretReleaseEntry
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("parse secret release rules: %w", err)
	}
	normalizedRules := make([]model.SecretReleaseEntry, 0, len(rules))
	for _, rule := range rules {
		normalized, err := normalizeRule(rule)
		if err != nil {
			return nil, err
		}
		normalizedRules = append(normalizedRules, normalized)
	}
	return normalizedRules, nil
}

func Run(ctx context.Context, opts Options) error {
	if opts.TLSStore == nil {
		return fmt.Errorf("TLS store is required")
	}
	httpAddr := strings.TrimSpace(opts.HTTPAddr)
	if httpAddr == "" {
		httpAddr = ":8080"
	}
	httpsAddr := strings.TrimSpace(opts.HTTPSAddr)
	if httpsAddr == "" {
		httpsAddr = ":8443"
	}

	audit, err := newAuditLogger(opts.AuditLogPath)
	if err != nil {
		return fmt.Errorf("open proxy audit log: %w", err)
	}
	defer func() { _ = audit.Close() }()

	proxy := newProxy(opts.AllowedDomains, opts.DeniedDomains, opts.SecretRules, opts.ForceHTTPSMITM, audit)
	httpServer := &http.Server{
		Addr:              httpAddr,
		Handler:           proxy.handler("http"),
		ReadHeaderTimeout: 10 * time.Second,
	}
	httpListener, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("listen HTTP proxy on %s: %w", httpAddr, err)
	}

	httpsListener, err := net.Listen("tcp", httpsAddr)
	if err != nil {
		_ = httpListener.Close()
		return fmt.Errorf("listen HTTPS proxy on %s: %w", httpsAddr, err)
	}
	httpsServer, err := newHTTPSDispatchServer(proxy, opts.TLSStore, httpsListener)
	if err != nil {
		_ = httpListener.Close()
		_ = httpsListener.Close()
		return fmt.Errorf("create HTTPS dispatch server: %w", err)
	}

	errCh := make(chan error, 2)
	go func() {
		if err := httpServer.Serve(httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			errCh <- fmt.Errorf("serve HTTP proxy: %w", err)
		}
	}()
	go func() {
		if err := httpsServer.Serve(); err != nil && !errors.Is(err, net.ErrClosed) {
			errCh <- fmt.Errorf("serve HTTPS proxy: %w", err)
		}
	}()
	if opts.OnReady != nil {
		opts.OnReady()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		_ = httpsServer.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		_ = httpServer.Close()
		_ = httpsServer.Shutdown(context.Background())
		return err
	}
}

type proxy struct {
	allowedDomains []string
	deniedDomains  []string
	secretRules    []model.SecretReleaseEntry
	forceHTTPSMITM bool
	audit          *auditLogger
	transport      *http.Transport
}

func normalizeHostPatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		value, err := domainpattern.NormalizeHost(pattern)
		if err != nil || value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func newProxy(allowedDomains []string, deniedDomains []string, secretRules []model.SecretReleaseEntry, forceHTTPSMITM bool, audit *auditLogger) *proxy {
	return &proxy{
		allowedDomains: normalizeHostPatterns(allowedDomains),
		deniedDomains:  normalizeHostPatterns(deniedDomains),
		secretRules:    secretRules,
		forceHTTPSMITM: forceHTTPSMITM,
		audit:          audit,
		transport: &http.Transport{
			Proxy:                 nil,
			ForceAttemptHTTP2:     false,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 120 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
		},
	}
}

func (p *proxy) handler(scheme string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		host := normalizedRequestHost(req)
		event := newHTTPAuditEvent(req, host, scheme)
		if scheme == "https" {
			if sni := normalizedTLSHost(req); sni != "" && host != "" && sni != host {
				mismatchEvent := event
				mismatchEvent.Domain = host
				mismatchEvent.Verdict = "pass"
				mismatchEvent.Rule = "sni-host-mismatch"
				p.audit.Log(mismatchEvent)
			}
		}
		if host == "" {
			event.Status = http.StatusBadRequest
			event.Verdict = "deny"
			event.Rule = "invalid-host"
			p.audit.Log(event)
			http.Error(w, "Missing target host", http.StatusBadRequest)
			return
		}
		if !p.hostPermitted(host) {
			event.Status = http.StatusForbidden
			event.Verdict = "deny"
			event.Rule = "allowlist"
			p.audit.Log(event)
			http.Error(w, "Domain not in allowlist", http.StatusForbidden)
			return
		}
		if err := rewriteHeaders(scheme, host, req.Header, p.secretRules); err != nil {
			event.Status = http.StatusForbidden
			event.Verdict = "deny"
			event.Rule = "secret-injection"
			p.audit.Log(event)
			http.Error(w, "request denied", http.StatusForbidden)
			return
		}
		result, err := p.forward(w, req, scheme)
		if err != nil {
			event.Status = http.StatusBadGateway
			event.Verdict = "pass"
			event.Rule = "upstream-error"
			p.audit.Log(event)
			http.Error(w, "Upstream request failed", http.StatusBadGateway)
			return
		}
		event.Status = result.StatusCode
		event.ResponseSize = result.ResponseBytes
		event.ContentType = result.ContentType
		event.Verdict = "pass"
		event.Rule = "allowlist"
		p.audit.Log(event)
	}
}

type forwardResult struct {
	StatusCode    int
	ResponseBytes int64
	ContentType   string
}

func (p *proxy) forward(w http.ResponseWriter, req *http.Request, scheme string) (forwardResult, error) {
	target := *req.URL
	target.Scheme = scheme
	host := req.Host
	if strings.TrimSpace(host) == "" {
		host = normalizedRequestHost(req)
	}
	target.Host = host
	if strings.TrimSpace(target.Host) == "" {
		return forwardResult{}, fmt.Errorf("missing request host")
	}
	if !strings.Contains(target.Host, ":") {
		if scheme == "https" {
			target.Host += ":443"
		} else {
			target.Host += ":80"
		}
	}
	outgoing := req.Clone(req.Context())
	outgoing.RequestURI = ""
	outgoing.URL = &target
	outgoing.Header.Set("X-Forwarded-Proto", scheme)
	if clientIP := clientIPFromAddr(req.RemoteAddr); clientIP != "" {
		if prior := strings.TrimSpace(outgoing.Header.Get("X-Forwarded-For")); prior != "" {
			outgoing.Header.Set("X-Forwarded-For", prior+", "+clientIP)
		} else {
			outgoing.Header.Set("X-Forwarded-For", clientIP)
		}
	}
	resp, err := p.transport.RoundTrip(outgoing)
	if err != nil {
		return forwardResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	responseBytes, _ := io.Copy(w, resp.Body)
	return forwardResult{
		StatusCode:    resp.StatusCode,
		ResponseBytes: responseBytes,
		ContentType:   strings.TrimSpace(resp.Header.Get("Content-Type")),
	}, nil
}

type httpsDispatchServer struct {
	listener     net.Listener
	mitmListener net.Listener
	proxy        *proxy
	mitmServer   *http.Server
	mitmAddr     string

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	closing bool
}

func newHTTPSDispatchServer(proxy *proxy, store *tlsstore.Store, listener net.Listener) (*httpsDispatchServer, error) {
	mitmListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &httpsDispatchServer{
		listener:     listener,
		mitmListener: mitmListener,
		proxy:        proxy,
		mitmServer: &http.Server{
			Handler:           proxy.handler("https"),
			ReadHeaderTimeout: 10 * time.Second,
			TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
					if hello == nil || strings.TrimSpace(hello.ServerName) == "" {
						return nil, errors.New("missing SNI")
					}
					return store.Certificate(hello.ServerName)
				},
			},
		},
		mitmAddr: mitmListener.Addr().String(),
		conns:    map[net.Conn]struct{}{},
	}
	return server, nil
}

func (s *httpsDispatchServer) Serve() error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.mitmServer.ServeTLS(s.mitmListener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			errCh <- err
			_ = s.listener.Close()
		}
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case mitmErr := <-errCh:
				return mitmErr
			default:
			}
			if s.isClosing() {
				return nil
			}
			return err
		}
		s.trackConn(conn)
		go s.serveConn(conn)
	}
}

func (s *httpsDispatchServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		_ = s.listener.Close()
		_ = s.mitmListener.Close()
		_ = s.mitmServer.Shutdown(ctx)
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *httpsDispatchServer) serveConn(conn net.Conn) {
	defer s.untrackConn(conn)

	host, preface, err := readClientHello(conn)
	if err != nil {
		s.proxy.audit.Log(newTCPAuditEvent("", "deny", "tls-clienthello"))
		_ = conn.Close()
		return
	}
	if !s.proxy.hostPermitted(host) {
		s.proxy.audit.Log(newTCPAuditEvent(host, "deny", "allowlist"))
		_ = conn.Close()
		return
	}
	if shouldMITM(host, s.proxy.secretRules, s.proxy.forceHTTPSMITM) {
		s.proxy.audit.Log(newTCPAuditEvent(host, "pass", "mitm-dispatch"))
		tunnelToAddress(conn, preface, s.mitmAddr)
		return
	}
	s.proxy.audit.Log(newTCPAuditEvent(host, "pass", "allowlist"))
	tunnelTLS(conn, preface, host)
}

func (s *httpsDispatchServer) trackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		_ = conn.Close()
		return
	}
	s.conns[conn] = struct{}{}
}

func (s *httpsDispatchServer) untrackConn(conn net.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

func (s *httpsDispatchServer) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

func requiresMITM(host string, rules []model.SecretReleaseEntry) bool {
	normalizedHost, err := domainpattern.NormalizeHost(host)
	if err != nil {
		return false
	}
	for _, rule := range rules {
		if hostAllowed(normalizedHost, rule.Hosts) {
			return true
		}
	}
	return false
}

func shouldMITM(host string, rules []model.SecretReleaseEntry, forceHTTPSMITM bool) bool {
	return forceHTTPSMITM || requiresMITM(host, rules)
}

func tunnelTLS(downstream net.Conn, preface []byte, host string) {
	tunnelToAddress(downstream, preface, net.JoinHostPort(host, "443"))
}

func tunnelToAddress(downstream net.Conn, preface []byte, upstreamAddr string) {
	upstream, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp", upstreamAddr)
	if err != nil {
		_ = downstream.Close()
		return
	}
	defer func() { _ = upstream.Close() }()

	errCh := make(chan error, 2)
	go func() {
		_, copyErr := io.Copy(upstream, io.MultiReader(bytes.NewReader(preface), downstream))
		closeWrite(upstream)
		errCh <- copyErr
	}()
	go func() {
		_, copyErr := io.Copy(downstream, upstream)
		closeWrite(downstream)
		errCh <- copyErr
	}()
	<-errCh
	<-errCh
}

func closeWrite(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := conn.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}

func readClientHello(conn net.Conn) (string, []byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	preface := make([]byte, 0, 4096)
	handshake := make([]byte, 0, 4096)
	totalHandshakeLen := -1
	recordCount := 0

	for {
		recordHeader := make([]byte, 5)
		if _, err := io.ReadFull(conn, recordHeader); err != nil {
			return "", nil, err
		}
		preface = append(preface, recordHeader...)
		recordType := recordHeader[0]
		if recordType != 22 {
			return "", nil, fmt.Errorf("unexpected TLS record type: %d", recordType)
		}
		recordLen := int(binary.BigEndian.Uint16(recordHeader[3:5]))
		if recordLen <= 0 || recordLen > 1<<14+2048 {
			return "", nil, fmt.Errorf("invalid TLS record length")
		}
		recordPayload := make([]byte, recordLen)
		if _, err := io.ReadFull(conn, recordPayload); err != nil {
			return "", nil, err
		}
		preface = append(preface, recordPayload...)
		handshake = append(handshake, recordPayload...)

		if totalHandshakeLen == -1 {
			recordCount++
			if recordCount > maxTLSRecordsBeforeClientHelloLength {
				return "", nil, fmt.Errorf("TLS ClientHello length not determined after %d records", maxTLSRecordsBeforeClientHelloLength)
			}
			if len(handshake) < 4 {
				continue
			}
			if handshake[0] != 1 {
				return "", nil, fmt.Errorf("first TLS handshake is not ClientHello")
			}
			totalHandshakeLen = int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
			if totalHandshakeLen <= 0 || totalHandshakeLen > 1<<20 {
				return "", nil, fmt.Errorf("invalid TLS ClientHello length")
			}
		}

		if len(handshake) >= totalHandshakeLen+4 {
			host, err := parseClientHelloServerName(handshake[4 : 4+totalHandshakeLen])
			if err != nil {
				return "", nil, err
			}
			if host == "" {
				return "", nil, fmt.Errorf("missing SNI")
			}
			return host, preface, nil
		}
	}
}

func parseClientHelloServerName(body []byte) (string, error) {
	s := cryptobyte.String(body)
	var legacyVersion uint16
	var random []byte
	if !s.ReadUint16(&legacyVersion) || !s.ReadBytes(&random, 32) {
		return "", fmt.Errorf("invalid ClientHello")
	}

	var sessionID cryptobyte.String
	if !s.ReadUint8LengthPrefixed(&sessionID) {
		return "", fmt.Errorf("invalid ClientHello")
	}

	var cipherSuites cryptobyte.String
	if !s.ReadUint16LengthPrefixed(&cipherSuites) || len(cipherSuites) == 0 {
		return "", fmt.Errorf("invalid ClientHello")
	}

	var compressionMethods cryptobyte.String
	if !s.ReadUint8LengthPrefixed(&compressionMethods) || len(compressionMethods) == 0 {
		return "", fmt.Errorf("invalid ClientHello")
	}

	if s.Empty() {
		return "", nil
	}

	var extensions cryptobyte.String
	if !s.ReadUint16LengthPrefixed(&extensions) || !s.Empty() {
		return "", fmt.Errorf("invalid ClientHello")
	}

	for !extensions.Empty() {
		var extType uint16
		var extData cryptobyte.String
		if !extensions.ReadUint16(&extType) || !extensions.ReadUint16LengthPrefixed(&extData) {
			return "", fmt.Errorf("invalid ClientHello extension")
		}
		if extType != 0 {
			continue
		}

		var nameList cryptobyte.String
		if !extData.ReadUint16LengthPrefixed(&nameList) || !extData.Empty() {
			return "", fmt.Errorf("invalid SNI extension")
		}
		for !nameList.Empty() {
			var nameType uint8
			var nameValue cryptobyte.String
			if !nameList.ReadUint8(&nameType) || !nameList.ReadUint16LengthPrefixed(&nameValue) {
				return "", fmt.Errorf("invalid SNI hostname")
			}
			if nameType == 0 {
				name, err := domainpattern.NormalizeHost(string(nameValue))
				if err == nil && name != "" {
					return name, nil
				}
			}
		}
		return "", nil
	}
	return "", nil
}

func copyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func normalizeRule(rule model.SecretReleaseEntry) (model.SecretReleaseEntry, error) {
	placeholder := strings.TrimSpace(rule.Placeholder)
	if placeholder == "" {
		return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry missing placeholder for %q", rule.SecretID)
	}
	if rule.Value == "" {
		return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry missing secret for %q", rule.SecretID)
	}
	header := strings.ToLower(strings.TrimSpace(rule.Header))
	if header == "" {
		return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry missing header for %q", rule.SecretID)
	}
	format := strings.TrimSpace(rule.Format)
	if format != "" && !strings.Contains(format, "%s") {
		return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry has invalid format for %q", rule.SecretID)
	}
	normalizedHosts := make([]string, 0, len(rule.Hosts))
	for _, host := range rule.Hosts {
		value, err := domainpattern.Normalize(host)
		if err != nil {
			return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry has invalid host pattern %q for %q: %w", strings.TrimSpace(host), rule.SecretID, err)
		}
		if value == "" {
			continue
		}
		normalizedHosts = append(normalizedHosts, value)
	}
	hosts := util.Dedupe(normalizedHosts)
	if len(hosts) == 0 {
		return model.SecretReleaseEntry{}, fmt.Errorf("secret release entry missing hosts for %q", rule.SecretID)
	}
	rule.Placeholder = placeholder
	rule.Header = header
	rule.Format = format
	rule.Hosts = hosts
	return rule, nil
}

func rewriteHeaders(scheme string, host string, headers http.Header, rules []model.SecretReleaseEntry) error {
	normalizedHost, err := domainpattern.NormalizeHost(host)
	if err != nil {
		return fmt.Errorf("invalid request host")
	}
	secureTransport := strings.EqualFold(strings.TrimSpace(scheme), "https")
	for header, values := range headers {
		updated := make([]string, len(values))
		copy(updated, values)
		changed := false
		for i, value := range updated {
			for _, rule := range rules {
				if !strings.Contains(value, rule.Placeholder) {
					continue
				}
				if !secureTransport {
					return fmt.Errorf("request denied")
				}
				if !hostAllowed(normalizedHost, rule.Hosts) {
					return fmt.Errorf("request denied")
				}
				replacement := rule.Value
				// Apply format only when the header value is the bare placeholder;
				// this avoids turning "Bearer <placeholder>" into "Bearer Bearer <secret>".
				if strings.EqualFold(header, rule.Header) && rule.Format != "" && strings.TrimSpace(value) == rule.Placeholder {
					replacement = strings.ReplaceAll(rule.Format, "%s", rule.Value)
				}
				value = strings.ReplaceAll(value, rule.Placeholder, replacement)
				changed = true
			}
			updated[i] = value
		}
		if changed {
			headers[header] = updated
		}
	}
	return nil
}

func hostAllowed(host string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	value, err := domainpattern.NormalizeHost(host)
	if err != nil {
		return false
	}
	for _, pattern := range patterns {
		if domainpattern.MatchNormalizedHost(value, pattern) {
			return true
		}
	}
	return false
}

// hostPermitted enforces deny-wins: a host that matches any denied domain (or
// is a subdomain of one) is rejected even when it would match an allow entry.
// dnsmasq blocks denied apex domains, but the proxy suffix-matches, so a
// denied host more specific than an allowed parent (deny tracking.example.com,
// allow example.com) would otherwise pass here — the deny check closes that.
func (p *proxy) hostPermitted(host string) bool {
	if hostAllowed(host, p.deniedDomains) {
		return false
	}
	return hostAllowed(host, p.allowedDomains)
}

func newHTTPAuditEvent(req *http.Request, host string, scheme string) auditEvent {
	event := auditEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Type:      "http",
		Method:    strings.ToUpper(strings.TrimSpace(reqMethod(req))),
		Domain:    host,
		Path:      requestPath(req),
		Port:      requestPort(req, scheme),
	}
	if req != nil && req.ContentLength > 0 {
		event.RequestSize = req.ContentLength
	}
	return event
}

func reqMethod(req *http.Request) string {
	if req == nil {
		return ""
	}
	return req.Method
}

func requestPath(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "/"
	}
	path := strings.TrimSpace(req.URL.EscapedPath())
	if path == "" {
		path = strings.TrimSpace(req.URL.Path)
	}
	if path == "" {
		return "/"
	}
	return path
}

func requestPort(req *http.Request, scheme string) int {
	defaultPort := 80
	if strings.EqualFold(strings.TrimSpace(scheme), "https") {
		defaultPort = 443
	}
	if req == nil {
		return defaultPort
	}

	if port := parsePortFromHost(req.Host); port > 0 {
		return port
	}
	if req.URL != nil {
		if port := parsePortFromHost(req.URL.Host); port > 0 {
			return port
		}
	}
	return defaultPort
}

func parsePortFromHost(rawHost string) int {
	hostValue := strings.TrimSpace(rawHost)
	if hostValue == "" {
		return 0
	}
	if _, port, err := net.SplitHostPort(hostValue); err == nil {
		parsed, convErr := strconv.Atoi(strings.TrimSpace(port))
		if convErr == nil && parsed > 0 {
			return parsed
		}
		return 0
	}
	return 0
}

func newTCPAuditEvent(domain string, verdict string, rule string) auditEvent {
	return auditEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Type:      "tcp",
		Domain:    strings.TrimSpace(domain),
		Port:      443,
		Verdict:   strings.TrimSpace(verdict),
		Rule:      strings.TrimSpace(rule),
	}
}

func normalizedRequestHost(req *http.Request) string {
	if req == nil {
		return ""
	}
	if host := normalizeHostValue(req.Host); host != "" {
		return host
	}
	if req.URL != nil {
		if host := normalizeHostValue(req.URL.Host); host != "" {
			return host
		}
		if parsed, err := url.Parse(req.URL.String()); err == nil {
			if host := normalizeHostValue(parsed.Host); host != "" {
				return host
			}
		}
	}
	return ""
}

func normalizedTLSHost(req *http.Request) string {
	if req == nil || req.TLS == nil {
		return ""
	}
	return normalizeHostValue(req.TLS.ServerName)
}

func normalizeHostValue(value string) string {
	host, err := domainpattern.NormalizeHost(value)
	if err != nil {
		return ""
	}
	return host
}

func clientIPFromAddr(remoteAddr string) string {
	addr := strings.TrimSpace(remoteAddr)
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.TrimSpace(host)
	}
	return addr
}
