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

Before starting substantial work, open or identify a GitHub issue and discuss
the intended change with the project team. Submit changes as GitHub pull
requests against `main`.

Keep changes focused and follow the repository guidance in `AGENTS.md`. Format
modified Go files and run the required checks before submitting a pull request:

```sh
make build
make test
make lint
```

Contributions created with generative AI must be reviewed by a human and
disclosed in accordance with the [Eclipse Foundation AI
guidelines](https://www.eclipse.org/projects/handbook/#ai).

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
