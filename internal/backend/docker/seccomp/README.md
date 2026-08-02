# Rootless sandbox seccomp profile

`rootless-default.json` is the seccomp profile enclave applies to tool
containers on a rootless Docker daemon. It is the Moby default profile plus the
minimum delta the nested user-namespace launcher needs (see
`docs/security/rootless.md`):

- `unshare`, argument-filtered to `CLONE_NEWUSER|CLONE_NEWNS` — the launcher
  creates exactly one user+mount namespace; other namespace types stay blocked.
- `mount`, `umount2`, `mount_setattr`, `open_tree`, `move_mount`, `fsopen`,
  `fsconfig`, `fsmount`, `fspick` — the launcher's read-only system binds. The
  kernel still requires `CAP_SYS_ADMIN` in the mount's owning user namespace,
  which processes outside the launcher's child namespace do not have.
- `setns` — the exec wrapper (`enclave-sandbox-exec`) joins the sandbox
  namespace; the kernel requires `CAP_SYS_ADMIN` in the target namespace.

## Provenance

Base: `default.json` from https://github.com/moby/profiles (vendored in
moby/moby), Apache License 2.0. The delta rules are appended at the end of the
`syscalls` array.

## Regenerating

Fetch the current Moby default profile and re-append the two delta rules (the
`unshare` rule's mask is `~(CLONE_NEWUSER|CLONE_NEWNS)` =
`0xffffffffeffdffff`). `TestRootlessSeccompProfile` in the parent package
asserts the delta survives regeneration.
