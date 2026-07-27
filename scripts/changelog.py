#!/usr/bin/env python3
"""Generate release changelog artifacts from non-merge Git commits."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import asdict, dataclass
from pathlib import Path

try:
    from scripts.versioning import read_project_version
except ModuleNotFoundError:  # pragma: no cover - direct execution
    from versioning import read_project_version

REPO_ROOT = Path(__file__).resolve().parents[1]
FIRST_RELEASE_SUBJECT = "初始化项目"
JSON_OUTPUT = Path("desktop/web-grid/src/generated/changelog.json")
MARKDOWN_OUTPUT = Path("CHANGELOG.md")
SEMVER_TAG = re.compile(r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
HOUSEKEEPING_SUBJECT = re.compile(
    r"^(?:chore(?:\(release\))?:\s*release\s+v|merge\b|合并)",
    flags=re.IGNORECASE,
)


@dataclass(frozen=True)
class ChangelogEntry:
    subject: str
    commit: str | None


def _git(repo_root: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=repo_root,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    if result.returncode != 0:
        raise ValueError(result.stderr.strip() or f"git {' '.join(args)} failed")
    return result.stdout


def _previous_release_tag(repo_root: Path, version: str) -> str | None:
    target = tuple(int(part) for part in version.split("."))
    tags = _git(
        repo_root,
        "tag",
        "--merged",
        "HEAD",
        "--list",
        "v[0-9]*.[0-9]*.[0-9]*",
    ).splitlines()
    candidates: list[tuple[tuple[int, int, int], str]] = []
    for tag in tags:
        match = SEMVER_TAG.fullmatch(tag.strip())
        if match is None:
            continue
        parsed = tuple(int(part) for part in match.groups())
        if parsed < target:
            candidates.append((parsed, tag.strip()))
    return max(candidates, default=((), ""))[1] or None


def collect_changelog(repo_root: Path, version: str) -> list[ChangelogEntry]:
    previous = _previous_release_tag(repo_root, version)
    if previous is None:
        return [ChangelogEntry(subject=FIRST_RELEASE_SUBJECT, commit=None)]

    revision_range = f"{previous}..HEAD" if previous else "HEAD"
    output = _git(
        repo_root,
        "log",
        "--no-merges",
        "--format=%h%x1f%s",
        revision_range,
    )
    entries: list[ChangelogEntry] = []
    for line in output.splitlines():
        commit, separator, subject = line.partition("\x1f")
        normalized = subject.strip()
        if not separator or not normalized or HOUSEKEEPING_SUBJECT.match(normalized):
            continue
        entries.append(ChangelogEntry(subject=normalized, commit=commit.strip()))
    return entries


def render_json(entries: list[ChangelogEntry]) -> str:
    return (
        json.dumps(
            {"entries": [asdict(entry) for entry in entries]},
            ensure_ascii=False,
            indent=2,
        )
        + "\n"
    )


def render_markdown(version: str, entries: list[ChangelogEntry]) -> str:
    lines = [f"# VibeTable {version}", ""]
    if entries:
        lines.extend(
            f"- {entry.subject}" + (f" (`{entry.commit}`)" if entry.commit is not None else "")
            for entry in entries
        )
    else:
        lines.append("- 暂无用户可见变更")
    return "\n".join(lines) + "\n"


def generated_contents(repo_root: Path, version: str) -> dict[Path, str]:
    entries = collect_changelog(repo_root, version)
    return {
        repo_root / JSON_OUTPUT: render_json(entries),
        repo_root / MARKDOWN_OUTPUT: render_markdown(version, entries),
    }


def write_changelog(repo_root: Path, version: str) -> list[Path]:
    changed: list[Path] = []
    for path, content in generated_contents(repo_root, version).items():
        if path.is_file() and path.read_text(encoding="utf-8") == content:
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8", newline="")
        changed.append(path)
    return changed


def check_changelog(repo_root: Path, version: str) -> list[str]:
    errors: list[str] = []
    for path, expected in generated_contents(repo_root, version).items():
        if not path.is_file():
            errors.append(f"missing generated changelog: {path.relative_to(repo_root)}")
        elif path.read_text(encoding="utf-8") != expected:
            errors.append(f"stale generated changelog: {path.relative_to(repo_root)}")
    return errors


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--write", action="store_true")
    action.add_argument("--check", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        version = read_project_version(REPO_ROOT)
        if args.write:
            for path in write_changelog(REPO_ROOT, version):
                print(path.relative_to(REPO_ROOT))
            return 0
        errors = check_changelog(REPO_ROOT, version)
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        errors = [str(exc)]
    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
