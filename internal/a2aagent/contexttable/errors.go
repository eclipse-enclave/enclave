// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package contexttable

import (
	"encoding/json"

	"enclave/internal/a2a"
)

// ErrorInfo reasons for the refusals the table raises.
const (
	ReasonTaskNotFound      = a2a.RefusalTaskNotFound
	ReasonContextUnknown    = a2a.RefusalContextUnknown
	ReasonContextExpired    = a2a.RefusalContextExpired
	ReasonMessageIDConflict = a2a.RefusalMessageIDConflict
	ReasonMessageIDReused   = a2a.RefusalMessageIDReused
	ReasonRetryAmbiguous    = a2a.RefusalRetryAmbiguous
	ReasonAdmissionStopped  = a2a.RefusalAdmissionStopped
	ReasonQuarantined       = a2a.RefusalQuarantined
	ReasonQueueFull         = a2a.RefusalQueueFull
	ReasonInvalidRequest    = a2a.RefusalInvalidRequest
	ReasonNotCancelable     = a2a.RefusalNotCancelable
)

// Refusal builds an *a2a.Error with one enclave ErrorInfo without depending on a2a.NewError.
func Refusal(code int, message, reason string) *a2a.Error {
	data, _ := json.Marshal([]a2a.ErrorInfo{{Type: a2a.ErrorInfoType, Domain: a2a.ErrorDomain, Reason: reason}})
	return &a2a.Error{Code: code, Message: message, Data: data}
}

func errTaskNotFound() *a2a.Error {
	return Refusal(a2a.CodeTaskNotFound, "task not found", ReasonTaskNotFound)
}

func errContextUnknown() *a2a.Error {
	return Refusal(a2a.CodeContextUnknown, "context unknown", ReasonContextUnknown)
}

func errContextExpired() *a2a.Error {
	return Refusal(a2a.CodeContextExpired, "context expired", ReasonContextExpired)
}
