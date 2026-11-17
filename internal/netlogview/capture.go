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

// CaptureView is a capture record together with the body and message files it
// refers to.
type CaptureView struct {
	Record netcapture.Record
	// Path is the record file.
	Path     string
	Request  CapturedBody
	Response CapturedBody
	Messages CapturedMessages
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

// CapturedMessages are the stored messages of a WebSocket as read from their
// file.
type CapturedMessages struct {
	List []netcapture.Message
	// Path is the messages file; empty when nothing was stored.
	Path string
	Err  error
}

// WriteCapture renders a capture as the two HTTP messages it holds, followed by
// the messages of a WebSocket. Headers, bodies and messages come from the
// sandbox and from upstream servers, so everything is made display safe: a
// payload cannot drive the terminal of whoever reads it.
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
	if record.WebSocket != nil && record.WebSocket.Open {
		text.WriteString("  " + opts.colorize("open", logx.ColorDim))
	} else {
		text.WriteString("  " + opts.colorize((time.Duration(record.DurationMS)*time.Millisecond).String(), logx.ColorDim))
	}
	text.WriteString("\n" + opts.colorize(displaySafe(view.Path), logx.ColorDim) + "\n\n")

	request := record.Request
	requestLine := displaySafe(request.Method) + " " + displaySafe(request.URL)
	if request.Proto != "" {
		requestLine += " " + displaySafe(request.Proto)
	}
	text.WriteString(opts.colorize(requestLine, logx.ColorBold) + "\n")
	writeHeaders(&text, request.Headers)
	writeBody(&text, "request", request.Body, view.Request, request.Headers, opts)
	if request.BodyOmitted != "" {
		text.WriteString(opts.colorize("(request body not stored: "+displaySafe(request.BodyOmitted)+")", logx.ColorDim) + "\n")
	}

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
		if response.Status == http.StatusSwitchingProtocols && record.WebSocket == nil {
			text.WriteString("\n" + opts.colorize("(the upgraded connection is not captured)", logx.ColorDim) + "\n")
		}
		writeBody(&text, "response", response.Body, view.Response, response.Headers, opts)
		if response.BodyOmitted != "" {
			text.WriteString(opts.colorize("(response body not stored: "+displaySafe(response.BodyOmitted)+")", logx.ColorDim) + "\n")
		}
		if record.WebSocket != nil {
			writeMessages(&text, record.WebSocket, view.Messages, opts)
		}
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
	// A body with a content encoding was stored as relayed and is not shown.
	encoding := strings.Join(headers.Values("Content-Encoding"), ", ")
	size := util.FormatBytes(body.StoredBytes)
	if body.Truncated {
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
}

// writeMessages prints a note on a WebSocket's messages file, then each
// message: > for what the client sent, < for what the server sent.
func writeMessages(text *strings.Builder, socket *netcapture.WebSocket, stored CapturedMessages, opts RenderOptions) {
	if socket.MessagesOmitted != "" {
		text.WriteString("\n" + opts.colorize("(WebSocket messages not stored: "+displaySafe(socket.MessagesOmitted)+")", logx.ColorDim) + "\n")
		return
	}
	note := "WebSocket"
	if socket.Open {
		note += fmt.Sprintf(" still open, %d messages so far", len(stored.List))
	} else {
		note += fmt.Sprintf(", %d messages", socket.Messages)
		if socket.StoredMessages != socket.Messages {
			note += fmt.Sprintf(", %d stored", socket.StoredMessages)
		}
	}
	if socket.Truncated {
		note += ", truncated"
	}
	switch {
	case stored.Err != nil:
		note += ", not shown: " + displaySafe(stored.Err.Error())
	case stored.Path != "":
		note += ": " + displaySafe(stored.Path)
	}
	text.WriteString("\n" + opts.colorize("("+note+")", logx.ColorDim) + "\n")
	if socket.Error != "" {
		text.WriteString(opts.colorize("(frames no longer followed: "+displaySafe(socket.Error)+")", logx.ColorYellow) + "\n")
	}
	for _, message := range stored.List {
		writeMessage(text, message, opts)
	}
}

func writeMessage(text *strings.Builder, message netcapture.Message, opts RenderOptions) {
	marker, color := "<", logx.ColorGreen
	if message.From == netcapture.FromClient {
		marker, color = ">", logx.ColorCyan
	}
	head := marker + " " + displaySafe(message.From)
	if at, err := time.Parse(netlog.TimeFormat, message.Timestamp); err == nil {
		head += " " + at.In(opts.location()).Format("15:04:05.000")
	}
	head += " " + displaySafe(message.Type)
	if message.Code != 0 {
		head += fmt.Sprintf(" %d", message.Code)
	}
	size := util.FormatBytes(message.StoredBytes)
	if message.Truncated {
		size += " of " + util.FormatBytes(message.Bytes) + ", truncated"
	}
	if len(message.Base64) > 0 {
		size += ", binary"
	}
	text.WriteString("\n" + opts.colorize(head, color) + " " + opts.colorize("("+size+")", logx.ColorDim) + "\n")
	if message.Text != "" {
		content := displaySafeText(message.Text)
		text.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			text.WriteString("\n")
		}
	}
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
		data = netcapture.TrimPartialRune(data)
	}
	if !isText(data) {
		return "", "binary"
	}
	return displaySafeText(string(data)), ""
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
