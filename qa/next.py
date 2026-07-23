#!/usr/bin/env python3
"""VibeTable unified cross-stack quality gate.

Runs version/package checks, Python and contract tests, .NET tests, web-grid
tests/build, Directus extension tests/typecheck/build and the end-to-end smoke.

Design contract (verbatim from the Task 12 brief):

* ``--list`` prints the exact ordered stages, including ``dev-build`` after
  the read-only repository checks so a clean checkout exercises the real
  development-launcher build path before the per-stack test stages.
* ``--ci`` runs them in order, STOPS on the first failure, and returns that
  stage's non-zero exit code.
* Each stage uses :func:`subprocess.run` with an ARGUMENT LIST (never
  ``shell=True``), with the repository root as the working directory, and
  decodes child output as UTF-8 with ``errors="replace"`` so a stray byte
  never crashes the gate.
* Each stage's stdout and stderr are preserved verbatim in the report so a
  reviewer can see exactly what the underlying tool printed.
* A skipped smoke test is missing Windows/WebView2 evidence and fails the
  Phase-A gate; only an executed end-to-end pass is accepted.

The dotnet/npm commands are resolved through PATHEXT on Windows (so the
``.cmd`` shims work) and dotnet is preferred from the x64 install at
``C:\\Program Files\\dotnet`` when present. This module is import-safe:
importing it has no side effects. ``main()`` does the work.
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
import time
from dataclasses import asdict, dataclass
from datetime import datetime
from pathlib import Path

# ---------------------------------------------------------------------------
# Repository layout
# ---------------------------------------------------------------------------

REPO_ROOT: Path = Path(__file__).resolve().parent.parent
WEB_GRID_DIR: Path = REPO_ROOT / "desktop" / "web-grid"
DESKTOP_SLN: Path = REPO_ROOT / "desktop" / "VibeTable.Desktop.sln"
E2E_SMOKE_TEST: Path = REPO_ROOT / "tests" / "e2e" / "test_next_readonly_smoke.py"
DEV_LAUNCHER: Path = REPO_ROOT / "scripts" / "dev.py"

# G0.2: Directus extension directories are discovered from the version-controlled
# manifest (directus/extensions/manifest.json) rather than hard-coded. Each
# directus-* stage iterates every declared extension in order.
_DIRECTUS_EXTENSIONS_ROOT: Path = REPO_ROOT / "directus" / "extensions"


def _directus_extension_dirs() -> list[Path]:
    """Return the ordered list of Directus extension source directories.

    Reads ``directus/extensions/manifest.json``. Falls back to the single
    ``vibetable-bulk-mutation`` directory if the manifest is absent (defensive —
    the manifest is committed and should always exist).
    """
    manifest = _DIRECTUS_EXTENSIONS_ROOT / "manifest.json"
    if manifest.is_file():
        import json

        data = json.loads(manifest.read_text(encoding="utf-8"))
        names = [ext["name"] for ext in data.get("extensions", []) if "name" in ext]
        if names:
            return [_DIRECTUS_EXTENSIONS_ROOT / name for name in names]
    return [_DIRECTUS_EXTENSIONS_ROOT / "vibetable-bulk-mutation"]


#: The exact, ordered Phase A stage list. Tests assert this tuple verbatim.
STAGES: tuple[str, ...] = (
    "version",
    "package",
    "dev-build",
    "python",
    "contracts",
    "dotnet",
    "web-test",
    "web-build",
    "directus-test",
    "directus-typecheck",
    "directus-build",
    "smoke",
)


# ---------------------------------------------------------------------------
# Result type
# ---------------------------------------------------------------------------


@dataclass
class StageResult:
    """Captured outcome of running one stage.

    ``returncode`` is the underlying tool's exit status. ``stdout`` and
    ``stderr`` preserve the tool's output verbatim (decoded UTF-8 /
    replace). ``skipped`` is set by :func:`is_stage_skipped` callers to flag
    a stage whose underlying test was deliberately skipped (the smoke test
    on a broken WebView2 runtime); such stages do NOT fail the gate.
    """

    stage: str
    command: list[str]
    returncode: int
    elapsed: float
    stdout: str = ""
    stderr: str = ""
    cwd: str = ""
    skipped: bool = False

    def to_dict(self) -> dict:
        return asdict(self)


# ---------------------------------------------------------------------------
# Executable resolution (Windows .cmd shims + x64 dotnet)
# ---------------------------------------------------------------------------


def _resolve_executable(name: str) -> str:
    """Resolve a bare command name to an executable path.

    On Windows ``npm`` / ``dotnet`` ship as ``.cmd`` shims; ``subprocess.run``
    does NOT consult PATHEXT for bare names, so a naive ``["npm", "ci"]``
    raises ``FileNotFoundError``. :func:`shutil.which` DOES respect PATHEXT,
    so we use it. Absolute paths and ``sys.executable`` are returned as-is.
    """
    if os.path.sep in name or (os.path.altsep and os.path.altsep in name):
        return name
    resolved = shutil.which(name)
    return resolved or name


def _dotnet_path() -> str:
    """Prefer the x64 .NET SDK when present.

    The WPF host targets x64 (``PlatformTarget=x64`` in
    ``desktop/Directory.Build.props``); an x86-only dotnet on PATH cannot
    build it. We prefer ``C:\\Program Files\\dotnet\\dotnet.exe`` when it
    exists, falling back to whatever ``shutil.which`` finds.
    """
    preferred = r"C:\Program Files\dotnet\dotnet.exe"
    if Path(preferred).is_file():
        return preferred
    resolved = shutil.which("dotnet")
    return resolved or "dotnet"


# ---------------------------------------------------------------------------
# Stage command construction
# ---------------------------------------------------------------------------


def stage_command(
    stage: str, *, directus_extension_dir: Path | None = None
) -> tuple[list[str], str]:
    """Return the (argument-list command, cwd) for ``stage``.

    The commands mirror the per-stage brief in Task 12. The argument lists
    are passed verbatim to :func:`subprocess.run`; no shell invocation.

    G0.2: for ``directus-*`` stages, ``directus_extension_dir`` selects which
    extension to target. When omitted, the first declared extension is used.
    """
    if stage == "version":
        return ([sys.executable, "qa/version_check.py"], str(REPO_ROOT))
    if stage == "package":
        return ([sys.executable, "qa/package_check.py"], str(REPO_ROOT))
    if stage == "dev-build":
        return (
            [sys.executable, str(DEV_LAUNCHER), "--build-only"],
            str(REPO_ROOT),
        )
    if stage == "python":
        # Backend + RPC tests: framing, dispatcher, server, table service.
        return (
            [
                sys.executable,
                "-m",
                "pytest",
                "tests/backend",
                "--no-cov",
                "-q",
            ],
            str(REPO_ROOT),
        )
    if stage == "contracts":
        # Language-neutral contract fixtures. Asserted byte-for-byte by both
        # the Python contract tests and the C# fixture tests (dotnet stage).
        return (
            [
                sys.executable,
                "-m",
                "pytest",
                "tests/contract",
                "--no-cov",
                "-q",
            ],
            str(REPO_ROOT),
        )
    if stage == "dotnet":
        return (
            [
                _dotnet_path(),
                "test",
                str(DESKTOP_SLN),
                "--configuration",
                "Release",
                "/p:CollectCoverage=true",
                "/p:CoverletOutputFormat=cobertura",
            ],
            str(REPO_ROOT),
        )
    if stage == "web-test":
        return (
            [_resolve_executable("npm"), "run", "test:coverage"],
            str(WEB_GRID_DIR),
        )
    if stage == "web-build":
        return (
            [_resolve_executable("npm"), "run", "build"],
            str(WEB_GRID_DIR),
        )
    if stage.startswith("directus-"):
        ext_dirs = _directus_extension_dirs()
        ext_dir = directus_extension_dir or ext_dirs[0]
        if stage == "directus-test":
            return (
                [_resolve_executable("npm"), "run", "test:coverage"],
                str(ext_dir),
            )
        if stage == "directus-typecheck":
            return (
                [_resolve_executable("npm"), "run", "typecheck"],
                str(ext_dir),
            )
        if stage == "directus-build":
            return ([_resolve_executable("npm"), "run", "build"], str(ext_dir))
    if stage == "smoke":
        return (
            [
                sys.executable,
                "-m",
                "pytest",
                str(E2E_SMOKE_TEST),
                "--no-cov",
                "-q",
            ],
            str(REPO_ROOT),
        )
    raise ValueError(f"unknown stage: {stage!r}")


# ---------------------------------------------------------------------------
# Stage execution
# ---------------------------------------------------------------------------


def run_stage(stage: str) -> StageResult:
    """Execute ``stage`` and capture its result.

    Uses :func:`subprocess.run` with an argument list (no shell), repo-root
    cwd, and UTF-8 / replace decoding. Elapsed time is wall-clock seconds.

    G0.2: for ``directus-*`` stages, iterates every extension declared in the
    manifest. The stage passes only if ALL extensions pass; output from each
    extension is concatenated so the report shows which one failed.
    """
    if stage.startswith("directus-"):
        return _run_multi_extension_stage(stage)
    command, cwd = stage_command(stage)
    start = time.perf_counter()
    try:
        proc = subprocess.run(
            command,
            cwd=cwd,
            check=False,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
        returncode = proc.returncode
        stdout = proc.stdout or ""
        stderr = proc.stderr or ""
    except FileNotFoundError as exc:
        # Missing tool (e.g. dotnet not installed) is a hard failure with the
        # reason captured in stderr so the report shows what was missing.
        returncode = 127
        stdout = ""
        stderr = f"command not found: {command[0] if command else '?'}: {exc}"
    elapsed = time.perf_counter() - start
    return StageResult(
        stage=stage,
        command=command,
        returncode=returncode,
        elapsed=elapsed,
        stdout=stdout,
        stderr=stderr,
        cwd=cwd,
    )


def _run_multi_extension_stage(stage: str) -> StageResult:
    """Run a ``directus-*`` stage across every declared extension.

    Iterates :func:`_directus_extension_dirs`; stops at the first failure.
    The returned result carries the failing extension's command, but stdout
    and stderr are concatenated across all extensions so a reviewer can see
    the full per-extension trail.
    """
    ext_dirs = _directus_extension_dirs()
    combined_stdout: list[str] = []
    combined_stderr: list[str] = []
    total_elapsed = 0.0
    last_command: list[str] = []
    last_cwd = ""
    for ext_dir in ext_dirs:
        command, cwd = stage_command(stage, directus_extension_dir=ext_dir)
        last_command = command
        last_cwd = cwd
        header = f"--- Directus extension: {ext_dir.name} ({stage}) ---\n"
        start = time.perf_counter()
        try:
            proc = subprocess.run(
                command,
                cwd=cwd,
                check=False,
                capture_output=True,
                text=True,
                encoding="utf-8",
                errors="replace",
            )
            returncode = proc.returncode
            stdout = proc.stdout or ""
            stderr = proc.stderr or ""
        except FileNotFoundError as exc:
            returncode = 127
            stdout = ""
            stderr = f"command not found: {command[0] if command else '?'}: {exc}"
        total_elapsed += time.perf_counter() - start
        combined_stdout.append(header + stdout)
        combined_stderr.append(stderr)
        if returncode != 0:
            return StageResult(
                stage=stage,
                command=last_command,
                returncode=returncode,
                elapsed=total_elapsed,
                stdout="".join(combined_stdout),
                stderr="".join(combined_stderr),
                cwd=last_cwd,
            )
    return StageResult(
        stage=stage,
        command=last_command,
        returncode=0,
        elapsed=total_elapsed,
        stdout="".join(combined_stdout),
        stderr="".join(combined_stderr),
        cwd=last_cwd,
    )


def is_stage_skipped(result: StageResult) -> bool:
    """True when the smoke stage's underlying pytest skipped all tests.

    ``pytest`` exits 0 whether tests pass or skip; we detect a deliberate
    skip by looking for the ``skip`` marker in the tail of pytest's output
    (e.g. ``1 skipped``).
    """
    if result.stage != "smoke":
        return False
    tail = (result.stdout + result.stderr).lower()
    if "skipped" not in tail:
        return False
    # If any test FAILED (even alongside a skip), pytest exits non-zero and
    # the gate fails; only a pure skip (exit 0) is treated as acceptable.
    return result.returncode == 0


def is_stage_failure(result: StageResult) -> bool:
    """True on non-zero exit or a skipped end-to-end smoke stage."""
    return result.returncode != 0 or is_stage_skipped(result)


# ---------------------------------------------------------------------------
# Reporting
# ---------------------------------------------------------------------------


def _write_console_text(stream, value: str) -> None:
    """Write child output without crashing on a narrower Windows code page."""
    try:
        stream.write(value)
    except UnicodeEncodeError:
        encoding = getattr(stream, "encoding", None) or "utf-8"
        safe = value.encode(encoding, errors="replace").decode(encoding)
        stream.write(safe)


def _print_stage_result(result: StageResult) -> None:
    status = (
        "SKIP" if is_stage_skipped(result) else ("PASS" if not is_stage_failure(result) else "FAIL")
    )
    print(f"[{status}] {result.stage} ({result.elapsed:.2f}s) $ {' '.join(result.command)}")
    if result.stdout:
        _write_console_text(
            sys.stdout,
            result.stdout if result.stdout.endswith("\n") else result.stdout + "\n",
        )
    if result.stderr:
        _write_console_text(
            sys.stderr,
            result.stderr if result.stderr.endswith("\n") else result.stderr + "\n",
        )


def _print_summary(results: list[StageResult]) -> int:
    """Print the per-stage summary and return the gate exit code."""
    print()
    print("=" * 72)
    print("  VibeTable Next Phase A gate summary")
    print("=" * 72)
    exit_code = 0
    for r in results:
        failed = is_stage_failure(r)
        if failed:
            status = "SKIP" if is_stage_skipped(r) else "FAIL"
            exit_code = r.returncode or 1
        else:
            status = "PASS"
        print(f"  {r.stage:<20} {status:<5} ({r.elapsed:6.2f}s)")
    # Stages after the first failure were not run.
    not_run = [s for s in STAGES if s not in {r.stage for r in results}]
    if not_run:
        print("  (not run: " + ", ".join(not_run) + ")")
    total = sum(r.elapsed for r in results)
    print(f"  total elapsed: {total:.2f}s")
    print("=" * 72)
    return exit_code


# ---------------------------------------------------------------------------
# CLI entry points
# ---------------------------------------------------------------------------


def cmd_list() -> int:
    """Print the exact ordered stages, one per line."""
    for stage in STAGES:
        print(stage)
    return 0


def cmd_ci() -> int:
    """Run every stage in order, stopping on the first failure."""
    print(f"# VibeTable Next Phase A gate ({datetime.now().isoformat()})")
    print(f"# repo: {REPO_ROOT}")
    print(f"# stages: {', '.join(STAGES)}")
    results: list[StageResult] = []
    for stage in STAGES:
        print()
        print(f"---- stage: {stage} ----")
        result = run_stage(stage)
        results.append(result)
        _print_stage_result(result)
        if is_stage_failure(result):
            # STOP on first failure; remaining stages are not executed.
            break
    return _print_summary(results)


def build_arg_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="next.py",
        description="VibeTable cross-stack aggregate quality gate.",
    )
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument(
        "--list",
        action="store_true",
        help="Print the exact ordered stages (one per line) and exit.",
    )
    mode.add_argument(
        "--ci",
        action="store_true",
        help="Run every stage in order, stopping on the first failure. "
        "Exits with the failing stage's non-zero code.",
    )
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_arg_parser()
    args = parser.parse_args(argv)
    if args.list:
        return cmd_list()
    if args.ci:
        return cmd_ci()
    parser.error("no mode selected")  # unreachable: mutually exclusive required
    return 2


if __name__ == "__main__":
    sys.exit(main())
