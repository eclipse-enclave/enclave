# Theia

Runs an Enclave container with `sleep infinity` and auto-launches the host
`theia` desktop IDE attached to it via the devcontainer protocol.

The IDE process lives on the host; the container provides the dev environment
that Theia connects into. Theia's AI features call out through the enclave
gateway, so the same secret-injection and allowlist rules apply.

## Usage

```bash
enclave --tool theia        # start the container and open the IDE (one step)
enclave theia <container>   # reattach the IDE to an already-running container
```

Because the container's entrypoint is `sleep infinity` and the IDE launches on
the host, this profile has no interactive foreground mode: `enclave --tool
theia` runs detached automatically, prints the container name, and opens the
IDE. Use `enclave theia <container>` to reattach later (the name may be omitted
when exactly one enclave container is running).

## Configuration

- **Command**: `sleep infinity`
- **Config directory**: `~/.theia`
- **postStart.openIDE**: `theia`. Triggers the host launcher once the
  container is running.

## API Keys

| Variable | Purpose |
|----------|---------|
| `ANTHROPIC_API_KEY` | Anthropic API access for Theia AI features |
| `OPENAI_API_KEY` | OpenAI API access for Theia AI features |
| `GEMINI_API_KEY` | Google Gemini API access for Theia AI features |

Theia's Google provider also reads `GEMINI_API_KEY`, although its preference
description only mentions `GOOGLE_API_KEY`. Set `GEMINI_API_KEY` on the host;
`GOOGLE_API_KEY` is not forwarded into the container.

## Other AI Providers

Configure providers in Theia as described in the
[Theia AI documentation](https://theia-ide.org/docs/user_ai/). Enclave does not
declare hosts or keys for the following providers, so they need extra setup:

- **Custom OpenAI- and Anthropic-compatible endpoints**, for example
  OpenRouter: allow the endpoint's host, per run with
  `enclave --tool theia --allow-domain openrouter.ai` or permanently with
  `allow_domains` in `~/.config/enclave/config.json` (project configs cannot
  widen the allowlist), and set the key on the model entry. `"apiKey": true`
  does not work: inside the container the global OpenAI and Anthropic keys are
  placeholders that the gateway only releases to their own provider's hosts.
- **Ollama** on the host: forward its port with `--bridge-port 11434`. On
  Linux with Docker Engine, Ollama must listen on the Docker bridge IP (set with
  `OLLAMA_HOST`) and the firewall must allow the port; see
  [Linux: host service configuration](../../../docs/networking.md#linux-host-service-configuration).
- **llamafile**: letting Theia start the llamafile inside the container is
  recommended, since it needs no network setup. Put the file in the project
  directory, which is mounted at the same path, and make it executable. A
  llamafile running on the host would need its port bridged like Ollama.
- **Hugging Face** (experimental): allow `huggingface.co`, then set the key in
  Theia's settings or pass `HUGGINGFACE_API_KEY` with `--pass-env`.

## IDE Preferences

Preference overrides passed to Theia via `--session-preference` are merged from
(highest wins):

1. Project: `~/.config/enclave/projects/<hash>/config.json` under `{"theia":{"preferences":{...}}}`
2. Global:  `~/.config/enclave/tools/theia/preferences.json` (flat map, honors `$XDG_CONFIG_HOME`)
3. Built-in default: `ai-features.chat.defaultToolConfirmation=always_allow`

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (metadata, sandbox behavior, network, credentials) |
| `install.sh` | No-op (Theia installs at attach time) |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `entrypoint.d/setup.sh` | Runtime setup (creates config dir) |
