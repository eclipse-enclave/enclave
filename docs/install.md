# Installation

## Requirements

Linux and macOS (with Docker Desktop) are supported natively. On Windows,
Enclave runs inside WSL2: the Linux instructions apply within the WSL
distribution, and `enclave.exe` is a launcher that forwards to it rather than a
native build. See [Windows](windows.md).

Runtime dependencies:

- One container engine. Enclave detects whichever is installed and asks once
  when both are; see [Backend detection](cli-reference.md#backend-detection).
  - Rootful Docker (CLI on `PATH`, daemon running) with the buildx plugin; the
    sandbox image build requires BuildKit. Rootless Docker is not supported.
  - Rootless Podman (`podman` on `PATH`) with netavark networking. It needs no
    daemon and no privileged group. On hosts without idmapped-mount support
    the first start after each image rebuild copies the image, which can take
    minutes for large tool images; see
    [Podman backend](cli-reference.md#podman-backend).
- Git 2.26 or newer is required to read the host identity at session start.
- Optional: `qemu-system-x86_64` and `cpio` for the experimental `qemu` backend.
  It builds an x86-64 guest and is practical only on x86-64 Linux hosts, where
  KVM can accelerate it; on arm64 hosts and on macOS it falls back to full
  emulation.

Building from source additionally requires Make and Go 1.24 or newer. Use
the official [Go installation instructions](https://go.dev/doc/install) if your
distribution does not provide a recent enough version.

### Docker

On Ubuntu 24.04:

```bash
sudo apt-get update
sudo apt-get install docker.io docker-buildx git
sudo systemctl enable --now docker
```

On Fedora 44:

```bash
sudo dnf install docker docker-buildx
sudo systemctl enable --now docker
```

Your user needs access to the Docker socket. To use the `docker` group on
either distribution, add your user and then sign out and back in, or run
`newgrp docker` to apply the membership in the current terminal:

```bash
sudo gpasswd -a "$USER" docker
```

See Docker's [Linux post-install instructions](https://docs.docker.com/engine/install/linux-postinstall/).
The group grants root-level privileges.

On macOS, install Docker Desktop.

### Rootless Podman

Rootless Podman runs as your user, so there is no daemon to start and no group
to join. Install it on Ubuntu 24.04 with:

```bash
sudo apt-get update
sudo apt-get install podman git
```

or on Fedora 44 with:

```bash
sudo dnf install podman
```

Podman's [rootless setup guide](https://github.com/containers/podman/blob/main/docs/tutorials/rootless_tutorial.md)
covers the subordinate ID ranges and other host prerequisites.

### Running as a regular user

Run enclave as your regular user, not through `sudo`: enclave refuses to run as
root unless you pass `--allow-root` or set `ENCLAVE_ALLOW_ROOT=1` (see
[Running as root](cli-reference.md#running-as-root)). Run it from a project
directory, too: it refuses to mount your home directory, hidden home entries
such as `~/.ssh`, or other sensitive host data. To mount one such directory,
pass it with `--add-sensitive-dir` or list it in `sensitive_dir_exceptions` in
config; `--allow-sensitive-mounts` lifts the check for all of them (see
[Sensitive mounts](cli-reference.md#sensitive-mounts)).

## Debian package

For Ubuntu 24.04 on x86-64, download the `.deb` from the
[rolling release](https://github.com/eclipse-enclave/enclave/releases/tag/rolling),
then install it with APT so runtime dependencies are resolved:

```bash
sudo apt install ./enclave_*_amd64.deb
```

The package depends on Docker. On a Podman-only host, use the
[standalone binary](#standalone-binary) instead.

## RPM package

For Fedora Linux on x86-64, download the `.rpm` from the
[rolling release](https://github.com/eclipse-enclave/enclave/releases/tag/rolling),
then install it with DNF:

```bash
sudo dnf install ./enclave-*.x86_64.rpm
```

This package depends on Docker too; Podman-only hosts use the
[standalone binary](#standalone-binary).

## Standalone binary

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
it, and install it into a directory on your `PATH` that you own, so it can
update itself. On Linux:

```bash
sha256sum --check --ignore-missing checksums.txt
install -d ~/.local/bin
install enclave-linux-amd64 ~/.local/bin/enclave
```

On macOS, substituting `enclave-darwin-amd64` on Intel:

```bash
shasum -a 256 --check --ignore-missing checksums.txt
xattr -d com.apple.quarantine ./enclave-darwin-arm64 2>/dev/null || true
install -d ~/.local/bin
install enclave-darwin-arm64 ~/.local/bin/enclave
```

Most Linux distributions put `~/.local/bin` on `PATH` at login once it exists;
on macOS, add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc`.

The macOS binaries are unsigned and not notarized, so a binary downloaded
through a browser carries a quarantine attribute that makes Gatekeeper refuse to
run it; `install` propagates the attribute, so remove it first. Fetching the
artifact with `curl` or `gh release download` avoids it entirely, which is why
the command above tolerates its absence.

The binary includes the Dockerfiles, extensions, documentation, and other
runtime assets and extracts them on first use. Shell completion never extracts
them; until another command has, it reads tool and feature names from the
binary itself.

### Updates

A standalone binary installed this way can update itself: `enclave self-update`
replaces the executable with the latest build of its release channel, here
`rolling`. Enclave checks for a different build at most once every 24 hours and
reports it on interactive commands. To install updates automatically, add
`"auto_update": true` to the global `config.json`; `"update_channel"` selects
`rolling` or `stable`, the official releases published under
[download.eclipse.org/enclave/releases/](https://download.eclipse.org/enclave/releases/).
The download is checked against the release's `checksums.txt`, which catches
corruption but not a compromised release, so automatic updates trust whoever
can publish the release. Package and source installs do not self-update;
upgrade them with the package manager or by rebuilding.

The updater never elevates privileges. A system-wide install, such as
`sudo install enclave-linux-amd64 /usr/local/bin/enclave`, is updated by
repeating these steps; Enclave still reports new builds, with the commands to
run. See [`enclave self-update`](cli-reference.md#inspect) for the full
channel and update-check behavior.

## From source

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

### Shell completion

On Linux, `make install` also installs shell completions: bash into
`~/.local/share/bash-completion/completions/`, which is picked up automatically,
and zsh into `~/.local/share/zsh/site-functions/`, which zsh only searches once
you add it to `fpath` in `~/.zshrc`, before whatever runs `compinit`:

```zsh
fpath=(~/.local/share/zsh/site-functions $fpath)
```

Frameworks such as oh-my-zsh and prezto call `compinit` themselves, so put the
line above their setup and leave the rest to them. On a plain zsh setup that
does not call it anywhere, add `autoload -U compinit && compinit` after the
`fpath` line. Both completion paths honor `XDG_DATA_HOME` if you set it.

The `.deb` and `.rpm` packages install both completions into system directories
that need no such setup. Any shell can also generate its own script on demand
with `enclave completion <bash|zsh|fish>`.

## Windows (WSL2)

There is no native Windows build. Install WSL2 with an Ubuntu 24.04
distribution and make Docker available inside it, either through Docker
Desktop's WSL integration or by installing Docker Engine in the distribution.
Then follow the Linux instructions above from inside WSL, using the `.deb` or
the Linux binary matching the host architecture.

That is enough to use Enclave from a WSL shell. To use it from PowerShell as
well, install the launcher on Windows too:

```powershell
scoop install https://github.com/eclipse-enclave/enclave/releases/download/rolling/enclave.json
```

`enclave.exe` forwards every argument to the Linux binary inside the
distribution and returns its exit code. Run it from a directory inside the
distribution, for example `\\wsl.localhost\Ubuntu\home\you\project`. A Windows
drive path such as `C:\Users\you\project` is refused by default: it would have
to be reached through `/mnt/c`, where every file access crosses the WSL interop
layer and is much slower.

See [Windows](windows.md) for the working-directory rules, environment
forwarding, and exit codes.
