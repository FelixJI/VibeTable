#!/usr/bin/env python3
"""First-run installer for the bundled local Directus (online, app-private).

This is the entry point an installer/launcher calls on the customer's machine
the first time the app runs. It pulls Directus 12 (+ the pinned, C++20-capable
``isolated-vm@6.1.2``) via ``npm install`` **entirely into this folder** —
nothing is written to the global npm cache, global prefix, or system PATH.

Why a separate script (not just ``run.py``):
  * The installer wants a clear install-only step that can be run once, report
    success/failure, and exit — separate from the long-running server.
  * It verifies the native binary actually loaded (the most fragile step) so a
    broken install is caught immediately, not on first request.

Pre-requisites on the customer machine:
  * Node.js 24.x reachable on PATH (the host app's launcher is expected to have
    either prompted the user to install it, or shipped a portable copy).
  * Internet access for the one-time ``npm install`` (~600 MB download).

Run::

    python scripts/local_directus/install.py

Exit code 0 = ready to start; non-zero = see stderr.
"""

from __future__ import annotations

import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import run  # noqa: E402  — reuse the launcher's install logic


def _ok(msg: str) -> None:
    print(f"[install] {msg}", flush=True)


def main() -> int:
    _ok("online first-run install (app-private, no global pollution)")
    run.ensure_npm_installed()

    # Verify the most fragile piece actually loads: the isolated-vm native
    # module. If this fails the install is incomplete and Directus will crash
    # at startup (it needs isolated-vm for the extension sandbox runtime).
    proc = _verify_native()
    if proc != 0:
        print(
            "[install] FAILED: isolated-vm native module did not load. "
            "This usually means a Node/VS ABI mismatch. Re-run after ensuring "
            "Node.js 24.x is on PATH.",
            file=sys.stderr,
        )
        return proc

    _ok("isolated-vm native binary loads correctly")
    _ok("install complete. Next: run.py will bootstrap the DB and start Directus.")
    return 0


def _verify_native() -> int:
    """Return 0 if isolated-vm's native binding loads under this Node, else 1."""
    import shutil
    import subprocess

    node = shutil.which("node")
    if not node:
        print("[install] WARN: node not on PATH; skipping native verify", file=sys.stderr)
        return 0
    snippet = (
        "try { const m = require('./node_modules/isolated-vm'); "
        "new m.Isolate(); console.log('IVM_OK'); } "
        "catch (e) { console.error('IVM_FAIL:', e.message); process.exit(1); }"
    )
    proc = subprocess.run(
        [node, "-e", snippet],
        cwd=HERE,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if proc.returncode != 0 or "IVM_OK" not in (proc.stdout or ""):
        print(f"[install] native verify output: {proc.stdout}{proc.stderr}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
