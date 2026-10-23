// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"enclave/internal/buildinfo"
)

// source is where one release channel publishes its standalone binaries,
// each next to a checksums.txt covering them.
type source interface {
	// latest identifies the channel's newest build, e.g. by commit. Callers
	// only compare identifiers for equality and display them.
	latest(ctx context.Context) (string, error)
	// assetURL is the download URL of a file published with the build that
	// latest identifies.
	assetURL(latest string, name string) string
	// upToDate reports whether current, a build published on this channel,
	// needs no update to the build latest identifies.
	upToDate(current buildinfo.Info, latest string) bool
	// label names current, a build published on this channel, for messages.
	label(current buildinfo.Info) string
}

// downloadSource is a channel directory on the Eclipse download server. Its
// latest file names the newest build: the commit of the rolling channel,
// whose artifacts are swapped in place with every push to main, or the
// version of the stable channel, which keeps a directory per release.
type downloadSource struct {
	fetcher fetcher
	// channelURL is the channel directory, without a trailing slash.
	channelURL string
	stable     bool
}

func newRollingSource(fetcher fetcher, baseURL string) downloadSource {
	return downloadSource{fetcher: fetcher, channelURL: baseURL + "/rolling"}
}

func newStableSource(fetcher fetcher, baseURL string) downloadSource {
	return downloadSource{fetcher: fetcher, channelURL: baseURL + "/releases", stable: true}
}

// releaseVersionPattern accepts the versions release tags produce: 1.0.0, or
// 1.1.0-rc.1 with alpha, beta, or rc, and no leading zeros (release.yml and
// scripts/version.sh enforce the same grammar). Anything else, HTML error
// pages included, is rejected before it can form a URL.
var releaseVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.(0|[1-9][0-9]*))?$`)

func (d downloadSource) latest(ctx context.Context) (string, error) {
	url := d.channelURL + "/" + latestFilename
	response, err := d.fetcher.get(ctx, url)
	if err != nil {
		return "", fmt.Errorf("resolve latest %s build: %w", d.name(), err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxLatestBytes))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", url, err)
	}
	latest := strings.TrimSpace(string(body))
	if d.stable {
		if !releaseVersionPattern.MatchString(latest) {
			return "", fmt.Errorf("%s did not name a release version", url)
		}
		return latest, nil
	}
	latest = strings.ToLower(latest)
	if (len(latest) != 40 && len(latest) != 64) || !validHex(latest) {
		return "", fmt.Errorf("%s did not name a commit", url)
	}
	return latest, nil
}

func (d downloadSource) assetURL(latest string, name string) string {
	if d.stable {
		return d.channelURL + "/" + latest + "/" + name
	}
	return d.channelURL + "/" + name
}

// upToDate: a rolling build is current when it is the published commit. A
// stable build is current when it is the published release or a newer one,
// such as a pre-release the pointer never names or a release installed by
// hand before the pointer moved; the channel never downgrades on its own.
func (d downloadSource) upToDate(current buildinfo.Info, latest string) bool {
	if d.stable {
		return compareVersions(current.Version, latest) >= 0
	}
	return commitsEqual(current.Commit, latest)
}

func (d downloadSource) label(current buildinfo.Info) string {
	if d.stable {
		return strings.TrimSpace(current.Version)
	}
	return strings.TrimSpace(current.Commit)
}

func (d downloadSource) name() string {
	if d.stable {
		return "stable"
	}
	return "rolling"
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
	request.Header.Set("User-Agent", "eclipse-enclave-self-update")
	response, err := f.client.Do(request) // #nosec G704 -- URLs come from fixed release sources; tests substitute a local server.
	if err != nil {
		cancel(nil)
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		cancel(nil)
		return nil, fmt.Errorf("%s returned %s", url, response.Status)
	}
	response.Body = newIdleBody(ctx, cancel, response.Body, f.idleTimeout)
	return response, nil
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
