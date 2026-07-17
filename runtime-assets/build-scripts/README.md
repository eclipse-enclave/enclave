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

## Scripts

- `install-agent-node-runtime.sh`: validates private Node runtime and writes npm/npx wrappers.
- `install-feature-apt-packages.sh`: selects enabled features and installs aggregated `aptPackages`.
- `run-feature-installs.sh`: runs enabled feature `install.sh` scripts by phase and priority.
- `install-tool-templates.sh`: aggregates `extensions/tools/*/templates/*` into `/usr/local/share/enclave/templates/`.
- `install-agent-helper-bins.sh`: installs helper binaries into the agent user's local bin.
- `bin/enclave-agent-npm-install`: low-level npm install helper using the private agent Node runtime.
- `bin/enclave-install-npm-tool`: shared npm-tool installer wrapper used by simple Node-based tool installers.
- `bin/enclave-install-tool`: shared tool installer entrypoint used by generated Docker stages.

## Validation

Scripts are validated by repository linting (`make lint`), which runs `shellcheck` across `*.sh` files.
