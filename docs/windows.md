# Windows

How to run Enclave from Windows through WSL2, which working directories work,
and how environment variables, signals, and exit codes cross the boundary.

`enclave.exe` is a launcher, not a native port. It forwards every argument to
the Linux `enclave` binary inside a WSL2 distribution and returns that binary's
exit code. It parses no arguments of its own, so `enclave.exe --help`,
`enclave.exe ps --json`, and `enclave.exe --tool codex -- -p "…"` behave
exactly as on Linux.

No native Windows implementation is planned: Enclave needs a Linux container
runtime and Linux path semantics.

## Setup

1. Install WSL2 and a distribution:

   ```powershell
   wsl --install -d Ubuntu-24.04
   ```

2. Make Docker available inside the distribution, either through Docker
   Desktop's WSL integration or by installing Docker Engine in the
   distribution itself. See [Installation](install.md).

3. Install the Linux `enclave` build inside the distribution: the `.deb` or the
   `enclave-linux-amd64` / `enclave-linux-arm64` binary from the
   [latest release](https://github.com/eclipse-enclave/enclave/releases/latest).
   This binary runs the sessions.

4. Install the launcher on Windows with [Scoop](https://scoop.sh):

   ```powershell
   scoop install https://github.com/eclipse-enclave/enclave/releases/latest/download/enclave.json
   ```

   Or download `enclave-windows-amd64.zip` (`enclave-windows-arm64.zip` on
   arm64), verify it against `checksums.txt`, and put the extracted
   `enclave.exe` on your `PATH`.

Steps 1 to 3 suffice inside a WSL shell; step 4 adds PowerShell and
`cmd.exe`, and needs step 3 (otherwise it reports that enclave is not
installed in the distribution).

- A distribution created with `wsl --import` logs in as root by default, and
  Enclave [refuses to run as root](install.md#running-as-root). Create a
  regular user and make it the default (`[user] default=<name>` in the
  distribution's `/etc/wsl.conf`). `ENCLAVE_ALLOW_ROOT` set on Windows is
  forwarded into the distribution like any other `ENCLAVE_` variable.
- `winget` is not supported: it needs a pull request into
  `microsoft/winget-pkgs` per release.

## Working directory

The launcher derives the distribution from the current directory, since a
distribution only sees its own filesystem.

| Current directory | Result |
|-------------------|--------|
| `\\wsl.localhost\Ubuntu\home\you\project` (or `\\wsl$\…`) | Runs in `Ubuntu` at `/home/you/project`. This is the intended path |
| A drive letter mapping a WSL share, e.g. `Z:\` → `\\wsl.localhost\Ubuntu\home\you\project` | Resolved back to the share and treated as the row above |
| `C:\Users\you\project` | Refused. With `ENCLAVE_WSL_ALLOW_WINDOWS_PATH=1`: runs at `/mnt/c/Users/you/project` in `ENCLAVE_WSL_DISTRO` or the WSL default distribution |
| `\\server\share\…` | Refused. A network share has no path inside a distribution |
| A drive letter mapping anything else | Refused, and the error names what the drive points at |
| Any of the above reached through a `..` component | Refused. A `..` above a distribution root names a directory in a different distribution, so it is not resolved |

- PowerShell accepts `\\wsl.localhost\…` as its location (first row).
  `cmd.exe` cannot, so `pushd \\wsl.localhost\Ubuntu\home\you\project` maps a
  drive letter, which the launcher resolves back to the share (second row).
- A letter for a real file server is refused: no distribution path names those
  files, and guessing would bind-mount the wrong directory.
- Path below the mapping is appended: with `Z:` mapped to `/home/you/project`,
  `Z:\src` means `/home/you/project/src`.
- Drive paths are refused by default because every access under `/mnt/c`
  crosses the slow WSL interop layer. Keep projects in the distribution (e.g.
  under `~/`); `ENCLAVE_WSL_ALLOW_WINDOWS_PATH=1` is the escape hatch.
- If `ENCLAVE_WSL_DISTRO` names another distribution than the directory's, the
  directory wins with a warning.

## Environment variables

The launcher reads these on the Windows side. They are not forwarded into the
distribution.

| Variable | Effect |
|----------|--------|
| `ENCLAVE_WSL_DISTRO` | Distribution to use for a Windows drive path. Ignored, with a warning, when the current directory already names one |
| `ENCLAVE_WSL_ALLOW_WINDOWS_PATH` | Set to `1` to accept a Windows drive path and mount it through `/mnt/<letter>` |
| `ENCLAVE_WSL_FORWARD_ENV` | Comma-separated list of extra variables to forward into the distribution, each optionally with a `WSLENV` flag suffix |

WSL only passes variables named in `WSLENV`, so nothing from your Windows shell
reaches the distribution by default. The launcher forwards:

- every `ENCLAVE_*` variable, except the three above and `ENCLAVE_HOME` (whose
  value is a Windows path that the Linux binary cannot use)
- anything named in `ENCLAVE_WSL_FORWARD_ENV`, which overrides those exclusions

Each entry in `ENCLAVE_WSL_FORWARD_ENV` may carry a `WSLENV` flag suffix:

| Suffix | Effect |
|--------|--------|
| `/p` | Translate a single path |
| `/l` | Translate a path list |
| `/u`, `/w` | Restrict the direction |

```powershell
$env:ENCLAVE_WSL_FORWARD_ENV = "ANTHROPIC_API_KEY,MY_SCRATCH_DIR/p"
```

A `WSLENV` you set yourself is preserved; the launcher appends to it, and your
flags win for a name you already listed.

Credentials such as `ANTHROPIC_API_KEY` set in PowerShell do not cross over
unless named. Usually you configure them inside the distribution (log in from
the container, or use `~/.local/state/enclave/secrets/global.env`); see
[Authentication and secrets](auth.md).

## Terminal, signals, and exit codes

- Stdio is inherited, so the agent (and `docker attach`) gets a real terminal.
- `wsl.exe` forwards Ctrl-C to the child; the launcher ignores it so it cannot
  exit first.
- The child's exit code is passed through.
- A launcher-level failure (no WSL2, an unusable working directory, no
  `enclave` in the distribution) exits with 125, as Docker does when it fails
  before running the program. Messages the launcher produces
  itself are prefixed `enclave (windows launcher):`.
- The exception is the fallback for a `wsl.exe` without `--cd` (see below):
  there the shell inside the distribution reports an unreachable directory in
  its own words and exits 125.

## Shell profile is not loaded

The launcher runs the binary with `wsl.exe -e`, so no shell re-parses your
arguments, and `/etc/profile`, `/etc/profile.d/*`, `~/.profile`, and
`~/.bashrc` do not run. That only matters if a profile makes Docker reachable,
e.g. `export DOCKER_HOST=unix:///run/user/$UID/docker.sock` in `~/.bashrc`:
Enclave then cannot reach the daemon, although `enclave` works in a WSL
terminal. Set such variables in `/etc/environment` or a systemd user
environment, or forward them from Windows with `ENCLAVE_WSL_FORWARD_ENV`.

## How the launcher finds the binary

The launcher first runs a fixed login-shell probe in the distribution that
looks for `enclave` on `PATH` and in `~/.local/bin`, `/usr/local/bin`, and
`/usr/bin`, then executes the found binary directly with your arguments. This
costs one extra round trip, roughly 100–300 ms on a cold distribution, and the
result is not cached. On a `wsl.exe` old enough to lack `--cd`, the launcher
changes directory through a shell instead, still passing your arguments as
positional parameters. The details are in
[Windows launcher](DEV.md#windows-launcher).

## Limitations

- The session does not inherit the distribution's shell profile; see
  [Shell profile is not loaded](#shell-profile-is-not-loaded).
- The launcher installs nothing: not WSL2, not a distribution, not Docker, not
  the Linux `enclave` build.
- No diagnostic command; failures are reported inline.
- Argument passing is verified in CI against a reimplementation of Windows'
  command-line parsing, and against a real `wsl.exe` by a manual pre-release
  gate; see [Windows launcher](DEV.md#windows-launcher).
