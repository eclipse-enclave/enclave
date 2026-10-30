# Changelog

## 1.0.0 - 2026-10-30

First release of Eclipse Enclave, a sandbox for running agentic coding tools.

### Highlights

- Built-in tools: Claude Code, Codex, Copilot, OpenCode, Pi and Theia. More tools and features
  are in [enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions).
- Runs on Docker (rootful) or Podman (rootless); the `qemu` backend is experimental.
- Network access goes through a gateway with an allowlist and an audit log
  (`enclave network log`).
- Linux and macOS binaries, deb and rpm packages; on Windows, a launcher for WSL2.
- Standalone binaries update themselves with `enclave self-update`, from the `stable` or the
  `rolling` channel.

### Upgrading from a rolling build

- The Mistral Vibe tool (`mistral-vibe`) and the features `debug-tools`, `shell-extras` and
  `playwright` (now `playwright-mcp`) moved to
  [enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions). A configuration
  that still names one of them fails with "unknown tool" or "unknown features"; install it from
  enclave-extensions.
  [#183](https://github.com/eclipse-enclave/enclave/pull/183)
- `devtools` no longer contains Go, Enclave's Go linters or `rpm`, and the image no longer
  contains `debhelper`, `fakeroot` or `lintian`. Go is in the new `golang-dev` feature, off by
  default; the rest is in the enclave-extensions features `enclave-dev`, `rpm-dev` and `deb-dev`.
  [#183](https://github.com/eclipse-enclave/enclave/pull/183)
- `node-dev` no longer installs `eslint`, `prettier`, `ts-node`, `nodemon` or `@types/node`
  globally; it keeps `typescript`, `yarn` and `pnpm`. Run project-local tools with `npx`.
  [#183](https://github.com/eclipse-enclave/enclave/pull/183)
- `native-build-deps` is now called `theia-dev`, and `--playwright-mcp` was removed. A config
  file that still sets `playwright_mcp` is ignored, so Playwright MCP is gone until you install
  `playwright-mcp` from enclave-extensions and enable it with `--features playwright-mcp`.
  [#183](https://github.com/eclipse-enclave/enclave/pull/183)
- `enclave status` and `--session-monitor` were removed, together with the tmux session inside
  the container. A config file that still sets `session_monitor` is ignored.
  [#190](https://github.com/eclipse-enclave/enclave/pull/190)
