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

- `golangci-lint` (`v2.15.0`)
- `gosec` (`v2.30.0`)
- `govulncheck` (`v1.9.0`)

Version overrides are supported via environment variables:

- `GOLANGCI_LINT_VERSION`
- `GOSEC_VERSION`
- `GOVULNCHECK_VERSION`
