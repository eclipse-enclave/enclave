// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"fmt"
	"strings"

	"enclave/internal/backend/vmhost"
	"enclave/internal/util"
)

type transport string

const (
	transportVirtiofs transport = "virtiofs"
	transport9P       transport = "9p"

	// Control exports are mounted by tag at fixed guest paths: the base image
	// knows controlInTag, and the boot script it finds there knows the rest.
	controlInTag  = "enclave-in"
	controlOutTag = "enclave-out"
	guestRunPath  = "/run/enclave"
	guestInPath   = guestRunPath + "/in"
	guestOutPath  = guestRunPath + "/out"
	// guestFilesPath is where the guest sees staged single-file mounts.
	guestFilesPath = guestOutPath + "/files"
)

// share is one host directory exported to the guest. Target is empty for
// exports the guest mounts at a fixed path.
type share struct {
	Tag       string
	Source    string
	Target    string
	ReadOnly  bool
	CacheMmap bool
	Transport transport
}

// exportTransport picks virtiofs wherever the host can enforce the export's
// mode. virtiofsd without --readonly exports everything writable and leaves
// read-only to the guest's mount flag, so such exports go through 9p, where
// QEMU refuses the writes.
func exportTransport(readOnly bool, caps virtiofsdCaps) transport {
	if readOnly && !caps.ReadOnly {
		return transport9P
	}
	return transportVirtiofs
}

// sessionShares returns the control exports followed by the request's
// mounts, parents first.
func sessionShares(rt runtimeFiles, mounts []vmhost.DirMount, caps virtiofsdCaps) []share {
	vmhost.SortParentsFirst(mounts)
	shares := []share{
		// The in export carries the scripts the guest runs as root and the
		// session's keys: read-only on the host like any other read-only
		// export. The base image's bootstrap tries both transports.
		{Tag: controlInTag, Source: rt.in(), ReadOnly: true, Transport: exportTransport(true, caps)},
		{Tag: controlOutTag, Source: rt.out(), Transport: transportVirtiofs},
	}
	for _, mount := range mounts {
		shares = append(shares, share{
			Tag:       mount.Tag,
			Source:    mount.Source,
			Target:    mount.Target,
			ReadOnly:  mount.ReadOnly,
			CacheMmap: mount.CacheMmap,
			Transport: exportTransport(mount.ReadOnly, caps),
		})
	}
	return shares
}

// mountsManifest lists the exports the guest boot script mounts, one
// tab-separated line each: transport, tag, mode, cache, target.
func mountsManifest(shares []share) (string, error) {
	var out strings.Builder
	for _, sh := range shares {
		if sh.Target == "" {
			continue
		}
		if err := rejectManifestField("mount target", sh.Target); err != nil {
			return "", err
		}
		mode := "rw"
		if sh.ReadOnly {
			mode = "ro"
		}
		cache := "default"
		if sh.CacheMmap {
			cache = "mmap"
		}
		fmt.Fprintf(&out, "%s\t%s\t%s\t%s\t%s\n", sh.Transport, sh.Tag, mode, cache, sh.Target)
	}
	return out.String(), nil
}

// filesManifest lists the staged single-file mounts, one tab-separated line
// each: mode, staged file, target. The guest binds the staged file over the
// target, so a writable mount's content lives in the export and never on the
// guest's persistent disk.
func filesManifest(files []vmhost.FileMount) (string, error) {
	var out strings.Builder
	for _, file := range files {
		if err := rejectManifestField("file mount target", file.Target); err != nil {
			return "", err
		}
		mode, staged := "rw", file.GuestResult
		if file.ReadOnly {
			mode, staged = "ro", file.GuestSource
		}
		fmt.Fprintf(&out, "%s\t%s\t%s\n", mode, staged, file.Target)
	}
	return out.String(), nil
}

// rejectControlCover refuses a mount that would hide the control exports: a
// target that is the guest's control directory or one of its ancestors.
// Targets below it are refused by the mount planner.
func rejectControlCover(mounts []vmhost.DirMount) error {
	for _, mount := range mounts {
		if util.PathWithin(mount.Target, guestRunPath) {
			return fmt.Errorf("vm backend: mount target %q covers the guest's control directory %s", mount.Target, guestRunPath)
		}
	}
	return nil
}

func rejectManifestField(label string, value string) error {
	if strings.ContainsAny(value, "\t\n\r") {
		return fmt.Errorf("vm backend: %s %q contains a tab or line break", label, value)
	}
	return nil
}
