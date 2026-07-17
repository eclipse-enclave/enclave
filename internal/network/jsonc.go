// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

// StripJSONCComments removes // and /* */ comments from JSONC input,
// returning valid JSON. Comments inside JSON string literals are preserved.
func StripJSONCComments(input []byte) []byte {
	out := make([]byte, 0, len(input))
	i := 0
	n := len(input)

	for i < n {
		// Check for string literal
		if input[i] == '"' {
			out = append(out, '"')
			i++
			for i < n {
				if input[i] == '\\' && i+1 < n {
					out = append(out, input[i], input[i+1])
					i += 2
					continue
				}
				if input[i] == '"' {
					out = append(out, '"')
					i++
					break
				}
				out = append(out, input[i])
				i++
			}
			continue
		}

		// Check for line comment
		if i+1 < n && input[i] == '/' && input[i+1] == '/' {
			i += 2
			for i < n && input[i] != '\n' {
				i++
			}
			continue
		}

		// Check for block comment
		if i+1 < n && input[i] == '/' && input[i+1] == '*' {
			i += 2
			for i+1 < n {
				if input[i] == '*' && input[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			if i >= n {
				// Unterminated block comment — skip remaining
				break
			}
			continue
		}

		out = append(out, input[i])
		i++
	}

	return out
}
