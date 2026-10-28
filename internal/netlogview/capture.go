// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlogview

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"enclave/internal/logx"
	"enclave/internal/netcapture"
	"enclave/internal/netlog"
	"enclave/internal/util"
)

// CaptureView is a capture record together with the body files it refers to.
type CaptureView struct {
	Record netcapture.Record
	// Path is the record file.
	Path     string
	Request  CapturedBody
	Response CapturedBody
}

// CapturedBody is a stored body as read from its file.
type CapturedBody struct {
	Data []byte
	// Path is the body file; empty when nothing was stored.
	Path string
	// Err is why a file the record names could not be read: it was pruned
	// meanwhile, or it is not a file the store writes.
	Err error
}

// WriteCapture renders a capture as the two HTTP messages it holds. Headers and
// bodies come from the sandbox and from upstream servers, so everything is made
// display safe: a body cannot drive the terminal of whoever reads it.
func WriteCapture(out io.StringWriter, view CaptureView, opts RenderOptions) error {
	record := view.Record
	var text strings.Builder

	text.WriteString(opts.colorize("CAPTURE", logx.ColorCyan) + "  " + displaySafe(record.ID))
	if record.Session != "" {
		text.WriteString("  " + displaySafe(record.Session))
	}
	if at, err := time.Parse(netlog.TimeFormat, record.Timestamp); err == nil {
		text.WriteString("  " + opts.colorize(at.In(opts.location()).Format("2006-01-02 15:04:05"), logx.ColorDim))
	}
	text.WriteString("  " + opts.colorize((time.Duration(record.DurationMS)*time.Millisecond).String(), logx.ColorDim))
	text.WriteString("\n" + opts.colorize(displaySafe(view.Path), logx.ColorDim) + "\n\n")

	request := record.Request
	requestLine := displaySafe(request.Method) + " " + displaySafe(request.URL)
	if request.Proto != "" {
		requestLine += " " + displaySafe(request.Proto)
	}
	text.WriteString(opts.colorize(requestLine, logx.ColorBold) + "\n")
	writeHeaders(&text, request.Headers)
	writeBody(&text, "request", request.Body, view.Request, request.Headers, opts)

	if record.Error != "" {
		text.WriteString("\n" + opts.colorize("Relay failed: "+displaySafe(record.Error), logx.ColorRed) + "\n")
	}
	if response := record.Response; response != nil {
		statusLine := displaySafe(response.Proto)
		if statusLine == "" {
			statusLine = "HTTP"
		}
		statusLine += fmt.Sprintf(" %d %s", response.Status, http.StatusText(response.Status))
		statusColor := logx.ColorGreen
		if response.Status >= 400 {
			statusColor = logx.ColorYellow
		}
		text.WriteString("\n" + opts.colorize(strings.TrimRight(statusLine, " "), statusColor) + "\n")
		writeHeaders(&text, response.Headers)
		if response.Status == http.StatusSwitchingProtocols {
			text.WriteString("\n" + opts.colorize("(the upgraded connection is not captured)", logx.ColorDim) + "\n")
		}
		writeBody(&text, "response", response.Body, view.Response, response.Headers, opts)
	} else if record.Error == "" {
		text.WriteString("\n" + opts.colorize("(no response recorded)", logx.ColorDim) + "\n")
	}

	_, err := out.WriteString(text.String())
	return err
}

func writeHeaders(text *strings.Builder, headers http.Header) {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, value := range headers[name] {
			text.WriteString(displaySafe(name) + ": " + displaySafe(value) + "\n")
		}
	}
}

// writeBody prints a body followed by one note saying how much of it was
// stored and where, so it can be piped to other tools straight from its file.
func writeBody(text *strings.Builder, label string, body *netcapture.Body, stored CapturedBody, headers http.Header, opts RenderOptions) {
	if body == nil {
		return
	}
	// A body the capture decoded is shown as stored; its headers still name the
	// encoding it was relayed in. Every other encoding, repeated ones included,
	// was stored as relayed and is not shown.
	encoding := strings.Join(headers.Values("Content-Encoding"), ", ")
	size := util.FormatBytes(body.StoredBytes)
	switch {
	case body.DecodedFrom != "":
		encoding = ""
		size += " decoded from " + util.FormatBytes(body.Bytes) + " of " + displaySafe(body.DecodedFrom)
		if body.Truncated {
			size += ", truncated"
		}
	case body.Truncated:
		size += " of " + util.FormatBytes(body.Bytes) + ", truncated"
	}
	note := label + " body"
	switch {
	case stored.Err != nil:
		note += " not shown, " + size + ": " + displaySafe(stored.Err.Error())
	case len(stored.Data) == 0:
		note += ", " + size
	default:
		content, detail := displayBody(stored.Data, encoding, body.Truncated)
		if detail != "" {
			note += " " + detail
		}
		note += ", " + size + ": " + displaySafe(stored.Path)
		if content != "" {
			text.WriteString("\n" + content)
			if !strings.HasSuffix(content, "\n") {
				text.WriteString("\n")
			}
		}
	}
	text.WriteString(opts.colorize("("+note+")", logx.ColorDim) + "\n")
	if command, ok := decodeCommands[strings.ToLower(strings.TrimSpace(encoding))]; ok && len(stored.Data) > 0 {
		text.WriteString(opts.colorize("(decode with: "+command+" "+shellQuote(displaySafe(stored.Path))+")", logx.ColorDim) + "\n")
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// decodeCommands name a tool that decodes a content encoding the viewer cannot,
// so the stored body can still be read.
var decodeCommands = map[string]string{
	"br":   "brotli -dc",
	"zstd": "zstd -dc",
}

// displayBody returns the body as terminal text, or a note saying why it is
// not shown. A body cut short may end inside a character; that partial
// character is dropped rather than making the whole body binary.
func displayBody(data []byte, encoding string, truncated bool) (string, string) {
	switch encoding = strings.ToLower(strings.TrimSpace(encoding)); encoding {
	case "", "identity":
	default:
		return "", displaySafe(encoding) + "-encoded, not shown"
	}
	if truncated {
		data = trimPartialRune(data)
	}
	if !isText(data) {
		return "", "binary"
	}
	return displaySafeText(string(data)), ""
}

// trimPartialRune drops an incomplete UTF-8 sequence at the end of data.
func trimPartialRune(data []byte) []byte {
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

func isText(data []byte) bool {
	return utf8.Valid(data) && !bytes.ContainsRune(data, 0)
}

// displaySafeText is displaySafe that keeps the line structure.
func displaySafeText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, value)
}
