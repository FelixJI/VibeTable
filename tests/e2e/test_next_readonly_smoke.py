"""End-to-end startup smoke for the migrated VibeTable desktop shell.

The test launches the built WPF executable and waits for a readiness report
that can only be written after three production boundaries succeed: the real
Python backend handshake, real WebView2 navigation to the bundled web build,
and the renderer-to-host ``app.ready`` bridge message. Data integration is
covered by the backend and contract suites.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

import pytest

from scripts._host_paths import host_bin_exe

ROOT = Path(__file__).resolve().parents[2]
HOST_PROJECT = ROOT / "desktop" / "src" / "VibeTable.Desktop"
DESKTOP_SOURCE = ROOT / "desktop" / "src"
PACKAGED_HOST = os.environ.get("VIBETABLE_E2E_HOST")
HOST_EXE = (
    Path(PACKAGED_HOST).resolve()
    if PACKAGED_HOST
    else host_bin_exe(ROOT, config="Release")
)
PREFERRED_DOTNET = Path(r"C:\Program Files\dotnet\dotnet.exe")
DOTNET = (
    str(PREFERRED_DOTNET) if PREFERRED_DOTNET.is_file() else (shutil.which("dotnet") or "dotnet")
)
READINESS_TIMEOUT_SECONDS = 60.0


def _host_is_stale(executable: Path) -> bool:
    if PACKAGED_HOST:
        return not executable.is_file()
    if not executable.is_file():
        return True
    built_at = executable.stat().st_mtime
    inputs = [
        path
        for pattern in ("*.cs", "*.xaml", "*.csproj")
        for path in DESKTOP_SOURCE.rglob(pattern)
        if "bin" not in path.parts and "obj" not in path.parts
    ]
    inputs.append(ROOT / "desktop" / "Directory.Build.props")
    return any(path.stat().st_mtime > built_at for path in inputs)


def _ensure_host_built() -> Path:
    if not _host_is_stale(HOST_EXE):
        return HOST_EXE
    if not Path(DOTNET).is_file():
        pytest.fail("dotnet SDK not found; cannot build the WPF smoke host")
    proc = subprocess.run(
        [DOTNET, "build", str(HOST_PROJECT), "--configuration", "Release"],
        cwd=ROOT,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=300,
        check=False,
    )
    if proc.returncode != 0:
        pytest.fail(f"WPF host build failed:\nstdout:\n{proc.stdout}\nstderr:\n{proc.stderr}")
    if not HOST_EXE.is_file():
        pytest.fail(f"host executable not found after build: {HOST_EXE}")
    return HOST_EXE


def _read_report(readiness_dir: Path) -> dict[str, object] | None:
    path = readiness_dir / "vibetable-readiness.json"
    if not path.is_file():
        return None
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        return None
    return value if isinstance(value, dict) else None


def _trace(readiness_dir: Path) -> str:
    sections: list[str] = []
    for name in ("vibetable-trace.log", "host-stdout.log", "host-stderr.log"):
        path = readiness_dir / name
        try:
            sections.append(
                f"--- {name} ---\n"
                + path.read_text(encoding="utf-8", errors="replace")
            )
        except OSError:
            sections.append(f"--- {name} unavailable ---")
    return "\n".join(sections)


@pytest.mark.e2e
def test_next_shell_reaches_backend_webview_and_renderer_ready(tmp_path: Path) -> None:
    if sys.platform != "win32":
        pytest.fail("VibeTable WPF smoke requires a Windows desktop with WebView2")

    host = _ensure_host_built()
    readiness_dir = tmp_path / "readiness"
    readiness_dir.mkdir()
    environment = os.environ.copy()
    # Desktop CI and remote Windows agents may not expose a stable GPU
    # process. Force WebView2 software rendering for this real renderer smoke.
    environment.setdefault(
        "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
        "--disable-gpu",
    )
    environment["VIBETABLE_E2E_WEBVIEW2_USER_DATA_ROOT"] = str(
        tmp_path / "webview2-user-data"
    )

    with (
        (readiness_dir / "host-stdout.log").open("wb") as stdout_log,
        (readiness_dir / "host-stderr.log").open("wb") as stderr_log,
    ):
        proc = subprocess.Popen(
            [
                str(host),
                "--test-mode",
                "--readiness-dir",
                str(readiness_dir),
            ],
            cwd=host.parent,
            env=environment,
            stdout=stdout_log,
            stderr=stderr_log,
        )
        report: dict[str, object] | None = None
        try:
            deadline = time.monotonic() + READINESS_TIMEOUT_SECONDS
            while time.monotonic() < deadline:
                report = _read_report(readiness_dir)
                if report is not None:
                    break
                if proc.poll() is not None:
                    pytest.fail(
                        f"WPF host exited with code {proc.returncode} before readiness.\n"
                        f"Trace:\n{_trace(readiness_dir)}"
                    )
                time.sleep(0.25)
            if report is None:
                pytest.fail(
                    f"readiness not reported within {READINESS_TIMEOUT_SECONDS:g}s.\n"
                    f"Trace:\n{_trace(readiness_dir)}"
                )
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=10)

    assert report.get("ready") is True, report
    assert report.get("mode") == "shell", report
    assert report.get("backendReady") is True, report
    assert report.get("webViewReady") is True, report
    assert report.get("rendererReady") is True, report
    assert report.get("error") is None, report
