---
slug: /
sidebar_position: 1
title: Introduction
---

import useBaseUrl from '@docusaurus/useBaseUrl';
import ThemedImage from '@theme/ThemedImage';

# Eclipse Enclave

Eclipse Enclave gives AI coding agents full autonomy inside isolated containers.
Each session gets its own filesystem and, with the default Docker backend, a
network gateway that only lets through the domains you allow. The current
working directory where you invoked `enclave` is mounted into the container;
set up a dedicated git worktree per parallel session and their changes stay off
each other.

## Autonomy on your host is risky

AI coding agents are most productive with full autonomy: running commands and
ad-hoc scripts, editing files, all without asking permission at every step. That
same freedom lets an unrestricted agent on your host break system files, leak
secrets, act on a prompt injection, or let parallel sessions interfere with each
other.

## Eclipse Enclave: a sandbox for every session

Eclipse Enclave puts each agent session in its own container — behind a
filtering gateway with the default Docker backend — so you can hand an agent
full autonomy without handing it your host.

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

## Next steps

Head to [Getting Started](/getting-started) to install Enclave and start your
first sandboxed session.
