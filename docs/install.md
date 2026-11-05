# Installation

Requirements, install options, updates, and upgrade notes. For a step-by-step
walkthrough per platform, see
[Getting Started](https://enclave.eclipse.dev/docs/getting-started) on the
website.

## Requirements

| Requirement | Details |
|-------------|---------|
| Host | Linux or macOS. Windows through WSL2 only; see [Windows](windows.md) |
| Container engine | Rootful Docker with the buildx plugin (the image build needs BuildKit), or Podman (rootless works). Rootless Docker is not supported |
| Git | 2.26 or newer, with a configured author and committer identity; see [Git](git.md) |
| QEMU (optional) | `qemu-system-x86_64` and `cpio` for the experimental `qemu` backend, practical only on x86-64 Linux with KVM; see [Backends](backends.md#host-requirements) |
| Build from source | Make and Go; see [CONTRIBUTING.md](../CONTRIBUTING.md#building-from-source) |

With both Docker and Podman installed, Enclave asks once which one to use; see
[Backends](backends.md). Host AppArmor profiles can affect binaries inside
rootless Podman containers; see
[AppArmor on rootless Podman](security/rootless.md#apparmor-on-rootless-podman).

### Container engine setup

Ubuntu 24.04:

```bash
sudo apt-get update
sudo apt-get install docker.io docker-buildx git
sudo systemctl enable --now docker
```

Fedora 44:

```bash
sudo dnf install docker docker-buildx
sudo systemctl enable --now docker
```

For Podman instead, install `podman netavark git` (Ubuntu 24.04) or
`podman git` (Fedora 44). Podman needs neither the buildx plugin nor a daemon.

On macOS, install Docker Desktop.

### Docker socket access

Docker needs access to its socket. Add your user to the `docker` group, then
sign out and back in, or run `newgrp docker` in the current terminal:

```bash
sudo gpasswd -a "$USER" docker
```

The group grants root-equivalent privileges. See Docker's
[Linux post-install steps](https://docs.docker.com/engine/install/linux-postinstall/).

## Install

Builds are published on the GitHub
[rolling release](https://github.com/eclipse-enclave/enclave/releases/tag/rolling),
rebuilt from every push to `main`.

| Platform | Artifact | Install with |
|----------|----------|--------------|
| Ubuntu 24.04, x86-64 | `enclave_*_amd64.deb` (recommended) | `sudo apt install ./enclave_*_amd64.deb` |
| Fedora, x86-64 | `enclave-*.x86_64.rpm` | `sudo dnf install ./enclave-*.x86_64.rpm` |
| Linux x86-64 / arm64 | `enclave-linux-amd64` / `enclave-linux-arm64` | [Standalone binary](#standalone-binary) |
| macOS Apple Silicon / Intel | `enclave-darwin-arm64` / `enclave-darwin-amd64` | [Standalone binary](#standalone-binary) |
| Windows x86-64 / arm64 | `enclave-windows-amd64.zip` / `enclave-windows-arm64.zip` | [Windows](windows.md) (WSL2 launcher) |

The `.deb` and `.rpm` packages accept either Docker (with buildx) or Podman as
a dependency. Docker is pulled in when neither is installed, so install Podman
first if you want it. Installing with `apt` or `dnf` resolves the runtime
dependencies.

### Standalone binary

Download the binary together with `checksums.txt`, verify it, and install it
into a directory on your `PATH` that you own, so it can
[update itself](#updating).

Linux:

```bash
sha256sum --check --ignore-missing checksums.txt
install -d ~/.local/bin
install enclave-linux-amd64 ~/.local/bin/enclave
```

macOS (use `enclave-darwin-amd64` on Intel):

```bash
shasum -a 256 --check --ignore-missing checksums.txt
xattr -d com.apple.quarantine ./enclave-darwin-arm64 2>/dev/null || true
install -d ~/.local/bin
install enclave-darwin-arm64 ~/.local/bin/enclave
```

- Most Linux distributions put `~/.local/bin` on `PATH` at login once it
  exists. On macOS, add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc`.
- The macOS binaries are unsigned and not notarized. A browser download carries
  a quarantine attribute that makes Gatekeeper refuse to run the binary, and
  `install` copies the attribute, so the `xattr` step comes first. Downloads
  with `curl` or `gh release download` have no attribute, which is why the
  command tolerates its absence.
- The binary contains the Dockerfiles, extensions, documentation, and other
  runtime assets and extracts them on first use. Shell completion never
  extracts them; until another command has, it reads tool and feature names
  from the binary.

### From source

Requires Make and Go; see
[CONTRIBUTING.md](../CONTRIBUTING.md#building-from-source) for the exact
versions and the full build instructions.

```bash
git clone https://github.com/eclipse-enclave/enclave.git
cd enclave
make install
```

| Platform | Binary destination | Shell completions |
|----------|--------------------|-------------------|
| Linux | `~/.local/bin/` | bash: `~/.local/share/bash-completion/completions/`; zsh: `~/.local/share/zsh/site-functions/` |
| macOS | `/usr/local/bin/` (may need `sudo` or a writable directory) | not installed |

Override the binary destination with `make install INSTALL_BIN=...`. Both
completion paths honor `XDG_DATA_HOME`.

## Shell completion

| Install method | Setup |
|----------------|-------|
| `.deb`, `.rpm` | None. Completions are installed into system directories |
| `make install` on Linux | bash: none. zsh: add the directory to `fpath` (below) |
| Any | Generate a script with `enclave completion <bash\|zsh\|fish>` |

zsh only searches `~/.local/share/zsh/site-functions/` once it is on `fpath`.
Add this to `~/.zshrc` before whatever runs `compinit`:

```zsh
fpath=(~/.local/share/zsh/site-functions $fpath)
```

Frameworks such as oh-my-zsh and prezto call `compinit` themselves, so put the
line above their setup. A plain zsh setup without `compinit` needs
`autoload -U compinit && compinit` after the `fpath` line.

## Updating

| Install method | How to update |
|----------------|---------------|
| Standalone binary in a directory you own | `enclave self-update`, or automatically with `"auto_update": true` |
| Standalone binary in a system directory (e.g. after `sudo install`) | Repeat the install steps; Enclave prints the commands |
| `.deb`, `.rpm` | Package manager |
| Source build, or a binary using a runtime asset tree beside it or in `ENCLAVE_HOME` | Rebuild or reinstall |

Only standalone release binaries update themselves. The other installs are
refused and never checked, because replacing the binary alone would leave
their assets behind.

### `enclave self-update`

Downloads the `linux` or `darwin` binary for the host architecture from the
configured release channel, verifies it against the release's
`checksums.txt`, and atomically replaces the running executable.

- The checksums come from the same release as the binary. They detect
  corrupted or truncated downloads, not a compromised release, so automatic
  updates trust whoever can publish the release.
- If the executable already matches the release's checksum, for example
  because a concurrent update installed it, nothing is downloaded.
- A download that receives no data for 30 seconds fails. Ctrl-C or SIGTERM
  aborts it with exit code 130 and removes the partial file.
- The updater never elevates privileges. If the executable's directory is not
  writable, or the system denies the final replacement (for example in a
  sticky directory holding another user's binary), `self-update` exits with an
  error. Both it and the periodic check then print the download URLs of the
  binary and `checksums.txt` and the command that verifies the download and
  reinstalls it with `sudo install`.

### Release channels

| Channel | Built from | Served from |
|---------|------------|-------------|
| `rolling` | Every push to `main` | `https://download.eclipse.org/enclave/rolling/`; `latest` names the commit of the current build |
| `stable` | Version tags | `https://download.eclipse.org/enclave/releases/<version>/`; `releases/latest` names the newest final release |

- A binary follows the channel it was published on unless the global config
  sets `update_channel`. `enclave config` shows the effective channel.
- Switching channels installs the other channel's latest build, even if it is
  older.
- Within `stable`, only a higher version is offered. A pre-release (`alpha`,
  `beta`, or `rc`, such as `1.1.0-rc.1`) is published but never becomes
  `latest`, so it updates only once a higher final release becomes `latest`;
  later pre-releases are never offered. A release installed by hand before
  `latest` moved is never replaced by an older one.

### Update checks

Interactive invocations (stdin, stdout, and stderr all terminals) check the
channel at most once every 24 hours and report a different build. The last
check is recorded in `~/.local/state/enclave/self-update-check`.

- `"auto_update": true` in the global config installs the update during that
  check. Ctrl-C during that install exits with code 130 without running the
  command.
- All update-check output, including `--verbose` diagnostics, goes to stderr.
- Checks are skipped for scripts and other non-interactive callers, shell
  completion, structured output, and user commands. A failed check waits for
  the next interval.
- `auto_update` and `update_channel` are global-only keys; see
  [Configuration](configuration.md).

## Running as root

Enclave refuses to run as root, including through `sudo`. Run it as a regular
user with access to the Docker socket (see
[Docker socket access](#docker-socket-access)), or with rootless Podman
(`--backend podman`).

The agent's container user takes the host UID. As root, the agent would run as
UID 0, which is host root on bind mounts under rootful Docker. Files written to
the project and to the stores under the config, state, and cache roots would
become root-owned, and later runs as the regular user would fail on them. With
`sudo -E`, those roots are in the regular user's home.

To skip the check, for example in a CI job container, pass `--allow-root` or
set `ENCLAVE_ALLOW_ROOT=1`. Each such run prints a warning.

- The opt-in only skips the check. Running as root stays unsupported, and the
  runtime image cannot be built for UID 0 yet.
- It has no config key, so neither global nor project config can grant it.
- Help, version, and shell completion work without it and write nothing.

Enclave also refuses to mount your home directory, hidden home entries such as
`~/.ssh`, and other sensitive host data, so run it from a project directory.
See [Mounts](mounts.md#sensitive-mounts).

## Upgrade notes

Steps needed once when upgrading from older releases.

### Per-session networks

Sessions now run on a dedicated network per session instead of the default
bridge.

- Podman hosts using CNI must switch to netavark before starting sessions.
- On Linux Docker Engine, existing `docker0`-specific firewall rules for
  `--bridge-port` and the IDE bridge no longer match. Replace them with rules
  for each session's bridge and subnet; see
  [host service configuration](networking.md#linux-host-service-configuration).
- Session startup now requires a successful `docker info` or `podman info`
  query to select network options. Previously, a failed query only suppressed
  configuration warnings.
- Older releases that put sessions on Docker's default bridge could set
  `"icc": false` in `/etc/docker/daemon.json` to block direct
  container-to-container traffic there. That interim mitigation needs an active
  `br_netfilter` path, covers only the default bridge, can break unrelated
  default-bridge workloads, and does not replace per-session networks.

### Legacy TLS certificate cache

Only the gateway CA persists on the host now; leaf certificates stay inside
each gateway container. Upgrades may leave an unused cache at
`~/.local/state/enclave/tls/hosts`. On Linux, its old container-owned
permissions can prevent normal removal. Delete it with
`sudo rm -rf ~/.local/state/enclave/tls/hosts`, or with
`podman unshare rm -rf ~/.local/state/enclave/tls/hosts` when rootless Podman
last owned it.
