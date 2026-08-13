// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"enclave/internal/logx"
)

// Style selects the output form. Human form is for a terminal; machine form is
// tab separated so `cut -f` and `awk` work on a pipe or a redirect.
type Style string

const (
	StyleHuman   Style = "human"
	StyleMachine Style = "machine"
)

// Glyphs are single width so columns stay aligned in any terminal font.
const (
	glyphPass = "✓"
	glyphDeny = "✗"
)

// Human column widths. Values longer than their column push the rest of the
// row right rather than being truncated, so nothing is silently lost.
const (
	widthTime   = 8
	widthMethod = 5
	widthDomain = 24
	widthPath   = 20
	widthStatus = 3
)

// machineColumns is the fixed column order of the machine form. Consumers rely
// on the positions, so append rather than reorder.
var machineColumns = []string{
	"timestamp", "verdict", "type", "method", "domain",
	"path", "status", "req_bytes", "resp_bytes", "rule", "session",
}

// MachineColumns returns the machine-form column names in order.
func MachineColumns() []string {
	return append([]string(nil), machineColumns...)
}

// RenderOptions controls how events are turned into text.
type RenderOptions struct {
	Style Style
	// Color adds SGR sequences to human output. It is always false in machine
	// form so escapes never reach a file.
	Color bool
	// Location renders human timestamps in the caller's zone. Nil means local.
	Location *time.Location
}

func (o RenderOptions) location() *time.Location {
	if o.Location == nil {
		return time.Local
	}
	return o.Location
}

func (o RenderOptions) machine() bool {
	return o.Style != StyleHuman
}

// colorize keeps rendering pure: the caller decides whether colour is allowed,
// so the same events render identically in a test with no terminal.
func (o RenderOptions) colorize(text string, color logx.Color) string {
	if !o.Color || o.machine() || color == "" {
		return text
	}
	return string(color) + text + string(logx.ColorReset)
}

// RenderEvents renders a full event stream. In human form a session marker
// becomes a boundary header carrying that session's verdict counts; in machine
// form it is a row like any other.
func RenderEvents(events []Event, opts RenderOptions) string {
	var out strings.Builder
	if opts.machine() {
		for _, event := range events {
			out.WriteString(RenderEvent(event, opts))
			out.WriteString("\n")
		}
		return out.String()
	}

	for index, event := range events {
		if event.IsSessionMarker() {
			if index > 0 {
				out.WriteString("\n")
			}
			pass, deny := countUntilNextMarker(events[index+1:])
			out.WriteString(RenderSessionHeader(event, pass, deny, opts))
			out.WriteString("\n\n")
			continue
		}
		out.WriteString(RenderEvent(event, opts))
		out.WriteString("\n")
	}
	return out.String()
}

func countUntilNextMarker(events []Event) (pass int, deny int) {
	for _, event := range events {
		if event.IsSessionMarker() {
			return pass, deny
		}
		if strings.EqualFold(event.Verdict, VerdictDeny) {
			deny++
			continue
		}
		pass++
	}
	return pass, deny
}

// RenderEvent renders one event as a single line without its newline.
func RenderEvent(event Event, opts RenderOptions) string {
	if opts.machine() {
		return renderMachineRow(event)
	}
	if event.IsSessionMarker() {
		return RenderSessionHeader(event, 0, 0, opts)
	}
	return renderHumanRow(event, opts)
}

// RenderSessionHeader renders a session boundary. Counts of zero are omitted so
// a live follow, which cannot know them yet, still prints a clean boundary.
func RenderSessionHeader(event Event, pass int, deny int, opts RenderOptions) string {
	if opts.machine() {
		return renderMachineRow(event)
	}
	session := strings.TrimSpace(event.Session)
	if session == "" {
		session = "unknown session"
	}
	line := " " + opts.colorize("SESSION", logx.ColorCyan) + "  " + session
	if at, ok := event.Time(); ok {
		line += "  " + opts.colorize(at.In(opts.location()).Format("2006-01-02 15:04:05"), logx.ColorDim)
	}
	if pass > 0 || deny > 0 {
		line += fmt.Sprintf("  %s  %s",
			opts.colorize(strconv.Itoa(pass)+" pass", logx.ColorGreen),
			opts.colorize(strconv.Itoa(deny)+" deny", logx.ColorRed))
	}
	return line
}

func renderHumanRow(event Event, opts RenderOptions) string {
	timestamp := "--:--:--"
	if at, ok := event.Time(); ok {
		timestamp = at.In(opts.location()).Format("15:04:05")
	}

	glyph := glyphPass
	glyphColor := logx.ColorGreen
	if strings.EqualFold(event.Verdict, VerdictDeny) {
		glyph = glyphDeny
		glyphColor = logx.ColorRed
	}

	status := ""
	statusColor := logx.Color("")
	if event.Status > 0 {
		status = strconv.Itoa(event.Status)
		if event.Status >= 400 {
			statusColor = logx.ColorYellow
		}
	}

	var line strings.Builder
	line.WriteString(" ")
	line.WriteString(opts.colorize(pad(timestamp, widthTime), logx.ColorDim))
	line.WriteString("  ")
	line.WriteString(opts.colorize(glyph, glyphColor))
	line.WriteString("  ")
	line.WriteString(pad(humanKind(event), widthMethod))
	line.WriteString(" ")
	line.WriteString(opts.colorize(pad(humanDomain(event), widthDomain), logx.ColorCyan))
	line.WriteString(" ")
	line.WriteString(opts.colorize(pad(event.Path, widthPath), logx.ColorDim))
	line.WriteString(" ")
	line.WriteString(opts.colorize(padLeft(status, widthStatus), statusColor))
	line.WriteString("  ")
	line.WriteString(humanDetail(event))
	return strings.TrimRight(line.String(), " ")
}

// humanKind collapses type and method into one column: the ambiguity between a
// method and an event type does not matter to a reader, and machine form keeps
// them separate for tools that care.
func humanKind(event Event) string {
	if method := strings.TrimSpace(event.Method); method != "" {
		return method
	}
	return event.Type
}

func humanDomain(event Event) string {
	domain := strings.TrimSpace(event.Domain)
	if domain == "" {
		return ""
	}
	if event.Type == TypeTCP && event.Port > 0 {
		return domain + ":" + strconv.Itoa(event.Port)
	}
	return domain
}

// humanDetail is the trailing column: the matched rule when the event was
// denied or carried no payload, and the transferred size otherwise.
func humanDetail(event Event) string {
	if strings.EqualFold(event.Verdict, VerdictDeny) {
		return event.Rule
	}
	if event.ResponseSize > 0 {
		return FormatBytes(event.ResponseSize)
	}
	if event.RequestSize > 0 {
		return FormatBytes(event.RequestSize)
	}
	return event.Rule
}

func renderMachineRow(event Event) string {
	fields := []string{
		dash(event.Timestamp),
		dash(strings.ToUpper(event.Verdict)),
		dash(event.Type),
		dash(event.Method),
		dash(event.Domain),
		dash(event.Path),
		numberCell(event.Status, event.Status > 0),
		numberCell64(event.RequestSize, hasPayload(event)),
		numberCell64(event.ResponseSize, hasPayload(event)),
		dash(event.Rule),
		dash(event.Session),
	}
	return strings.Join(fields, "\t")
}

// hasPayload reports whether byte counts are meaningful for the event type. DNS
// and session events have none, so their byte columns stay "-" rather than
// claiming a transfer of zero.
func hasPayload(event Event) bool {
	switch event.Type {
	case TypeHTTP, TypeTCP:
		return true
	default:
		return false
	}
}

// RenderSummary renders a per-domain aggregate.
func RenderSummary(summary Summary, opts RenderOptions) string {
	if opts.machine() {
		var out strings.Builder
		for _, entry := range summary.Domains {
			out.WriteString(strings.Join([]string{
				entry.Domain,
				strconv.Itoa(entry.Pass),
				strconv.Itoa(entry.Deny),
				strconv.FormatInt(entry.Sent, 10),
				strconv.FormatInt(entry.Received, 10),
				timestampCell(entry.FirstSeen),
				timestampCell(entry.LastSeen),
			}, "\t"))
			out.WriteString("\n")
		}
		return out.String()
	}

	domainWidth := len("DOMAIN")
	for _, entry := range summary.Domains {
		if len(entry.Domain) > domainWidth {
			domainWidth = len(entry.Domain)
		}
	}

	var out strings.Builder
	out.WriteString(" " + pad("DOMAIN", domainWidth) + "  " +
		padLeft("PASS", 5) + "  " + padLeft("DENY", 5) + "  " +
		padLeft("SENT", 9) + "  " + padLeft("RECV", 9) + "  LAST\n")
	for _, entry := range summary.Domains {
		last := ""
		if !entry.LastSeen.IsZero() {
			last = entry.LastSeen.In(opts.location()).Format("15:04:05")
		}
		line := " " + opts.colorize(pad(entry.Domain, domainWidth), logx.ColorCyan) + "  " +
			opts.colorize(padLeft(strconv.Itoa(entry.Pass), 5), logx.ColorGreen) + "  " +
			opts.colorize(padLeft(strconv.Itoa(entry.Deny), 5), denyColor(entry.Deny)) + "  " +
			padLeft(byteCell(entry.Sent), 9) + "  " +
			padLeft(byteCell(entry.Received), 9) + "  " +
			opts.colorize(last, logx.ColorDim)
		out.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	if len(summary.Domains) > 0 {
		out.WriteString(" " + strings.Repeat(" ", domainWidth) + "  " +
			padLeft(strings.Repeat("─", 4), 5) + "  " + padLeft(strings.Repeat("─", 4), 5) + "\n")
		out.WriteString(" " + strings.Repeat(" ", domainWidth) + "  " +
			padLeft(strconv.Itoa(summary.TotalPass), 5) + "  " +
			padLeft(strconv.Itoa(summary.TotalDeny), 5) + "\n")
	}
	return out.String()
}

func denyColor(deny int) logx.Color {
	if deny > 0 {
		return logx.ColorRed
	}
	return ""
}

func byteCell(bytes int64) string {
	if bytes <= 0 {
		return "-"
	}
	return FormatBytes(bytes)
}

func timestampCell(at time.Time) string {
	if at.IsZero() {
		return "-"
	}
	return at.UTC().Format(TimeFormat)
}

func numberCell(value int, present bool) string {
	if !present {
		return "-"
	}
	return strconv.Itoa(value)
}

func numberCell64(value int64, present bool) string {
	if !present {
		return "-"
	}
	return strconv.FormatInt(value, 10)
}

func dash(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "-"
	}
	return trimmed
}

func pad(value string, width int) string {
	if len([]rune(value)) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len([]rune(value)))
}

func padLeft(value string, width int) string {
	if len([]rune(value)) >= width {
		return value
	}
	return strings.Repeat(" ", width-len([]rune(value))) + value
}
