# Theia Next

Runs an Enclave container with `sleep infinity` and auto-launches the host
`theia-next` (preview) desktop IDE attached to it via the devcontainer protocol.

The IDE process lives on the host; the container provides the dev environment
that Theia Next connects into. Theia's AI features call out through the
enclave gateway, so the same secret-injection and allowlist rules apply.

## Usage

```bash
enclave --tool theia-next        # start the container and open the IDE (one step)
enclave theia-next <container>   # reattach the IDE to an already-running container
```

Because the container's entrypoint is `sleep infinity` and the IDE launches on
the host, this profile has no interactive foreground mode: `enclave --tool
theia-next` runs detached automatically, prints the container name, and opens
the IDE. Use `enclave theia-next <container>` to reattach later (the name may be
omitted when exactly one enclave container is running).

## Configuration

- **Command**: `sleep infinity`
- **Config directory**: `~/.theia`
- **postStart.openIDE**: `theia-next`. Triggers the host launcher once the
  container is running.

## API Keys

| Variable | Purpose |
|----------|---------|
| `ANTHROPIC_API_KEY` | Anthropic API access for Theia AI features |

## IDE Preferences

Preference overrides passed to Theia via `--session-preference` are merged from
(highest wins):

1. Project: `~/.config/enclave/projects/<hash>/config.json` under `{"theia":{"preferences":{...}}}`
2. Global:  `~/.config/enclave/tools/theia/preferences.json` (flat map, honors `$XDG_CONFIG_HOME`)
3. Built-in default: `ai-features.chat.defaultToolConfirmation=always_allow`

Note: the preference config is shared with the `theia` tool; both variants
read from the same files.

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (metadata, sandbox behavior, network, credentials) |
| `install.sh` | No-op (Theia installs at attach time) |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `entrypoint.d/setup.sh` | Runtime setup (creates config dir) |
