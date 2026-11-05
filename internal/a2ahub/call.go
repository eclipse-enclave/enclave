// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"enclave/internal/a2a"
)

// Call is host access (interfaces.md section 11): the first stdin line is the
// invocation credential, the rest is the request body. An empty body is a GET,
// anything else a JSON-RPC POST. The response body is copied to out as it
// arrives; ok is true for HTTP 200, which includes JSON-RPC errors.
func Call(ctx context.Context, baseURL, path string, in io.Reader, out io.Writer) (ok bool, err error) {
	u, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || u.Scheme != "" || u.Host != "" {
		return false, fmt.Errorf("path %q must be an absolute hub path", path)
	}
	br := bufio.NewReader(in)
	line, err := br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read credential: %w", err)
	}
	token := strings.TrimRight(line, "\r\n")
	if token == "" {
		return false, errors.New("no credential on the first stdin line")
	}
	body, err := io.ReadAll(br)
	if err != nil {
		return false, fmt.Errorf("read request body: %w", err)
	}
	method := http.MethodGet
	var reqBody io.Reader
	if len(bytes.TrimSpace(body)) > 0 {
		method, reqBody = http.MethodPost, bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, reqBody)
	if err != nil {
		return false, err
	}
	req.Header.Set(a2a.HeaderAuthorization, a2a.BearerPrefix+token)
	a2a.SetProtocolHeaders(req.Header)
	req.Header.Set("Accept", "application/json, "+a2a.SSEContentType)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableCompression: true}}
	resp, err := client.Do(req) // #nosec G704 -- the hub's own loopback listener; the path is checked above.
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return false, fmt.Errorf("copy response: %w", err)
	}
	return resp.StatusCode == http.StatusOK, nil
}
