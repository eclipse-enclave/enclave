// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"errors"
	"fmt"
)

type ExitError struct {
	Code   int
	Stderr string
}

func IsExitError(err error) bool {
	var exitErr *ExitError
	return errors.As(err, &exitErr)
}

func (e *ExitError) Error() string {
	if e == nil {
		return "container exited"
	}
	if e.Stderr == "" {
		return fmt.Sprintf("container exited with code %d", e.Code)
	}
	return fmt.Sprintf("container exited with code %d: %s", e.Code, e.Stderr)
}
