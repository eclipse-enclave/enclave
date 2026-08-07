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

- A supported host: Linux, macOS, or Windows through WSL2.
- A container backend. Enclave uses Docker; an experimental QEMU microVM
  backend exists but currently runs without network restrictions, is x86-64
  only, and is practical only on x86-64 Linux hosts, where KVM can accelerate
  it.
- Git. Enclave runs from inside a git repository, and works with git worktrees when
  you want to isolate parallel sessions.
- Credentials for a supported agent, such as Claude Code, Codex, or OpenCode.

## Install

Prebuilt binaries, a Debian package, and an RPM package are published on the
[rolling release page](https://github.com/eclipse-enclave/enclave/releases/tag/rolling).

:::info
The rolling release is a pre-release built from the current `main` branch, not a
stable versioned release. Its assets are replaced as `main` moves, so expect
behavior to change between downloads.
:::

### Ubuntu 24.04 (x86-64)

Download `enclave_<version>_amd64.deb` and install it with APT so the runtime
dependencies are resolved:

```bash
sudo apt install ./enclave_*_amd64.deb
```

### Fedora Linux (x86-64)

Download `enclave-<version>.x86_64.rpm` and install it with DNF so the runtime
dependencies are resolved:

```bash
sudo dnf install ./enclave-*.x86_64.rpm
```

### Other Linux (x86-64 and arm64)

Download the binary matching your architecture, `enclave-linux-amd64` or
`enclave-linux-arm64`, together with `checksums.txt`. Verify it and put it on
your `PATH`:

```bash
sha256sum --check --ignore-missing checksums.txt
sudo install enclave-linux-amd64 /usr/local/bin/enclave
```

### macOS

Use `enclave-darwin-arm64` on Apple Silicon and `enclave-darwin-amd64` on Intel.
Download it together with `checksums.txt`:

```bash
shasum -a 256 --check --ignore-missing checksums.txt
xattr -d com.apple.quarantine ./enclave-darwin-arm64 2>/dev/null || true
sudo install -d /usr/local/bin
sudo install enclave-darwin-arm64 /usr/local/bin/enclave
```

:::note
The macOS binaries are unsigned and not notarized. A download through the browser
carries a quarantine attribute that makes Gatekeeper refuse to run the binary,
and `install` propagates it, so the `xattr` step comes first. Downloading with
`curl` or `gh release download` avoids the attribute in the first place, which is
why the command tolerates its absence.
:::

### Windows (WSL2)

There is no native Windows build. The supported path is WSL2:

1. Install WSL2 with an Ubuntu 24.04 distribution.
2. Make Docker available inside that distribution, either through Docker
   Desktop's WSL integration or by installing Docker Engine in the distribution
   itself.
3. Follow the Linux instructions above from inside the distribution.

Keep the project in the WSL filesystem (for example under `~/`) rather than on a
Windows drive under `/mnt/c`. Paths on `/mnt/c` cross the interop layer and are
noticeably slower.

### Build from source

Building from source still works and needs the Go toolchain:

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
