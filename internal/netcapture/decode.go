// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netcapture

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

// decodableEncoding returns the content encoding of a message whose body the
// store decodes before redacting and storing it, or "" when it is stored as
// relayed. Only the encodings the standard library can decode qualify, and
// only on their own: a body encoded twice is stored as relayed.
func decodableEncoding(header http.Header) string {
	values := header.Values("Content-Encoding")
	if len(values) != 1 {
		return ""
	}
	switch encoding := strings.ToLower(strings.TrimSpace(values[0])); encoding {
	case "gzip", "x-gzip", "deflate":
		return encoding
	default:
		return ""
	}
}

// errCaptureFull stops a decoder whose decoded output no longer fits the
// capture, so a small body cannot keep it busy expanding.
var errCaptureFull = errors.New("capture full")

// decodingSink decodes a body as it is relayed and hands the decoded bytes to
// its sink, which redacts and stores them. Decoding runs on its own goroutine,
// fed through a pipe, because the decompressors read rather than accept
// writes. It starts with the first write, so a message without a body costs
// nothing.
type decodingSink struct {
	sink     *bodySink
	encoding string

	mu     sync.Mutex
	pipe   *io.PipeWriter
	done   chan struct{}
	closed bool
	// wire counts the encoded bytes that crossed the proxy.
	wire int64
	// failed is set by the decoder when the body did not decode to its end. It
	// is read only after done is closed.
	failed bool
}

func newDecodingSink(sink *bodySink, encoding string) *decodingSink {
	return &decodingSink{sink: sink, encoding: encoding}
}

// Write always reports success: the client's copy of the body must not depend
// on the capture.
func (d *decodingSink) Write(p []byte) (int, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return len(p), nil
	}
	d.wire += int64(len(p))
	if d.pipe == nil {
		d.start()
	}
	pipe := d.pipe
	d.mu.Unlock()
	// Fails once the decoder has stopped, which drops the rest.
	_, _ = pipe.Write(p)
	return len(p), nil
}

func (d *decodingSink) start() {
	reader, writer := io.Pipe()
	d.pipe, d.done = writer, make(chan struct{})
	go func() {
		defer close(d.done)
		if err := d.decode(reader); err != nil && !errors.Is(err, errCaptureFull) {
			d.failed = true
		}
		// Unblock a writer if decoding stopped before the input did.
		_ = reader.CloseWithError(errCaptureFull)
	}()
}

func (d *decodingSink) decode(input io.Reader) error {
	decoder, err := newDecoder(d.encoding, input)
	if err != nil {
		return err
	}
	buffer := make([]byte, 32*1024)
	for {
		n, err := decoder.Read(buffer)
		if n > 0 {
			_, _ = d.sink.Write(buffer[:n])
			if d.sink.full() {
				return errCaptureFull
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// newDecoder reads deflate as zlib, which is what the encoding means, and
// falls back to raw DEFLATE, which some servers send under that name.
func newDecoder(encoding string, input io.Reader) (io.Reader, error) {
	if encoding != "deflate" {
		return gzip.NewReader(input)
	}
	buffered := bufio.NewReader(input)
	header, err := buffered.Peek(2)
	if err == nil && header[0]&0x0f == 8 && (uint16(header[0])<<8|uint16(header[1]))%31 == 0 {
		return zlib.NewReader(buffered)
	}
	return flate.NewReader(buffered), nil
}

// finish ends the input, waits for the decoder to drain it, and reports the
// encoded byte count and whether the body decoded to its end.
func (d *decodingSink) finish() (int64, bool) {
	d.mu.Lock()
	d.closed = true
	pipe, done, wire := d.pipe, d.done, d.wire
	d.mu.Unlock()
	if pipe == nil {
		return wire, false
	}
	_ = pipe.Close()
	<-done
	return wire, d.failed
}
