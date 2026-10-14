// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"enclave/internal/buildinfo"
)

// ErrChannelUnavailable reports a release channel this binary cannot fetch
// from yet.
var ErrChannelUnavailable = errors.New("no release source")

// source is where one release channel publishes its standalone binaries,
// each next to a checksums.txt covering them.
type source interface {
	// latest identifies the channel's newest build, e.g. by commit. Callers
	// only compare identifiers for equality and display them.
	latest(ctx context.Context) (string, error)
	// assetURL is the download URL of a file published with that build.
	assetURL(name string) string
	// isCurrent reports whether current, a build published on this channel,
	// is the build latest identifies.
	isCurrent(current buildinfo.Info, latest string) bool
	// label names current, a build published on this channel, for messages.
	label(current buildinfo.Info) string
}

// rollingSource is the GitHub rolling release, rebuilt from every push to
// main and identified by the commit its tag points at.
type rollingSource struct {
	fetcher   fetcher
	refURL    string
	assetsURL string
}

func (r rollingSource) latest(ctx context.Context) (string, error) {
	response, err := r.fetcher.get(ctx, r.refURL)
	if err != nil {
		return "", fmt.Errorf("resolve rolling release: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var ref struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxRefResponseBytes)).Decode(&ref); err != nil {
		return "", fmt.Errorf("decode rolling release reference: %w", err)
	}
	sha := strings.ToLower(strings.TrimSpace(ref.Object.SHA))
	if ref.Object.Type != "commit" || len(sha) < 7 || !validHex(sha) {
		return "", fmt.Errorf("rolling release reference did not identify a commit")
	}
	return sha, nil
}

func (r rollingSource) assetURL(name string) string {
	return r.assetsURL + "/" + name
}

func (r rollingSource) isCurrent(current buildinfo.Info, latest string) bool {
	return commitsEqual(current.Commit, latest)
}

func (r rollingSource) label(current buildinfo.Info) string {
	return strings.TrimSpace(current.Commit)
}

// pendingSource stands in for a channel whose download location is not
// settled. Stable releases are expected on the Eclipse Foundation download
// server; once that contract exists it gets its own source.
type pendingSource struct {
	channel string
}

func (p pendingSource) latest(context.Context) (string, error) {
	return "", fmt.Errorf("%w for the %s channel yet; set \"update_channel\": \"rolling\" or update manually", ErrChannelUnavailable, p.channel)
}

func (p pendingSource) assetURL(string) string { return "" }

func (p pendingSource) isCurrent(buildinfo.Info, string) bool { return false }

func (p pendingSource) label(current buildinfo.Info) string {
	return strings.TrimSpace(current.Version)
}

type fetcher struct {
	client *http.Client
	// idleTimeout fails a response body that delivers no data for that long;
	// the transport's ResponseHeaderTimeout ends once the body starts.
	idleTimeout time.Duration
}

func (f fetcher) get(ctx context.Context, url string) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "eclipse-enclave-self-update")
	response, err := f.client.Do(request) // #nosec G704 -- URLs come from fixed release sources; tests substitute a local server.
	if err != nil {
		cancel(nil)
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		err := responseError(url, response)
		_ = response.Body.Close()
		cancel(nil)
		return nil, err
	}
	response.Body = newIdleBody(ctx, cancel, response.Body, f.idleTimeout)
	return response, nil
}

// responseError names GitHub's rate limit, which it reports as a bare 403 or
// 429, and otherwise passes on the message of a GitHub error body.
func responseError(url string, response *http.Response) error {
	limited := response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests
	if limited && response.Header.Get("X-RateLimit-Remaining") == "0" {
		reset := ""
		if seconds, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			reset = "; it resets at " + time.Unix(seconds, 0).Format("15:04")
		}
		return fmt.Errorf("%s returned %s: GitHub API rate limit exceeded%s", url, response.Status, reset)
	}
	var body struct {
		Message string `json:"message"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, maxErrorBodyBytes)).Decode(&body) == nil {
		if message := printableMessage(body.Message); message != "" {
			return fmt.Errorf("%s returned %s: %s", url, response.Status, message)
		}
	}
	return fmt.Errorf("%s returned %s", url, response.Status)
}

// printableMessage flattens a server-supplied message to one bounded line
// without control characters a terminal would interpret.
func printableMessage(message string) string {
	message = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)), " ")
	if runes := []rune(message); len(runes) > maxErrorMessageRunes {
		message = string(runes[:maxErrorMessageRunes]) + "…"
	}
	return message
}

var errBodyIdle = errors.New("response body idle")

// idleBody cancels its request when timeout passes without data, so a stalled
// download fails instead of blocking until the operation's deadline.
type idleBody struct {
	io.ReadCloser
	ctx     context.Context
	cancel  context.CancelCauseFunc
	timer   *time.Timer
	timeout time.Duration
}

func newIdleBody(ctx context.Context, cancel context.CancelCauseFunc, body io.ReadCloser, timeout time.Duration) *idleBody {
	b := &idleBody{ReadCloser: body, ctx: ctx, cancel: cancel, timeout: timeout}
	if timeout > 0 {
		b.timer = time.AfterFunc(timeout, func() { cancel(errBodyIdle) })
	}
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && b.timer != nil {
		b.timer.Reset(b.timeout)
	}
	if err != nil && errors.Is(context.Cause(b.ctx), errBodyIdle) {
		err = fmt.Errorf("no data received for %s", b.timeout)
	}
	return n, err
}

func (b *idleBody) Close() error {
	if b.timer != nil {
		b.timer.Stop()
	}
	err := b.ReadCloser.Close()
	b.cancel(nil)
	return err
}
