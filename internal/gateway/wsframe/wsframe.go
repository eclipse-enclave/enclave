// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package wsframe decodes RFC 6455 WebSocket frame headers without taking part
// in the connection. It enforces only the rules of the RFC itself; which
// frames a caller accepts, and what it does with their payloads, is up to the
// caller.
package wsframe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Opcode is the frame type.
type Opcode byte

const (
	OpContinuation Opcode = 0x0
	OpText         Opcode = 0x1
	OpBinary       Opcode = 0x2
	OpClose        Opcode = 0x8
	OpPing         Opcode = 0x9
	OpPong         Opcode = 0xa
)

// IsControl reports whether frames of this type are control frames, which may
// arrive between the fragments of a message.
func (o Opcode) IsControl() bool {
	return o&0x8 != 0
}

// RSV1 is the reserved bit permessage-deflate uses to mark a compressed
// message, as it appears in Header.Rsv.
const RSV1 = 0x4

// MaxHeaderLen is the longest a frame header can be: two bytes, a 64-bit
// length and a masking key.
const MaxHeaderLen = 14

// maxControlPayload is the longest payload a control frame may carry.
const maxControlPayload = 125

// Header is a decoded frame header.
type Header struct {
	Fin bool
	// Rsv holds RSV1, RSV2 and RSV3 as the bits 0x4, 0x2 and 0x1.
	Rsv    byte
	Opcode Opcode
	Masked bool
	Mask   [4]byte
	Length uint64
}

var (
	// ErrShort reports that the bytes given end inside the header.
	ErrShort = errors.New("incomplete WebSocket frame header")
	// ErrInvalid reports a header that breaks RFC 6455.
	ErrInvalid = errors.New("invalid WebSocket frame")
)

// Parse decodes the header at the start of b and returns its length. While b
// holds only part of the header it returns ErrShort, unless the part already
// shows the header to be invalid.
func Parse(b []byte) (Header, int, error) {
	if len(b) < 2 {
		return Header{}, 0, ErrShort
	}
	h := Header{
		Fin:    b[0]&0x80 != 0,
		Rsv:    b[0] >> 4 & 0x7,
		Opcode: Opcode(b[0] & 0x0f),
		Masked: b[1]&0x80 != 0,
		Length: uint64(b[1] & 0x7f),
	}
	switch {
	case h.Opcode > OpPong || (h.Opcode > OpBinary && h.Opcode < OpClose):
		return Header{}, 0, fmt.Errorf("%w: reserved opcode %#x", ErrInvalid, byte(h.Opcode))
	case h.Opcode.IsControl() && !h.Fin:
		return Header{}, 0, fmt.Errorf("%w: fragmented control frame", ErrInvalid)
	case h.Opcode.IsControl() && h.Length > maxControlPayload:
		return Header{}, 0, fmt.Errorf("%w: control frame longer than %d bytes", ErrInvalid, maxControlPayload)
	}
	n := headerLen(b[1])
	if len(b) < n {
		return Header{}, 0, ErrShort
	}
	switch h.Length {
	case 126:
		h.Length = uint64(binary.BigEndian.Uint16(b[2:]))
	case 127:
		h.Length = binary.BigEndian.Uint64(b[2:])
		if h.Length>>63 != 0 {
			return Header{}, 0, fmt.Errorf("%w: length with its most significant bit set", ErrInvalid)
		}
	}
	if h.Masked {
		copy(h.Mask[:], b[n-4:n])
	}
	return h, n, nil
}

// headerLen returns the length of a header from its second byte.
func headerLen(b1 byte) int {
	n := 2
	switch b1 & 0x7f {
	case 126:
		n += 2
	case 127:
		n += 8
	}
	if b1&0x80 != 0 {
		n += 4
	}
	return n
}

// ReadHeader reads one header from r and returns it together with its bytes
// as read, for callers that relay frames unchanged.
func ReadHeader(r io.Reader) (Header, []byte, error) {
	raw := make([]byte, 2, MaxHeaderLen)
	if _, err := io.ReadFull(r, raw); err != nil {
		return Header{}, nil, err
	}
	if _, _, err := Parse(raw); err != nil && !errors.Is(err, ErrShort) {
		return Header{}, raw, err
	}
	raw = raw[:headerLen(raw[1])]
	if _, err := io.ReadFull(r, raw[2:]); err != nil {
		return Header{}, raw, noEOF(err)
	}
	h, _, err := Parse(raw)
	return h, raw, err
}

// noEOF reports a header cut short as such, not as a stream that ended between
// frames.
func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// Unmask applies the masking key to payload in place. offset is the position
// of payload[0] within the frame's payload, so a payload that arrives in
// pieces can be unmasked piece by piece.
func Unmask(key [4]byte, payload []byte, offset uint64) {
	for i := range payload {
		payload[i] ^= key[(offset+uint64(i))&3]
	}
}
