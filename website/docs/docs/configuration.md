---
sidebar_position: 4
title: Configuration
---

# Configuration

Enclave works without any configuration: `enclave` starts the `claude` agent at
full autonomy, in a container, behind a restricted network. You configure it
when you want something else — a different agent, one more allowed domain, an
extra directory mounted in.

A setting can come from three places:

| Layer | Where | Use it for |
| --- | --- | --- |
| CLI flag | `enclave --tool codex` | a single run |
| Project config | `~/.config/enclave/projects/<hash>/config.json` | one repository |
| Global config | `~/.config/enclave/config.json` | your defaults everywhere |

Both files are plain JSON. Every key is optional, and every key has a matching
CLI flag:

```json
{
  "tool": "codex",
  "features": ["+playwright"],
  "add_readonly_dirs": ["~/reference/specs"]
}
```

:::note macOS
The config root is `~/Library/Application Support/org.eclipse.enclave/config/`
instead of `~/.config/enclave/`. Substitute it in every path on this page.
:::

## Find your config files

Project config is keyed by a hash of the project directory, so you never have to
write that path yourself. `enclave config` prints both files, whether or not they
exist yet:

```bash
enclave config
```

```text
Global Config: /home/you/.config/enclave/config.json (missing)
Project Config: /home/you/.config/enclave/projects/1a2b3c4d5e6f/config.json (missing)

Config sources (highest wins): cli > tool_override > project > global > default
```

Create either file yourself — `mkdir -p` its directory, start with `{}` — and
Enclave picks it up on the next run. JSON comments are not supported, so keep
the file strict JSON.

Two things follow from the layout:

- Project config lives **outside** the worktree, keyed by hash. A repository
  cannot carry its own sandbox policy, and an agent working in it cannot rewrite
  the rules it runs under.
- **Each git worktree is its own project.** A worktree at `../myproject-agent`
  gets its own hash, its own project config, and its own persistent state.

## Precedence

Highest wins:

1. CLI flags
2. Per-tool overrides (`tool_overrides.<tool>`, see below)
3. Project config
4. Global config
5. Built-in defaults

When a value is not what you expect, ask Enclave where it came from:

```bash
enclave config --view source     # annotate every value with its origin
enclave config --view effective  # just the resolved values
enclave config --view diff       # only values that something overrode
enclave config --json            # the same data, machine-readable
```

## The options you will actually use

### Agent and session

| Key | Flag | What it does |
| --- | --- | --- |
| `tool` | `--tool <name>` | Which agent runs. Defaults to `claude`; `enclave tools` lists what is installed. |
| `yolo` | `--no-yolo` | Full autonomy, on by default. Turn it off to make the agent ask for confirmation again. |
| `features` | `--features <list>` | Extra tooling baked into the image (see [Features](#features)). |
| `session_monitor` | `--session-monitor` | Run the agent under a managed tmux session so `enclave status` can snapshot what it is doing. |
| `ports` | `-p 3000` | Publish a container port to the host — reach the agent's dev server from your browser. |
| `bridge_ports` | `--bridge-port 9800` | The other direction: make a host service reachable at `localhost:9800` inside the container. |

:::note
On Linux, a bridged host service has to listen on the Docker bridge IP rather
than `127.0.0.1`, and the host firewall has to let the bridge network through.
See [Bridging host ports](https://github.com/eclipse-enclave/enclave/blob/main/docs/networking.md#bridging-host-ports).
:::

### Files

| Key | Flag | What it does |
| --- | --- | --- |
| `add_dirs` | `--add-dir <path>` | Mount another host directory, writable, at the same path inside the container. |
| `add_readonly_dirs` | `--add-readonly-dir <path>` | The same, read-only. Good for reference material you do not want touched. |
| `project_mount` | `--project-mount readonly` | Mount the project read-only: the agent can read and analyze, only you can write. |

### Network

| Key | Flag | What it does |
| --- | --- | --- |
| `allow_domains` | `--allow-domain <domain>` | Add domains to the gateway allowlist. Bare DNS names only, no scheme or path. |
| `allow_all_network` | `--allow-all-network` | Turn network filtering off entirely for the session. |
| `network_log` | `--network-log requests` | Log request-level audit events instead of coarse pass/deny. |

### Auth and secrets

| Key | Flag | What it does |
| --- | --- | --- |
| `auth_name` | `--auth-name <slug>` | Keep several logins per agent (`personal`, `api`, …) and pick one per run. |
| `auth_scope` | `--auth-scope project` | Isolate credentials per project instead of sharing them per agent. |
| `pass_env` | `--pass-env KEY1,KEY2` | Forward specific host environment variables in. Nothing else leaks in. |
| `secrets_scope` | `--secrets-scope global` | Which layers of the secrets files are read. |

The full key list, including build and persistence options, is in
[docs/configuration.md](https://github.com/eclipse-enclave/enclave/blob/main/docs/configuration.md).

## What project config cannot do

Project config is deliberately weaker than global config: settings that would
widen the sandbox are ignored there, with a warning naming the file. Set them in
global config or pass them on the command line instead.

| Ignored in project config | Why |
| --- | --- |
| `tool` | Choosing the agent stays a user decision — pass `--tool` or set it globally. |
| `yolo` | A repository cannot decide how much autonomy its agent gets. |
| `allow_all_network`, `allow_domains` | A repository cannot widen its own network allowlist. |
| `pass_env` | A repository cannot ask for your host environment variables. |
| `host_config`, `tool_overrides.<tool>.host_config_paths` | A repository cannot widen host-config passthrough. |
| `add_dirs`, `add_readonly_dirs` outside the project | Project scope can only mount subdirectories of the project itself. |
| `base_image`, `bridge_ports` | Base image and host-port bridging stay host-side decisions. |
| `project_mount: "writable"` | Project scope can tighten a stricter global default, never loosen it. |

## Per-tool overrides

`tool_overrides.<tool>` applies to one agent only, and wins over the surrounding
project and global values:

```json
{
  "tool": "claude",
  "allow_domains": ["api.deepseek.com"],
  "tool_overrides": {
    "codex": {
      "auth_name": "personal",
      "features": ["+python-dev"]
    }
  }
}
```

Overrides take the same keys as the surrounding file, except `tool` itself and a
nested `tool_overrides`.

## Features

Features are optional tool stacks compiled into the image. `devtools`,
`github-cli`, `node-dev`, and `python-dev` are on by default; `playwright`,
`debug-tools`, `gitlab-cli`, and `shell-extras` are opt-in.

Use `+` and `-` to adjust the inherited set instead of replacing it:

```json
{
  "features": ["+playwright", "-python-dev"]
}
```

A list without prefixes replaces the set entirely, and `[]` means none. Check the
result before you build — `enclave features` marks every feature `✓` or `✗` and
names the config file that switched it off:

```bash
enclave features
```

The same syntax works on the command line: `--features +playwright`, or the
keywords `--features default|all|none`.

## Reuse the agent config you already have

By default the container starts from Enclave's own agent config, not your host
one. Two ways to change that:

**Pass reviewed paths through from the host.** `host_config: "passthrough"`
copies a per-tool allow-list of files out of your host config directory — for
Claude that is `agents/`, `commands/`, `settings.json`, and `skills/` under
`~/.claude`. Auth files, OAuth JSON, and session/history state are blocked even
if you add them to the list. Narrow or widen the list per tool:

```json
{
  "host_config": "passthrough",
  "tool_overrides": {
    "claude": {
      "host_config_paths": ["default", "-skills/"]
    }
  }
}
```

**Or keep a separate, container-only config.** Files under
`~/.config/enclave/tools/<tool>/` mirror the agent's native config layout and are
overlaid at session start — `~/.config/enclave/tools/claude/settings.json`
becomes the container's `~/.claude/settings.json`. Per project, use
`~/.config/enclave/projects/<hash>/<tool>/config/`. Shared skills go in
`~/.config/enclave/skills/<skill>/` and reach every skill-capable agent.

Both mechanisms compose, and JSON/TOML *patches* can merge into a file instead of
replacing it. The precedence order, the patch merge semantics, and the full
passthrough safety rules are documented in
[docs/configuration.md](https://github.com/eclipse-enclave/enclave/blob/main/docs/configuration.md).

## Going further

- [Configuration reference](https://github.com/eclipse-enclave/enclave/blob/main/docs/configuration.md) — every key, patches, skills, precedence details
- [CLI reference](https://github.com/eclipse-enclave/enclave/blob/main/docs/cli-reference.md) — every command and flag
- [Networking](https://github.com/eclipse-enclave/enclave/blob/main/docs/networking.md) — allowlists, the gateway, port forwarding
- [Authentication and secrets](https://github.com/eclipse-enclave/enclave/blob/main/docs/auth.md) — logins, secrets layers, API keys
