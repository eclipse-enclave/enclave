# playwright

Playwright browsers and MCP server for browser automation and UI testing.
Opt-in (disabled by default).

**Priority**: 75

## Installation

1. Installs `@playwright/mcp` globally via npm (requires Node.js on PATH)
2. Installs Chromium and its system dependencies to `/opt/playwright-browsers`
3. Uses the Playwright version bundled with `@playwright/mcp` to ensure
   browser/server compatibility

## Usage

Pass `--playwright-mcp` to automatically enable this feature and register
the MCP server in Claude Code's local scope:

```bash
enclave --playwright-mcp
```

The entrypoint manages the server lifecycle — adding it when the flag is
active and removing it when it's not, since the local config persists
across sessions in the config store.

**Note:** This feature is only supported with Claude Code. Passing
`--playwright-mcp` with other tools emits a warning and is ignored.

### Avoiding rebuilds

`--playwright-mcp` automatically adds the `playwright` feature to the image.
Since Playwright is opt-in, running without the flag produces a different image
hash and triggers a rebuild. To always include Playwright in the image (so
toggling `--playwright-mcp` only controls the MCP server, not the image), add
it to your features in `~/.config/enclave/config.json`:

```json
{
  "features": ["default", "playwright"]
}
```

## Requirements

Node.js must be available at build time. The agent node runtime
(`/opt/enclave/node`) is automatically added to PATH during feature
installation.
