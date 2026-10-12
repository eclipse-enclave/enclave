# claude-agent-sdk

Claude Agent SDK (`@anthropic-ai/claude-agent-sdk`). Required by Theia's Claude
Code provider, which loads the SDK from the Theia backend inside the container.
Opt-in (disabled by default).

**Priority**: 60

## Usage

```bash
enclave --features +claude-agent-sdk --tool theia
```

To enable it permanently, add `"features": ["+claude-agent-sdk"]` to
`~/.config/enclave/config.json` or a project's
`~/.config/enclave/projects/<hash>/config.json`.

## Installation

Installs `@anthropic-ai/claude-agent-sdk` from npm with the private agent Node
runtime into `~/.local/lib/node_modules/`, independent of `node-dev` and the
project's Node version. The package bundles a native `claude` binary (about
280 MB). `enclave --rebuild` picks up a newer release.

`CLAUDE_AGENT_SDK_PATH` points Theia at the install. Theia versions that do not
read it only search `npm root -g`; set the
`ai-features.claudeCode.executablePath` preference to
`/home/agent/.local/lib/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs`
instead.

## Auth

Claude Code needs an API key; its interactive `/login` is not available when
Theia runs it. Either export `ANTHROPIC_API_KEY` on the host, which the Theia
profiles declare, so the container sees a placeholder and the gateway injects
the real key for `api.anthropic.com`; or set `ai-features.claudeCode.apiKey`
in Theia's settings, which Theia stores in clear text.
