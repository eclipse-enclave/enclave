// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"enclave/internal/filter"
)

// bindInput is what bind sources read from.
type bindInput struct {
	captures        map[string]string
	query           url.Values
	header          http.Header
	body            []byte
	bodyContentType string
	response        []byte
	responseType    string
	// jsonKeys is the policy's JSONKeys mode.
	jsonKeys string
}

// recipientHeaders are the fields recipients are read from. Gmail sends to
// To, Cc, and Bcc only. The Resent fields are checked because other servers
// deliver to them. Trace fields such as Delivered-To and X-Original-To never
// add a recipient, so they are not read.
var recipientHeaders = []string{"To", "Cc", "Bcc", "Resent-To", "Resent-Cc", "Resent-Bcc"}

// replyHeaders direct replies, read receipts, and bounces. The destinations
// mode reads them with recipientHeaders. From and Sender are not read: they
// name the account, and servers such as Gmail replace an address the
// account does not own.
var replyHeaders = []string{"Reply-To", "Mail-Reply-To", "Mail-Followup-To", "Disposition-Notification-To", "Return-Receipt-To", "Errors-To"}

// runBind executes a validated pipeline. Every step yields a list of strings.
// A json step that reads the body, the response, or a multipart part needs
// it to declare JSON, see jsonMediaType.
func runBind(steps []filter.BindStep, in bindInput) ([]string, error) {
	var values []string
	var contentType string
	// typed is set while values hold a body, response, or part of
	// contentType.
	typed := false
	for i, step := range steps {
		var err error
		inputTyped := typed
		typed = false
		switch step.Op {
		case filter.StepPath:
			value, ok := in.captures[step.Arg]
			if !ok {
				return nil, fmt.Errorf("path capture %s is missing", step.Arg)
			}
			values = []string{value}
		case filter.StepQuery:
			values = queryValues(in.query, filter.QueryParamNames(step.Arg, in.jsonKeys))
		case filter.StepHeader:
			values = append([]string(nil), in.header.Values(step.Arg)...)
		case filter.StepBody:
			values = []string{string(in.body)}
			contentType, typed = in.bodyContentType, true
		case filter.StepResponse:
			values = []string{string(in.response)}
			contentType, typed = in.responseType, true
		case filter.StepMultipart:
			var partType string
			values, err = mapValues(values, func(v string) ([]string, error) {
				parts, err := parseMultipart([]byte(v), contentType)
				if err != nil {
					return nil, err
				}
				part, err := selectPart(parts, step)
				partType = part.contentType
				return []string{string(part.data)}, err
			})
			contentType, typed = partType, true
		case filter.StepJSON:
			path, perr := filter.ParseJSONPath(step.Arg)
			if perr != nil {
				return nil, perr
			}
			if inputTyped && !jsonMediaType(contentType) {
				err = errors.New("the input is not declared as JSON (application/json)")
				break
			}
			if in.jsonKeys == filter.JSONKeysProtobuf {
				path = path.WithProtobufNames()
			}
			values, err = mapValues(values, func(v string) ([]string, error) {
				doc, err := filter.DecodeJSONKeys([]byte(v), in.jsonKeys)
				if err != nil {
					return nil, err
				}
				return path.SelectStrings(doc)
			})
		case filter.StepBase64URL:
			values, err = mapValues(values, func(v string) ([]string, error) {
				decoded, err := decodeBase64URL(v)
				return []string{string(decoded)}, err
			})
		case filter.StepRFC822:
			headers := recipientHeaders
			if step.Arg == filter.RFC822Destinations {
				headers = append(slices.Clone(recipientHeaders), replyHeaders...)
			}
			values, err = mapValues(values, func(v string) ([]string, error) {
				return rfc822Addresses(v, headers)
			})
		case filter.StepSplit:
			values, err = mapValues(values, func(v string) ([]string, error) {
				var out []string
				for _, part := range strings.Split(v, step.Arg) {
					if trimmed := strings.TrimSpace(part); trimmed != "" {
						out = append(out, trimmed)
					}
				}
				return out, nil
			})
		default:
			return nil, fmt.Errorf("unknown step %q", step.Op)
		}
		if err != nil {
			return nil, fmt.Errorf("step %d (%s): %w", i, step.Op, err)
		}
	}
	return values, nil
}

func mapValues(values []string, fn func(string) ([]string, error)) ([]string, error) {
	var out []string
	for _, v := range values {
		mapped, err := fn(v)
		if err != nil {
			return nil, err
		}
		out = append(out, mapped...)
	}
	return out, nil
}

// decodeBase64URL accepts URL-safe and standard base64, padded or not.
func decodeBase64URL(v string) ([]byte, error) {
	if strings.ContainsAny(v, " \t\r\n") {
		return nil, errors.New("base64 value contains whitespace")
	}
	normalized := strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(v), "=")
	decoded, err := base64.RawURLEncoding.DecodeString(normalized)
	if err != nil {
		return nil, errors.New("invalid base64")
	}
	return decoded, nil
}

// rfc822Addresses collects the addresses of every field of a message named
// in headers, with ASCII letters lowercased. Any parse failure is an error.
func rfc822Addresses(raw string, headers []string) ([]string, error) {
	fields, err := readHeaderFields(raw)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range fields {
		header, ok := addressHeader(f.name, headers)
		if !ok || strings.TrimSpace(f.value) == "" {
			continue
		}
		// ParseAddressList resolves groups to their members and fails on
		// a group it cannot resolve, such as one without its closing ";".
		list, err := mail.ParseAddressList(f.value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s header", header)
		}
		for _, addr := range list {
			out = append(out, asciiLower(addr.Address))
		}
	}
	return out, nil
}

func addressHeader(name string, headers []string) (string, bool) {
	for _, h := range headers {
		if strings.EqualFold(name, h) {
			return h, true
		}
	}
	return "", false
}

type headerField struct {
	name  string
	value string
}

// folded is a header field while its continuation lines are read.
type folded struct {
	name  string
	lines []string
}

// readHeaderFields splits the header block of a message, up to the first
// empty line or the end, into unfolded fields. Unlike net/mail it rejects
// every form a mail server could split or name differently: line endings
// that are not all CRLF or all LF, a bare CR, NUL, a line without a colon, a
// field name that is empty or holds whitespace, control, or non-ASCII
// characters or an encoded word, and folding that starts the block or
// consists of whitespace only, which some parsers read as the end of the
// header block. Whitespace between a field name and its colon is obsolete
// syntax (RFC 5322 section 4.5.3) that servers still accept, so it is
// dropped and "Bcc :" names Bcc.
func readHeaderFields(raw string) ([]headerField, error) {
	eol := "\n"
	if i := strings.IndexAny(raw, "\r\n"); i >= 0 && raw[i] == '\r' {
		eol = "\r\n"
	}
	var pending []folded
	for rest := raw; rest != ""; {
		var line string
		line, rest, _ = strings.Cut(rest, eol)
		if strings.ContainsAny(line, "\r\n") {
			return nil, errors.New("inconsistent line endings in message headers")
		}
		if strings.ContainsRune(line, 0) {
			return nil, errors.New("NUL in message headers")
		}
		if line == "" {
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			if len(pending) == 0 {
				return nil, errors.New("message headers start with a folded line")
			}
			if strings.Trim(line, " \t") == "" {
				return nil, errors.New("whitespace-only folded line in message headers")
			}
			last := &pending[len(pending)-1]
			last.lines = append(last.lines, line)
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, errors.New("malformed message header line")
		}
		name = strings.TrimRight(name, " \t")
		if err := checkFieldName(name); err != nil {
			return nil, err
		}
		pending = append(pending, folded{name: name, lines: []string{value}})
	}
	fields := make([]headerField, len(pending))
	for i, f := range pending {
		fields[i] = headerField{name: f.name, value: strings.Join(f.lines, "")}
	}
	return fields, nil
}

// checkFieldName accepts printable ASCII without encoded words, the RFC 5322
// field name syntax.
func checkFieldName(name string) error {
	if name == "" {
		return errors.New("empty message header name")
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < '!' || c > '~' {
			return errors.New("message header name contains whitespace, control, or non-ASCII characters")
		}
	}
	if strings.Contains(name, "=?") {
		return errors.New("message header name contains an encoded word")
	}
	return nil
}

// asciiLower lowercases ASCII letters only, so that no non-ASCII character
// folds into an ASCII one a pattern could match.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

type mimePart struct {
	contentType string
	data        []byte
}

// partHeaders are the only header fields an inspected multipart part may
// carry, lowercase.
var partHeaders = map[string]bool{"content-type": true, "content-transfer-encoding": true, "content-length": true}

// parseMultipart splits a multipart body into its parts and rejects every
// form an upstream could read differently. The Content-Type must name a
// valid boundary once, without RFC 2231, start, or start-info parameters.
// The body must follow RFC 2046 with CRLF line endings: whitespace only
// before the first and after the last delimiter, a delimiter line with
// nothing but optional padding after the boundary, no other occurrence of
// the boundary, and at most filter.MaxMultipartParts parts. Part headers are
// CRLF lines, never folded, and only Content-Type, Content-Transfer-Encoding
// with an identity value (with quoted-printable, "=54o:" reads as "To:"
// upstream), and Content-Length matching the part, each at most once. A
// nested multipart part is denied.
//
// The engine selects parts by position or by the type of the first part,
// which is the root of multipart/related only while nothing names another
// one: start names the root by Content-ID, and a server may honor a
// Content-ID otherwise, so both are denied.
func parseMultipart(body []byte, contentType string) ([]mimePart, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return nil, errors.New("body is not multipart")
	}
	if err := checkMultipartParams(contentType); err != nil {
		return nil, err
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, errors.New("multipart body has no boundary")
	}
	if !validBoundary(boundary) {
		return nil, errors.New("multipart boundary is not valid")
	}
	return splitMultipart(body, boundary)
}

func splitMultipart(body []byte, boundary string) ([]mimePart, error) {
	dash := []byte("--" + boundary)
	delimiter := []byte("\r\n--" + boundary)
	first := bytes.Index(body, dash)
	if first < 0 {
		return nil, errors.New("multipart body has no parts")
	}
	if preamble := body[:first]; len(preamble) > 0 && (!blank(preamble) || !bytes.HasSuffix(preamble, []byte("\r\n"))) {
		return nil, errors.New("multipart body has a preamble")
	}
	rest := body[first+len(dash):]
	var parts []mimePart
	for {
		if after, ok := bytes.CutPrefix(rest, []byte("--")); ok {
			if len(parts) == 0 {
				return nil, errors.New("multipart body has no parts")
			}
			if !blank(after) {
				return nil, errors.New("multipart body has an epilogue")
			}
			return parts, nil
		}
		rest = bytes.TrimLeft(rest, " \t")
		var ok bool
		if rest, ok = bytes.CutPrefix(rest, []byte("\r\n")); !ok {
			return nil, errors.New("invalid multipart delimiter line")
		}
		end := bytes.Index(rest, delimiter)
		if end < 0 {
			return nil, errors.New("invalid multipart body: no closing delimiter")
		}
		content := rest[:end]
		rest = rest[end+len(delimiter):]
		if bytes.Contains(content, dash) {
			return nil, errors.New("multipart part contains its boundary")
		}
		if len(parts) == filter.MaxMultipartParts {
			return nil, fmt.Errorf("multipart body has more than %d parts", filter.MaxMultipartParts)
		}
		part, err := parsePart(content)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
}

// parsePart reads the header block of a part and returns its content.
func parsePart(content []byte) (mimePart, error) {
	var header, data []byte
	if rest, ok := bytes.CutPrefix(content, []byte("\r\n")); ok {
		data = rest
	} else {
		end := bytes.Index(content, []byte("\r\n\r\n"))
		if end < 0 {
			return mimePart{}, errors.New("multipart part headers do not end with an empty CRLF line")
		}
		header, data = content[:end], content[end+4:]
	}
	var part mimePart
	seen := map[string]bool{}
	for _, line := range strings.Split(string(header), "\r\n") {
		if header == nil {
			break
		}
		if strings.ContainsAny(line, "\r\n") {
			return mimePart{}, errors.New("multipart part headers must use CRLF line endings")
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			return mimePart{}, errors.New("multipart part headers must not be folded")
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || leadingToken(name) != name || name == "" {
			return mimePart{}, errors.New("malformed multipart part header")
		}
		key := strings.ToLower(name)
		if !partHeaders[key] {
			return mimePart{}, errors.New("multipart part carries a header other than Content-Type, Content-Transfer-Encoding, and Content-Length")
		}
		if seen[key] {
			return mimePart{}, errors.New("multipart part repeats a header")
		}
		seen[key] = true
		value = strings.Trim(value, " \t")
		for i := 0; i < len(value); i++ {
			if c := value[i]; (c < 0x20 && c != '\t') || c > 0x7e {
				return mimePart{}, errors.New("multipart part header holds control or non-ASCII characters")
			}
		}
		switch key {
		case "content-type":
			mediaType, _, err := mime.ParseMediaType(value)
			if err != nil {
				return mimePart{}, errors.New("multipart part Content-Type does not parse")
			}
			if strings.HasPrefix(mediaType, "multipart/") {
				return mimePart{}, errors.New("nested multipart parts are not allowed")
			}
			part.contentType = value
		case "content-transfer-encoding":
			switch strings.ToLower(value) {
			case "7bit", "8bit", "binary":
			default:
				return mimePart{}, errors.New("multipart part uses a transfer encoding")
			}
		case "content-length":
			if value == "" || strings.Trim(value, "0123456789") != "" || value != strconv.Itoa(len(data)) {
				return mimePart{}, errors.New("multipart part Content-Length does not match the part")
			}
		}
	}
	part.data = data
	return part, nil
}

// validBoundary checks the RFC 2046 boundary syntax: 1 to 70 characters of
// letters, digits, and "'()+_,-./:=? ", not ending with a space.
func validBoundary(boundary string) bool {
	if len(boundary) > 70 || strings.HasSuffix(boundary, " ") {
		return false
	}
	for i := 0; i < len(boundary); i++ {
		if !boundaryChar(boundary[i]) {
			return false
		}
	}
	return true
}

func boundaryChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("'()+_,-./:=? ", c) >= 0
}

// blank reports whether data holds only spaces, tabs, and CRLF line breaks.
func blank(data []byte) bool {
	text := strings.ReplaceAll(string(data), "\r\n", "")
	return strings.Trim(text, " \t") == ""
}

// checkMultipartParams rejects parameters of a multipart Content-Type that
// mime.ParseMediaType merges or drops silently: RFC 2231 extended or
// continued parameters, which can override boundary or hide start, and
// repeated names. It also rejects start and start-info.
func checkMultipartParams(contentType string) error {
	names, err := mediaParamNames(contentType)
	if err != nil {
		return errors.New("invalid multipart Content-Type parameters")
	}
	seen := map[string]bool{}
	for _, name := range names {
		switch {
		case strings.Contains(name, "*"):
			return errors.New("multipart Content-Type uses RFC 2231 parameters")
		case seen[name]:
			return errors.New("multipart Content-Type repeats a parameter")
		case name == "start" || name == "start-info":
			return errors.New("multipart Content-Type names a start part")
		}
		seen[name] = true
	}
	return nil
}

// mediaParamNames returns the lowercased parameter names of a media type in
// order, tokenized like mime.ParseMediaType but before it merges RFC 2231
// continuations. Quoted values with backslash escapes are rejected.
func mediaParamNames(v string) ([]string, error) {
	errSyntax := errors.New("invalid media type parameters")
	base, _, _ := strings.Cut(v, ";")
	rest := v[len(base):]
	var names []string
	for {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if rest == "" {
			return names, nil
		}
		if rest[0] != ';' {
			return nil, errSyntax
		}
		rest = strings.TrimLeftFunc(rest[1:], unicode.IsSpace)
		if rest == "" {
			return names, nil
		}
		name := leadingToken(rest)
		if name == "" {
			return nil, errSyntax
		}
		rest = strings.TrimLeftFunc(rest[len(name):], unicode.IsSpace)
		if !strings.HasPrefix(rest, "=") {
			return nil, errSyntax
		}
		rest = strings.TrimLeftFunc(rest[1:], unicode.IsSpace)
		if strings.HasPrefix(rest, `"`) {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 || strings.Contains(rest[1:1+end], `\`) {
				return nil, errSyntax
			}
			rest = rest[end+2:]
		} else {
			value := leadingToken(rest)
			if value == "" {
				return nil, errSyntax
			}
			rest = rest[len(value):]
		}
		names = append(names, strings.ToLower(name))
	}
}

// leadingToken returns the RFC 2045 token at the start of s.
func leadingToken(s string) string {
	end := strings.IndexFunc(s, func(r rune) bool {
		return r <= ' ' || r >= 0x7f || strings.ContainsRune(`()<>@,;:\"/[]?=`, r)
	})
	if end < 0 {
		return s
	}
	return s[:end]
}

// selectPart applies a multipart step. With an index it takes that part.
// With a content type prefix it takes the first part, which must match:
// servers such as Google's upload endpoints read the first part as metadata,
// so a filter must check that part and not a later one of the same type.
func selectPart(parts []mimePart, step filter.BindStep) (mimePart, error) {
	if step.Index != nil {
		if *step.Index >= len(parts) {
			return mimePart{}, fmt.Errorf("multipart body has no part %d", *step.Index)
		}
		return parts[*step.Index], nil
	}
	if !strings.HasPrefix(strings.ToLower(parts[0].contentType), strings.ToLower(step.Arg)) {
		return mimePart{}, fmt.Errorf("first multipart part is not %s", step.Arg)
	}
	return parts[0], nil
}
