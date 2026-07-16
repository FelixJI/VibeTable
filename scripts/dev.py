#!/usr/bin/env python3
"""One-click dev launcher: local Directus 12 + WPF client, fully isolated.

Brings up the whole VibeTable stack for real data testing on a developer's
workstation — no Docker, no manual env juggling:

1. Start a local Directus 12 (SQLite) via ``scripts/local_directus/run.py``
   logic — runtime ``npm install``, auto-generated secrets, port-conflict
   auto-evasion, schema bootstrapped on first run.
2. Build the WPF host (Release) if stale/missing.
3. Launch the WPF client with ``VIBETABLE_DIRECTUS_URL`` / ``VIBETABLE_DIRECTUS_PROJECT``
   pointed at the local Directus. The host itself starts the Python backend
   (``.venv\\Scripts\\python.exe -m backend``) — so the entire front+back+data
   plane comes up from this one command.

The Directus + client processes run with an environment that is isolated from
the caller's: only the variables needed for the run are injected, so it never
clashes with a globally-configured ``VIBETABLE_DIRECTUS_URL``.

Run::

    .venv\\Scripts\\python.exe scripts\\dev.py            # full stack
    .venv\\Scripts\\python.exe scripts\\dev.py --no-host  # Directus only

Stop everything with Ctrl+C.
"""

from __future__ import annotations

import argparse
import contextlib
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
LOCAL_DIRECTUS = ROOT / "scripts" / "local_directus"
LOCAL_DIRECTUS_RUN = LOCAL_DIRECTUS / "run.py"
HOST_PROJECT = ROOT / "desktop" / "src" / "VibeTable.Desktop"
HOST_EXE = HOST_PROJECT / "bin" / "Release" / "net10.0-windows" / "VibeTable.Desktop.exe"
PREFERRED_DOTNET = Path(r"C:\Program Files\dotnet\dotnet.exe")
DOTNET = (
    str(PREFERRED_DOTNET) if PREFERRED_DOTNET.is_file() else (shutil.which("dotnet") or "dotnet")
)
PYTHON = sys.executable
DIRECTUS_READY_TIMEOUT = 120.0
_PROCS: list[subprocess.Popen[str]] = []


def _info(msg: str) -> None:
    print(f"[dev] {msg}", flush=True)


def _start_local_directus() -> str:
    """Start the local Directus (reusing run.py) and return its base URL.

    We import run.py as a module so its install/bootstrap/scheme steps run
    deterministically, then launch ``directus start`` ourselves so we own the
    process lifetime and can stream/teardown it alongside the client.
    """
    sys.path.insert(0, str(LOCAL_DIRECTUS))
    import importlib

    run = importlib.import_module("run")  # scripts/local_directus/run.py
    run.ensure_npm_installed()
    env_values = run.materialize_env()
    port = int(env_values.get("PORT", run.DEFAULT_PORT))
    port = run.pick_port(port)
    if port != int(env_values.get("PORT", run.DEFAULT_PORT)):
        env_values["PORT"] = str(port)
        run.ENV_FILE.write_text(run._render_env(env_values), encoding="utf-8")
    run.link_bulk_mutation_extension()
    run.ensure_directories(env_values)
    run.bootstrap_database()

    base_url = f"http://localhost:{port}"
    _info(f"starting local Directus on {base_url} ...")
    proc = run.start_directus(str(port))
    _PROCS.append(proc)
    # Wait for readiness.
    deadline = time.monotonic() + DIRECTUS_READY_TIMEOUT
    last = ""
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(f"{base_url}/server/ping", timeout=3) as r:
                if r.status == 200:
                    _info(f"Directus ready at {base_url}")
                    return base_url
        except OSError as exc:
            last = repr(exc)
        time.sleep(0.5)
    raise RuntimeError(f"Directus did not become ready ({last})")


def _host_is_stale() -> bool:
    if not HOST_EXE.is_file():
        return True
    built_at = HOST_EXE.stat().st_mtime
    inputs = [
        *HOST_PROJECT.rglob("*.cs"),
        *HOST_PROJECT.rglob("*.xaml"),
        *HOST_PROJECT.rglob("*.csproj"),
    ]
    return any(path.stat().st_mtime > built_at for path in inputs)


def _ensure_host_built() -> Path:
    if not _host_is_stale():
        _info(f"WPF host up to date: {HOST_EXE}")
        return HOST_EXE
    if not Path(DOTNET).is_file():
        raise RuntimeError(
            "dotnet SDK not found; build the host manually: "
            "dotnet build desktop/VibeTable.Desktop.sln --configuration Release"
        )
    _info("building WPF host (Release) ...")
    proc = subprocess.run(
        [DOTNET, "build", str(HOST_PROJECT), "--configuration", "Release"],
        cwd=ROOT,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
        timeout=600,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"WPF host build failed:\nstdout:\n{proc.stdout}\nstderr:\n{proc.stderr}"
        )
    if not HOST_EXE.is_file():
        raise RuntimeError(f"host executable not found after build: {HOST_EXE}")
    _info(f"WPF host built: {HOST_EXE}")
    return HOST_EXE


def _launch_host(
    base_url: str | None = None, host_directus_auto: bool = False
) -> subprocess.Popen[str]:
    """Launch the WPF client with an isolated Directus env.

    The env is built fresh (PATH + system basics + only the VIBETABLE_DIRECTUS_*
    vars for this run) so a globally-set VIBETABLE_DIRECTUS_URL cannot leak in or
    conflict.

    When ``host_directus_auto`` is True, the host is launched with
    ``--directus-auto`` and brings up Directus itself; no ``VIBETABLE_DIRECTUS_URL``
    is injected (the host sets it once its own Directus supervisor is ready).
    """
    host = _ensure_host_built()
    argv = [str(host)]
    env = {
        # Minimal, isolated environment: system paths + the run-specific vars.
        "PATH": os.environ.get("PATH", ""),
        "SYSTEMROOT": os.environ.get("SYSTEMROOT", r"C:\WINDOWS"),
        # WPF's font cache (MS.Internal.FontCache.Util) builds its fonts URI
        # from ``windir`` (NOT SystemRoot); without it the CoreWebView2-less
        # type init throws UriFormatException and the host dies before any
        # window appears. Keep it in the isolated env.
        "WINDIR": os.environ.get("WINDIR", r"C:\WINDOWS"),
        "TEMP": os.environ.get("TEMP", ""),
        "TMP": os.environ.get("TMP", ""),
        "USERPROFILE": os.environ.get("USERPROFILE", ""),
        "LOCALAPPDATA": os.environ.get("LOCALAPPDATA", ""),
    }
    if host_directus_auto:
        argv.append("--directus-auto")
        _info(f"launching WPF client -> {host} --directus-auto (host owns Directus)")
    else:
        # The ONLY Directus routing the host sees:
        env["VIBETABLE_DIRECTUS_URL"] = base_url or ""
        env["VIBETABLE_DIRECTUS_PROJECT"] = "default"
        _info(f"launching WPF client -> {host}")
        _info(f"  VIBETABLE_DIRECTUS_URL = {base_url}")
        _info("  Directus admin: see scripts/local_directus/.env (ADMIN_EMAIL/ADMIN_PASSWORD)")
    proc = subprocess.Popen(
        argv,
        cwd=HOST_EXE.parent,
        env=env,
    )
    _PROCS.append(proc)
    return proc


def _cleanup(*_: object) -> None:
    _info("shutting down ...")
    for proc in reversed(_PROCS):
        if proc.poll() is None:
            try:
                proc.terminate()
                proc.wait(timeout=10)
            except (subprocess.TimeoutExpired, OSError):
                proc.kill()
    # Best-effort: free the Directus port if the child lingered.
    with contextlib.suppress(OSError, subprocess.SubprocessError):
        subprocess.run(
            [
                "cmd",
                "/c",
                "for /f \"tokens=5\" %a in ('netstat -ano ^| findstr :8055 ^| findstr LISTENING') do taskkill /F /PID %a",
            ],
            shell=False,
            capture_output=True,
            timeout=15,
        )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--no-host",
        action="store_true",
        help="start only the local Directus; do not build/launch the WPF client",
    )
    parser.add_argument(
        "--no-directus",
        action="store_true",
        help="skip starting Directus (use when it is already running); "
        "requires VIBETABLE_DIRECTUS_URL or --directus-url",
    )
    parser.add_argument(
        "--directus-url",
        default=None,
        help="explicit Directus base URL (overrides local start)",
    )
    parser.add_argument(
        "--host-directus-auto",
        action="store_true",
        help="dev.py starts only the WPF host (with --directus-auto); the host "
        "itself brings up and supervises the local Directus. Use this to test "
        "the production --directus-auto path; the host then owns Directus's "
        "lifetime (no separate dev.py-managed Directus process).",
    )
    args = parser.parse_args()

    signal.signal(signal.SIGINT, _cleanup)
    signal.signal(signal.SIGTERM, _cleanup)

    try:
        if args.host_directus_auto:
            # Host-managed mode: the WPF host runs with --directus-auto and
            # brings up Directus itself. dev.py only builds + launches the host.
            _launch_host(host_directus_auto=True)
            _info("host launched with --directus-auto (host owns Directus). Ctrl+C to stop.")
            for proc in _PROCS:
                proc.wait()
            return 0

        if args.directus_url:
            base_url = args.directus_url.rstrip("/")
        elif args.no_directus:
            base_url = os.environ.get("VIBETABLE_DIRECTUS_URL", "").rstrip("/")
            if not base_url:
                raise SystemExit("--no-directus requires --directus-url or VIBETABLE_DIRECTUS_URL")
        else:
            base_url = _start_local_directus()

        if args.no_host:
            _info("--no-host: Directus is running. Press Ctrl+C to stop.")
            _info(f"  URL: {base_url}")
            signal.pause() if hasattr(signal, "pause") else time.sleep(1 << 30)
            return 0

        _launch_host(base_url)
        _info("stack is up. Ctrl+C to stop everything.")
        # Wait for the client to exit; cleanup runs via signal on Ctrl+C.
        for proc in _PROCS:
            proc.wait()
    except BaseException:
        _cleanup()
        raise
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
