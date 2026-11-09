# Git in the sandbox

How Enclave sets up Git identity in the container, which Git config files it
protects from the agent, and what that protection does not cover.

## Identity

Every session, even outside a repository, needs a complete author and
committer identity. If it is incomplete, or host Git config cannot be read,
startup fails; Enclave never invents one. Sources:

| Source | Notes |
|--------|-------|
| Host global config (`user.name`, `user.email`) | Git reads `~/.gitconfig` and `$XDG_CONFIG_HOME/git/config` (default `~/.config/git/config`), or `$GIT_CONFIG_GLOBAL` instead when set. Included files are resolved on the host, including `includeIf gitdir` rules for the project. Host system Git settings can supply missing global values |
| The project's Git config | Repository-local and worktree settings take precedence over global ones |
| Session `GIT_AUTHOR_*` and `GIT_COMMITTER_*` variables | Supply them through the project's `.env`, devcontainer `containerEnv`, or tool/feature environment variables |

- Variables exported in the host shell, and devcontainer `runArgs`
  `--env`/`-e`/`--env-file` values, are not considered.
- The identity is written to the container's Git config, never to the
  project's `.git/config`; `user.useConfigOnly` stops Git from guessing later.
- A host `~/.gitconfig` is mounted read-only and copied in for aliases and
  preferences; edits to the copy do not reach the host.

## Signing

`commit.gpgsign` and `tag.gpgsign` are always disabled in the container,
because host GPG and SSH signing keys are not available there. Signatures
therefore cannot tell an agent's pushes from yours; see
[SSH keys](auth.md#ssh-keys).

## Config protection

Existing Git config files are mounted read-only even when the project is
writable. The rest of the Git metadata stays writable, so staging and
committing still work. Protection is always enabled.

Protected files:

- The current repository's `.git/config` and `config.worktree`, including those
  of the main and sibling linked worktrees.
- Config files discovered through Git's includes.
- Exposed global and system config files discovered through Git, including
  empty global files and empty files selected by `GIT_CONFIG_SYSTEM`. Empty
  system files at Git's compiled-in default path are not discovered.
- A project-root `.gitconfig`, only if Git reads or includes it. Its filename
  alone does not make it active configuration.
- Linked-worktree pointer files.

Containing directories are anchored with writable bind mounts, so renaming a
parent cannot bypass the protection. Protection covers exposed bind-mount
aliases without exposing otherwise unmounted host files; on Docker this
includes aliases introduced by devcontainer `runArgs`.

Make changes to protected config files on the host and restart the session.

### Commands that fail or partly succeed

In-container `git config --local` and `--worktree` writes to existing files
fail. Commands that write repository config can fail or complete only part of
their work:

| Command | Effect |
|---------|--------|
| `git remote add`, `git submodule update --init`, upstream setup | Can fail |
| `git push -u` | Can push successfully without saving upstream tracking |
| `git branch -m` | Can rename the branch before reporting a config-write failure |
| A checkout that would replace a protected tracked config file | Can report `unable to unlink old`, leave the old contents in place as a modified file, and still switch branches with exit status 0 |
| `rm -rf`, checkouts, and worktree cleanup touching anchored directories | Can fail after making partial changes, because anchored directories cannot be removed or renamed inside the session |

### Details and limits

- This is startup protection for discovered, existing files. It is not a
  read-only policy for all Git metadata, nested repositories, or config files
  created later. Missing include targets and missing `config.worktree` files
  remain creatable, even when the existing configuration already references
  them.
- Each protected file and otherwise unmounted parent directory adds a mount.
  Repositories with many worktrees or deeply nested includes can need many
  mounts; on QEMU each directory mount is also a separate 9p device.
- The `worktree_metadata` option controls how linked-worktree gitdir and
  commondir mounts are made; see
  [Worktree metadata](mounts.md#worktree-metadata).

> [!WARNING]
> Config protection does not prevent host code execution through writable Git
> metadata. An agent can install hooks in `$GIT_COMMON_DIR/hooks/` or create a
> previously absent `.git/commondir` to redirect Git to an unprotected config.
> These changes can affect later host Git commands. Use
> `--project-mount readonly`, without writable aliases, when Git metadata must
> not change; linked-worktree metadata also follows the read-only project
> mount.
