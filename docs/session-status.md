# Session status snapshots

`enclave status` reports what each running agent session currently shows on
its terminal, so external frontends and orchestrators can derive the session
state (idle, working, blocked on user input) without attaching to it.

enclave deliberately does **not** interpret the terminal content. It is a
"dumb source": it captures raw detection inputs — the rendered screen text and
the OSC window title — and the consumer owns the detection rules. This keeps
one definition of state across sources (a local agent, an enclave container, a
terminal embedded in another application).

## How it works

A terminal has no readable screen buffer: the rendered screen only exists
inside a terminal emulator. To make capture possible, the entrypoint runs the
agent inside a managed tmux session when the session monitor is enabled
(opt-in via `--session-monitor`). The session uses a dedicated socket
(`tmux -L enclave`) and a
baked-in config (`/usr/local/share/enclave/tmux-session.conf`), so it is
isolated from any user `~/.tmux.conf` and
shows no tmux chrome — no status bar, wheel scrollback keeps working, and the
agent's window title still reaches the outer terminal.

`enclave status` then runs `tmux capture-pane` inside each labeled container
to read the rendered screen (correctly wrapped at the real terminal width,
alternate-screen aware) and `#{pane_title}` for the OSC title. It captures as
the user that owns the tmux server (recorded on the container), so it reaches
the right per-user socket even under `--runtime-uid-remap`, where the
container's default exec user is root but the agent runs remapped.

Enable per session with `--session-monitor`, or persistently with
`"session_monitor": true` in the configuration. Sessions without the monitor
run the agent as a plain TTY process and report `"capture": "unavailable"`.

## Usage

```bash
enclave status              # sessions of the current project (table)
enclave status --json      # machine-readable snapshots
enclave status --all       # sessions from all projects on the host
enclave status --tool claude --name main --json
```

Like `exec`, `status` targets the project resolved from the working directory
(worktree-aware); pass `--all` to report every running session on the host.
The table shows one row per running session: name, tool, capture mode, OSC
title, and the bottommost non-blank screen line.

## Snapshot format (`--json`)

One object per running session:

```jsonc
[
  {
    "agent":        "claude",                       // tool label
    "session_id":   "enclave-claude-a1b2c3-1",    // container name, stable and unique
    "timestamp":    1751900000000,                  // capture time, unix epoch ms
    "screen":       "● Done.\n\n❯\n…",              // rendered visible screen, plain text
    "osc_title":    "✳ Gate latency collector",     // window title as set by the agent
    "osc_progress": null,                           // not tracked (tmux has no OSC 9;4 support)

    "session_name": "main",                         // enclave extras
    "status":       "running",
    "capture":      "tmux"                          // or "unavailable"
  }
]
```

Field notes for consumers:

- `screen` is the rendered visible pane, capped at its trailing 24 rows:
  finished text, wrapped at the actual terminal width, including trailing blank
  rows. If a full-screen program (editor, pager) is active, its screen is
  captured.
- `osc_title` is sent exactly as the agent set it, including leading markers
  (`✳`, spinner characters) that some agents use to broadcast state.
- `osc_progress` is always `null`; consumers treat it as an optional,
  negligible signal.
- `capture: "unavailable"` marks sessions without the managed tmux session
  (started without `--session-monitor`, created before this feature, or images
  without tmux). An `error` field is added when a capture attempt failed.

Orchestrators poll `enclave status --json` at their preferred interval and
diff snapshots per `session_id`.

## Caveats

- A background session that was never attached keeps its initial terminal size
  (typically 80×24), so its screen wraps narrower than what a user sees after
  attaching. Wrapping is still internally consistent, which is what detection
  needs.
- Inside the managed session `TERM` is `tmux-256color` (truecolor enabled)
  instead of `xterm-256color`.
- tmux's prefix key (`Ctrl-B`) is active in attached sessions; scrollback
  beyond the mouse wheel uses tmux copy-mode. Because tmux handles the mouse,
  text selection uses tmux's copy semantics — hold Shift while dragging for
  the terminal's native selection.
- Under the session monitor the container exit code no longer reflects the
  agent's exit code (the tmux client always exits 0). Leave the monitor off
  if you script against a session's exit status.
