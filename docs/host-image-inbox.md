# Host image inbox

The host image inbox gets an image from the host into the agent with minimal,
auditable exposure. Instead of giving the container any display, clipboard, or
compositor, a host-side action captures one image, validates it, and drops it
into a read-only bind mount; the agent just reads a file path.

## Usage

Host-side clipboard and screenshot import currently supports Linux Wayland and
X11 desktops. The read-only inbox mount itself is platform-independent.

Start a session with the inbox mounted:

```bash
enclave run --image-inbox
```

This bind-mounts the **shared, global** host inbox read-only at `/mnt/host-images`
(also exported to the agent as `$ENCLAVE_IMAGE_INBOX`). The same directory
(`~/.cache/enclave/inbox/`) is mounted into *every* `--image-inbox` session, so an
image imported once is visible to all of them. Then, from the host, import
whatever is on your clipboard:

```bash
enclave img import            # reads the clipboard
enclave img import --screenshot   # region screenshot instead
```

`img import` writes to the shared inbox — no session targeting needed, so it
works from anywhere (including a desktop hotkey with no project context). It
prints the container path (`/mnt/host-images/<name>`) and, unless `--no-copy` is
given, also copies that path onto your host clipboard — so your next paste in the
agent yields the path. Point the agent at that path. If no `--image-inbox`
session is running yet, the image is still written and any session you start
later will see it.

> The paste is done by your **terminal**, not the container: it types the
> host-clipboard text over the TTY. Use your terminal's paste shortcut (usually
> `Ctrl+Shift+V`, or middle-click; `Cmd+V` on macOS) — a plain `Ctrl+V` sends a
> control character in most terminals. You're pasting the *path*, not the image;
> the agent reads the file at that path.

## Automating the import (hotkey)

The design forbids background clipboard watching (that would let the agent see
every image you ever copy — see the security contract). A **global hotkey** is
the sanctioned automation: one keypress is still an explicit, discrete trigger,
so the contract holds. Bind the command in your desktop environment:

```
# sway / i3
bindsym $mod+Shift+v exec enclave img import

# Hyprland
bind = $mod SHIFT, V, exec, enclave img import

# GNOME: Settings → Keyboard → Custom Shortcuts → command: enclave img import
# KDE:   System Settings → Shortcuts → Custom → command
# X11 generic (sxhkd):
#   super + shift + v
#       enclave img import
```

Then the whole flow is: copy an image → press the hotkey → the path is on your
clipboard → paste it into the agent. Because the inbox is global, the hotkey
needs no project or session context — it just writes to the shared inbox.

### Requirements

- **Wayland:** `wl-clipboard` (`wl-paste`/`wl-copy`); screenshots need `grim` + `slurp`.
- **X11:** `xclip`; screenshots need `maim`.

The tools are only needed when you *import* — starting a session validates
nothing. Missing tools produce an actionable error naming the package.

## Security contract

1. **Nothing display-shaped in the container.** No X11 socket, no Wayland
   socket, no session D-Bus, no portal, no compositor — only one read-only bind
   mount of a host-controlled directory.
2. **Explicit trigger only.** Every import is a discrete user action. There is
   no clipboard watching or polling; an image you never imported can never reach
   the agent.
3. **The agent cannot pull.** Import runs host-side; the container has no channel
   to invoke it. The mount is read-only, so the agent can only read the images
   you imported — never write, delete, or request more.
4. **Validated content only.** MIME allow-list (`image/png`, `image/jpeg`), a
   hard 10 MiB size cap, a magic-byte sniff that must agree with the advertised
   clipboard MIME, and atomic mode-0600 writes into the 0700 global inbox
   directory `~/.cache/enclave/inbox/`.

### Scope: the inbox is global

The inbox is deliberately **shared across all `--image-inbox` sessions on the
host** — that is what makes "copy anywhere, one hotkey, available everywhere"
work without project/session targeting. The consequence: any `--image-inbox`
session (in *any* project) can read *every* image you have imported, not just
the ones "meant" for it. A compromised agent can `ls /mnt/host-images` and read
them all — pasting a path is not an access control.

So residual exposure is: exactly the images you deliberately imported, readable
by any inbox-enabled session, until they are removed. This is looser than
enclave's usual per-project isolation; only enable `--image-inbox` on sessions
you're comfortable seeing all imported images. Sessions **without** the flag get
no inbox mount and see nothing.

The inbox is not removed on session teardown (sessions share it). Clear it with
`enclave cleanup --all`, or just delete `~/.cache/enclave/inbox/`.
