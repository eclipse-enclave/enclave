# VM backend guest scripts

Guest-side scripts of the experimental `vm` backend, which runs a session in
an Ubuntu cloud guest under QEMU/KVM. The host side is `internal/backend/vm`;
user documentation is in `docs/backends.md`.

## `base/`

Baked into a base image when it is provisioned. Their content is part of the
base key, so a change here produces a new base for new project disks.

- `provision.sh` runs once, as root, from cloud-init in a pristine cloud
  image. It installs the Dockerfile's system and tool-base packages, the
  pinned yq, Node of the Dockerfile's major line, the `agent` user with the
  host UID and GID, and the selected tool, in the order the Alpine bundle
  builder does. It ends by disabling cloud-init and removing the SSH host keys
  and machine id.
- `enclave-vm-bootstrap` and `enclave-vm-boot.service` are the whole host
  protocol a base image knows: mount the `enclave-in` export, through virtiofs
  or 9p, and run its `boot` script.

## `session/`

Written into the control export for every boot, so a disk created by an older
enclave still speaks the current host's protocol.

- `boot` mounts the exports listed in `mounts.tsv`, binds the staged files of
  `files.tsv` over their targets, installs the session's SSH keys, and reports
  `ready` or `failed: <reason>` in the `enclave-out` export.
- `session` is the command the host runs over SSH. It reads the launch payload
  (`argv`, `env`, `workdir`, `user`, `loopback-ports`), runs the tool, reports
  the exit code, and powers the guest off. With `detached` it runs itself
  under `dtach`, which gives the tool a terminal that outlives the connection;
  with `attach <key>` it joins that terminal.

## What the host cannot assume

`virtiofsd` runs unprivileged on the host and serves every guest user with the
host user's rights. It cannot create a file for another user, so everything
the guest creates inside an export is created as `agent`, never as root.
