#!/usr/bin/env python3
"""Generate release changelog artifacts from non-merge Git commits."""

from __future__ import annotations

import argparse
import json
import os
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
CONVENTIONAL_SUBJECT = re.compile(
    r"^(?P<type>[a-z][a-z0-9-]*)(?:\((?P<scope>[^)]+)\))?(?P<breaking>!)?:\s+\S",
    flags=re.IGNORECASE,
)
CHANGELOG_DIRECTIVE = re.compile(
    r"^Changelog:\s*(include|skip)\s*$", flags=re.IGNORECASE | re.MULTILINE
)
BREAKING_CHANGE = re.compile(r"^BREAKING(?: |-)?CHANGE:\s+\S", flags=re.IGNORECASE | re.MULTILINE)
USER_VISIBLE_TYPES = frozenset({"feat", "fix", "perf", "revert"})
DEPENDENCY_TYPES = frozenset({"deps", "dependencies"})


@dataclass(frozen=True)
class ChangelogEntry:
    subject: str
    commit: str | None
    category: str = "changes"


def _category(subject: str, message: str) -> str | None:
    directives = CHANGELOG_DIRECTIVE.findall(message)
    if directives:
        return "changes" if directives[-1].lower() == "include" else None

    match = CONVENTIONAL_SUBJECT.match(subject)
    if match is None:
        return None
    change_type = match.group("type").lower()
    scope = (match.group("scope") or "").lower()
    if match.group("breaking") == "!" or BREAKING_CHANGE.search(message) is not None:
        return "breaking"
    if change_type in DEPENDENCY_TYPES or scope in DEPENDENCY_TYPES:
        return "dependencies"
    if change_type in USER_VISIBLE_TYPES:
        return "changes"
    return None


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
    configured = os.environ.get("AUTOMATION_CHANGELOG_BASE_TAG", "").strip()
    if configured:
        if SEMVER_TAG.fullmatch(configured) is None:
            raise ValueError(f"invalid AUTOMATION_CHANGELOG_BASE_TAG: {configured}")
        return configured
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
    revision_range = f"{previous}..HEAD" if previous else "HEAD"
    output = _git(
        repo_root,
        "log",
        "--no-merges",
        "--format=%H%x1f%s%x1f%B%x1e",
        revision_range,
    )
    entries: list[ChangelogEntry] = []
    for record in output.split("\x1e"):
        commit, separator, remainder = record.strip("\r\n").partition("\x1f")
        subject, message_separator, message = remainder.partition("\x1f")
        normalized = subject.strip()
        if not separator or not message_separator or not normalized:
            continue
        category = _category(normalized, message)
        if category is None:
            continue
        entries.append(
            ChangelogEntry(subject=normalized, commit=commit.strip()[:8], category=category)
        )
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
    sections = (
        ("breaking", "破坏性变更"),
        ("changes", "变更"),
        ("dependencies", "依赖更新"),
    )
    for category, title in sections:
        selected = [entry for entry in entries if entry.category == category]
        if not selected:
            continue
        lines.extend((f"## {title}", ""))
        lines.extend(
            f"- {entry.subject}" + (f" (`{entry.commit}`)" if entry.commit is not None else "")
            for entry in selected
        )
        lines.append("")
    if not entries:
        lines.append("- 内部改进与维护")
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


def write_changelog_json(repo_root: Path, version: str) -> list[Path]:
    """只更新应用内消费的 JSON；累计 CHANGELOG.md 由公共发布核心维护。"""
    path = repo_root / JSON_OUTPUT
    content = render_json(collect_changelog(repo_root, version))
    if path.is_file() and path.read_text(encoding="utf-8") == content:
        return []
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8", newline="")
    return [path]


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
    action.add_argument("--write-json", action="store_true")
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
        if args.write_json:
            for path in write_changelog_json(REPO_ROOT, version):
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
