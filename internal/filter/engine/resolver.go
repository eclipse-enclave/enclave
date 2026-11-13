// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"enclave/internal/filter"
	"enclave/internal/filter/token"
)

// Resolver tuning.
const (
	ResolverCacheTTL      = 30 * time.Second
	resolverCacheSize     = 4096
	resolverTimeout       = 10 * time.Second
	maxResolverBody       = 1 << 20
	maxLookupsPerValue    = 128
	maxResolverIDLength   = 512
	resolverAcceptHeader  = "application/json"
	resolverIDDescription = "letters, digits, \".\", \"_\", \"~\", and \"-\""
)

var resolverIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// Fetcher performs a resolver lookup: a GET of rawURL with the real
// credential, returning the body of a 2xx response. The request bypasses the
// policy and is never visible to the container.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) ([]byte, error)
}

// HTTPFetcher is the Fetcher the gateway uses.
type HTTPFetcher struct {
	Client *http.Client
	Tokens token.Source
	Inject filter.Inject
}

// lookupError is a failed resolver lookup with a short cause for the
// network log, such as "http-404" or "unreachable". It never holds a secret.
type lookupError struct {
	cause string
	err   error
}

func (e *lookupError) Error() string { return e.err.Error() }

func (e *lookupError) Unwrap() error { return e.err }

func lookupFailure(cause string, err error) error {
	return &lookupError{cause: cause, err: err}
}

// resolverCause returns the cause of a resolver error, or "error".
func resolverCause(err error) string {
	var failure *lookupError
	if errors.As(err, &failure) {
		return failure.cause
	}
	return "error"
}

// Fetch implements Fetcher. A 401 invalidates the token it used.
func (f *HTTPFetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	value, err := f.Tokens.Token(ctx)
	if err != nil {
		return nil, lookupFailure("token:"+token.Cause(err), errors.New("no access token for the lookup"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build resolver request: %w", err)
	}
	req.Header.Set(f.Inject.Header, f.Inject.Apply(value))
	req.Header.Set("Accept", resolverAcceptHeader)
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req) // #nosec G704 -- resolver URLs are validated against the governed hosts.
	if err != nil {
		return nil, lookupFailure("unreachable", errors.New("resolver request failed"))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		f.Tokens.Invalidate(value)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, lookupFailure(fmt.Sprintf("http-%d", resp.StatusCode), fmt.Errorf("resolver lookup returned HTTP %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResolverBody+1))
	if err != nil {
		return nil, lookupFailure("bad-response", errors.New("resolver response unreadable"))
	}
	if len(body) > maxResolverBody {
		return nil, lookupFailure("bad-response", errors.New("resolver response too large"))
	}
	return body, nil
}

type resolver struct {
	name    string
	def     filter.Resolver
	extract filter.JSONPath
	fetch   Fetcher
	cache   *parentCache
	// jsonKeys is the policy's JSONKeys mode.
	jsonKeys string
}

// validResolverID keeps IDs to a charset that path escaping cannot turn into
// a different path, query, or dot segment.
func validResolverID(id string) bool {
	return id != "." && id != ".." && len(id) <= maxResolverIDLength && resolverIDPattern.MatchString(id)
}

func (r *resolver) parents(ctx context.Context, id string) ([]string, error) {
	if !validResolverID(id) {
		return nil, lookupFailure("invalid-id", fmt.Errorf("resource ID must use only %s", resolverIDDescription))
	}
	if parents, ok := r.cache.get(r.name, id); ok {
		return parents, nil
	}
	ctx, cancel := context.WithTimeout(ctx, resolverTimeout)
	defer cancel()
	rawURL := strings.Replace(r.def.URL, filter.ResolverIDToken, url.PathEscape(id), 1)
	body, err := r.fetch.Fetch(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	doc, err := filter.DecodeJSONKeys(body, r.jsonKeys)
	if err != nil {
		return nil, lookupFailure("bad-response", fmt.Errorf("resolver %s: %w", r.name, err))
	}
	parents, err := r.extract.SelectStrings(doc)
	if err != nil {
		return nil, lookupFailure("bad-response", fmt.Errorf("resolver %s: %w", r.name, err))
	}
	r.cache.put(r.name, id, parents)
	return parents, nil
}

// within reports whether id has an ancestor in targets. A failed first lookup
// is an error. A failed lookup further up ends that branch of the walk, as if
// it reached the top. Neither ever grants.
func (r *resolver) within(ctx context.Context, id string, targets map[string]bool) (bool, error) {
	visited := map[string]bool{id: true}
	frontier := []string{id}
	lookups := 0
	for depth := 1; depth <= r.def.MaxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, node := range frontier {
			if lookups == maxLookupsPerValue {
				return false, nil
			}
			lookups++
			parents, err := r.parents(ctx, node)
			if err != nil {
				if depth == 1 {
					return false, err
				}
				continue
			}
			for _, parent := range parents {
				if targets[parent] {
					return true, nil
				}
				if !visited[parent] && validResolverID(parent) {
					visited[parent] = true
					next = append(next, parent)
				}
			}
		}
		frontier = next
	}
	return false, nil
}

type parentCache struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	max     int
	entries map[string]cacheEntry
}

type cacheEntry struct {
	parents []string
	expires time.Time
}

func newParentCache(now func() time.Time) *parentCache {
	return &parentCache{now: now, ttl: ResolverCacheTTL, max: resolverCacheSize, entries: map[string]cacheEntry{}}
}

func cacheKey(resolverName, id string) string { return resolverName + "\x00" + id }

func (c *parentCache) get(resolverName, id string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[cacheKey(resolverName, id)]
	if !ok || !c.now().Before(entry.expires) {
		return nil, false
	}
	return entry.parents, true
}

func (c *parentCache) put(resolverName, id string, parents []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.entries) >= c.max {
		for key, entry := range c.entries {
			if !now.Before(entry.expires) {
				delete(c.entries, key)
			}
		}
	}
	if len(c.entries) >= c.max {
		var oldestKey string
		var oldest time.Time
		for key, entry := range c.entries {
			if oldestKey == "" || entry.expires.Before(oldest) {
				oldestKey, oldest = key, entry.expires
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[cacheKey(resolverName, id)] = cacheEntry{parents: append([]string(nil), parents...), expires: now.Add(c.ttl)}
}

func (c *parentCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
