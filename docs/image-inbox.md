# Image inbox

Hand a screenshot or clipboard image from the host to a session's agent.
`enclave img import` validates the host image and writes it into the
project's image inbox, which the project's inbox-enabled sessions mount
read-only. The agent reads a file; the container gets no display, clipboard,
or compositor access. Boundaries: [Security](security/README.md).

## Common tasks

### Enable the inbox

Start a session with the inbox mounted at `/mnt/host-images`, also exported
as `ENCLAVE_IMAGE_INBOX`:

```bash
enclave --image-inbox
```

To enable it for every session of a project, set `"image_inbox": true` in the
project config; see [Configuration](configuration.md). Docker and Podman only;
`--backend qemu` with the inbox fails.

### Import a host image

With a PNG or JPEG on the host clipboard, run this from the project directory:

```bash
enclave img import               # the clipboard
enclave img import --screenshot  # a screen region you select
```

It prints the container path, such as
`/mnt/host-images/20261119T104512Z-3f9c2a1e.png`, and puts that path on the host
clipboard. Paste it into the session with your terminal's paste shortcut
(usually `Ctrl+Shift+V`) and the agent reads the file.

Files and pipes work on every platform and leave the clipboard alone:

```bash
enclave img import --file shot.png
enclave img import - < shot.jpg
```

### Bind a hotkey

A hotkey runs `img import` without a terminal or a project directory. It
targets the only project with a running inbox-enabled session; with several,
add `--session <name>` (see [Targeting](#targeting)). When `notify-send` is
installed, a desktop notification shows the result.

sway and i3:

```text
bindsym $mod+Shift+s exec enclave img import --screenshot
bindsym $mod+Shift+v exec enclave img import
```

Hyprland:

```text
bind = SUPER SHIFT, S, exec, enclave img import --screenshot
bind = SUPER SHIFT, V, exec, enclave img import
```

sxhkd:

```text
super + shift + s
    enclave img import --screenshot
super + shift + v
    enclave img import
```

GNOME and KDE Plasma on Wayland cannot run `--screenshot`, because `grim` and
`slurp` need a wlroots compositor. Take the screenshot with the desktop's own
tool and bind a hotkey to the clipboard import:

- GNOME: `Print` opens the screenshot tool, which copies the selection to the
  clipboard. Add the shortcut under Settings, Keyboard, View and Customize
  Shortcuts, Custom Shortcuts, with the command `enclave img import`.
- KDE Plasma: `Meta+Shift+Print` captures a region with Spectacle. In
  Spectacle's settings, set "After taking a screenshot" to copy the image to
  the clipboard. Add the shortcut under System Settings, Keyboard, Shortcuts,
  Add New, Command or Script, with the command `enclave img import`.

If a hotkey does nothing, the desktop's `PATH` may lack `enclave`; use the
absolute path that `command -v enclave` prints.

## Reference

### Flags

A session started with `--image-inbox` (config key `image_inbox`) mounts the
project's inbox read-only at `/mnt/host-images` and sets
`ENCLAVE_IMAGE_INBOX`. Without a source flag, `img import` reads the host
clipboard. Its flags:

| Flag | Description |
|------|-------------|
| `--screenshot` | Read a screen region you select |
| `--file <path>` | Read a PNG or JPEG file |
| `-` | Read the host image from stdin |
| `--session <target>` | Import into the project of this session: a session name, container name, or container ID prefix, as for `attach` |
| `--no-clipboard` | Do not put the container path on the clipboard after a clipboard or screenshot import |
| `--backend <name>` | Container engine for this invocation |

The source options are mutually exclusive. Input must be a PNG or JPEG of at
most 10 MiB, and its content must match the type the clipboard advertises or
the file extension (`.png`, `.jpg`, `.jpeg`). The host image is written
atomically with mode 0600, named by import time and a random suffix.

Stdout carries only the container path, so `P=$(enclave img import ...)`
captures it; status messages go to stderr. The desktop notification names the
target project's directory on success, saying if the clipboard copy failed,
and carries the error on failure, including the candidate list. Unknown flags
and a cancelled screen selection show none.

### Targeting

Only running inbox-enabled sessions receive imports. The first match wins:

1. `--session <target>`. Session names in the working directory's project
   win over the same name elsewhere. A session without an inbox is an error;
   it never falls through to the next rules.
2. The working directory's project.
3. The only project with such sessions; several sessions of one project are
   fine.

Anything else is an error that lists the candidate projects and their
sessions.

### Requirements

Clipboard and screenshot import need Linux with one of:

| Display server | Clipboard | Screenshot |
|----------------|-----------|------------|
| Wayland (`WAYLAND_DISPLAY`) | `wl-clipboard` | `grim`, `slurp` (wlroots compositors) |
| X11 (`DISPLAY`) | `xclip` | `maim` |

A missing tool is named in the error. On macOS and WSL, use `--file` or `-`.
`notify-send` is optional.

### Host path

Each project's inbox is `~/.cache/enclave/inbox/<project-hash>/`, mode 0700,
under the [cache root](configuration.md#host-directories).

## Lifetime and targeting

A project's inbox is removed when the project's last inbox-enabled session
ends, at the latest on the next Enclave command that touches inboxes. A
stopped container that has not been removed no longer counts. There is no
expiry time and no size cap.

Ending a foreground session and `enclave stop` remove the inbox right away. A
background session that ends on its own is noticed by the next `img import`,
the next start of an inbox-enabled session, or `enclave cleanup`. Each of
these removes every project's inbox that no inbox-enabled session holds.
Sessions of another container engine (Docker versus Podman) are not seen, so
use one engine for inbox-enabled sessions.

Why it works this way:

- The inbox lives only while a session runs, so no expiry time can delete a
  host image that a running conversation still references.
- Imports need a running session; there is no staging, which keeps the
  lifetime rule simple. `--file` covers a host image saved earlier.
- Import refuses to guess the project, because a wrong guess exposes a
  screenshot to another project's agent.
