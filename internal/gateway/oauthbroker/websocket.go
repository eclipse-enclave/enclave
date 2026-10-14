// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package oauthbroker

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"slices"
	"strings"
)

// maxWebSocketHeld bounds a frame, and the fragments of a message held back
// together, while they are scanned.
const maxWebSocketHeld = 16 << 20

// prepareWebSocket readies an upgrade that carries released credentials for
// scanning. Only WebSocket frames can be parsed, and without the compression
// extensions the handshake offered, their payloads are plain bytes.
func prepareWebSocket(req *http.Request) error {
	if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		return ErrDenied
	}
	req.Header.Del("Sec-WebSocket-Extensions")
	return nil
}

// scanWebSocket wraps the upgraded connection so that frames from the server
// reach the workload only after they are checked for patterns.
func scanWebSocket(resp *http.Response, patterns [][]byte) (*http.Response, error) {
	conn, ok := resp.Body.(io.ReadWriteCloser)
	if !ok || len(resp.Header.Values("Sec-WebSocket-Extensions")) != 0 {
		_ = resp.Body.Close()
		return nil, ErrResponse
	}
	resp.Body = &websocketTripwire{conn: conn, patterns: patterns}
	return resp, nil
}

// websocketTripwire relays server frames unchanged until one would complete a
// pattern, then fails before that frame is returned. A message's fragments are
// scanned as one payload; a fragment that ends in a partial match is held back
// until the next fragment settles it, so no prefix of a pattern escapes either.
type websocketTripwire struct {
	conn       io.ReadWriteCloser
	patterns   [][]byte
	ready      []byte
	held       []byte
	tail       []byte
	fragmented bool
	err        error
}

func (w *websocketTripwire) Read(p []byte) (int, error) {
	for len(w.ready) == 0 {
		if w.err != nil {
			return 0, w.err
		}
		w.err = w.next()
	}
	n := copy(p, w.ready)
	w.ready = w.ready[n:]
	return n, nil
}

func (w *websocketTripwire) Write(p []byte) (int, error) {
	return w.conn.Write(p)
}

func (w *websocketTripwire) Close() error {
	return w.conn.Close()
}

// next reads one frame and queues what may reach the workload. A connection
// that ends inside a held message drops the held fragments.
func (w *websocketTripwire) next() error {
	header := make([]byte, 2, 10)
	if _, err := io.ReadFull(w.conn, header); err != nil {
		return err
	}
	final, opcode := header[0]&0x80 != 0, header[0]&0x0f
	// Reserved bits mark compressed or extended frames, which the handshake
	// did not negotiate; servers never mask frames.
	if header[0]&0x70 != 0 || header[1]&0x80 != 0 {
		return ErrResponse
	}
	length := uint64(header[1] & 0x7f)
	if length >= 126 {
		size := 2
		if length == 127 {
			size = 8
		}
		header = header[:2+size]
		if _, err := io.ReadFull(w.conn, header[2:]); err != nil {
			return err
		}
		if size == 2 {
			length = uint64(binary.BigEndian.Uint16(header[2:]))
		} else {
			length = binary.BigEndian.Uint64(header[2:])
		}
	}
	control := opcode >= 8
	switch {
	case opcode > 10 || (opcode > 2 && opcode < 8):
		return ErrResponse
	case control && (!final || length > 125):
		return ErrResponse
	case length > maxWebSocketHeld-uint64(len(w.held)):
		return ErrResponse
	case !control && (opcode == 0) != w.fragmented:
		return ErrResponse
	}
	frame := make([]byte, len(header)+int(length))
	copy(frame, header)
	if _, err := io.ReadFull(w.conn, frame[len(header):]); err != nil {
		return err
	}
	payload := frame[len(header):]
	if control {
		if contains(payload, w.patterns) {
			return ErrResponse
		}
		if opcode == 8 {
			// A close ends the connection; an unfinished message cannot
			// complete.
			w.held, w.tail, w.fragmented = nil, nil, false
		}
		w.ready = append(w.ready, frame...)
		return nil
	}
	window := slices.Concat(w.tail, payload)
	if contains(window, w.patterns) {
		return ErrResponse
	}
	w.held = append(w.held, frame...)
	w.fragmented = !final
	if final {
		w.tail = nil
	} else {
		w.tail = window[max(0, len(window)-longest(w.patterns)+1):]
	}
	if final || partialMatch(window, w.patterns) == 0 {
		w.ready, w.held = append(w.ready, w.held...), nil
	}
	return nil
}

func contains(data []byte, patterns [][]byte) bool {
	return slices.ContainsFunc(patterns, func(pattern []byte) bool { return bytes.Contains(data, pattern) })
}

func longest(patterns [][]byte) int {
	n := 0
	for _, pattern := range patterns {
		n = max(n, len(pattern))
	}
	return n
}

// partialMatch returns the length of the longest suffix of data that begins a
// pattern without completing it.
func partialMatch(data []byte, patterns [][]byte) int {
	longest := 0
	for _, pattern := range patterns {
		for k := min(len(pattern)-1, len(data)); k > longest; k-- {
			if bytes.HasSuffix(data, pattern[:k]) {
				longest = k
				break
			}
		}
	}
	return longest
}
