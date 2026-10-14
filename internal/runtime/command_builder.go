// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"strings"

	"enclave/internal/protectedclient"
)

const agentWrapperTemplate = "if [ -f ~/.bashrc ]; then . ~/.bashrc; fi; exec \"$@\""

type commandBuilder struct {
	*Runtime
}

func newCommandBuilder(r *Runtime) commandBuilder {
	return commandBuilder{Runtime: r}
}

func (b commandBuilder) Build() []string {
	if b.run.Shell {
		if b.run.Admin {
			cmdArgs := b.run.CmdArgs
			if len(cmdArgs) == 0 {
				cmdArgs = []string{"/bin/bash"}
			}
			// bash -c uses the next argument as $0; the rest become $1.. for exec "$@".
			return b.withProtectedGitHub(append([]string{"bash", "-c", "echo 'Admin shell - sudo access enabled' && exec \"$@\"", "bash"}, cmdArgs...))
		}
		if len(b.run.CmdArgs) > 0 {
			return b.withProtectedGitHub(append([]string{"/bin/bash"}, b.run.CmdArgs...))
		}
		return b.withProtectedGitHub([]string{"/bin/bash"})
	}

	agentArgs := strings.Fields(b.profile.Command)
	if b.yoloEnabled && b.profile.YoloFlag != "" {
		agentArgs = append(agentArgs, strings.Fields(b.profile.YoloFlag)...)
	}
	if b.run.MemoryDisabled() {
		agentArgs = append(agentArgs, b.profile.NoMemoryArgs...)
	}
	agentArgs = append(agentArgs, b.run.CmdArgs...)

	// bash -c uses the next argument as $0; the rest become $1.. for exec "$@".
	wrapper := agentWrapperTemplate
	if b.protectedGitHubEnabled() {
		wrapper = "if [ -f ~/.bashrc ]; then . ~/.bashrc; fi\n" + protectedclient.GitHubSetup + "\nexec \"$@\""
	}
	return append([]string{"bash", "-c", wrapper, "bash"}, agentArgs...)
}

func (b commandBuilder) withProtectedGitHub(command []string) []string {
	if !b.protectedGitHubEnabled() {
		return command
	}
	return protectedclient.Wrap(command)
}
