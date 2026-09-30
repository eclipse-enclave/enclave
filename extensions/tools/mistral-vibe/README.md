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

Export `MISTRAL_API_KEY` on the host or put it in an Enclave secrets file (see
[Authentication](../../../docs/auth.md#secrets)). With the network gateway, the
container only sees a placeholder; the gateway injects the real key into
requests to `api.mistral.ai`, `console.mistral.ai` (account lookup), and
`chat.mistral.ai` (organization-managed config).

Without a key, Vibe runs its onboarding and saves the key to `~/.vibe/.env`,
which Enclave keeps in the shared per-tool auth store so other projects reuse
it. `--reset-auth` clears it.

## Workspace Trust

In yolo mode (the default), Enclave launches Vibe with `--trust`, which trusts
the project directory for that invocation only and skips the folder-trust
prompt.

## Usage

Run `enclave --tool mistral-vibe`.

## Network

Requires access to `api.mistral.ai` (provided by the `mistral.conf` allowlist fragment), `console.mistral.ai`, and `chat.mistral.ai`. All three are allowed through the credential release hosts in `spec.yaml`.
