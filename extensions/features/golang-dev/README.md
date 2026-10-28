# golang-dev

Go toolchain and debugger from Debian. Opt-in (disabled by default).

**Priority**: 45

## Packages

| Package | Purpose |
|---------|---------|
| `golang-go` | Go compiler and tools (`go`) |
| `delve` | Go debugger (`dlv`) |

Debian's Go can be older than the Go versions upstream still supports. It
works as a bootstrap: with the default `GOTOOLCHAIN=auto`, `go` downloads the
toolchain a project's `go.mod` asks for. The base allowlist includes the Go
module proxy and toolchain hosts (`go.conf`).
