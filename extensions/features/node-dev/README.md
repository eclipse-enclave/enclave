# node-dev

Node.js development tools including nvm for version management and global npm
packages. Enabled by default.

**Priority**: 70

## Installation

1. Installs [nvm](https://github.com/nvm-sh/nvm) for Node.js version management
2. If the base image already has `node` on `PATH`, skips `nvm install --lts`
   and keeps the base-image Node as the default (`nvm use system`)
3. Otherwise, installs the latest LTS Node.js release via nvm and snapshots it
   for runtime cache seeding
4. Exposes the active Node.js binaries through `~/.local/bin` so agents with a
   sanitized `PATH` can still find `node`, `npm`, and `npx`
5. Attempts to install optional global npm packages using the active
   user-facing npm:

| Package | Purpose |
|---------|---------|
| `typescript` | TypeScript compiler |
| `@types/node` | Node.js type definitions |
| `ts-node` | TypeScript execution engine |
| `eslint` | JavaScript/TypeScript linter |
| `prettier` | Code formatter |
| `nodemon` | File watcher / auto-restart |
| `yarn` | Package manager |
| `pnpm` | Package manager |

## Node version precedence

When nvm is active, the entrypoint selects a Node.js version using:

1. **Project `.nvmrc`** — auto-detected from the project root
2. **nvm default** — the version installed at image build time

On Node base images, the image-provided `node` remains active by default.
nvm is still available for manual version switching (`nvm use <version>`).
If node-dev cannot install or snapshot a usable Node.js runtime, image builds
fail instead of producing a container with `node` missing from agent commands.
