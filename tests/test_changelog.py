from __future__ import annotations

from pathlib import Path

import pytest

from scripts import changelog
from scripts.versioning import read_project_version

REPO_ROOT = Path(__file__).resolve().parent.parent


def test_first_release_has_the_project_initialization_entry(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        changelog,
        "_previous_release_tag",
        lambda _repo_root, _version: None,
    )

    entries = changelog.collect_changelog(REPO_ROOT, "0.1.0")

    assert entries == [changelog.ChangelogEntry(subject="初始化项目", commit=None)]
    assert changelog.render_markdown("0.1.0", entries) == ("# VibeTable 0.1.0\n\n- 初始化项目\n")


def test_changelog_uses_non_merge_commits_and_filters_merge_wording(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        changelog,
        "_previous_release_tag",
        lambda _repo_root, _version: "v0.1.0",
    )
    monkeypatch.setattr(
        changelog,
        "_git",
        lambda _repo_root, *_args: "\n".join(
            [
                "a1b2c3d\x1ffeat: 新增筛选器",
                "b2c3d4e\x1fMerge branch 'feature/filter'",
                "c3d4e5f\x1f合并主分支",
                "d4e5f6a\x1fchore: release v0.1.1",
                "d5e6f7a\x1fchore(release): prepare 0.2.0",
                "e5f6a7b\x1ffix: 修复导出",
            ]
        ),
    )

    entries = changelog.collect_changelog(REPO_ROOT, "0.1.1")

    assert entries == [
        changelog.ChangelogEntry(subject="feat: 新增筛选器", commit="a1b2c3d"),
        changelog.ChangelogEntry(subject="fix: 修复导出", commit="e5f6a7b"),
    ]


def test_checked_in_changelog_matches_the_current_version() -> None:
    assert (
        changelog.check_changelog(
            REPO_ROOT,
            read_project_version(REPO_ROOT),
        )
        == []
    )
