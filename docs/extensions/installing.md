# Installing extensions from git

`enclave tools|features|commands` install, update, and remove extensions from
git repositories into `~/.config/enclave/extensions/`. The official repository,
[enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions),
holds the tools and features that are not built in:

```bash
enclave tools add eclipse-enclave/enclave-extensions --name <tool>
enclave features add eclipse-enclave/enclave-extensions --name <feature>
```

## Commands

| Command | Description |
|---------|-------------|
| `enclave tools\|features\|commands list` | List extensions (built-in and installed), with source and provenance |
| `enclave tools\|features\|commands add <source>` | Install an extension from a git repository |
| `enclave tools\|features\|commands update [<name\|source>...]` | Refresh installed extensions from their recorded source |
| `enclave tools\|features\|commands remove <name>...` | Remove installed extensions |

| Flag | Verbs | Effect |
|------|-------|--------|
| `--name <name>` | `add` | Install only the named extension; repeatable |
| `--all` | `add` | Install every match |
| `--path <subpath>` | `add` | Directory inside the source |
| `--ref <ref>` | `add`, `update` | Branch, tag, or commit |
| `--dry-run` | `add`, `update` | Fetch, stage, and validate, but write nothing |
| `--yes`, `-y` | `add`, `update`, `remove` | Skip the confirmation prompt (the summary is still shown) |
| `--force` | `add`, `update`, `remove` | Override collision policy and local-edit protection |
| `--json` | all | Machine-readable output. For `add`, `update`, and `remove` it implies non-interactive and requires `--yes`. See [JSON output](json-output.md). |

`features list` also accepts `--tool`, `--features`, and `--slim` and reports
which features a run with them would enable. `tools list` accepts `--tool` to
mark the selected tool.

- With no subcommand, each is an alias for `list`. `enclave commands` manages
  [command extensions](README.md#command-extensions).
- `update` here refreshes sources on the host. The unrelated top-level
  `enclave update` rebuilds images. A changed extension changes its content
  hash, which rebuilds the image on the next run.

## Sources

| Form | Example |
|------|---------|
| `owner/repo` shorthand | `acme/kits` (assumes `https://github.com`) |
| Shorthand with subpath | `acme/kits/extensions/features/github-cli` |
| https, http, or git URL | `https://gitlab.com/acme/kits` |
| Forge tree URL | `https://github.com/acme/kits/tree/v1.2.0/extensions/tools/foo` |
| ssh or scp-style remote | `git@github.com:acme/kits.git`, `ssh://git@host/acme/kits.git` |
| Local path | `./my-kits`, `~/kits`, `/abs/path/to/kits` |

- A trailing `.git` is stripped from shorthands and plain URLs.
- A tree URL (`/tree/<ref>/...`, GitLab `/-/tree/<ref>/...`) supplies ref and
  subpath; a conflicting `--ref` or an extra `--path` is rejected.
- The shorthand is exactly two segments, the rest is the subpath. For a GitLab
  subgroup (`group/subgroup/project`), use the clone URL plus `--path`.

## Discovery

The verb fixes the kind (`tools`: `sandbox`, `features`: `mixin`, `commands`:
`command`). Without a subpath, the repository is scanned up to 8 directories
deep for specs whose `kind` matches and whose `name` matches the directory;
mismatches and unresolvable names are reported as skips. A root-level spec has
no directory to match, but its `name` must not contain a path separator or
`..`.

| Situation | Result |
|-----------|--------|
| Exactly one match | Installed without asking |
| Several matches, no `--name` or `--all` | Listed and confirmed interactively; an error with `--yes` or `--json` |
| `--name <name>` | Only the named extensions |
| `--all` | Every match |
| Two matches in different directories with the same name | Error naming both directories, even with `--all`; disambiguate with `--path`. Only names being installed are checked. |
| Extensions of other kinds only | Error counting them per kind and naming the right verb |
| Feature that ships only host commands | Installs, with a warning pointing at `kind: command` and `enclave commands add` |

`--name` and `--all` are mutually exclusive, and so are `--path` and `--name`:
one addresses a directory inside the source, the other filters discovery
results within it.

## Trust

An installed extension is unreviewed code that runs at container build and
start time.

> [!WARNING]
> [Host commands](../commands.md#commands-from-extensions) are the only
> capability that runs outside a container, on your host as your user. The
> capability summary lists them in its first row after the source, naming each
> verb. Treat that row as the decision point.

### Capability summary

Before writing anything, `add` and `update` summarize what the staged content
declares and ships. Scripts are reported by count and name only; review their
code yourself.

| Capability | Source in the extension |
|------------|-------------------------|
| Runs commands on the host | `commands/host/*` |
| Needs root to install | `needsRoot` |
| Runs an install or startup script | `install.sh`, `commands.install`, `commands.startup` |
| Install steps running as root | `commands.install` entries without `user` (default root, independent of `needsRoot`) |
| Runs an update probe in a container on each automatic update check | `check-update.sh` |
| Widens the network allowlist or denies domains | `network.allowedDomains`, `network.deniedDomains` |
| Publishes a container port on the host | `ports` |
| Declares per-project cache mounts in the container home | `caches` |
| Declares credentials and where they are released as HTTP headers | `credentials`, `network.serviceAuth` |
| Turns on the approval-bypass flag (active by default once declared) | `sandbox.yoloFlag` |
| Appends its own argv for continue, resume, or no-memory (can carry the same flag) | `sandbox.continueArgs`, `resumeArgs`, `noMemoryArgs` |
| Keeps a writable agent-memory directory between sessions, and at what scope | `sandbox.memoryDir`, `memoryScope` |
| Pins store paths the config overlay may not replace | `sandbox.statePaths` |
| Launches an IDE on your host after start | `postStart.openIDE` |
| Ships skills | `skills/` |
| Host config or credential passthrough | `sandbox.passthroughPaths`, `hostConfigDir`, `hostCredentialsFile`, `hostOauthJson` |
| Seeds files into your project directory | `commands.initFiles`, `files/workspace/**` |

The summary also lists environment variables, proxy-managed aliases,
providers, credential files, `gateway-allowlist.conf` directives, apt packages,
`files/home/**`, feature `configDir` and `authFiles`, whether the extension
shadows a built-in, an ignored `go/` directory, and the content size.

An update shows a diff against the installed version, so new capabilities are
visible before you accept, and a gained host command repeats the warning. The
diff also catches changes that keep the shape: `commands.install` and
`commands.startup` by command text, `environment.variables` by value, and
`commands.initFiles` by mode and content digest.

### Auditing host commands

Under `--json`, the `add`/`update`/`remove` envelope carries host commands in
`hostCommands`. `list` shows them per extension as a `[host: ...]` suffix, or
`hostCommands` with `--json`. They resolve whenever an extension is installed,
enabled or not. `hostCommands` includes executables whose name a built-in or
your own command already takes; the summary omits those, and `add` warns.

### Prompts, credentials, and `--force`

- Under `--json`, git may not prompt for credentials, on the terminal or via
  askpass (including a scripted `GIT_ASKPASS`), so a private HTTPS remote fails
  with git's message. Use a credential helper. An ssh passphrase prompt can
  still appear.
- `--force` replaces an installed, hand-edited, or unmanaged extension, or one
  that shadows a built-in. It never overrides a validation error: a spec that
  fails to load or violates the [limits](#limits) blocks the install.

## Provenance

A successful install writes `.enclave-source.json` into the installed
extension directory:

| Field | Meaning |
|-------|---------|
| `remote`, `source`, `subpath` | Redacted remote URL, the source string as given (also redacted), and the repository subpath |
| `ref`, `refType` | The requested ref and whether it is a `branch`, `tag`, or `commit` |
| `commit` | The exact commit installed. For an annotated tag, the commit it points at, never the tag object's id. |
| `installedAt`, `installedBy` | Install timestamp and the enclave version that performed it |
| `treeHash` | A digest of the installed files, used to detect local edits |

- Without `--ref`, the install follows the remote HEAD's branch; a detached
  HEAD is recorded as a commit pin.
- The sidecar is excluded from the build context and image hash, so re-pinning
  identical content does not rebuild and prints no rebuild hint.
- Hand edits change the tree hash; `update` then refuses until `--force`, which
  discards them.

| Sidecar state | Meaning | `update` | `remove` |
|---------------|---------|----------|----------|
| Present | Managed install | Updates | Removes |
| Missing | Unmanaged: placed by hand or predates the installer ("not installed by enclave") | Skips; `--force` does not invent a source; re-add it to manage it | Requires `--force` |
| Unparseable | "its provenance file could not be read" | Skips; re-add it to manage it | Requires `--force` |

`remove` and `update` enumerate directories, not parsed specs, so an extension
whose spec no longer loads (for example after a `schemaVersion` bump) stays
manageable.

## Update semantics

With no arguments, `update` targets every extension of that kind installed from
a git source; built-ins and unmanaged directories are skipped.

An argument containing `/`, `:`, or `~` (or a lone `.`) is read as a source, in
any form `add` accepts, and targets every extension of that kind installed from
it:

```sh
enclave features update acme/kits
enclave features update acme/kits/extensions/features   # only extensions under this path
enclave features update https://gitlab.com/acme/kits/-/tree/main/extensions/features
```

- A source matches when its remote equals the recorded one as written,
  credentials removed (`acme/kits` does not match `git@github.com:acme/kits`),
  and the recorded subpath is at or below the source's.
- Only shorthands and tree URLs carry a subpath; other sources select the whole
  repository. A tree URL also matches only its ref, so the `add` string works
  unchanged.
- Names and sources can be mixed; a source matching nothing is an error.
- `--ref <ref>` moves one extension to another ref; selecting more than one is
  an error.

For each target:

| Pin | What `update` does |
|-----|--------------------|
| Commit, no `--ref` or `--force` | Nothing to check, no network call. Local edits still block with an error asking for `--force`. |
| Branch or tag | One `git ls-remote` resolves the ref. If it matches the recorded commit and there are no local edits, prints "up to date" and stops. |
| Otherwise | Fetches, stages, and validates like `add`; shows the changed files (capped with a count) and the capability diff; swaps after confirmation, or immediately with `--yes`. |

Targets that share a remote and a ref, such as several extensions from one kit
repository, are resolved and fetched once for the whole run.

## Staging and recovery

`add` and `update` stage content in `.incoming-*` inside the destination
directory (so the rename stays on one filesystem). After validation and
confirmation, existing content moves to `.replaced-*`, the staged directory is
renamed into place, and the old copy is deleted. Both are dot-directories,
invisible to listing and validation, and leftovers are swept after 24 hours.

> [!WARNING]
> `.replaced-*` is only a recovery window. If a process is killed between the
> two renames, the previous content is recoverable from that directory until
> the next sweep, at most 24 hours later.

`--dry-run` stages in the system temp directory and writes nothing, not even
the user extension root.

## Remove semantics

`remove` only touches user-installed extensions:

- A built-in is never removable.
- Removing a user extension that overlaid a built-in of the same name
  reactivates the built-in, and the output says so.
- An extension Enclave did not install (no readable sidecar) needs `--force`.
- Host state (patches, persistent stores, auth) is kept for a reinstall; the
  output names it.
- Host commands go with the directory; nothing was copied into
  `~/.config/enclave/commands/`.

## Requirements

`git` must be on `PATH`; there is no other transport.

A fetch is blobless, shallow, and single-ref (`--filter=blob:none --depth 1`),
with a sparse checkout of the installed directories. Each step falls back
independently on failure, without probing the server version:

| Server limitation | Fallback |
|-------------------|----------|
| Ignores or rejects the object filter | Full shallow fetch |
| Checkout cannot go sparse | Full checkout of the fetched commit |
| Refuses to fetch a bare commit SHA | Fetch the default branch and check out the pinned commit from it |

Submodules are never fetched or checked out. A repository whose root is itself
an extension is checked out in full.

## Limits

- Names must match `^[a-z0-9][a-z0-9._-]*$`, since a name becomes a directory,
  a build-context path, and part of the generated Dockerfile.
- User-global root only; project-local extensions are never loaded
  ([Extension sources](README.md#extension-sources)).
- At most 1000 files and 10 MiB. Symlinks, other non-regular files, and paths
  escaping the destination are rejected by name. The spec is read first, under
  the same rules plus a 1 MiB cap; a symlinked spec is reported as unusable.
- A `go/` directory is copied but never compiled in
  ([Go code](README.md#go-code-tools-only)).
- No registry, index, or signature verification: trust is whatever you extend
  to the git repository you name.
