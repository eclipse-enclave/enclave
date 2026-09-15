// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"enclave/internal/util"
)

// defaultHost is assumed for bare owner/repo shorthands.
const defaultHost = "https://github.com"

// source is a parsed install source: which repository to fetch, at which ref,
// and which directory inside it to install. Ref and Subpath may be empty.
type source struct {
	Raw        string
	RemoteURL  string
	Ref        string
	RefFromURL bool
	Subpath    string
}

// parseSource accepts owner/repo shorthands (optionally followed by a
// subpath), https/ssh/git repository URLs, forge "tree" URLs, scp-style
// remotes, and local filesystem paths to git repositories.
func parseSource(raw string) (source, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return source{}, fmt.Errorf("source is empty")
	}
	if strings.HasPrefix(trimmed, "-") {
		return source{}, fmt.Errorf("invalid source %q", trimmed)
	}
	src := source{Raw: trimmed}

	switch {
	case isLocalPath(trimmed):
		localPath := trimmed
		if strings.HasPrefix(trimmed, "~") {
			home, err := os.UserHomeDir()
			if err != nil {
				return source{}, err
			}
			localPath = util.ExpandTilde(trimmed, home)
		}
		abs, err := filepath.Abs(localPath)
		if err != nil {
			return source{}, err
		}
		src.RemoteURL = abs
		return src, nil
	case isSCPLike(trimmed):
		// A trailing ".git" is stripped from every repository URL form; an
		// scp-style remote is no exception even though it is otherwise passed
		// through untouched (there is no reliable way to split a subpath out
		// of it).
		src.RemoteURL = strings.TrimSuffix(trimmed, ".git")
		return src, nil
	case hasScheme(trimmed):
		return parseURLSource(src, trimmed)
	default:
		return parseShorthand(src, trimmed)
	}
}

func isLocalPath(raw string) bool {
	return strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") ||
		strings.HasPrefix(raw, "/") || raw == "." || strings.HasPrefix(raw, "~")
}

// isSCPLike matches git's scp-style syntax (user@host:path) without mistaking
// a URL scheme for it.
func isSCPLike(raw string) bool {
	if hasScheme(raw) {
		return false
	}
	at := strings.Index(raw, "@")
	colon := strings.Index(raw, ":")
	return at > 0 && colon > at
}

func hasScheme(raw string) bool {
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://", "file://"} {
		if strings.HasPrefix(raw, scheme) {
			return true
		}
	}
	return false
}

func parseURLSource(src source, raw string) (source, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return source{}, fmt.Errorf("invalid source URL %q: %w", raw, err)
	}
	if parsed.Host == "" {
		return source{}, fmt.Errorf("source URL %q has no host", raw)
	}
	segments, err := splitSegments(strings.Trim(parsed.Path, "/"))
	if err != nil {
		return source{}, err
	}
	if len(segments) < 2 {
		return source{}, fmt.Errorf("source URL %q does not name a repository", raw)
	}

	repoEnd, ref, rest, err := splitTreeURL(segments)
	if err != nil {
		return source{}, err
	}
	repo := append([]string{}, segments[:repoEnd]...)
	repo[len(repo)-1] = strings.TrimSuffix(repo[len(repo)-1], ".git")

	base := &url.URL{Scheme: parsed.Scheme, User: parsed.User, Host: parsed.Host, Path: "/" + strings.Join(repo, "/")}
	src.RemoteURL = base.String()
	src.Ref = ref
	src.RefFromURL = ref != ""
	if len(rest) > 0 {
		subpath, subErr := normalizeSubpath(strings.Join(rest, "/"))
		if subErr != nil {
			return source{}, subErr
		}
		src.Subpath = subpath
	}
	return src, nil
}

// splitTreeURL locates a forge tree marker ("/tree/<ref>/..." or
// "/-/tree/<ref>/...") and reports where the repository path ends, the ref it
// names, and the remaining path segments.
func splitTreeURL(segments []string) (repoEnd int, ref string, rest []string, err error) {
	for i, segment := range segments {
		switch segment {
		case "blob":
			return 0, "", nil, fmt.Errorf("a blob URL points at a file; link the extension directory instead")
		case "tree":
			if i+1 >= len(segments) {
				return 0, "", nil, fmt.Errorf("tree URL is missing a ref")
			}
			end := i
			if i > 0 && segments[i-1] == "-" {
				end = i - 1
			}
			if end < 2 {
				return 0, "", nil, fmt.Errorf("tree URL does not name a repository")
			}
			return end, segments[i+1], segments[i+2:], nil
		}
	}
	return len(segments), "", nil, nil
}

func parseShorthand(src source, raw string) (source, error) {
	segments, err := splitSegments(raw)
	if err != nil {
		return source{}, err
	}
	if len(segments) < 2 {
		return source{}, fmt.Errorf("source %q must be owner/repo (optionally followed by a path)", raw)
	}
	owner := segments[0]
	repo := strings.TrimSuffix(segments[1], ".git")
	src.RemoteURL = fmt.Sprintf("%s/%s/%s", defaultHost, owner, repo)
	if len(segments) > 2 {
		subpath, subErr := normalizeSubpath(strings.Join(segments[2:], "/"))
		if subErr != nil {
			return source{}, subErr
		}
		src.Subpath = subpath
	}
	return src, nil
}

func splitSegments(raw string) ([]string, error) {
	if raw == "" {
		return nil, fmt.Errorf("source path is empty")
	}
	segments := strings.Split(raw, "/")
	for _, segment := range segments {
		if segment == "" {
			return nil, fmt.Errorf("source %q has an empty path segment", raw)
		}
		if segment == "." || segment == ".." {
			return nil, fmt.Errorf("source %q must not contain %q", raw, segment)
		}
	}
	return segments, nil
}

// normalizeSubpath validates a repository-relative directory path and returns
// it slash-separated with no leading or trailing separator.
func normalizeSubpath(raw string) (string, error) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if trimmed == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("path %q must be relative to the repository root", raw)
	}
	cleaned := path.Clean(trimmed)
	if _, err := splitSegments(strings.Trim(cleaned, "/")); err != nil {
		return "", err
	}
	return strings.Trim(cleaned, "/"), nil
}

// WithRef applies an explicit --ref. A ref that contradicts one already taken
// from a tree URL is an error rather than a silent precedence rule.
func (s source) WithRef(ref string) (source, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return s, nil
	}
	if s.RefFromURL && s.Ref != trimmed {
		return source{}, fmt.Errorf("source URL names ref %q but --ref says %q", s.Ref, trimmed)
	}
	s.Ref = trimmed
	return s, nil
}

// WithPath applies an explicit --path. It is rejected when the source string
// already carried a subpath, so the two can never disagree.
func (s source) WithPath(p string) (source, error) {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return s, nil
	}
	if s.Subpath != "" {
		return source{}, fmt.Errorf("source already names path %q; drop --path", s.Subpath)
	}
	subpath, err := normalizeSubpath(trimmed)
	if err != nil {
		return source{}, err
	}
	s.Subpath = subpath
	return s, nil
}

// Display is the redacted, human-facing rendering of a source.
func (s source) Display() string {
	display := RedactRemote(s.RemoteURL)
	if s.Subpath != "" {
		display += "/" + s.Subpath
	}
	if s.Ref != "" {
		display += "@" + s.Ref
	}
	return display
}

// httpUserinfoPattern matches the userinfo component of an http(s) URL in free
// text, which is how git's stderr embeds the remote it failed on.
var httpUserinfoPattern = regexp.MustCompile(`(https?://)[^/@\s]*@`)

// RedactRemote strips credentials from an http(s) remote URL, or from any
// text containing one, so it is safe to print or persist. ssh, git,
// scp-style, and local-path remotes are returned unchanged: their userinfo
// (if any) is a required login, not a secret.
func RedactRemote(remote string) string {
	if parsed, err := url.Parse(remote); err == nil && parsed.User != nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") {
		parsed.User = nil
		return parsed.String()
	}
	return httpUserinfoPattern.ReplaceAllString(remote, "$1")
}
