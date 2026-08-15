"""Safely prune reproducible repository caches and stale build output."""

from __future__ import annotations

import argparse
import os
import shutil
import stat
import sys
import time
from collections.abc import Callable, Iterable
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
CACHE_ROOTS = (
    ".cache",
    ".codex-dotnet",
    ".pytest_cache",
    ".ruff_cache",
    ".mypy_cache",
    ".tmp",
    ".codex-go-cache",
    ".codex-go-tmp",
    ".codex-test-tmp",
    ".npm-cache",
    "sidecar/.cache",
    "sidecar/.codex-go-cache",
    "sidecar/.codex-go-tmp",
    "sidecar/.codex-test-tmp",
    "sidecar/.tmp",
)
PROTECTED_NAMES = {".git", ".venv", ".tools", "node_modules"}


@dataclass(frozen=True)
class Candidate:
    path: Path
    category: str
    bytes: int
    newest_mtime: float


def _is_relative_to(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
    except ValueError:
        return False
    return True


def _is_linked(path: Path) -> bool:
    return path.is_symlink() or (hasattr(os.path, "isjunction") and os.path.isjunction(path))


def _contains_protected_entry(path: Path) -> bool:
    if not path.is_dir():
        return False
    for base, directories, files in os.walk(path, followlinks=False):
        if PROTECTED_NAMES.intersection(directories) or PROTECTED_NAMES.intersection(files):
            return True
        base_path = Path(base)
        directories[:] = [name for name in directories if not _is_linked(base_path / name)]
    return False


def validate_target(root: Path, target: Path) -> Path:
    root = root.resolve()
    target = target.absolute()
    resolved = target.resolve(strict=False)
    if resolved == root or not _is_relative_to(resolved, root):
        raise ValueError(f"refusing target outside repository: {target}")
    relative_parts = resolved.relative_to(root).parts
    if not relative_parts or any(part in PROTECTED_NAMES for part in relative_parts):
        raise ValueError(f"refusing protected target: {target}")
    if _is_linked(target):
        raise ValueError(f"refusing linked target: {target}")
    if _contains_protected_entry(target):
        raise ValueError(f"refusing target containing protected entries: {target}")
    return resolved


def _measure(path: Path) -> tuple[int, float]:
    if path.is_file():
        stat = path.stat()
        return stat.st_size, stat.st_mtime
    total = 0
    newest = path.stat().st_mtime
    for base, directories, files in os.walk(path, followlinks=False):
        base_path = Path(base)
        directories[:] = [
            name
            for name in directories
            if not (base_path / name).is_symlink()
            and not (hasattr(os.path, "isjunction") and os.path.isjunction(base_path / name))
        ]
        for name in files:
            try:
                stat = (base_path / name).stat()
            except OSError:
                continue
            total += stat.st_size
            newest = max(newest, stat.st_mtime)
    return total, newest


def _existing(paths: Iterable[Path]) -> Iterable[Path]:
    return (path for path in paths if path.exists())


def _artifact_paths(root: Path, keep_qa_runs: int) -> Iterable[Path]:
    build = root / "build"
    if build.is_dir():
        yield from (
            path
            for path in build.iterdir()
            if path.name != "qa" and not _contains_protected_entry(path)
        )
        qa = build / "qa"
        if qa.is_dir():
            qa_runs = sorted(
                (path for path in qa.iterdir() if path.is_dir()),
                key=lambda path: path.stat().st_mtime,
                reverse=True,
            )
            yield from qa_runs[keep_qa_runs:]

    dist = root / "dist"
    if dist.is_dir():
        yield from dist.iterdir()
    yield root / "VibeTable.Next"
    yield root / "desktop" / "web-grid" / "dist"
    yield root / "sidecar" / "build"
    yield root / "sidecar" / "vibetable-pb.exe"

    for projects in (root / "desktop" / "src", root / "desktop" / "tests"):
        if not projects.is_dir():
            continue
        for project in projects.iterdir():
            if project.is_dir():
                yield project / "bin"
                yield project / "obj"


def collect_candidates(
    root: Path,
    *,
    scope: str,
    older_than_days: int,
    keep_qa_runs: int,
    now: float | None = None,
) -> list[Candidate]:
    if older_than_days < 0:
        raise ValueError("older_than_days must be non-negative")
    if keep_qa_runs < 0:
        raise ValueError("keep_qa_runs must be non-negative")
    root = root.resolve()
    paths: list[tuple[Path, str]] = []
    if scope in {"caches", "all"}:
        paths.extend((root / relative, "cache") for relative in CACHE_ROOTS)
    if scope in {"artifacts", "all"}:
        paths.extend((path, "artifact") for path in _artifact_paths(root, keep_qa_runs))

    cutoff = (time.time() if now is None else now) - older_than_days * 86_400
    candidates: list[Candidate] = []
    seen: set[Path] = set()
    for raw_path, category in paths:
        if not raw_path.exists():
            continue
        path = validate_target(root, raw_path)
        if path in seen:
            continue
        seen.add(path)
        size, newest = _measure(path)
        if newest <= cutoff:
            candidates.append(Candidate(path, category, size, newest))
    return sorted(candidates, key=lambda item: str(item.path).lower())


def remove_candidates(root: Path, candidates: Iterable[Candidate]) -> list[str]:
    failures: list[str] = []
    for candidate in candidates:
        try:
            path = validate_target(root, candidate.path)
            if path.is_dir():
                shutil.rmtree(path, onerror=_retry_readonly)
            elif path.exists():
                path.unlink()
        except (OSError, ValueError) as exc:
            failures.append(f"{candidate.path}: {exc}")
    return failures


def _retry_readonly(
    function: Callable[[str], object],
    path: str,
    exc_info: tuple[type[BaseException], BaseException, object],
) -> None:
    error = exc_info[1]
    if not isinstance(error, PermissionError):
        raise error
    os.chmod(path, stat.S_IWRITE)
    function(path)


def _format_bytes(value: int) -> str:
    units = ("B", "KiB", "MiB", "GiB", "TiB")
    amount = float(value)
    for unit in units:
        if amount < 1024 or unit == units[-1]:
            return f"{amount:.2f} {unit}"
        amount /= 1024
    raise AssertionError("unreachable")


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scope", choices=("caches", "artifacts", "all"), default="caches")
    parser.add_argument("--older-than-days", type=int, default=3)
    parser.add_argument("--keep-qa-runs", type=int, default=2)
    parser.add_argument(
        "--apply",
        action="store_true",
        help="delete selected paths; without this flag the command is a dry run",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        candidates = collect_candidates(
            REPO_ROOT,
            scope=args.scope,
            older_than_days=args.older_than_days,
            keep_qa_runs=args.keep_qa_runs,
        )
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    total = sum(candidate.bytes for candidate in candidates)
    mode = "APPLY" if args.apply else "DRY-RUN"
    for candidate in candidates:
        relative = candidate.path.relative_to(REPO_ROOT)
        print(f"[{mode}] {candidate.category:8} {_format_bytes(candidate.bytes):>12}  {relative}")
    print(f"{mode}: {len(candidates)} targets, {_format_bytes(total)} reclaimable")
    if not args.apply:
        return 0
    failures = remove_candidates(REPO_ROOT, candidates)
    for failure in failures:
        print(f"error: {failure}", file=sys.stderr)
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
