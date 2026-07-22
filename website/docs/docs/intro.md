---
slug: /
sidebar_position: 1
title: Introduction
---

import useBaseUrl from '@docusaurus/useBaseUrl';
import ThemedImage from '@theme/ThemedImage';

# Eclipse Enclave

Eclipse Enclave runs Claude, Codex, Gemini, and other AI coding agents at full
autonomy. Each one runs in its own isolated container, with your files and
network access under your control.

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
    <p>Run Claude Code, Codex, Gemini, Theia AI, OpenCode, and more from one CLI, each with the same isolation.</p>
  </div>
  <div className="enclave-box">
    <div className="box-title">Audit-ready by design</div>
    <p>The gateway logs DNS queries and proxied requests, giving you an evidence trail for security and compliance reviews.</p>
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
    alt="Eclipse Enclave architecture: the developer passes a working directory, extra mounts, and secrets into an isolated Docker container whose outbound traffic passes through a filtering gateway"
    sources={{
      light: useBaseUrl('/img/architecture.svg'),
      dark: useBaseUrl('/img/architecture-dark.svg'),
    }}
  />
</figure>

## Coming soon: HomeShell

Today Enclave is a CLI. HomeShell is a graphical companion on the way: group
your work into projects, split each into workstreams, and run agent sessions
across them. Start, watch, and switch between sandboxed sessions with a live
overview of every agent's status.

<figure className="enclave-diagram">
  <img
    src={useBaseUrl('/img/homeshell.png')}
    alt="HomeShell: a graphical dashboard listing Enclave projects, workstreams, and running agent sessions"
  />
  <figcaption className="enclave-caption">HomeShell is in active development and not yet released.</figcaption>
</figure>

## Next steps

Head to [Getting Started](/getting-started) to install Enclave and start your
first sandboxed session.
