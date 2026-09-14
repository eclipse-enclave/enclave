# Build Scripts (Docker Weaving)

These scripts are the build-composition layer for `Dockerfile`.

## Goals

- Keep `Dockerfile` structural and readable.
- Keep feature/tool composition logic in lintable shell files.
- Use explicit script contracts (env inputs + stable output paths).

## Script Contracts

All scripts run with `bash` and fail on script/config errors (`set -euo pipefail`).

Shared defaults (from `lib/common.sh`):

- `ENCLAVE_EXTENSIONS_ROOT` (default: `/opt/enclave/extensions`)
- `ENCLAVE_FEATURES_DIR` (default: `/opt/enclave/extensions/features`)
- `ENCLAVE_TOOLS_DIR` (default: `/opt/enclave/extensions/tools`)
- `ENCLAVE_AGENT_NODE_DIR` (default: `/opt/enclave/node`)
- `ENCLAVE_BUILD_SCRIPTS_DIR` (default: `/opt/enclave/build-scripts`)
- `ENCLAVE_TEMPLATES_DIR` (default: `/usr/local/share/enclave/templates`)
- `ENCLAVE_INSTALLED_TOOLS_FILE` (default: `/tmp/installed-tools.txt`)

Build-time selectors:

- `FEATURES`: `default` (default-enabled features), `all` (every feature), whitespace-separated feature list, or empty for none.
- `AGENT_TOOLS`: `all` (default-included tools), whitespace-separated tool list, or empty for none.
- `ENCLAVE_FEATURE_PHASE`: `root` or `user` (for `run-feature-installs.sh`).
- `ENCLAVE_FEATURE_INSTALL_STRICT`: `1` to fail on feature installer errors, default `0` (warn and continue).

Network settings (from `lib/common.sh`, forwarded by the CLI from the host environment as build args):

- `ENCLAVE_NET_RETRIES` (default: `5`): attempts per download or retried command.
- `ENCLAVE_NET_RETRY_DELAY_SECONDS` (default: `5`): base delay, multiplied by the attempt number.
- `ENCLAVE_NET_CONNECT_TIMEOUT_SECONDS` (default: `20`): `curl --connect-timeout`.
- `ENCLAVE_NET_STALL_TIMEOUT_SECONDS` (default: `60`): abort a transfer below the speed floor for this long.
- `ENCLAVE_NET_STALL_SPEED_BYTES` (default: `1024`): the speed floor in bytes per second.
- `ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS` (default: `30`, `0` disables; the CLI passes `5` for `--progress verbose`): interval of the download heartbeat, which reports bytes received, percentage, rate, and time left as whole lines because RUN-step output is line-oriented.
- `ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS` (default: `1800`, `0` disables): `timeout(1)` around each attempt of a retried executable.

## Network Helpers

Downloads made by the build scripts and by extension `install.sh` files go
through one of two `lib/common.sh` functions so timeouts, stall detection, and
retries are applied in one place. The Dockerfile's own `system` stage runs
before these scripts exist in the image and carries equivalent `curl` flags
inline; its `apt-get` calls are not retried.

- `enclave_curl [curl args...]`: `curl --fail --silent --show-error` with connect and stall timeouts on the transfer, wrapped in `enclave_retry`. Retries of a `-o` download resume the partial file with `--continue-at` and fall back to a fresh download when the server rejects ranges. A heartbeat on stderr reports bytes received, percentage (from the response headers), rate, and time left while a download runs. Without `-o`/`-O` the body is buffered per attempt and written to stdout only on success, so `$(enclave_curl <url>)` never sees a partial body.
- `enclave_retry <label> -- <cmd> [args...]`: runs the command, retrying with a growing delay while its stderr matches a transient network error (name resolution, timeouts, resets, stalled transfers, 5xx, apt `Hash Sum mismatch`) or the attempt exceeds the per-attempt wall-clock timeout, which is the only timeout it applies. Anything else, such as a 404 or a failed `sha256sum --check`, fails immediately. stdout streams through; stderr is replayed after each attempt.
- `enclave_apt_get <apt-get args...>`: `apt-get` under `enclave_retry`, reading apt's `APT::Status-Fd` channel to report the overall download percentage on the same interval as the curl heartbeat. Use it for `update` and `install` in root-phase scripts.
- `enclave_is_transient_network_error <file>`: the classifier both use.

`run-feature-installs.sh` and `enclave-install-tool` export these into the
extension `install.sh` processes they start, so extension scripts call them
directly. To run an extension script standalone (for example in CI), source
`lib/common.sh` first; `ENCLAVE_BUILD_SCRIPTS_DIR` points at this directory
when it is not at `/opt/enclave/build-scripts`.

Download upstream installer scripts to a file with `enclave_curl -o` and run the
file, rather than piping `curl` into `bash`: a retried download is safe, a
retried half-run installer is not. The installer's own internal downloads are
outside the helpers' reach.

## Scripts

- `install-agent-node-runtime.sh`: validates private Node runtime and writes npm/npx wrappers.
- `install-feature-apt-packages.sh`: selects enabled features and installs aggregated `aptPackages`.
- `run-feature-installs.sh`: runs enabled feature `install.sh` scripts by phase and priority.
- `install-tool-templates.sh`: aggregates `extensions/tools/*/templates/*` into `/usr/local/share/enclave/templates/`.
- `install-agent-helper-bins.sh`: installs helper binaries into the agent user's local bin.
- `bin/enclave-agent-npm-install`: low-level npm install helper using the private agent Node runtime. Leading `-`-prefixed arguments are forwarded to `npm install`, separated from the package specs by `--`. Fails if a package declares a bin that did not land, which is how a missing lifecycle script surfaces.
- `bin/enclave-install-npm-tool`: shared npm-tool installer wrapper used by simple Node-based tool installers. Takes `[npm-flag ...] <package> <binary> [label]` and forwards the flags to `enclave-agent-npm-install`. Flags must be self-contained (`--flag` or `--flag=value`); one that takes a separate value would consume the package argument.
- `bin/enclave-install-tool`: shared tool installer entrypoint used by generated Docker stages.

## Validation

Scripts are validated by repository linting (`make lint`), which runs `shellcheck` across `*.sh` files.
