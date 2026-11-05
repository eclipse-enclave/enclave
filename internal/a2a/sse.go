// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2a

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// SSEContentType is the response content type of streaming methods and of the
// enclave control and liveness streams.
const SSEContentType = "text/event-stream"

// Event is one Server-Sent Event. Data holds one JSON-RPC Response for A2A
// streams and one enclave control payload for control streams.
type Event struct {
	ID    string
	Event string
	Data  []byte
	Retry int
}

// ErrEventTooLarge is returned by SSEReader.Next when one event exceeds the bound.
var ErrEventTooLarge = errors.New("a2a: sse event exceeds size limit")

// SSEWriter writes events and flushes after each one; it never buffers.
type SSEWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// NewSSEWriter sets the stream headers on w; it fails when w cannot flush.
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	if !canFlush(w) {
		return nil, errors.New("a2a: response writer cannot flush")
	}
	h := w.Header()
	h.Set("Content-Type", SSEContentType)
	h.Set("Cache-Control", "no-store")
	// Asks reverse proxies such as nginx not to buffer the stream.
	h.Set("X-Accel-Buffering", "no")
	return &SSEWriter{w: w, rc: http.NewResponseController(w)}, nil
}

// Mirrors the Unwrap walk of http.ResponseController, which has no capability probe.
func canFlush(w http.ResponseWriter) bool {
	for {
		switch t := w.(type) {
		case http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = t.Unwrap()
		default:
			return false
		}
	}
}

// WriteEvent writes and flushes one event.
func (w *SSEWriter) WriteEvent(ev Event) error {
	if strings.ContainsAny(ev.ID, "\r\n\x00") {
		return fmt.Errorf("a2a: sse event id %q contains a line break or NUL", ev.ID)
	}
	if strings.ContainsAny(ev.Event, "\r\n") {
		return fmt.Errorf("a2a: sse event name %q contains a line break", ev.Event)
	}
	if ev.Retry < 0 {
		return fmt.Errorf("a2a: sse retry %d is negative", ev.Retry)
	}
	var buf bytes.Buffer
	if ev.ID != "" {
		buf.WriteString("id: " + ev.ID + "\n")
	}
	if ev.Event != "" {
		buf.WriteString("event: " + ev.Event + "\n")
	}
	if ev.Retry > 0 {
		buf.WriteString("retry: " + strconv.Itoa(ev.Retry) + "\n")
	}
	for _, line := range splitLines(ev.Data) {
		buf.WriteString("data: ")
		buf.Write(line)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')
	return w.write(buf.Bytes())
}

// WriteComment writes a keepalive comment line.
func (w *SSEWriter) WriteComment(text string) error {
	if strings.ContainsAny(text, "\r\n") {
		return errors.New("a2a: sse comment contains a line break")
	}
	return w.write([]byte(": " + text + "\n\n"))
}

func (w *SSEWriter) write(p []byte) error {
	if _, err := w.w.Write(p); err != nil {
		return err
	}
	return w.rc.Flush()
}

// splitLines splits on CRLF, LF and CR, the three SSE line terminators.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	for {
		i := bytes.IndexAny(data, "\r\n")
		if i < 0 {
			return append(lines, data)
		}
		lines = append(lines, data[:i])
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			i++
		}
		data = data[i+1:]
	}
}

// defaultMaxEventBytes applies when NewSSEReader gets a non-positive bound.
const defaultMaxEventBytes = 1 << 20

// SSEReader parses an event stream.
type SSEReader struct {
	br          *bufio.Reader
	max         int
	lastEventID string
	skipLF      bool
	started     bool
	err         error
	line        []byte
}

// NewSSEReader reads events from r; maxEventBytes bounds one event.
func NewSSEReader(r io.Reader, maxEventBytes int) *SSEReader {
	if maxEventBytes <= 0 {
		maxEventBytes = defaultMaxEventBytes
	}
	return &SSEReader{br: bufio.NewReader(r), max: maxEventBytes}
}

// Next returns the next event, or io.EOF at the end of the stream. A stream
// that ends inside an event returns io.ErrUnexpectedEOF; the partial event
// is discarded as WHATWG 9.2.6 requires. ID carries the last event ID seen on
// the stream, which persists across events.
func (r *SSEReader) Next() (Event, error) {
	if r.err != nil {
		return Event{}, r.err
	}
	ev, err := r.next()
	if err != nil {
		r.err = err
	}
	return ev, err
}

func (r *SSEReader) next() (Event, error) {
	var (
		data      bytes.Buffer
		eventType string
		retry     int
		pending   bool
		size      int
	)
	for {
		line, err := r.readLine(r.max - size)
		if err != nil {
			partial := len(line) > 0 && line[0] != ':'
			if errors.Is(err, io.EOF) && (pending || partial) {
				return Event{}, io.ErrUnexpectedEOF
			}
			return Event{}, err
		}
		size += len(line) + 1
		if len(line) == 0 {
			if data.Len() == 0 {
				eventType, retry, pending, size = "", 0, false, 0
				continue
			}
			payload := data.Bytes()
			payload = payload[:len(payload)-1]
			return Event{ID: r.lastEventID, Event: eventType, Data: payload, Retry: retry}, nil
		}
		if line[0] == ':' {
			continue
		}
		name, value := line, []byte(nil)
		if i := bytes.IndexByte(line, ':'); i >= 0 {
			name, value = line[:i], line[i+1:]
			value = bytes.TrimPrefix(value, []byte(" "))
		}
		switch string(name) {
		case "event":
			eventType = string(value)
			pending = true
		case "data":
			data.Write(value)
			data.WriteByte('\n')
			pending = true
		case "id":
			if bytes.IndexByte(value, 0) < 0 {
				r.lastEventID = string(value)
			}
		case "retry":
			if n, ok := parseRetry(value); ok {
				retry = n
			}
		}
	}
}

func parseRetry(value []byte) (int, bool) {
	if len(value) == 0 {
		return 0, false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(string(value))
	return n, err == nil
}

// readLine returns one line without its terminator; limit bounds its length
// plus terminator. The returned slice is valid until the next call.
func (r *SSEReader) readLine(limit int) ([]byte, error) {
	r.line = r.line[:0]
	for {
		c, err := r.br.ReadByte()
		if err != nil {
			// The unterminated tail lets the caller tell truncation from a clean end.
			return r.line, err
		}
		if r.skipLF {
			r.skipLF = false
			if c == '\n' {
				continue
			}
		}
		switch c {
		case '\n':
			return r.stripBOM(r.line), nil
		case '\r':
			r.skipLF = true
			return r.stripBOM(r.line), nil
		}
		r.line = append(r.line, c)
		if len(r.line)+1 > limit {
			return nil, ErrEventTooLarge
		}
	}
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func (r *SSEReader) stripBOM(line []byte) []byte {
	if r.started {
		return line
	}
	r.started = true
	return bytes.TrimPrefix(line, utf8BOM)
}
