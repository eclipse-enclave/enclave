// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"enclave/internal/logx"
	"enclave/internal/util"
)

// BuildRequest describes an image build. It is translated to `docker build`
// (or `docker buildx build`) flags.
type BuildRequest struct {
	ContextDir string
	Dockerfile string
	// DockerfileContent, when set, supplies the Dockerfile via a temporary file
	// (or stdin for buildx) so the build context can be read-only.
	DockerfileContent []byte
	Tags              []string
	Target            string
	BuildArgs         map[string]string
	Labels            map[string]string
	CacheFrom         []string
	BuildxCacheFrom   []string
	BuildxCacheTo     []string
	Progress          string
	NetworkMode       string
}

// ImageInspect returns the inspect view of a local image.
func ImageInspect(ctx context.Context, imageRef string) (ImageInspectResponse, error) {
	out, err := capture(ctx, "image", "inspect", "--format", "{{json .}}", imageRef)
	if err != nil {
		return ImageInspectResponse{}, err
	}
	line := firstLine(out)
	if line == "" {
		return ImageInspectResponse{}, &cliError{
			args:   []string{"image", "inspect", imageRef},
			stderr: "no such image: " + imageRef,
			err:    fmt.Errorf("image not found"),
		}
	}
	var resp ImageInspectResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return ImageInspectResponse{}, fmt.Errorf("decode image inspect for %s: %w", imageRef, err)
	}
	return resp, nil
}

// ImagePull pulls an image, discarding progress output.
func ImagePull(ctx context.Context, imageRef string) error {
	if imageRef == "" {
		return nil
	}
	_, err := capture(ctx, "pull", imageRef)
	return err
}

// ImageExists reports whether an image is present locally.
func ImageExists(ctx context.Context, imageRef string) (bool, error) {
	if imageRef == "" {
		return false, nil
	}
	_, err := ImageInspect(ctx, imageRef)
	if err == nil {
		return true, nil
	}
	if IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// EnsureImage makes an image available locally, pulling it when missing.
func EnsureImage(ctx context.Context, imageRef string) error {
	if imageRef == "" {
		return nil
	}
	_, inspectErr := ImageInspect(ctx, imageRef)
	if inspectErr == nil {
		logx.Debugf("EnsureImage: %s found locally", imageRef)
		return nil
	}
	logx.Debugf("EnsureImage: %s inspect failed: %v (attempting pull)", imageRef, inspectErr)
	pullErr := ImagePull(ctx, imageRef)
	if pullErr == nil {
		logx.Debugf("EnsureImage: %s pulled successfully", imageRef)
		return nil
	}
	logx.Debugf("EnsureImage: %s pull failed: %v", imageRef, pullErr)
	if IsNotFound(inspectErr) {
		return pullErr
	}
	return fmt.Errorf("inspect image %s: %w (pull failed: %v)", imageRef, inspectErr, pullErr)
}

// ImagePrune removes dangling images, optionally constrained to a label.
func ImagePrune(ctx context.Context, labelFilter string) (PruneReport, error) {
	args := []string{"image", "prune", "--force"}
	if labelFilter != "" {
		args = append(args, "--filter", "label="+labelFilter)
	}
	out, err := capture(ctx, args...)
	if err != nil {
		return PruneReport{}, err
	}
	return PruneReport{SpaceReclaimed: parseReclaimedSpace(out)}, nil
}

// FindImageByLabel returns the first locally-tagged image carrying label=value,
// or ("", false, nil) when none match.
func FindImageByLabel(ctx context.Context, label string, value string) (string, bool, error) {
	if strings.TrimSpace(label) == "" || strings.TrimSpace(value) == "" {
		return "", false, nil
	}
	out, err := capture(ctx, "image", "ls", "--filter", "label="+label+"="+value, "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return "", false, err
	}
	for _, tag := range splitLines(out) {
		if tag == "" || tag == "<none>:<none>" || strings.HasPrefix(tag, "<none>:") {
			continue
		}
		return tag, true, nil
	}
	return "", false, nil
}

// Tag adds target as an additional tag for the source image.
func Tag(ctx context.Context, source string, target string) error {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" {
		return fmt.Errorf("image tag requires non-empty source and target")
	}
	_, err := capture(ctx, "image", "tag", source, target)
	return err
}

// Build builds an image, using `docker buildx` when buildx cache import/export
// is requested and falling back to a plain `docker build` (without cache) when
// buildx is unavailable or no cache options are set.
func Build(ctx context.Context, req BuildRequest, out io.Writer) error {
	if shouldUseBuildx(req) {
		if BuildxAvailable(ctx) {
			return buildWithBuildx(ctx, req, out)
		}
		logx.Warnf("docker buildx is unavailable; falling back to plain docker build without buildx cache export")
	}
	return buildWithDockerCLI(ctx, req, out)
}

func buildWithDockerCLI(ctx context.Context, req BuildRequest, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if out == nil {
		out = io.Discard
	}

	args := []string{"build"}
	for _, tag := range req.Tags {
		if t := strings.TrimSpace(tag); t != "" {
			args = append(args, "--tag", t)
		}
	}

	if len(req.DockerfileContent) > 0 {
		tmpPath, cleanup, err := writeTempDockerfile(req.DockerfileContent)
		if err != nil {
			return err
		}
		defer cleanup()
		args = append(args, "--file", tmpPath)
	} else if dockerfile := resolveDockerfilePath(req); dockerfile != "" {
		args = append(args, "--file", dockerfile)
	}

	if t := strings.TrimSpace(req.Target); t != "" {
		args = append(args, "--target", t)
	}
	if nm := strings.TrimSpace(req.NetworkMode); nm != "" {
		args = append(args, "--network", nm)
	}
	args = append(args, sortedMapFlags("--build-arg", req.BuildArgs)...)
	args = append(args, sortedMapFlags("--label", req.Labels)...)
	for _, image := range cleanBuildxSpecs(req.CacheFrom) {
		args = append(args, "--cache-from", image)
	}
	progress := normalizeBuildProgress(req.Progress)
	switch {
	case progress == buildProgressQuiet:
		args = append(args, "--quiet")
	case !BuildkitEnabled():
	case progress == buildProgressVerbose:
		args = append(args, "--progress", "plain")
	default:
		args = append(args, "--progress", "auto")
	}
	args = append(args, req.ContextDir)

	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204 -- args built from trusted build config, passed without a shell.
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}
	return nil
}

// resolveDockerfilePath returns an absolute Dockerfile path for `--file`. A
// relative path is resolved against the build context.
func resolveDockerfilePath(req BuildRequest) string {
	dockerfile := strings.TrimSpace(req.Dockerfile)
	if dockerfile == "" {
		return ""
	}
	if filepath.IsAbs(dockerfile) {
		return dockerfile
	}
	return filepath.Join(req.ContextDir, dockerfile)
}

func writeTempDockerfile(content []byte) (string, func(), error) {
	file, err := os.CreateTemp("", "enclave-dockerfile-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dockerfile: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) } // #nosec G104 -- best-effort cleanup of our own temp file.
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("write temp dockerfile: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close temp dockerfile: %w", err)
	}
	return path, cleanup, nil
}

func shouldUseBuildx(req BuildRequest) bool {
	return len(req.BuildxCacheFrom) > 0 || len(req.BuildxCacheTo) > 0
}

// BuildxAvailable reports whether the docker buildx CLI plugin is installed.
func BuildxAvailable(ctx context.Context) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, dockerBinary, "buildx", "version")
	return cmd.Run() == nil
}

// buildWithBuildx builds an image via `docker buildx build --load`.
func buildWithBuildx(ctx context.Context, req BuildRequest, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if out == nil {
		out = io.Discard
	}
	normalized, err := normalizeBuildRequestPaths(req)
	if err != nil {
		return err
	}
	args, stdin := buildxCommandArgs(normalized)
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204 -- args are passed without a shell.
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker buildx build failed: %w", err)
	}
	return nil
}

func normalizeBuildRequestPaths(req BuildRequest) (BuildRequest, error) {
	if req.Dockerfile != "" {
		if filepath.IsAbs(req.Dockerfile) {
			if !util.PathWithin(req.ContextDir, req.Dockerfile) {
				return req, fmt.Errorf("dockerfile must be within the build context: %s", req.Dockerfile)
			}
			rel, err := filepath.Rel(req.ContextDir, req.Dockerfile)
			if err != nil {
				return req, err
			}
			req.Dockerfile = filepath.ToSlash(rel)
		} else {
			cleaned := filepath.ToSlash(filepath.Clean(req.Dockerfile))
			if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
				return req, fmt.Errorf("dockerfile path traversal not allowed: %s", req.Dockerfile)
			}
			req.Dockerfile = cleaned
		}
	}
	if len(req.DockerfileContent) > 0 && req.Dockerfile == "" {
		return req, fmt.Errorf("dockerfile path is required when dockerfile content is provided")
	}
	return req, nil
}

func buildxCommandArgs(req BuildRequest) ([]string, *bytes.Reader) {
	args := []string{"buildx", "build", "--load"}
	for _, tag := range req.Tags {
		if strings.TrimSpace(tag) == "" {
			continue
		}
		args = append(args, "--tag", tag)
	}
	if len(req.DockerfileContent) > 0 {
		args = append(args, "--file", "-")
	} else if strings.TrimSpace(req.Dockerfile) != "" {
		args = append(args, "--file", req.Dockerfile)
	}
	if strings.TrimSpace(req.Target) != "" {
		args = append(args, "--target", req.Target)
	}
	if strings.TrimSpace(req.NetworkMode) != "" {
		args = append(args, "--network", req.NetworkMode)
		if strings.TrimSpace(req.NetworkMode) == buildNetworkHost {
			args = append(args, "--allow", buildxNetworkHostEntitlement)
		}
	}
	args = append(args, sortedMapFlags("--build-arg", req.BuildArgs)...)
	args = append(args, sortedMapFlags("--label", req.Labels)...)
	for _, image := range cleanBuildxSpecs(req.CacheFrom) {
		args = append(args, "--cache-from", image)
	}
	for _, spec := range cleanBuildxSpecs(req.BuildxCacheFrom) {
		args = append(args, "--cache-from", spec)
	}
	for _, spec := range cleanBuildxSpecs(req.BuildxCacheTo) {
		args = append(args, "--cache-to", spec)
	}
	switch normalizeBuildProgress(req.Progress) {
	case buildProgressQuiet:
		args = append(args, "--quiet")
	case buildProgressVerbose:
		args = append(args, "--progress", "plain")
	case buildProgressCompact:
		args = append(args, "--progress", "auto")
	}
	args = append(args, req.ContextDir)
	if len(req.DockerfileContent) > 0 {
		return args, bytes.NewReader(req.DockerfileContent)
	}
	return args, nil
}

func cleanBuildxSpecs(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, raw := range values {
		if value := strings.TrimSpace(raw); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return util.Dedupe(cleaned)
}

// BuildkitEnabled reports whether BuildKit is enabled via DOCKER_BUILDKIT.
func BuildkitEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("DOCKER_BUILDKIT")))
	if raw == "" {
		return true
	}
	return raw != "0" && raw != "false"
}

const (
	buildProgressQuiet   = "quiet"
	buildProgressCompact = "compact"
	buildProgressVerbose = "verbose"
)

const (
	buildNetworkHost             = "host"
	buildxNetworkHostEntitlement = "network.host"
)

func normalizeBuildProgress(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}
