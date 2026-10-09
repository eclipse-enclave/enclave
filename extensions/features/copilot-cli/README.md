# copilot-cli

GitHub Copilot CLI (`copilot`). Also required by Theia's Copilot provider,
which runs the CLI from the Theia backend inside the container. Opt-in
(disabled by default).

**Priority**: 60

## Usage

```bash
enclave --features +copilot-cli --tool theia
```

To enable it permanently, add `"features": ["+copilot-cli"]` to
`~/.config/enclave/config.json` or a project's
`~/.config/enclave/projects/<hash>/config.json`.

## Installation

Installs `@github/copilot` from npm with the private agent Node runtime.
`enclave --rebuild` picks up a newer release.

## Auth

No token is needed. Theia's Copilot provider has its own sign-in, which runs
`copilot login --device-code` in the container. Theia removes
`COPILOT_GITHUB_TOKEN`, `GH_TOKEN` and `GITHUB_TOKEN` from the CLI's
environment, so tokens from the host do not reach it. The sign-in and Copilot
API hosts are covered by the GitHub allowlist fragment.

To use `copilot` directly in a terminal, run `copilot login` there. Without a
system keychain the CLI stores the token in plain text under `~/.copilot/`,
which is discarded with the container.
