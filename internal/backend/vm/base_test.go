// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

// baseSpec returns a base to build from a minimal build context, with the
// cloud image already downloaded.
func baseSpec(t *testing.T, b *Backend) BaseSpec {
	t.Helper()
	contextDir := t.TempDir()
	for path, content := range map[string]string{
		"Dockerfile":                           "ARG YQ_VERSION=v4.0.0\n",
		"entrypoint.sh":                        "#!/bin/bash\n",
		"runtime-assets/vm/base/provision.sh":  "#!/bin/bash\n",
		"extensions/tools/codex/install.sh":    "#!/bin/bash\n",
		"internal/not-for-the-guest/secret.go": "package secret\n",
	} {
		full := filepath.Join(contextDir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	images := filepath.Join(config.HostVMDir(b.opts.Host.Home), "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, cloudImageSHA256+".img"), []byte("cloud image"), 0o600); err != nil {
		t.Fatal(err)
	}
	return BaseSpec{Dir: BaseDir(b.opts.Host.Home, "claude", "newkey"), Key: "newkey", Tool: "claude", ContextDir: contextDir}
}

func TestBuildBasePublishesAProvisionedImage(t *testing.T) {
	b, host, _ := newTestBackend(t)
	spec := baseSpec(t, b)
	if err := b.BuildBase(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	stamp, err := ReadBaseStamp(spec.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := (BaseStamp{Key: "newkey", Tool: "claude", Image: cloudImageSHA256}); stamp != want {
		t.Errorf("stamp = %+v, want %+v", stamp, want)
	}
	info, err := os.Stat(filepath.Join(spec.Dir, baseDiskFile))
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Errorf("base disk mode = %v, %v; project disks depend on it staying unchanged", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(spec.Dir + ".tmp"); !os.IsNotExist(err) {
		t.Error("the staging directory was left behind")
	}

	image := filepath.Join(config.HostVMDir(b.opts.Host.Home), "images", cloudImageSHA256+".img")
	staged := filepath.Join(spec.Dir+".tmp", baseDiskFile)
	wantImg := [][]string{{"convert", "-O", "qcow2", image, staged}, {"resize", "-q", staged, defaultBaseDiskSize}}
	if got := host.commandsOf(qemuImgBinary); !slices.EqualFunc(got, wantImg, slices.Equal) {
		t.Errorf("qemu-img calls = %v, want %v", got, wantImg)
	}
	qemu := host.commandsOf(qemuBinary)
	if len(qemu) != 1 {
		t.Fatalf("qemu ran %d times", len(qemu))
	}
	line := strings.Join(qemu[0], " ")
	for _, want := range []string{"file=" + staged, "seed.iso,if=none,id=seed0,format=raw,readonly=on", "tag=" + provisionContextTag, "tag=" + controlOutTag} {
		if !strings.Contains(line, want) {
			t.Errorf("provisioning args lack %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "hostfwd") {
		t.Errorf("the provisioning guest is reachable from the host: %s", line)
	}

	// Only the context entries the guest script reads are shared, as a copy.
	var contextShare string
	for _, call := range host.commandsOf("virtiofsd") {
		for _, arg := range call {
			if strings.HasSuffix(arg, "/ctx") {
				contextShare = strings.Join(call, " ")
			}
		}
	}
	if !strings.Contains(contextShare, "--readonly") || strings.Contains(contextShare, spec.ContextDir) {
		t.Errorf("context export = %q; want a read-only private copy", contextShare)
	}

	if err := b.BuildBase(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("rebuilding a published base = %v", err)
	}
}

func TestBuildBaseKeepsLogsWhenProvisioningFails(t *testing.T) {
	b, host, _ := newTestBackend(t)
	spec := baseSpec(t, b)
	host.provisionResult = "3"
	err := b.BuildBase(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "provisioning failed with status 3") || !strings.Contains(err.Error(), "last line of the log") {
		t.Fatalf("BuildBase = %v", err)
	}
	if _, statErr := os.Stat(spec.Dir); !os.IsNotExist(statErr) {
		t.Error("a failed base was published")
	}
	logs := err.Error()[strings.LastIndex(err.Error(), "logs are kept in ")+len("logs are kept in "):]
	if _, statErr := os.Stat(filepath.Join(logs, "out", provisionLogFile)); statErr != nil {
		t.Errorf("provisioning log not kept in %q: %v", logs, statErr)
	}
	// The guest gets the entries its script reads, not the whole build context.
	if _, statErr := os.Stat(filepath.Join(logs, "ctx", "runtime-assets", "vm", "base", "provision.sh")); statErr != nil {
		t.Errorf("the provisioning script was not staged: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(logs, "ctx", "internal")); !os.IsNotExist(statErr) {
		t.Error("the guest was given build context it does not need")
	}
	t.Cleanup(func() { _ = os.RemoveAll(logs) })
}

// The guest reports success before it syncs and powers off. A QEMU that was
// interrupted after that leaves a disk that may lack the last writes.
func TestBuildBaseNeedsACleanShutdown(t *testing.T) {
	b, host, _ := newTestBackend(t)
	spec := baseSpec(t, b)
	host.provisionError = errors.New("signal: killed")
	err := b.BuildBase(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "did not shut down cleanly") {
		t.Fatalf("BuildBase = %v", err)
	}
	if _, statErr := os.Stat(spec.Dir); !os.IsNotExist(statErr) {
		t.Error("a base whose VM did not shut down cleanly was published")
	}
	logs := err.Error()[strings.LastIndex(err.Error(), "logs are kept in ")+len("logs are kept in "):]
	t.Cleanup(func() { _ = os.RemoveAll(logs) })
}

func TestBuildBaseNeedsASeedTool(t *testing.T) {
	b, host, _ := newTestBackend(t)
	spec := baseSpec(t, b)
	for _, tool := range []string{"cloud-localds", "genisoimage", "xorriso"} {
		host.missing[tool] = true
	}
	if err := b.BuildBase(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "cloud-localds, genisoimage or xorriso") {
		t.Fatalf("BuildBase without a seed tool = %v", err)
	}
}

func TestSeedArgs(t *testing.T) {
	if got, want := seedArgs([]string{"cloud-localds"}, "s.iso", "u", "m"), []string{"cloud-localds", "s.iso", "u", "m"}; !slices.Equal(got, want) {
		t.Errorf("cloud-localds: %v", got)
	}
	want := []string{"xorriso", "-as", "genisoimage", "-quiet", "-output", "s.iso", "-volid", "cidata", "-joliet", "-rock", "u", "m"}
	if got := seedArgs([]string{"xorriso", "-as", "genisoimage"}, "s.iso", "u", "m"); !slices.Equal(got, want) {
		t.Errorf("xorriso: %v", got)
	}
}

func TestEnsureCloudImageRejectsWrongDigest(t *testing.T) {
	b, _, _ := newTestBackend(t)
	_, err := b.ensureCloudImage(context.Background())
	if err == nil || !strings.Contains(err.Error(), "want "+cloudImageSHA256) {
		t.Fatalf("ensureCloudImage = %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(config.HostVMDir(b.opts.Host.Home), "images"))
	if len(entries) != 0 {
		t.Errorf("an unverified download was kept: %v", entries)
	}
}

func TestProvisionUserData(t *testing.T) {
	want := `#cloud-config
users: []
ssh_pwauth: false
runcmd:
  - - bash
    - -c
    - |
      mkdir -p /run/enclave-provision/ctx /run/enclave-provision/out
      modprobe virtiofs 2>/dev/null || true
      mount -t virtiofs -o ro enclave-ctx /run/enclave-provision/ctx
      mount -t virtiofs enclave-out /run/enclave-provision/out
      status=0
      bash /run/enclave-provision/ctx/runtime-assets/vm/base/provision.sh /run/enclave-provision/ctx 'codex' '1000' '1001' >/run/enclave-provision/out/provision.log 2>&1 || status=$?
      echo "$status" >/run/enclave-provision/out/result
      sync
      poweroff
`
	if got := provisionUserData("codex", "1000", "1001"); got != want {
		t.Errorf("user-data =\n%s\nwant\n%s", got, want)
	}
}

func TestBaseKeyFollowsItsInputs(t *testing.T) {
	host := model.Host{UID: "1000", GID: "1000"}
	key := BaseKey("image", "recipe", host)
	if key != BaseKey("image", "recipe", host) {
		t.Fatal("the key is not stable")
	}
	for name, other := range map[string]string{
		"image inputs": BaseKey("image2", "recipe", host),
		"recipe":       BaseKey("image", "recipe2", host),
		"uid":          BaseKey("image", "recipe", model.Host{UID: "1001", GID: "1000"}),
		"gid":          BaseKey("image", "recipe", model.Host{UID: "1000", GID: "1001"}),
	} {
		if other == key {
			t.Errorf("the key ignores the %s", name)
		}
	}
	t.Setenv(baseDiskSizeEnv, "80G")
	if BaseKey("image", "recipe", host) == key {
		t.Error("the key ignores the disk size")
	}
}
