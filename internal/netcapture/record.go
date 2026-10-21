// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package netcapture defines the full HTTP capture store of
// `--network-log=full`: the record format, the gateway-side writer that tees
// request and response bodies to disk, and the host-side pruning and lookup.
// Bodies are kept out of the network log, whose events only point at a capture
// through their capture_id.
package netcapture

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
}

// Request is the request as the client sent it, before the gateway released
// any secret into its headers.
type Request struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Proto   string      `json:"proto,omitempty"`
	Headers http.Header `json:"headers"`
	Body    *Body       `json:"body,omitempty"`
}

// Response is the upstream response as the proxy relayed it to the client.
type Response struct {
	Status  int         `json:"status"`
	Proto   string      `json:"proto,omitempty"`
	Headers http.Header `json:"headers"`
	Body    *Body       `json:"body,omitempty"`
}

// Body describes a stored message body. Bytes counts what crossed the proxy;
// StoredBytes what File holds, which differs when the body was truncated or
// decoded, or a secret value was replaced by its placeholder. DecodedFrom
// names the content encoding File was decoded from; without it, File holds the
// body as relayed.
type Body struct {
	File        string `json:"file,omitempty"`
	Bytes       int64  `json:"bytes"`
	StoredBytes int64  `json:"storedBytes"`
	Truncated   bool   `json:"truncated,omitempty"`
	DecodedFrom string `json:"decodedFrom,omitempty"`
}

const (
	SchemaVersion = "1"
	Kind          = "network-capture"
)

// Size limits. A body is truncated at MaxBodyBytes. A session directory keeps
// its most recent captures within MaxSessionBytes, and the host prunes the
// store of a project and tool to MaxStoreBytes at every session start.
const (
	MaxBodyBytes    = 8 * 1024 * 1024
	MaxSessionBytes = 512 * 1024 * 1024
	MaxStoreBytes   = 1024 * 1024 * 1024
)

const (
	recordSuffix       = ".json"
	requestBodySuffix  = ".request.body"
	responseBodySuffix = ".response.body"
)

// fileSuffixes are the files one capture may consist of, record first.
var fileSuffixes = []string{recordSuffix, requestBodySuffix, responseBodySuffix}

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

// SessionDir is the directory one session's captures are written to. Sessions
// are named by their container, which is a safe single path element.
func SessionDir(root string, session string) string {
	return filepath.Join(root, session)
}

// RecordPath is the record file of a capture in a session directory.
func RecordPath(sessionDir string, id string) string {
	return filepath.Join(sessionDir, id+recordSuffix)
}

// BodyPath resolves a body's file next to its record. The name comes from the
// record, so only the body file names the store itself writes are accepted.
func BodyPath(recordPath string, body *Body) (string, bool) {
	if body == nil {
		return "", false
	}
	if id, suffix := captureID(body.File); id == "" || suffix == recordSuffix {
		return "", false
	}
	return filepath.Join(filepath.Dir(recordPath), body.File), true
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
