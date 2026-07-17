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

func imgCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "img",
		Short: "Import host images into a running session",
		RunE:  rejectUnknownSubcommand,
	}
	cmd.AddCommand(imgImportCommand(res))
	return cmd
}

func imgImportCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a host clipboard or screenshot image into the shared read-only inbox",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			res.Action = "img-import"
			return nil
		},
	}
	cmd.Flags().BoolVar(&res.Options.ImgScreenshot, "screenshot", false, "Capture a region screenshot instead of reading the clipboard")
	cmd.Flags().BoolVar(&res.Options.ImgNoCopy, "no-copy", false, "Do not copy the resulting container path to the host clipboard")
	return cmd
}
