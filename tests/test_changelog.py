from __future__ import annotations

from pathlib import Path

import pytest

from scripts import changelog

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
    git_args: list[tuple[str, ...]] = []

    def fake_git(_repo_root: Path, *args: str) -> str:
        git_args.append(args)
        return "\n".join(
            [
                f"{'a1b2c3d' + '0' * 33}\x1ffeat: 新增筛选器",
                f"{'b2c3d4e' + '0' * 33}\x1fMerge branch 'feature/filter'",
                f"{'c3d4e5f' + '0' * 33}\x1f合并主分支",
                f"{'d4e5f6a' + '0' * 33}\x1fchore: release v0.1.1",
                f"{'d5e6f7a' + '0' * 33}\x1fchore(release): prepare 0.2.0",
                f"{'d6e7f8a' + '0' * 33}\x1fchore(main): release 0.1.1",
                f"{'e5f6a7b' + '0' * 33}\x1ffix: 修复导出",
            ]
        )

    monkeypatch.setattr(
        changelog,
        "_previous_release_tag",
        lambda _repo_root, _version: "v0.1.0",
    )
    monkeypatch.setattr(changelog, "_git", fake_git)

    entries = changelog.collect_changelog(REPO_ROOT, "0.1.1")

    assert git_args == [
        ("log", "--no-merges", "--format=%H%x1f%s", "v0.1.0..HEAD"),
    ]
    assert entries == [
        changelog.ChangelogEntry(subject="feat: 新增筛选器", commit="a1b2c3d0"),
        changelog.ChangelogEntry(subject="fix: 修复导出", commit="e5f6a7b0"),
    ]
