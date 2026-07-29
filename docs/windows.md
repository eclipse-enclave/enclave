# Windows

`enclave.exe` is a launcher, not a native port. It forwards every argument it is
given to the Linux `enclave` binary inside a WSL2 distribution and returns that
binary's exit code. It parses no arguments of its own, so `enclave.exe --help`,
`enclave.exe ps --json`, and `enclave.exe --tool codex -- -p "…"` all behave
exactly as they do on Linux.

There is no native Windows implementation and none is planned. Enclave needs a
Linux container runtime and Linux path semantics; the launcher exists so the
`enclave` command works from a Windows shell, not to remove that requirement.

## Setup

1. Install WSL2 and a distribution:

   ```powershell
   wsl --install -d Ubuntu-24.04
   ```

2. Make Docker available **inside** the distribution, either through Docker
   Desktop's WSL integration or by installing Docker Engine in the distribution
   itself. See the [requirements](../README.md#requirements).

3. Install the Linux `enclave` build inside the distribution — the `.deb` or the
   `enclave-linux-amd64` / `enclave-linux-arm64` binary from the
   [rolling release](https://github.com/eclipse-enclave/enclave/releases/tag/rolling).
   This is the part that does the work.

4. Install the launcher on Windows, with [Scoop](https://scoop.sh):

   ```powershell
   scoop install https://github.com/eclipse-enclave/enclave/releases/download/rolling/enclave.json
   ```

   Or download `enclave-windows-amd64.zip` (`enclave-windows-arm64.zip` on
   arm64), verify it against `checksums.txt`, and put the extracted `enclave.exe`
   on your `PATH`.

Steps 3 and 4 are both required. Installing only the launcher gives you a command
that reports that enclave is not installed in the distribution.

`winget` is not supported: it needs a pull request into `microsoft/winget-pkgs`
per release, which does not fit a rolling release.

## The working directory decides the distribution

Enclave operates on the project in the current directory, so the launcher derives
the distribution from that directory. A distribution can only see its own
filesystem, which is why nothing else could work.

| Current directory | Result |
|-------------------|--------|
| `\\wsl.localhost\Ubuntu\home\you\project` (or `\\wsl$\…`) | Runs in `Ubuntu` at `/home/you/project`. This is the intended path. |
| A drive letter mapping a WSL share, e.g. `Z:\` → `\\wsl.localhost\Ubuntu\home\you\project` | Resolved back to the share and treated as the row above. |
| `C:\Users\you\project` | Refused. With `ENCLAVE_WSL_ALLOW_WINDOWS_PATH=1`: runs at `/mnt/c/Users/you/project` in `ENCLAVE_WSL_DISTRO` or the WSL default distribution. |
| `\\server\share\…` | Refused. A network share has no path inside a distribution. |
| A drive letter mapping anything else | Refused, and the error names what the drive points at. |

PowerShell supports a `\\wsl.localhost\…` path as its current location, so the
first row is reachable there directly. `cmd.exe` cannot hold a UNC working
directory, so `pushd \\wsl.localhost\Ubuntu\home\you\project` assigns a free
drive letter instead. That is the second row: the launcher asks Windows what the
letter maps to and, when the answer is a WSL share, carries on as if you had
given it the share path. A letter mapping a real file server is still refused,
because no path inside a distribution names those files and guessing one would
bind-mount the wrong directory.

If the letter is mapped below the distribution root, or the working directory
sits deeper than the mapping, the remainder is appended — a `Z:` mapped to
`/home/you/project` makes `Z:\src` mean `/home/you/project/src`.

Windows drive paths are refused by default because every file access under
`/mnt/c` crosses the WSL interop layer. That is slow enough to matter for a
repository an agent is actively reading and writing. Keeping the project in the
distribution's own filesystem, for example under `~/`, is the fix;
`ENCLAVE_WSL_ALLOW_WINDOWS_PATH=1` is the escape hatch when it is not.

If `ENCLAVE_WSL_DISTRO` names a different distribution than the current directory
lives in, the directory wins and the launcher warns. The other distribution
cannot reach those files.

## Environment variables

The launcher reads these on the Windows side. They are not forwarded into the
distribution.

| Variable | Effect |
|----------|--------|
| `ENCLAVE_WSL_DISTRO` | Distribution to use for a Windows drive path. Ignored, with a warning, when the current directory already names one. |
| `ENCLAVE_WSL_ALLOW_WINDOWS_PATH` | Set to `1` to accept a Windows drive path and mount it through `/mnt/<letter>`. |
| `ENCLAVE_WSL_FORWARD_ENV` | Comma-separated list of extra variables to forward into the distribution. |

WSL only passes variables named in `WSLENV`, so nothing from your Windows shell
reaches the distribution by default. The launcher forwards:

- every `ENCLAVE_*` variable, except the three above and `ENCLAVE_HOME` (whose
  value is a Windows path that the Linux binary cannot use);
- anything named in `ENCLAVE_WSL_FORWARD_ENV`, which overrides those exclusions.

Each entry in `ENCLAVE_WSL_FORWARD_ENV` may carry a `WSLENV` flag suffix — `/p`
to translate a single path, `/l` a path list, `/u` and `/w` to restrict the
direction:

```powershell
$env:ENCLAVE_WSL_FORWARD_ENV = "ANTHROPIC_API_KEY,MY_SCRATCH_DIR/p"
```

A `WSLENV` you set yourself is preserved; the launcher appends to it and your
flags win for a name you already listed.

Credentials are worth being explicit about: an `ANTHROPIC_API_KEY` set in
PowerShell does **not** cross into the distribution unless you name it. The
normal path is to configure credentials inside the distribution — log in from
inside the container on the first run, or use
`~/.local/state/enclave/secrets/global.env` there. See
[Authentication & Secrets](auth.md).

## Terminal, signals, and exit codes

- Standard input, output, and error are inherited directly rather than piped, so
  the agent — and `docker attach` under it — gets a real terminal. The launcher
  does no terminal emulation.
- Ctrl-C is forwarded by `wsl.exe` to the child, which owns the decision about
  what to do with it. The launcher deliberately ignores it so it cannot exit
  before its child.
- The child's exit code is propagated unchanged.
- A launcher-level failure — no WSL2, an unusable working directory, no `enclave`
  in the distribution — exits **125**, following Docker's convention for "the
  launcher failed, not the program". Every such message is prefixed
  `enclave (windows launcher):`, so it is never mistaken for enclave's own
  output.

## How it finds the Linux binary

`make install` puts `enclave` in `~/.local/bin`, which is only on `PATH` via
`~/.profile`, and a non-login shell does not read that file. So the launcher
resolves the path first and then executes it:

1. `wsl.exe [-d <distro>] -e /bin/sh -lc '<fixed probe>'`, a login shell that
   tries `command -v enclave` and then `~/.local/bin/enclave`,
   `/usr/local/bin/enclave`, `/usr/bin/enclave`. The probe interpolates nothing.
2. `wsl.exe [-d <distro>] --cd <path> -e <absolute path> <your arguments…>`,
   which runs the binary directly with no shell to re-parse anything.

That costs one extra round trip, roughly 100–300 ms on a cold distribution. The
result is not cached. On a `wsl.exe` old enough to lack `--cd`, the launcher
retries and changes directory through a shell instead, still passing your
arguments as positional parameters so they are never re-split.

## Limitations

- No native Windows support.
- The launcher installs nothing: not WSL2, not a distribution, not Docker, not
  the Linux `enclave` build.
- No diagnostic command; failures are reported inline.
- Argument passing is verified against a reimplementation of Windows' own
  command-line parsing in CI, and against a real `wsl.exe` by a manual
  pre-release gate (see [DEV.md](DEV.md#windows-launcher)). GitHub-hosted Windows
  runners have no WSL2, so the empirical half cannot run there.
