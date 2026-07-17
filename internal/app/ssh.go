// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func setupSSH() error {
	home, err := resolveWritableHome()
	if err != nil {
		return err
	}

	enclaveSSH := config.HostSSHDir(home)
	logx.Infof("Setting up %s SSH directory.", model.AppName)
	if err := os.MkdirAll(enclaveSSH, 0o700); err != nil {
		return err
	}
	// #nosec G302
	// SSH directory requires execute bit for traversal.
	if err := os.Chmod(enclaveSSH, 0o700); err != nil {
		return err
	}

	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	if _, err := os.Stat(knownHosts); err == nil {
		copyPath := filepath.Join(enclaveSSH, "known_hosts")
		if err := copyFile(knownHosts, copyPath, 0o600); err != nil {
			return err
		}
		logx.Infof("Copied known_hosts from ~/.ssh")
	}

	keyPath := filepath.Join(enclaveSSH, "id_ed25519")
	pubPath := keyPath + ".pub"
	if _, err := os.Stat(keyPath); err == nil {
		logx.Infof("%s SSH key already exists", model.AppName)
	} else {
		logx.Infof("Generating dedicated SSH key for %s.", model.AppName)
		cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-C", fmt.Sprintf("%s@%s", model.AppName, hostName()), "-N", "") // #nosec G204 -- command and args are fixed, executed without shell.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		logx.Infof("Generated new SSH key: %s", keyPath)
		// #nosec G304 -- pubPath is derived from a trusted keyPath under the resolved home directory.
		if data, err := os.ReadFile(pubPath); err == nil {
			fmt.Println("")
			fmt.Println("Add this public key to your Git provider:")
			fmt.Println("")
			fmt.Print(string(data))
		}
		logx.Infof("Alternatively, replace the keys in %s with your desired keys", enclaveSSH)
	}

	logx.Successf("SSH setup complete. Directory: %s", enclaveSSH)
	logx.Infof("%s will use this directory for all SSH operations.", model.AppName)
	return nil
}

func copyFile(src string, dst string, mode os.FileMode) error {
	// #nosec G304 -- src is resolved from known SSH config locations.
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, mode); err != nil { // #nosec G703 -- dst is resolved under the managed SSH directory.
		return err
	}
	return nil
}

func hostName() string {
	name, err := os.Hostname()
	if err != nil {
		return model.AppName
	}
	return name
}
