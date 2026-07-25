#!/usr/bin/env python3
"""Fail when repository-owned Go sources are not gofmt-clean."""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SIDECAR = ROOT / "sidecar"


def _gofmt() -> str:
    suffix = "gofmt.exe" if os.name == "nt" else "gofmt"
    bundled = ROOT / ".tools" / "go-full" / "go" / "bin" / suffix
    return str(bundled) if bundled.is_file() else (shutil.which("gofmt") or "gofmt")


def source_files() -> list[Path]:
    """Return only repository sources, excluding local caches and vendored trees."""
    files: list[Path] = []
    for path in SIDECAR.rglob("*.go"):
        relative = path.relative_to(SIDECAR)
        if any(part.startswith(".") for part in relative.parts):
            continue
        if any(
            part in {"vendor", "node_modules", "build", "dist"}
            for part in relative.parts
        ):
            continue
        files.append(path)
    return sorted(files)


def main() -> int:
    files = source_files()
    if not files:
        print("no repository Go sources found", file=sys.stderr)
        return 2

    dirty: list[str] = []
    for offset in range(0, len(files), 200):
        command = [
            _gofmt(),
            "-l",
            *(str(path) for path in files[offset : offset + 200]),
        ]
        result = subprocess.run(
            command,
            cwd=SIDECAR,
            check=False,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
        if result.returncode:
            sys.stdout.write(result.stdout)
            sys.stderr.write(result.stderr)
            return result.returncode
        dirty.extend(line for line in result.stdout.splitlines() if line.strip())

    if dirty:
        print("\n".join(dirty))
        print("Go source files require gofmt.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
