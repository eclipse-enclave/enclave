// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"cmp"
	"fmt"
)

// resultError aborts a journal update with a result for the caller.
type resultError struct{ res Result }

func (e *resultError) Error() string { return e.res.Error }

// journalSend resolves the message ID (new, or the original for a flagged retry) and persists the entry before the send.
func (c *Client) journalSend(req SendRequest) (JournalEntry, *Result, error) {
	hash := ContentHash(req.Text)
	now := c.journal.Timestamp()
	var chosen JournalEntry
	err := c.journal.Update(func(entries []JournalEntry) ([]JournalEntry, error) {
		index := -1
		switch {
		case !req.Retry:
		case req.MessageID != "":
			for i := range entries {
				if entries[i].MessageID == req.MessageID {
					index = i
				}
			}
			if index >= 0 {
				e := entries[index]
				if e.Peer != req.Peer || e.ContentHash != hash || e.Context != req.Context {
					res := errorResult("message_id %s was sent to %s with other text or context; a retry repeats the original peer, text and context", req.MessageID, e.Peer)
					res.Candidates = []RecentEntry{recentEntry(e)}
					return nil, &resultError{res}
				}
			}
		default:
			var matches []int
			for i := range entries {
				e := entries[i]
				if e.Peer == req.Peer && e.ContentHash == hash && e.Context == req.Context {
					matches = append(matches, i)
				}
			}
			switch len(matches) {
			case 0:
				return nil, &resultError{errorResult("no earlier send to %s with this text and context; send without retry to start a new task", req.Peer)}
			case 1:
				index = matches[0]
			default:
				res := errorResult("%d earlier sends match; pass message_id to choose one", len(matches))
				for _, i := range matches {
					res.Candidates = append(res.Candidates, recentEntry(entries[i]))
				}
				return nil, &resultError{res}
			}
		}
		if index < 0 {
			id := req.MessageID
			if id == "" {
				id = newMessageID()
			}
			entries = append(entries, JournalEntry{
				MessageID:   id,
				Peer:        req.Peer,
				Context:     req.Context,
				ContentHash: hash,
				SentAt:      now,
			})
			index = len(entries) - 1
		}
		entries[index].Attempts++
		entries[index].Error = ""
		entries[index].UpdatedAt = now
		chosen = entries[index]
		// Keep the entry being sent newest so the size bound never evicts it.
		entries = append(append(entries[:index:index], entries[index+1:]...), chosen)
		return entries, nil
	})
	if re, ok := err.(*resultError); ok {
		return JournalEntry{}, &re.res, nil
	}
	if err != nil {
		return JournalEntry{}, nil, fmt.Errorf("a2a client: send journal: %w", err)
	}
	return chosen, nil, nil
}

// journalRecord stores the outcome on the entry found by message ID or handle; failures only lose recovery data.
func (c *Client) journalRecord(messageID, handle string, res Result) {
	now := c.journal.Timestamp()
	err := c.journal.Update(func(entries []JournalEntry) ([]JournalEntry, error) {
		for i := range entries {
			e := &entries[i]
			if (messageID != "" && e.MessageID == messageID) || (handle != "" && e.Handle == handle) {
				if res.Handle != "" {
					e.Handle = res.Handle
				}
				if res.Context != "" {
					e.ContextHandle = res.Context
				}
				if res.State != "" {
					e.State = res.State
				}
				e.Reason, e.Termination, e.Error = res.Reason, res.Termination, res.Error
				e.UpdatedAt = now
				return entries, nil
			}
		}
		return nil, errReadOnly
	})
	if err != nil && err != errReadOnly {
		c.logf("a2a: send journal: %v", err)
	}
}

func (c *Client) journalByHandle(handle string) (JournalEntry, bool) {
	entries, err := c.journal.Entries()
	if err != nil {
		return JournalEntry{}, false
	}
	for _, e := range entries {
		if e.Handle == handle {
			return e, true
		}
	}
	return JournalEntry{}, false
}

// recent lists the newest journal entries for handle recovery (CR-P3-2).
func (c *Client) recent() (Result, error) {
	entries, err := c.journal.Entries()
	if err != nil {
		return Result{}, fmt.Errorf("a2a client: send journal: %w", err)
	}
	if len(entries) == 0 {
		return Result{Note: "No sends recorded for this session."}, nil
	}
	res := Result{Note: noteRecent}
	for i := len(entries) - 1; i >= 0 && len(res.Recent) < recentLimit; i-- {
		res.Recent = append(res.Recent, recentEntry(entries[i]))
	}
	return res, nil
}

func recentEntry(e JournalEntry) RecentEntry {
	return RecentEntry{
		Handle:    e.Handle,
		Peer:      e.Peer,
		State:     e.State,
		MessageID: e.MessageID,
		Context:   cmp.Or(e.ContextHandle, e.Context),
		SentAt:    e.SentAt,
		Error:     e.Error,
	}
}
