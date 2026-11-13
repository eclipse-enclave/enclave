// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"fmt"
	"net/http"
	"strconv"

	"enclave/internal/filter"
	"enclave/internal/filter/token"
)

// Deny reasons, for the netlog reason field.
const (
	ReasonPlainHTTP         = "plain-http"
	ReasonAmbiguous         = "ambiguous"
	ReasonForeignCredential = "foreign-credential"
	ReasonPlaceholder       = "placeholder"
	ReasonNoRoute           = "no-route"
	ReasonCapability        = "capability"
	ReasonBody              = "body"
	ReasonBind              = "bind"
	ReasonConstraint        = "constraint"
	ReasonQuota             = "quota"
	ReasonMissingCredential = "missing-credential"
	// ReasonToken prefixes the cause of a failed token refresh, see
	// token.Cause: "token:invalid_grant", "token:unreachable".
	ReasonToken = "token"
	// ReasonResolver prefixes the cause of a failed resolver lookup:
	// "resolver:http-404", "resolver:unreachable".
	ReasonResolver    = "resolver"
	ReasonResponse    = "response"
	ReasonNotGoverned = "not-governed"
	ReasonDenyRoute   = "deny-route"
	ReasonFormBody    = "form-body"
)

// Denial is a rendered deny response. Filter names the filter that denied,
// Code is the HTTP status, Status the name rendered for {{status}}. Message
// never contains header values, bodies, or credential material.
type Denial struct {
	Filter      string
	Code        int
	Status      string
	Reason      string
	Message     string
	ContentType string
	Body        []byte
}

// Write sends the denial.
func (d *Denial) Write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", d.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(d.Body)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(d.Code)
	_, _ = w.Write(d.Body)
}

const maxQuotedValue = 120

// quote formats a request-derived value for a deny message, bounded in length.
func quote(value string) string {
	if len(value) > maxQuotedValue {
		value = value[:maxQuotedValue] + "..."
	}
	return strconv.Quote(value)
}

func renderDenial(filterName string, tpl *filter.DenyTemplate, code int, status string, reason string, format string, args ...any) *Denial {
	message := fmt.Sprintf("filter %s: %s", filterName, fmt.Sprintf(format, args...))
	d := &Denial{Filter: filterName, Code: code, Status: status, Reason: reason, Message: message}
	if tpl == nil {
		d.ContentType = "text/plain; charset=utf-8"
		d.Body = []byte(message + "\n")
		return d
	}
	d.ContentType = tpl.ContentType
	d.Body = tpl.Render(code, status, message)
	return d
}

// forbid is a policy denial: 403, PERMISSION_DENIED.
func (e *Engine) forbid(reason string, format string, args ...any) *Denial {
	return renderDenial(e.policy.Filter, e.policy.Deny, http.StatusForbidden, filter.StatusPermissionDenied, reason, format, args...)
}

// exhausted is a quota denial: 403, never 429 since clients retry on 429.
func (e *Engine) exhausted(counter string, limit int64, suffix string) *Denial {
	return renderDenial(e.policy.Filter, e.policy.Deny, http.StatusForbidden, filter.StatusResourceExhausted, ReasonQuota,
		"quota %s exhausted (%d)%s", counter, limit, suffix)
}

// tokenDenial is a token refresh failure: 502, UNAVAILABLE, with the reason
// "token:<cause>". Only a rejected grant or client asks for a new login.
func (e *Engine) tokenDenial(err error) *Denial {
	cause := token.Cause(err)
	reason := ReasonToken + ":" + cause
	if token.IsGrantError(err) {
		return renderDenial(e.policy.Filter, e.policy.Deny, http.StatusBadGateway, filter.StatusUnavailable, reason,
			"cannot obtain an access token (%s), run enclave filters login %s", cause, e.policy.Filter)
	}
	return renderDenial(e.policy.Filter, e.policy.Deny, http.StatusBadGateway, filter.StatusUnavailable, reason,
		"cannot obtain an access token, the token endpoint is unreachable or failed (%s)", cause)
}
