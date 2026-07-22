---
sidebar_position: 2
title: Getting Started
---

# Getting Started

This guide walks you through installing Eclipse Enclave and starting your first
sandboxed agent session.

:::info
The docs are still being filled in. The commands and paths below reflect the
current CLI and will get more detail over time.
:::

## Prerequisites

- A container backend. Enclave uses Docker; an experimental QEMU microVM
  backend exists but currently runs without network restrictions.
- Git. Enclave runs from inside a git repository, and works with git worktrees when
  you want to isolate parallel sessions.
- Credentials for a supported agent, such as Claude Code, Codex, or OpenCode.

## Install

{/* TODO: replace with the published install command once the release channel is live. */}

For now, build from source. You will need the Go toolchain.

```bash
git clone https://github.com/eclipse-enclave/enclave
cd enclave
make build
```

## Start your first session

From inside a git repository, launch the default agent in an isolated container:

```bash
enclave
```

Enclave builds the environment, mounts the current folder into a container, and
starts your agent against the branch you have checked out. The agent runs at full
autonomy with no confirmation prompts, and it stays contained.

To keep parallel sessions from stepping on each other, run each one in its own
git worktree. This is plain git, no Enclave-specific setup required:

```bash
git worktree add ../myproject-agent -b agent/experiment
cd ../myproject-agent
enclave
```

See [Run against an isolated worktree](/cli#run-against-an-isolated-worktree) for
more.

### Pick a specific agent

```bash
enclave --tool codex
```

### Resume a previous session

```bash
enclave continue
```

### List active sessions

```bash
enclave ps
```

## What happens under the hood

1. Enclave uses the current folder and its checked-out branch, or an isolated git
   worktree if you started the session from one.
2. It starts a container with that working directory mounted read/write, along
   with your tool config and package caches.
3. With the default Docker backend, a gateway sidecar filters outbound traffic
   against your network allowlist and logs DNS queries and proxied requests so
   you can audit them later.

## Next steps

See the [CLI Commands](/cli) reference for the full set of commands you can run.
From there, you can add custom skills, mount extra directories, and set network
allowlists per tool and per project. Guides for each of those are on the way.
