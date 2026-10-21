// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netcapture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"enclave/internal/netlog"
	"enclave/internal/util"
)

// Options configures a Store.
type Options struct {
	// Dir is the session directory. Empty disables capturing.
	Dir     string
	Session string
	// Secrets are redacted from everything the store writes.
	Secrets []Secret
	// MaxBodyBytes and MaxBytes default to MaxBodyBytes and MaxSessionBytes.
	MaxBodyBytes int64
	MaxBytes     int64
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Store writes the captures of one gateway session. Write errors never reach
// the caller: capturing must not break the request path, so a capture that
// cannot be written is simply missing, and its event carries no capture_id.
type Store struct {
	dir      string
	session  string
	redactor *redactor
	maxBody  int64
	maxBytes int64
	now      func() time.Time

	mu sync.Mutex
	// used is what the directory holds: finished captures plus the bodies of the
	// ones still in flight.
	used int64
	// finished is ordered oldest first, which is the order they are evicted in.
	finished []storedCapture
}

type storedCapture struct {
	id    string
	bytes int64
}

// Open prepares the store for a session directory the host created. A nil
// Store with a nil error means capturing is off; every method of a nil Store
// and of the nil Capture it hands out is a no-op.
//
// Captures already in the directory, left by an earlier run of the session or
// by the proxy before a policy reload restarted it, count against the session
// budget. Body files without a record belong to a capture that never finished
// and are removed: only this process writes the directory.
func Open(opts Options) (*Store, error) {
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		return nil, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("capture path is not a directory: %s", dir)
	}
	s := &Store{
		dir:      dir,
		session:  opts.Session,
		redactor: newRedactor(opts.Secrets),
		maxBody:  opts.MaxBodyBytes,
		maxBytes: opts.MaxBytes,
		now:      opts.Now,
	}
	if s.maxBody <= 0 {
		s.maxBody = MaxBodyBytes
	}
	if s.maxBytes <= 0 {
		s.maxBytes = MaxSessionBytes
	}
	if s.now == nil {
		s.now = time.Now
	}

	existing, err := scanSessionDir(dir, func(time.Time) bool { return true })
	if err != nil {
		return nil, err
	}
	for _, capture := range existing {
		s.finished = append(s.finished, storedCapture{id: capture.id, bytes: capture.bytes})
		s.used += capture.bytes
	}
	for _, id := range s.overBudget(0) {
		removeCapture(dir, id)
	}
	return s, nil
}

// Capture is one exchange being captured.
type Capture struct {
	store    *Store
	id       string
	started  time.Time
	record   Record
	request  *bodySink
	response *bodySink
	// requestDecoder and responseDecoder sit in front of the sinks when a body
	// carries an encoding the store decodes; nil otherwise.
	requestDecoder  *decodingSink
	responseDecoder *decodingSink
	// requestBody is the forwarded request body, if there was one.
	requestBody *teeBody
}

// Begin snapshots a request the proxy is about to relay. It must run before
// the gateway releases secrets into the headers, so the capture records the
// placeholders the sandbox sent.
func (s *Store) Begin(req *http.Request, scheme string) *Capture {
	if s == nil || req == nil {
		return nil
	}
	id, err := newID()
	if err != nil {
		return nil
	}
	started := s.now()
	capture := &Capture{
		store:   s,
		id:      id,
		started: started,
		record: Record{
			SchemaVersion: SchemaVersion,
			Kind:          Kind,
			ID:            id,
			Session:       s.session,
			Timestamp:     started.UTC().Format(netlog.TimeFormat),
			Request: Request{
				Method:  req.Method,
				URL:     s.redactor.string(requestURL(req, scheme)),
				Proto:   req.Proto,
				Headers: s.redactor.header(req.Header),
			},
		},
	}
	capture.request = s.newBodySink(id + requestBodySuffix)
	capture.response = s.newBodySink(id + responseBodySuffix)
	if encoding := decodableEncoding(req.Header); encoding != "" {
		capture.requestDecoder = newDecodingSink(capture.request, encoding)
	}
	return capture
}

func requestURL(req *http.Request, scheme string) string {
	host := req.Host
	if host == "" && req.URL != nil {
		host = req.URL.Host
	}
	target := "/"
	if req.URL != nil {
		target = req.URL.RequestURI()
	}
	return scheme + "://" + host + target
}

// RequestBody wraps the body the proxy forwards so that what the upstream
// reads is captured as it goes.
func (c *Capture) RequestBody(body io.ReadCloser) io.ReadCloser {
	if c == nil || body == nil || body == http.NoBody {
		return body
	}
	var sink io.Writer = c.request
	if c.requestDecoder != nil {
		sink = c.requestDecoder
	}
	c.requestBody = &teeBody{ReadCloser: body, sink: sink}
	return c.requestBody
}

// Response snapshots the head of the upstream response.
func (c *Capture) Response(resp *http.Response) {
	if c == nil || resp == nil {
		return
	}
	c.record.Response = &Response{
		Status:  resp.StatusCode,
		Proto:   resp.Proto,
		Headers: c.store.redactor.header(resp.Header),
	}
	if encoding := decodableEncoding(resp.Header); encoding != "" {
		c.responseDecoder = newDecodingSink(c.response, encoding)
	}
}

// ResponseBody returns the writer the relayed response body is copied to, or
// nil when there is no capture.
func (c *Capture) ResponseBody() io.Writer {
	if c == nil {
		return nil
	}
	if c.responseDecoder != nil {
		return c.responseDecoder
	}
	return c.response
}

// ResponseEnded reports how relaying the response body ended; an error marks
// the captured body as incomplete.
func (c *Capture) ResponseEnded(err error) {
	if c == nil || err == nil {
		return
	}
	c.response.markIncomplete()
}

// Finish writes the record and returns the capture ID, or "" when nothing was
// written. relayErr is the reason the exchange failed, if it did.
func (c *Capture) Finish(relayErr error) string {
	if c == nil {
		return ""
	}
	s := c.store
	c.record.DurationMS = s.now().Sub(c.started).Milliseconds()
	// An upstream may answer before it has read the whole request body, and the
	// transport can go on sending it after the response is relayed. What it
	// sends from here on is not recorded, so the body is marked incomplete.
	if c.requestBody != nil && !c.requestBody.complete() {
		c.request.markIncomplete()
	}
	c.record.Request.Body = closeBody(c.request, c.requestDecoder)
	if body := closeBody(c.response, c.responseDecoder); c.record.Response != nil {
		c.record.Response.Body = body
	}
	if relayErr != nil {
		c.record.Error = s.redactor.string(relayErr.Error())
	}
	bodies := c.request.stored + c.response.stored

	payload, err := json.MarshalIndent(c.record, "", "  ")
	if err == nil {
		payload = append(payload, '\n')
		// The record lands as one rename so a reader never sees it partial. It
		// skips the fsync because Finish runs on the relay path and a capture
		// lost in a crash is acceptable.
		err = util.WriteFileAtomicNoSync(RecordPath(s.dir, c.id), payload, 0o644)
	}
	if err != nil {
		c.request.remove()
		c.response.remove()
		s.release(bodies)
		return ""
	}
	s.finish(storedCapture{id: c.id, bytes: bodies + int64(len(payload))}, bodies)
	return c.id
}

// closeBody ends a body and describes it. A decoded body is stored decoded but
// counted as it crossed the proxy, and is incomplete if it did not decode to
// its end.
func closeBody(sink *bodySink, decoder *decodingSink) *Body {
	if decoder == nil {
		return sink.close()
	}
	wire, failed := decoder.finish()
	body := sink.close()
	if wire == 0 {
		return body
	}
	if body == nil {
		body = &Body{}
	}
	body.Bytes = wire
	body.DecodedFrom = decoder.encoding
	body.Truncated = body.Truncated || failed
	return body
}

func (s *Store) newBodySink(name string) *bodySink {
	return &bodySink{owner: s, path: filepath.Join(s.dir, name), redact: s.redactor.stream()}
}

// reserve books room in the session budget for up to n body bytes and returns
// how many may be written. Finished captures are evicted, oldest first, to make
// room; when the bodies still in flight fill the budget on their own, nothing
// more is granted and the caller stops storing its body.
func (s *Store) reserve(n int64) int64 {
	s.mu.Lock()
	var evicted []string
	for s.used+n > s.maxBytes && len(s.finished) > 0 {
		evicted = append(evicted, s.evictOldest())
	}
	granted := min(n, max(0, s.maxBytes-s.used))
	s.used += granted
	s.mu.Unlock()
	for _, id := range evicted {
		removeCapture(s.dir, id)
	}
	return granted
}

func (s *Store) release(bytes int64) {
	s.mu.Lock()
	s.used -= bytes
	s.mu.Unlock()
}

// finish books a written capture. Its body bytes are already counted, as they
// were written; the record is what it adds. The capture itself is kept even
// when bodies in flight fill the budget, so the ID handed to the network log
// names a capture that exists.
//
// The files of evicted captures are deleted after the lock is released, so
// that bodies still being written never wait for the deletions.
func (s *Store) finish(capture storedCapture, counted int64) {
	s.mu.Lock()
	s.used += capture.bytes - counted
	s.finished = append(s.finished, capture)
	evicted := s.overBudget(1)
	s.mu.Unlock()
	for _, id := range evicted {
		removeCapture(s.dir, id)
	}
}

// overBudget drops the oldest finished captures from the books until the
// directory fits the budget again, or only keep of them are left, and returns
// their IDs for deletion. Captures still in flight are never evicted. The
// caller holds s.mu, or has the store to itself.
func (s *Store) overBudget(keep int) []string {
	var evicted []string
	for s.used > s.maxBytes && len(s.finished) > keep {
		evicted = append(evicted, s.evictOldest())
	}
	return evicted
}

// evictOldest drops the oldest finished capture from the books and returns its
// ID. The caller holds s.mu and deletes the files.
func (s *Store) evictOldest() string {
	oldest := s.finished[0]
	s.finished = s.finished[1:]
	s.used -= oldest.bytes
	return oldest.id
}

// removeCapture deletes the record first, so a reader never finds a record
// whose bodies are already gone without being able to tell.
func removeCapture(dir string, id string) {
	for _, suffix := range fileSuffixes {
		_ = os.Remove(filepath.Join(dir, id+suffix))
	}
}

// bodySink stores one body, up to the size cap and as far as the session
// budget allows, and counts all of it. The transport reads the request body on
// its own goroutine, so a sink is locked.
type bodySink struct {
	owner  *Store
	path   string
	redact *streamRedactor

	mu        sync.Mutex
	file      *os.File
	seen      int64
	stored    int64
	truncated bool
	failed    bool
	closed    bool
	// incomplete is set when the message itself was cut short: the relay
	// failed midway, or the upstream answered before the transport had read the
	// whole request body.
	incomplete bool
}

// Write records p and always reports success: the client's copy of the body
// must not depend on the capture.
func (b *bodySink) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return len(p), nil
	}
	b.seen += int64(len(p))
	if b.truncated || b.failed {
		return len(p), nil
	}
	out := p
	if b.redact != nil {
		out = b.redact.feed(p)
	}
	b.persist(out)
	return len(p), nil
}

func (b *bodySink) persist(out []byte) {
	if room := b.owner.maxBody - b.stored; int64(len(out)) > room {
		out = out[:room]
		b.truncated = true
	}
	if len(out) == 0 {
		return
	}
	if granted := b.owner.reserve(int64(len(out))); granted < int64(len(out)) {
		out = out[:granted]
		b.truncated = true
		if granted == 0 {
			return
		}
	}
	if b.file == nil {
		// Readable by the host user, who is another UID than the gateway's proxy
		// user under rootful Docker; the host keeps the capture root private.
		file, err := os.OpenFile(b.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644) // #nosec G302 G304 -- path is built from a generated ID inside the session directory.
		if err != nil {
			b.owner.release(int64(len(out)))
			b.failed = true
			return
		}
		b.file = file
	}
	written, err := b.file.Write(out)
	b.stored += int64(written)
	if unused := int64(len(out) - written); unused > 0 {
		b.owner.release(unused)
	}
	if err != nil {
		b.failed = true
	}
}

// close ends the body and describes it for the record, or returns nil when
// there was none.
func (b *bodySink) close() *Body {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		if b.redact != nil && !b.truncated && !b.failed {
			b.persist(b.redact.flush())
		}
		if b.file != nil {
			if err := b.file.Close(); err != nil {
				b.failed = true
			}
		}
	}
	if b.seen == 0 && !b.incomplete {
		return nil
	}
	body := &Body{
		Bytes:       b.seen,
		StoredBytes: b.stored,
		Truncated:   b.truncated || b.failed || b.incomplete,
	}
	if b.stored > 0 {
		body.File = filepath.Base(b.path)
	}
	return body
}

// full reports whether the sink stores nothing more, so a decoder feeding it
// can stop.
func (b *bodySink) full() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated || b.failed || b.closed
}

// markIncomplete flags a message that did not cross the proxy whole.
func (b *bodySink) markIncomplete() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.incomplete = true
	}
}

func (b *bodySink) remove() {
	if b.stored > 0 {
		_ = os.Remove(b.path)
	}
}

type teeBody struct {
	io.ReadCloser
	sink io.Writer

	mu  sync.Mutex
	eof bool
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 {
		_, _ = t.sink.Write(p[:n])
	}
	if errors.Is(err, io.EOF) {
		t.mu.Lock()
		t.eof = true
		t.mu.Unlock()
	}
	return n, err
}

func (t *teeBody) complete() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.eof
}

// sessionCapture is a finished capture found on disk.
type sessionCapture struct {
	id       string
	bytes    int64
	modified time.Time
}

// scanSessionDir lists the finished captures of a session directory, oldest
// first. It also deletes leftovers for which abandoned reports true: body files
// without a record and staging files, judged by when they were last written.
// The directory's own writer passes a predicate that is always true, since
// nothing else writes there; anyone else must spare recent leftovers, which
// belong to captures still in flight.
func scanSessionDir(dir string, abandoned func(modified time.Time) bool) ([]sessionCapture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type files struct {
		record bool
		// modified is the record's time for a finished capture, and the latest
		// body write otherwise.
		modified time.Time
		bytes    int64
		bodies   []string
	}
	byID := map[string]*files{}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if strings.HasPrefix(name, ".") {
			if abandoned(info.ModTime()) {
				_ = os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		id, suffix := captureID(name)
		if id == "" {
			continue
		}
		group := byID[id]
		if group == nil {
			group = &files{}
			byID[id] = group
		}
		group.bytes += info.Size()
		switch suffix {
		case recordSuffix:
			group.record = true
			group.modified = info.ModTime()
		default:
			group.bodies = append(group.bodies, name)
			if !group.record && info.ModTime().After(group.modified) {
				group.modified = info.ModTime()
			}
		}
	}

	for id, group := range byID {
		if !group.record && abandoned(group.modified) {
			for _, name := range group.bodies {
				_ = os.Remove(filepath.Join(dir, name))
			}
			delete(byID, id)
		}
	}
	captures := make([]sessionCapture, 0, len(byID))
	for id, group := range byID {
		if !group.record {
			continue
		}
		captures = append(captures, sessionCapture{id: id, bytes: group.bytes, modified: group.modified})
	}
	sort.Slice(captures, func(i, j int) bool {
		if !captures[i].modified.Equal(captures[j].modified) {
			return captures[i].modified.Before(captures[j].modified)
		}
		return captures[i].id < captures[j].id
	})
	return captures, nil
}
