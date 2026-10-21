// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netcapture

import (
	"bytes"
	"net/http"
	"sort"
)

// Secret is a value the gateway releases and the placeholder that stands in for
// it inside the sandbox. The capture records the placeholder wherever the value
// would appear.
type Secret struct {
	Value       string
	Placeholder string
}

// redactor replaces released secret values by their placeholders. The request
// headers are captured before the gateway releases anything, so this covers
// what the gateway does not control: an upstream that echoes a credential back,
// or a value the sandbox obtained some other way.
type redactor struct {
	secrets []Secret
}

func newRedactor(secrets []Secret) *redactor {
	unique := map[string]Secret{}
	for _, secret := range secrets {
		if secret.Value == "" {
			continue
		}
		if _, seen := unique[secret.Value]; !seen {
			unique[secret.Value] = secret
		}
	}
	if len(unique) == 0 {
		return nil
	}
	r := &redactor{}
	for _, secret := range unique {
		r.secrets = append(r.secrets, secret)
	}
	// Longest first, so a value containing another is replaced whole.
	sort.Slice(r.secrets, func(i, j int) bool {
		if len(r.secrets[i].Value) != len(r.secrets[j].Value) {
			return len(r.secrets[i].Value) > len(r.secrets[j].Value)
		}
		return r.secrets[i].Value < r.secrets[j].Value
	})
	return r
}

func (r *redactor) string(value string) string {
	if r == nil {
		return value
	}
	return string(r.replace([]byte(value)))
}

func (r *redactor) replace(value []byte) []byte {
	out, _ := r.scan(value, false)
	return out
}

// header returns a redacted copy of header; the request's own headers are
// left alone. Should two names redact to the same one, their values are kept
// together.
func (r *redactor) header(header http.Header) http.Header {
	if r == nil {
		if clone := header.Clone(); clone != nil {
			return clone
		}
		return http.Header{}
	}
	redacted := make(http.Header, len(header))
	for key, values := range header {
		name := r.string(key)
		for _, value := range values {
			redacted[name] = append(redacted[name], r.string(value))
		}
	}
	return redacted
}

// streamRedactor redacts a body that arrives in pieces. A value split across
// two writes is only found once both halves are seen, so a tail that could be
// the start of one is held back.
type streamRedactor struct {
	redactor *redactor
	pending  []byte
}

func (r *redactor) stream() *streamRedactor {
	if r == nil {
		return nil
	}
	return &streamRedactor{redactor: r}
}

// feed returns the part of the stream that is final and keeps back a tail that
// could still turn out to start a secret value.
func (s *streamRedactor) feed(chunk []byte) []byte {
	out, tail := s.redactor.scan(append(s.pending, chunk...), true)
	s.pending = append(s.pending[:0], tail...)
	return out
}

// flush redacts and returns the held-back tail once the stream has ended.
func (s *streamRedactor) flush() []byte {
	out, _ := s.redactor.scan(s.pending, false)
	s.pending = nil
	return out
}

// scan replaces secret values left to right: of two overlapping values the one
// starting first wins, and of two starting together the longer one. With hold
// it stops at the first position where a value could still begin once more
// bytes arrive, so a match is only replaced when nothing that would win over it
// is still open, and returns the rest unprocessed.
//
// The next occurrence of each value is remembered and only searched again once
// a replacement has passed it, so a buffer full of matches is still scanned in
// linear time.
func (r *redactor) scan(buffer []byte, hold bool) ([]byte, []byte) {
	var out []byte
	const unknown = -2
	next := make([]int, len(r.secrets))
	for i := range next {
		next[i] = unknown
	}
	open := len(buffer)
	if hold {
		open = r.openPrefix(buffer)
	}
	position := 0
	for {
		if position > open {
			open = position + r.openPrefix(buffer[position:])
		}
		best := -1
		for i, secret := range r.secrets {
			if next[i] == unknown || (next[i] >= 0 && next[i] < position) {
				next[i] = -1
				if index := bytes.Index(buffer[position:], []byte(secret.Value)); index >= 0 {
					next[i] = position + index
				}
			}
			if next[i] >= 0 && (best < 0 || next[i] < next[best]) {
				best = i
			}
		}
		if best < 0 || next[best] >= open {
			return append(out, buffer[position:open]...), buffer[open:]
		}
		out = append(out, buffer[position:next[best]]...)
		out = append(out, r.secrets[best].Placeholder...)
		position = next[best] + len(r.secrets[best].Value)
	}
}

// openPrefix returns the first position from which the rest of buffer is a
// proper prefix of a secret value, or len(buffer) when there is none.
func (r *redactor) openPrefix(buffer []byte) int {
	open := len(buffer)
	for _, secret := range r.secrets {
		value := []byte(secret.Value)
		for start := max(0, len(buffer)-len(value)+1); start < open; start++ {
			if bytes.HasPrefix(value, buffer[start:]) {
				open = start
				break
			}
		}
	}
	return open
}
