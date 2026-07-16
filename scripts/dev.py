#!/usr/bin/env python3
"""Build + launch the WPF host. Directus and the Python backend come up on
their own — the host owns the whole stack now.

The host's startup decides whether to start a local Directus 12 (SQLite) for
this run:

* A bare launch (no flag, no ``VIBETABLE_DIRECTUS_URL``) starts the host which
  then brings up the local Directus *and* the Python backend itself.
* ``--directus-url <url>`` points the host at an external Directus instead.
* ``--no-directus-auto`` makes the host start only itself (e.g. to debug the UI
  against an already-running external Directus set via ``VIBETABLE_DIRECTUS_URL``).

So this script's only remaining job is the one thing a compiled exe cannot do
for itself: build the WPF host from source when it is stale, then launch it.
Run::

    .venv\\Scripts\\python.exe scripts\\dev.py                        # full stack
    .venv\\Scripts\\python.exe scripts\\dev.py --directus-url http://...   # remote Directus

Stop everything with Ctrl+C; the host owns teardown of its child processes.
"""

from __future__ import annotations

import argparse
import os
import shutil
import signal
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
HOST_PROJECT = ROOT / "desktop" / "src" / "VibeTable.Desktop"
HOST_EXE = HOST_PROJECT / "bin" / "Release" / "net10.0-windows" / "VibeTable.Desktop.exe"
PREFERRED_DOTNET = Path(r"C:\Program Files\dotnet\dotnet.exe")
DOTNET = (
    str(PREFERRED_DOTNET) if PREFERRED_DOTNET.is_file() else (shutil.which("dotnet") or "dotnet")
)
PYTHON = sys.executable
_PROCS: list[subprocess.Popen[str]] = []


def _info(msg: str) -> None:
    print(f"[dev] {msg}", flush=True)


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
    directus_url: str | None = None,
    no_directus_auto: bool = False,
) -> subprocess.Popen[str]:
    """Launch the WPF client with an isolated environment.

    The env is built fresh (system paths + only the VIBETABLE_DIRECTUS_* vars
    for this run) so a globally-set ``VIBETABLE_DIRECTUS_URL`` cannot leak in or
    conflict. The host then owns Directus and the Python backend lifetimes.
    """
    host = _ensure_host_built()
    argv = [str(host)]
    if no_directus_auto:
        argv.append("--no-directus-auto")
        _info("launching WPF client -> --no-directus-auto (host only)")
    else:
        _info("launching WPF client (host owns Directus + Python backend)")
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
    if directus_url:
        # The ONLY Directus routing the host sees:
        env["VIBETABLE_DIRECTUS_URL"] = directus_url
        env["VIBETABLE_DIRECTUS_PROJECT"] = "default"
        _info(f"  VIBETABLE_DIRECTUS_URL = {directus_url}")
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


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--directus-url",
        default=None,
        help="explicit external Directus base URL; the host connects to it "
        "instead of starting a local one",
    )
    parser.add_argument(
        "--no-directus-auto",
        action="store_true",
        help="start only the WPF host; do not let it auto-start a local "
        "Directus (use with VIBETABLE_DIRECTUS_URL or to debug the UI alone)",
    )
    args = parser.parse_args()

    signal.signal(signal.SIGINT, _cleanup)
    signal.signal(signal.SIGTERM, _cleanup)

    directus_url = args.directus_url.rstrip("/") if args.directus_url else None
    _launch_host(directus_url=directus_url, no_directus_auto=args.no_directus_auto)
    _info("stack is up. Ctrl+C to stop everything (the host owns its children).")
    # Wait for the client to exit; cleanup runs via signal on Ctrl+C.
    for proc in _PROCS:
        proc.wait()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
