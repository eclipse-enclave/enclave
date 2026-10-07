# native-build-deps

System headers that native Node.js modules link against at `npm install` time.
The base image already ships the compiler toolchain (`build-essential`,
`pkg-config`, `python3`); this feature adds the libraries some modules probe
for with `pkg-config`, for example `native-keymap` and `keytar` in Eclipse
Theia and other Electron apps. Enabled by default.

**Priority**: 45

## Packages

| Package | Purpose |
|---------|---------|
| `libx11-dev` | X11 client library headers |
| `libxkbfile-dev` | XKB keyboard file library headers |
| `libsecret-1-dev` | Secret Service API headers (`keytar`) |
