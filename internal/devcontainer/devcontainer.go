// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package devcontainer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tidwall/jsonc"

	"enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

type Spec struct {
	ConfigPath     string
	Image          string
	DockerfilePath string
	ContextDir     string
	BuildArgs      map[string]string
	Hash           string
	BaseImage      string
	RuntimeConfig  model.DevcontainerConfig
}

type ResolveOptions struct {
	ForceBaseImage bool
}

type config struct {
	Image              string                   `json:"image"`
	DockerFile         string                   `json:"dockerFile"`
	Context            string                   `json:"context"`
	Build              *devcontainerBuildConfig `json:"build"`
	RunArgs            []string                 `json:"runArgs"`
	Mounts             mounts                   `json:"mounts"`
	WorkspaceFolder    string                   `json:"workspaceFolder"`
	WorkspaceMount     string                   `json:"workspaceMount"`
	RemoteUser         string                   `json:"remoteUser"`
	ContainerEnv       map[string]string        `json:"containerEnv"`
	RemoteEnv          map[string]string        `json:"remoteEnv"`
	PostCreateCommand  command                  `json:"postCreateCommand"`
	PostStartCommand   command                  `json:"postStartCommand"`
	UpdateRemoteUserID *bool                    `json:"updateRemoteUserUID"`
	DockerComposeFile  json.RawMessage          `json:"dockerComposeFile"`
	Service            string                   `json:"service"`
	Features           map[string]interface{}   `json:"features"`
	ForwardPorts       []json.RawMessage        `json:"forwardPorts"`
}

type devcontainerBuildConfig struct {
	Dockerfile string            `json:"dockerfile"`
	DockerFile string            `json:"dockerFile"`
	Context    string            `json:"context"`
	Args       map[string]string `json:"args"`
}

type command struct {
	Value string
}

func (c *command) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		c.Value = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		c.Value = strings.TrimSpace(s)
		return nil
	}
	if data[0] == '[' {
		var parts []string
		if err := json.Unmarshal(data, &parts); err != nil {
			return err
		}
		c.Value = strings.TrimSpace(shellJoin(parts))
		return nil
	}
	return fmt.Errorf("unsupported command format")
}

type mount struct {
	Raw         string
	Type        string `json:"type"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	Consistency string `json:"consistency"`
	ReadOnly    *bool  `json:"readOnly"`
}

func (m mount) String() string {
	if strings.TrimSpace(m.Raw) != "" {
		return strings.TrimSpace(m.Raw)
	}
	mountType := strings.TrimSpace(m.Type)
	source := strings.TrimSpace(m.Source)
	target := strings.TrimSpace(m.Target)
	if mountType == "" {
		mountType = inferMountType(source)
	}
	if mountType == "" {
		return ""
	}
	parts := []string{"type=" + mountType}
	if source != "" {
		parts = append(parts, "source="+source)
	}
	if target != "" {
		parts = append(parts, "target="+target)
	}
	if m.Consistency != "" {
		parts = append(parts, "consistency="+m.Consistency)
	}
	if m.ReadOnly != nil && *m.ReadOnly {
		parts = append(parts, "readonly")
	}
	return strings.Join(parts, ",")
}

type mounts []mount

func (m *mounts) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] != '[' {
		return fmt.Errorf("mounts must be an array")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	entries := make([]mount, 0, len(raw))
	for _, item := range raw {
		item = bytes.TrimSpace(item)
		if len(item) == 0 {
			continue
		}
		if item[0] == '"' {
			var s string
			if err := json.Unmarshal(item, &s); err != nil {
				return err
			}
			entries = append(entries, mount{Raw: s})
			continue
		}
		var mnt mount
		if err := json.Unmarshal(item, &mnt); err != nil {
			return err
		}
		entries = append(entries, mnt)
	}
	*m = entries
	return nil
}

func ResolveSpec(project model.Project, opts ResolveOptions) (Spec, bool, error) {
	configPath := findConfig(project.RealDir)
	if configPath == "" {
		return Spec{}, false, nil
	}

	// #nosec G304 -- configPath is discovered from trusted project-local devcontainer locations.
	rawOriginal, err := os.ReadFile(configPath)
	if err != nil {
		return Spec{}, false, err
	}

	raw := stripJSONC(rawOriginal)

	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Spec{}, false, formatParseError(configPath, rawOriginal, raw, err)
	}

	configDir := filepath.Dir(configPath)
	image := strings.TrimSpace(cfg.Image)
	dockerfile := strings.TrimSpace(resolveDockerfile(cfg))
	runtimeConfig := resolveRuntimeConfig(cfg, project)

	if image != "" && dockerfile != "" {
		logx.Warnf("devcontainer.json sets both image and dockerFile; using image.")
	}
	warnUnsupportedConfig(cfg)

	spec := Spec{
		ConfigPath:    configPath,
		RuntimeConfig: runtimeConfig,
	}

	if image != "" {
		if err := validateImageReference(image); err != nil {
			return Spec{}, false, fmt.Errorf("devcontainer.json image %q is invalid: %w", image, err)
		}
		if !hasExplicitTagOrDigest(image) {
			logx.Warnf("devcontainer image %q has no explicit tag or digest; it defaults to latest and may drift", image)
		}
		if isLikelyNonDebian(image) && !opts.ForceBaseImage {
			return Spec{}, false, fmt.Errorf(
				"devcontainer.json specifies image %q which appears non-Debian (best-effort heuristic). enclave requires a Debian/Ubuntu base image. Use a Debian-based variant (for example \"node:22-bookworm\") or use build.dockerfile to provide a compatible base. Use --force-base-image to bypass this check",
				image,
			)
		}
		hash, err := specHash(configPath, "", image, nil)
		if err != nil {
			return Spec{}, false, err
		}
		spec.Image = image
		spec.Hash = hash
		spec.BaseImage = image
		return spec, true, nil
	}

	if dockerfile == "" {
		return Spec{}, false, fmt.Errorf("devcontainer.json must define image or dockerFile/build.dockerfile")
	}

	context := strings.TrimSpace(resolveContext(cfg))
	if context == "" {
		context = "."
	}

	dockerfilePath, err := resolvePath(configDir, dockerfile)
	if err != nil {
		return Spec{}, false, err
	}
	if !util.PathExists(dockerfilePath) {
		return Spec{}, false, fmt.Errorf("devcontainer dockerfile not found at %s", dockerfilePath)
	}

	contextDir, err := resolvePath(configDir, context)
	if err != nil {
		return Spec{}, false, err
	}
	if !util.PathExists(contextDir) {
		return Spec{}, false, fmt.Errorf("devcontainer build context not found at %s", contextDir)
	}

	buildArgs := map[string]string{}
	if cfg.Build != nil && len(cfg.Build.Args) > 0 {
		buildArgs = cfg.Build.Args
	}

	hash, err := specHash(configPath, dockerfilePath, "", buildArgs)
	if err != nil {
		return Spec{}, false, err
	}
	tag := imageTag(hash)

	spec.DockerfilePath = dockerfilePath
	spec.ContextDir = contextDir
	spec.BuildArgs = buildArgs
	spec.Hash = hash
	spec.BaseImage = tag

	return spec, true, nil
}

func BuildImage(spec Spec) error {
	if spec.BaseImage == "" {
		return fmt.Errorf("devcontainer base image tag is empty")
	}
	if spec.DockerfilePath == "" {
		return fmt.Errorf("devcontainer dockerfile path is empty")
	}
	if spec.ContextDir == "" {
		return fmt.Errorf("devcontainer build context is empty")
	}

	logx.Infof("Building devcontainer base image from %s.", spec.DockerfilePath)

	req := docker.BuildRequest{
		ContextDir: spec.ContextDir,
		Dockerfile: spec.DockerfilePath,
		Tags:       []string{spec.BaseImage},
		BuildArgs:  spec.BuildArgs,
	}
	if err := docker.Build(context.Background(), req, os.Stdout); err != nil {
		return fmt.Errorf("failed to build devcontainer base image: %w", err)
	}
	return nil
}

func resolveDockerfile(cfg config) string {
	if cfg.Build != nil {
		if cfg.Build.Dockerfile != "" {
			return cfg.Build.Dockerfile
		}
		if cfg.Build.DockerFile != "" {
			return cfg.Build.DockerFile
		}
	}
	return cfg.DockerFile
}

func resolveContext(cfg config) string {
	if cfg.Build != nil && cfg.Build.Context != "" {
		return cfg.Build.Context
	}
	return cfg.Context
}

func resolveForwardPorts(raw []json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var result []string
	for _, entry := range raw {
		entry = bytes.TrimSpace(entry)
		if len(entry) == 0 {
			continue
		}
		var n int
		if err := json.Unmarshal(entry, &n); err == nil {
			result = append(result, fmt.Sprintf("%d", n))
			continue
		}
		var s string
		if err := json.Unmarshal(entry, &s); err == nil {
			s = strings.TrimSpace(s)
			if s != "" {
				result = append(result, s)
			}
		}
	}
	return result
}

func resolveRuntimeConfig(cfg config, project model.Project) model.DevcontainerConfig {
	workspaceFolder := strings.TrimSpace(cfg.WorkspaceFolder)
	if workspaceFolder != "" {
		workspaceFolder = expandVars(workspaceFolder, project, "", true)
	}
	workspaceMount := strings.TrimSpace(cfg.WorkspaceMount)
	if workspaceMount != "" {
		// workspaceMount sources should not pull host env into bind paths.
		workspaceMount = expandVars(workspaceMount, project, workspaceFolder, false)
		if workspaceFolder == "" {
			if target := extractMountTarget(workspaceMount); target != "" {
				workspaceFolder = target
			}
		}
	}
	containerWorkspace := workspaceFolder
	if containerWorkspace == "" {
		containerWorkspace = project.Dir
	}
	containerEnv := expandEnv(cfg.ContainerEnv, project, containerWorkspace)
	runArgs := expandArgs(cfg.RunArgs, project, containerWorkspace)
	mounts := expandMounts(cfg.Mounts, project, containerWorkspace)
	return model.DevcontainerConfig{
		WorkspaceFolder:    workspaceFolder,
		WorkspaceMount:     workspaceMount,
		RemoteUser:         strings.TrimSpace(cfg.RemoteUser),
		ContainerEnv:       containerEnv,
		RunArgs:            runArgs,
		Mounts:             mounts,
		PostCreateCommand:  strings.TrimSpace(cfg.PostCreateCommand.Value),
		PostStartCommand:   strings.TrimSpace(cfg.PostStartCommand.Value),
		UpdateRemoteUserID: cfg.UpdateRemoteUserID,
		ForwardPorts:       resolveForwardPorts(cfg.ForwardPorts),
	}
}

func findConfig(projectDir string) string {
	candidates := []string{
		filepath.Join(projectDir, model.DevcontainerDir, model.DevcontainerFilename),
		filepath.Join(projectDir, model.DevcontainerFilename),
	}
	for _, candidate := range candidates {
		if util.PathExists(candidate) {
			return candidate
		}
	}
	return ""
}

func resolvePath(baseDir, target string) (string, error) {
	if filepath.IsAbs(target) {
		return filepath.Clean(target), nil
	}
	joined := filepath.Join(baseDir, target)
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", joined, err)
	}
	return abs, nil
}

func validateImageReference(image string) error {
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("image value is empty")
	}
	if strings.ContainsAny(image, " \t\r\n") {
		return fmt.Errorf("image must not contain whitespace")
	}
	if strings.ContainsAny(image, `;&|`+"`"+`$<>\'"(){}[]`) {
		return fmt.Errorf("image contains disallowed shell metacharacters")
	}
	return nil
}

func splitImageRef(ref string) (name string, tag string, hasDigest bool) {
	name = strings.TrimSpace(strings.ToLower(ref))
	if name == "" {
		return "", "", false
	}
	if idx := strings.Index(name, "@"); idx >= 0 {
		name = name[:idx]
		hasDigest = true
	}
	lastSlash := strings.LastIndex(name, "/")
	lastColon := strings.LastIndex(name, ":")
	if lastColon > lastSlash {
		tag = name[lastColon+1:]
		name = name[:lastColon]
	}
	return name, tag, hasDigest
}

func hasExplicitTagOrDigest(image string) bool {
	_, tag, hasDigest := splitImageRef(image)
	return hasDigest || tag != ""
}

func isLikelyNonDebian(image string) bool {
	ref := strings.TrimSpace(strings.ToLower(image))
	if ref == "" {
		return false
	}

	name, tag, _ := splitImageRef(ref)

	if strings.Contains(tag, "alpine") || strings.Contains(tag, "distroless") {
		return true
	}

	knownNonDebianBases := []string{
		"alpine",
		"archlinux",
		"fedora",
		"centos",
		"amazonlinux",
		"rockylinux",
	}

	parts := strings.Split(name, "/")
	for _, part := range parts {
		for _, base := range knownNonDebianBases {
			if part == base {
				return true
			}
		}
	}

	return false
}

func specHash(configPath, dockerfilePath, image string, buildArgs map[string]string) (string, error) {
	configHash, err := util.HashFile(configPath)
	if err != nil {
		return "", err
	}

	parts := []string{configHash}

	if dockerfilePath != "" {
		dockerfileHash, err := util.HashFile(dockerfilePath)
		if err != nil {
			return "", err
		}
		parts = append(parts, dockerfileHash)
	}

	if image != "" {
		parts = append(parts, "image:"+image)
	}
	if len(buildArgs) > 0 {
		parts = append(parts, "args:"+strings.Join(sortedBuildArgs(buildArgs), "|"))
	}

	return util.HashString(strings.Join(parts, "|")), nil
}

func imageTag(hash string) string {
	return model.AppName + "-devcontainer-base:" + model.ShortHash(hash)
}

func sortedBuildArgs(args map[string]string) []string {
	if len(args) == 0 {
		return nil
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+args[key])
	}
	return out
}

func warnUnsupportedConfig(cfg config) {
	if len(cfg.DockerComposeFile) > 0 || strings.TrimSpace(cfg.Service) != "" {
		logx.Warnf("devcontainer dockerComposeFile/service is not supported; falling back to image/dockerfile only.")
	}
	if len(cfg.Features) > 0 {
		ignored, suggested := summarizeUnsupportedDevcontainerFeatures(cfg.Features)
		if len(ignored) == 0 {
			logx.Warnf("devcontainer features are not processed by enclave and will be ignored.")
		} else {
			logx.Warnf("devcontainer features are not processed by enclave and will be ignored: %s", strings.Join(ignored, ", "))
		}
		logx.Warnf("To use devcontainer features, prebuild with devcontainer CLI (for example: devcontainer build --workspace-folder .) and reference that image in devcontainer.json.")
		if len(suggested) > 0 {
			logx.Warnf("Closest enclave equivalents: --features %s", strings.Join(suggested, ","))
		} else {
			logx.Warnf("Use --features to opt into enclave features explicitly for devcontainer mode.")
		}
	}
	if len(cfg.RemoteEnv) > 0 {
		logx.Warnf("devcontainer remoteEnv is not supported; ignoring.")
	}
	if cfg.UpdateRemoteUserID != nil && *cfg.UpdateRemoteUserID {
		logx.Warnf("devcontainer updateRemoteUserUID is not supported; ignoring.")
	}
}

var devcontainerFeatureEquivalents = map[string]string{
	"ghcr.io/devcontainers/features/github-cli": "github-cli",
	"ghcr.io/devcontainers/features/node":       "node-dev",
	"ghcr.io/devcontainers/features/python":     "python-dev",
}

func summarizeUnsupportedDevcontainerFeatures(features map[string]interface{}) ([]string, []string) {
	if len(features) == 0 {
		return nil, nil
	}

	ignored := make([]string, 0, len(features))
	suggestedSet := make(map[string]struct{}, len(features))
	for feature := range features {
		feature = strings.TrimSpace(feature)
		if feature == "" {
			continue
		}
		ignored = append(ignored, feature)
		if mapped, ok := mapDevcontainerFeature(feature); ok {
			suggestedSet[mapped] = struct{}{}
		}
	}
	sort.Strings(ignored)

	suggested := make([]string, 0, len(suggestedSet))
	for feature := range suggestedSet {
		suggested = append(suggested, feature)
	}
	sort.Strings(suggested)
	return ignored, suggested
}

func mapDevcontainerFeature(feature string) (string, bool) {
	normalized := normalizeDevcontainerFeature(feature)
	if normalized == "" {
		return "", false
	}
	mapped, ok := devcontainerFeatureEquivalents[normalized]
	return mapped, ok
}

func normalizeDevcontainerFeature(feature string) string {
	normalized := strings.ToLower(strings.TrimSpace(feature))
	if normalized == "" {
		return ""
	}

	normalized, _, _ = splitImageRef(normalized)
	return normalized
}

func expandEnv(env map[string]string, project model.Project, workspaceFolder string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = expandVars(value, project, workspaceFolder, true)
	}
	return out
}

func expandArgs(args []string, project model.Project, workspaceFolder string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for _, arg := range args {
		// Refuse ${localEnv:...} expansion in runArgs entirely. runArgs is a CLI
		// argument list passed to docker run (e.g. -v, -e, --label), and any host
		// env value substituted there can end up bound into the container as a
		// mount source, env var, or label — all exfiltration channels. The
		// container-side substitutions (${containerWorkspaceFolder} etc.) and
		// ${localWorkspaceFolder} remain available.
		out = append(out, expandVars(arg, project, workspaceFolder, false))
	}
	return out
}

func expandMounts(mounts mounts, project model.Project, workspaceFolder string) []string {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		// Refuse ${localEnv:...} expansion in mount strings. Mount sources have no
		// legitimate use for arbitrary host env vars, and host secrets bound into
		// container paths are an exfiltration vector.
		expanded := expandVars(mount.String(), project, workspaceFolder, false)
		if expanded != "" {
			out = append(out, expanded)
		}
	}
	return out
}

// deniedLocalEnvPatterns lists fragments that are matched as case-insensitive
// substrings against the requested ${localEnv:NAME} key. Any match causes the
// expansion to fall through to the empty-string case, identical to "var not
// set", to avoid leaking host-side credentials into the container env or
// runArgs. The list intentionally errs toward over-blocking for credential-like
// patterns; legitimate vars (HOME, USER, LANG, PATH, ...) are unaffected.
var deniedLocalEnvPatterns = []string{
	// Suffix-style fragments (credential-bearing names).
	"_TOKEN", "_KEY", "_SECRET", "_PASSWORD", "_PASS",
	"_CREDENTIAL", "_CREDENTIALS", "_APIKEY", "_API_KEY",
	// Prefix-style fragments (cloud / registry / SDK env namespaces).
	"AWS_", "GITHUB_", "OPENAI_", "ANTHROPIC_", "GOOGLE_", "GCP_",
	"AZURE_", "DOCKER_", "NPM_TOKEN", "PYPI_", "CARGO_REGISTRY",
	// Specific names that act as ambient auth handles.
	"SSH_AUTH_SOCK", "KUBECONFIG",
}

// isLocalEnvNameAllowed reports whether ${localEnv:name} may be expanded.
// Patterns in deniedLocalEnvPatterns are matched as case-insensitive substrings
// against name (uppercased), so e.g. "MY_TOKEN" matches "_TOKEN" and is
// blocked.
func isLocalEnvNameAllowed(name string) bool {
	upper := strings.ToUpper(name)
	for _, pat := range deniedLocalEnvPatterns {
		if strings.Contains(upper, pat) {
			return false
		}
	}
	return true
}

func expandVars(value string, project model.Project, workspaceFolder string, allowLocalEnv bool) string {
	if value == "" {
		return value
	}
	warnedEnv := make(map[string]struct{})
	warnAndEmpty := func(key string, reason string) string {
		if _, warned := warnedEnv[key]; !warned {
			logx.Warnf("devcontainer localEnv %q %s; expanding to empty string", key, reason)
			warnedEnv[key] = struct{}{}
		}
		return ""
	}
	localWorkspace := project.Dir
	localWorkspaceBase := filepath.Base(project.Dir)
	containerWorkspace := workspaceFolder
	if containerWorkspace == "" {
		containerWorkspace = project.Dir
	}
	containerWorkspaceBase := filepath.Base(containerWorkspace)
	replacer := func(input string) string {
		switch input {
		case "localWorkspaceFolder":
			return localWorkspace
		case "localWorkspaceFolderBasename":
			return localWorkspaceBase
		case "containerWorkspaceFolder":
			return containerWorkspace
		case "containerWorkspaceFolderBasename":
			return containerWorkspaceBase
		default:
			if strings.HasPrefix(input, "localEnv:") {
				key := strings.TrimPrefix(input, "localEnv:")
				if !allowLocalEnv {
					return warnAndEmpty(key, "is not allowed in this position")
				}
				if !isLocalEnvNameAllowed(key) {
					return warnAndEmpty(key, "matches a credential-like pattern and is blocked")
				}
				envValue, ok := os.LookupEnv(key)
				if !ok {
					return warnAndEmpty(key, "is not set")
				}
				return envValue
			}
		}
		return "${" + input + "}"
	}
	var out strings.Builder
	for {
		start := strings.Index(value, "${")
		if start == -1 {
			out.WriteString(value)
			break
		}
		out.WriteString(value[:start])
		value = value[start+2:]
		end := strings.Index(value, "}")
		if end == -1 {
			out.WriteString("${")
			out.WriteString(value)
			break
		}
		token := value[:end]
		out.WriteString(replacer(token))
		value = value[end+1:]
	}
	return out.String()
}

func inferMountType(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return "volume"
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "~") || strings.Contains(trimmed, "${localWorkspaceFolder}") || strings.Contains(trimmed, "${localEnv:") {
		return "bind"
	}
	return "volume"
}

func extractMountTarget(mount string) string {
	parts := strings.Split(mount, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "target=") {
			return strings.TrimPrefix(part, "target=")
		}
		if strings.HasPrefix(part, "dst=") {
			return strings.TrimPrefix(part, "dst=")
		}
		if strings.HasPrefix(part, "destination=") {
			return strings.TrimPrefix(part, "destination=")
		}
	}
	return ""
}

func shellJoin(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		escaped = append(escaped, shellQuote(part))
	}
	return strings.Join(escaped, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$") {
		return value
	}
	escaped := strings.ReplaceAll(value, "'", `'\''`)
	return "'" + escaped + "'"
}

func formatParseError(configPath string, original, normalized []byte, err error) error {
	location := fmt.Sprintf("invalid devcontainer.json at %s", configPath)
	// json.SyntaxError offsets are for the normalized bytes that were passed to
	// json.Unmarshal.
	if line := jsonErrorLine(normalized, err); line > 0 {
		location = fmt.Sprintf("%s (line %d)", location, line)
	}
	hints := parseErrorHints(original, normalized, err)
	if len(hints) == 0 {
		return fmt.Errorf("%s: %w", location, err)
	}
	return fmt.Errorf("%s: %w (hint: %s)", location, err, strings.Join(hints, "; "))
}

func jsonErrorLine(data []byte, err error) int {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return lineForOffset(data, syntaxErr.Offset)
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return lineForOffset(data, typeErr.Offset)
	}
	return 0
}

func lineForOffset(data []byte, offset int64) int {
	if offset < 0 {
		return 1
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	return 1 + bytes.Count(data[:offset], []byte{'\n'})
}

func parseErrorHints(original, normalized []byte, err error) []string {
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		return nil
	}

	offset := int(syntaxErr.Offset)
	var hints []string

	if byteNearOffset(normalized, offset, '\'') || byteNearOffset(original, offset, '\'') {
		hints = append(hints, "single-quoted strings are not valid JSON")
	}
	if commaNearOffset(normalized, offset) {
		hints = append(hints, "unexpected comma (check for double commas or trailing commas not handled by JSONC)")
	}

	return hints
}

func byteNearOffset(data []byte, offset int, target byte) bool {
	for _, idx := range []int{offset - 3, offset - 2, offset - 1, offset, offset + 1} {
		if idx >= 0 && idx < len(data) && data[idx] == target {
			return true
		}
	}
	return false
}

func commaNearOffset(data []byte, offset int) bool {
	if byteNearOffset(data, offset, ',') {
		return true
	}
	off := offset - 2
	if off < 0 {
		off = 0
	}
	if prev := previousSignificantByte(data, off); prev == ',' {
		return true
	}
	if prev := previousSignificantByte(data, off-1); prev == ',' {
		return true
	}
	return false
}

func previousSignificantByte(data []byte, index int) byte {
	for i := index; i >= 0; i-- {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return data[i]
		}
	}
	return 0
}

// stripJSONC normalizes JSONC input into standard JSON.
func stripJSONC(input []byte) []byte {
	return jsonc.ToJSON(input)
}

// UserExistsInImage probes whether the given user resolves inside the image.
// It is used to decide whether a devcontainer remoteUser can be honored.
func UserExistsInImage(imageName string, user string) bool {
	user = strings.TrimSpace(user)
	if user == "" || strings.TrimSpace(imageName) == "" {
		return false
	}
	if err := docker.Run(context.Background(), &docker.ContainerConfig{
		Image:      imageName,
		Entrypoint: []string{"id"},
		Cmd:        []string{"-u", user},
	}, &docker.HostConfig{AutoRemove: true}, ""); err != nil {
		return false
	}
	return true
}
