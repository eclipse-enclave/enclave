// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// ErrGitMissing reports that the host has no git binary.
var ErrGitMissing = errors.New("git was not found on PATH; install git to add or update extensions")

// fullSHAPattern matches a full object name in either hash: 40 hex digits for
// SHA-1, 64 for a SHA-256 repository.
var fullSHAPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// RemoteRef is a resolved remote reference.
type RemoteRef struct {
	Commit  string
	Ref     string
	RefType string
}

// Repo is a fetched repository at one commit. Files lists the whole tree
// without any file content having been downloaded; Materialize downloads the
// content of the named directories only.
type Repo interface {
	Commit() string
	Ref() string
	RefType() string
	Files() []string
	Materialize(ctx context.Context, dirs []string) error
	Dir() string
	Close()
}

// Fetcher retrieves repositories.
type Fetcher interface {
	ResolveRef(ctx context.Context, remote string, ref string) (RemoteRef, error)
	Open(ctx context.Context, remote string, ref string) (Repo, error)
	// OpenAt fetches a reference that is already resolved, so a caller that
	// had to resolve it first — to decide whether a fetch is needed at all —
	// does not pay for a second round trip.
	OpenAt(ctx context.Context, remote string, resolved RemoteRef) (Repo, error)
}

// fetchKey identifies one remote reference within a single command.
type fetchKey struct {
	remote string
	ref    string
}

// fetchCache serves one resolved reference and one fetched repository per
// (remote, ref) for the duration of one command. Several extensions commonly
// come from a single kit repository at a single ref, and without this each of
// them would resolve and clone that repository again. Failures are not cached:
// each extension reports its own.
type fetchCache struct {
	fetcher Fetcher
	refs    map[fetchKey]RemoteRef
	repos   map[fetchKey]Repo
}

func newFetchCache(fetcher Fetcher) *fetchCache {
	return &fetchCache{
		fetcher: fetcher,
		refs:    map[fetchKey]RemoteRef{},
		repos:   map[fetchKey]Repo{},
	}
}

func (c *fetchCache) resolve(ctx context.Context, remote string, ref string) (RemoteRef, error) {
	key := fetchKey{remote: remote, ref: ref}
	if resolved, ok := c.refs[key]; ok {
		return resolved, nil
	}
	resolved, err := c.fetcher.ResolveRef(ctx, remote, ref)
	if err != nil {
		return RemoteRef{}, err
	}
	c.refs[key] = resolved
	return resolved, nil
}

// open returns the repository for an already-resolved reference, fetching it
// on first use.
func (c *fetchCache) open(ctx context.Context, remote string, resolved RemoteRef) (Repo, error) {
	key := fetchKey{remote: remote, ref: resolved.Ref}
	if repo, ok := c.repos[key]; ok {
		return repo, nil
	}
	repo, err := c.fetcher.OpenAt(ctx, remote, resolved)
	if err != nil {
		return nil, err
	}
	c.repos[key] = repo
	return repo, nil
}

// close discards every repository fetched through this cache. Callers must not
// close a repository themselves: it may still be serving another extension.
func (c *fetchCache) close() {
	for _, repo := range c.repos {
		repo.Close()
	}
	c.repos = map[fetchKey]Repo{}
}

type gitFetcher struct {
	git          string
	allowPrompts bool
}

// NewGitFetcher returns a Fetcher backed by the host's git. Without prompts,
// git may not ask for credentials on the terminal: a private remote then fails
// with git's own message instead of blocking a run whose report is a JSON
// envelope nobody is watching.
func NewGitFetcher(allowPrompts bool) (Fetcher, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return nil, ErrGitMissing
	}
	return &gitFetcher{git: path, allowPrompts: allowPrompts}, nil
}

// ResolveRef resolves ref to a commit with one network round trip and no
// repository data transferred. An empty ref resolves the remote HEAD and
// reports the branch it points at, so an install records a branch to follow
// rather than a moving HEAD; a HEAD that names no branch is pinned to its
// commit instead (see parseSymrefHead). A full SHA needs no network at all.
func (f *gitFetcher) ResolveRef(ctx context.Context, remote string, ref string) (RemoteRef, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		out, err := f.run(ctx, "", "ls-remote", "--symref", remote, "HEAD")
		if err != nil {
			return RemoteRef{}, err
		}
		return parseSymrefHead(out, remote)
	}
	if fullSHAPattern.MatchString(trimmed) {
		return RemoteRef{Commit: trimmed, Ref: trimmed, RefType: RefTypeCommit}, nil
	}
	// The requested ref reaches git as a bare fetch/ls-remote operand later, so
	// it is validated here before any network round trip is made with it.
	if err := validateRefName(trimmed); err != nil {
		return RemoteRef{}, fmt.Errorf("requested ref %q: %w", trimmed, err)
	}
	out, err := f.run(ctx, "", "ls-remote", remote, "refs/heads/"+trimmed, "refs/tags/"+trimmed)
	if err != nil {
		return RemoteRef{}, err
	}
	resolved, ok := parseLsRemote(out, trimmed)
	if !ok {
		return RemoteRef{}, fmt.Errorf("%s has no branch or tag %q", RedactRemote(remote), trimmed)
	}
	return resolved, nil
}

// validateRefName rejects ref names that are unsafe to hand to git as a bare
// command-line operand, or that violate git's own ref-name rules.
func validateRefName(ref string) error {
	if ref == "" {
		return errors.New("ref name is empty")
	}
	if strings.HasPrefix(ref, "-") {
		return errors.New("ref name looks like a command-line option")
	}
	if strings.Contains(ref, "..") {
		return errors.New(`ref name contains ".."`)
	}
	if strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") {
		return errors.New(`ref name has a leading or trailing "/"`)
	}
	if strings.HasSuffix(ref, ".") {
		return errors.New(`ref name ends with "."`)
	}
	if strings.HasSuffix(ref, ".lock") {
		return errors.New(`ref name ends with ".lock"`)
	}
	for _, r := range ref {
		switch {
		case r < 0x20 || r == 0x7f:
			return errors.New("ref name contains a control character")
		case r == ' ':
			return errors.New("ref name contains a space")
		case strings.ContainsRune(`~^:?*[\`, r):
			return fmt.Errorf("ref name contains %q", string(r))
		}
	}
	return nil
}

func parseSymrefHead(out string, remote string) (RemoteRef, error) {
	result := RemoteRef{RefType: RefTypeBranch}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// The symref target has to be HEAD itself, not merely end in it. A
		// local clone advertises refs/remotes/origin/HEAD alongside its own
		// HEAD, and matching on the suffix pinned the install to
		// refs/remotes/origin/<branch>: a ref name no later fetch resolves,
		// and a stale tracking ref rather than the branch that moves.
		if fields[0] == "ref:" && fields[len(fields)-1] == "HEAD" {
			result.Ref = strings.TrimPrefix(fields[1], "refs/heads/")
			continue
		}
		if fields[1] == "HEAD" {
			result.Commit = fields[0]
		}
	}
	if result.Commit == "" {
		return RemoteRef{}, fmt.Errorf("%s has no HEAD", RedactRemote(remote))
	}
	if result.Ref == "" {
		// A remote HEAD that is not symbolic names no branch to follow — a local
		// clone with a detached HEAD, most often. Recording "HEAD" as the ref
		// would leave update looking for a branch or tag of that name forever, so
		// the install is pinned to the commit HEAD is at instead.
		return RemoteRef{Commit: result.Commit, Ref: result.Commit, RefType: RefTypeCommit}, nil
	}
	// The branch name came from the remote's own advertisement and is about to
	// be used as a bare git operand, so it is untrusted regardless of the
	// remote's reputation: a hostile server can name its HEAD branch anything,
	// including a string that git would parse as a command-line option.
	if err := validateRefName(result.Ref); err != nil {
		return RemoteRef{}, fmt.Errorf("%s's HEAD names branch %q, which is unusable: %w", RedactRemote(remote), result.Ref, err)
	}
	return result, nil
}

func parseLsRemote(out string, ref string) (RemoteRef, bool) {
	branch := RemoteRef{}
	tag := RemoteRef{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[1] {
		case "refs/heads/" + ref:
			branch = RemoteRef{Commit: fields[0], Ref: ref, RefType: RefTypeBranch}
		case "refs/tags/" + ref:
			tag = RemoteRef{Commit: fields[0], Ref: ref, RefType: RefTypeTag}
		}
	}
	// A branch wins over a same-named tag, matching git's own precedence.
	if branch.Commit != "" {
		return branch, true
	}
	if tag.Commit != "" {
		return tag, true
	}
	return RemoteRef{}, false
}

// Open resolves ref and fetches it.
func (f *gitFetcher) Open(ctx context.Context, remote string, ref string) (Repo, error) {
	resolved, err := f.ResolveRef(ctx, remote, ref)
	if err != nil {
		return nil, err
	}
	return f.OpenAt(ctx, remote, resolved)
}

// OpenAt performs a blobless shallow fetch: the commit and its trees arrive,
// file content does not. Callers list the tree for free and then Materialize
// only the directories they install.
func (f *gitFetcher) OpenAt(ctx context.Context, remote string, resolved RemoteRef) (Repo, error) {
	dir, err := os.MkdirTemp("", "enclave-extinstall-*")
	if err != nil {
		return nil, err
	}
	repo := &gitRepo{fetcher: f, dir: dir, ref: resolved}
	if err := repo.init(ctx, remote, resolved); err != nil {
		repo.Close()
		return nil, err
	}
	// -z is what makes the listing exact: without it git C-quotes any path
	// holding non-ASCII bytes (core.quotePath defaults to true), and a name with
	// a leading or trailing space would not survive line trimming either.
	files, err := f.run(ctx, dir, "ls-tree", "-r", "--name-only", "-z", repo.checkoutTarget)
	if err != nil {
		repo.Close()
		return nil, err
	}
	repo.files = splitNUL(files)
	return repo, nil
}

type gitRepo struct {
	fetcher  *gitFetcher
	dir      string
	ref      RemoteRef
	files    []string
	blobless bool
	sparse   bool
	// checkoutTarget is the revision ls-tree, rev-parse, and checkout act on.
	// It is FETCH_HEAD after a normal ref fetch, or a raw commit SHA after the
	// pinned-commit fallback, which cannot use FETCH_HEAD at all (see
	// fetchPinnedCommitFromDefaultBranch).
	checkoutTarget string
}

func (r *gitRepo) init(ctx context.Context, remote string, resolved RemoteRef) error {
	if _, err := r.fetcher.run(ctx, r.dir, "init", "-q"); err != nil {
		return err
	}
	if _, err := r.fetcher.run(ctx, r.dir, "remote", "add", "origin", remote); err != nil {
		return err
	}

	fetchRef := resolved.Ref
	if resolved.RefType == RefTypeCommit {
		fetchRef = resolved.Commit
	}
	r.checkoutTarget = "FETCH_HEAD"

	// Preferred path: commit and trees only. A server that rejects the filter
	// outright fails this fetch (non-zero exit); one that merely does not
	// support it (e.g. uploadpack.allowFilter off) instead answers with a
	// full fetch anyway and only warns on stderr. Both mean the filter did
	// not apply, so both are detected before trusting r.blobless.
	_, filterStderr, err := r.fetcher.runCombined(ctx, r.dir, "fetch", "--depth", "1", "--no-tags", "--filter=blob:none", "origin", fetchRef)
	if err == nil && !filterIgnoredByServer(filterStderr) {
		r.blobless = true
		return r.verifyCommit(ctx, resolved)
	}
	if err == nil {
		// The fetch already transferred full objects; nothing more to do.
		return r.verifyCommit(ctx, resolved)
	}
	// Server refuses object filters (uploadpack.allowFilter off): shallow fetch
	// everything at this commit instead.
	_, plainErr := r.fetcher.run(ctx, r.dir, "fetch", "--depth", "1", "--no-tags", "origin", fetchRef)
	if plainErr == nil {
		return r.verifyCommit(ctx, resolved)
	}
	if resolved.RefType != RefTypeCommit {
		return plainErr
	}
	// Server refuses fetch-by-sha too: fall back to the default branch and
	// check the pinned commit out of it directly.
	return r.fetchPinnedCommitFromDefaultBranch(ctx, resolved)
}

// fetchPinnedCommitFromDefaultBranch handles a server that refuses to fetch a
// specific commit SHA directly: it fetches the default branch in full (a
// shallow, depth-limited fetch may not include the pinned commit's history)
// and confirms the pinned commit is actually present.
//
// FETCH_HEAD is a pseudoref that `git update-ref` refuses to write, so
// checkoutTarget carries the commit SHA instead.
func (r *gitRepo) fetchPinnedCommitFromDefaultBranch(ctx context.Context, resolved RemoteRef) error {
	if _, err := r.fetcher.run(ctx, r.dir, "fetch", "--no-tags", "origin"); err != nil {
		return err
	}
	if _, err := r.fetcher.run(ctx, r.dir, "cat-file", "-e", resolved.Commit+"^{commit}"); err != nil {
		return fmt.Errorf("commit %s was not found after fetching the default branch: %w", ShortCommit(resolved.Commit), err)
	}
	r.checkoutTarget = resolved.Commit
	return r.verifyCommit(ctx, resolved)
}

// verifyCommit adopts the commit the fetch landed on when a branch or tag moved
// between ls-remote and fetch, and fails when an explicit commit pin was not
// the commit fetched.
func (r *gitRepo) verifyCommit(ctx context.Context, resolved RemoteRef) error {
	out, err := r.fetcher.run(ctx, r.dir, "rev-parse", r.checkoutTarget)
	if err != nil {
		return err
	}
	fetched := strings.TrimSpace(out)
	if resolved.Commit != "" && fetched != resolved.Commit {
		if resolved.RefType == RefTypeCommit {
			return fmt.Errorf("requested commit %s but fetched %s", ShortCommit(resolved.Commit), ShortCommit(fetched))
		}
		r.ref.Commit = fetched
	}
	if r.ref.Commit == "" {
		r.ref.Commit = fetched
	}
	return nil
}

func (r *gitRepo) Commit() string  { return r.ref.Commit }
func (r *gitRepo) Ref() string     { return r.ref.Ref }
func (r *gitRepo) RefType() string { return r.ref.RefType }
func (r *gitRepo) Files() []string { return r.files }
func (r *gitRepo) Dir() string     { return r.dir }

func (r *gitRepo) Close() {
	if r.dir != "" {
		_ = os.RemoveAll(r.dir)
	}
}

// Materialize checks out only dirs. With a blobless fetch this downloads their
// blobs and nothing else; when sparse checkout is unavailable it falls back to
// a full checkout of the fetched commit. Every directory is validated before
// it reaches git as a bare operand: dirs ultimately comes from an extension
// request, which is untrusted the same way a remote-supplied ref is.
func (r *gitRepo) Materialize(ctx context.Context, dirs []string) error {
	normalized := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		clean, err := normalizeSubpath(dir)
		if err != nil {
			return fmt.Errorf("materialize path %q: %w", dir, err)
		}
		if strings.HasPrefix(clean, "-") {
			return fmt.Errorf("materialize path %q looks like a command-line option, refusing", dir)
		}
		normalized = append(normalized, clean)
	}
	if len(normalized) > 0 {
		if err := r.trySparse(ctx, normalized); err == nil {
			r.sparse = true
			return nil
		}
		// A half-applied sparse attempt leaves sparsity enabled with the default
		// cone, which would reduce the full checkout below to the root files.
		_, _ = r.fetcher.run(ctx, r.dir, "sparse-checkout", "disable")
	}
	_, err := r.fetcher.run(ctx, r.dir, "checkout", "--detach", r.checkoutTarget)
	return err
}

func (r *gitRepo) trySparse(ctx context.Context, dirs []string) error {
	if _, err := r.fetcher.run(ctx, r.dir, "sparse-checkout", "init", "--cone"); err != nil {
		return err
	}
	args := append([]string{"sparse-checkout", "set"}, dirs...)
	if _, err := r.fetcher.run(ctx, r.dir, args...); err != nil {
		return err
	}
	_, err := r.fetcher.run(ctx, r.dir, "checkout", "--detach", r.checkoutTarget)
	return err
}

// run executes git with output captured. Errors carry git's stderr, with any
// URL userinfo redacted so a token in a remote never reaches a log.
func (f *gitFetcher) run(ctx context.Context, dir string, args ...string) (string, error) {
	stdout, _, err := f.runCombined(ctx, dir, args...)
	return stdout, err
}

// runCombined is like run but also returns stderr on success, so a caller can
// inspect a warning git emits even when it exits zero (for example, silently
// ignoring an object filter the server does not support).
func (f *gitFetcher) runCombined(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error) {
	full := append([]string{"-c", "advice.detachedHead=false"}, args...)
	// #nosec G204 -- the binary is resolved via LookPath. Every other argument
	// is a fixed git subcommand/flag, a commit SHA matched against
	// fullSHAPattern, a ref name that passed validateRefName (rejecting a
	// leading "-" and git's other unsafe ref characters), or a repository
	// path that passed normalizeSubpath and a leading-"-" check in
	// Materialize; no caller-controlled value reaches git unvalidated.
	cmd := exec.CommandContext(ctx, f.git, full...)
	cmd.Dir = dir
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	cmd.Env = f.env()
	if runErr := cmd.Run(); runErr != nil {
		detail := strings.TrimSpace(stderrBuf.String())
		if detail == "" {
			detail = runErr.Error()
		}
		return "", stderrBuf.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), RedactRemote(detail))
	}
	return stdoutBuf.String(), stderrBuf.String(), nil
}

// env is the environment for a git invocation. Silencing the credential
// question takes both variables: git asks an askpass program first (a VS Code
// terminal exports GIT_ASKPASS, which opens a dialog), and an empty
// GIT_ASKPASS ends that search before core.askpass and SSH_ASKPASS are
// consulted, leaving the terminal that GIT_TERMINAL_PROMPT closes. Credential
// helpers still answer, and an ssh key passphrase is ssh's own prompt.
func (f *gitFetcher) env() []string {
	env := append(os.Environ(), "GIT_ADVICE=0")
	if !f.allowPrompts {
		env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	}
	return env
}

// filterIgnoredByServer reports whether git silently answered a
// --filter=blob:none fetch with full objects because the server does not
// support object filtering (for example, uploadpack.allowFilter is off).
// Such a fetch still exits zero, so the warning on stderr is the only signal.
func filterIgnoredByServer(stderr string) bool {
	return strings.Contains(stderr, "not recognized by server")
}

// splitNUL splits NUL-terminated git output into records, dropping the empty
// one after the final terminator. Nothing is trimmed: every byte between two
// NULs belongs to the path.
func splitNUL(out string) []string {
	records := make([]string, 0, strings.Count(out, "\x00"))
	for _, record := range strings.Split(out, "\x00") {
		if record != "" {
			records = append(records, record)
		}
	}
	return records
}
