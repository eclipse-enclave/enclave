# theia-dev

Headers for building Eclipse Theia and other Electron apps in the container.
Their native Node.js modules, such as `native-keymap` and `keytar`, probe for
these libraries with `pkg-config` at `npm install` time. The base image already
ships the compiler toolchain (`build-essential`, `pkg-config`, `python3`).
Opt-in (disabled by default). Formerly `native-build-deps`.

To enable it only for the Theia tools, add this to
`~/.config/enclave/config.json`:

```json
{
  "tool_overrides": {
    "theia": { "features": ["+theia-dev"] },
    "theia-next": { "features": ["+theia-dev"] }
  }
}
```

**Priority**: 75

## Packages

| Package | Purpose |
|---------|---------|
| `libx11-dev` | X11 client library headers |
| `libxkbfile-dev` | XKB keyboard file library headers (`native-keymap`) |
| `libsecret-1-dev` | Secret Service API headers (`keytar`) |
