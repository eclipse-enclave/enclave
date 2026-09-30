# Dependencies

This document lists the **direct** Go module dependencies declared in `go.mod`.
Indirect dependencies are pulled in transitively and are not listed here.

## Direct Go Dependencies

| Module | Version | Purpose |
|--------|---------|---------|
| `github.com/BurntSushi/toml` | `v1.6.0` | TOML parsing/encoding for host-side config patch merge support. |
| `github.com/docker/sandbox-kit-spec/v3` | `v3.0.0-m.7` | Strict sbx v3 descriptor grammar and validation (requires Go 1.26.8). |
| `github.com/moby/patternmatcher` | `v0.6.1` | `.dockerignore` parsing (`ignorefile`) for build-input hashing. |
| `github.com/spf13/cobra` | `v1.8.0` | CLI framework for command parsing. |
| `github.com/spf13/pflag` | `v1.0.5` | Flag parsing library used by Cobra. |
| `github.com/tidwall/jsonc` | `v0.3.2` | JSONC preprocessing (comments/trailing commas) for devcontainer config parsing. |
| `golang.org/x/crypto` | `v0.57.0` | TLS/crypto primitives for the network gateway's MITM proxy. |
| `golang.org/x/sys` | `v0.48.0` | Platform filesystem and system calls. |
| `golang.org/x/term` | `v0.46.0` | Terminal/TTY detection for colored output. |
| `gopkg.in/yaml.v3` | `v3.0.1` | Enforce single-document v3 manifests before shell consumption. |
| `sigs.k8s.io/yaml` | `v1.6.0` | YAML/JSON legacy manifests and host configuration. |

## Notes

- enclave shells out to the `docker` CLI at runtime rather than linking the
  Docker Engine SDK. The `docker` binary must be on `PATH` with a reachable
  daemon — this is a runtime requirement, not a Go module dependency.
- The experimental `qemu` backend shells out to `qemu-system-x86_64` and `cpio`.
  Its current Alpine bundle builder also uses Docker as a packaging helper; prebuilt
  bundles can run without Docker using `--no-rebuild --image-name /path/to/bundle`.
