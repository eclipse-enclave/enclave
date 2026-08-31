# Installing Extensions from Git

`enclave tools` and `enclave features` install, update, and remove extensions
from git repositories into the user-global extension root
(`~/.config/enclave/extensions/`). This document covers that installer; see
[Extension Architecture](README.md) for the spec format itself.

## Commands

| Command | Description |
|---------|-------------|
| `enclave features list` / `enclave tools list` | List extensions (built-in and installed), with source and provenance |
| `enclave features add <source>` / `enclave tools add <source>` | Install an extension from a git repository |
| `enclave features update [<name>...]` / `enclave tools update [<name>...]` | Refresh installed extensions from their recorded source |
| `enclave features remove <name>...` / `enclave tools remove <name>...` | Remove installed extensions |

`enclave features` and `enclave tools` with no subcommand are aliases for
`list`. `enclave features update`/`enclave tools update` refresh extension
**sources** on the host; they do not touch images. The unrelated top-level
`enclave update` rebuilds container **images** from whatever extensions are
currently on disk. Installing or updating an extension changes its content
hash, which is what actually triggers a rebuild on the next run.

## Sources

| Form | Example |
|------|---------|
| `owner/repo` shorthand | `acme/kits` (assumes `https://github.com`) |
| shorthand with subpath | `acme/kits/extensions/features/github-cli` |
| https/http/git URL | `https://gitlab.com/acme/kits` |
| forge tree URL | `https://github.com/acme/kits/tree/v1.2.0/extensions/tools/foo` |
| ssh/scp-style remote | `git@github.com:acme/kits.git`, `ssh://git@host/acme/kits.git` |
| local path | `./my-kits`, `~/kits`, `/abs/path/to/kits` |

A trailing `.git` is stripped from shorthands and plain URLs. A forge tree URL
(`/tree/<ref>/...`, including GitLab's `/-/tree/<ref>/...`) supplies both the
ref and the subpath; a conflicting `--ref` is rejected rather than silently
overriding it, and a `--path` is rejected when the source string already
names one.

The `owner/repo` shorthand is exactly two segments: everything after the
second segment is taken as the subpath. This makes it ambiguous for GitLab
subgroups (`group/subgroup/project` would parse `subgroup` as the repo name
and `project` as a subpath). For a nested-group repository, use the full
clone URL instead, and pass `--path` to name the extension directory inside
it — the escape hatch for exactly this case.

## Discovery

The verb fixes the kind: `enclave tools add` looks only for `kind: sandbox`
extensions, `enclave features add` only for `kind: mixin`. With no subpath in
the source, the whole repository (up to 8 directories deep) is scanned for
spec documents (`spec.yaml`/`spec.json`) whose `kind` matches and whose spec
`name` matches their containing directory's name — a mismatch, or a name the
loader could never resolve, is reported as a skip rather than silently
ignored. A spec at the repository root is not checked against a directory
name (there is none); its declared `name` must still not contain a path
separator or `..`, since nothing else stands between an untrusted spec value
and a destination path.

- Exactly one match: installed without asking.
- Several matches, no `--name`/`--all`: listed and, interactively, confirmed
  with a prompt; non-interactively (`--yes`/`--json`) this is an error —
  `add` never guesses which ones you meant.
- `--name <name>` (repeatable) installs only the named extension(s); `--all`
  installs every match. They are mutually exclusive, and so are `--path` and
  `--name`: one addresses a directory inside the source, the other filters
  discovery results within it.
- Two matched candidates in different directories that declare the *same*
  name are a hard error naming both directories, even under `--all` — there
  is no way to install both into the same destination directory, so you
  disambiguate with `--path` up front rather than have one silently win. Only
  the names being installed are checked: `--name foo` still errors on two
  `foo` directories, but a duplicate pair you did not ask for is not your
  problem.
- If the source has extensions of the *other* kind but none of the requested
  one, the error names the right verb (`enclave features add` when you ran
  `enclave tools add` against a features-only repo).

## Trust

An extension is unreviewed code and configuration that, once installed, can
run at container build and start time. Before writing anything, `add` and
`update` show a capability summary distilled from the staged content itself
(never from claims elsewhere): whether it needs root to install, runs an
install/startup script (by count and script name, never full command text —
this is a capability summary, not a code audit), how many `commands.install`
steps run as root (a step with no `user` field defaults to root, independently
of the top-level `needsRoot`), whether it ships a `check-update.sh` that enclave
runs in a container on each automatic update check, widens the network allowlist
or denies domains, declares credentials and where they're released as HTTP
headers, whether it flips on the approval-bypass flag (`sandbox.yoloFlag`,
active by default once declared), ships skills, host config/credential
passthrough, or seeds files into your project directory. An update shows the
same information as a diff against what's currently installed, so a newly
granted capability is visible before you accept it.

`--yes` skips the confirmation prompt after the summary is shown; it never
skips the summary itself. `--json` implies non-interactive and requires
`--yes` (it has no way to prompt).

Under `--json`, git may not ask for credentials either, on the terminal or
through an askpass program: a private HTTPS remote fails with git's own
message rather than blocking on a question nobody sees. That covers a
scripted `GIT_ASKPASS` too, since git offers no way to tell one from a
dialog, so an unattended install wants a credential helper. Helpers keep
working, and an ssh key passphrase is ssh's own prompt, which can still
appear.

`--force` overrides collision policy —
replacing an already-installed extension, one that was hand-edited, one
enclave did not install, or **one that would shadow a built-in extension of
the same name** — but never overrides a validation error: a spec that fails
to load, or content that violates the [limits](#limits) below, blocks the
install regardless of `--force`. An extension's name is one of those
validation errors: it must match `^[a-z0-9][a-z0-9._-]*$`. The name becomes a
directory, a build-context path, and an interpolation into the generated
Dockerfile, so the charset is deliberately narrower than what a filesystem
accepts. `--dry-run` performs the same fetch,
staging, and validation as a real install and prints what would happen, but
never touches the destination — see the identical behavior under [Update
semantics](#update-semantics).

## Provenance

A successful install writes `.enclave-source.json` into the installed
extension directory:

| Field | Meaning |
|-------|---------|
| `remote`, `source`, `subpath` | Redacted remote URL, the source string as given (also redacted), and the repository subpath |
| `ref`, `refType` | The requested ref and whether it is a `branch`, `tag`, or `commit` |
| `commit` | The exact commit installed |
| `installedAt`, `installedBy` | Install timestamp and the enclave version that performed it |
| `treeHash` | A digest of the installed files, used to detect local edits |

Without `--ref`, the install follows the branch the remote's HEAD names. A
source whose HEAD names no branch — a local clone sitting on a detached HEAD —
offers nothing to follow, so the install records a commit pin instead and
`update` treats it as one.

This sidecar is excluded from the Docker build context and from the image
identity hash — re-pinning to a new commit with byte-identical content does
not force a rebuild, and the sidecar never reaches the image. Such a re-pin
therefore prints no "rebuilds the image" hint either: there is nothing for the
next run to rebuild. A directory under the user extension root with no
sidecar is unmanaged: it was placed there by hand (or predates the
installer), so `update` skips it (there is no recorded source to update from,
and `--force` does not invent one) while `remove` requires `--force`. A
sidecar that exists but fails to parse is reported distinctly ("its
provenance file could not be read") from a missing one ("not installed by
enclave").

An extension whose `spec.yaml` no longer loads, which an upstream
`schemaVersion` bump is enough to cause, stays manageable: `remove` and
`update` enumerate the directories under the extension roots rather than the
specs that parse, so recovery never means deleting a directory by hand.

Editing an installed extension's files by hand changes its tree hash. The
next `update` detects the mismatch and refuses to overwrite your edits until
you pass `--force`, which discards them.

## Update semantics

With no names, `update` targets every extension of that kind installed from
a git source (built-ins and unmanaged directories are skipped). For each
target:

- **Commit pin, no `--ref`/`--force`**: nothing to check — a commit is
  immutable, so an install pinned to one is already up to date with **no
  network call at all**. (Local edits still block this path with an error
  telling you to pass `--force`.)
- **Branch or tag pin**: a single `git ls-remote` resolves the ref to a
  commit. If it matches the recorded commit and there are no local edits,
  the run prints "up to date" and stops — nothing is fetched or staged.
- Otherwise, the new commit is fetched, staged, and validated exactly like
  `add`; the changed-file list (added/removed/modified paths, capped with a
  count once there are more than a handful) and the capability diff are
  shown, and the swap happens after confirmation (or immediately under
  `--yes`).

Targets that share a remote and a ref — several extensions from one kit
repository — are resolved and fetched once for the whole run, not once each.

`--ref <ref>` moves a single named extension to a different branch, tag, or
commit; it errors if more than one target is selected. `--dry-run` performs
the fetch/stage/validate but writes nothing.

## Staging and recovery

`add` and `update` never write into the destination directly. Content is
staged in a `.incoming-*` directory created inside the destination directory
itself, so the final rename never crosses a filesystem boundary; only after
staging, validation, and confirmation does the swap happen. During the swap,
any content already at the destination is first moved aside to a
`.replaced-*` directory, the staged directory is renamed into its place, and
the moved-aside copy is deleted once the new content is live.

Both prefixes are dot-directories: they are invisible to extension listing
and validation, and a leftover from an interrupted run is swept away
automatically after 24 hours.

A `--dry-run` stages in the system temp directory instead, since it never
commits and so needs neither the same-filesystem rename nor the destination
to exist. That is what makes "writes nothing" hold on a host that has never
installed an extension: the user extension root is not created either.

`.replaced-*` is a **time-boxed recovery window, not a durable backup**. If a
process is killed between the swap's two renames, the previous content is
recoverable from that directory — but only until the next sweep runs; after
24 hours it is gone for good. Waiting a day before checking is not safe.

## Remove semantics

`remove` only ever touches user-installed extensions:

- A built-in is never removable — there is nothing to remove.
- Removing a user extension that **overlaid** a built-in of the same name
  reactivates the built-in; the output says so explicitly.
- An extension enclave did not install (no readable sidecar) needs `--force`.
- Per-project and per-tool host state — patches, persistent stores, auth —
  is deliberately left in place. It may belong to a future reinstall, and
  `remove` only ever removes the extension directory itself; the output
  names what was left untouched.

## Requirements

`git` must be on `PATH`; there is no other transport. A fetch prefers a
blobless, shallow, single-ref fetch (`--filter=blob:none --depth 1`) followed
by a sparse checkout of only the directories being installed, so file content
you don't need is never downloaded. Each stage degrades independently on a
server that can't do better — a server that ignores or rejects the object
filter falls back to a full shallow fetch, a checkout that can't go sparse
falls back to a full checkout of the fetched commit, and a server that
refuses to fetch a bare commit SHA falls back to fetching the default branch
and checking out the pinned commit from it. None of this is negotiated by
probing the server's git version; every step is a plain try-then-fall-back.
Submodules are never fetched or checked out. A repository whose root is
itself an extension is checked out in full: that extension owns the whole
tree, so no set of directories describes it.

## JSON contracts

`enclave <features|tools> list --json` emits `schemaVersion: "1"` and one
entry per extension, whether built-in or installed:

```json
{
  "schemaVersion": "1",
  "kind": "feature",
  "extensions": [
    {
      "name": "github-cli",
      "displayName": "GitHub CLI",
      "description": "GitHub CLI (gh)",
      "source": "builtin",
      "enabled": true,
      "managed": false
    },
    {
      "name": "demo",
      "displayName": "Demo",
      "description": "Demo feature for documentation walkthrough",
      "source": "user",
      "enabled": true,
      "managed": true,
      "origin": {
        "remote": "/tmp/acme-kits",
        "source": "/tmp/acme-kits",
        "subpath": "extensions/features/demo",
        "ref": "main",
        "refType": "branch",
        "commit": "5e6a30d81a0ae3c73172c29f429ee0ba84a28597",
        "installedAt": "2026-08-26T19:42:23Z",
        "locallyModified": false
      }
    }
  ]
}
```

`source` is `builtin`, `user`, or `override` (a user directory shadowing a
built-in of the same name). An entry whose sidecar exists but could not be
read carries `originError` instead of `origin`, with `managed: false`.

`add`, `update`, and `remove` share one result envelope (also
`schemaVersion: "1"`), written to stdout as a single JSON document when
`--json` is passed:

```json
{
  "schemaVersion": "1",
  "kind": "feature",
  "results": [
    {
      "name": "demo",
      "action": "installed",
      "commit": "5e6a30d81a0ae3c73172c29f429ee0ba84a28597",
      "path": "/home/you/.config/enclave/extensions/features/demo"
    }
  ]
}
```

`action` is one of `installed`, `updated`, `unchanged`, `removed`, `skipped`,
or `failed`; a failed entry carries `error` instead of `commit`/`path`. An
entry's `error` never repeats its `name` (`"has local modifications; pass
--force to discard them"`, not `"foo has local modifications…"`) — `name` is
the identifier, so prepend it if you want a full line, which is what the
human-facing output does. In
`--json` mode, this document is the whole report: stdout carries nothing else,
and the human narration (discovery listing, skip/warn lines, the capability
summary, post-install hints) is not rendered at all rather than dumped on
stderr — warnings appear in each result's `warnings`, and failures in its
`error`. An error that ends the run before any extension is touched (an
unreachable source, a rejected flag combination) is reported as a `failed`
result with an empty `name`, so stdout is parseable unconditionally. The
process exits non-zero if any result's `action` is `failed`,
independent of whether other extensions in the same invocation succeeded.

## Limits

- Extensions install into the **user-global** root only
  (`~/.config/enclave/extensions/`); project-local extension directories are
  never loaded (see [Extension Sources](README.md#extension-sources)).
- An extension is capped at 1000 files and 10 MiB of content; symlinks and
  other non-regular files are rejected by name, as is any path that would
  escape the destination directory.
- A `go/` directory in an installed extension is copied but never compiled
  in: custom hooks/handlers still require rebuilding the `enclave` binary.
- There is no extension registry or index — every install names a git
  repository directly — and no signature verification of fetched content;
  trust is whatever you extend to the source you point at.
