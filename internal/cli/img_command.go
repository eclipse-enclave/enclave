// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// ActionImgImport is the action of `enclave img import`.
const ActionImgImport = "img-import"

func imgCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "img",
		Short: "Hand host images to running sessions",
		RunE:  rejectUnknownSubcommand,
	}
	cmd.AddCommand(imgImportCommand(res))
	return cmd
}

func imgImportCommand(res *Result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import (--file <path> | -)",
		Short: "Import a PNG or JPEG into the image inbox of a running session's project",
		Long: `Import a PNG or JPEG host image into a project's image inbox, where the
project's sessions started with --image-inbox see it read-only under
/mnt/host-images. The container path is printed on stdout.

The target is the working directory's project if it has a running
inbox-enabled session, otherwise the only project that has one.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				if args[0] != "-" {
					return fmt.Errorf("unexpected argument %q; pass --file <path>, or - to read from stdin", args[0])
				}
				res.Options.ImgStdin = true
			}
			if res.Options.ImgStdin && res.Options.ImgFile != "" {
				return errors.New("--file and - are mutually exclusive")
			}
			if !res.Options.ImgStdin && res.Options.ImgFile == "" {
				return errors.New("no image source; pass --file <path>, or - to read from stdin")
			}
			res.Action = ActionImgImport
			return nil
		},
	}
	cmd.Flags().StringVar(&res.Options.ImgFile, "file", "", "Read the host image from a PNG or JPEG file")
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "backend")
	return cmd
}
