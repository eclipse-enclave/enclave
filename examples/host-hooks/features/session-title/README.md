# Session title: host-hook demonstrator

A small external-only Python 3 feature that sets `Enclave · codex · payment-service`
while a foreground run, exec or attach owns the terminal. It demonstrates a second
terminal resource (`title`) that can coexist with tint (`background`), without
Enclave core changes, container packages, replay or a leave executable.

## Try the uncommitted POC

From the repository root, copy it into the user provider root (Linux/XDG example):

```sh
mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/enclave/hooks/host"
cp -R examples/host-hooks/features/session-title/hooks/host \
  "${XDG_CONFIG_HOME:-$HOME/.config}/enclave/hooks/host/session-title"
```

Use a fresh destination when copying; it must contain `manifest.json` directly.
The phase executable must retain its executable bit and Python 3 must be on PATH.
Enable this user provider in global/project/per-tool config:

```json
{
  "host_hooks": {
    "user:session-title": {
      "enabled": true,
      "settings": {"prefix": "Enclave"}
    }
  }
}
```

Start `enclave --tool codex`, or exec/attach to an existing session. Those terminal
phases receive the actual target tool/project, not necessarily the invocation's
cwd/default tool. Only the project's basename is displayed. `prefix` is optional
and defaults to `Enclave`; control characters are replaced with spaces and the
complete UTF-8 title is capped at 512 bytes. Titles are not colors, so `NO_COLOR`
does not disable this provider. Background/non-TTY contexts produce no effects.
Do not enable both copies of the provider: they claim the same `title` resource.

On macOS use the platform's Enclave config root instead of the XDG path above;
see [configuration](../../../../docs/configuration.md).

Once the files exist in a Git source, install through the normal extension path:

```sh
enclave features add /path/to/enclave --path examples/host-hooks/features/session-title --yes
```

Then enable `feature:session-title` instead of `user:session-title`. `features`
selection is unrelated to host activation. The spec is opt-in and has no container
contents; changes need no Enclave or image rebuild. Removing the extension leaves
its config entry; remove that separately. For the copied user provider, disable
its entry and remove the copied directory to undo the POC.

## Protocol sketch

`hooks/host/manifest.json` declares only `terminal-enter`. The executable consumes
one JSON request on stdin and returns one JSON response on stdout. Its effect is:

```json
{
  "schemaVersion": "1",
  "terminalEffects": [{
    "resource": "title",
    "enter": "\u001b[22;2t\u001b]2;Enclave · codex · payment-service\u0007",
    "leave": "\u001b[23;2t"
  }]
}
```

The enter bytes push the old window title then set the new title; the leave bytes
pop it. These are [xterm title-stack and OSC 2 operations](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html).
The terminal remembers the old title, not the provider. There is no terminal query,
`/dev/tty` access, session-stdin consumption, background process or persistent state.
Enclave registers the restore bytes before emitting enter, owns scope/signal cleanup,
and can restore even if this provider is removed while the session runs.

## Deliberate POC limits

- Requires a terminal implementing OSC 2 **and** the xterm window-title stack.
  There is no capability negotiation or fallback title; on a terminal supporting
  only OSC 2, the label can remain until the shell changes it.
- Whether this labels a window, tab or pane is terminal/multiplexer-specific. It
  does not directly rename tmux windows, Herdr tabs or desktop application windows.
- Shells/agents may issue their own title updates or manipulate the same stack.
  This is a one-shot entry label, not continuous title enforcement. In particular,
  a shell startup title may immediately overwrite it.
- SIGKILL cannot run cleanup. No real graphical terminal compatibility is certified
  by the protocol/byte tests below; test in your terminal before relying on restore.

Run `python3 examples/host-hooks/features/session-title/test_provider.py` and
`go test ./internal/hosthooks ./internal/extinstall`. The Go checks execute an
isolated copy, test title+tint coexistence/reset and normal Git installation.
See the [v1 contract](../../../../docs/extensions/host-hooks.md) for lifecycle details.
