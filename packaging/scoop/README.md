# Scoop packaging

`enclave.template.json` is the [Scoop](https://scoop.sh) manifest for the Windows
launcher. The release workflows substitute `@VERSION@`, `@BASE_URL@`,
`@AMD64_HASH@`, and `@ARM64_HASH@` and publish the result as `enclave.json`
next to the archives, so it can be installed straight from the release (replace
`rolling` with a release tag such as `v1.0.0` for an official release):

```powershell
scoop install https://github.com/eclipse-enclave/enclave/releases/download/rolling/enclave.json
```

The manifest pins the archive hashes. Rolling-release assets are replaced as
`main` moves, so a manifest cached from an earlier build will fail hash
verification; download it again when that happens.

There is no Scoop bucket yet, and `winget` is deferred: it needs a pull request
into `microsoft/winget-pkgs` per release.

The archive contains `enclave.exe`, which is a launcher that forwards to the
Linux enclave binary inside WSL2. Installing it does not install enclave itself;
see [`docs/windows.md`](../../docs/windows.md).
