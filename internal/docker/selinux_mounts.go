// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import "enclave/internal/util"

// SplitMountsForSELinux converts bind mounts to HostConfig.Binds string format
// with the ":z" shared relabel suffix when SELinux is enforcing. Volume and
// tmpfs mounts pass through unchanged in the remaining slice.
//
// The ":z" relabel flag is only expressible through the `-v src:dst:opts`
// (Binds) form, not through `--mount`, so SELinux hosts route bind mounts there.
func SplitMountsForSELinux(mounts []Mount) (binds []string, remaining []Mount, err error) {
	return SplitMountsForSELinuxWith(mounts, util.IsSELinuxEnforcing())
}

// SplitMountsForSELinuxWith is SplitMountsForSELinux with the enforcing
// decision supplied by the caller, so callers can inject it and tests can
// exercise both branches regardless of the host's SELinux state.
func SplitMountsForSELinuxWith(mounts []Mount, enforcing bool) (binds []string, remaining []Mount, err error) {
	if !enforcing {
		return nil, mounts, nil
	}
	return splitBindMounts(mounts)
}

// splitBindMounts extracts bind mounts into Binds strings with :z relabeling
// and passes other mount types through unchanged.
func splitBindMounts(mounts []Mount) (binds []string, remaining []Mount, err error) {
	for _, m := range mounts {
		if m.Type == MountTypeBind {
			bind, err := formatBind(m)
			if err != nil {
				return nil, nil, err
			}
			binds = append(binds, bind)
		} else {
			remaining = append(remaining, m)
		}
	}
	return binds, remaining, nil
}

func formatBind(m Mount) (string, error) {
	s, err := volumeBindSpec(m)
	if err != nil {
		return "", err
	}
	if m.ReadOnly {
		s += ":ro,z"
	} else {
		s += ":z"
	}
	return s, nil
}
