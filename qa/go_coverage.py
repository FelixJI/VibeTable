"""Independent, declarative Go line, decision-arm, and diff coverage gates."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[1]
SIDECAR = REPO_ROOT / "sidecar"
PROJECT_CONFIG = REPO_ROOT / ".ci" / "project.json"
GROUP_NAME = re.compile(r"^[a-z][a-z0-9-]*$")
GO_PACKAGE = re.compile(r"^\./(?:cmd|internal|tests)/[A-Za-z0-9_][A-Za-z0-9_/-]*$")


@dataclass(frozen=True)
class CoverageGroup:
    name: str
    cover_packages: tuple[str, ...]
    test_packages: tuple[str, ...]
    line_minimum: int
    branch_minimum: int
    diff_minimum: int

    @property
    def scopes(self) -> tuple[str, ...]:
        return tuple(f"sidecar/{package.removeprefix('./')}" for package in self.cover_packages)


def _mapping(value: Any, label: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ValueError(f"{label} must be an object")
    return value


def _packages(
    value: Any,
    label: str,
    *,
    cover_packages: bool = False,
    allow_all: bool = False,
) -> tuple[str, ...]:
    if not isinstance(value, list) or not value:
        raise ValueError(f"{label} must be a non-empty list")
    if not all(isinstance(item, str) for item in value):
        raise ValueError(f"{label} must contain only strings")
    packages = tuple(value)
    if len(packages) != len(set(packages)):
        raise ValueError(f"{label} must not contain duplicates")
    for package in packages:
        if allow_all and package == "./...":
            continue
        if (
            not GO_PACKAGE.fullmatch(package)
            or package.endswith("/")
            or "//" in package
            or "/../" in package
        ):
            raise ValueError(f"invalid Go package {package!r} in {label}")
        if cover_packages and not package.startswith("./internal/"):
            raise ValueError(f"coverage target must be an internal Go package: {package!r}")
    return packages


def _thresholds(value: Any, group_name: str) -> tuple[int, int, int]:
    minimum = _mapping(value, f"minimum for {group_name}")
    if set(minimum) != {"line", "branch", "diff"}:
        raise ValueError(f"minimum must declare line, branch, and diff for {group_name}")
    values = tuple(minimum[key] for key in ("line", "branch", "diff"))
    if any(isinstance(item, bool) or not isinstance(item, int) for item in values):
        raise ValueError(f"coverage minimums must be integers for {group_name}")
    if any(item <= 0 or item > 100 for item in values):
        raise ValueError(f"coverage minimums must be between 1 and 100 for {group_name}")
    return values


def load_groups(config_path: Path = PROJECT_CONFIG) -> tuple[CoverageGroup, ...]:
    payload = _mapping(json.loads(config_path.read_text(encoding="utf-8")), "project config")
    quality = _mapping(payload.get("quality"), "quality")
    go_coverage = _mapping(quality.get("go_coverage"), "quality.go_coverage")
    if set(go_coverage) != {"groups"}:
        raise ValueError("quality.go_coverage has unknown fields")
    inventory = _mapping(go_coverage.get("groups"), "quality.go_coverage.groups")
    if not inventory:
        raise ValueError("quality.go_coverage.groups must not be empty")

    groups: list[CoverageGroup] = []
    claimed_packages: dict[str, str] = {}
    for name, raw_entry in inventory.items():
        if not isinstance(name, str) or not GROUP_NAME.fullmatch(name):
            raise ValueError(f"invalid Go coverage group name {name!r}")
        entry = _mapping(raw_entry, f"Go coverage group {name}")
        expected_fields = {"cover_packages", "test_packages", "minimum"}
        if set(entry) != expected_fields:
            raise ValueError(f"Go coverage group {name} has unknown fields or missing fields")
        cover_packages = _packages(
            entry["cover_packages"],
            f"{name}.cover_packages",
            cover_packages=True,
        )
        test_packages = _packages(
            entry["test_packages"],
            f"{name}.test_packages",
            allow_all=True,
        )
        line, branch, diff = _thresholds(entry["minimum"], name)
        for package in cover_packages:
            if owner := claimed_packages.get(package):
                raise ValueError(
                    f"Go coverage groups must not overlap: {package} belongs to {owner} and {name}"
                )
            claimed_packages[package] = name
        groups.append(
            CoverageGroup(
                name=name,
                cover_packages=cover_packages,
                test_packages=test_packages,
                line_minimum=line,
                branch_minimum=branch,
                diff_minimum=diff,
            )
        )
    return tuple(groups)


def _base_ref() -> str:
    branch = os.environ.get("GITHUB_BASE_REF", "").strip()
    if branch:
        return f"origin/{branch}"
    return os.environ.get("VIBETABLE_GO_COVERAGE_BASE_REF", "GitHub/main")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable")
    parser.add_argument("--config", type=Path, default=PROJECT_CONFIG, help="project config")
    args = parser.parse_args(argv)
    for group in load_groups(args.config):
        output = REPO_ROOT / "build" / "qa" / "go-coverage" / group.name
        output.mkdir(parents=True, exist_ok=True)
        profile = output / "coverage.out"
        report = output / "report.json"
        profile.unlink(missing_ok=True)
        report.unlink(missing_ok=True)
        subprocess.run(
            [
                args.go,
                "test",
                "-count=1",
                "-covermode=count",
                f"-coverpkg={','.join(group.cover_packages)}",
                f"-coverprofile={profile}",
                *group.test_packages,
            ],
            cwd=SIDECAR,
            check=True,
        )
        command = [
            args.go,
            "run",
            "./cmd/go-coverage-report",
            "--group",
            group.name,
            "--profile",
            str(profile),
            "--repository-root",
            str(REPO_ROOT),
            "--base-ref",
            _base_ref(),
            "--report",
            str(report),
            "--line-min",
            str(group.line_minimum),
            "--branch-min",
            str(group.branch_minimum),
            "--diff-min",
            str(group.diff_minimum),
        ]
        for scope in group.scopes:
            command.extend(("--scope", scope))
        subprocess.run(command, cwd=SIDECAR, check=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
