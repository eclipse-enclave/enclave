# Contributing to Eclipse Enclave

Eclipse Enclave runs AI coding agents in isolated, policy-controlled
containers. This page covers how to build it, where changes go, and what a pull
request needs.

> [!TIP]
> Looking for build instructions? Start with
> [Building from source](#building-from-source) below; the
> [Developer Guide](docs/DEV.md) has the full build, test, and packaging
> reference.

| Resource | Link |
|----------|------|
| Source code | https://github.com/eclipse-enclave/enclave |
| Project home | https://projects.eclipse.org/projects/ecd.enclave |
| Developer resources | https://projects.eclipse.org/projects/ecd.enclave/developer |
| Mailing list | [enclave-dev](https://accounts.eclipse.org/mailing-list/enclave-dev) |
| Architecture | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |

## Building from source

Requirements:

- Go 1.24 or newer, and Make. If your distribution ships an older Go, use the
  official [Go installation instructions](https://go.dev/doc/install).
- Linux or macOS. On Windows, use WSL2.
- A container engine to run what you build; see
  [Installation](docs/install.md#requirements).

```bash
git clone https://github.com/eclipse-enclave/enclave.git
cd enclave
make build      # build bin/enclave
make install    # install to ~/.local/bin (Linux) or /usr/local/bin (macOS)
make test       # run the tests
make lint       # run the linters
```

The [Developer Guide](docs/DEV.md) is the full reference: running changes
from the working copy, tests, package builds, code generation, and the option
checklist.

## Contributing code

<a name="discuss-first"></a>

- [1.](#discuss-first) Before starting substantial work, open or find a GitHub
  issue and discuss the change with the project team.

<a name="where-it-goes"></a>

- [2.](#where-it-goes) Open the pull request in the right repository:

  | Change | Repository |
  |--------|------------|
  | A new tool | [enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions#readme), the official extensions repository |
  | A change to one of the six core tools, a core feature, or the extension mechanism | This repository |
  | A proposal to change the set of six core tools | An issue in this repository |

  Enclave ships six tools: Claude Code, Codex, and Copilot from large vendors,
  and the open-source Theia (with its `theia-next` preview profile), OpenCode,
  and Pi.

  The Eclipse Enclave
  [committers](https://projects.eclipse.org/projects/ecd.enclave/who) vote on
  a proposal to change the six for at least one week. It passes with at least
  three +1 votes and no -1; if the project has three or fewer committers, all
  of them must vote +1. These are the thresholds of
  [committer elections](https://www.eclipse.org/projects/handbook/#elections-how).
  Committers are appointed or elected under the
  [Eclipse Foundation Development Process](https://www.eclipse.org/projects/dev_process/);
  a merged commit does not make anyone a committer.

<a name="pull-requests"></a>

- [3.](#pull-requests) Submit changes as GitHub pull requests against `main`.

<a name="focused-changes"></a>

- [4.](#focused-changes) Keep changes focused and follow the repository
  guidance in [AGENTS.md](AGENTS.md).

<a name="required-checks"></a>

- [5.](#required-checks) Format modified Go files with `gofmt -w` and run the
  required checks before submitting:

  ```sh
  make build
  make test
  make lint
  ```

  Documentation-only changes under `docs/` do not need them.

<a name="human-in-the-loop"></a>

- [6.](#human-in-the-loop) Use whatever tools you like to write a
  contribution, but keep a human in the loop. Read and review all
  LLM-generated code or text before asking others to review it. You are the
  author and fully accountable for the contribution: be confident that it is
  worth a maintainer's review time, and be able to answer questions about it
  yourself.

## Pull request checklist

- Commit messages follow Conventional Commits (`type: short imperative
  summary`); see [Commits](docs/DEV.md#commits).
- The PR description has a `changelog` block (the PR template provides it).
  Do not edit `CHANGELOG.md`; only the release-prep PR does.
  - Write one `- ` item per user-visible change, in the user's terms, with
    wrapped lines indented by two spaces as in `CHANGELOG.md`.
  - Write `none` inside the block when users will not notice the change.
  - Start a breaking change with `Breaking:` and say what to do instead.
  - Do not add a PR or issue link; the release adds it.
  - The note can be corrected in the description until the release, without
    a commit.

  ```changelog
  - Adding or removing a feature no longer reinstalls the features
    ordered before it when the image rebuilds.
  ```

  See [Releases](docs/RELEASING.md) for how the notes are collected.

## Legal

- Terms of use: this repository is subject to the
  [Eclipse Foundation Terms of Use](https://www.eclipse.org/legal/terms-of-use/).
- License: Eclipse Enclave is distributed under the
  [MIT License](LICENSE). Contributions are received under the terms of
  that license.
- Development process: the project is governed by the
  [Eclipse Foundation Development Process](https://www.eclipse.org/projects/dev_process/)
  and operates under the
  [Eclipse Foundation Intellectual Property Policy](https://www.eclipse.org/org/documents/Eclipse_IP_Policy.pdf).
- Eclipse Contributor Agreement: sign the
  [ECA](https://www.eclipse.org/legal/eca/) electronically. It records that
  every contribution complies with the Developer Certificate of Origin. The
  email address in a commit's Git `Author` field must match the one associated
  with your ECA.
