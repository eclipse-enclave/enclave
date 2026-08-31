// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"enclave/internal/logx"
)

// A run touches several extensions, so every block this package prints opens
// with a rule carrying the extension's name. Without that separator the blocks
// of a multi-extension run read as one wall of text.
const (
	ruleGlyph  = "━"
	ruleLeadIn = 2 // rule cells before the name
	ruleTailIn = 2 // rule cells after the right-hand caption
	minRuleGap = 2 // rule cells between the name and the caption

	defaultWidth = 80
	// maxWidth keeps a rule from spanning a very wide terminal, where a
	// full-width line is harder to read than a short one.
	maxWidth = 96
	minWidth = 40

	bodyIndent = "  "
	// labelGap separates the label column from the value column.
	labelGap = 2
)

// Style is how narration is decorated. The zero value is plain text at a
// default width, which is what a pipe and a test buffer want.
type Style struct {
	Color bool
	Width int
}

// TerminalStyle resolves the style for a stream: color when the stream is a
// terminal that wants it, wrapped to that terminal's width.
func TerminalStyle(f *os.File) Style {
	style := Style{Color: logx.ColorEnabledFor(f)}
	if f == nil {
		return style
	}
	if cols, _, err := term.GetSize(int(f.Fd())); err == nil { // #nosec G115 -- file descriptor from Fd() fits in int on all supported platforms.
		style.Width = cols
	}
	return style
}

func (s Style) width() int {
	if s.Width <= 0 {
		return defaultWidth
	}
	return min(max(s.Width, minWidth), maxWidth)
}

func (s Style) paint(text string, color logx.Color) string {
	if !s.Color || color == "" || text == "" {
		return text
	}
	return string(color) + text + string(logx.ColorReset)
}

// section opens a block for one extension: a rule carrying the name on the
// left and a caption such as `tool @ 8f0bbc7` on the right. When the two do not
// fit on one line the caption moves below the rule rather than being truncated.
func (e Env) section(name string, caption string) {
	w := e.narrate()
	style := e.Style
	width := style.width()
	nameCells := ruleLeadIn + 1 + displayWidth(name) + 1 // "━━ name "
	painted := style.paint(name, logx.ColorBold)

	if caption == "" {
		fill := width - nameCells
		_, _ = fmt.Fprintf(w, "\n%s %s %s\n\n",
			style.paint(strings.Repeat(ruleGlyph, ruleLeadIn), logx.ColorDim),
			painted,
			style.paint(strings.Repeat(ruleGlyph, max(fill, minRuleGap)), logx.ColorDim))
		return
	}

	captionCells := 1 + displayWidth(caption) + 1 + ruleTailIn // " caption ━━"
	fill := width - nameCells - captionCells
	if fill < minRuleGap {
		_, _ = fmt.Fprintf(w, "\n%s %s %s\n%s%s\n\n",
			style.paint(strings.Repeat(ruleGlyph, ruleLeadIn), logx.ColorDim),
			painted,
			style.paint(strings.Repeat(ruleGlyph, minRuleGap), logx.ColorDim),
			bodyIndent,
			style.paint(caption, logx.ColorDim))
		return
	}
	_, _ = fmt.Fprintf(w, "\n%s %s %s %s %s\n\n",
		style.paint(strings.Repeat(ruleGlyph, ruleLeadIn), logx.ColorDim),
		painted,
		style.paint(strings.Repeat(ruleGlyph, fill), logx.ColorDim),
		style.paint(caption, logx.ColorDim),
		style.paint(strings.Repeat(ruleGlyph, ruleTailIn), logx.ColorDim))
}

// rowSet buffers label/value pairs so the value column can be aligned to the
// widest label actually present instead of a constant that every caller has to
// keep in sync.
type rowSet struct {
	labels []string
	values []string
}

func (r *rowSet) add(label string, value string) {
	if value == "" {
		return
	}
	r.labels = append(r.labels, label)
	r.values = append(r.values, value)
}

func (r *rowSet) empty() bool { return len(r.labels) == 0 }

// render writes the buffered rows, wrapping each value into the column so a
// long list of domains or directives stays inside the terminal.
func (r *rowSet) render(w io.Writer, style Style) {
	labelWidth := 0
	for _, label := range r.labels {
		labelWidth = max(labelWidth, displayWidth(label))
	}
	valueColumn := len(bodyIndent) + labelWidth + labelGap
	valueWidth := max(style.width()-valueColumn, minWidth/2)
	continuation := strings.Repeat(" ", valueColumn)

	for i, label := range r.labels {
		padding := strings.Repeat(" ", labelWidth-displayWidth(label)+labelGap)
		lines := wrapValue(r.values[i], valueWidth)
		_, _ = fmt.Fprintf(w, "%s%s%s%s\n", bodyIndent, style.paint(label, logx.ColorDim), padding, lines[0])
		for _, line := range lines[1:] {
			_, _ = fmt.Fprintf(w, "%s%s\n", continuation, line)
		}
	}
}

// wrapValue breaks a value at spaces to fit width. Comma-joined lists break
// after their separators for free. A single token longer than the column (a
// long path, say) is left whole so it stays selectable in one piece.
func wrapValue(value string, width int) []string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{value}
	}
	lines := []string{}
	current := words[0]
	for _, word := range words[1:] {
		if displayWidth(current)+1+displayWidth(word) > width {
			lines = append(lines, current)
			current = word
			continue
		}
		current += " " + word
	}
	return append(lines, current)
}

// Outcome markers. Narration is a report on work already attempted, so these
// mark how each line landed rather than decorating it.
const (
	markOK   = "✓"
	markWarn = "!"
	markInfo = "·"
)

func (e Env) outcome(mark string, color logx.Color, format string, args ...any) {
	_, _ = fmt.Fprintf(e.narrate(), "%s%s %s\n", bodyIndent, e.Style.paint(mark, color), fmt.Sprintf(format, args...))
}

// note is a continuation under the outcome line it qualifies, indented past the
// marker so the two read as one item.
func (e Env) note(format string, args ...any) {
	_, _ = fmt.Fprintf(e.narrate(), "%s  %s\n", bodyIndent, e.Style.paint(fmt.Sprintf(format, args...), logx.ColorDim))
}

// unchanged reports a no-op on one line instead of opening a section for it.
// `update` with no names walks every installed extension, and most of them are
// usually already current; a rule per no-op would bury the ones that changed.
func (e Env) unchanged(name string, format string, args ...any) {
	_, _ = fmt.Fprintf(e.narrate(), "%s%s %s %s\n",
		bodyIndent,
		e.Style.paint(markOK, logx.ColorGreen),
		name,
		e.Style.paint(fmt.Sprintf(format, args...), logx.ColorDim))
}

// summarize closes a run that touched more than one extension with a count per
// outcome, so the tail of the output answers "what just happened" without
// scrolling back through the blocks. A dry run gets none: every result is
// ActionSkipped there, and "2 skipped" describes the rehearsal rather than the
// two installs it just walked through.
func (e Env) summarize(kind string, results []ActionResult, dryRun bool) {
	if len(results) < 2 || dryRun {
		return
	}
	order := []string{ActionInstalled, ActionUpdated, ActionUnchanged, ActionRemoved, ActionSkipped, ActionFailed}
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Action]++
	}
	parts := make([]string, 0, len(order))
	for _, action := range order {
		if counts[action] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[action], action))
		}
	}
	if len(parts) == 0 {
		return
	}
	color := logx.ColorGreen
	if counts[ActionFailed] > 0 {
		color = logx.ColorYellow
	}
	_, _ = fmt.Fprintf(e.narrate(), "\n%s\n", e.Style.paint(fmt.Sprintf("%d %ss: %s", len(results), kind, strings.Join(parts, ", ")), color))
}

// displayWidth counts terminal cells. Every glyph this package prints is
// single-width, so runes are the right unit and bytes are not: `credential →
// host` is 17 cells and 19 bytes.
func displayWidth(s string) int { return utf8.RuneCountInString(s) }
