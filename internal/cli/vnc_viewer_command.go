// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"github.com/spf13/cobra"
)

func vncViewerCommand(res *Result) *cobra.Command {
	return &cobra.Command{
		Use:   "vnc-viewer [container-name]",
		Short: "Open a VNC viewer on a session's contained display",
		Long: `Open a host-installed VNC viewer on the contained display of a running
session started with the vnc feature (enclave --features +vnc).

If [container-name] is omitted, the current project's only VNC-enabled
container is used. If several are running, names are listed and one must be
passed explicitly.

The viewer defaults to xtigervncviewer (macOS: the built-in Screen Sharing via
open) and can be replaced with the vnc_viewer config key:

  {"vnc_viewer": ["remmina", "-c", "vnc://:{password}@{host}:{port}"]}

Placeholders {host}, {port}, {password} and {container} are substituted. A
command referencing neither {host} nor {port} gets <host>:<port> appended. The
session password is also exported as VNC_PASSWORD and ENCLAVE_VNC_PASSWORD.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, cmdArgs []string) error {
			res.Action = "vnc-viewer"
			if len(cmdArgs) == 1 {
				res.Options.CmdArgs = append(res.Options.CmdArgs, cmdArgs[0])
			}
			return nil
		},
	}
}
