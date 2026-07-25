#!/usr/bin/env python3
"""Build the product stack and launch the desktop host in development mode."""

from __future__ import annotations

import argparse
import os
import shutil
import signal
import subprocess
import sys
from pathlib import Path

if __package__:
    from ._host_paths import host_bin_exe
else:
    from _host_paths import host_bin_exe

ROOT = Path(__file__).resolve().parents[1]
WEB_DIR = ROOT / "desktop" / "web-grid"
SIDECAR_DIR = ROOT / "sidecar"
HOST_PROJECT = ROOT / "desktop" / "src" / "VibeTable.Desktop"
BUILD_DIR = ROOT / "build" / "dev"
HOST_BINARY = host_bin_exe(ROOT, config="Release", host_project=HOST_PROJECT)
SIDECAR_BINARY = BUILD_DIR / (
    "vibetable-pb.exe" if os.name == "nt" else "vibetable-pb"
)
PROCESSES: list[subprocess.Popen[str]] = []
UNSAFE_INHERITED_RUNTIME_VARIABLES = {
    "VIBETABLE_E2E_WEBVIEW2_USER_DATA_ROOT",
    "VIBETABLE_E2E_MUTATION_BARRIER_DIR",
    "VIBETABLE_SIDECAR_DATA_DIR",
    "VIBETABLE_SIDECAR_SESSION_SECRET",
    "VIBETABLE_SIDECAR_URL",
    "VIBETABLE_STATE_DIR",
    "VIBETABLE_WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
    "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
}


def _resolve(name: str) -> str:
    if name == "go":
        suffix = "go.exe" if os.name == "nt" else "go"
        bundled = ROOT / ".tools" / "go-full" / "go" / "bin" / suffix
        if bundled.is_file():
            return str(bundled)
    if name == "dotnet":
        preferred = Path(r"C:\Program Files\dotnet\dotnet.exe")
        if preferred.is_file():
            return str(preferred)
    return shutil.which(name) or name


def _run(command: list[str], *, cwd: Path) -> None:
    subprocess.run(command, cwd=cwd, check=True)


def build(*, web: bool = True, sidecar: bool = True, host: bool = True) -> None:
    BUILD_DIR.mkdir(parents=True, exist_ok=True)
    if sidecar:
        _run(
            [
                _resolve("go"),
                "build",
                "-trimpath",
                "-o",
                str(SIDECAR_BINARY),
                "./cmd/vibetable-pb",
            ],
            cwd=SIDECAR_DIR,
        )
    if web:
        # Dependencies must already be restored. Development never installs at
        # runtime and release artifacts never contain Node/npm.
        _run([_resolve("npm"), "run", "build"], cwd=WEB_DIR)
    if host:
        _run(
            [
                _resolve("dotnet"),
                "build",
                str(HOST_PROJECT),
                "--configuration",
                "Release",
            ],
            cwd=ROOT,
        )


def _start(command: list[str], *, cwd: Path, env: dict[str, str] | None = None):
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=env,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    PROCESSES.append(process)
    return process


def _cleanup(*_args: object) -> None:
    for process in reversed(PROCESSES):
        if process.poll() is None:
            process.terminate()
    for process in reversed(PROCESSES):
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()


def launch(data_dir: Path) -> int:
    if not HOST_BINARY.is_file():
        raise FileNotFoundError(
            f"desktop host is missing: {HOST_BINARY}; "
            "run without --no-host-build first"
        )

    environment = os.environ.copy()
    for variable in UNSAFE_INHERITED_RUNTIME_VARIABLES:
        environment.pop(variable, None)
    # The source-layout host owns both runtime children. Passing the exact
    # interpreter keeps it on this repository's environment.
    environment["VIBETABLE_PYTHON"] = sys.executable
    development_data_root = data_dir.resolve()
    host = _start(
        [
            str(HOST_BINARY),
            "--dev-data-root",
            str(development_data_root),
        ],
        cwd=ROOT,
        env=environment,
    )
    print(
        f"VibeTable desktop started: {HOST_BINARY} (Ctrl+C to stop)",
        flush=True,
    )
    return host.wait()


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build-only", action="store_true")
    parser.add_argument("--no-web-build", action="store_true")
    parser.add_argument("--no-sidecar-build", action="store_true")
    parser.add_argument("--no-host-build", action="store_true")
    parser.add_argument(
        "--data-dir",
        type=Path,
        default=ROOT / ".dev-data" / "pocketbase",
    )
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    signal.signal(signal.SIGINT, _cleanup)
    if hasattr(signal, "SIGTERM"):
        signal.signal(signal.SIGTERM, _cleanup)
    try:
        build(
            web=not args.no_web_build,
            sidecar=not args.no_sidecar_build,
            host=not args.no_host_build,
        )
        return 0 if args.build_only else launch(args.data_dir)
    except (OSError, subprocess.CalledProcessError, ValueError) as exc:
        print(f"development stack failed: {exc}", file=sys.stderr)
        return 1
    finally:
        _cleanup()


if __name__ == "__main__":
    raise SystemExit(main())
