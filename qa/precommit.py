#!/usr/bin/env python3
"""Pre-commit helper entrypoint for running project checks.

This wrapper is intentionally small so the repository can run reliably even when
pre-commit is installed in different environments:

- It forces a writable Ruff cache location inside the repo.
- It accepts arbitrary file arguments passed by pre-commit.
- It keeps behavior identical to the old hooks while avoiding environment drift.
"""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parent.parent
RUFF_CACHE_DIR = PROJECT_ROOT / ".ruff_cache" / "pre-commit"


def run_command(cmd: list[str], *, filenames: list[str]) -> int:
    if cmd[:3] == [sys.executable, "-m", "ruff"] and any(
        arg in {"format", "check"} for arg in cmd[3:4]
    ):
        cmd.extend(filenames)
    env = os.environ.copy()
    env["PYTHONUTF8"] = "1"
    env["RUFF_CACHE_DIR"] = str(RUFF_CACHE_DIR)
    RUFF_CACHE_DIR.mkdir(parents=True, exist_ok=True)
    result = subprocess.run(
        cmd,
        cwd=PROJECT_ROOT,
        env=env,
        check=False,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    return result.returncode


def main() -> int:
    if len(sys.argv) < 2:
        print(
            "Usage: python qa/precommit.py <ruff-format|ruff-check|version|package>",
            file=sys.stderr,
        )
        return 1

    task = sys.argv[1]
    filenames = sys.argv[2:]

    if task == "ruff-format":
        return run_command([sys.executable, "-m", "ruff", "format", "--check"], filenames=filenames)
    if task == "ruff-check":
        return run_command([sys.executable, "-m", "ruff", "check"], filenames=filenames)
    if task == "version-consistency":
        return run_command(
            [sys.executable, str(PROJECT_ROOT / "qa" / "version_check.py")], filenames=[]
        )
    if task == "package-contract":
        return run_command(
            [sys.executable, str(PROJECT_ROOT / "qa" / "package_check.py")], filenames=[]
        )

    print(f"Unknown task: {task}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
