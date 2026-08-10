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

That single env var selects the instance for `glab`, retargets token injection
at it, and joins the network allowlist — no spec edit or `allow_domains` entry.
The tokens then go only to that instance, while `gitlab.com` stays reachable.
See `serviceAuth.hostsFromCredential` in `docs/extensions/README.md` for the
accepted value forms.

`GITLAB_HOST` applies to the run that sets it and is not written to the
persisted env store. To make it permanent, put it in a secrets file — globally
in `~/.local/state/enclave/secrets/global.env`, or per project in
`~/.local/state/enclave/secrets/projects/<hash>/<tool>.env`, which is keyed by
the agent tool (e.g. `claude.env`), not by this feature.
