// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"enclave/internal/buildinfo"
	"enclave/internal/config"
	"enclave/internal/util"
)

// rollingVersion is the version the rolling release stamps into its
// standalone binaries; packages and source builds carry other versions.
const rollingVersion = "rolling"

const (
	rollingRefURL         = "https://api.github.com/repos/eclipse-enclave/enclave/git/ref/tags/rolling"
	rollingAssetsURL      = "https://github.com/eclipse-enclave/enclave/releases/download/rolling"
	checksumsFilename     = "checksums.txt"
	maxChecksumsBytes     = 1 << 20
	maxRefResponseBytes   = 64 << 10
	maxBinaryBytes        = 128 << 20
	responseHeaderTimeout = 30 * time.Second
	updateTempPattern     = ".enclave-update-*"
	unknownBuildValue     = "unknown"
	// staleUpdateTempAge exceeds any install's timeout, so a temporary file
	// this old belongs to an install that was killed before cleaning up.
	staleUpdateTempAge = time.Hour
)

type Status struct {
	CurrentCommit   string
	LatestCommit    string
	UpdateAvailable bool
}

type Service struct {
	client         *http.Client
	refURL         string
	assetsURL      string
	goos           string
	goarch         string
	current        buildinfo.Info
	maxBinaryBytes int64
	executablePath func() (string, error)
	externalAssets func(exePath string) (string, bool)
}

func New() *Service {
	// No Client.Timeout: it would also cap reading the binary, failing every
	// download on a slow link. Callers bound each operation with a context.
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return &Service{
		client:         &http.Client{Transport: transport},
		refURL:         rollingRefURL,
		assetsURL:      rollingAssetsURL,
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		current:        buildinfo.Read(),
		maxBinaryBytes: maxBinaryBytes,
		executablePath: resolvedExecutablePath,
		externalAssets: config.ExternalAppRoot,
	}
}

// Supported returns why the running executable must not replace itself, or
// nil when it may. Only standalone rolling binaries qualify: packages and
// source builds have their own upgrade paths, and a binary that runs against
// an asset tree beside it or in ENCLAVE_HOME would keep using those old assets
// after the swap.
func (s *Service) Supported() error {
	if _, err := artifactName(s.goos, s.goarch); err != nil {
		return err
	}
	if s.current.Version != rollingVersion {
		return fmt.Errorf("self-update only replaces standalone rolling binaries, but this build is version %q; upgrade package installs with the package manager and source builds by rebuilding", s.current.Version)
	}
	target, err := s.executablePath()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	if root, ok := s.externalAssets(target); ok {
		return fmt.Errorf("%s runs against the assets in %s, which self-update cannot replace; update it the way it was installed", target, root)
	}
	return nil
}

func (s *Service) Check(ctx context.Context) (Status, error) {
	latest, err := s.latestCommit(ctx)
	if err != nil {
		return Status{}, err
	}
	currentCommit := strings.TrimSpace(s.current.Commit)
	return Status{
		CurrentCommit:   currentCommit,
		LatestCommit:    latest,
		UpdateAvailable: !commitsEqual(currentCommit, latest),
	}, nil
}

// Install replaces the running executable with the rolling binary built from
// expectedCommit, the commit Check reported.
func (s *Service) Install(ctx context.Context, expectedCommit string) (string, error) {
	if err := s.Supported(); err != nil {
		return "", err
	}
	artifact, err := artifactName(s.goos, s.goarch)
	if err != nil {
		return "", err
	}
	expected, err := s.expectedChecksum(ctx, artifact)
	if err != nil {
		return "", err
	}

	target, err := s.executablePath()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("inspect current executable %s: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("current executable is not a regular file: %s", target)
	}
	removeStaleUpdates(filepath.Dir(target), time.Now())

	response, err := s.get(ctx, s.assetsURL+"/"+artifact)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", artifact, err)
	}
	defer func() { _ = response.Body.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(target), updateTempPattern)
	if err != nil {
		return "", fmt.Errorf("create update beside %s: %w", target, err)
	}
	tmpPath := tmp.Name()
	installed := false
	defer func() {
		_ = tmp.Close()
		if !installed {
			_ = os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(response.Body, s.maxBinaryBytes+1))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", artifact, err)
	}
	if written > s.maxBinaryBytes {
		return "", fmt.Errorf("download %s exceeds %d bytes", artifact, s.maxBinaryBytes)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != expected {
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", artifact, actual, expected)
	}
	// Each publish replaces the whole rolling release and only moves the tag
	// forward, so a tag that still names expectedCommit means the checksums
	// and the binary both came from that commit's release.
	latest, err := s.latestCommit(ctx)
	if err != nil {
		return "", err
	}
	if !commitsEqual(expectedCommit, latest) {
		return "", fmt.Errorf("rolling release moved from %.8s to %.8s during the download; run the update again", expectedCommit, latest)
	}

	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("set update permissions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync update: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close update: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil { // #nosec G703 -- target is the running executable and tmpPath is its sibling.
		return "", fmt.Errorf("replace %s: %w", target, err)
	}
	installed = true
	if err := util.SyncDir(filepath.Dir(target)); err != nil {
		return "", fmt.Errorf("sync executable directory: %w", err)
	}
	return target, nil
}

// removeStaleUpdates deletes temporary downloads that an interrupted install
// left beside the executable; a signal or crash skips Install's own cleanup.
func removeStaleUpdates(dir string, now time.Time) {
	matches, _ := filepath.Glob(filepath.Join(dir, updateTempPattern))
	for _, path := range matches {
		info, err := os.Lstat(path)
		if err == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) > staleUpdateTempAge {
			_ = os.Remove(path)
		}
	}
}

func (s *Service) latestCommit(ctx context.Context) (string, error) {
	response, err := s.get(ctx, s.refURL)
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

func (s *Service) expectedChecksum(ctx context.Context, artifact string) (string, error) {
	response, err := s.get(ctx, s.assetsURL+"/"+checksumsFilename)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", checksumsFilename, err)
	}
	defer func() { _ = response.Body.Close() }()

	scanner := bufio.NewScanner(io.LimitReader(response.Body, maxChecksumsBytes))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != artifact {
			continue
		}
		digest := strings.ToLower(fields[0])
		if len(digest) != sha256.Size*2 || !validHex(digest) {
			return "", fmt.Errorf("invalid checksum for %s", artifact)
		}
		return digest, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", checksumsFilename, err)
	}
	return "", fmt.Errorf("%s has no checksum for %s", checksumsFilename, artifact)
}

func (s *Service) get(ctx context.Context, url string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "eclipse-enclave-self-update")
	response, err := s.client.Do(request) // #nosec G704 -- URLs are fixed GitHub release endpoints; tests substitute a local server.
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%s returned %s", url, response.Status)
	}
	return response, nil
}

func artifactName(goos string, goarch string) (string, error) {
	if (goos != "linux" && goos != "darwin") || (goarch != "amd64" && goarch != "arm64") {
		return "", fmt.Errorf("self-update is not supported on %s/%s", goos, goarch)
	}
	return "enclave-" + goos + "-" + goarch, nil
}

func commitsEqual(current string, latest string) bool {
	current = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(current), "-dirty"))
	latest = strings.ToLower(strings.TrimSpace(latest))
	if current == "" || current == unknownBuildValue {
		return false
	}
	return strings.HasPrefix(current, latest) || strings.HasPrefix(latest, current)
}

func validHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func resolvedExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	return path, nil
}
