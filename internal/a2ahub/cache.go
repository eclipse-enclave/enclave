// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"sync"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

// liveTasksPerWorker bounds non-terminal cache entries; terminal ones are
// bounded by Config.RetainedResults.
const liveTasksPerWorker = 1024

// owner identifies the principal a task is bound to.
type owner struct {
	member     string
	generation string
}

func ownerOf(p a2a.Principal) owner { return owner{member: p.Member, generation: p.Generation} }

// taskSummary is the advisory record of one task seen in an adapter response.
type taskSummary struct {
	owner       owner
	state       a2a.TaskState
	reason      a2a.EndingReason
	termination a2a.Termination
	terminal    bool
}

type workerCache struct {
	generation peergroup.Generation
	tasks      map[string]*taskSummary
	// live and terminal hold task IDs in insertion order; live may hold
	// IDs that turned terminal since, which eviction skips.
	live      []string
	liveCount int
	terminal  []string
}

// ownerCache is the hub's advisory, per-epoch task ownership cache (proposal
// 6, 10): an absent entry means "forward and let the worker decide".
type ownerCache struct {
	mu       sync.Mutex
	retained int
	workers  map[string]*workerCache
}

func newOwnerCache(retained int) *ownerCache {
	return &ownerCache{retained: retained, workers: map[string]*workerCache{}}
}

// owner returns the cached owner of a worker's task.
func (c *ownerCache) owner(worker string, gen peergroup.Generation, taskID string) (owner, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	wc := c.workers[worker]
	if wc == nil || wc.generation != gen {
		return owner{}, false
	}
	t := wc.tasks[taskID]
	if t == nil {
		return owner{}, false
	}
	return t.owner, true
}

// record notes a task seen in a response to a request by o; the first owner wins.
func (c *ownerCache) record(worker string, gen peergroup.Generation, taskID string, o owner, s taskSummary) {
	if taskID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	wc := c.workers[worker]
	if wc == nil || wc.generation != gen {
		wc = &workerCache{generation: gen, tasks: map[string]*taskSummary{}}
		c.workers[worker] = wc
	}
	t := wc.tasks[taskID]
	if t == nil {
		t = &taskSummary{owner: o}
		wc.tasks[taskID] = t
		wc.live = append(wc.live, taskID)
		wc.liveCount++
	}
	if t.terminal {
		return
	}
	if s.state != "" {
		t.state = s.state
	}
	if s.reason != "" {
		t.reason = s.reason
	}
	if s.termination != "" {
		t.termination = s.termination
	}
	if t.state.Terminal() {
		t.terminal = true
		wc.liveCount--
		wc.terminal = append(wc.terminal, taskID)
	}
	wc.evict(c.retained)
}

func (wc *workerCache) evict(retained int) {
	for len(wc.terminal) > retained {
		delete(wc.tasks, wc.terminal[0])
		wc.terminal = wc.terminal[1:]
	}
	for wc.liveCount > liveTasksPerWorker && len(wc.live) > 0 {
		id := wc.live[0]
		wc.live = wc.live[1:]
		if t := wc.tasks[id]; t != nil && !t.terminal {
			delete(wc.tasks, id)
			wc.liveCount--
		}
	}
	if len(wc.live) > 2*liveTasksPerWorker {
		kept := make([]string, 0, wc.liveCount)
		for _, id := range wc.live {
			if t := wc.tasks[id]; t != nil && !t.terminal {
				kept = append(kept, id)
			}
		}
		wc.live = kept
	}
}

// prune drops workers absent from st or with another generation.
func (c *ownerCache) prune(st *state) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, wc := range c.workers {
		m, ok := st.members[name]
		if !ok || m.Generation != wc.generation {
			delete(c.workers, name)
		}
	}
}

func (c *ownerCache) summary(worker, taskID string) (taskSummary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	wc := c.workers[worker]
	if wc == nil || wc.tasks[taskID] == nil {
		return taskSummary{}, false
	}
	return *wc.tasks[taskID], true
}
