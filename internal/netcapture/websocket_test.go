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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

var clientMask = [4]byte{0x12, 0x34, 0x56, 0x78}

// wsFrame builds a frame. first is the first header byte: FIN, the reserved
// bits and the opcode. A mask masks the payload as a client must.
func wsFrame(first byte, payload []byte, mask *[4]byte) []byte {
	second := byte(0)
	if mask != nil {
		second = 0x80
	}
	var out []byte
	switch {
	case len(payload) < 126:
		out = []byte{first, second | byte(len(payload))}
	case len(payload) <= 0xffff:
		out = binary.BigEndian.AppendUint16([]byte{first, second | 126}, uint16(len(payload)))
	default:
		out = binary.BigEndian.AppendUint64([]byte{first, second | 127}, uint64(len(payload)))
	}
	if mask == nil {
		return append(out, payload...)
	}
	out = append(out, mask[:]...)
	for i, b := range payload {
		out = append(out, b^mask[i%4])
	}
	return out
}

func clientText(text string) []byte { return wsFrame(0x81, []byte(text), &clientMask) }
func serverText(text string) []byte { return wsFrame(0x81, []byte(text), nil) }

// feed writes data in pieces of size piece, as a relay copying in chunks would.
func feed(w io.Writer, piece int, frames ...[]byte) {
	data := bytes.Join(frames, nil)
	for len(data) > 0 {
		n := min(piece, len(data))
		_, _ = w.Write(data[:n])
		data = data[n:]
	}
}

// beginSocket captures a WebSocket handshake whose response accepted
// extensions, and returns the capture and the writers for each direction.
func beginSocket(t *testing.T, store *Store, extensions string) (*Capture, io.Writer, io.Writer) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/responses", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	capture := store.Begin(req, "https")
	resp := &http.Response{StatusCode: http.StatusSwitchingProtocols, Proto: "HTTP/1.1", Header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}}
	if extensions != "" {
		resp.Header.Set("Sec-WebSocket-Extensions", extensions)
	}
	capture.Response(resp)
	client, server := capture.WebSocket(resp)
	if client == nil || server == nil {
		t.Fatal("WebSocket() did not capture the connection")
	}
	return capture, client, server
}

// finishSocket ends the capture and returns its record and messages.
func finishSocket(t *testing.T, store *Store, capture *Capture) (Record, []Message) {
	t.Helper()
	id := capture.Finish(nil)
	if id == "" {
		t.Fatal("Finish() returned no ID")
	}
	recordPath := RecordPath(store.dir, id)
	record, err := Load(recordPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if record.WebSocket == nil {
		t.Fatal("record has no websocket field")
	}
	if _, err := os.Stat(OpenRecordPath(store.dir, id)); !os.IsNotExist(err) {
		t.Fatalf("open record still present after the connection closed: %v", err)
	}
	_, messages, err := ReadMessages(recordPath, record.WebSocket)
	if err != nil {
		t.Fatalf("ReadMessages() error = %v", err)
	}
	return record, messages
}

func describe(messages []Message) []string {
	var out []string
	for _, m := range messages {
		out = append(out, m.From+" "+m.Type+" "+m.Text+string(m.Base64))
	}
	return out
}

func assertMessages(t *testing.T, messages []Message, want ...string) {
	t.Helper()
	if got := describe(messages); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWebSocketCaptureStoresMessagesInOrder(t *testing.T) {
	store := openTestStore(t, Options{})
	capture, client, server := beginSocket(t, store, "")

	open, err := Load(OpenRecordPath(store.dir, capture.id))
	if err != nil {
		t.Fatalf("no open record while the connection is open: %v", err)
	}
	if open.WebSocket == nil || !open.WebSocket.Open || open.WebSocket.File != capture.id+messagesSuffix {
		t.Fatalf("open record websocket = %+v", open.WebSocket)
	}

	// One byte at a time splits every header and payload, masking key included.
	feed(client, 1, clientText(`{"type":"response.create","input":"<b>&</b>"}`))
	feed(server, 1, serverText(`{"type":"response.created"}`), wsFrame(0x82, []byte{0, 1, 2}, nil), wsFrame(0x89, []byte("hb"), nil))
	feed(client, 1, wsFrame(0x8a, []byte("hb"), &clientMask), wsFrame(0x88, append([]byte{0x03, 0xe8}, "bye"...), &clientMask))

	record, messages := finishSocket(t, store, capture)
	assertMessages(t, messages,
		`client text {"type":"response.create","input":"<b>&</b>"}`,
		`server text {"type":"response.created"}`,
		"server binary \x00\x01\x02",
		"server ping hb",
		"client pong hb",
		"client close bye",
	)
	if messages[5].Code != 1000 || messages[0].Bytes != int64(len(`{"type":"response.create","input":"<b>&</b>"}`)) || messages[0].Timestamp == "" {
		t.Fatalf("message details = %+v", messages)
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, record.WebSocket.File)) // #nosec G304 -- test-owned temp path.
	if err != nil || !bytes.Contains(raw, []byte(`<b>&</b>`)) {
		t.Fatalf("messages file escapes markup: %s", raw)
	}
	socket := record.WebSocket
	if socket.Open || socket.Messages != 6 || socket.StoredMessages != 6 || socket.Truncated || socket.Error != "" {
		t.Fatalf("record websocket = %+v", socket)
	}
	info, err := os.Stat(filepath.Join(store.dir, socket.File))
	if err != nil || info.Size() != socket.StoredBytes {
		t.Fatalf("messages file = %v, %v; record says %d bytes", info, err, socket.StoredBytes)
	}
}

func TestWebSocketCaptureReassemblesFragmentedMessages(t *testing.T) {
	store := openTestStore(t, Options{})
	capture, client, server := beginSocket(t, store, "")

	feed(client, 3,
		wsFrame(0x01, []byte("Hello, "), &clientMask),
		wsFrame(0x89, []byte("in between"), &clientMask),
		wsFrame(0x00, []byte("fragmented "), &clientMask),
		wsFrame(0x80, []byte("world"), &clientMask),
	)
	feed(server, 5,
		wsFrame(0x02, []byte("ab"), nil),
		wsFrame(0x8a, nil, nil),
		wsFrame(0x80, []byte("cd"), nil),
	)

	_, messages := finishSocket(t, store, capture)
	assertMessages(t, messages,
		"client ping in between",
		"client text Hello, fragmented world",
		"server pong ",
		"server binary abcd",
	)
	if messages[1].Bytes != int64(len("Hello, fragmented world")) || messages[1].Truncated {
		t.Fatalf("reassembled message = %+v", messages[1])
	}
}

// deflater compresses the messages of one direction as permessage-deflate
// does: each message ends in a sync flush whose empty block is stripped.
type deflater struct {
	takeover bool
	buffer   bytes.Buffer
	writer   *flate.Writer
}

func (d *deflater) compress(t *testing.T, message string) []byte {
	t.Helper()
	if d.writer == nil || !d.takeover {
		writer, err := flate.NewWriter(&d.buffer, flate.BestCompression)
		if err != nil {
			t.Fatal(err)
		}
		d.writer = writer
	}
	_, _ = d.writer.Write([]byte(message))
	_ = d.writer.Flush()
	out := bytes.TrimSuffix(d.buffer.Bytes(), []byte{0x00, 0x00, 0xff, 0xff})
	out = append([]byte{}, out...)
	d.buffer.Reset()
	return out
}

func TestWebSocketCaptureDecodesPermessageDeflate(t *testing.T) {
	turn := strings.Repeat(`{"role":"user","content":"edit README.md and keep the license header"}`, 20)
	for name, tc := range map[string]struct {
		extensions string
		takeover   bool
	}{
		"with context takeover":    {extensions: "permessage-deflate; client_max_window_bits=15", takeover: true},
		"without context takeover": {extensions: "permessage-deflate; server_no_context_takeover; client_no_context_takeover"},
	} {
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t, Options{})
			capture, client, server := beginSocket(t, store, tc.extensions)
			fromClient := &deflater{takeover: tc.takeover}
			fromServer := &deflater{takeover: tc.takeover}

			first := fromClient.compress(t, turn)
			second := fromClient.compress(t, turn)
			if tc.takeover && len(second) >= len(first)/4 {
				t.Fatalf("second message is %d bytes, first %d: it does not refer back into the first", len(second), len(first))
			}
			feed(client, 7, wsFrame(0xc1, first, &clientMask), wsFrame(0xc1, second, &clientMask))
			// An uncompressed message may sit between compressed ones.
			feed(server, 7, wsFrame(0xc1, fromServer.compress(t, "reply one"), nil), wsFrame(0x81, []byte("plain"), nil), wsFrame(0xc1, fromServer.compress(t, "reply one, again"), nil))
			// A compressed message may be fragmented; only its first frame is marked.
			last := fromServer.compress(t, "reply in pieces")
			feed(server, 2, wsFrame(0x41, last[:3], nil), wsFrame(0x80, last[3:], nil))

			record, messages := finishSocket(t, store, capture)
			assertMessages(t, messages,
				"client text "+turn,
				"client text "+turn,
				"server text reply one",
				"server text plain",
				"server text reply one, again",
				"server text reply in pieces",
			)
			if record.WebSocket.Compression != "permessage-deflate" {
				t.Fatalf("record websocket = %+v", record.WebSocket)
			}
			if messages[1].DecodedFrom != "permessage-deflate" || messages[1].Bytes != int64(len(second)) || messages[1].StoredBytes != int64(len(turn)) {
				t.Fatalf("decoded message = %+v", messages[1])
			}
			if messages[3].DecodedFrom != "" {
				t.Fatalf("uncompressed message = %+v", messages[3])
			}
		})
	}
}

func TestWebSocketCaptureKeepsMessagesItCannotDecode(t *testing.T) {
	store := openTestStore(t, Options{})
	capture, _, server := beginSocket(t, store, "permessage-deflate")
	feed(server, 64, wsFrame(0xc1, []byte{0xff, 0xff, 0xff}, nil), wsFrame(0xa1, []byte("rsv2"), nil))
	_, messages := finishSocket(t, store, capture)
	if len(messages) != 2 || !messages[0].Compressed || !bytes.Equal(messages[0].Base64, []byte{0xff, 0xff, 0xff}) || messages[0].Text != "" {
		t.Fatalf("undecodable message = %+v", messages)
	}
	if !messages[1].Compressed || string(messages[1].Base64) != "rsv2" {
		t.Fatalf("message with a reserved bit no extension claims = %+v", messages[1])
	}

	// Without the extension accepted, a compressed frame is kept as relayed.
	store = openTestStore(t, Options{})
	capture, _, server = beginSocket(t, store, "")
	compressed := (&deflater{}).compress(t, "hello")
	feed(server, 64, wsFrame(0xc1, compressed, nil))
	_, messages = finishSocket(t, store, capture)
	if len(messages) != 1 || !messages[0].Compressed || !bytes.Equal(messages[0].Base64, compressed) {
		t.Fatalf("compressed message without the extension = %+v", messages)
	}
}

func TestWebSocketCaptureTruncatesMessagesOverTheCap(t *testing.T) {
	store := openTestStore(t, Options{MaxBodyBytes: 16})
	capture, client, server := beginSocket(t, store, "permessage-deflate")
	feed(client, 10, clientText("0123456789abcdefOVER"))
	// Decoding goes on past the cap, so the window stays right and the next
	// message, which refers back into this one, still decodes.
	fromServer := &deflater{takeover: true}
	long := "a" + strings.Repeat("\u00e9", 40)
	feed(server, 10, wsFrame(0xc1, fromServer.compress(t, long), nil), wsFrame(0xc1, fromServer.compress(t, long[:11]), nil))

	record, messages := finishSocket(t, store, capture)
	if m := messages[0]; m.Text != "0123456789abcdef" || !m.Truncated || m.Bytes != 20 || m.StoredBytes != 16 {
		t.Fatalf("message over the cap = %+v", m)
	}
	// The cut fell inside a character, which is dropped rather than turning the
	// payload into bytes.
	want := long[:16]
	for !utf8.ValidString(want) {
		want = want[:len(want)-1]
	}
	if len(want) == 16 {
		t.Fatal("the cap does not fall inside a character")
	}
	if m := messages[1]; m.Text != want || !m.Truncated || m.StoredBytes != int64(len(want)) {
		t.Fatalf("decoded message over the cap = %+v", m)
	}
	if m := messages[2]; m.Text != long[:11] || m.Truncated {
		t.Fatalf("message after one over the cap = %+v", m)
	}
	if !record.WebSocket.Truncated || record.WebSocket.StoredMessages != 3 {
		t.Fatalf("record websocket = %+v", record.WebSocket)
	}
}

func TestWebSocketCaptureStopsStoringAtTheSocketBudget(t *testing.T) {
	store := openTestStore(t, Options{MaxSocketBytes: 400})
	capture, client, _ := beginSocket(t, store, "")
	for range 20 {
		feed(client, 64, clientText(strings.Repeat("x", 50)))
	}
	record, messages := finishSocket(t, store, capture)
	socket := record.WebSocket
	if socket.Messages != 20 || socket.StoredMessages != int64(len(messages)) || len(messages) == 0 || len(messages) >= 20 {
		t.Fatalf("record websocket = %+v with %d messages stored", socket, len(messages))
	}
	if !socket.Truncated || socket.StoredBytes > 400 {
		t.Fatalf("record websocket = %+v", socket)
	}
}

func TestWebSocketCaptureHoldsTheSessionBudgetWhileOpen(t *testing.T) {
	store := openTestStore(t, Options{MaxBytes: 2048})
	earlier := captureExchange(t, store, "", strings.Repeat("r", 1024))
	capture, client, _ := beginSocket(t, store, "")
	for range 40 {
		feed(client, 64, clientText(strings.Repeat("x", 50)))
	}
	// The socket evicted the finished capture to make room, and then stopped.
	if _, err := os.Stat(RecordPath(store.dir, earlier)); !os.IsNotExist(err) {
		t.Fatalf("finished capture not evicted for the open socket: %v", err)
	}
	store.mu.Lock()
	used := store.used
	store.mu.Unlock()
	if used > 2048 {
		t.Fatalf("session holds %d bytes over its 2048 budget while the socket is open", used)
	}
	record, _ := finishSocket(t, store, capture)
	if !record.WebSocket.Truncated || record.WebSocket.StoredMessages >= 40 {
		t.Fatalf("record websocket = %+v", record.WebSocket)
	}
}

func TestWebSocketCaptureRedactsReleasedSecrets(t *testing.T) {
	const secret = "sk-live-do-not-store-0123456789"
	const placeholder = "ENCLAVE_SECRET_0123456789abcdef"
	store := openTestStore(t, Options{MaxBodyBytes: 64, Secrets: []Secret{{Value: secret, Placeholder: placeholder}}})
	capture, client, server := beginSocket(t, store, "permessage-deflate")

	half := len(secret) / 2
	feed(server, 9,
		serverText("token "+secret),
		wsFrame(0x01, []byte("split "+secret[:half]), nil),
		wsFrame(0x80, []byte(secret[half:]), nil),
		wsFrame(0xc1, (&deflater{}).compress(t, "compressed "+secret), nil),
		// A value that straddles the cap is replaced before the payload is cut.
		serverText(strings.Repeat("p", 50)+secret),
	)
	feed(client, 9, wsFrame(0x88, append([]byte{0x03, 0xe8}, secret...), &clientMask))
	// A message the connection ends in may stop inside a value, whose start is
	// then left out.
	cut := serverText("tail " + secret)
	feed(server, 9, cut[:len(cut)-10])

	_, messages := finishSocket(t, store, capture)
	assertMessages(t, messages,
		"server text token "+placeholder,
		"server text split "+placeholder,
		"server text compressed "+placeholder,
		"server text "+strings.Repeat("p", 50)+placeholder[:14],
		"client close "+placeholder,
		"server text tail ",
	)
	entries, _ := os.ReadDir(store.dir)
	for _, entry := range entries {
		raw, _ := os.ReadFile(filepath.Join(store.dir, entry.Name())) // #nosec G304 -- test-owned temp path.
		if bytes.Contains(raw, []byte(secret[:8])) {
			t.Errorf("%s contains part of the released secret", entry.Name())
		}
	}
}

func TestWebSocketCaptureKeepsWhatArrivedOfUnfinishedMessages(t *testing.T) {
	store := openTestStore(t, Options{})
	capture, client, server := beginSocket(t, store, "")
	feed(client, 64, wsFrame(0x01, []byte("first half, "), &clientMask), wsFrame(0x00, []byte("second"), &clientMask))
	frame := serverText("cut off in the middle")
	feed(server, 64, frame[:10])

	record, messages := finishSocket(t, store, capture)
	assertMessages(t, messages,
		"client text first half, second",
		"server text cut off ",
	)
	if !messages[0].Truncated || !messages[1].Truncated || messages[1].Bytes != 8 || !record.WebSocket.Truncated {
		t.Fatalf("unfinished messages = %+v, record %+v", messages, record.WebSocket)
	}
}

func TestWebSocketCaptureStopsFollowingAnInvalidStream(t *testing.T) {
	store := openTestStore(t, Options{})
	capture, client, server := beginSocket(t, store, "")
	feed(server, 64, serverText("before"), wsFrame(0x83, []byte("reserved"), nil), serverText("after"))
	feed(client, 64, clientText("client side unaffected"))

	record, messages := finishSocket(t, store, capture)
	assertMessages(t, messages,
		"server text before",
		"client text client side unaffected",
	)
	if !strings.HasPrefix(record.WebSocket.Error, "server: invalid WebSocket frame") || record.WebSocket.Bytes == 0 {
		t.Fatalf("record websocket = %+v", record.WebSocket)
	}
}

func TestWebSocketCaptureLeavesOtherUpgradesAlone(t *testing.T) {
	store := openTestStore(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/exec", nil)
	capture := store.Begin(req, "https")
	resp := &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: http.Header{"Upgrade": {"SPDY/3.1"}}}
	capture.Response(resp)
	if client, server := capture.WebSocket(resp); client != nil || server != nil {
		t.Fatal("WebSocket() captures a SPDY upgrade")
	}
	id := capture.Finish(nil)
	record, err := Load(RecordPath(store.dir, id))
	if err != nil || record.WebSocket != nil {
		t.Fatalf("record = %+v, %v", record, err)
	}
	var nilCapture *Capture
	if client, server := nilCapture.WebSocket(resp); client != nil || server != nil {
		t.Fatal("nil capture returned writers")
	}
}

func TestWebSocketCaptureWithoutMessagesStoresNoFile(t *testing.T) {
	store := openTestStore(t, Options{})
	capture, _, _ := beginSocket(t, store, "")
	record, messages := finishSocket(t, store, capture)
	if record.WebSocket.File != "" || len(messages) != 0 {
		t.Fatalf("record websocket = %+v", record.WebSocket)
	}
	entries, _ := os.ReadDir(store.dir)
	if len(entries) != 1 {
		t.Fatalf("session directory holds %d files, want only the record", len(entries))
	}
}

func TestReadMessagesSkipsALineStillBeingWritten(t *testing.T) {
	dir := t.TempDir()
	id := "0123456789abcdef"
	line, _ := json.Marshal(Message{From: FromClient, Type: MessageText, Text: "done"})
	if err := os.WriteFile(filepath.Join(dir, id+messagesSuffix), append(append(line, '\n'), `{"from":"ser`...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, messages, err := ReadMessages(OpenRecordPath(dir, id), &WebSocket{File: id + messagesSuffix})
	if err != nil || len(messages) != 1 || messages[0].Text != "done" {
		t.Fatalf("ReadMessages() = %+v, %v", messages, err)
	}
	if _, ok := MessagesPath(RecordPath(dir, id), &WebSocket{File: id + requestBodySuffix}); ok {
		t.Fatal("MessagesPath() accepts a body file")
	}
	if _, ok := BodyPath(RecordPath(dir, id), &Body{File: id + messagesSuffix}); ok {
		t.Fatal("BodyPath() accepts a messages file")
	}
}

func TestOpenRemovesSocketsLeftOpenByAStoppedGateway(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, Options{Dir: dir})
	capture, client, _ := beginSocket(t, store, "")
	feed(client, 64, clientText("never closed"))
	finished := captureExchange(t, store, "", "kept")

	// The open record does not make a capture finished, so the next proxy
	// treats it as left behind.
	reopened := openTestStore(t, Options{Dir: dir})
	for _, path := range []string{OpenRecordPath(dir, capture.id), filepath.Join(dir, capture.id+messagesSuffix)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s survived reopening: %v", filepath.Base(path), err)
		}
	}
	if len(reopened.finished) != 1 || reopened.finished[0].id != finished {
		t.Fatalf("reopened store holds %+v", reopened.finished)
	}
}

func TestTrimPartialRune(t *testing.T) {
	euro := []byte("€") // three bytes
	for _, tc := range []struct {
		in, want []byte
	}{
		{[]byte("abc"), []byte("abc")},
		{append([]byte("ab"), euro...), append([]byte("ab"), euro...)},
		{append([]byte("ab"), euro[:1]...), []byte("ab")},
		{append([]byte("ab"), euro[:2]...), []byte("ab")},
		{[]byte{}, []byte{}},
	} {
		if got := TrimPartialRune(tc.in); !bytes.Equal(got, tc.want) {
			t.Fatalf("TrimPartialRune(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
