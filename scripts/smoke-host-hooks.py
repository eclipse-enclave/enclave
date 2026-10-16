#!/usr/bin/env python3
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

"""Live host-hook checks using isolated roots and disposable sessions.

Requires a prebuilt claude image and a running container engine. No builds,
installed user extensions, credentials, or existing sessions are changed.
CLI_BIN, SMOKE_IMAGE and SMOKE_BACKEND override the defaults. The isolated
smoke root is retained for inspection. Herdr acquisition is not tested here.
"""
import json
import os
import pathlib
import pty
import select
import shutil
import signal
import subprocess
import tempfile
import time

repo = pathlib.Path(__file__).resolve().parents[1]
binary = os.environ.get('CLI_BIN', str(repo / 'bin/enclave'))
backend = os.environ.get('SMOKE_BACKEND', 'docker')
image = os.environ.get('SMOKE_IMAGE', 'enclave-claude:latest')
root = pathlib.Path(tempfile.mkdtemp(prefix='enclave-hooks-live-'))
home = root / 'home'
home.mkdir()
(home / '.gitconfig').write_text('[user]\n name = Hook Smoke\n email = hooks@example.invalid\n')
project = root / 'project'
project.mkdir()
config = root / 'config' / 'enclave'
config.mkdir(parents=True)
shutil.copytree(repo / 'examples/host-hooks/features/session-tint', config / 'extensions/features/session-tint')
(config / 'config.json').write_text(json.dumps({'tool': 'claude', 'backend': backend, 'host_hooks': {'feature:session-tint': {'enabled': True, 'settings': {'color': '#2a0f12'}}, 'builtin:session-tint': {'enabled': False}, 'user:smoke': {'enabled': True}}}))
provider = config / 'hooks/host/smoke'
provider.mkdir(parents=True)
(provider / 'manifest.json').write_text('{"schemaVersion":"1","phases":{"prepare":"prepare"}}')
(provider / 'prepare').write_text("#!/usr/bin/env python3\nimport json,os,sys\nr=json.load(sys.stdin)\nwith open(os.environ['HOOK_LOG'],'a') as f:f.write(json.dumps({'request':r,'pid':os.getppid()})+'\\n')\nprint(json.dumps({'schemaVersion':'1','hostEnvironment':{'setIfAbsent':{'HOOK_SMOKE_ENV':'one'}},'restartHostProcess':True}))\n")
(provider / 'prepare').chmod(0o755)
env = {k: v for k, v in os.environ.items() if k in ['PATH', 'DOCKER_HOST', 'DOCKER_CONTEXT', 'XDG_RUNTIME_DIR', 'LANG', 'TERM']}
env.update(HOME=str(home), XDG_CONFIG_HOME=str(root / 'config'), XDG_STATE_HOME=str(root / 'state'), XDG_CACHE_HOME=str(root / 'cache'), ENCLAVE_HOME=str(repo), ENCLAVE_COLOR='always', GIT_AUTHOR_NAME='Hook Smoke', GIT_AUTHOR_EMAIL='hooks@example.invalid', GIT_COMMITTER_NAME='Hook Smoke', GIT_COMMITTER_EMAIL='hooks@example.invalid', HOOK_LOG=str(root / 'requests.jsonl'))
base = [binary, '--tool', 'claude', '--backend', backend, '--image-name', image, '--no-rebuild', '--allow-all-network', '--host-config', 'none', '--slim', '--auth-scope', 'project']

def run(args, tty=True, detach=False, timeout=35, interaction=None, expected_code=0):
    invocation = base if not any((a in ['exec', 'attach'] for a in args)) else [binary, '--tool', 'claude', '--backend', backend]
    if not tty:
        master, slave = pty.openpty()
        try:
            p = subprocess.run(invocation + args, cwd=project, env=env, stdin=slave, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        finally:
            os.close(master)
            os.close(slave)
        if p.returncode:
            raise RuntimeError((p.returncode, p.stdout.decode(), p.stderr.decode()))
        return (p.stdout, p.stderr, p.pid if hasattr(p, 'pid') else 0)
    master, slave = pty.openpty()
    p = subprocess.Popen(invocation + args, cwd=project, env=env, stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
    os.close(slave)
    data = b''
    sent = False
    handled = False
    start = time.monotonic()
    while time.monotonic() - start < timeout:
        if select.select([master], [], [], 0.1)[0]:
            try:
                chunk = os.read(master, 65536)
            except OSError:
                break
            if not chunk:
                break
            data += chunk
        if interaction == 'ctrlc' and b'HOOK-READY' in data and (not sent):
            os.write(master, b'\x03')
            sent = True
        if interaction == 'ctrlc' and b'HOOK-HANDLED' in data and (not handled):
            assert b'\x1b]111\x07' not in data and p.poll() is None, data
            os.write(master, b'exit\n')
            handled = True
        if interaction == 'term' and b'\x1b]11;#2a0f12\x07' in data and (not sent):
            p.send_signal(signal.SIGTERM)
            sent = True
        if detach and b'\x1b]11;#2a0f12\x07' in data and (not sent):
            time.sleep(0.5)
            os.write(master, b'\x10\x11')
            sent = True
        if p.poll() is not None:
            break
    try:
        # PTY EOF can precede waitpid visibility; allow normal process cleanup.
        p.wait(timeout=max(0.2, timeout - (time.monotonic() - start)))
    except subprocess.TimeoutExpired:
        os.killpg(p.pid, signal.SIGTERM)
        try:
            p.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(p.pid, signal.SIGKILL)
            p.wait()
        raise RuntimeError('timed out ' + repr(data[-4000:]))
    os.close(master)
    if p.returncode != expected_code:
        raise RuntimeError((p.returncode, data.decode(errors='replace')))
    return (data, b'', p.pid)
name = None
try:
    out, _, pid = run(['--ephemeral', 'shell', '--', '-c', 'printf "HOOK-RUN:%s\\n" "${HOOK_SMOKE_ENV-unset}"'])
    assert b'HOOK-RUN:unset' in out, out
    assert out.count(b'\x1b]11;#2a0f12\x07') == 1 and out.count(b'\x1b]111\x07') == 1, out
    requests = [json.loads(x) for x in (root / 'requests.jsonl').read_text().splitlines()]
    assert len(requests) == 1 and requests[0]['pid'] == pid, requests
    print('PASS foreground external tint, same PID replay once, no implicit env injection', flush=True)
    out, _, _ = run(['--ephemeral', 'shell', '--', '-c', 'printf REDIRECTED'], tty=False)
    assert b'REDIRECTED' in out and b'\x1b]11;' not in out, out
    print('PASS redirected foreground has no terminal effects', flush=True)
    override = config / 'extensions/tools/claude'
    override.mkdir(parents=True)
    (override / 'spec.yaml').write_text('schemaVersion: "1"\nkind: sandbox\nname: claude\nsandbox:\n  entrypoint: {run: [sleep, infinity]}\n  yoloEnabled: false\n')
    out, err, _ = run(['--background', '--name', 'hook-smoke'], tty=False)
    name = out.decode().strip().splitlines()[-1]
    assert name.startswith('enclave-') and b'\x1b]11;' not in out, (out, err)
    print('PASS background output retains final container name, with no hook output or terminal effects', flush=True)
    out, _, _ = run(['--name', name, 'exec', '--', '/bin/sh', '-c', 'printf HOOK-EXEC'])
    assert b'HOOK-EXEC' in out and out.count(b'\x1b]11;#2a0f12\x07') == 1 and (out.count(b'\x1b]111\x07') == 1), out
    print('PASS actual exec external tint/reset', flush=True)
    before = len((root / 'requests.jsonl').read_text().splitlines())
    out, _, _ = run(['attach', '--detach-keys', 'ctrl-p,ctrl-q', name], detach=True)
    assert out.count(b'\x1b]11;#2a0f12\x07') == 1 and out.count(b'\x1b]111\x07') == 1, out
    assert len((root / 'requests.jsonl').read_text().splitlines()) == before
    print('PASS actual attach detach/reset with no prepare', flush=True)
    out, _, _ = run(['--ephemeral', 'shell', '--', '-c', 'trap \'printf HOOK-HANDLED\' INT; printf HOOK-READY; while :; do read -r line; [ "$line" = exit ] && break; done'], interaction='ctrlc')
    assert out.count(b'\x1b]111\x07') == 1, out
    print('PASS agent-handled Ctrl-C retains tint until session exits', flush=True)
    out, _, _ = run(['--ephemeral', 'shell', '--', '-c', 'sleep 90'], interaction='term', expected_code=130)
    assert out.count(b'\x1b]111\x07') == 1, out
    print('PASS SIGTERM during foreground start resets exactly once and returns interrupted code', flush=True)
finally:
    if name:
        subprocess.run([binary, '--backend', backend, 'stop', name], cwd=project, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=25)
        subprocess.run([backend, 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
    print('isolated smoke root:', root, flush=True)
