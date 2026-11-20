# Backends

Which container engine or VM runs a session, how Enclave picks it, and what
differs between Docker, Podman, and the experimental QEMU and VM backends.

| Backend | Status | Notes |
|---------|--------|-------|
| `docker` | Default | Rootful Docker with buildx. Rootless Docker is not supported; see [Rootless container engines](security/rootless.md) and [Host hardening](security/host-hardening.md) |
| `podman` | Supported | Podman's Docker-compatible CLI; rootless is the tested configuration |
| `qemu` | Experimental | Foreground sessions in an x86-64 Alpine microVM, without network restrictions |
| `vm` | Experimental | Foreground and detached sessions in an x86-64 Ubuntu VM with a persistent disk per project and tool, without network restrictions |

## Choosing a backend

`backend` (key or `--backend`) is `auto` (default), `docker`, `podman`,
`qemu`, or `vm`. Any explicit value disables detection.

### Detection

- `auto` uses docker if its CLI is on `PATH`, otherwise podman. A `docker` that
  is the `podman-docker` shim counts as podman, also when chosen explicitly
  (with a notice).
- With both installed, the first engine-using command asks once and saves
  `"backend"` in `~/.config/enclave/config.json`. Without terminals on stdin,
  stdout, and stderr, or with `--json`/`--yes`, it uses docker and a notice
  points at the key.
- With neither installed, the engine check reports it.
- Engine-free commands (`tools`, `features`, `config`, `review-target`,
  `network print`, `network diff`, `devcontainer generate`) neither detect nor
  ask.

### Selecting the engine per command

Engine-using commands take `--backend` for one invocation without changing the
saved value: `ps`, `stop`, `attach`, `exec`, `update`, `theia`/`theia-next`,
`info`, `cleanup`, `auth import`/`export`, and `network status`, `log`,
`apply`, and mutations. `network print` and `diff` do not; `config` takes it to
preview resolution.

`ps` does not merge engines: after `enclave run --backend podman`, use
`enclave ps --backend podman` and `enclave attach --backend podman <name>`.

## Podman

`--backend podman` drives the Docker backend through podman's
Docker-compatible CLI (`podman` on `PATH`): buildah builds, restricted-egress
gateway, detached sessions, `exec`/`attach`, and persistent stores all work.

### User namespaces

| Container | Namespace |
|-----------|-----------|
| Gateway sidecar | Runs as root inside a `--userns=keep-id` user namespace |
| Auth-reconcile containers | `--userns=keep-id` |
| Session container | Joins the gateway's user namespace (`--userns=container:<gateway>`) alongside its network namespace |

Bind-mounted stores therefore keep the host UID/GID under rootless podman. The
session shares the gateway's user namespace because a separate keep-id
namespace cannot mount `/sys` in a network namespace it does not own; runc
fails on that, while crun masks it by bind-mounting the host `/sys`.

Without idmapped-mount support, rootless podman copies an image into a keep-id
layer on its first start and after every rebuild. Enclave triggers this before
the gateway starts and notes waits over two seconds; for multi-gigabyte images
it takes minutes and blocks every other podman command, `enclave ps` included.

### Image builds

- Podman's local layer cache replaces `--cache-from`/inline cache; the buildx
  cache flags are ignored with a warning.
- npm, Go, and uv build caches under the agent home are dropped, because
  buildah would leave their parent directories root-owned. Apt and layer
  caching remain, so only cold rebuilds are slower.
- Tags match Docker's, stored as `localhost/enclave-<tool>:...`.
- Gateway and tool base images are fully qualified or digest-pinned, so they
  build without `unqualified-search-registries` or aliases in
  `registries.conf`. A bare short name in `--base-image` or a devcontainer
  `image` still needs one.
- `cleanup --build-cache` finds nothing; podman has no separate build cache.

### Devcontainers

Both `image` and `build.dockerfile` work; the session user (and the remote
user under `--use-remote-user`) gets the host UID. A devcontainer Dockerfile is
built as written, so put `--mount=type=cache` targets outside home directories
to avoid root-owned parents.

### Per-session networks

[Per-session networks](networking.md#per-session-docker-networks) require
netavark (CNI hosts must switch) and use `--opt isolate=strict`, since before
podman 6 / netavark 2 bridges are isolated only on request.

- With netavark older than 1.7, or if `strict` is rejected, Enclave warns and
  uses `isolate=true`, which isolates sessions from each other but not from
  non-isolated podman networks; restricted gateways keep their inbound filter.
- Each network gets a `/24` from podman's pool (no Docker pool limit). Ports
  bind `127.0.0.1` as under Docker.
- An exited session's stopped gateway stays until the session is removed.

### Details

Container inspection accepts both podman 4.x output, which renders `Entrypoint`
as one string, and the Docker-shaped arrays of podman 5.

Host AppArmor profiles can affect binaries inside rootless containers; see
[AppArmor on rootless Podman](security/rootless.md#apparmor-on-rootless-podman).

## QEMU (experimental)

`--backend qemu` runs a foreground session in a minimal Alpine microVM bundle.

| Aspect | Behavior |
|--------|----------|
| Network | Implies `--allow-all-network` and `--disable-gateway` and prints a notice that network isolation is unavailable. `--allow-domain` and `allow_domains` from config are rejected |
| Features | Implies `--slim`. `--features` is rejected; features selected in config, including unknown names, are dropped with a notice |
| Not supported yet | Detached sessions, `exec`, `attach`, restricted egress, HTTP secret release, devcontainers, non-default feature stacks |
| Listing | QEMU sessions are not listed by `enclave ps` |
| Host ports | A host port of `0` (`-p 0:3000`) is rejected |
| Stores | The same per-tool and per-project host stores as Docker sessions, so auth, tool config, and persisted env carry over when switching between `docker` and `qemu` |
| Signals | SIGINT, SIGTERM, or SIGHUP to the enclave process (including a closed terminal) kills the VM without a guest shutdown. Enclave survives the signal and still writes back file mounts and syncs credentials to the store |

`config --json` reports the forced values.

### Host requirements

The backend is x86-64 only:

- It requires `qemu-system-x86_64` and `cpio` on the host.
- The bundle builder produces an x86-64 Alpine rootfs (`--platform linux/amd64`,
  `apk --arch x86_64`) regardless of host architecture. There is no arm64 guest
  bundle.
- QEMU is launched with `-machine microvm,accel=kvm:tcg`, so only x86-64 Linux
  hosts get KVM acceleration. On arm64 hosts and on macOS the guest runs under
  TCG emulation and is correspondingly slow; use the default `docker` backend
  there.

The bundle builder uses Docker as a packaging helper; the session itself runs
under QEMU. A prebuilt bundle can be used without Docker via
`--no-rebuild --image-name /path/to/bundle`. Tool installers that assume
Debian/glibc may fail until they get dedicated microVM support.

### Memory

The guest has no disk: its root filesystem is the unpacked initramfs, held in
RAM for the whole session. Guest memory is sized per bundle at launch, at least
4096 MiB (or the tool's `qemuMinMemoryMiB`) and more for large bundles. The
host must have that much memory available; the size is not capped to host RAM.

On Linux, Enclave warns (and continues) when the allocation exceeds
`MemAvailable` from `/proc/meminfo`. It ignores cgroup limits and reserves, so
no warning does not guarantee a fit; elsewhere, or if unreadable, it skips the
check.

### Mounts

Each directory mount is a separate 9p device. Git config protection
bind-mounts staged read-only files over their guest targets; see
[Git](git.md#details-and-limits).

### Tuning variables

These host environment variables adjust the QEMU command line for
experiments:

| Variable | Default | Effect |
|----------|---------|--------|
| `ENCLAVE_QEMU_CPU` | `max` | CPU model passed to `-cpu` |
| `ENCLAVE_QEMU_TRANSPORT` | `pci` | Virtio transport for devices; `mmio` selects virtio-mmio |
| `ENCLAVE_QEMU_KERNEL_APPEND_EXTRA` | unset | Appended to the guest kernel command line after `console=ttyS0` |

## VM (experimental)

`--backend vm` runs a session in a full QEMU/KVM virtual machine: an Ubuntu
26.04 guest whose disk persists per project and tool. The project is mounted
at its host path, and the guest shuts down when the tool exits.

```bash
enclave run --backend vm                 # foreground
enclave run --backend vm --background    # detached; prints the session name
enclave ps --backend vm
enclave attach --backend vm <name>
enclave stop --backend vm <name>
```

| Aspect | Behavior |
|--------|----------|
| Network | As for QEMU: implies `--allow-all-network` and `--disable-gateway` with a notice; `--allow-domain` and `allow_domains` from config are rejected |
| Features | As for QEMU: implies `--slim`; `--features` is rejected and features selected in config are dropped with a notice |
| Not supported yet | `exec`, restricted egress, HTTP secret release, devcontainers, `--base-image` |
| Listing | `enclave ps --backend vm` lists VM sessions with status `running` or `exited`; it needs no container engine |
| Disk | One disk per project and tool, kept between sessions. A second session of the same project and tool is refused while the first VM runs, also if the enclave process that started it is gone |
| Stores | The same per-tool and per-project host stores as Docker sessions; credentials are synced to the store when the session ends |
| Host ports | A host port of `0` is rejected. Without a host IP, ports bind `127.0.0.1` |
| Signals | SIGINT, SIGTERM, or SIGHUP to the enclave process ends the session and shuts the guest down. Enclave still writes back file mounts and syncs credentials |

### Detached VM sessions

A detached session's tool runs on a terminal of its own in the guest (`dtach`),
and the VM keeps running after `enclave run` returns.

- `attach` joins that terminal. The detach key is one `ctrl-<key>`
  (`ctrl-\` by default); key sequences such as `ctrl-p,ctrl-q` are rejected.
  A session started in the foreground cannot be attached from another
  terminal.
- `stop` shuts the guest down, writes back file mounts, and syncs
  credentials. The guest gets at least 30 seconds to shut down before the VM
  is ended, whatever timeout the command uses for containers. `stop` also
  stops a foreground session of another terminal, which then finalizes
  itself.
- `stop` exits non-zero if the VM could not be stopped or the session could
  not be finalized. The session then stays listed, and `stop` can be run
  again.
- A tool that exits ends its VM. The session stays listed as `exited` under
  `ps --all` until it is stopped or its name is started again, and the next
  session of the project finalizes it first.
- A project and tool have one disk, so one VM session at a time, whatever the
  session names. A second `run` is refused before it changes any store.

### VM host requirements

The backend needs an x86-64 Linux host with KVM: `/dev/kvm` must be accessible
to the user, and there is no emulation fallback. It uses these host tools and
never Docker:

- `qemu-system-x86_64` and `qemu-img`
- `virtiofsd`, on `PATH` or in `/usr/libexec`, `/usr/lib`, or `/usr/lib/qemu`
- `ssh` and `ssh-keygen`
- `cloud-localds`, `genisoimage`, or `xorriso`, only to build a base image

### Base image and disk

The first run for a tool downloads a pinned Ubuntu cloud image, checks its
SHA-256, boots it once, and lets the guest install the tool. This takes several
minutes and needs internet access from the guest. The result is a base image
under `~/.local/state/enclave/vm/bases/<tool>/<hash>/`.

Each project and tool gets a qcow2 disk in
`~/.local/state/enclave/projects/<project-hash>/<tool>/vm/`. It is an overlay
on the base it was created on and keeps what the guest changes outside the
mounts, such as installed packages and files in the guest home.

- A base is never rebuilt in place, because the disks created on it depend on
  its content. `--rebuild` is ignored with a warning.
- Changed inputs (the pinned cloud image, image build inputs, provisioning
  scripts, host UID or GID, disk size) produce a new base for new disks. A
  project with a disk stays on that disk's base, with a notice, and needs no
  other base: it also restarts under `--no-rebuild`.
- Automatic tool updates do not reach an existing disk. The tool has the
  version installed in the base unless it updates itself in the guest.
- To start over, remove the project's `vm` directory while no session uses it.
  `enclave cleanup --all` refuses to run while a VM session uses a disk it
  would remove.
- `--image-name <dir>` uses a prebuilt base directory.

### VM mounts

Each directory mount and store is a virtiofs export served by its own
unprivileged `virtiofsd`. The guest's tool user has the host UID and GID.

- Read-only directories, among them the one that carries the guest's session
  scripts and keys, are enforced on the host: by `virtiofsd --readonly` where
  the installed version has it, otherwise through 9p with `readonly=on`.
- A file mount is a staged copy bound over its target, so its content never
  lands on the guest's disk. A writable one is written back when the session
  ends, unless the host file was edited or replaced in the meantime. Enclave
  then leaves the host file alone, keeps the guest's version under
  `~/.local/state/enclave/vm/kept/<session>-<id>/`, and reports it as an
  error.
- If the guest's version can be neither written back nor kept, the session
  stays as it is. `enclave stop --backend vm <name>` retries, and the project
  cannot start another VM session until that has succeeded.
- Directories the guest lacks above a mount point are created for the tool
  user. Existing guest directories keep their owner. A symbolic link in the
  guest anywhere on the path of a mount point fails the session start.
- A mount the guest cannot establish fails the session start.

### Session terminal

The tool runs on an SSH connection to a port forwarded on `127.0.0.1`, with a
client key and a guest host key generated for the session. The user's SSH
configuration, keys, and agent are not used. The command and its environment
reach the guest in files of a private runtime directory, removed once the tool
has started.

### VM tuning variables

| Variable | Default | Effect |
|----------|---------|--------|
| `ENCLAVE_VM_MEMORY_MIB` | `4096` | Guest memory in MiB |
| `ENCLAVE_VM_CPUS` | Host CPUs, at most `4` | Guest CPUs |
| `ENCLAVE_VM_DISK_SIZE` | `40G` | Virtual size of a new base image and the disks created on it |
| `ENCLAVE_VM_BOOT_TIMEOUT` | `180` | Seconds to wait for the guest to report its mounts |
| `ENCLAVE_VM_VIRTIOFSD` | unset | Path of the `virtiofsd` binary |
