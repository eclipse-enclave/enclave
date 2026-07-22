---
sidebar_position: 3
title: CLI Commands
---

# CLI commands

Everything in Eclipse Enclave runs through the `enclave` command. You run it from
inside a git repository, and it starts the agent in an isolated container so its
activity stays confined to the container and the worktree you mounted. By default the session works against the branch
currently checked out in that folder. To run several agents in parallel without
their changes colliding, give each one its own git worktree (see
[Run against an isolated worktree](#run-against-an-isolated-worktree)).

:::info
The CLI is still evolving, and more commands and flags are on the way. This page
covers the core commands available today; it will grow as the surface stabilizes.
:::

## Overview

| Command | What it does |
| --- | --- |
| `enclave` | Start a sandboxed session with the default agent in the current repository. |
| `enclave --tool <name>` | Start a session with a specific agent. |
| `enclave continue` | Resume your previous session. |
| `enclave ps` | List the sessions that are currently running. |
| `enclave --background` | Start a session detached from your terminal. |
| `enclave attach <container>` | Attach to a running session (default detach key: `Ctrl-\`). |

## Start a session

Run `enclave` from inside a git repository to launch the default agent:

```bash
enclave
```

Enclave builds the environment, mounts the current folder into a fresh container,
and starts the agent against the branch you have checked out. The agent runs at
full autonomy with no confirmation prompts, and it stays contained: its filesystem
access and outbound network are scoped to the sandbox.

## Run against an isolated worktree

By default the agent works on your current branch in the current folder. To keep
parallel sessions from interfering, or to keep an agent off your working branch,
create a separate git worktree first and start the session there. This is plain
git, no Enclave-specific setup required:

```bash
git worktree add ../myproject-agent -b agent/experiment
cd ../myproject-agent
enclave
```

Each worktree has its own working directory and branch, so agents in different
worktrees never touch each other's changes. When you are done, review the branch
and clean up with `git worktree remove ../myproject-agent`.

## Pick a specific agent

Use `--tool` to choose which agent runs in the session. Enclave supports Claude
Code, Codex, Theia AI, OpenCode, and more.

```bash
enclave --tool codex
```

## Resume a previous session

Pick up where you left off. Auth, config, and history persist across restarts, so
`continue` drops you back into your last session with its state intact.

```bash
enclave continue
```

## List active sessions

See which sessions are running right now, so you can track parallel agents and
choose one to inspect or resume.

```bash
enclave ps
```

The `NAME` column in the output is what you pass to `enclave attach`.

## Run in the background and reattach

Long-running agents do not need to keep your terminal busy. Start a session with
`--background` to detach it from your shell right after boot, then reattach when
you want to check in or interact:

```bash
enclave --background            # start detached (optionally pair with --name)
enclave ps                      # find the running session's container name
enclave attach <container>      # reattach to its TTY
```

Combine `--background` with `--name` when you want a stable, memorable container
name to reattach to:

```bash
enclave --background --name my-task
enclave attach my-task
```

### Detach without stopping the session

Once attached, press `Ctrl-\` (the docker attach default) to detach from the
session while leaving the container and its agent running. You can then reattach
later with `enclave attach <container>`.

To use a different detach key sequence, pass `--detach-keys` when attaching:

```bash
enclave attach my-task --detach-keys "ctrl-p,ctrl-q"
```

Pressing `Ctrl-C` inside the session sends an interrupt to the agent instead of
detaching, so use the detach key when you want to leave the container running.

## Next steps

New to Enclave? Start with [Getting Started](/getting-started) to install it and
run your first session. Deeper guides for custom skills, extra mounts, and
per-project network allowlists are on the way.
