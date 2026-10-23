// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package selfupdate

import (
	"bufio"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"enclave/internal/buildinfo"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

const (
	// downloadBaseURL is the project's directory on the Eclipse download
	// server; each channel publishes below it (see docs/DEV.md, Releases).
	downloadBaseURL       = "https://download.eclipse.org/enclave"
	checksumsFilename     = "checksums.txt"
	latestFilename        = "latest"
	maxChecksumsBytes     = 1 << 20
	maxLatestBytes        = 4 << 10
	maxBinaryBytes        = 128 << 20
	responseHeaderTimeout = 30 * time.Second
	responseIdleTimeout   = 30 * time.Second
	updateTempPattern     = ".enclave-update-*"
	unknownBuildValue     = "unknown"
	// staleUpdateTempAge exceeds any install's timeout, so a temporary file
	// this old belongs to an install that was killed before cleaning up.
	staleUpdateTempAge = time.Hour
)

// ErrNotWritable reports an executable this user cannot replace; the updater
// does not elevate privileges, so such an install is updated by reinstalling.
var ErrNotWritable = errors.New("not writable")

type Status struct {
	// Channel is the release channel the update comes from.
	Channel string
	// Current names the running build, Latest the channel's newest build.
	Current         string
	Latest          string
	UpdateAvailable bool
}

type Service struct {
	fetcher        fetcher
	sources        map[string]source
	channel        string
	buildChannel   string
	goos           string
	goarch         string
	current        buildinfo.Info
	maxBinaryBytes int64
	executablePath func() (string, error)
	externalAssets func(exePath string) (string, bool)
	writableDir    func(dir string) bool
}

// New returns a Service that updates from channel, or from the channel this
// binary was published on when channel is empty.
func New(channel string) *Service {
	// No Client.Timeout: it would also cap reading the binary, failing every
	// download on a slow link. Callers bound each operation with a context.
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	fetcher := fetcher{client: &http.Client{Transport: transport}, idleTimeout: responseIdleTimeout}
	buildChannel := buildinfo.Channel()
	return &Service{
		fetcher: fetcher,
		sources: map[string]source{
			model.UpdateChannelRolling: newRollingSource(fetcher, downloadBaseURL),
			model.UpdateChannelStable:  newStableSource(fetcher, downloadBaseURL),
		},
		channel:        targetChannel(channel, buildChannel),
		buildChannel:   buildChannel,
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		current:        buildinfo.Read(),
		maxBinaryBytes: maxBinaryBytes,
		executablePath: resolvedExecutablePath,
		externalAssets: config.ExternalAppRoot,
		writableDir:    config.LooksWritableDir,
	}
}

func targetChannel(configured string, build string) string {
	if configured != "" {
		return configured
	}
	return build
}

// Supported returns why the running executable must not replace itself, or
// nil when it may. Only standalone release binaries qualify: packages and
// source builds carry no channel stamp and have their own upgrade paths, and
// a binary that runs against an asset tree beside it or in ENCLAVE_HOME would
// keep using those old assets after the swap.
func (s *Service) Supported() error {
	if _, err := artifactName(s.goos, s.goarch); err != nil {
		return err
	}
	if _, ok := s.sources[s.buildChannel]; !ok {
		return fmt.Errorf("self-update only replaces standalone release binaries, but this is a package or source build (version %q); upgrade it with the package manager or by rebuilding", s.current.Version)
	}
	if _, ok := s.sources[s.channel]; !ok {
		return fmt.Errorf("unknown update channel %q", s.channel)
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

// Writable returns an error wrapping ErrNotWritable, with the command that
// reinstalls the build latest identifies by hand, when this user cannot
// replace the running executable. It writes nothing, so callers can ask
// before offering an update.
func (s *Service) Writable(latest string) error {
	target, err := s.executablePath()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	if !s.writableDir(filepath.Dir(target)) {
		return s.notWritable(target, latest)
	}
	return nil
}

// notWritable spells out the manual reinstall, verification included, so it
// is no less safe than the update it replaces.
func (s *Service) notWritable(target string, latest string) error {
	artifact, err := artifactName(s.goos, s.goarch)
	if err != nil {
		return err
	}
	src := s.sources[s.channel]
	return fmt.Errorf("enclave cannot replace %s: its location is %w for this user and the updater does not elevate privileges; download %s and %s into one directory and reinstall from there with: %s",
		target, ErrNotWritable, src.assetURL(latest, artifact), src.assetURL(latest, checksumsFilename), s.reinstallCommand(artifact, target))
}

func (s *Service) reinstallCommand(artifact string, target string) string {
	install := "sudo install " + artifact + " " + shellArg(target)
	if s.goos == "darwin" {
		// xattr -c drops the quarantine attribute of a browser download and,
		// unlike xattr -d, succeeds when there is none.
		return "shasum -a 256 --check --ignore-missing " + checksumsFilename + " && xattr -c " + artifact + " && " + install
	}
	return "sha256sum --check --ignore-missing " + checksumsFilename + " && " + install
}

// shellArg quotes path for a command line unless it is a plain word.
func shellArg(path string) string {
	plain := path != "" && !strings.ContainsFunc(path, func(r rune) bool {
		return !strings.ContainsRune("/._-+", r) && (r > unicode.MaxASCII || !unicode.IsLetter(r) && !unicode.IsDigit(r))
	})
	if plain {
		return path
	}
	return util.ShellQuote(path)
}

// Check compares the running build with the newest one on the channel. A
// binary from another channel is never current, so switching channels
// installs that channel's build even when it is older.
func (s *Service) Check(ctx context.Context) (Status, error) {
	if err := s.Supported(); err != nil {
		return Status{}, err
	}
	src := s.sources[s.channel]
	latest, err := src.latest(ctx)
	if err != nil {
		return Status{}, err
	}
	current := s.buildChannel == s.channel && src.upToDate(s.current, latest)
	return Status{
		Channel:         s.channel,
		Current:         s.sources[s.buildChannel].label(s.current),
		Latest:          latest,
		UpdateAvailable: !current,
	}, nil
}

// Install replaces the running executable with the channel build that latest
// identifies, as reported by Check. It returns the executable's path and
// whether it replaced it: an executable that already is that build, for
// example because another update installed it while this one waited for the
// lock, is left alone.
func (s *Service) Install(ctx context.Context, latest string) (string, bool, error) {
	if err := s.Supported(); err != nil {
		return "", false, err
	}
	artifact, err := artifactName(s.goos, s.goarch)
	if err != nil {
		return "", false, err
	}
	target, err := s.executablePath()
	if err != nil {
		return "", false, fmt.Errorf("resolve current executable: %w", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", false, fmt.Errorf("inspect current executable %s: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("current executable is not a regular file: %s", target)
	}
	removeStaleUpdates(filepath.Dir(target), time.Now())

	// Created before any request, so an unwritable directory fails offline.
	tmp, err := os.CreateTemp(filepath.Dir(target), updateTempPattern)
	if errors.Is(err, fs.ErrPermission) {
		return "", false, s.notWritable(target, latest)
	}
	if err != nil {
		return "", false, fmt.Errorf("create update beside %s: %w", target, err)
	}
	tmpPath := tmp.Name()
	installed := false
	defer func() {
		_ = tmp.Close()
		if !installed {
			_ = os.Remove(tmpPath)
		}
	}()

	src := s.sources[s.channel]
	if err := confirmLatest(ctx, src, s.channel, latest); err != nil {
		return "", false, err
	}
	expected, err := s.expectedChecksum(ctx, src, latest, artifact)
	if err != nil {
		return "", false, err
	}
	if current, err := fileSHA256(target); err == nil && current == expected {
		return target, false, nil
	}

	response, err := s.fetcher.get(ctx, src.assetURL(latest, artifact))
	if err != nil {
		return "", false, fmt.Errorf("download %s: %w", artifact, err)
	}
	defer func() { _ = response.Body.Close() }()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(response.Body, s.maxBinaryBytes+1))
	if err != nil {
		return "", false, fmt.Errorf("download %s: %w", artifact, err)
	}
	if written > s.maxBinaryBytes {
		return "", false, fmt.Errorf("download %s exceeds %d bytes", artifact, s.maxBinaryBytes)
	}
	// A publish swaps in a build's whole directory before naming it latest
	// and only moves forward, so a channel naming latest before and after the
	// download means the checksums and the binary both came from that build.
	if err := confirmLatest(ctx, src, s.channel, latest); err != nil {
		return "", false, err
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != expected {
		return "", false, fmt.Errorf("checksum mismatch for %s: got %s, want %s", artifact, actual, expected)
	}

	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return "", false, fmt.Errorf("set update permissions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", false, fmt.Errorf("sync update: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", false, fmt.Errorf("close update: %w", err)
	}
	err = os.Rename(tmpPath, target) // #nosec G703 -- target is the running executable and tmpPath is its sibling.
	if errors.Is(err, fs.ErrPermission) {
		// A sticky directory such as /tmp passes Writable but refuses to
		// replace another user's file.
		return "", false, s.notWritable(target, latest)
	}
	if err != nil {
		return "", false, fmt.Errorf("replace %s: %w", target, err)
	}
	installed = true
	if err := util.SyncDir(filepath.Dir(target)); err != nil {
		return "", false, fmt.Errorf("sync executable directory: %w", err)
	}
	return target, true, nil
}

func confirmLatest(ctx context.Context, src source, channel string, latest string) error {
	now, err := src.latest(ctx)
	if err != nil {
		return err
	}
	if now != latest {
		return fmt.Errorf("%s release moved from %s to %s; run the update again", channel, ShortBuild(latest), ShortBuild(now))
	}
	return nil
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

func (s *Service) expectedChecksum(ctx context.Context, src source, latest string, artifact string) (string, error) {
	response, err := s.fetcher.get(ctx, src.assetURL(latest, checksumsFilename))
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

// compareVersions orders release versions as semantic versions (1.2.0 <
// 1.10.0, 1.3.0-rc.1 < 1.3.0). A current version that is not a release
// version, such as unknown or git describe output, sorts below every release
// so that it is offered the update.
func compareVersions(current string, latest string) int {
	current = strings.TrimPrefix(strings.TrimSpace(current), "v")
	if !releaseVersionPattern.MatchString(current) {
		return -1
	}
	currentCore, currentPre, _ := strings.Cut(current, "-")
	latestCore, latestPre, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(latest), "v"), "-")
	if c := compareIdentifiers(strings.Split(currentCore, "."), strings.Split(latestCore, ".")); c != 0 {
		return c
	}
	switch {
	case currentPre == "" && latestPre == "":
		return 0
	case currentPre == "":
		return 1
	case latestPre == "":
		return -1
	}
	return compareIdentifiers(strings.Split(currentPre, "."), strings.Split(latestPre, "."))
}

// compareIdentifiers follows semantic versioning: numeric identifiers compare
// as numbers and sort below alphanumeric ones, and a shorter list of
// otherwise equal identifiers sorts first.
func compareIdentifiers(a []string, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		aNumeric, bNumeric := isNumeric(a[i]), isNumeric(b[i])
		var c int
		switch {
		case aNumeric && bNumeric:
			c = compareNumbers(a[i], b[i])
		case aNumeric:
			c = -1
		case bNumeric:
			c = 1
		default:
			c = strings.Compare(a[i], b[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

func isNumeric(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
}

// compareNumbers orders decimal strings of any length by magnitude.
func compareNumbers(a string, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if c := cmp.Compare(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// ShortBuild abbreviates a commit identifying a build to the seven characters
// `enclave version` shows; versions are kept.
func ShortBuild(build string) string {
	if build == "" {
		return "unknown"
	}
	base, dirty := strings.CutSuffix(build, "-dirty")
	if len(base) > 7 && validHex(base) {
		base = base[:7]
	}
	if dirty {
		return base + "-dirty"
	}
	return base
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- path is the running executable.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func validHex(value string) bool {
	return value != "" && !strings.ContainsFunc(value, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F')
	})
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
