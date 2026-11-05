# Custom commands

Add your own `enclave <name>` verbs: scripts that run on the host or inside a
session, and host commands shipped by installed extensions.

```bash
mkdir -p ~/.config/enclave/commands/host
cat > ~/.config/enclave/commands/host/deploy <<'EOF'
#!/bin/sh
echo "deploying with args: $@"
EOF
chmod +x ~/.config/enclave/commands/host/deploy
enclave deploy --env prod    # runs the script with ["--env", "prod"]
```

## Where commands come from

| Source | Location | Runs |
|--------|----------|------|
| Your host commands | `~/.config/enclave/commands/host/<name>` | On the host |
| Your session commands | `~/.config/enclave/commands/session/<name>` | Inside a sandboxed session container |
| Extension host commands | `commands/host/<name>` in an installed extension | On the host |

Names resolve in this order, first match wins:

1. Built-in verbs.
2. Your `commands/host/`, then `commands/session/`.
3. Command extensions, then tools, then features, alphabetically within each
   kind.

Installing an extension never takes a name you already use. To turn off a
single verb, list it in `disabled_commands` in the global config (see
[Global only](configuration.md#global-only)); the name then fails as unknown.

## Host commands

Any executable file (`chmod +x`) in `~/.config/enclave/commands/host/` becomes
a verb. The shebang picks the interpreter, so any language works.

- Everything after the command name, including flags, `--`, and `--help`, is
  passed verbatim. stdin, stdout, stderr, and the exit code pass through.
- enclave's own flags go before the name and are limited to global flags, for
  example `enclave --verbose deploy`.
- Symlinks are followed and may point anywhere; a broken link is warned about.
- The environment is inherited, plus `ENCLAVE_BIN` (the enclave binary),
  `ENCLAVE_PROJECT_ROOT` (the current project directory),
  `ENCLAVE_CONFIG_DIR` (`~/.config/enclave`), and `ENCLAVE_ALLOW_ROOT=1` when
  the run allowed root, so a script re-invoking `$ENCLAVE_BIN` inherits it.

## Session commands

An executable in `~/.config/enclave/commands/session/<name>` runs inside a
sandboxed session container, like `enclave shell`.

- The `session/` tree is mounted read-only at a fixed neutral path, and the
  script is exec'd there with its arguments verbatim.
- The shebang works if the interpreter and architecture exist in the image;
  slim images may lack some interpreters.
- The full session flag set is accepted before the name, for example
  `enclave --tool codex triage ...`.
- No `ENCLAVE_*` variables are injected.
- Symlinks must be relative and stay inside the `session/` tree across every
  hop, since absolute targets do not resolve in the container. Other symlinks
  are warned about and skipped.

## Commands from extensions

Every extension kind may ship executables in `commands/host/`. They become
verbs on every host where the extension is installed, with the same contract
as your own [host commands](#host-commands), and `remove` takes them away
again.

> [!WARNING]
> Extension host commands run on the host, outside the sandbox, with your
> privileges. Everything else an extension ships stays inside the container.

`add` and `update` list them in the first row of the capability summary, and
`list` shows them per extension; see
[Auditing host commands](extensions/installing.md#auditing-host-commands). A
command extension (`kind: command`, installed with `enclave commands add`)
ships host commands only; see
[Command extensions](extensions/README.md#command-extensions).

## Shipping host commands in an extension

```
extensions/features/my-feature/
├── spec.yaml
└── commands/
    └── host/
        └── my-viewer      # chmod +x, becomes `enclave my-viewer`
```

Ship one only when the job cannot be done inside a session, for example
driving a host GUI or reaching something the sandbox blocks.

| Rule | Effect |
|------|--------|
| Executable bit | Must be set in the repository; other files (a `README`, data) are ignored silently |
| Only `commands/host/` is read | Anything else under `commands/` is a validation warning: `ignoring commands/<name>` |
| Name collisions | A name owned by a built-in or the user is skipped; `add`, `update`, and every run warn. Prefer a name tied to the extension. |
| Installed as a set | Users drop single verbs with `disabled_commands`, so put independent verbs in separate extensions |
| Not in the image | `commands/` is excluded from the build context and image hash; edits force no rebuild, and nothing in a session can run it |
| User root only | Only `~/.config/enclave/extensions/` is scanned, including hand-made directories and overrides of built-ins. A built-in's own `commands/` is not read; a built-in that wants a verb adds a real command. |
