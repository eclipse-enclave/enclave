# Theia

Runs an Enclave container with `sleep infinity` and auto-launches the host
`theia` desktop IDE attached to it via the devcontainer protocol.

The IDE process lives on the host; the container provides the dev environment
that Theia connects into. Theia's AI features call out through the enclave
gateway, so the same secret-injection and allowlist rules apply.

## Configuration

- **Command**: `sleep infinity`
- **Config directory**: `~/.theia`
- **post_start.open_ide**: `theia`. Triggers the host launcher once the
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

## External API Port

`--theia-api-port <port>` couples two steps that otherwise had to be done by
hand:

1. Publishes `<port>` on the host **loopback** (equivalent to `-p <port>`,
   resolving to `127.0.0.1:<port>-><port>`), so it is reachable from the host
   only (not the LAN).
2. Hands these preferences to Theia at launch so it serves its external API on
   the same port:

   ```json
   "externalApi.delivery": "separatePort",
   "externalApi.port":     <port>,
   "externalApi.hostname": "0.0.0.0"
   ```

`externalApi.hostname` is `0.0.0.0` (the in-container bind) so the service is
reachable across the gateway network namespace via the published loopback port.
Pass `--theia-api-token <token>` to also set `externalApi.token`; when omitted,
no token preference is set. The token reaches the IDE process as a launch
argument, so it is visible via `ps` to other users on the host (it is redacted
from the launch log). This is acceptable for the loopback-only dev flow this
targets; do not rely on it as a secret on a shared multi-user host.

```bash
enclave --tool theia --background --theia-api-port 3333
```

The flag also applies when re-attaching an existing session
(`enclave theia <container> --theia-api-port 3333`) — the preferences are
re-injected, though the host port is only published at the initial container
start. The preferences are injected regardless of yolo mode (they enable an API
surface rather than relaxing tool confirmation).

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (metadata, sandbox behavior, network, credentials) |
| `install.sh` | No-op (Theia installs at attach time) |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `entrypoint.d/setup.sh` | Runtime setup (creates config dir) |
