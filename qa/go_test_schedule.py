"""Schedule the full sidecar test inventory without app package competition."""

from __future__ import annotations

import argparse
import subprocess
import sys
import time
from datetime import UTC, datetime
from pathlib import Path

SIDECAR_PACKAGE = "github.com/vibetable/vibetable/sidecar"
APP_PACKAGE = f"{SIDECAR_PACKAGE}/internal/app"


def package_batches(go: str, cwd: Path) -> tuple[tuple[str, tuple[str, ...]], ...]:
    result = subprocess.run(
        [go, "list", "./..."],
        cwd=cwd,
        check=True,
        stdout=subprocess.PIPE,
        encoding="utf-8",
    )
    packages = tuple(result.stdout.splitlines())
    if (
        len(packages) < 2
        or APP_PACKAGE not in packages
        or len(set(packages)) != len(packages)
        or any(
            not (package == SIDECAR_PACKAGE or package.startswith(f"{SIDECAR_PACKAGE}/"))
            or "..." in package
            or any(character.isspace() for character in package)
            for package in packages
        )
    ):
        raise ValueError("go list returned an invalid full sidecar package inventory")
    remaining = tuple(package for package in packages if package != APP_PACKAGE)
    return (("app", (APP_PACKAGE,)), ("remaining", remaining))


def run_test_batch(
    go: str, cwd: Path, name: str, packages: tuple[str, ...], options: tuple[str, ...] = ()
) -> None:
    started = time.monotonic()
    print(
        f"Go test {name} started at {datetime.now(UTC).isoformat()} ({len(packages)} packages)",
        flush=True,
    )
    try:
        subprocess.run([go, "test", *options, *packages], cwd=cwd, check=True)
    finally:
        elapsed = time.monotonic() - started
        print(
            f"Go test {name} finished at {datetime.now(UTC).isoformat()} ({elapsed:.3f}s)",
            flush=True,
        )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable")
    args = parser.parse_args(argv)
    cwd = Path.cwd()
    try:
        for name, packages in package_batches(args.go, cwd):
            run_test_batch(args.go, cwd, name, packages)
    except subprocess.CalledProcessError as error:
        return error.returncode
    except (OSError, ValueError) as error:
        print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
