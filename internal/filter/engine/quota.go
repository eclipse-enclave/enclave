// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"sort"
	"sync"
)

// Ledger tracks per-session counter usage. In-flight requests hold
// reservations that count against the quota until they commit or release, so
// concurrent requests can never overshoot it.
type Ledger struct {
	mu       sync.Mutex
	counters map[string]*counterState
}

type counterState struct {
	limit int64
	keyed bool
	// Keyed counters: committed distinct keys and pending keys with their
	// reservation count.
	committedKeys map[string]struct{}
	pendingKeys   map[string]int
	// Unkeyed counters.
	committed int64
	pending   int64
}

func (c *counterState) used() int64 {
	if c.keyed {
		return int64(len(c.committedKeys) + len(c.pendingKeys))
	}
	return c.committed + c.pending
}

// NewLedger creates a ledger for counters with quotas. keyed tells for each
// counter whether it counts distinct keys.
func NewLedger(limits map[string]int64, keyed map[string]bool) *Ledger {
	l := &Ledger{counters: map[string]*counterState{}}
	for name, limit := range limits {
		l.counters[name] = &counterState{
			limit:         limit,
			keyed:         keyed[name],
			committedKeys: map[string]struct{}{},
			pendingKeys:   map[string]int{},
		}
	}
	return l
}

// Need is what one request adds to a counter: its keys for a keyed counter,
// one otherwise.
type Need struct {
	Counter string
	Keys    []string
}

// QuotaExceeded names the counter that would exceed its quota.
type QuotaExceeded struct {
	Counter string
	Limit   int64
}

// Reservation holds quota for one request until Commit or Release.
type Reservation struct {
	ledger *Ledger
	items  []reservedItem
	done   bool
}

type reservedItem struct {
	counter string
	keys    []string
	n       int64
}

// Reserve atomically reserves all needs or none. Counters without a quota are
// ignored. A quota of zero rejects every counted request. A keyed need only
// takes quota for keys that are neither committed nor reserved by another
// request.
func (l *Ledger) Reserve(needs []Need) (*Reservation, *QuotaExceeded) {
	l.mu.Lock()
	defer l.mu.Unlock()
	merged := map[string]*reservedItem{}
	for _, need := range needs {
		state, ok := l.counters[need.Counter]
		if !ok {
			continue
		}
		item := merged[need.Counter]
		if item == nil {
			item = &reservedItem{counter: need.Counter}
			merged[need.Counter] = item
		}
		if state.keyed {
			item.keys = append(item.keys, need.Keys...)
		} else {
			item.n++
		}
	}
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		state, item := l.counters[name], merged[name]
		if state.limit == 0 {
			return nil, &QuotaExceeded{Counter: name, Limit: 0}
		}
		if !state.keyed {
			if state.used()+item.n > state.limit {
				return nil, &QuotaExceeded{Counter: name, Limit: state.limit}
			}
			continue
		}
		var keys []string
		fresh := int64(0)
		for _, key := range dedupe(item.keys) {
			if _, done := state.committedKeys[key]; done {
				continue
			}
			keys = append(keys, key)
			if _, pending := state.pendingKeys[key]; !pending {
				fresh++
			}
		}
		item.keys = keys
		if state.used()+fresh > state.limit {
			return nil, &QuotaExceeded{Counter: name, Limit: state.limit}
		}
	}
	res := &Reservation{ledger: l}
	for _, name := range names {
		state, item := l.counters[name], merged[name]
		if state.keyed {
			for _, key := range item.keys {
				state.pendingKeys[key]++
			}
		} else {
			state.pending += item.n
		}
		res.items = append(res.items, *item)
	}
	return res, nil
}

// Commit turns the reservation into usage. Calling it twice, or after
// Release, does nothing.
func (r *Reservation) Commit() { r.finish(true) }

// Release returns the reserved quota. Calling it twice, or after Commit, does
// nothing.
func (r *Reservation) Release() { r.finish(false) }

func (r *Reservation) finish(commit bool) {
	if r == nil {
		return
	}
	l := r.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.done {
		return
	}
	r.done = true
	for _, item := range r.items {
		state := l.counters[item.counter]
		if state.keyed {
			for _, key := range item.keys {
				state.pendingKeys[key]--
				if state.pendingKeys[key] <= 0 {
					delete(state.pendingKeys, key)
				}
				if commit {
					state.committedKeys[key] = struct{}{}
				}
			}
			continue
		}
		state.pending -= item.n
		if commit {
			state.committed += item.n
		}
	}
}

// Usage reports committed usage per counter with a quota.
func (l *Ledger) Usage() map[string]int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]int64{}
	for name, state := range l.counters {
		if state.keyed {
			out[name] = int64(len(state.committedKeys))
		} else {
			out[name] = state.committed
		}
	}
	return out
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
