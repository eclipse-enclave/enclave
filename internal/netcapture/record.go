// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package netcapture defines the full HTTP capture store of
// `--network-log=full`: the record format, the gateway-side writer that tees
// request and response bodies and WebSocket messages to disk, and the
// host-side pruning and lookup. Bodies and messages are kept out of the network
// log, whose events only point at a capture through their capture_id.
package netcapture

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

// Record is the <id>.json file of one captured exchange. Field names and their
// JSON tags are a consumer-facing contract (`enclave network log
// --show-capture <id> --json` emits the file verbatim); only add fields. Keys
// are camelCase like the other schemaVersion envelopes; ts keeps the network
// log's name for the same timestamp.
type Record struct {
	SchemaVersion string `json:"schemaVersion"`
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	Session       string `json:"session,omitempty"`
	// Timestamp is when the proxy received the request, in the network log's
	// timestamp layout.
	Timestamp  string    `json:"ts"`
	DurationMS int64     `json:"durationMs"`
	Request    Request   `json:"request"`
	Response   *Response `json:"response,omitempty"`
	// Error is set when the request could not be relayed, for example because
	// the upstream was unreachable.
	Error string `json:"error,omitempty"`
	// WebSocket describes the messages of a connection the response switched
	// to WebSocket.
	WebSocket *WebSocket `json:"websocket,omitempty"`
}

// Request is the request as the client sent it, before the gateway released
// any secret into its headers.
type Request struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Proto   string      `json:"proto,omitempty"`
	Headers http.Header `json:"headers"`
	Body    *Body       `json:"body,omitempty"`
	// BodyOmitted says why the body was forwarded but not stored.
	BodyOmitted string `json:"bodyOmitted,omitempty"`
}

// Response is the upstream response as the proxy relayed it to the client.
type Response struct {
	Status  int         `json:"status"`
	Proto   string      `json:"proto,omitempty"`
	Headers http.Header `json:"headers"`
	Body    *Body       `json:"body,omitempty"`
	// BodyOmitted says why the body was relayed but not stored.
	BodyOmitted string `json:"bodyOmitted,omitempty"`
}

// OmittedEncoding is why a body with a content encoding is not stored:
// redaction cannot find a secret inside encoded bytes.
const OmittedEncoding = "unexpected content encoding"

// OmittedExtension is why the messages of a WebSocket that negotiated an
// extension are not stored: the proxy offered none, and an extension such as
// permessage-deflate encodes the payloads redaction has to read.
const OmittedExtension = "unexpected WebSocket extension"

// Body describes a stored message body, which File holds as relayed. Bytes
// counts what crossed the proxy; StoredBytes what File holds, which differs
// when the body was truncated or a secret value was replaced by its
// placeholder.
type Body struct {
	File        string `json:"file,omitempty"`
	Bytes       int64  `json:"bytes"`
	StoredBytes int64  `json:"storedBytes"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// WebSocket describes the messages file of a WebSocket connection, which holds
// one Message per line. The record of a connection still open is the
// <id>.open.json file, with Open set and the counts not filled in yet; the
// <id>.json record replaces it when the connection closes.
type WebSocket struct {
	File string `json:"file,omitempty"`
	Open bool   `json:"open,omitempty"`
	// MessagesOmitted says why the messages were relayed but not stored.
	MessagesOmitted string `json:"messagesOmitted,omitempty"`
	Messages        int64  `json:"messages"`
	StoredMessages  int64  `json:"storedMessages"`
	// Bytes counts what crossed the proxy in both directions after the
	// handshake; StoredBytes is the size of File.
	Bytes       int64 `json:"bytes"`
	StoredBytes int64 `json:"storedBytes"`
	// Truncated is set when not every message is stored whole.
	Truncated bool `json:"truncated,omitempty"`
	// Error says why the frames of a direction could not be followed to the
	// end; its later messages are not captured.
	Error string `json:"error,omitempty"`
}

// Message is one line of a WebSocket messages file. Timestamp is when the
// message's last byte was relayed. Lines keep the order in which each side sent
// its messages; between the two sides they follow the order of storing, which
// may lag the relay, so Timestamp is the order across both. Bytes counts the
// payload as relayed; StoredBytes the payload stored, after truncation and
// redaction. The payload is in Text for text
// messages and close reasons that are valid UTF-8, and in Base64 otherwise.
type Message struct {
	Timestamp   string `json:"ts"`
	From        string `json:"from"`
	Type        string `json:"type"`
	Bytes       int64  `json:"bytes"`
	StoredBytes int64  `json:"storedBytes"`
	Truncated   bool   `json:"truncated,omitempty"`
	Code        int    `json:"code,omitempty"`
	Text        string `json:"text,omitempty"`
	Base64      []byte `json:"base64,omitempty"`
}

// Message senders and types.
const (
	FromClient = "client"
	FromServer = "server"

	MessageText   = "text"
	MessageBinary = "binary"
	MessageClose  = "close"
	MessagePing   = "ping"
	MessagePong   = "pong"
)

const (
	SchemaVersion = "1"
	Kind          = "network-capture"
)

// Size limits. A body or message payload is truncated at MaxBodyBytes, and
// the messages file of a WebSocket stops growing at MaxSocketBytes. A session
// directory keeps its most recent captures within MaxSessionBytes, and the
// host prunes the store of a project and tool to MaxStoreBytes at every session
// start.
const (
	MaxBodyBytes    = 8 * 1024 * 1024
	MaxSocketBytes  = 128 * 1024 * 1024
	MaxSessionBytes = 512 * 1024 * 1024
	MaxStoreBytes   = 1024 * 1024 * 1024
)

const (
	recordSuffix       = ".json"
	openRecordSuffix   = ".open.json"
	requestBodySuffix  = ".request.body"
	responseBodySuffix = ".response.body"
	messagesSuffix     = ".messages.jsonl"
)

// fileSuffixes are the files one capture may consist of, records first.
var fileSuffixes = []string{recordSuffix, openRecordSuffix, requestBodySuffix, responseBodySuffix, messagesSuffix}

const idBytes = 8

var idPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

func newID() (string, error) {
	raw := make([]byte, idBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// ValidID reports whether id has the shape of a capture ID. Lookups check it
// before building a path from user input.
func ValidID(id string) bool {
	return idPattern.MatchString(id)
}

// RecordPath is the record file of a capture in a session directory.
func RecordPath(sessionDir string, id string) string {
	return filepath.Join(sessionDir, id+recordSuffix)
}

// OpenRecordPath is the record file of a WebSocket capture whose connection is
// still open.
func OpenRecordPath(sessionDir string, id string) string {
	return filepath.Join(sessionDir, id+openRecordSuffix)
}

// BodyPath resolves a body's file next to its record. The name comes from the
// record, so only the body file names the store itself writes are accepted.
func BodyPath(recordPath string, body *Body) (string, bool) {
	if body == nil {
		return "", false
	}
	return siblingPath(recordPath, body.File, requestBodySuffix, responseBodySuffix)
}

// MessagesPath resolves the messages file of a WebSocket capture next to its
// record, like BodyPath.
func MessagesPath(recordPath string, socket *WebSocket) (string, bool) {
	if socket == nil {
		return "", false
	}
	return siblingPath(recordPath, socket.File, messagesSuffix)
}

func siblingPath(recordPath string, name string, suffixes ...string) (string, bool) {
	if id, suffix := captureID(name); id == "" || !slices.Contains(suffixes, suffix) {
		return "", false
	}
	return filepath.Join(filepath.Dir(recordPath), name), true
}

// maxRecordBytes bounds a record read back from disk. Headers are its only
// large part, and Go caps them at 1 MiB per request and 10 MiB per response.
const maxRecordBytes = 64 << 20

// ReadRecord reads a record file as stored.
func ReadRecord(path string) ([]byte, error) {
	return readCaptureFile(path, maxRecordBytes)
}

// ReadBody reads the stored body a record describes and returns its path. The
// second return is empty when nothing was stored.
func ReadBody(recordPath string, body *Body) (string, []byte, error) {
	path, ok := BodyPath(recordPath, body)
	if !ok {
		return "", nil, nil
	}
	data, err := readCaptureFile(path, MaxBodyBytes)
	return path, data, err
}

// ReadMessages reads the messages file a record describes and returns its
// path. A last line without its newline is still being written and is left
// out. The second return is empty when nothing was stored.
func ReadMessages(recordPath string, socket *WebSocket) (string, []Message, error) {
	path, ok := MessagesPath(recordPath, socket)
	if !ok {
		return "", nil, nil
	}
	data, err := readCaptureFile(path, MaxSocketBytes)
	if err != nil {
		return path, nil, err
	}
	var messages []Message
	for number := 1; ; number++ {
		line, rest, complete := bytes.Cut(data, []byte{'\n'})
		if !complete {
			return path, messages, nil
		}
		data = rest
		var message Message
		if err := json.Unmarshal(line, &message); err != nil {
			return path, messages, fmt.Errorf("parse %s line %d: %w", path, number, err)
		}
		messages = append(messages, message)
	}
}

// readCaptureFile reads a file from a session directory. The gateway can write
// there, so a name only says what the store would have written: a symlink, a
// FIFO or anything else but a regular file is refused rather than followed, and
// a file larger than the store writes is not read. The file is checked after it
// is opened as well as before, so swapping it in between is caught, and it is
// opened without blocking, so a FIFO swapped in cannot stall the reader.
func readCaptureFile(path string, limit int64) ([]byte, error) {
	linked, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !linked.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- path is resolved inside enclave's own state directory.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(linked, opened) {
		return nil, fmt.Errorf("%s changed while it was opened", path)
	}
	if opened.Size() > limit {
		return nil, fmt.Errorf("%s is larger than a capture file can be", path)
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

// Load reads a record file.
func Load(path string) (Record, error) {
	raw, err := ReadRecord(path)
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return record, nil
}

// captureID returns the ID a capture file belongs to, or "" for anything else
// in a session directory, such as a staging file.
func captureID(name string) (string, string) {
	for _, suffix := range fileSuffixes {
		if id, ok := strings.CutSuffix(name, suffix); ok && ValidID(id) {
			return id, suffix
		}
	}
	return "", ""
}
