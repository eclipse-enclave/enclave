// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A2ASpec is the sandbox.a2a block of a tool's spec.yaml (proposal 9, 20).
// It and its validation live here, not in internal/tools, so the spec loader
// does not pull internal/tools into the gateway proxy build; tools aliases it.
type A2ASpec struct {
	// Command is the headless argv; the prompt arrives on stdin.
	Command []string `json:"command"`
	// NewSessionArgs are appended for a new context; "{sessionId}" is a driver-chosen ID.
	NewSessionArgs []string `json:"newSessionArgs,omitempty"`
	// ResumeArgs are appended to resume a harness session; "{sessionId}" is substituted.
	ResumeArgs []string `json:"resumeArgs"`
	// EventFormat names the driver that decodes the event stream, e.g. "claude-stream-json".
	EventFormat string `json:"eventFormat"`
	// VersionCommand prints the harness version; Versions is the accepted range.
	VersionCommand []string `json:"versionCommand,omitempty"`
	Versions       string   `json:"versions,omitempty"`
	// Skills become the agent card skills; the tool spec is their only v1 source.
	Skills []A2ASkillSpec `json:"skills,omitempty"`
	// OrchestratorArgs are appended to the interactive harness command of orchestrator sessions.
	OrchestratorArgs []string `json:"orchestratorArgs,omitempty"`
}

// A2ASkillSpec is one agent card skill.
type A2ASkillSpec struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
}

const (
	// A2ASessionIDPlaceholder is replaced by the harness session ID in NewSessionArgs and ResumeArgs.
	A2ASessionIDPlaceholder = "{sessionId}"
	// A2AEventFormatClaudeStreamJSON is Claude Code's "-p --output-format stream-json".
	A2AEventFormatClaudeStreamJSON = "claude-stream-json"
	// A2AEventFormatCodexExecJSON is Codex's "exec --json".
	A2AEventFormatCodexExecJSON = "codex-exec-json"
	// A2AEventFormatPiJSON is Pi's "-p --mode json".
	A2AEventFormatPiJSON = "pi-json"
	// A2AEventFormatCopilotJSON is Copilot's "--output-format json".
	A2AEventFormatCopilotJSON = "copilot-json"
)

// A2AEventFormats lists the event formats a driver exists for.
var A2AEventFormats = []string{
	A2AEventFormatClaudeStreamJSON,
	A2AEventFormatCodexExecJSON,
	A2AEventFormatPiJSON,
	A2AEventFormatCopilotJSON,
}

// ValidateA2ASpec checks an a2a block; errors name the field as "a2a.<field>".
func ValidateA2ASpec(spec *A2ASpec) error {
	if spec == nil {
		return nil
	}
	if len(spec.Command) == 0 {
		return errors.New("a2a.command is required")
	}
	if err := validateA2AArgv("a2a.command", spec.Command, 0); err != nil {
		return err
	}
	if len(spec.ResumeArgs) == 0 {
		return errors.New("a2a.resumeArgs is required")
	}
	if err := validateA2AArgv("a2a.resumeArgs", spec.ResumeArgs, 1); err != nil {
		return err
	}
	if len(spec.NewSessionArgs) > 0 {
		if err := validateA2AArgv("a2a.newSessionArgs", spec.NewSessionArgs, 1); err != nil {
			return err
		}
	}
	if spec.EventFormat == "" {
		return errors.New("a2a.eventFormat is required")
	}
	if !knownA2AEventFormat(spec.EventFormat) {
		return fmt.Errorf("a2a.eventFormat %q is not supported (supported: %s)", spec.EventFormat, strings.Join(A2AEventFormats, ", "))
	}
	if (len(spec.VersionCommand) == 0) != (spec.Versions == "") {
		return errors.New("a2a.versionCommand and a2a.versions must be set together")
	}
	if len(spec.VersionCommand) > 0 {
		if err := validateA2AArgv("a2a.versionCommand", spec.VersionCommand, 0); err != nil {
			return err
		}
		if _, err := ParseA2AVersionRange(spec.Versions); err != nil {
			return fmt.Errorf("a2a.versions: %w", err)
		}
	}
	if len(spec.OrchestratorArgs) > 0 {
		if err := validateA2AArgv("a2a.orchestratorArgs", spec.OrchestratorArgs, 0); err != nil {
			return err
		}
	}
	return validateA2ASkills(spec.Skills)
}

// validateA2AArgv rejects blank tokens and requires the placeholder exactly placeholders times.
func validateA2AArgv(field string, argv []string, placeholders int) error {
	count := 0
	for i, arg := range argv {
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("%s[%d] is blank", field, i)
		}
		count += strings.Count(arg, A2ASessionIDPlaceholder)
	}
	if count != placeholders {
		if placeholders == 0 {
			return fmt.Errorf("%s must not contain %s", field, A2ASessionIDPlaceholder)
		}
		return fmt.Errorf("%s must contain %s exactly once", field, A2ASessionIDPlaceholder)
	}
	return nil
}

func knownA2AEventFormat(format string) bool {
	for _, f := range A2AEventFormats {
		if f == format {
			return true
		}
	}
	return false
}

func validateA2ASkills(skills []A2ASkillSpec) error {
	seen := map[string]bool{}
	for i, skill := range skills {
		field := fmt.Sprintf("a2a.skills[%d]", i)
		if strings.TrimSpace(skill.ID) == "" {
			return fmt.Errorf("%s.id is required", field)
		}
		if seen[skill.ID] {
			return fmt.Errorf("%s.id %q is duplicated", field, skill.ID)
		}
		seen[skill.ID] = true
		if strings.TrimSpace(skill.Name) == "" {
			return fmt.Errorf("%s.name is required", field)
		}
		if strings.TrimSpace(skill.Description) == "" {
			return fmt.Errorf("%s.description is required", field)
		}
		for j, tag := range skill.Tags {
			if strings.TrimSpace(tag) == "" {
				return fmt.Errorf("%s.tags[%d] is blank", field, j)
			}
		}
	}
	return nil
}

// A2AVersion is a major.minor.patch harness version.
type A2AVersion struct{ Major, Minor, Patch int }

func (v A2AVersion) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

func (v A2AVersion) compare(o A2AVersion) int {
	switch {
	case v.Major != o.Major:
		return sign(v.Major - o.Major)
	case v.Minor != o.Minor:
		return sign(v.Minor - o.Minor)
	default:
		return sign(v.Patch - o.Patch)
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

var a2aVersionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseA2AVersion parses "major.minor.patch" exactly.
func ParseA2AVersion(s string) (A2AVersion, error) {
	m := a2aVersionPattern.FindStringSubmatch(s)
	if m == nil || m[0] != s {
		return A2AVersion{}, fmt.Errorf("version %q is not major.minor.patch", s)
	}
	return a2aVersionFromMatch(m)
}

// ExtractA2AVersion finds the first major.minor.patch in a version command's output.
func ExtractA2AVersion(output string) (A2AVersion, error) {
	m := a2aVersionPattern.FindStringSubmatch(output)
	if m == nil {
		return A2AVersion{}, fmt.Errorf("no major.minor.patch version in %q", strings.TrimSpace(output))
	}
	return a2aVersionFromMatch(m)
}

func a2aVersionFromMatch(m []string) (A2AVersion, error) {
	var parts [3]int
	for i, digits := range m[1:] {
		n, err := strconv.Atoi(digits)
		if err != nil {
			return A2AVersion{}, fmt.Errorf("version %q: %w", m[0], err)
		}
		parts[i] = n
	}
	return A2AVersion{Major: parts[0], Minor: parts[1], Patch: parts[2]}, nil
}

type a2aVersionConstraint struct {
	op string
	v  A2AVersion
}

// A2AVersionRange is a space-separated conjunction such as ">=2.1.306 <3.0.0".
type A2AVersionRange struct {
	text        string
	constraints []a2aVersionConstraint
}

func (r A2AVersionRange) String() string { return r.text }

// ParseA2AVersionRange accepts operators >=, >, <=, < and = before each version.
func ParseA2AVersionRange(s string) (A2AVersionRange, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return A2AVersionRange{}, errors.New("empty version range")
	}
	r := A2AVersionRange{text: strings.Join(fields, " ")}
	for _, field := range fields {
		op := ""
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(field, candidate) {
				op = candidate
				break
			}
		}
		if op == "" {
			return A2AVersionRange{}, fmt.Errorf("constraint %q needs an operator (>=, >, <=, <, =)", field)
		}
		v, err := ParseA2AVersion(strings.TrimPrefix(field, op))
		if err != nil {
			return A2AVersionRange{}, fmt.Errorf("constraint %q: %w", field, err)
		}
		r.constraints = append(r.constraints, a2aVersionConstraint{op: op, v: v})
	}
	return r, nil
}

// Contains reports whether v satisfies every constraint.
func (r A2AVersionRange) Contains(v A2AVersion) bool {
	for _, c := range r.constraints {
		cmp := v.compare(c.v)
		ok := false
		switch c.op {
		case ">=":
			ok = cmp >= 0
		case ">":
			ok = cmp > 0
		case "<=":
			ok = cmp <= 0
		case "<":
			ok = cmp < 0
		case "=":
			ok = cmp == 0
		}
		if !ok {
			return false
		}
	}
	return true
}
