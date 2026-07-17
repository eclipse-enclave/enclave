# Host hardening (rootful Docker)

Enclave requires a rootful Docker daemon. By default, container UID 0 maps to
host UID 0; enabling Docker user namespace remapping reduces the impact of a
container escape or accidental privileged host-file access.

Rootless Docker is not currently supported because the restricted-network
gateway requires netfilter, ipset, sysctl, `NET_ADMIN`, and `NET_RAW`. See
[Rootless Docker compatibility](rootless.md).

## Enable userns-remap

1. Ensure subordinate ID ranges exist:

```bash
sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 "$USER"
```

2. Merge `"userns-remap": "default"` into `/etc/docker/daemon.json`:

```json
{
  "userns-remap": "default"
}
```

3. Restart Docker:

```bash
sudo systemctl restart docker
```

4. Verify:

```bash
docker info --format '{{json .SecurityOptions}}'
```

The output should include `name=userns`.

## Devcontainer compatibility

User namespace remapping can conflict with Dev Container implementations that
expect host and container UIDs to match when bind-mounting workspaces, installing
extensions, or running lifecycle scripts. Test the contributor workflow before
enabling it on a machine that relies on `enclave devcontainer`.

If userns-remap is incompatible with the required devcontainer workflow, use
standard rootful Docker and treat Enclave's non-root tool user, mount controls,
and restricted-network gateway as defense in depth rather than a container
escape boundary.

Changing Docker security mode affects every local container and may require
recreating existing containers and correcting ownership of bind-mounted data.
