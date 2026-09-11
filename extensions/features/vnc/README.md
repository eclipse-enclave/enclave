# vnc feature

Opt-in feature that gives a session a **contained GUI**: a virtual X display
(TigerVNC's `Xvnc`) running a fullscreened Chromium, served over **VNC (RFB)**.
The raw RFB port is published on the host loopback, so you can attach any VNC
client of your choice.

Enable it:

```bash
enclave --features +vnc …
```

The feature needs a base image whose archive ships Chromium as a deb. The
default Debian base does; on an Ubuntu base, which only ships Chromium as a
snap, the install fails and the build stops with an error naming this feature.

## Connecting a VNC client

The RFB port (container `5900`) is published with an OS-assigned host port on
the loopback interface, so concurrent sessions get distinct ports. The session
prints the resolved `vnc://localhost:<port>` at startup; `enclave ps --json`
reports it too (look for container port `5900`). Read the per-session password
out of the session and point your client at it:

```bash
enclave exec --name <session> -- cat /tmp/enclave-vnc/vnc-password
vncviewer 127.0.0.1:<published-host-port>
```

## What runs in the container

`commands.startup` launches `vnc-supervisor` (installed to `/usr/local/bin`)
as the sandbox user. It keeps three components alive with per-component restart
loops, logging to `/tmp/enclave-vnc/log/`:

1. **Xvnc**: virtual X display `:99`, RFB server on the published container
   port `5900`, VncAuth required. It listens on all container interfaces
   (no `-localhost`) so the host's loopback-published port reaches it.
2. **matchbox-window-manager**: fullscreens every window (kiosk-style).
3. **Chromium**: headful on the virtual display. It starts on a local
   waiting page and stays there until a page is opened. When
   `$ENCLAVE_VNC_URL` is set, a one-shot watcher probes it and forwards it
   into the running browser once its TCP port accepts connections. Loading the
   URL directly would instead park the display on a connection-refused error
   page whenever the target server starts later than the stack. Sessions can
   also drive the browser on demand via `vnc-open`, which is how a consuming
   feature opens a URL it only knows at runtime.

Both the supervisor and `vnc-open` launch Chromium through the shared
`/usr/local/bin/vnc-chromium` wrapper, so the browser behaves the same however
it was started; that script's header documents the switches it sets and why.

The feature entrypoint additionally exports `DISPLAY=:99` and
`BROWSER=/usr/local/bin/vnc-open`, and `install.sh` registers `vnc-open` as
the image-wide `x-scheme-handler` for http/https (desktop entry plus
`/etc/xdg/mimeapps.list`), so X clients and "open in browser" flows land on
the contained display (`vnc-open`'s header explains why the scheme-handler
registration, not just `$BROWSER`, is load-bearing). `vnc-open <url>` reuses
the supervisor's Chromium profile, waiting briefly for its singleton if the
stack is still booting, so URLs open in the running browser instead of racing
it, and logs to `/tmp/enclave-vnc/log/vnc-open.log`.

A set `DISPLAY` is also how many tools decide a GUI is available, so with this
feature enabled `gpg` pinentry, `SSH_ASKPASS`, and `GIT_ASKPASS` prompts render
on the contained display rather than in the terminal. Check the VNC client if
an interactive command appears to hang.

## Access control

The supervisor generates a random password on first start and enforces it at
the RFB layer (VncAuth), so Xvnc demands it from every client. It writes two
copies:

- obfuscated auth file: `/tmp/enclave-vnc/rfb-passwd` (Xvnc)
- plaintext: `/tmp/enclave-vnc/vnc-password` (mode 0600)

Holding that password is what grants control of the display, and nothing else
does. It is generated per session, so it reaches exactly one session's display
and no other — which is why the (untrusted) agent knowing it is harmless, and
why reading it out of the session is safe.

The plaintext path is the **integration contract** for a trusted host-side
viewer: `/tmp/enclave-vnc/vnc-password` inside the session, alongside the
container port `5900` binding that `enclave ps --json` reports. The path is the
contract; how a viewer reads it is up to the backend it drives. `enclave exec`
always allocates a TTY, so it serves the interactive flow above but not a
headless one — a non-interactive viewer needs a backend-level read (for Docker,
`docker exec`) until the CLI grows a non-TTY exec.

## Configuration

Environment variables read by the supervisor (set via a consuming feature's
`environment.variables` or `-e`):

| Variable | Default | Meaning |
|----------|---------|---------|
| `ENCLAVE_VNC_URL` | unset | Optional URL auto-forwarded into the browser once its port is reachable. Left unset, the display stays on the waiting page and sessions open pages on demand via `vnc-open`. |
| `ENCLAVE_VNC_URL_WAIT_SECONDS` | `300` | How long that forward waits for the URL's port before giving up and logging. |
| `ENCLAVE_VNC_GEOMETRY` | `1600x1000` | Initial display size (a resize-capable client can change it) |
| `ENCLAVE_VNC_DISPLAY` | `:99` | X display number |

The RFB port is not configurable: it must match the `ports:` declaration in
`spec.yaml` (container port `5900`), so the supervisor hardcodes it.

A consuming feature should leave `ENCLAVE_VNC_URL` unset whenever the page it
wants is only determined at runtime, and call `vnc-open` with the full URL
instead. Auto-forwarding a bare server root in that situation lands the display
on a default view, which can clobber whatever state the intended URL would have
selected.

## Troubleshooting

The entrypoint starts the supervisor with its stdout and stderr discarded, so
the files under `/tmp/enclave-vnc/log/` are the only record. Start with
`supervisor.log` (startup, auth-file generation, URL forwarding), then
`xvnc.log`, `wm.log`, `browser.log`, and `vnc-open.log` for the individual
components.

## Residual risks

[Security boundaries](../../../docs/security/README.md#published-ports-and-contained-displays)
covers how a published display fits the overall threat model. Feature-specific:

- Holding the password is sufficient to drive the display, so keep it to
  trusted local viewers.
- The host publish is loopback-only, but Xvnc listens on all interfaces
  inside the container's network namespace. Under network isolation that
  namespace belongs to the session's gateway container on a shared Docker
  bridge, so other containers on that bridge (including other sessions'
  gateways) can reach the RFB port directly, with VncAuth as the only gate.
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
