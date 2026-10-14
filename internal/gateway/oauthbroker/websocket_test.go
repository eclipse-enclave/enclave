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
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const websocketSecret = "real-provider-token-0123456789"

func serverFrame(opcode byte, final bool, payload string) []byte {
	first := opcode
	if final {
		first |= 0x80
	}
	frame := []byte{first}
	switch {
	case len(payload) < 126:
		frame = append(frame, byte(len(payload)))
	case len(payload) <= 0xffff:
		frame = binary.BigEndian.AppendUint16(append(frame, 126), uint16(len(payload)))
	default:
		frame = binary.BigEndian.AppendUint64(append(frame, 127), uint64(len(payload)))
	}
	return append(frame, payload...)
}

type upgradedConn struct {
	io.Reader
	written bytes.Buffer
	closed  bool
}

func (c *upgradedConn) Write(p []byte) (int, error) { return c.written.Write(p) }
func (c *upgradedConn) Close() error                { c.closed = true; return nil }

// relayFrames returns what the workload would read from frames, and the
// error that ended the stream.
func relayFrames(frames ...[]byte) ([]byte, error) {
	w := &websocketTripwire{conn: &upgradedConn{Reader: bytes.NewReader(bytes.Join(frames, nil))}, patterns: [][]byte{[]byte(websocketSecret)}}
	var out []byte
	buffer := make([]byte, 7)
	for {
		n, err := w.Read(buffer)
		out = append(out, buffer[:n]...)
		if err != nil {
			return out, err
		}
	}
}

func TestWebSocketTripwireRelaysCleanFramesUnchanged(t *testing.T) {
	frames := [][]byte{
		serverFrame(1, true, "short"),
		serverFrame(2, true, strings.Repeat("m", 300)),
		serverFrame(1, true, strings.Repeat("l", 70000)),
		serverFrame(1, false, "starts real-provider-"),
		serverFrame(9, true, "ping between fragments"),
		serverFrame(0, true, "something else"),
		serverFrame(8, true, "\x03\xe8bye"),
	}
	out, err := relayFrames(frames...)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("relay ended with %v", err)
	}
	// The ping overtakes the fragment held for its partial match.
	want := bytes.Join([][]byte{frames[0], frames[1], frames[2], frames[4], frames[3], frames[5], frames[6]}, nil)
	if !bytes.Equal(out, want) {
		t.Fatalf("relayed %d bytes, want %d unchanged", len(out), len(want))
	}
}

func TestWebSocketTripwireStopsCredentials(t *testing.T) {
	half := len(websocketSecret) / 2
	for name, tc := range map[string]struct {
		frames    [][]byte
		delivered int
	}{
		"in one frame":           {frames: [][]byte{serverFrame(1, true, "ok"), serverFrame(1, true, "token "+websocketSecret)}, delivered: 1},
		"across fragments":       {frames: [][]byte{serverFrame(1, false, "token "+websocketSecret[:half]), serverFrame(9, true, "p"), serverFrame(0, true, websocketSecret[half:])}, delivered: 0},
		"across three fragments": {frames: [][]byte{serverFrame(1, false, websocketSecret[:3]), serverFrame(0, false, websocketSecret[3:half]), serverFrame(0, true, websocketSecret[half:])}},
		"in a close reason":      {frames: [][]byte{serverFrame(8, true, "\x03\xe8"+websocketSecret)}},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := relayFrames(tc.frames...)
			if !errors.Is(err, ErrResponse) {
				t.Fatalf("relay ended with %v", err)
			}
			if bytes.Contains(out, []byte(websocketSecret[:half])) || bytes.Contains(out, []byte(websocketSecret[half:])) {
				t.Fatalf("part of the credential reached the workload: %q", out)
			}
			if tc.delivered > 0 && !bytes.Equal(out, bytes.Join(tc.frames[:tc.delivered], nil)) {
				t.Fatalf("frames before the credential were not relayed: %q", out)
			}
		})
	}
}

func TestWebSocketTripwireFailsClosedOnUnscannableFrames(t *testing.T) {
	masked := serverFrame(1, true, "x")
	masked[1] |= 0x80
	compressed := serverFrame(1, true, "x")
	compressed[0] |= 0x40
	oversized := binary.BigEndian.AppendUint64([]byte{0x82, 127}, maxWebSocketHeld+1)
	for name, frame := range map[string][]byte{
		"masked":                  masked,
		"compressed":              compressed,
		"oversized":               oversized,
		"unknown opcode":          serverFrame(3, true, "x"),
		"fragmented control":      serverFrame(9, false, "x"),
		"continuation unstarted":  serverFrame(0, true, "x"),
		"long control frame":      serverFrame(10, true, strings.Repeat("x", 126)),
		"new message in fragment": append(serverFrame(1, false, "a"), serverFrame(1, true, "b")...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := relayFrames(frame); !errors.Is(err, ErrResponse) {
				t.Fatalf("relay ended with %v", err)
			}
		})
	}
}

func TestWebSocketTripwireDropsHeldFragmentsAtClose(t *testing.T) {
	held := serverFrame(1, false, "token "+websocketSecret[:10])
	closing := serverFrame(8, true, "")
	out, err := relayFrames(held, closing)
	if !errors.Is(err, io.EOF) || !bytes.Equal(out, closing) {
		t.Fatalf("relayed %q, %v", out, err)
	}
}

func TestMediatedUpgradesAreScannableWebSockets(t *testing.T) {
	for name, tc := range map[string]struct {
		upgrade, extensions string
		wantErr             error
	}{
		"websocket":           {upgrade: "websocket"},
		"other protocol":      {upgrade: "h2c", wantErr: ErrDenied},
		"accepted extensions": {upgrade: "websocket", extensions: "permessage-deflate", wantErr: ErrResponse},
	} {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", tc.upgrade)
			req.Header.Set("Sec-WebSocket-Extensions", "permessage-deflate")
			var offered []string
			response, err := mediate(req, fixtureTransport(func(out *http.Request) (*http.Response, error) {
				offered = out.Header.Values("Sec-WebSocket-Extensions")
				header := http.Header{"Connection": {"Upgrade"}, "Upgrade": {tc.upgrade}}
				if tc.extensions != "" {
					header.Set("Sec-WebSocket-Extensions", tc.extensions)
				}
				return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: header, Body: &upgradedConn{Reader: strings.NewReader("")}}, nil
			}), websocketSecret)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("mediate = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || len(offered) != 0 {
				t.Fatalf("mediate = %v, offered extensions %v", err, offered)
			}
			if _, ok := response.Body.(*websocketTripwire); !ok {
				t.Fatalf("upgraded stream is not scanned: %T", response.Body)
			}
		})
	}
}
