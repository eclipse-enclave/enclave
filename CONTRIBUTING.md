# Contributing to Eclipse Enclave

Thanks for your interest in Eclipse Enclave, a sandboxed runtime for executing
AI agents in isolated, policy-controlled environments.

- Project home: https://projects.eclipse.org/projects/ecd.enclave
- Developer resources: https://projects.eclipse.org/projects/ecd.enclave/developer
- Source code: https://github.com/eclipse-enclave/enclave

## Terms of Use

This repository is subject to the [Eclipse Foundation Terms of
Use](https://www.eclipse.org/legal/terms-of-use/).

## Project License

Eclipse Enclave is distributed under the [MIT License](LICENSE.md).
Contributions are received under the terms of that license.

## Contributing Code

<a name="discuss-first"></a>

- [1.](#discuss-first) Before starting substantial work, open or identify a
  GitHub issue and discuss the intended change with the project team.

<a name="where-it-goes"></a>

- [2.](#where-it-goes) Open the pull request in the right repository:
  - A new tool goes to
    [enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions#readme),
    the official extensions repository. Enclave itself ships six tools: Claude
    Code, Codex, and Copilot from large vendors, and the open-source Theia
    (with its `theia-next` preview profile), OpenCode, and Pi.
  - A change to one of the six, to a core feature, or to the extension
    mechanism itself goes here.
  - A proposal to change the six is an issue here. The Eclipse Enclave
    [committers](https://projects.eclipse.org/projects/ecd.enclave/who) vote on
    it for at least one week. It passes with at least three +1 votes and no -1;
    if the project has three or fewer committers, all of them must vote +1.
    These are the thresholds of
    [committer elections](https://www.eclipse.org/projects/handbook/#elections-how).
    Committers are appointed or elected under the
    [Eclipse Foundation Development Process](https://www.eclipse.org/projects/dev_process/);
    a merged commit does not make anyone a committer.

<a name="pull-requests"></a>

- [3.](#pull-requests) Submit changes as GitHub pull requests against `main`.

<a name="focused-changes"></a>

- [4.](#focused-changes) Keep changes focused and follow the repository guidance
  in `AGENTS.md`.

<a name="required-checks"></a>

- [5.](#required-checks) Format modified Go files and run the required checks
  before submitting a pull request:

  ```sh
  make build
  make test
  make lint
  ```

<a name="human-in-the-loop"></a>

- [6.](#human-in-the-loop) Contributors can use whatever tools they would like
  to craft their contributions, but there must be a human in the loop.
  Contributors must read and review all LLM-generated code or text before they
  ask other project members to review it. The contributor is always the author
  and is fully accountable for their contributions. Contributors should be
  sufficiently confident that the contribution is high enough quality that
  asking for a review is a good use of scarce maintainer time, and they should
  be able to answer questions about their work themselves during review.

## Eclipse Foundation Development Process

This Eclipse Foundation open source project is governed by the [Eclipse
Foundation Development Process](https://www.eclipse.org/projects/dev_process/)
and operates under the [Eclipse Foundation Intellectual Property
Policy](https://www.eclipse.org/org/documents/Eclipse_IP_Policy.pdf).

## Eclipse Contributor Agreement

Contributors must electronically sign the [Eclipse Contributor Agreement
(ECA)](https://www.eclipse.org/legal/eca/).

The ECA records that every contribution complies with the Developer Certificate
of Origin. The email address in a contribution's Git `Author` field must match
the email address associated with the contributor's ECA.

## Contact

Contact the project developers through the [enclave-dev mailing
list](https://accounts.eclipse.org/mailing-list/enclave-dev).
