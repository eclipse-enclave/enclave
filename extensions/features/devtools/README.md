# devtools

Core development tools and lint tooling. Enabled by default.

**Priority**: 40

## Packages

| Package | Purpose |
|---------|---------|
| `vim` | Text editor |
| `nano` | Text editor |
| `htop` | Interactive process viewer |
| `tree` | Directory listing |
| `golang-go` | Go compiler |
| `rpm` | RPM package build tools (`rpmbuild`) |
| `shellcheck` | Shell script linter |
| `yq` | YAML processor |
| `ripgrep` | Fast grep (`rg`) |
| `fd-find` | Fast find (`fdfind`) |
| `netcat-openbsd` | Network utility |
| `socat` | Multipurpose relay |
| `dnsutils` | DNS tools (`dig`, `nslookup`) |
| `iputils-ping` | `ping` |
| `xxd` | Hex dump |

## Installed via `install.sh`

- `golangci-lint` (`v2.10.1`), from the upstream release archive
- `gosec` (`v2.23.0`), from the upstream release archive
- `govulncheck` (`v1.1.4`), built with the distro Go via `go install`

golangci-lint and gosec are downloaded as prebuilt binaries and verified against
the release checksum list, so the install compiles nothing and does not depend
on the Go version. govulncheck has no binary release. The install runs with
`GOTOOLCHAIN=local`, so a linter version that needs a newer Go than the image
ships fails loudly instead of downloading a second toolchain.

## Go version

The image ships Debian's `golang-go` package (Go 1.24 on trixie), which matches
the `go` directive in this repository's `go.mod`. Go and the linters are in the
default feature set so a contributor can run `make build`, `make test`, and
`make lint` in a session without installing anything. The CI lint job runs the
same `install.sh`.

Version overrides are supported via environment variables:

- `GOLANGCI_LINT_VERSION`
- `GOSEC_VERSION`
- `GOVULNCHECK_VERSION`
