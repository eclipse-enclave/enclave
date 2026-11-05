// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	dockercmd "enclave/internal/docker"
	"enclave/internal/peergroup"
	"enclave/internal/util"
)

// secretsOwnerMode is how secrets.json is handed to the hub proxy uid.
type secretsOwnerMode int

const (
	// secretsOwnerLocal: the host process is root and chowns directly.
	secretsOwnerLocal secretsOwnerMode = iota
	// secretsOwnerUnshare: rootless Podman maps container uid 65532 through
	// the caller's subordinate range, which "podman unshare" can chown into.
	secretsOwnerUnshare
	// secretsOwnerHelper: a short-lived container of the hub image runs chown
	// as container root with only CAP_CHOWN, no network and the staging
	// directory mounted; container root is host root on rootful Docker and the
	// host user on rootless Docker, so 65532 lands where the hub sees it.
	secretsOwnerHelper
)

// secretsOwnerPlan is the mode plus the host ids the proxy uid maps to.
type secretsOwnerPlan struct {
	mode secretsOwnerMode
	uid  int
	gid  int
	// userns is "host" under userns-remap: remapped container root cannot
	// chown a file owned by an unmapped host uid.
	userns string
}

// dockerRemapUser is the subordinate-id user of "userns-remap": "default".
const dockerRemapUser = "dockremap"

func secretsOwnerPlanFor(info dockercmd.SystemInfo, podman bool, euid int, readFile func(string) ([]byte, error)) (secretsOwnerPlan, error) {
	rootless, remapped := false, false
	for _, option := range info.SecurityOptions {
		if strings.Contains(option, "name=rootless") {
			rootless = true
		}
		if strings.Contains(option, "name=userns") {
			remapped = true
		}
	}
	plan := secretsOwnerPlan{uid: peergroup.HubProxyUID, gid: peergroup.HubProxyUID}
	switch {
	case podman && rootless:
		plan.mode = secretsOwnerUnshare
	case remapped && !podman:
		uidBase, gidBase, err := dockerRemapBase(info, readFile)
		if err != nil {
			return secretsOwnerPlan{}, err
		}
		plan.uid, plan.gid = uidBase+peergroup.HubProxyUID, gidBase+peergroup.HubProxyUID
		plan.mode, plan.userns = secretsOwnerHelper, "host"
		if euid == 0 {
			plan.mode, plan.userns = secretsOwnerLocal, ""
		}
	case euid == 0:
		plan.mode = secretsOwnerLocal
	default:
		plan.mode = secretsOwnerHelper
	}
	return plan, nil
}

// dockerRemapBase returns the host uid and gid that container root maps to
// under userns-remap. The daemon names its root dir "<uid>.<gid>" then; the
// subordinate ranges of dockremap are the fallback.
func dockerRemapBase(info dockercmd.SystemInfo, readFile func(string) ([]byte, error)) (int, int, error) {
	if uid, gid, ok := strings.Cut(filepath.Base(strings.TrimSpace(info.DockerRootDir)), "."); ok {
		u, uidErr := strconv.Atoi(uid)
		g, gidErr := strconv.Atoi(gid)
		if uidErr == nil && gidErr == nil && u > 0 && g > 0 {
			return u, g, nil
		}
	}
	uid, uidErr := subordinateStart(readFile, "/etc/subuid", dockerRemapUser)
	gid, gidErr := subordinateStart(readFile, "/etc/subgid", dockerRemapUser)
	if uidErr != nil || gidErr != nil {
		return 0, 0, fmt.Errorf("--peer-group cannot find the userns-remap range: Docker root %q is not <uid>.<gid> and %s has no usable subordinate ranges (%v, %v)", info.DockerRootDir, dockerRemapUser, uidErr, gidErr)
	}
	return uid, gid, nil
}

// subordinateStart returns the first range start of user in a subid file
// that covers the proxy uid.
func subordinateStart(readFile func(string) ([]byte, error), path, user string) (int, error) {
	data, err := readFile(path)
	if err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Split(strings.TrimSpace(scanner.Text()), ":")
		if len(fields) != 3 || fields[0] != user {
			continue
		}
		start, startErr := strconv.Atoi(fields[1])
		count, countErr := strconv.Atoi(fields[2])
		if startErr == nil && countErr == nil && start > 0 && count > peergroup.HubProxyUID {
			return start, nil
		}
	}
	return 0, fmt.Errorf("no range of at least %d ids for %s in %s", peergroup.HubProxyUID+1, user, path)
}

// secretsOwnerFromInfo returns the SecretsOwner for the engine info describes.
// hubImage runs the helper container where one is needed.
func secretsOwnerFromInfo(info dockercmd.SystemInfo, hubImage string) (peergroup.SecretsOwner, error) {
	plan, err := secretsOwnerPlanFor(info, dockercmd.IsPodman(), os.Geteuid(), os.ReadFile)
	if err != nil {
		return nil, err
	}
	return secretsOwnerFor(plan, hubImage), nil
}

var (
	secretsHelperRun = dockercmd.RunCapture
	secretsUnshare   = dockercmd.Unshare
)

func secretsOwnerFor(plan secretsOwnerPlan, hubImage string) peergroup.SecretsOwner {
	owner := strconv.Itoa(plan.uid) + ":" + strconv.Itoa(plan.gid)
	switch plan.mode {
	case secretsOwnerLocal:
		return peergroup.LocalSecretsOwner(plan.uid)
	case secretsOwnerUnshare:
		return func(ctx context.Context, path string) error {
			// chmod first: after the chown the host user no longer owns the file.
			if err := os.Chmod(path, 0o400); err != nil {
				return err
			}
			if err := secretsUnshare(ctx, "chown", owner, path); err != nil {
				return fmt.Errorf("podman unshare chown (needs a subordinate uid range of at least %d entries): %w", peergroup.HubProxyUID+1, err)
			}
			return nil
		}
	default:
		return func(ctx context.Context, path string) error {
			if err := os.Chmod(path, 0o400); err != nil {
				return err
			}
			if strings.TrimSpace(hubImage) == "" {
				return fmt.Errorf("no A2A hub image to run the secrets ownership helper")
			}
			config, hostConfig, err := secretsHelperConfig(hubImage, path, owner, plan.userns, util.IsSELinuxEnforcing())
			if err != nil {
				return err
			}
			if _, err := secretsHelperRun(ctx, config, hostConfig, ""); err != nil {
				return fmt.Errorf("hand secrets.json to uid %d: %w", plan.uid, err)
			}
			return nil
		}
	}
}

func secretsHelperConfig(image, path, owner, userns string, selinux bool) (*dockercmd.ContainerConfig, *dockercmd.HostConfig, error) {
	binds, mounts, err := bindMounts([]dockercmd.Mount{{Type: dockercmd.MountTypeBind, Source: filepath.Dir(path), Target: secretsHelperDir}}, selinux)
	if err != nil {
		return nil, nil, err
	}
	config := &dockercmd.ContainerConfig{
		Image:      image,
		User:       "0:0",
		Entrypoint: dockercmd.StrSlice{"chown"},
		Cmd:        dockercmd.StrSlice{owner, secretsHelperDir + "/" + filepath.Base(path)},
	}
	hostConfig := &dockercmd.HostConfig{
		AutoRemove:     true,
		NetworkMode:    "none",
		UserNS:         userns,
		CapDrop:        []string{"ALL"},
		CapAdd:         []string{"CHOWN"},
		SecurityOpt:    []string{"no-new-privileges"},
		ReadonlyRootfs: true,
		LogDriver:      "none",
		Binds:          binds,
		Mounts:         mounts,
	}
	return config, hostConfig, nil
}
