# gitlab-cli

GitLab CLI (`glab`) for interacting with GitLab repositories, issues, and merge
requests from the command line. Opt-in (disabled by default).

**Priority**: 50

## Installation

Downloads the latest `.deb` release from GitLab's release API. Requires root.

## Auth

`GITLAB_TOKEN`/`GITLAB_ACCESS_TOKEN`, `OAUTH_TOKEN` and
`JOB_TOKEN`/`CI_JOB_TOKEN` are resolved from the host environment, the layered
secrets files, or the persisted env store, and injected as env vars. `spec.yaml`
maps each to the header the gateway injects.

## Self-hosted instances

Set `GITLAB_HOST` to point `glab` at a self-hosted instance:

```bash
GITLAB_HOST=gitlab.example.com enclave --features +gitlab-cli
```

That single env var selects the instance for `glab`, retargets token injection,
and joins the network allowlist — no spec edit or `allow_domains` entry. See
`serviceAuth.hostsFromCredential` in `docs/extensions/README.md` for the
accepted value forms and why the host replaces the `gitlab.com` defaults instead
of adding to them.

To set it persistently, put it in a secrets file rather than the shell — global
in `~/.local/state/enclave/secrets/global.env`, or per project under
`~/.local/state/enclave/projects/<hash>/`.
