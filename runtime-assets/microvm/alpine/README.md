# Alpine QEMU microVM bundle

This directory contains the experimental builder for the `qemu` backend's minimal Alpine guest bundle.

The generated bundle contains:

- `vmlinuz` from Alpine `linux-virt`
- `initramfs.cpio.zst` with Alpine userland, the selected agent tool, and enclave runtime assets
- `enclave-vm-initramfs.json` with the packed and unpacked initramfs sizes
- `enclave-vm-bundle.json` with the declared guest memory floor

## Initramfs sizing

The guest has no disk: the initramfs is unpacked into a ramfs that becomes the
rootfs and is never reclaimed, and while unpacking the kernel holds the image
and its unpacked contents at the same time. Guest memory therefore scales with
the bundle: at launch the host derives it from `enclave-vm-initramfs.json`
(`backendqemu.RequiredMemoryMiB`), never below the floor in
`enclave-vm-bundle.json` (the profile's `qemuMinMemoryMiB`). Two consequences
for the builder:

- The image is zstd-compressed, which roughly halves the boot-time peak.
- Build-time caches (npm, pip, uv) and `/boot` are stripped from the rootfs.
  Anything left in it costs guest RAM byte for byte for the whole session.

The image needs no padding: the host appends a per-run overlay archive at boot
and aligns it to the 4-byte boundary the kernel's `unpack_to_rootfs()` requires
(`concatenateFiles`).

Bundles passed via `--image-name` may also ship an uncompressed `initramfs.cpio`
and either metadata file may be missing: the floor then defaults to 4096 MiB,
and without `enclave-vm-initramfs.json` it is used as is.

## Console size

The guest runs on the emulated serial console, which carries no window size:
left alone it reports 0x0, and terminal UIs then either render one column wide
or fall back to 80x24 no matter how large the host terminal is. The generated
run script therefore seeds the console with the host terminal's size
(`stty rows … cols …`, from `resolveConsoleSize`). The size is applied once at
startup; resizing the host terminal mid-session does not reach the guest,
because the console is not a controlling terminal there and no `SIGWINCH` is
delivered.

The builder is invoked by `enclave --backend qemu --allow-all-network --slim ...` and uses Docker only as a host-side packaging helper. The resulting session runs under `qemu-system-x86_64`, not Docker.

Sessions mount the same persistent store directories as the Docker backend (XDG state, via `internal/backend/hoststore`), so auth credentials, tool config, and persisted env are shared with Docker sessions of the same tool/project.
