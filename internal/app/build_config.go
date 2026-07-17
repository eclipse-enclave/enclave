// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"strings"

	"enclave/internal/devcontainer"
	"enclave/internal/model"
	"enclave/internal/util"
)

const (
	buildTargetSlim     = "slim"
	buildTargetStandard = "standard"
	buildTagLatest      = "latest"
)

type buildConfig struct {
	BaseImage    string
	Devcontainer *devcontainer.Spec
	HashSuffix   string
	ImageName    string
	Target       string
}

func resolveBuildConfig(opts model.BuildOptions, tool string, project model.Project, appRoot string) (buildConfig, error) {
	imageName := strings.TrimSpace(opts.ImageName)
	if imageName == "" {
		return buildConfig{}, fmt.Errorf("--image-name requires a non-empty value")
	}

	target := resolveBuildTarget(opts)
	hashSuffix := "target-" + target + "-" + buildArgsHashSuffix(opts, tool)

	if opts.Devcontainer {
		spec, found, err := devcontainer.ResolveSpec(project, devcontainer.ResolveOptions{
			ForceBaseImage: opts.ForceBaseImage,
		})
		if err != nil {
			return buildConfig{}, err
		}
		if !found {
			return buildConfig{}, fmt.Errorf("no devcontainer.json found in %s (expected .devcontainer/devcontainer.json or devcontainer.json)", project.RealDir)
		}
		if spec.BaseImage == "" {
			return buildConfig{}, fmt.Errorf("devcontainer base image could not be resolved")
		}
		if !opts.ImageNameSet {
			imageName = derivePerToolImageName(tool, "devcontainer", spec.Hash, target)
		}
		return buildConfig{
			BaseImage:    spec.BaseImage,
			Devcontainer: &spec,
			HashSuffix:   hashSuffix + "-devcontainer-" + spec.Hash,
			ImageName:    imageName,
			Target:       target,
		}, nil
	}

	if strings.TrimSpace(opts.BaseImage) != "" {
		baseImage := strings.TrimSpace(opts.BaseImage)
		if !opts.ImageNameSet {
			imageName = derivePerToolImageName(tool, "base", util.HashString(baseImage), target)
		}
		return buildConfig{
			BaseImage:  baseImage,
			HashSuffix: hashSuffix + "-base-" + util.HashString(baseImage),
			ImageName:  imageName,
			Target:     target,
		}, nil
	}
	if !opts.ImageNameSet {
		if opts.Features != nil {
			configHash := util.HashString(buildArgsHashSuffix(opts, tool))
			imageName = derivePerToolImageName(tool, "custom", configHash, target)
		} else if branchImage, ok := branchImageName(appRoot, tool, target); ok {
			imageName = branchImage
		} else {
			imageName = fmt.Sprintf("%s-%s:%s", model.AppName, tool, targetToTagName(target))
		}
	}
	return buildConfig{ImageName: imageName, Target: target, HashSuffix: hashSuffix}, nil
}

// resolveBuildTarget returns the Docker target stage name.
// Note: "slim" is a logical target that maps to "standard" stage with empty features.
func resolveBuildTarget(opts model.BuildOptions) string {
	if opts.Slim {
		return buildTargetSlim // Logical target, maps to standard stage
	}
	return buildTargetStandard
}

// dockerTarget converts a logical target to the actual Dockerfile stage name.
func dockerTarget(target string) string {
	if target == buildTargetSlim {
		return buildTargetStandard // slim uses the standard stage with empty features
	}
	return target
}

func targetToTagName(target string) string {
	switch target {
	case buildTargetStandard:
		return buildTagLatest
	default:
		return target // slim is the only other reachable target; it keeps its name
	}
}

func derivePerToolImageName(tool string, prefix string, hash string, target string) string {
	hash = model.ShortHash(hash)
	return model.AppName + "-" + tool + ":" + prefix + "-" + hash + "-" + targetToTagName(target)
}

func buildArgsHashSuffix(opts model.BuildOptions, tool string) string {
	agentTools := resolveAgentToolsArg(tool)
	features := resolveFeaturesArg(opts)
	suffix := "agent-tools-" + util.HashString(agentTools) + "-features-" + util.HashString(features)
	if opts.UseRemoteUser {
		suffix += "-remote-user"
	}
	// Explicit build UID/GID values affect derived image names before host
	// resolution. The effective host fallback is added to the rebuild hash
	// later by appendEffectiveBuildIdentityHashSuffix.
	if strings.TrimSpace(opts.BuildUID) != "" {
		suffix += "-build-uid-" + util.HashString(strings.TrimSpace(opts.BuildUID))
	}
	if strings.TrimSpace(opts.BuildGID) != "" {
		suffix += "-build-gid-" + util.HashString(strings.TrimSpace(opts.BuildGID))
	}
	return suffix
}
