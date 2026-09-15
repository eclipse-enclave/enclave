// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bytes"
	"io"
	"regexp"
	"strings"
)

// buildahMissingArgPattern matches buildah's warning for an ARG that was
// declared without a default and not given a --build-arg, in the key=value
// form logrus uses when stderr is a pipe and the bracketed form it uses on a
// terminal. BuildKit prints nothing in that case.
var buildahMissingArgPattern = regexp.MustCompile(
	`^(?:time="[^"]*" level=warning msg="missing \\"([^"\\]+)\\" build argument\. .*"` +
		`|WARN\[[0-9]+\] missing "([^"]+)" build argument\. .*)\s*$`)

// buildahWarningFilter drops buildah's missing-build-argument warnings for the
// ARGs a build leaves unset on purpose and forwards everything else unchanged.
// Only the start of a line that could still become such a warning is held
// back, so step output that arrives without a newline is not delayed.
type buildahWarningFilter struct {
	out     io.Writer
	names   map[string]bool
	held    []byte // start of the current line while it may still be a warning
	passing bool   // the start of the current line went out; pass the rest
}

func newBuildahWarningFilter(out io.Writer, names []string) *buildahWarningFilter {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	return &buildahWarningFilter{out: out, names: set}
}

// Write consumes all of p; it reports a short count only when the underlying
// writer fails.
func (f *buildahWarningFilter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		var err error
		switch {
		case f.passing && idx < 0:
			err = f.write(p)
			p = nil
		case f.passing:
			err = f.write(p[:idx+1])
			f.passing = false
			p = p[idx+1:]
		case idx < 0:
			f.held = append(f.held, p...)
			p = nil
			if !f.mayBeWarning() {
				err = f.release()
			}
		default:
			line := append(f.held, p[:idx+1]...)
			f.held = nil
			p = p[idx+1:]
			if !f.drop(line) {
				err = f.write(line)
			}
		}
		if err != nil {
			return 0, err
		}
	}
	return n, nil
}

// Flush forwards whatever is still held once the engine has exited.
func (f *buildahWarningFilter) Flush() error {
	held := f.held
	f.held = nil
	f.passing = false
	return f.write(held)
}

// mayBeWarning reports whether the held start of a line could still turn
// into a missing-build-argument warning.
func (f *buildahWarningFilter) mayBeWarning() bool {
	if len(f.held) > buildLogLineBytes {
		return false
	}
	for _, prefix := range []string{`time="`, "WARN["} {
		if len(f.held) < len(prefix) {
			if strings.HasPrefix(prefix, string(f.held)) {
				return true
			}
			continue
		}
		if bytes.HasPrefix(f.held, []byte(prefix)) {
			return true
		}
	}
	return false
}

// release forwards the held start of a line that is not a warning; the rest
// of the line then passes straight through as it arrives.
func (f *buildahWarningFilter) release() error {
	held := f.held
	f.held = nil
	f.passing = true
	return f.write(held)
}

func (f *buildahWarningFilter) drop(line []byte) bool {
	m := buildahMissingArgPattern.FindSubmatch(line)
	if m == nil {
		return false
	}
	name := m[1]
	if len(name) == 0 {
		name = m[2]
	}
	return f.names[string(name)]
}

func (f *buildahWarningFilter) write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	_, err := f.out.Write(p)
	return err
}

// engineOutput wraps the build output with the filtering the engine needs;
// flush must run once the engine has exited.
func engineOutput(out io.Writer, req BuildRequest) (w io.Writer, flush func()) {
	if !IsPodman() || len(req.OptionalBuildArgs) == 0 {
		return out, func() {}
	}
	filter := newBuildahWarningFilter(out, req.OptionalBuildArgs)
	return filter, func() { _ = filter.Flush() } // #nosec G104 -- best-effort flush of held output.
}
