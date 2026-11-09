# Dependencies

The direct Go module dependencies declared in `go.mod` and what they are used
for. Indirect dependencies are pulled in transitively and are not listed.

For the Dash license-check inventory of Go and website dependencies, see the
repository-root [DEPENDENCIES](../DEPENDENCIES) file.

## Direct Go dependencies

| Module | Version | Purpose |
|--------|---------|---------|
| `github.com/BurntSushi/toml` | `v1.6.0` | TOML parsing and encoding for host-side config patch merging |
| `github.com/moby/patternmatcher` | `v0.6.1` | `.dockerignore` parsing (`ignorefile`) for build-input hashing |
| `github.com/spf13/cobra` | `v1.10.2` | CLI framework for command parsing |
| `github.com/spf13/pflag` | `v1.0.10` | Flag parsing library used by Cobra |
| `github.com/tidwall/jsonc` | `v0.3.2` | JSONC preprocessing (comments, trailing commas) for devcontainer config parsing |
| `golang.org/x/crypto` | `v0.48.0` | TLS and crypto primitives for the network gateway's MITM proxy |
| `golang.org/x/sys` | `v0.41.0` | Unix and Windows system calls: terminal handling, file locks, filesystem checks, and the Windows launcher's drive-letter queries |
| `golang.org/x/term` | `v0.40.0` | Terminal and TTY detection for colored output, prompts, and raw-mode console handling |
| `sigs.k8s.io/yaml` | `v1.6.0` | YAML/JSON extension manifest decoding |

`go.mod` is the source of truth for versions; update this table when a direct
dependency changes. `make check-versions` reports available updates (see
[DEV.md](DEV.md#dependency-updates)).

## Runtime tools

These are not Go module dependencies, but Enclave needs them on the host:

| Tool | Used by |
|------|---------|
| `docker` or `podman` CLI, with a reachable engine | Container backends. Enclave shells out to the CLI rather than linking the Docker Engine SDK |
| `qemu-system-x86_64`, `cpio` | The experimental `qemu` backend |

The `qemu` backend's current Alpine bundle builder also uses Docker as a
packaging helper. Prebuilt bundles can run without Docker using
`--no-rebuild --image-name /path/to/bundle`.
