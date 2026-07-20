// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package logx

import (
	"io"
	"os"
)

// writerIsRawTTY reports whether w is a terminal currently in raw mode, i.e.
// with output post-processing disabled. A raw terminal no longer translates
// "\n" into "\r\n", so a log line written while an interactive attach (e.g.
// `docker run -it`) holds the terminal raw needs explicit carriage returns to
// keep following output column-aligned.
func writerIsRawTTY(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	return terminalIsRaw(file.Fd())
}
