"""Independent Go core line, decision-arm, and diff coverage gate."""

from __future__ import annotations

import argparse
import os
import subprocess
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
SIDECAR = REPO_ROOT / "sidecar"
SCOPES = (
    "sidecar/internal/schemacore",
    "sidecar/internal/relatedcomputation",
    "sidecar/internal/workspacesearch",
)
PACKAGES = (
    "./internal/schemacore",
    "./internal/relatedcomputation",
    "./internal/workspacesearch",
)


def _base_ref() -> str:
    branch = os.environ.get("GITHUB_BASE_REF", "").strip()
    if branch:
        return f"origin/{branch}"
    return os.environ.get("VIBETABLE_GO_COVERAGE_BASE_REF", "GitHub/main")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable")
    args = parser.parse_args(argv)
    output = REPO_ROOT / "build" / "qa" / "go-core-coverage"
    output.mkdir(parents=True, exist_ok=True)
    profile = output / "coverage.out"
    report = output / "report.json"
    coverpkg = ",".join(PACKAGES)
    subprocess.run(
        [
            args.go,
            "test",
            "-count=1",
            "-covermode=count",
            f"-coverpkg={coverpkg}",
            f"-coverprofile={profile}",
            *PACKAGES,
        ],
        cwd=SIDECAR,
        check=True,
    )
    command = [
        args.go,
        "run",
        "./cmd/go-coverage-report",
        "--profile",
        str(profile),
        "--repository-root",
        str(REPO_ROOT),
        "--base-ref",
        _base_ref(),
        "--report",
        str(report),
        "--line-min",
        "85",
        "--branch-min",
        "75",
        "--diff-min",
        "90",
    ]
    for scope in SCOPES:
        command.extend(("--scope", scope))
    subprocess.run(command, cwd=SIDECAR, check=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
