# vnc feature

Opt-in feature that gives a session a **contained GUI**: a virtual X display
(TigerVNC's `Xvnc`) running a fullscreened Chromium, served over **VNC (RFB)**.
The raw RFB port is published on the host loopback, so you can attach any VNC
client of your choice.

Enable it:

```bash
enclave --features +vnc …
```

## Connecting a VNC client

The RFB port (container `5900`) is published with an OS-assigned host port on
the loopback interface, so concurrent sessions get distinct ports. Find the
published port with `enclave ps --json` (look for the `5900` container port),
then read the per-session password out of the container and point your client
at it:

```bash
docker exec <container> cat /tmp/enclave-vnc/vnc-password
vncviewer 127.0.0.1:<published-host-port>
```

The password is required (see [Access control](#access-control)). It only ever
gates that one session's display, so reading it out of the container is safe.

## What runs in the container

`commands.startup` launches `vnc-supervisor` (installed to `/usr/local/bin`)
as the sandbox user. It keeps three components alive with per-component restart
loops, logging to `/tmp/enclave-vnc/log/`:

1. **Xvnc**: virtual X display `:99`, RFB server on the published port
   (default `5900`), VncAuth required. It listens on all container interfaces
   (no `-localhost`) so the host's loopback-published port reaches it.
2. **matchbox-window-manager**: fullscreens every window (kiosk-style).
3. **Chromium**: headful on the virtual display. It starts on a local
   waiting page and stays there until a page is opened. When `$VNC_URL` is set
   (default `about:blank`, meaning no target), a one-shot watcher probes it and
   forwards it into the running browser once its TCP port accepts connections.
   Loading the URL directly would instead park the display on a
   connection-refused error page whenever the target server starts later than
   the stack. Sessions can also drive the browser on demand via `vnc-open`,
   which is how a consuming feature opens a URL it only knows at runtime.
   Chromium's own sandbox is disabled, because the default Docker seccomp
   profile blocks the unprivileged user namespaces it needs, so the container
   remains the isolation boundary.

The feature entrypoint additionally exports `DISPLAY=:99` and
`BROWSER=/usr/local/bin/vnc-open`, and `install.sh` registers `vnc-open` as
the image-wide `x-scheme-handler` for http/https (desktop entry plus
`/etc/xdg/mimeapps.list`), so X clients and "open in browser" flows land on
the contained display. The handler registration matters because `xdg-open`
resolves scheme handlers via `xdg-mime` *before* falling back to `$BROWSER`:
without it, the apt-installed `chromium.desktop` wins, crashes sandbox-less,
and silently drops the URL. `vnc-open <url>` reuses the supervisor's Chromium
profile, waiting briefly for its singleton if the stack is still booting, so
URLs open in the running browser instead of racing it.

## Access control

The supervisor generates a random password on first start and enforces it at
the RFB layer (VncAuth), so Xvnc demands it from every client. It writes two
copies:

- obfuscated auth file: `/tmp/enclave-vnc/rfb-passwd` (Xvnc)
- plaintext: `/tmp/enclave-vnc/vnc-password` (mode 0600)

That password is what shapes the boundary. Holding it is what grants control
of the display, and nothing else does. It is generated per session, so it
grants control of exactly one session's display and no other.

The plaintext path is what a VNC client user reads (see
[Connecting a VNC client](#connecting-a-vnc-client)), and it doubles as the
**integration contract** for a trusted host-side viewer, which can pick the
password up with `docker exec <container> cat /tmp/enclave-vnc/vnc-password`.
Because its reach stops at that one session's own display, the (untrusted)
agent knowing it is harmless.

## Configuration

Environment variables read by the supervisor (set via a consuming feature's
`environment.variables` or `-e`):

| Variable | Default | Meaning |
|----------|---------|---------|
| `VNC_URL` | `about:blank` | Optional URL auto-forwarded into the browser once its port is reachable. Left unset (the default), the display stays on the waiting page and sessions open pages on demand via `vnc-open`. |
| `VNC_GEOMETRY` | `1600x1000` | Initial display size (a resize-capable client can change it) |
| `VNC_DISPLAY` | `:99` | X display number |
| `VNC_RFB_PORT` | `5900` | RFB port (must match the spec's `ports:` declaration if changed) |

A consuming feature should leave `VNC_URL` unset whenever the page it wants is
only determined at runtime, and call `vnc-open` with the full URL instead.
Auto-forwarding a bare server root in that situation lands the display on a
default view, which can clobber whatever state the intended URL would have
selected.

## Residual risks

- Holding the password is sufficient to drive the display, so keep it to
  trusted local viewers.
- A human can be phished by what the streamed page *shows*. A viewer cannot
  vouch for the session's content.
- Clipboard crossing: Xvnc syncs the display's X selections with the RFB
  clipboard natively (`SendCutText`/`AcceptCutText`/`SetPrimary`/`SendPrimary`,
  all on by default), so any authenticated RFB client can exchange clipboard
  text with the session. Nothing in this feature gates that. A viewer built on
  top of it has to mediate the clipboard itself if it wants to.

## Cost

Chromium + X + VNC + fonts add roughly 600 MB to the image and a persistent
browser process to the session, hence `defaultEnabled: false`.
