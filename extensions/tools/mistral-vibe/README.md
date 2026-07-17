# Mistral Vibe Extension

[Mistral Vibe](https://github.com/mistralai/mistral-vibe) is Mistral's open-source CLI coding assistant.

## Opt-in

Mistral Vibe builds its own per-tool image. Select it per run with
`--tool mistral-vibe`, or make it the default in `~/.config/enclave/config.json`:
```json
{
    "tool": "mistral-vibe"
}
```

## Authentication

Set `MISTRAL_API_KEY` as an environment variable, or let Vibe prompt you on first run (saved to `~/.vibe/.env`).

## Usage

Run `enclave --tool mistral-vibe`.

## Network

Requires access to `api.mistral.ai` and `codestral.mistral.ai` (provided by the `mistral.conf` allowlist fragment).
