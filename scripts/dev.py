#!/usr/bin/env python3
"""Build the development assets and launch the WPF host.

The host's startup decides whether to start a local Directus 12 (SQLite) for
this run:

* A bare launch (no flag, no ``VIBETABLE_DIRECTUS_URL``) starts the host which
  then brings up the local Directus *and* the Python backend itself.
* ``--directus-url <url>`` points the host at an external Directus instead.
* ``--no-directus-auto`` makes the host start only itself (e.g. to debug the UI
  against an already-running external Directus set via ``VIBETABLE_DIRECTUS_URL``).

The script builds the WebView application and every first-party Directus
extension when their source is newer than the ignored build output. It then
runs an incremental MSBuild build (MSBuild owns project-graph staleness) and
launches the host.
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

# Run both as ``python scripts/dev.py`` (sibling import) and as a package
# member; mirrors the try/except used for ``versioning`` elsewhere.
try:
    from scripts._host_paths import host_bin_exe, host_target_framework
    from scripts.extension_manifest import extension_names, package_entry_paths
except ImportError:  # pragma: no cover - exercised only by direct script runs
    from _host_paths import host_bin_exe, host_target_framework
    from extension_manifest import extension_names, package_entry_paths

ROOT = Path(__file__).resolve().parents[1]
HOST_PROJECT = ROOT / "desktop" / "src" / "VibeTable.Desktop"
HOST_CONFIG = "Release"
HOST_TFM = host_target_framework(ROOT)
HOST_EXE = host_bin_exe(ROOT, config=HOST_CONFIG)
PREFERRED_DOTNET = Path(r"C:\Program Files\dotnet\dotnet.exe")
DOTNET = (
    str(PREFERRED_DOTNET) if PREFERRED_DOTNET.is_file() else (shutil.which("dotnet") or "dotnet")
)
PYTHON = sys.executable
_PROCS: list[subprocess.Popen[str]] = []

WEB_PROJECT = ROOT / "desktop" / "web-grid"
WEB_OUTPUT = WEB_PROJECT / "dist" / "index.html"
DIRECTUS_EXTENSION_DIRS = [
    ROOT / "directus" / "extensions" / name for name in extension_names(ROOT)
]


def _resolve_npm() -> str:
    bundled_cli = ROOT / "runtime" / "node" / "node_modules" / "npm" / "bin" / "npm-cli.js"
    bundled_cmd = ROOT / "runtime" / "node" / ("npm.cmd" if os.name == "nt" else "npm")
    if bundled_cli.is_file() and bundled_cmd.is_file():
        return str(bundled_cmd)
    return shutil.which("npm.cmd") or shutil.which("npm") or "npm"


NPM = _resolve_npm()


def _info(msg: str) -> None:
    print(f"[dev] {msg}", flush=True)


def _is_stale(output: Path, inputs: list[Path]) -> bool:
    if not output.is_file():
        return True
    built_at = output.stat().st_mtime
    return any(path.is_file() and path.stat().st_mtime > built_at for path in inputs)


def _project_inputs(project: Path) -> list[Path]:
    inputs: list[Path] = []
    for relative in ("package.json", "package-lock.json", "tsconfig.json", "vite.config.ts"):
        candidate = project / relative
        if candidate.is_file():
            inputs.append(candidate)
    source = project / "src"
    if source.is_dir():
        inputs.extend(path for path in source.rglob("*") if path.is_file())
    return inputs


def _run_build(label: str, command: list[str], cwd: Path, *, timeout: int = 600) -> None:
    _info(f"building {label} ...")
    proc = subprocess.run(
        command,
        cwd=cwd,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
        timeout=timeout,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"{label} build failed (exit {proc.returncode}):\n"
            f"stdout:\n{proc.stdout}\nstderr:\n{proc.stderr}"
        )


def _ensure_node_dependencies(project: Path) -> None:
    lockfile = project / "package-lock.json"
    install_marker = project / "node_modules" / ".package-lock.json"
    if not (project / "node_modules").is_dir() or _is_stale(install_marker, [lockfile]):
        _run_build(f"{project.name} dependencies", [NPM, "ci"], project)


def _ensure_web_built() -> None:
    if not _is_stale(WEB_OUTPUT, _project_inputs(WEB_PROJECT)):
        _info(f"web-grid up to date: {WEB_OUTPUT}")
        return
    _ensure_node_dependencies(WEB_PROJECT)
    _run_build("web-grid", [NPM, "run", "build"], WEB_PROJECT)
    if not WEB_OUTPUT.is_file():
        raise RuntimeError(f"web-grid entry not found after build: {WEB_OUTPUT}")


def _ensure_directus_extensions_built() -> None:
    for project in DIRECTUS_EXTENSION_DIRS:
        outputs = [project / entry for entry in package_entry_paths(project)]
        if all(not _is_stale(output, _project_inputs(project)) for output in outputs):
            _info(f"Directus extension up to date: {project.name}")
            continue
        _ensure_node_dependencies(project)
        _run_build(f"Directus extension {project.name}", [NPM, "run", "build"], project)
        for output in outputs:
            if not output.is_file():
                raise RuntimeError(f"Directus extension entry not found after build: {output}")


def _ensure_host_built() -> Path:
    _ensure_web_built()
    _ensure_directus_extensions_built()
    if not Path(DOTNET).is_file():
        raise RuntimeError(
            "dotnet SDK not found; build the host manually: "
            "dotnet build desktop/VibeTable.Desktop.sln --configuration Release"
        )
    _run_build(
        f"WPF host ({HOST_CONFIG}/{HOST_TFM})",
        [DOTNET, "build", str(HOST_PROJECT), "--configuration", HOST_CONFIG],
        ROOT,
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
        # The host uses the same interpreter that successfully ran this
        # launcher instead of trusting a possibly stale .venv shim.
        "VIBETABLE_PYTHON": PYTHON,
    }
    for name in (
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "NO_PROXY",
        "http_proxy",
        "https_proxy",
        "no_proxy",
        "NODE_EXTRA_CA_CERTS",
        "SSL_CERT_FILE",
    ):
        if value := os.environ.get(name):
            env[name] = value
    if directus_url:
        # The ONLY Directus routing the host sees:
        env["VIBETABLE_DIRECTUS_URL"] = directus_url
        env["VIBETABLE_DIRECTUS_PROJECT"] = "default"
        _info(f"  VIBETABLE_DIRECTUS_URL = {directus_url}")
    proc = subprocess.Popen(
        argv,
        cwd=HOST_EXE.parent,
        env=env,
        text=True,
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
        "Directus (combine with --directus-url for an external service, "
        "or use it to debug the UI alone)",
    )
    args = parser.parse_args()

    signal.signal(signal.SIGINT, _cleanup)
    signal.signal(signal.SIGTERM, _cleanup)

    directus_url = args.directus_url.rstrip("/") if args.directus_url else None
    _launch_host(directus_url=directus_url, no_directus_auto=args.no_directus_auto)
    _info("stack is up. Ctrl+C to stop everything (the host owns its children).")
    # Wait for the client to exit; cleanup runs via signal on Ctrl+C.
    return_code = 0
    for proc in _PROCS:
        return_code = proc.wait()
    return return_code


if __name__ == "__main__":
    raise SystemExit(main())
