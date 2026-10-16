#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install the working-tree Herdr and title examples for the enclave-test wrapper.
set -euo pipefail

setup_repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec python3 - "$setup_repo_dir" "$@" <<'PY'
import argparse
import filecmp
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile


repo = Path(sys.argv[1])
parser = argparse.ArgumentParser(
    prog="setup-host-hooks.sh",
    description="Refresh the enclave-test binary, then install and enable Herdr and title hooks.",
)
parser.add_argument("--dry-run", action="store_true", help="show changes without writing files")
args = parser.parse_args(sys.argv[2:])


def same_tree(source, target):
    if not target.is_dir() or target.is_symlink():
        return False
    source_files = {p.relative_to(source) for p in source.rglob("*")}
    target_files = {p.relative_to(target) for p in target.rglob("*")}
    if source_files != target_files:
        return False
    for relative in source_files:
        src, dst = source / relative, target / relative
        if dst.is_symlink() or src.is_dir() != dst.is_dir():
            return False
        if src.is_file() and (
            not dst.is_file()
            or src.read_bytes() != dst.read_bytes()
            or stat.S_IMODE(src.stat().st_mode) != stat.S_IMODE(dst.stat().st_mode)
        ):
            return False
    return True


def main():
    cli = shutil.which("enclave-test")
    if cli is None:
        raise ValueError("enclave-test is not on PATH")
    probe_env = dict(os.environ, ENCLAVE_HOME=str(repo))
    result = subprocess.run(
        [cli, "config", "--json", "--view", "effective"],
        capture_output=True, text=True, timeout=30, env=probe_env,
    )
    if result.returncode:
        raise ValueError("enclave-test config failed: " + result.stderr.strip())
    inspection = json.loads(result.stdout)
    config_path = Path(inspection.get("paths", {}).get("global", ""))
    if not config_path.is_absolute() or config_path.name != "config.json":
        raise ValueError("enclave-test did not report an absolute global config.json path")
    # This is the local wrapper's verified layout, not the normal enclave install.
    test_root = Path.home() / "temp/enclave-test"
    if config_path != test_root / "config/enclave/config.json":
        raise ValueError("enclave-test no longer uses the expected ~/temp/enclave-test layout")
    test_binary = (test_root / "enclave").resolve()
    built_binary = repo / "bin/enclave"
    if not built_binary.is_file() or not os.access(built_binary, os.X_OK):
        raise ValueError("missing executable bin/enclave; run make build first")
    probe_env.update(
        XDG_CONFIG_HOME=str(test_root / "config"),
        XDG_STATE_HOME=str(test_root / "state"),
        XDG_CACHE_HOME=str(test_root / "cache"),
    )
    candidate = subprocess.run(
        [str(built_binary), "config", "--json", "--view", "effective"],
        capture_output=True, text=True, timeout=30, env=probe_env,
    )
    if candidate.returncode:
        raise ValueError("built binary config check failed: " + candidate.stderr.strip())
    if "host_hooks" not in json.loads(candidate.stdout).get("effective", {}):
        raise ValueError("bin/enclave lacks host-hook support; rebuild this branch first")
    binary_changed = not test_binary.exists() or not filecmp.cmp(built_binary, test_binary, shallow=False)
    # Keep a config-file symlink intact while updating its actual target.
    config_target = config_path.resolve()
    config = json.loads(config_target.read_text()) if config_target.exists() else {}
    if not isinstance(config, dict):
        raise ValueError("the existing config must be a JSON object")
    hooks = config.setdefault("host_hooks", {})
    if not isinstance(hooks, dict):
        raise ValueError("the existing host_hooks must be a JSON object")

    installations = []
    for name, phase in (("herdr", "prepare"), ("session-title", "terminal-enter")):
        source = repo / "examples/host-hooks/features" / name / "hooks/host"
        manifest = json.loads((source / "manifest.json").read_text())
        if manifest.get("schemaVersion") != "1" or manifest.get("phases") != {phase: phase}:
            raise ValueError("unexpected example manifest: " + str(source))
        if not (source / "provider.py").is_file() or not os.access(source / phase, os.X_OK):
            raise ValueError("missing provider.py or executable handler: " + str(source))
        provider_id = "user:" + name
        entry = hooks.setdefault(provider_id, {})
        if not isinstance(entry, dict):
            raise ValueError(provider_id + " config must be a JSON object")
        entry["enabled"] = True
        destination = config_path.parent / "hooks/host" / name
        installations.append((provider_id, source, destination, not same_tree(source, destination)))

    payload = json.dumps(config, indent=2, ensure_ascii=False) + "\n"
    config_changed = not config_target.exists() or json.loads(config_target.read_text()) != config
    print("CLI:    " + cli)
    print("Binary: " + ("refresh " if binary_changed else "keep ") + str(test_binary))
    print("Config: " + str(config_path))
    for provider_id, _, destination, changed in installations:
        print(("Install " if changed else "Keep    ") + provider_id + " -> " + str(destination))
    print("Config: " + ("merge enabled=true for both providers" if config_changed else "already enabled"))
    if args.dry_run:
        print("Dry run: no files changed.")
        return
    if not binary_changed and not config_changed and not any(item[3] for item in installations):
        print("Already up to date.")
        return

    # Backups stay outside discovery roots, so they cannot become providers.
    backup_dir = None

    def backup(path, name):
        nonlocal backup_dir
        if not path.exists() and not path.is_symlink():
            return
        if backup_dir is None:
            stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
            backup_dir = config_path.parent / "backups/host-hook-setup" / stamp
            backup_dir.mkdir(parents=True, mode=0o700)
            print("Backups: " + str(backup_dir))
        target = backup_dir / name
        if path.is_symlink():
            target.symlink_to(os.readlink(path))
        elif path.is_dir():
            shutil.copytree(path, target, symlinks=True)
        else:
            shutil.copy2(path, target)

    if binary_changed:
        backup(test_binary, "enclave")
        fd, temporary = tempfile.mkstemp(prefix=".hook-binary-", dir=test_binary.parent)
        os.close(fd)
        try:
            shutil.copy2(built_binary, temporary)
            os.chmod(temporary, 0o755)
            os.replace(temporary, test_binary)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)

    for provider_id, source, destination, changed in installations:
        if not changed:
            continue
        backup(destination, provider_id.removeprefix("user:"))
        destination.parent.mkdir(parents=True, exist_ok=True)
        # Stage a complete copy before replacing the installed provider.
        with tempfile.TemporaryDirectory(prefix=".hook-setup-", dir=destination.parent) as temporary:
            staged = Path(temporary) / "provider"
            shutil.copytree(source, staged)
            if destination.is_symlink() or destination.is_file():
                destination.unlink()
            elif destination.exists():
                shutil.rmtree(destination)
            os.replace(staged, destination)

    if config_changed:
        backup(config_target, "config.json")
        config_target.parent.mkdir(parents=True, exist_ok=True)
        mode = stat.S_IMODE(config_target.stat().st_mode) if config_target.exists() else 0o600
        fd, temporary = tempfile.mkstemp(prefix=".hook-config-", dir=config_target.parent)
        try:
            with os.fdopen(fd, "w") as output:
                os.fchmod(output.fileno(), mode)
                output.write(payload)
            os.replace(temporary, config_target)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
    print("Enabled user:herdr and user:session-title; other settings were preserved.")
    print("Run enclave-test from a Herdr pane. No image rebuild is needed.")


try:
    main()
except (OSError, ValueError, TypeError, subprocess.SubprocessError) as error:
    print("Setup failed: " + str(error), file=sys.stderr)
    sys.exit(1)
PY
