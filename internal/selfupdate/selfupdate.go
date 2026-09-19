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
	"enclave/internal/util"
)

const (
	rollingRefURL      = "https://api.github.com/repos/eclipse-enclave/enclave/git/ref/tags/rolling"
	rollingAssetsURL   = "https://github.com/eclipse-enclave/enclave/releases/download/rolling"
	checksumsFilename  = "checksums.txt"
	maxChecksumsBytes  = 1 << 20
	maxBinaryBytes     = 128 << 20
	defaultHTTPTimeout = 30 * time.Second
	unknownBuildValue  = "unknown"
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
	executablePath func() (string, error)
}

func New() *Service {
	return &Service{
		client:         &http.Client{Timeout: defaultHTTPTimeout},
		refURL:         rollingRefURL,
		assetsURL:      rollingAssetsURL,
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		executablePath: resolvedExecutablePath,
	}
}

func (s *Service) Check(ctx context.Context, current buildinfo.Info) (Status, error) {
	latest, err := s.latestCommit(ctx)
	if err != nil {
		return Status{}, err
	}
	currentCommit := strings.TrimSpace(current.Commit)
	return Status{
		CurrentCommit:   currentCommit,
		LatestCommit:    latest,
		UpdateAvailable: !commitsEqual(currentCommit, latest),
	}, nil
}

func (s *Service) Install(ctx context.Context) (string, error) {
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

	response, err := s.get(ctx, s.assetsURL+"/"+artifact)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", artifact, err)
	}
	defer func() { _ = response.Body.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(target), ".enclave-update-*")
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
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(response.Body, maxBinaryBytes+1))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", artifact, err)
	}
	if written > maxBinaryBytes {
		return "", fmt.Errorf("download %s exceeds %d bytes", artifact, maxBinaryBytes)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != expected {
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", artifact, actual, expected)
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
	if err := json.NewDecoder(io.LimitReader(response.Body, maxChecksumsBytes)).Decode(&ref); err != nil {
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
