// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestResolveBundleMemoryMiBDefaultsTo4096(t *testing.T) {
	got, err := resolveBundleMemoryMiB(t.TempDir())
	if err != nil {
		t.Fatalf("resolveBundleMemoryMiB: %v", err)
	}
	if DefaultMemoryMiB != 4096 {
		t.Fatalf("DefaultMemoryMiB = %d, want 4096", DefaultMemoryMiB)
	}
	if got != DefaultMemoryMiB {
		t.Fatalf("default memory = %d, want %d", got, DefaultMemoryMiB)
	}
}

func TestBuildRuntimeMountsPropagatesStoreCacheMmap(t *testing.T) {
	be := New(Options{Host: model.Host{Home: t.TempDir()}})
	projectDir := t.TempDir()
	controlDir := filepath.Join(t.TempDir(), "control")
	req := backend.Request{
		Session: backend.SessionMeta{Tool: "codex"},
		Mounts: []backend.Mount{{
			Type:          backend.MountTypeBind,
			Source:        projectDir,
			ContainerPath: "/workspace",
		}},
		Stores: []backend.PersistentStore{
			{
				Kind:          backend.StoreKindConfig,
				Key:           backend.StoreKey{Owner: "codex", ProjectHash: "project", Suffix: "default"},
				ContainerPath: "/home/agent/.codex",
				CacheMmap:     true,
			},
			{
				Kind:          backend.StoreKindAuth,
				Key:           backend.StoreKey{Owner: "codex"},
				ContainerPath: "/home/agent/.enclave-auth",
			},
		},
	}

	mounts, err := be.buildRuntimeMounts(req, controlDir, "")
	if err != nil {
		t.Fatalf("buildRuntimeMounts: %v", err)
	}

	if got := findRuntimeMount(t, mounts, "/workspace").CacheMmap; got {
		t.Fatal("workspace mount unexpectedly uses cache=mmap")
	}
	if got := findRuntimeMount(t, mounts, "/home/agent/.codex").CacheMmap; !got {
		t.Fatal("store with CacheMmap should use cache=mmap")
	}
	if got := findRuntimeMount(t, mounts, "/home/agent/.enclave-auth").CacheMmap; got {
		t.Fatal("store without CacheMmap unexpectedly uses cache=mmap")
	}
	if got := findRuntimeMount(t, mounts, guestControlPath).CacheMmap; got {
		t.Fatal("control mount unexpectedly uses cache=mmap")
	}
}

func TestRequiredMemoryMiBCoversUnpackAndWorkload(t *testing.T) {
	cases := []struct {
		name         string
		uncompressed int64
		compressed   int64
		want         int
	}{
		// Boot needs 1024 + 384 + 256 MiB, the session 1024 + 2048 MiB.
		{name: "workload dominates", uncompressed: 1024 << 20, compressed: 384 << 20, want: 3072},
		// Boot needs 4096 + 1536 + 256 MiB, the session 4096 + 2048 MiB.
		{name: "unpacking dominates", uncompressed: 4096 << 20, compressed: 1536 << 20, want: 6144},
		{name: "unknown sizes", uncompressed: 0, compressed: 0, want: 2048},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequiredMemoryMiB(tc.uncompressed, tc.compressed); got != tc.want {
				t.Fatalf("RequiredMemoryMiB(%d, %d) = %d, want %d", tc.uncompressed, tc.compressed, got, tc.want)
			}
		})
	}
}

func TestInitramfsPathPrefersCompressedImage(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyInitramfsFile)
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := InitramfsPath(dir)
	if err != nil {
		t.Fatalf("InitramfsPath with legacy image: %v", err)
	}
	if got != legacy {
		t.Fatalf("InitramfsPath = %s, want %s", got, legacy)
	}
	compressed := filepath.Join(dir, InitramfsFile)
	if err := os.WriteFile(compressed, []byte("zstd"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = InitramfsPath(dir)
	if err != nil {
		t.Fatalf("InitramfsPath with compressed image: %v", err)
	}
	if got != compressed {
		t.Fatalf("InitramfsPath = %s, want %s", got, compressed)
	}
}

func TestInitramfsPathReportsMissingImage(t *testing.T) {
	if _, err := InitramfsPath(t.TempDir()); err == nil {
		t.Fatal("expected an error for a bundle without an initramfs")
	}
}

// A bundle whose initramfs cannot be unpacked in its declared memory must be
// rejected on the host; the guest failure mode is an unrelated-looking kernel
// panic about a missing init.
func TestCheckBundleMemoryRejectsUndersizedGuest(t *testing.T) {
	dir := t.TempDir()
	meta := []byte(`{"uncompressedBytes":3221225472,"compressedBytes":1073741824,"compression":"zstd"}`)
	if err := os.WriteFile(filepath.Join(dir, InitramfsMetaFile), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	err := checkBundleMemory(dir, DefaultMemoryMiB)
	if err == nil {
		t.Fatal("expected undersized guest memory to be rejected")
	}
	if !strings.Contains(err.Error(), "needs at least 5120 MiB") {
		t.Fatalf("error does not name the required memory: %v", err)
	}
	if err := checkBundleMemory(dir, 5120); err != nil {
		t.Fatalf("checkBundleMemory with sufficient memory: %v", err)
	}
	if err := checkBundleMemory(t.TempDir(), DefaultMemoryMiB); err != nil {
		t.Fatalf("checkBundleMemory without metadata: %v", err)
	}
}

// The kernel's unpack_to_rootfs() only detects the appended plain cpio overlay
// when it starts on a 4-byte boundary.
func TestConcatenateFilesAlignsSegments(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	overlay := filepath.Join(dir, "overlay")
	if err := os.WriteFile(base, []byte("compressed"), 0o600); err != nil { // 10 bytes
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("070701overlay"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "initramfs.img")
	if err := concatenateFiles(output, base, overlay); err != nil {
		t.Fatalf("concatenateFiles: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte("compressed"), 0, 0)
	want = append(want, []byte("070701overlay")...)
	if !bytes.Equal(data, want) {
		t.Fatalf("runtime initramfs = %q, want %q", data, want)
	}
	if got := bytes.Index(data, []byte("070701overlay")) % initramfsSegmentAlignment; got != 0 {
		t.Fatalf("overlay segment starts at offset %% %d = %d, want 0", initramfsSegmentAlignment, got)
	}
}

func findRuntimeMount(t *testing.T, mounts []runtimeMount, target string) runtimeMount {
	t.Helper()
	for _, mount := range mounts {
		if mount.Target == target {
			return mount
		}
	}
	t.Fatalf("runtime mount %s not found in %#v", target, mounts)
	return runtimeMount{}
}
