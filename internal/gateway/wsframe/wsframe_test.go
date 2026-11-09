// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wsframe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func header(first byte, length uint64, mask *[4]byte) []byte {
	second := byte(0)
	if mask != nil {
		second = 0x80
	}
	var out []byte
	switch {
	case length < 126:
		out = []byte{first, second | byte(length)}
	case length <= 0xffff:
		out = binary.BigEndian.AppendUint16([]byte{first, second | 126}, uint16(length))
	default:
		out = binary.BigEndian.AppendUint64([]byte{first, second | 127}, length)
	}
	if mask != nil {
		out = append(out, mask[:]...)
	}
	return out
}

func TestParseDecodesEveryLengthForm(t *testing.T) {
	key := [4]byte{1, 2, 3, 4}
	for name, tc := range map[string]struct {
		raw  []byte
		want Header
	}{
		"7-bit":             {header(0x81, 5, nil), Header{Fin: true, Opcode: OpText, Length: 5}},
		"16-bit":            {header(0x02, 300, nil), Header{Opcode: OpBinary, Length: 300}},
		"64-bit":            {header(0x80, 70000, nil), Header{Fin: true, Opcode: OpContinuation, Length: 70000}},
		"masked":            {header(0x81, 3, &key), Header{Fin: true, Opcode: OpText, Masked: true, Mask: key, Length: 3}},
		"masked 64-bit":     {header(0x82, 1<<40, &key), Header{Fin: true, Opcode: OpBinary, Masked: true, Mask: key, Length: 1 << 40}},
		"compressed":        {header(0xc1, 10, nil), Header{Fin: true, Rsv: RSV1, Opcode: OpText, Length: 10}},
		"all reserved bits": {header(0xf2, 0, nil), Header{Fin: true, Rsv: 0x7, Opcode: OpBinary}},
		"close":             {header(0x88, 125, nil), Header{Fin: true, Opcode: OpClose, Length: 125}},
	} {
		t.Run(name, func(t *testing.T) {
			raw := append(tc.raw, "payload"...)
			got, n, err := Parse(raw)
			if err != nil || n != len(tc.raw) || got != tc.want {
				t.Fatalf("Parse() = %+v, %d, %v; want %+v, %d", got, n, err, tc.want, len(tc.raw))
			}
			for cut := range len(tc.raw) {
				if _, _, err := Parse(tc.raw[:cut]); !errors.Is(err, ErrShort) {
					t.Fatalf("Parse() of %d header bytes = %v, want ErrShort", cut, err)
				}
			}
		})
	}
}

func TestParseRejectsWhatRFC6455Forbids(t *testing.T) {
	for name, raw := range map[string][]byte{
		"reserved data opcode":    header(0x83, 0, nil),
		"reserved control opcode": header(0x8b, 0, nil),
		"fragmented control":      header(0x09, 0, nil),
		"long control":            header(0x8a, 126, nil),
		"64-bit length top bit":   binary.BigEndian.AppendUint64([]byte{0x82, 127}, 1<<63),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Parse(raw); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse() = %v, want ErrInvalid", err)
			}
		})
	}
	// The first two bytes settle the opcode, so a bad one is reported without
	// waiting for the rest of the header.
	if _, _, err := Parse([]byte{0x83, 126}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Parse() of a partial header with a reserved opcode = %v, want ErrInvalid", err)
	}
}

func TestReadHeaderReturnsTheBytesRead(t *testing.T) {
	key := [4]byte{9, 8, 7, 6}
	raw := header(0x82, 300, &key)
	stream := bytes.NewReader(append(append([]byte{}, raw...), 0xaa))
	got, read, err := ReadHeader(stream)
	if err != nil || !bytes.Equal(read, raw) || got.Length != 300 || got.Mask != key {
		t.Fatalf("ReadHeader() = %+v, %x, %v", got, read, err)
	}
	if rest, _ := io.ReadAll(stream); !bytes.Equal(rest, []byte{0xaa}) {
		t.Fatalf("ReadHeader() read past the header: %x left", rest)
	}

	if _, _, err := ReadHeader(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadHeader() between frames = %v, want io.EOF", err)
	}
	for cut := 1; cut < len(raw); cut++ {
		if _, _, err := ReadHeader(bytes.NewReader(raw[:cut])); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("ReadHeader() of a header cut at %d = %v, want io.ErrUnexpectedEOF", cut, err)
		}
	}
	if _, _, err := ReadHeader(bytes.NewReader(header(0x83, 0, nil))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ReadHeader() of a reserved opcode = %v, want ErrInvalid", err)
	}
}

func TestUnmaskInPiecesMatchesUnmaskingAtOnce(t *testing.T) {
	key := [4]byte{0x37, 0xfa, 0x21, 0x3d}
	plain := []byte("Hello, WebSocket payload split anywhere")
	masked := append([]byte{}, plain...)
	Unmask(key, masked, 0)
	if bytes.Equal(masked, plain) {
		t.Fatal("Unmask() changed nothing")
	}
	for split := range len(masked) {
		pieces := append([]byte{}, masked...)
		Unmask(key, pieces[:split], 0)
		Unmask(key, pieces[split:], uint64(split))
		if !bytes.Equal(pieces, plain) {
			t.Fatalf("unmasking split at %d = %q", split, pieces)
		}
	}
}
