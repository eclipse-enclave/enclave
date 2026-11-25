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
	"strings"

	"github.com/spf13/cobra"

	"enclave/internal/model"
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
		Use:   "import [--screenshot | --file <path> | -] [--session <target>] [--json]",
		Short: "Import a PNG or JPEG into the image inbox of a running session's project",
		Long: `Import a PNG or JPEG host image into a project's image inbox, where the
project's sessions started with --image-inbox see it read-only under
/mnt/host-images. The container path is printed on stdout.

Without a source flag the image on the host clipboard is imported;
--screenshot captures a selected screen region instead. Both need Linux with
Wayland (wl-clipboard; grim and slurp) or X11 (xclip; maim). For these two
sources the container path is also copied to the host clipboard unless
--no-clipboard is given. They also show a desktop notification when
notify-send is installed; --file and - do not.

The target project is, in order: the project of the session named by
--session (a session name, container name, or container ID prefix of a
running inbox-enabled session); the working directory's project if it has a
running inbox-enabled session; the only project that has one. Anything else
is an error.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Set before validation, so that an invalid import is still reported
			// as one, including as a desktop notification.
			res.Action = ActionImgImport
			if len(args) == 1 {
				if args[0] != "-" {
					return fmt.Errorf("unexpected argument %q; pass --screenshot, --file <path>, or - to read from stdin", args[0])
				}
				res.Options.ImgStdin = true
			}
			sources := 0
			for _, set := range []bool{res.Options.ImgScreenshot, res.Options.ImgFile != "", res.Options.ImgStdin} {
				if set {
					sources++
				}
			}
			if sources > 1 {
				return errors.New("--screenshot, --file, and - are mutually exclusive")
			}
			if cmd.Flags().Changed("session") {
				res.Options.ImgSession = strings.TrimSpace(res.Options.ImgSession)
				if res.Options.ImgSession == "" {
					return fmt.Errorf("empty --session; pass a session or container name from `%s ps`", model.AppName)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&res.Options.ImgScreenshot, "screenshot", false, "Capture a selected screen region instead of reading the clipboard")
	cmd.Flags().StringVar(&res.Options.ImgFile, "file", "", "Read the host image from a PNG or JPEG file")
	cmd.Flags().StringVar(&res.Options.ImgSession, "session", "", "Import into the project of this running inbox-enabled session (name, container name, or ID prefix)")
	cmd.Flags().BoolVar(&res.Options.ImgNoClipboard, "no-clipboard", false, "Do not copy the container path to the host clipboard")
	cmd.Flags().BoolVar(&res.Options.ImgJSON, "json", false, "Print the result as a JSON object instead of the bare path")
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "backend")
	return cmd
}
