// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	pngMagic  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	jpegMagic = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
)

func TestSniffImageType(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		wantMIME string
		wantExt  string
		wantOK   bool
	}{
		{"png", pngMagic, "image/png", "png", true},
		{"jpeg", jpegMagic, "image/jpeg", "jpg", true},
		{"gif", []byte("GIF89a"), "", "", false},
		{"empty", nil, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mime, ext, ok := sniffImageType(tt.data)
			if mime != tt.wantMIME || ext != tt.wantExt || ok != tt.wantOK {
				t.Fatalf("sniffImageType = (%q,%q,%v), want (%q,%q,%v)", mime, ext, ok, tt.wantMIME, tt.wantExt, tt.wantOK)
			}
		})
	}
}

func TestValidateImage(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		advertised string
		maxBytes   int64
		wantExt    string
		wantErr    bool
	}{
		{"png ok", pngMagic, "image/png", 1024, "png", false},
		{"jpeg alias ok", jpegMagic, "image/jpg", 1024, "jpg", false},
		{"no advertised ok", pngMagic, "", 1024, "png", false},
		{"mime mismatch", pngMagic, "image/jpeg", 1024, "", true},
		{"too large", pngMagic, "image/png", 4, "", true},
		{"not image", []byte("hello world"), "", 1024, "", true},
		{"empty", nil, "", 1024, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext, err := validateImage(tt.data, tt.advertised, tt.maxBytes)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got ext=%q", ext)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ext != tt.wantExt {
				t.Fatalf("ext = %q, want %q", ext, tt.wantExt)
			}
		})
	}
}

func TestChooseImageMIME(t *testing.T) {
	tests := []struct {
		name  string
		types []string
		want  string
		ok    bool
	}{
		{"png preferred", []string{"text/plain", "image/jpeg", "image/png"}, "image/png", true},
		{"jpeg only", []string{"text/plain", "image/jpeg"}, "image/jpeg", true},
		{"jpg alias", []string{"image/jpg"}, "image/jpeg", true},
		{"none", []string{"text/plain", "text/html"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := chooseImageMIME(tt.types)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("chooseImageMIME = (%q,%v), want (%q,%v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestInboxFilename(t *testing.T) {
	ts := time.Date(2026, 7, 6, 14, 57, 21, 0, time.UTC)
	got := inboxFilename(ts, []byte{0xAB, 0xCD, 0xEF, 0x01}, "png")
	want := "20260706T145721Z-abcdef01.png"
	if got != want {
		t.Fatalf("inboxFilename = %q, want %q", got, want)
	}
}

func TestWriteInboxImage(t *testing.T) {
	dir := t.TempDir()
	name := "20260706T145721Z-abcdef01.png"
	if err := writeInboxImage(dir, name, pngMagic); err != nil {
		t.Fatalf("writeInboxImage: %v", err)
	}
	final := filepath.Join(dir, name)
	info, err := os.Stat(final)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(pngMagic) {
		t.Fatalf("content mismatch")
	}
	// No leftover temp files.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
}
