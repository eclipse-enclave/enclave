# Host hardening (rootful Docker)

Optional hardening for hosts running Enclave on rootful Docker, the default
backend. By default, container UID 0 maps to host UID 0; Docker user namespace
remapping reduces the impact of a container escape or of accidental privileged
access to host files.

Rootless Podman (`--backend podman`) avoids the root daemon; rootless Docker is
not supported. See [Rootless container engines](rootless.md).

## Preserve Docker firewall integration

Enclave puts each Docker session on its own user-defined bridge. The session
boundary and host-loopback port publishing rely on the firewall rules Docker
manages.

> [!WARNING]
> Do not disable the daemon's `iptables` or `ip6tables` integration unless you
> maintain equivalent isolation and publishing rules yourself. Configurations
> such as `"iptables": false` are outside Enclave's supported security posture.

Under `--backend podman`, netavark manages the equivalent rules inside the
rootless network namespace, and this section does not apply.

Releases that put sessions on Docker's default bridge had an optional
`"icc": false` mitigation; see [Upgrade notes](../install.md#upgrade-notes).

## Enable userns-remap

1. Merge `"userns-remap": "default"` into `/etc/docker/daemon.json`:

   ```json
   {
     "userns-remap": "default"
   }
   ```

2. Restart Docker, which creates the `dockremap` user:

   ```bash
   sudo systemctl restart docker
   ```

3. Check that `dockremap` has subordinate ID ranges. Some distributions do not
   add them, and Docker then fails to start; add a range that does not overlap
   existing entries and restart again:

   ```bash
   grep dockremap /etc/subuid /etc/subgid
   sudo usermod --add-subuids 231072-296607 --add-subgids 231072-296607 dockremap
   sudo systemctl restart docker
   ```

4. Verify. The output should include `name=userns`:

   ```bash
   docker info --format '{{json .SecurityOptions}}'
   ```

Remapping affects every local container. Existing containers may need to be
recreated, and ownership of bind-mounted data corrected.

## Devcontainer compatibility

User namespace remapping can conflict with Dev Container implementations that
expect host and container UIDs to match when they bind-mount workspaces,
install extensions, or run lifecycle scripts. Test the contributor workflow
before enabling it on a machine that relies on `enclave devcontainer`.

If userns-remap is incompatible with the required devcontainer workflow, use
standard rootful Docker. Enclave's non-root tool user, mount controls, and
restricted-network gateway are then defense in depth, not a container-escape
boundary.
