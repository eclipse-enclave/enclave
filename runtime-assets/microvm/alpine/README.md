# Alpine QEMU microVM bundle

This directory contains the experimental builder for the `qemu` backend's minimal Alpine guest bundle.

The generated bundle contains:

- `vmlinuz` from Alpine `linux-virt`
- `initramfs.cpio` with Alpine userland, the selected agent tool, and enclave runtime assets
- `enclave-vm-bundle.json` with guest sizing metadata

The builder is invoked by `enclave --backend qemu --allow-all-network --slim ...` and uses Docker only as a host-side packaging helper. The resulting session runs under `qemu-system-x86_64`, not Docker.

Sessions mount the same persistent store directories as the Docker backend (XDG state, via `internal/backend/hoststore`), so auth credentials, tool config, and persisted env are shared with Docker sessions of the same tool/project.
