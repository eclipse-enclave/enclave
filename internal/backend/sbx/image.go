// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package sbx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/logx"
)

// ImageSource is the host engine that builds Enclave images. sbx keeps its
// own image store, so a built image is exported from here and loaded there.
type ImageSource interface {
	Inspect(ctx context.Context, ref string) (ImageConfig, error)
	Save(ctx context.Context, ref string, path string) error
}

const imageStampFile = "images.json"

// ensureImage makes ref available to sbx. A host-side stamp records which
// engine image ID was last loaded under ref, so an unchanged image is loaded
// only once; the image IDs of the two stores are not comparable.
func (b *Backend) ensureImage(ctx context.Context, ref string, progress io.Writer) (ImageConfig, error) {
	img, err := b.images.Inspect(ctx, ref)
	if err != nil {
		return ImageConfig{}, fmt.Errorf("sbx backend: image %s is not in the host engine (sbx sessions use images built with docker): %w", ref, err)
	}
	stamps := b.readImageStamps()
	if img.ID != "" && stamps[ref] == img.ID {
		present, err := b.templatePresent(ctx, ref)
		if err != nil {
			return ImageConfig{}, err
		}
		if present {
			return img, nil
		}
	}
	tmpDir, err := os.MkdirTemp("", "enclave-sbx-image-")
	if err != nil {
		return ImageConfig{}, fmt.Errorf("sbx backend: create image export directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tarPath := filepath.Join(tmpDir, "image.tar")
	logx.Infof("Loading %s into sbx (once per image build; large images take a while).", ref)
	if err := b.images.Save(ctx, ref, tarPath); err != nil {
		return ImageConfig{}, fmt.Errorf("sbx backend: export image: %w", err)
	}
	if err := b.cli.Run(ctx, progress, "template", "load", tarPath); err != nil {
		return ImageConfig{}, err
	}
	if img.ID != "" {
		stamps[ref] = img.ID
		b.writeImageStamps(stamps)
	}
	return img, nil
}

type templateImage struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

func (b *Backend) templatePresent(ctx context.Context, ref string) (bool, error) {
	out, err := b.cli.Output(ctx, "template", "ls", "--json")
	if err != nil {
		return false, err
	}
	var images []templateImage
	if err := json.Unmarshal(out, &images); err != nil {
		return false, fmt.Errorf("sbx backend: decode sbx template ls output: %w", err)
	}
	want := normalizeImageRef(ref)
	for _, image := range images {
		if image.Repository == "" || image.Tag == "" {
			continue
		}
		if normalizeImageRef(image.Repository+":"+image.Tag) == want {
			return true, nil
		}
	}
	return false, nil
}

// normalizeImageRef spells a tag reference the way registries resolve it:
// docker.io/library/name:latest for a bare name.
func normalizeImageRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	name, digest, _ := strings.Cut(ref, "@")
	slash := strings.LastIndex(name, "/")
	if !strings.Contains(name[slash+1:], ":") && digest == "" {
		name += ":latest"
	}
	first, _, hasSlash := strings.Cut(name, "/")
	switch {
	case !hasSlash:
		name = "docker.io/library/" + name
	case !strings.ContainsAny(first, ".:") && first != "localhost":
		name = "docker.io/" + name
	}
	if digest != "" {
		return name + "@" + digest
	}
	return name
}

func (b *Backend) readImageStamps() map[string]string {
	stamps := map[string]string{}
	if b.opts.StateDir == "" {
		return stamps
	}
	data, err := os.ReadFile(filepath.Join(b.opts.StateDir, imageStampFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logx.Debugf("sbx backend: ignoring unreadable image stamps: %v", err)
		}
		return stamps
	}
	if err := json.Unmarshal(data, &stamps); err != nil {
		logx.Debugf("sbx backend: ignoring invalid image stamps: %v", err)
		return map[string]string{}
	}
	return stamps
}

func (b *Backend) writeImageStamps(stamps map[string]string) {
	if b.opts.StateDir == "" {
		return
	}
	data, err := json.MarshalIndent(stamps, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(b.opts.StateDir, 0o700); err != nil {
		logx.Debugf("sbx backend: cannot record loaded image: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(b.opts.StateDir, imageStampFile), append(data, '\n'), 0o600); err != nil {
		logx.Debugf("sbx backend: cannot record loaded image: %v", err)
	}
}
