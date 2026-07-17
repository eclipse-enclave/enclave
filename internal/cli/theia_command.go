// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func theiaCommand(res *Result, variant string) *cobra.Command {
	return &cobra.Command{
		Use:   variant + " [container-name]",
		Short: fmt.Sprintf("Open %s attached to a running enclave container", variant),
		Long: fmt.Sprintf(`Launch the %s desktop IDE attached to a running enclave container.

If [container-name] is omitted, the single running enclave container is used.
If multiple are running, names are listed and one must be passed explicitly.

Preferences are merged from (highest wins):
  - ~/.config/enclave/projects/<hash>/config.json under {"theia":{"preferences":{...}}}
  - ~/.config/enclave/tools/theia/preferences.json
  - built-in defaults

Roots honor $XDG_CONFIG_HOME on Linux (ignored on macOS).`, variant),
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			res.Action = variant
			if len(cmdArgs) == 1 {
				res.Options.CmdArgs = append(res.Options.CmdArgs, cmdArgs[0])
			}
			return nil
		},
	}
}
