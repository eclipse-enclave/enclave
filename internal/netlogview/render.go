// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlogview

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"enclave/internal/logx"
	"enclave/internal/netlog"
	"enclave/internal/util"
)

// Glyphs are single width so columns stay aligned in any terminal font.
const (
	glyphPass = "✓"
	glyphDeny = "✗"
)

// Column widths. Values longer than their column push the rest of the row right
// rather than being truncated, so nothing is silently lost.
const (
	widthTime   = 8
	widthMethod = 5
	widthDomain = 24
	widthPath   = 20
	widthStatus = 3
)

// RenderOptions controls how events are turned into text. This is the terminal
// form; machine consumers read the JSON the reader emits verbatim.
type RenderOptions struct {
	Color bool
	// Location renders timestamps in the caller's zone. Nil means local.
	Location *time.Location
}

func (o RenderOptions) location() *time.Location {
	if o.Location == nil {
		return time.Local
	}
	return o.Location
}

// colorize keeps rendering pure: the caller decides whether colour is allowed,
// so the same events render identically in a test with no terminal.
func (o RenderOptions) colorize(text string, color logx.Color) string {
	if !o.Color || color == "" {
		return text
	}
	return string(color) + text + string(logx.ColorReset)
}

// WriteEvents renders a full event stream into out, one row at a time, so a
// large log streams through the caller's buffer instead of being assembled in
// memory first. A session marker becomes a boundary header carrying that
// session's verdict counts.
func WriteEvents(out io.StringWriter, events []netlog.Event, opts RenderOptions) error {
	for index, event := range events {
		if event.IsSessionMarker() {
			separator := ""
			if index > 0 {
				separator = "\n"
			}
			pass, deny := countUntilNextMarker(events[index+1:])
			if err := writeParts(out, separator, renderSessionHeader(event, pass, deny, opts), "\n\n"); err != nil {
				return err
			}
			continue
		}
		if err := writeParts(out, renderHumanRow(event, opts), "\n"); err != nil {
			return err
		}
	}
	return nil
}

// WriteEvent renders one event arriving after the backlog, for callers
// following a live log, with the same session separation WriteEvents gives it.
func WriteEvent(out io.StringWriter, event netlog.Event, opts RenderOptions) error {
	if event.IsSessionMarker() {
		return writeParts(out, "\n", renderSessionHeader(event, 0, 0, opts), "\n\n")
	}
	return writeParts(out, renderHumanRow(event, opts), "\n")
}

func writeParts(out io.StringWriter, parts ...string) error {
	for _, part := range parts {
		if _, err := out.WriteString(part); err != nil {
			return err
		}
	}
	return nil
}

func countUntilNextMarker(events []netlog.Event) (pass int, deny int) {
	for _, event := range events {
		if event.IsSessionMarker() {
			return pass, deny
		}
		if strings.EqualFold(event.Verdict, netlog.VerdictDeny) {
			deny++
			continue
		}
		pass++
	}
	return pass, deny
}

// renderSessionHeader renders a session boundary. Counts of zero are omitted so
// a live follow, which cannot know them yet, still prints a clean boundary.
func renderSessionHeader(event netlog.Event, pass int, deny int, opts RenderOptions) string {
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

func renderHumanRow(event netlog.Event, opts RenderOptions) string {
	timestamp := "--:--:--"
	if at, ok := event.Time(); ok {
		timestamp = at.In(opts.location()).Format("15:04:05")
	}

	glyph := glyphPass
	glyphColor := logx.ColorGreen
	if strings.EqualFold(event.Verdict, netlog.VerdictDeny) {
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
	// Enough for a typical row with colour escapes, so the builder does not have
	// to grow four times per event.
	line.Grow(160)
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
// method and an event type does not matter to a reader, and the JSON form keeps
// them separate for tools that care.
func humanKind(event netlog.Event) string {
	if method := strings.TrimSpace(event.Method); method != "" {
		return method
	}
	return event.Type
}

func humanDomain(event netlog.Event) string {
	domain := strings.TrimSpace(event.Domain)
	if domain == "" {
		return ""
	}
	if event.Type == netlog.TypeTCP && event.Port > 0 {
		return domain + ":" + strconv.Itoa(event.Port)
	}
	return domain
}

// humanDetail is the trailing column: the matched rule when the event was
// denied or carried no payload, and the transferred size otherwise.
func humanDetail(event netlog.Event) string {
	if strings.EqualFold(event.Verdict, netlog.VerdictDeny) {
		return event.Rule
	}
	if event.ResponseSize > 0 {
		return util.FormatBytes(event.ResponseSize)
	}
	if event.RequestSize > 0 {
		return util.FormatBytes(event.RequestSize)
	}
	return event.Rule
}

// RenderSummary renders a per-domain aggregate.
func RenderSummary(summary Summary, opts RenderOptions) string {
	domainWidth := len("DOMAIN")
	for _, entry := range summary.Domains {
		if cell := utf8.RuneCountInString(summaryDomainCell(entry.Domain)); cell > domainWidth {
			domainWidth = cell
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
		line := " " + opts.colorize(pad(summaryDomainCell(entry.Domain), domainWidth), logx.ColorCyan) + "  " +
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

// summaryDomainCell labels the domainless group Aggregate produces.
func summaryDomainCell(domain string) string {
	if domain == "" {
		return "(no domain)"
	}
	return domain
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
	return util.FormatBytes(bytes)
}

func pad(value string, width int) string {
	missing := width - utf8.RuneCountInString(value)
	if missing <= 0 {
		return value
	}
	return value + strings.Repeat(" ", missing)
}

func padLeft(value string, width int) string {
	missing := width - utf8.RuneCountInString(value)
	if missing <= 0 {
		return value
	}
	return strings.Repeat(" ", missing) + value
}
