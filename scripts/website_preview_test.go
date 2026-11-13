// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const (
	websitePreviewBuild  = "../.github/workflows/website-preview-build.yml"
	websitePreviewDeploy = "../.github/workflows/website-preview-deploy.yml"
)

type previewStep struct {
	Name string
	If   string
	Uses string
	Run  string
	With map[string]any
	Env  map[string]any
}

type previewJob struct {
	If          string
	Uses        string
	Secrets     any
	CacheMode   string `json:"cache-mode"`
	Needs       any
	Permissions any
	Concurrency map[string]any
	Steps       []previewStep
}

type previewWorkflow struct {
	// YAML 1.1 reads the key "on" as true.
	On          map[string]any `json:"true"`
	Permissions any
	Jobs        map[string]previewJob
}

func readPreviewWorkflow(t *testing.T, path string) previewWorkflow {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc previewWorkflow
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// The deploy token reaches only the called deploy job, which runs no PR code
// and deploys to the PR the trusted pull_request_target event names. Only
// invariants whose loss would break that, the build's isolation, the approval
// gate or the deploy/removal queue are checked here.
func TestWebsitePreviewTrustBoundary(t *testing.T) {
	const number = "${{ github.event.pull_request.number }}"
	const sha = "${{ github.event.pull_request.head.sha }}"
	read := map[string]any{"contents": "read"}
	deployPermissions := map[string]any{"contents": "read", "pull-requests": "write"}

	caller := readPreviewWorkflow(t, websitePreviewBuild)
	if _, ok := caller.On["pull_request_target"]; !ok || len(caller.On) != 1 {
		t.Fatalf("build triggers = %v, want only pull_request_target", caller.On)
	}
	if !reflect.DeepEqual(caller.Permissions, read) {
		t.Fatalf("build workflow permissions = %v, want %v", caller.Permissions, read)
	}
	authorize := caller.Jobs["authorize-preview"]
	// The check approves any label a writer adds; only this gate limits that
	// to the preview label.
	if want := "github.event.action != 'labeled' || github.event.label.name == 'preview'"; authorize.If != want {
		t.Fatalf("authorize condition = %q, want %q", authorize.If, want)
	}
	if len(authorize.Steps) != 1 || authorize.Steps[0].Uses != "" {
		t.Fatalf("authorize steps = %+v, want only the check, without PR code", authorize.Steps)
	}

	build := caller.Jobs["build-preview"]
	if build.Needs != "authorize-preview" || build.If != "needs.authorize-preview.outputs.approved == 'true'" {
		t.Fatalf("build needs %v if %q, want the authorize job's approval", build.Needs, build.If)
	}
	if build.Permissions != nil || build.CacheMode != "none" {
		t.Fatalf("build permissions %v cache-mode %q, want the workflow's read-only token and no cache", build.Permissions, build.CacheMode)
	}
	buildText, err := yaml.Marshal(build)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(buildText), "secrets.") || strings.Contains(string(buildText), "github.token") {
		t.Fatalf("build job references a token or secret:\n%s", buildText)
	}
	if checkout := build.Steps[0]; !strings.HasPrefix(checkout.Uses, "actions/checkout@") ||
		checkout.With["ref"] != sha || checkout.With["persist-credentials"] != false {
		t.Fatalf("build checkout = %+v, want the PR head without persisted credentials", checkout)
	}
	if setupNode := stepNamed(t, build.Steps, "Use Node.js 22.x"); setupNode.With["package-manager-cache"] != false {
		t.Fatalf("build setup-node = %+v, want automatic caching off", setupNode)
	}

	// A local call runs the deploy workflow from this commit with this run's
	// event, and the called workflow can only lower the permissions given.
	call := caller.Jobs["deploy-preview"]
	if call.Needs != "build-preview" || call.Uses != "./.github/workflows/website-preview-deploy.yml" ||
		!reflect.DeepEqual(call.Permissions, deployPermissions) ||
		!reflect.DeepEqual(call.Secrets, map[string]any{"DEPLOY_TOKEN": "${{ secrets.DEPLOY_TOKEN }}"}) {
		t.Fatalf("deploy call = %+v, want the local deploy workflow after the build with only DEPLOY_TOKEN and %v", call, deployPermissions)
	}

	called := readPreviewWorkflow(t, websitePreviewDeploy)
	if _, ok := called.On["workflow_call"]; !ok || len(called.On) != 1 {
		t.Fatalf("deploy triggers = %v, want only workflow_call", called.On)
	}
	if !reflect.DeepEqual(called.Permissions, deployPermissions) {
		t.Fatalf("deploy workflow permissions = %v, want %v", called.Permissions, deployPermissions)
	}
	deploy := called.Jobs["deploy-preview"]
	if deploy.If != "github.event_name == 'pull_request_target'" || deploy.Permissions != nil {
		t.Fatalf("deploy if %q permissions %v, want the caller's pull_request_target event and no grants of its own", deploy.If, deploy.Permissions)
	}
	group := map[string]any{"group": "website-preview-" + number, "queue": "max"}
	if !reflect.DeepEqual(deploy.Concurrency, group) {
		t.Fatalf("deploy concurrency = %v, want %v", deploy.Concurrency, group)
	}
	if check := deploy.Steps[0]; check.Env["NUMBER"] != number || check.Env["HEAD_SHA"] != sha {
		t.Fatalf("first deploy step = %+v, want the check of the event's PR", check)
	}
	for _, step := range deploy.Steps[1:] {
		if step.Run != "" || step.If != "steps.pr.outputs.deploy == 'true'" || step.With["run-id"] != nil {
			t.Fatalf("deploy step %+v, want no script, the PR check's approval and only this run's artifact", step)
		}
		if strings.HasPrefix(step.Uses, "actions/checkout@") &&
			(step.With["persist-credentials"] != false || step.With["ref"] != nil || step.With["repository"] != nil) {
			t.Fatalf("deploy checkout = %+v, want the default branch without persisted credentials", step)
		}
	}
	if got := stepNamed(t, deploy.Steps, "Deploy preview").With["pr-number"]; got != number {
		t.Fatalf("deployed PR = %v, want %v", got, number)
	}
	if got := stepNamed(t, deploy.Steps, "Comment preview URL on the PR").With["number"]; got != number {
		t.Fatalf("commented PR = %v, want %v", got, number)
	}

	remove := readPreviewWorkflow(t, "../.github/workflows/website-preview-remove.yml")
	// A PR may drop its website changes after its preview was deployed.
	if want := map[string]any{"pull_request_target": map[string]any{"types": []any{"closed"}}}; !reflect.DeepEqual(remove.On, want) {
		t.Fatalf("remove triggers = %v, want %v", remove.On, want)
	}
	removeJob := remove.Jobs["remove-preview"]
	if !reflect.DeepEqual(removeJob.Concurrency, group) {
		t.Fatalf("remove concurrency = %v, want the deploy job's %v", removeJob.Concurrency, group)
	}
	for _, step := range removeJob.Steps[1:] {
		if step.If != "steps.preview.outputs.exists == 'true'" {
			t.Fatalf("remove step %q runs without a preview", step.Name)
		}
	}

	for name, job := range map[string]previewJob{"authorize-preview": authorize, "build-preview": build, "deploy-preview": deploy, "remove-preview": removeJob} {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "${{") {
				t.Fatalf("%s step %q expands an expression in its script", name, step.Name)
			}
		}
	}
}

// pull_request_target ignores the fork PR approval setting, so the authorize
// job decides whether PR code may run at all.
func TestWebsitePreviewAuthorizesBuild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the workflow runs on Linux")
	}
	check := releaseShell(t, websitePreviewBuild, "Check who may build this PR")
	// A GitHub that reports $PERMISSION for sender on test/repo.
	const gh = `#!/bin/bash
set -euo pipefail
[ "$*" = "api repos/test/repo/collaborators/sender/permission --jq .permission" ] || { echo "unexpected gh arguments: $*" >&2; exit 1; }
[ "$PERMISSION" != error ] || { echo "gh: Server Error (HTTP 502)" >&2; exit 1; }
echo "$PERMISSION"
`
	const approved = "approved=true\n"
	const needsWrite = "needs the preview label from someone with write access"
	for _, tc := range []struct {
		name, action, headRepo, author, permission, want, wantOut string
		fail                                                      bool
	}{
		// Only users with write access can push to this repository.
		{name: "same-repo", action: "synchronize", headRepo: "test/repo", want: approved},
		{name: "same-repo-label-by-triage", action: "labeled", headRepo: "test/repo", permission: "read", wantOut: "sender has read access"},
		{name: "fork-push-by-writer", action: "synchronize", headRepo: "author/fork", permission: "write", want: approved},
		{name: "fork-opened-by-admin", action: "opened", headRepo: "author/fork", permission: "admin", want: approved},
		{name: "fork-push-by-reader", action: "synchronize", headRepo: "author/fork", permission: "read", wantOut: needsWrite},
		// A writer reopening someone else's fork PR approves nothing; only the
		// label does.
		{name: "fork-reopened-by-other-writer", action: "reopened", headRepo: "author/fork", author: "author", permission: "write", wantOut: "sender triggered this event for author's PR"},
		{name: "label-by-writer-on-other-pr", action: "labeled", headRepo: "author/fork", author: "author", permission: "write", want: approved},
		{name: "label-by-triage", action: "labeled", headRepo: "author/fork", permission: "read", wantOut: "sender has read access"},
		{name: "api-error", action: "synchronize", headRepo: "author/fork", permission: "error", wantOut: "HTTP 502", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, output, err := runPreviewStep(t, check, gh,
				"REPO=test/repo",
				"ACTION="+tc.action,
				"HEAD_REPO="+tc.headRepo,
				"SENDER=sender",
				"AUTHOR="+cmp.Or(tc.author, "sender"),
				"PERMISSION="+tc.permission,
			)
			checkStepResult(t, out, output, err, tc.want, tc.wantOut, tc.fail)
		})
	}
}

// The event is a snapshot, so the deploy job checks that PR again before the
// token step. The check only validates the event's PR; it never picks one.
func TestWebsitePreviewChecksPRBeforeDeploy(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the workflow runs on Linux")
	}
	const head = "1111111111111111111111111111111111111111"
	const moved = "2222222222222222222222222222222222222222"
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed")
	}
	check := releaseShell(t, websitePreviewDeploy, "Check the PR is open at the built commit")
	// A GitHub that serves PR 42 of test/repo as $PULL. The step's --jq runs
	// as is.
	const gh = `#!/bin/bash
set -euo pipefail
[ "$1 $2 $3" = "api repos/test/repo/pulls/42 --jq" ] && [ "$#" = 4 ] || { echo "unexpected gh arguments: $*" >&2; exit 1; }
[ -n "$PULL" ] || { echo "gh: Server Error (HTTP 502)" >&2; exit 1; }
jq -r "$4" <<<"$PULL"
`
	pull := func(state, sha string) string {
		return `{"number":42,"state":"` + state + `","head":{"sha":"` + sha + `"}}`
	}
	for _, tc := range []struct {
		name, pull, want, wantOut string
		fail                      bool
	}{
		{name: "open", pull: pull("open", head), want: "deploy=true\n"},
		{name: "closed", pull: pull("closed", head), wantOut: "is closed at " + head},
		{name: "moved", pull: pull("open", moved), wantOut: "is open at " + moved},
		{name: "api-error", wantOut: "HTTP 502", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, output, err := runPreviewStep(t, check, gh,
				"REPO=test/repo",
				"NUMBER=42",
				"HEAD_SHA="+head,
				"PULL="+tc.pull,
			)
			checkStepResult(t, out, output, err, tc.want, tc.wantOut, tc.fail)
		})
	}
}

// Every closed PR runs the removal. It must not fail or comment for a PR that
// never had a preview, skip one it cannot look up, or delete the preview of a
// PR that was reopened while the removal waited in the queue.
func TestWebsitePreviewRemovesOnlyClosedPreview(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the workflow runs on Linux")
	}
	check := releaseShell(t, "../.github/workflows/website-preview-remove.yml", "Check for a preview")
	// A GitHub that reports PR 42 as $STATE (with the workflow token) and
	// answers the previews contents request with $RESPONSE (with the deploy
	// token).
	const gh = `#!/bin/bash
set -euo pipefail
case "$*" in
  "api repos/test/repo/pulls/42 --jq .state")
    [ "$GH_TOKEN" = workflow-token ] || { echo "PR read with $GH_TOKEN" >&2; exit 1; }
    [ "$STATE" != error ] || { echo "gh: Server Error (HTTP 502)" >&2; exit 1; }
    echo "$STATE" ;;
  "api repos/eclipse-enclave/enclave-website-previews/contents/pr-previews/pr-42?ref=previews --silent")
    [ "$GH_TOKEN" = deploy-token ] || { echo "preview read with $GH_TOKEN" >&2; exit 1; }
    [ "$RESPONSE" = ok ] || { echo "$RESPONSE" >&2; exit 1; } ;;
  *) echo "unexpected gh arguments: $*" >&2; exit 1 ;;
esac
`
	const notFound = "gh: Not Found (HTTP 404)"
	for _, tc := range []struct {
		name, state, response, want, wantOut string
		fail                                 bool
	}{
		{name: "exists", state: "closed", response: "ok", want: "exists=true\n"},
		{name: "none", state: "closed", response: notFound, wantOut: "PR #42 has no preview"},
		{name: "reopened", state: "open", response: "ok", wantOut: "PR #42 is open again"},
		{name: "preview-api-error", state: "closed", response: "gh: Server Error (HTTP 502)", wantOut: "HTTP 502", fail: true},
		{name: "pr-api-error", state: "error", response: "ok", wantOut: "HTTP 502", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, output, err := runPreviewStep(t, check, gh,
				"GH_TOKEN=deploy-token",
				"PR_TOKEN=workflow-token",
				"REPO=test/repo",
				"NUMBER=42",
				"STATE="+tc.state,
				"RESPONSE="+tc.response,
			)
			checkStepResult(t, out, output, err, tc.want, tc.wantOut, tc.fail)
		})
	}
}

// runPreviewStep runs a workflow step's script as the runner would, with gh
// replaced by a fake, and returns its output and what it wrote to
// GITHUB_OUTPUT.
func runPreviewStep(t *testing.T, script, gh string, env ...string) (string, string, error) {
	t.Helper()
	root := t.TempDir()
	writeTestExecutable(t, filepath.Join(root, "bin", "gh"), gh)
	if err := os.WriteFile(filepath.Join(root, "output"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"),
		"GITHUB_OUTPUT="+filepath.Join(root, "output"),
		"GH_TOKEN=unused",
	)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	output, readErr := os.ReadFile(filepath.Join(root, "output"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(out), string(output), err
}

func checkStepResult(t *testing.T, out, output string, err error, want, wantOut string, fail bool) {
	t.Helper()
	if (err != nil) != fail {
		t.Fatalf("step = %v, want failure %v: %s", err, fail, out)
	}
	if !strings.Contains(out, wantOut) {
		t.Fatalf("output = %q, want it to contain %q", out, wantOut)
	}
	if output != want {
		t.Fatalf("GITHUB_OUTPUT = %q, want %q", output, want)
	}
}

func stepNamed(t *testing.T, steps []previewStep, name string) previewStep {
	t.Helper()
	for _, step := range steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("step %q not found", name)
	return previewStep{}
}
