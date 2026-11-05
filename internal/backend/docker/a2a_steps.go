// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"fmt"
)

// a2aStep is one reversible lifecycle step; undo may be nil when the step
// leaves nothing behind.
type a2aStep struct {
	name string
	do   func(ctx context.Context) error
	undo func(ctx context.Context) error
}

// a2aSteps runs steps in order and remembers the completed ones so a later
// failure can undo them in reverse.
type a2aSteps struct {
	done []a2aStep
	// trace records step names as they run and undo, for tests and reports.
	trace []string
}

func (s *a2aSteps) run(ctx context.Context, steps ...a2aStep) error {
	for _, step := range steps {
		s.trace = append(s.trace, step.name)
		if err := step.do(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		s.done = append(s.done, step)
	}
	return nil
}

// rollback undoes every completed step in reverse; it keeps going after a
// failed undo so later resources are not stranded, and runs detached from
// cancellation because an interrupted start must still clean up.
func (s *a2aSteps) rollback(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	var errs []error
	for i := len(s.done) - 1; i >= 0; i-- {
		step := s.done[i]
		if step.undo == nil {
			continue
		}
		s.trace = append(s.trace, "undo "+step.name)
		if err := step.undo(ctx); err != nil {
			errs = append(errs, fmt.Errorf("undo %s: %w", step.name, err))
		}
	}
	s.done = nil
	return errors.Join(errs...)
}
