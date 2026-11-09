---
slug: /
sidebar_position: 1
title: Introduction
---

import useBaseUrl from '@docusaurus/useBaseUrl';
import ThemedImage from '@theme/ThemedImage';

# Eclipse Enclave

Eclipse Enclave runs Claude Code, Codex, Copilot, OpenCode, Pi, Theia, and other
AI coding agents at full autonomy. Each one runs in its own isolated container,
with your files and network access under your control.

You get an agent that acts without asking for permission at every step, without
handing it the run of your host. Run it from any git repository:

```bash
enclave
```

Enclave builds the sandbox, mounts your current checkout, and starts the agent
against the branch you have checked out. See [Getting Started](/getting-started)
to install and dig in.

## The freedom that makes agents useful makes them dangerous

An agent is most productive when it can act without asking: running commands and
ad-hoc scripts, editing files, all without a confirmation prompt at every step.
But that same freedom lets an unrestricted agent on your host delete the wrong
files, leak secrets, be hijacked by a prompt injection, or let parallel sessions
interfere with each other.

## Isolation that stays out of the agent's way

Eclipse Enclave puts each agent session in its own container, behind a filtering
gateway with the default Docker backend, so you can hand an agent full autonomy
without handing it your host.

<div className="enclave-cards">
  <div className="enclave-box">
    <div className="box-title">OS-level containerization</div>
    <p>Each session runs in a container with its own filesystem, process tree, and network stack. Only what you mount in — typically the project worktree — is shared with the host.</p>
  </div>
  <div className="enclave-box">
    <div className="box-title">Git worktree isolation</div>
    <p>Run each session in its own git worktree so parallel agents cannot interfere with each other's changes. You review and integrate on your terms.</p>
  </div>
  <div className="enclave-box">
    <div className="box-title">Network policy enforcement</div>
    <p>With the default Docker backend, a sidecar gateway filters DNS and proxies outbound traffic, so an agent only reaches allowlisted domains. Everything else is blocked and logged.</p>
  </div>
  <div className="enclave-box">
    <div className="box-title">Works with your agents</div>
    <p>Run Claude Code, Codex, Theia AI, OpenCode, and more from one CLI, each with the same isolation.</p>
  </div>
  <div className="enclave-box">
    <div className="box-title">Audit-ready by design</div>
    <p>The gateway logs every connection it proxies and every lookup it blocks, giving you an evidence trail for security and compliance reviews.</p>
  </div>
  <div className="enclave-box">
    <div className="box-title">Session lifecycle management</div>
    <p>Create, pause, resume, and inspect sessions at any time. Auth, config, and history persist across restarts.</p>
  </div>
</div>

The developer passes the working directory (a branch checkout or a dedicated git
worktree), any extra mounts, and secrets into the container. Secrets configured
for HTTP release are masked from the agent: the gateway injects them only into
requests to matching allowlisted hosts, so their values stay hidden. Other
credentials are exposed inside the container as mounted files or environment
variables. With the default Docker backend, all outbound traffic passes through
the gateway; the experimental QEMU microVM backend currently runs without
network restrictions.

<figure className="enclave-diagram">
  <ThemedImage
    alt="Anatomy of an Enclave session: the developer, the project worktree, extra mounts, secrets, and a persistent store feed an ephemeral sandbox running the AI coding agent; all sandbox traffic leaves through a filtering gateway that allows or blocks it"
    sources={{
      light: useBaseUrl('/img/architecture.svg'),
      dark: useBaseUrl('/img/architecture-dark.svg'),
    }}
  />
</figure>

The numbers mark the four controls Enclave puts around an agent:

1. See: only the mounted worktree, extra mounts, and secrets you pass in.
   Declared credentials arrive as placeholders.
2. Run: an ephemeral container per session, isolated from the host.
3. Reach: only allowlisted domains, through the gateway, which logs allowed
   and blocked connections.
4. Keep: auth, config, history, and memory persist in a store on the host.

## HomeShell (preview) \{#homeshell}

[HomeShell](https://github.com/eclipse-enclave/homeshell) is a graphical
companion for Enclave: start, watch, and answer agent sessions across your
projects from one web UI or desktop window. Install it from the
[HomeShell README](https://github.com/eclipse-enclave/homeshell#install).

<figure className="enclave-diagram">
  <img
    src={useBaseUrl('/img/homeshell.png')}
    alt="HomeShell: a graphical dashboard listing Enclave projects, workstreams, and running agent sessions"
  />
  <figcaption className="enclave-caption">Builds follow every change to HomeShell; there is no stable release yet.</figcaption>
</figure>

## Next steps

Head to [Getting Started](/getting-started) to install Enclave and start your
first sandboxed session.
