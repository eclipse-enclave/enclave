<p align="center">
  <img src="docs/assets/appicon.png" alt="Enclave" width="128" height="128">
</p>

<h1 align="center">Enclave</h1>

A Docker-based sandbox for running agentic coding tools — Claude, Codex, OpenCode, and others — in an isolated container while keeping your project files on the host. Network access is restricted to allowlisted domains by default, auth and history persist across sessions, and YOLO mode is on so agents can act without confirmation prompts.

## Requirements

Linux and macOS (with Docker Desktop) are supported natively. On Windows,
Enclave runs inside WSL2: the Linux instructions apply within the WSL
distribution, and `enclave.exe` is a launcher that forwards to it rather than a
native build. See [Windows](docs/windows.md).

Runtime dependencies:

- Rootful Docker (CLI on `PATH`, daemon running) with the buildx plugin; the
  sandbox image build requires BuildKit. Rootless Docker is not supported.
- Optional: `qemu-system-x86_64` and `cpio` for the experimental `qemu` backend,
  which builds an x86-64 guest and is practical only on x86-64 Linux hosts,
  where KVM can accelerate it. On arm64 hosts and on macOS it falls back to
  full emulation. The default `docker` backend is unaffected and runs natively
  on every supported platform.

Building from source additionally requires Git, Make, and Go 1.24 or newer. Use
the official [Go installation instructions](https://go.dev/doc/install) if your
distribution does not provide a recent enough version.

On Ubuntu 24.04, install the runtime dependencies with:

```bash
sudo apt-get update
sudo apt-get install docker.io docker-buildx git
sudo systemctl enable --now docker
```

On Fedora 44, install the runtime dependencies with:

```bash
sudo dnf install docker docker-buildx
sudo systemctl enable --now docker
```

Access to the Docker socket is required. To use the `docker` group on either
distribution, add your user and then sign out and back in, or run `newgrp docker`
to apply the membership in the current terminal:

```bash
sudo gpasswd -a "$USER" docker
```

See Docker's [Linux post-install instructions](https://docs.docker.com/engine/install/linux-postinstall/)
and account for the group's root-equivalent privileges.

On macOS, install Docker Desktop and the source-build dependencies above.

## Installation

### Debian package (recommended)

For Ubuntu 24.04 on x86-64, download the `.deb` from the
[rolling release](https://github.com/eclipse-enclave/enclave/releases/tag/rolling),
then install it with APT so runtime dependencies are resolved:

```bash
sudo apt install ./enclave_*_amd64.deb
```

### RPM package

For Fedora Linux on x86-64, download the `.rpm` from the
[rolling release](https://github.com/eclipse-enclave/enclave/releases/tag/rolling),
then install it with DNF:

```bash
sudo dnf install ./enclave-*.x86_64.rpm
```

### Standalone binary

The rolling release also provides self-contained binaries:

| Artifact | Platform |
|----------|----------|
| `enclave-linux-amd64` | Linux x86-64 |
| `enclave-linux-arm64` | Linux arm64 |
| `enclave-darwin-arm64` | macOS (Apple Silicon) |
| `enclave-darwin-amd64` | macOS (Intel) |
| `enclave-windows-amd64.zip` | Windows x86-64 (WSL2 launcher) |
| `enclave-windows-arm64.zip` | Windows arm64 (WSL2 launcher) |

Download the artifact for your platform together with `checksums.txt`, verify
it, and place it on your `PATH`. On Linux:

```bash
sha256sum --check --ignore-missing checksums.txt
sudo install enclave-linux-amd64 /usr/local/bin/enclave
```

On macOS, substituting `enclave-darwin-amd64` on Intel:

```bash
shasum -a 256 --check --ignore-missing checksums.txt
xattr -d com.apple.quarantine ./enclave-darwin-arm64 2>/dev/null || true
sudo install -d /usr/local/bin
sudo install enclave-darwin-arm64 /usr/local/bin/enclave
```

The macOS binaries are unsigned and not notarized, so a binary downloaded
through a browser carries a quarantine attribute that makes Gatekeeper refuse to
run it; `install` propagates the attribute, so remove it first. Fetching the
artifact with `curl` or `gh release download` avoids it entirely, which is why
the command above tolerates its absence.

The binary includes the Dockerfiles, extensions, documentation, and other
runtime assets. It extracts its assets on first use.

### From source

Clone the repository, then build and install the self-contained binary:

```bash
git clone https://github.com/eclipse-enclave/enclave.git
cd enclave
make install
```

On Linux this installs the binary to `~/.local/bin/`. Make sure that directory
is on your `PATH`. On macOS the default is `/usr/local/bin/`; copying there may
need `sudo` or a writable `/usr/local/bin`. Override the destination with
`make install INSTALL_BIN=...`.

### Windows (WSL2)

There is no native Windows build. Install WSL2 with an Ubuntu 24.04
distribution, make Docker available inside it — either through Docker Desktop's
WSL integration or by installing Docker Engine in the distribution — and follow
the Linux instructions above from inside WSL, using the `.deb` or the Linux
binary matching the host architecture.

That is enough to use Enclave from a WSL shell. To use it from PowerShell as
well, install the launcher on Windows too:

```powershell
scoop install https://github.com/eclipse-enclave/enclave/releases/download/rolling/enclave.json
```

`enclave.exe` forwards every argument to the Linux binary inside the
distribution and returns its exit code; it is not a native build. Run it from a
directory inside the distribution, for example
`\\wsl.localhost\Ubuntu\home\you\project`. A Windows drive path such as
`C:\Users\you\project` is refused by default: it would have to be reached
through `/mnt/c`, where every file access crosses the WSL interop layer and is
markedly slower.

See [Windows](docs/windows.md) for the working-directory rules, environment
forwarding, and exit codes.

## Quick Start

Run in any project directory:

```bash
enclave                     # Start claude (default) in current project
enclave --tool codex        # Use a different tool
enclave --backend qemu --tool codex  # Experimental QEMU microVM run (implies --slim, all-network)
enclave continue            # Continue latest session
enclave ps                  # List running containers (--all for stopped, --json for scripts)
enclave exec                # Attach to running container
enclave shell               # Open interactive shell in container
enclave info                # Show config and image details
```

**Authentication:** The simplest and recommended approach is to just log in from inside the container the first time you run — OAuth sessions are saved to a persistent auth store on the host and reused automatically on every subsequent run. No configuration needed.

To use declared env credentials instead, place them in `~/.local/state/enclave/secrets/global.env`. See [Authentication & Secrets](docs/auth.md).

For the full command and flag reference, see [docs/cli-reference.md](docs/cli-reference.md).

## Custom Commands

Drop an executable file into `~/.config/enclave/commands/host/<name>` and it becomes a
`enclave <name>` verb that runs on the host. The file must have the executable
bit set (`chmod +x`); the OS resolves its interpreter via the shebang line, so
any language works. Symlinks are followed to their target, so a link to an
executable script registers as a command (a broken link is warned about). Host
command symlinks may point anywhere; session command symlinks must be
**relative** links that stay inside `~/.config/enclave/commands/session` across every
hop, because that directory is bind-mounted at a neutral path inside the
container and absolute link targets do not resolve there (an absolute,
escaping, or looping session symlink is warned about and skipped). Built-in
commands always take precedence over a custom command of the same name.

```bash
mkdir -p ~/.config/enclave/commands/host
cat > ~/.config/enclave/commands/host/deploy <<'EOF'
#!/bin/sh
echo "deploying with args: $@"
EOF
chmod +x ~/.config/enclave/commands/host/deploy
enclave deploy --env prod    # runs the script with ["--env", "prod"]
```

Everything after the command name is passed to the script verbatim (including
flags, `--`, and `--help`); stdin, stdout, stderr, and the exit code pass
through untouched. enclave's own flags must come **before** the command name,
and host commands accept only global flags there (e.g. `enclave --verbose
deploy`). Host commands run with the inherited environment plus
`ENCLAVE_BIN` (path to the enclave binary), `ENCLAVE_PROJECT_ROOT` (the
current project directory), and `ENCLAVE_CONFIG_DIR` (`~/.config/enclave`).

Dropping the executable into `~/.config/enclave/commands/session/<name>` instead runs
it **inside a sandboxed session container** (like `enclave shell`). The
`session/` tree is mounted read-only at a fixed neutral path inside the
container and the script is exec'd there with its arguments verbatim, so its
shebang is honored as long as the interpreter and architecture exist in the
container image (slim images may lack some interpreters). Session commands
accept the full session flag set before the name (e.g. `enclave --tool codex
triage ...`) to control the sandbox; unlike host commands, they receive **no**
`ENCLAVE_*` environment injection. If the same name exists
in both `host/` and `session/`, the host command wins.

## Learn More

| Topic | Doc |
|-------|-----|
| CLI Reference | [docs/cli-reference.md](docs/cli-reference.md) |
| Development | [docs/DEV.md](docs/DEV.md) |
| Authentication & Secrets | [docs/auth.md](docs/auth.md) |
| Networking | [docs/networking.md](docs/networking.md) |
| Tool Profiles & Images | [docs/tools.md](docs/tools.md) |
| Sessions & Persistence | [docs/persistence.md](docs/persistence.md) |
| Configuration | [docs/configuration.md](docs/configuration.md) |
| Windows (WSL2) | [docs/windows.md](docs/windows.md) |
| Architecture | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| Extensions | [docs/extensions/README.md](docs/extensions/README.md) |
| Security | [docs/security/README.md](docs/security/README.md) |

Project resources: [Eclipse project page](https://projects.eclipse.org/projects/ecd.enclave),
[contributing guide](CONTRIBUTING.md), [security policy](SECURITY.md),
[code of conduct](CODE_OF_CONDUCT.md), [license](LICENSE.md), and
[notices](NOTICE.md).
