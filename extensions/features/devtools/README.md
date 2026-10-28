# devtools

General command-line and network tools. Enabled by default.

**Priority**: 40

## Packages

| Package | Purpose |
|---------|---------|
| `vim` | Text editor |
| `nano` | Text editor |
| `htop` | Interactive process viewer |
| `tree` | Directory listing |
| `shellcheck` | Shell script linter |
| `ripgrep` | Fast grep (`rg`) |
| `fd-find` | Fast find (`fdfind`) |
| `netcat-openbsd` | Network utility |
| `dnsutils` | DNS tools (`dig`, `nslookup`) |
| `iputils-ping` | `ping` |
| `xxd` | Hex dump |

The base image already provides `socat` and mikefarah `yq` v4. Go is in the
opt-in `golang-dev` feature.
