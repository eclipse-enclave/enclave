// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netcapture

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"enclave/internal/gateway/wsframe"
	"enclave/internal/netlog"
)

// socketCapture follows the frames of a WebSocket connection in both
// directions and appends each message to the messages file as one line. Each
// direction is fed by the goroutine relaying it, so the two only share the
// file and its counts.
type socketCapture struct {
	store          *Store
	path           string
	client, server *socketDirection

	mu             sync.Mutex
	file           *os.File
	messages       int64
	storedMessages int64
	stored         int64
	// full is set once the socket stores nothing more: its own budget or the
	// session's is used up, or a write failed.
	full      bool
	truncated bool
}

// newSocket creates the messages file of a WebSocket connection, or returns
// nil when it cannot.
func (s *Store) newSocket(id string) *socketCapture {
	path := filepath.Join(s.dir, id+messagesSuffix)
	// Readable by the host user; see bodySink.persist.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o644) // #nosec G302 G304 -- path is built from a generated ID inside the session directory.
	if err != nil {
		return nil
	}
	socket := &socketCapture{store: s, path: path, file: file}
	socket.client = &socketDirection{socket: socket, from: FromClient}
	socket.server = &socketDirection{socket: socket, from: FromServer}
	return socket
}

// add stores a finished message, unless the socket is full.
func (s *socketCapture) add(message Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages++
	s.truncated = s.truncated || message.Truncated
	if s.full {
		s.truncated = true
		return
	}
	// Payloads are read with tail -f as often as with jq, so markup stays as
	// sent rather than escaped.
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(message); err != nil {
		s.truncated = true
		return
	}
	line := encoded.Bytes()
	size := int64(len(line))
	// A line is stored whole or not at all, so the file stays valid JSON Lines.
	if s.stored+size > s.store.maxSocket {
		s.full, s.truncated = true, true
		return
	}
	if granted := s.store.reserve(size); granted < size {
		s.store.release(granted)
		s.full, s.truncated = true, true
		return
	}
	written, err := s.file.Write(line)
	s.stored += int64(written)
	if unused := size - int64(written); unused > 0 {
		s.store.release(unused)
	}
	if err != nil {
		s.full, s.truncated = true, true
		return
	}
	s.storedMessages++
}

// finish ends both directions once the relay has stopped, stores what they
// still hold as cut short, and describes the socket for the record. Both
// relay goroutines have returned by then, so nothing adds a message after it.
func (s *socketCapture) finish() *WebSocket {
	if s == nil {
		return nil
	}
	s.client.finish()
	s.server.finish()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.file.Close(); err != nil {
		s.truncated = true
	}
	if s.stored == 0 {
		_ = os.Remove(s.path)
	}
	socket := &WebSocket{
		Messages:       s.messages,
		StoredMessages: s.storedMessages,
		Bytes:          s.client.wire + s.server.wire,
		StoredBytes:    s.stored,
		Truncated:      s.truncated,
		Error:          strings.Join(slices.DeleteFunc([]string{s.client.err, s.server.err}, func(err string) bool { return err == "" }), "; "),
	}
	if s.stored > 0 {
		socket.File = filepath.Base(s.path)
	}
	return socket
}

func (s *socketCapture) storedBytes() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stored
}

func (s *socketCapture) remove() {
	if s != nil {
		_ = os.Remove(s.path)
	}
}

// socketDirection follows the frames one side sends. It sees the bytes in
// whatever pieces the relay copies them, so it keeps a frame header that
// arrives split until it is whole.
type socketDirection struct {
	socket *socketCapture
	from   string
	// wire counts the bytes this side sent.
	wire int64
	// relayed is when the bytes being parsed were relayed, which is when a
	// message that ends in them was sent on.
	relayed time.Time

	header  []byte
	frame   wsframe.Header
	inFrame bool
	// offset is how much of the current frame's payload has been seen.
	offset  uint64
	message *pendingMessage
	control []byte
	// err is why the frames could not be followed; once set, nothing more is
	// parsed.
	err string
}

// pendingMessage is a data message whose frames are still arriving.
type pendingMessage struct {
	opcode wsframe.Opcode
	// payload holds the payload as relayed and unmasked, up to the buffer
	// limit; bytes counts all of it.
	payload []byte
	bytes   int64
}

// Write follows p and always reports success: what the other side receives
// must not depend on the capture.
func (d *socketDirection) Write(p []byte) (int, error) {
	d.wire += int64(len(p))
	d.relayed = time.Now()
	rest := p
	for len(rest) > 0 && d.err == "" {
		if d.inFrame {
			rest = d.readPayload(rest)
		} else {
			rest = d.readHeader(rest)
		}
	}
	return len(p), nil
}

func (d *socketDirection) readHeader(p []byte) []byte {
	buffered := len(d.header)
	take := min(len(p), wsframe.MaxHeaderLen-buffered)
	d.header = append(d.header, p[:take]...)
	header, n, err := wsframe.Parse(d.header)
	if errors.Is(err, wsframe.ErrShort) {
		return p[take:]
	}
	if err != nil {
		d.stop(err)
		return nil
	}
	d.header = d.header[:0]
	d.beginFrame(header)
	return p[n-buffered:]
}

func (d *socketDirection) beginFrame(header wsframe.Header) {
	// The capture withholds extension offers from the handshake, so no
	// extension gives the reserved bits a meaning, and the receiving side
	// fails the connection.
	if header.Rsv != 0 {
		d.stop(errors.New("reserved bits set without a negotiated extension"))
		return
	}
	d.frame, d.inFrame, d.offset = header, true, 0
	switch {
	case header.Opcode.IsControl():
		d.control = d.control[:0]
	case header.Opcode == wsframe.OpContinuation:
		if d.message == nil {
			d.stop(errors.New("continuation frame without a message"))
			return
		}
	default:
		// A new message while another is unfinished breaks the protocol, and the
		// receiving side fails the connection; the unfinished one is kept.
		if d.message != nil {
			d.emit(d.message, true)
		}
		d.message = &pendingMessage{opcode: header.Opcode}
	}
	if header.Length == 0 {
		d.endFrame()
	}
}

func (d *socketDirection) readPayload(p []byte) []byte {
	chunk := p[:min(uint64(len(p)), d.frame.Length-d.offset)]
	if d.frame.Opcode.IsControl() {
		start := len(d.control)
		d.control = append(d.control, chunk...)
		d.unmask(d.control[start:])
	} else {
		message := d.message
		message.bytes += int64(len(chunk))
		keep := chunk[:min(len(chunk), max(0, d.bufferLimit()-len(message.payload)))]
		start := len(message.payload)
		message.payload = append(message.payload, keep...)
		d.unmask(message.payload[start:])
	}
	d.offset += uint64(len(chunk))
	if d.offset == d.frame.Length {
		d.endFrame()
	}
	return p[len(chunk):]
}

// bufferLimit is how much of a message payload is held. Beyond the size cap it
// leaves room for a secret value that straddles the cap, so that the value is
// still found and none of it is stored.
func (d *socketDirection) bufferLimit() int {
	return int(d.socket.store.maxBody) + d.socket.store.redactor.Load().longest()
}

func (d *socketDirection) unmask(payload []byte) {
	if d.frame.Masked {
		wsframe.Unmask(d.frame.Mask, payload, d.offset)
	}
}

func (d *socketDirection) endFrame() {
	d.inFrame = false
	switch {
	case d.frame.Opcode.IsControl():
		d.emit(&pendingMessage{opcode: d.frame.Opcode, payload: d.control, bytes: int64(len(d.control))}, false)
	case d.frame.Fin:
		d.emit(d.message, false)
		d.message = nil
	}
}

// stop gives up following the frames, which cannot be found again after a
// header that does not parse. What arrived of an unfinished message is kept.
func (d *socketDirection) stop(err error) {
	d.err = d.from + ": " + err.Error()
	d.flush()
}

// finish stores what is left of a message the connection ended in.
func (d *socketDirection) finish() {
	if d.err == "" {
		d.relayed = time.Now()
		d.flush()
	}
}

func (d *socketDirection) flush() {
	if d.inFrame && d.frame.Opcode.IsControl() {
		d.emit(&pendingMessage{opcode: d.frame.Opcode, payload: d.control, bytes: int64(len(d.control))}, true)
	}
	if d.message != nil {
		d.emit(d.message, true)
	}
	d.inFrame, d.message = false, nil
}

// emit redacts and stores a message. cut marks one that did not
// arrive whole.
func (d *socketDirection) emit(pending *pendingMessage, cut bool) {
	store := d.socket.store
	message := Message{
		Timestamp: d.relayed.UTC().Format(netlog.TimeFormat),
		From:      d.from,
		Type:      messageType(pending.opcode),
		Bytes:     pending.bytes,
	}
	payload := pending.payload
	cut = cut || pending.bytes > int64(len(payload))
	payload = store.redactor.Load().payload(payload, cut)
	if int64(len(payload)) > store.maxBody {
		payload, cut = payload[:store.maxBody], true
	}
	textual := message.Type == MessageText || message.Type == MessageClose
	if cut && textual {
		payload = TrimPartialRune(payload)
	}
	message.StoredBytes = int64(len(payload))
	message.Truncated = cut
	setPayload(&message, payload, textual)
	d.socket.add(message)
}

// setPayload puts the payload into the field its type calls for. Text that is
// not valid UTF-8 is kept as bytes.
func setPayload(message *Message, payload []byte, textual bool) {
	if message.Type == MessageClose && len(payload) >= 2 {
		message.Code = int(binary.BigEndian.Uint16(payload))
		payload = payload[2:]
	}
	if len(payload) == 0 {
		return
	}
	if textual && utf8.Valid(payload) {
		message.Text = string(payload)
		return
	}
	message.Base64 = payload
}

// TrimPartialRune drops an incomplete UTF-8 sequence at the end of data, which
// a cut at a byte cap can leave in text.
func TrimPartialRune(data []byte) []byte {
	for back := 1; back <= utf8.UTFMax && back <= len(data); back++ {
		start := len(data) - back
		if utf8.RuneStart(data[start]) {
			if !utf8.FullRune(data[start:]) {
				return data[:start]
			}
			break
		}
	}
	return data
}

func messageType(opcode wsframe.Opcode) string {
	switch opcode {
	case wsframe.OpText:
		return MessageText
	case wsframe.OpBinary:
		return MessageBinary
	case wsframe.OpClose:
		return MessageClose
	case wsframe.OpPing:
		return MessagePing
	case wsframe.OpPong:
		return MessagePong
	default:
		return fmt.Sprintf("opcode-%#x", byte(opcode))
	}
}
