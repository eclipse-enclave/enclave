# Backends

Which container engine or VM runs a session, how Enclave picks it, and what
differs between Docker, Podman, and the experimental QEMU backend.

| Backend | Status | Notes |
|---------|--------|-------|
| `docker` | Default | Rootful Docker with buildx. Rootless Docker is not supported; see [Rootless container engines](security/rootless.md) and [Host hardening](security/host-hardening.md) |
| `podman` | Supported | Podman's Docker-compatible CLI; rootless is the tested configuration |
| `qemu` | Experimental | Foreground sessions in an x86-64 Alpine microVM, without network restrictions |

## Choosing a backend

`backend` (key or `--backend`) is `auto` (default), `docker`, `podman`, or
`qemu`. Any explicit value disables detection.

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
