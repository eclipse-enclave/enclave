# Gemini CLI

Google Gemini CLI is Google's terminal-based AI coding agent.

## Configuration

- **Command**: `gemini`
- **YOLO flag**: `--approval-mode=yolo`
- **Config directory**: `~/.gemini`
- **Settings file**: `~/.gemini/settings.json`

## API Keys

| Variable | Purpose |
|----------|---------|
| `GEMINI_API_KEY` | Google Gemini API access |

## Auth Files

- `credentials.json`

## Memory

Gemini's `/memory add` appends to `~/.gemini/GEMINI.md`, which Gemini loads as
global context automatically. That file is bind-mounted from the per-project
host location `~/.local/state/enclave/projects/<hash>/gemini/memory/GEMINI.md`, so memory
is scoped per project and stored outside the working directory (never
committed). Pass `--no-memory` to disable the mount. Ephemeral
(`--ephemeral`) sessions skip the host mount, so memory written during them
is discarded with the session's config store.

Reliability: the mount is a single-file bind mount. Upstream `/memory add`
(`performAddMemoryEntry` in `packages/core/src/tools/memoryTool.ts`) rewrites
`GEMINI.md` in place via `fs.readFile` + `fs.writeFile`, not temp-file +
atomic rename, so host-side updates keep flowing through the bind mount for the
life of the session. Should upstream ever switch to a temp-file + rename
strategy, the write would fail loudly rather than silently lose memory:
`rename(2)` onto a bind-mount point returns `EBUSY` on Linux. (Silent inode
detachment only affects symlinks, which are not used here.)

## Network Access

Allowlisted domains include Google, GitHub, and common package registries
(npm, PyPI, Go, CDNs, TLS/OCSP).

## Settings

The default settings template disables telemetry. In YOLO mode, the runtime
also pre-trusts the current workspace in `~/.gemini/trustedFolders.json` to
skip Gemini's folder-trust prompt inside the container.

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (metadata, sandbox behavior, network, credentials) |
| `install.sh` | Installs Gemini via private agent npm (`enclave-agent-npm-install @google/gemini-cli`) |
| `check-update.sh` | Returns the latest npm version for automatic update probes |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `templates/settings.json` | Default settings template |
| `entrypoint.d/setup.sh` | Runtime setup (creates config dir, sets env vars, pre-trusts workspace in YOLO mode) |
