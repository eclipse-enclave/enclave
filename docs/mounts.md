# Mounts

Which host directories a session can see: the project, additional
directories, linked-worktree Git metadata, and the checks that keep sensitive
host data out of the container.

## Common tasks

```bash
enclave --add-dir ~/other-proj          # Mount an additional host directory
enclave --add-readonly-dir ~/sdk        # Mount an additional directory read-only
enclave --project-mount readonly        # Mount the project read-only
enclave --add-sensitive-dir ~/.m2       # Mount a sensitive directory for this run
```

| Key | Flag | Effect |
|-----|------|--------|
| `project_mount` | `--project-mount <writable\|readonly>` | Mount the project/worktree read-write (default) or read-only |
| `add_dirs` | `--add-dir <path>` | Additional directories to mount |
| `add_readonly_dirs` | `--add-readonly-dir <path>` | Additional directories to mount read-only |
| `worktree_metadata` | `--worktree-metadata <follow\|readonly\|none>` | Linked-worktree Git metadata mounts |
| `sensitive_dir_exceptions` | `--add-sensitive-dir`, `--add-readonly-sensitive-dir` (CLI-only, also mount the path) | Paths whose subtrees may be mounted although they are sensitive |

`--add-dir` and `--add-readonly-dir` take one path per flag (commas belong to
the path) and add to the configured list; see
[List options](configuration.md#list-options).

## Project mount

The project (or worktree) is writable by default. With `readonly`, writable
`add_dirs` inside the project, devcontainer project mounts, and (unless
`worktree_metadata` says otherwise) linked-worktree metadata become read-only
too. Directories outside the project, caches, and history follow their own
options. Existing Git config files are always read-only; see
[Git config protection](git.md#config-protection).

## Worktree metadata

`worktree_metadata` controls the gitdir and commondir mounts resolved from a
linked worktree's `.git` file: `follow` (default) ties them to the project
mode, `readonly` forces them read-only, `none` skips them. With read-only
metadata the agent can edit files and run `git log`/`diff`/`status`, but every
Git write, including `git add`, fails; commit on the host. For a `.git`
directory inside the project, only the project mode applies.

A verified linked worktree can mount its gitdir and common directory even when
separate Git metadata prevents identifying an inheritance anchor. In that case,
[project configuration stays checkout-local](configuration.md#linked-worktrees)
and startup warns. Sensitive-path restrictions and metadata mount modes still
apply.

Default mode leaves the registry writable so agents can use `git worktree add`
for subagents. Existing config and pointer files stay protected; operations
that remove or move entries protected at startup can fail. See
[Git config protection](git.md#config-protection).

## Sensitive mounts

Session-starting commands (`run`, `shell`, `continue`, `resume`,
`devcontainer` variants, session user commands) refuse to mount a project or
additional directory, including from config, that exposes sensitive host data,
unless a [sensitive-dir exception](#sensitive-dir-exceptions) covers it. A
refusal names the flag or config file. So run Enclave from a project
directory, not `~`.

A directory is sensitive when it:

- is your home directory or one of its parents, such as `/` or `/home`
- is, lies inside, or contains a hidden entry of your home directory, such as
  `~/.ssh`, `~/.aws`, `~/.config`, or `~/.bashrc`. A hidden entry that is a
  symlink counts where it points, so a stow-managed dotfiles directory is
  sensitive too
- is, lies inside, or contains a per-user application data directory: the XDG
  config, data, state, cache, and runtime directories on Linux (honoring
  `XDG_*` overrides), or the entries of `~/Library` on macOS except
  `CloudStorage` and `Mobile Documents`, which hold your own synced files
- is, lies inside, or contains Enclave's own config, state, or cache root
- contains the SSH agent socket named by `SSH_AUTH_SOCK`

Read-only mounts are checked too, since reading leaks credentials. The check
runs before any build and on every start, including `continue` and `resume`;
`exec` and `attach` enter running containers and are not checked.

### Sensitive-dir exceptions

An exception covers a path and everything below it, never a parent (compared
after `~` expansion and symlink resolution). Both sources add up, and every
exempted mount warns on every start, naming its exception.

| Source | Scope | Mounts the path |
|--------|-------|-----------------|
| `--add-sensitive-dir <path>`, `--add-readonly-sensitive-dir <path>` | This invocation | Yes, like `--add-dir` and `--add-readonly-dir`; adds to `add_dirs`/`add_readonly_dirs` from config |
| `sensitive_dir_exceptions` in global or project config | Every run | No |

- Pass the flags again for `continue` and `resume`, which start fresh runs.
- On a non-sensitive path the flags just mount it, so wrappers can always pass
  `--add-sensitive-dir`.
- On the project directory, `--add-sensitive-dir` exempts it without a second
  mount. `--add-readonly-sensitive-dir` is refused there even when not
  sensitive (use `--project-mount readonly`); `--add-readonly-dir` only warns
  and skips.
- An exception for a parent (`--add-sensitive-dir ~/.dotfiles` run from
  `~/.dotfiles/nvim`) mounts the parent and covers the project.
- `sensitive_dir_exceptions` merges like other
  [list options](configuration.md#list-options); entries need not exist yet;
  relative entries and home or its parents are ignored with a warning.

### Hard-protected locations

A mount that is, lies inside, or contains one of these is refused despite any
exception; mount a narrower directory.

| Location | Why |
|----------|-----|
| Your home directory and its parents | Exposes all user data |
| Enclave's own config, state, and cache roots | The agent could edit `config.json` to widen its own exceptions or network access for the next run, or add a host command under `commands/host/` that runs on the host |
| The SSH agent socket | Whoever reaches it can sign with your SSH keys |

The roots count where they really live: with `~/.config` linked to
`~/.dotfiles/config`, `~/.dotfiles` contains Enclave's config root.

### Mounting all sensitive paths

`--allow-sensitive-mounts` or `ENCLAVE_ALLOW_SENSITIVE_MOUNTS=1` mounts every
sensitive directory, hard-protected ones included, with a warning each;
`--allow-sensitive-mounts=false` overrides the variable. It has no config key
(see [CLI-only options](configuration.md#cli-only-options)).

Still blocked: devcontainer bind sources outside the project, and sensitive
linked-worktree metadata outside the project (skipped with a warning).

Mounts Enclave adds itself (`~/.gitconfig`, `~/.claude/ide` for the IDE bridge,
direnv approvals, session stores) are not checked.
