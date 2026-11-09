// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netcapture

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"enclave/internal/gateway/wsframe"
	"enclave/internal/netlog"
)

const permessageDeflate = "permessage-deflate"

// socketCapture follows the frames of a WebSocket connection in both
// directions and appends each message to the messages file as one line. Each
// direction is fed by the goroutine relaying it, so the two only share the
// file and its counts.
type socketCapture struct {
	store *Store
	path  string
	// compression is the extension the handshake response accepted and the
	// capture decodes, or "".
	compression    string
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

// newSocket creates the messages file of a WebSocket connection whose
// handshake response carried header, or returns nil when it cannot.
func (s *Store) newSocket(id string, header http.Header) *socketCapture {
	path := filepath.Join(s.dir, id+messagesSuffix)
	// Readable by the host user; see bodySink.persist.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o644) // #nosec G302 G304 -- path is built from a generated ID inside the session directory.
	if err != nil {
		return nil
	}
	socket := &socketCapture{store: s, path: path, file: file}
	deflate, other := negotiatedExtensions(header)
	// Another extension may give the reserved bits a meaning the capture does
	// not know, so messages are decoded only when permessage-deflate is the
	// only one.
	if deflate != nil && !other {
		socket.compression = permessageDeflate
	}
	socket.client = &socketDirection{socket: socket, from: FromClient}
	socket.server = &socketDirection{socket: socket, from: FromServer}
	if socket.compression != "" {
		socket.client.inflater = &inflater{takeover: !deflate.clientNoContextTakeover}
		socket.server.inflater = &inflater{takeover: !deflate.serverNoContextTakeover}
	}
	return socket
}

type deflateParams struct {
	serverNoContextTakeover bool
	clientNoContextTakeover bool
}

// negotiatedExtensions reads the extensions a handshake response accepted:
// the permessage-deflate parameters, if it was accepted once, and whether
// anything else was. The window size parameters do not matter for decoding,
// which always keeps the largest window.
func negotiatedExtensions(header http.Header) (*deflateParams, bool) {
	var deflate *deflateParams
	other := false
	for _, value := range header.Values("Sec-WebSocket-Extensions") {
		for extension := range strings.SplitSeq(value, ",") {
			name, params, _ := strings.Cut(extension, ";")
			switch name = strings.ToLower(strings.TrimSpace(name)); {
			case name == "":
			case name != permessageDeflate || deflate != nil:
				other = true
			default:
				deflate = &deflateParams{}
				for param := range strings.SplitSeq(params, ";") {
					key, _, _ := strings.Cut(param, "=")
					switch strings.ToLower(strings.TrimSpace(key)) {
					case "server_no_context_takeover":
						deflate.serverNoContextTakeover = true
					case "client_no_context_takeover":
						deflate.clientNoContextTakeover = true
					}
				}
			}
		}
	}
	return deflate, other
}

// add stores a finished message, unless the socket is full.
func (s *socketCapture) add(message Message, cut bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages++
	if cut {
		s.truncated = true
	}
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
		Compression:    s.compression,
		Messages:       s.messages,
		StoredMessages: s.storedMessages,
		Bytes:          s.client.wire + s.server.wire,
		StoredBytes:    s.stored,
		Truncated:      s.truncated,
		Error:          strings.Join(nonEmpty(s.client.err, s.server.err), "; "),
	}
	if s.stored > 0 {
		socket.File = filepath.Base(s.path)
	}
	return socket
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
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
	// inflater decodes compressed messages; nil without permessage-deflate.
	inflater *inflater
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
	rsv    byte
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
		d.message = &pendingMessage{opcode: header.Opcode, rsv: header.Rsv}
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
	return int(d.socket.store.maxBody) + d.socket.store.redactor.longest()
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

// emit decodes, redacts and stores a message. cut marks one that did not
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
	switch {
	case pending.rsv == 0:
	case pending.rsv == wsframe.RSV1 && d.inflater != nil:
		decoded, more, ok := d.inflater.inflate(payload, !cut, d.bufferLimit())
		if ok {
			payload, cut = decoded, cut || more
			message.DecodedFrom = permessageDeflate
		} else {
			message.Compressed = true
		}
	default:
		message.Compressed = true
	}
	payload = store.redactor.payload(payload, cut)
	if int64(len(payload)) > store.maxBody {
		payload, cut = payload[:store.maxBody], true
	}
	textual := (message.Type == MessageText || message.Type == MessageClose) && !message.Compressed
	if cut && textual {
		payload = TrimPartialRune(payload)
	}
	message.StoredBytes = int64(len(payload))
	message.Truncated = cut
	setPayload(&message, payload, textual)
	d.socket.add(message, cut)
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

// windowSize is the DEFLATE window: how far back a compressed message can
// refer, into the messages before it when the context is taken over.
const windowSize = 32 << 10

// maxInflate bounds the decoding of one message. Past the size cap a message
// is decoded only to keep the window right for the messages after it.
const maxInflate = 8 * MaxBodyBytes

// inflater decodes the permessage-deflate messages of one direction (RFC
// 7692). With context takeover the direction is one DEFLATE stream whose
// messages refer back into earlier ones. A decompressor cannot resume once its
// input has run out, so each message is decoded by a fresh one primed with
// the last window of decoded output.
type inflater struct {
	takeover bool
	window   []byte
	// lost is set when the window is no longer known, so later messages cannot
	// be decoded.
	lost   bool
	reader io.ReadCloser
	buffer []byte
}

// inflate decodes a message as far as limit, and reports whether it decoded to
// more than that and whether decoding worked. complete is false for a message
// that did not arrive whole.
func (f *inflater) inflate(payload []byte, complete bool, limit int) ([]byte, bool, bool) {
	if f.lost {
		return nil, false, false
	}
	// The sender strips the empty block that ends each message.
	input := bytes.NewReader(append(payload[:len(payload):len(payload)], 0x00, 0x00, 0xff, 0xff))
	var dictionary []byte
	if f.takeover {
		dictionary = f.window
	}
	if f.reader == nil {
		f.reader = flate.NewReaderDict(input, dictionary)
		f.buffer = make([]byte, windowSize)
	} else if err := f.reader.(flate.Resetter).Reset(input, dictionary); err != nil {
		f.lost = f.takeover
		return nil, false, false
	}
	var out []byte
	window := f.window
	total := 0
	var err error
	for {
		var n int
		n, err = f.reader.Read(f.buffer)
		total += n
		if room := limit - len(out); room > 0 {
			out = append(out, f.buffer[:min(n, room)]...)
		}
		if f.takeover {
			window = appendWindow(window, f.buffer[:n])
		} else if total > limit {
			break
		}
		if err != nil || total > maxInflate {
			break
		}
	}
	// Without a closing block the decompressor runs out of input after the
	// message, which is how a message normally ends.
	failed := err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)
	if f.takeover {
		f.window = window
		f.lost = failed || !complete || total > maxInflate
	}
	if failed {
		return nil, false, false
	}
	return out, total > len(out), true
}

// appendWindow keeps the last windowSize bytes of window followed by data.
func appendWindow(window []byte, data []byte) []byte {
	if len(data) >= windowSize {
		return append(window[:0], data[len(data)-windowSize:]...)
	}
	window = append(window, data...)
	if excess := len(window) - windowSize; excess > 0 {
		window = append(window[:0], window[excess:]...)
	}
	return window
}
