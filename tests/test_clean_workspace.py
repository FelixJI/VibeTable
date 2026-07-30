from __future__ import annotations

import os
from contextlib import suppress
from pathlib import Path

import pytest

from scripts.clean_workspace import collect_candidates, remove_candidates, validate_target


def _old(path: Path, *, root: Path, now: float) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"x")
    old = now - 10 * 86_400
    os.utime(path, (old, old))
    for parent in path.parents:
        if parent == root:
            break
        with suppress(OSError):
            os.utime(parent, (old, old))


def test_cache_cleanup_is_dry_until_candidates_are_explicitly_removed(tmp_path: Path) -> None:
    now = 2_000_000_000.0
    cached = tmp_path / ".codex-go-cache" / "entry"
    _old(cached, root=tmp_path, now=now)
    sidecar_cached = tmp_path / "sidecar" / ".codex-go-cache" / "entry"
    _old(sidecar_cached, root=tmp_path, now=now)

    candidates = collect_candidates(
        tmp_path,
        scope="caches",
        older_than_days=7,
        keep_qa_runs=2,
        now=now,
    )

    assert [item.path for item in candidates] == [
        tmp_path / ".codex-go-cache",
        tmp_path / "sidecar" / ".codex-go-cache",
    ]
    assert cached.exists()
    assert sidecar_cached.exists()
    assert remove_candidates(tmp_path, candidates) == []
    assert not cached.exists()
    assert not sidecar_cached.exists()


def test_artifact_cleanup_preserves_recent_qa_runs_and_protected_dependencies(
    tmp_path: Path,
) -> None:
    now = 2_000_000_000.0
    for name in ("qa-old", "qa-middle", "qa-new"):
        _old(tmp_path / "build" / "qa" / name / "result.json", root=tmp_path, now=now)
    os.utime(tmp_path / "build" / "qa" / "qa-middle", (now - 20, now - 20))
    os.utime(tmp_path / "build" / "qa" / "qa-new", (now - 10, now - 10))
    _old(tmp_path / "build" / "next-scratch" / "host.exe", root=tmp_path, now=now)
    _old(tmp_path / "sidecar" / "build" / "sidecar.exe", root=tmp_path, now=now)
    _old(tmp_path / "sidecar" / "vibetable-pb.exe", root=tmp_path, now=now)
    _old(
        tmp_path / "desktop" / "src" / "Host" / "bin" / "host.dll",
        root=tmp_path,
        now=now,
    )
    _old(tmp_path / ".tools" / "sdk" / "tool.exe", root=tmp_path, now=now)
    _old(
        tmp_path / "desktop" / "web-grid" / "node_modules" / "pkg" / "index.js",
        root=tmp_path,
        now=now,
    )

    candidates = collect_candidates(
        tmp_path,
        scope="artifacts",
        older_than_days=7,
        keep_qa_runs=2,
        now=now,
    )
    relative = {item.path.relative_to(tmp_path).as_posix() for item in candidates}

    assert "build/qa/qa-old" in relative
    assert "build/qa/qa-middle" not in relative
    assert "build/qa/qa-new" not in relative
    assert "build/next-scratch" in relative
    assert "sidecar/build" in relative
    assert "sidecar/vibetable-pb.exe" in relative
    assert "desktop/src/Host/bin" in relative
    assert all(".tools" not in item and "node_modules" not in item for item in relative)


def test_target_validation_refuses_repository_root_and_protected_paths(tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="outside repository"):
        validate_target(tmp_path, tmp_path)
    with pytest.raises(ValueError, match="protected target"):
        validate_target(tmp_path, tmp_path / ".git" / "objects")
    with pytest.raises(ValueError, match="protected target"):
        validate_target(tmp_path, tmp_path / ".venv" / "Scripts")


def test_cleanup_retries_readonly_generated_files(tmp_path: Path) -> None:
    now = 2_000_000_000.0
    generated = tmp_path / ".cache" / "module" / "BUILD.bazel"
    _old(generated, root=tmp_path, now=now)
    generated.chmod(0o444)
    candidates = collect_candidates(
        tmp_path,
        scope="caches",
        older_than_days=0,
        keep_qa_runs=2,
        now=now,
    )

    assert remove_candidates(tmp_path, candidates) == []
    assert not generated.exists()
