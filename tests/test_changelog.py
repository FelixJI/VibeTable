from __future__ import annotations

from pathlib import Path

import pytest

from scripts import changelog

REPO_ROOT = Path(__file__).resolve().parent.parent


def test_first_formal_release_uses_visible_history_from_head(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        changelog,
        "_previous_release_tag",
        lambda _repo_root, _version: None,
    )
    commit = "a" * 40
    monkeypatch.setattr(
        changelog,
        "_git",
        lambda _repo_root, *args: f"{commit}\x1ffeat: 初始化项目\x1ffeat: 初始化项目\x1e",
    )

    entries = changelog.collect_changelog(REPO_ROOT, "0.1.0")

    assert entries == [changelog.ChangelogEntry(subject="feat: 初始化项目", commit="aaaaaaaa")]
    assert changelog.render_markdown("0.1.0", entries) == (
        "# VibeTable 0.1.0\n\n## 变更\n\n- feat: 初始化项目 (`aaaaaaaa`)\n\n"
    )


def test_changelog_uses_user_visible_conventional_types_and_directives(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    git_args: list[tuple[str, ...]] = []

    def record(seed: str, subject: str, body: str = "") -> str:
        commit = seed + "0" * (40 - len(seed))
        message = subject if not body else f"{subject}\n\n{body}"
        return f"{commit}\x1f{subject}\x1f{message}\x1e"

    def fake_git(_repo_root: Path, *args: str) -> str:
        git_args.append(args)
        return "".join(
            [
                record("a1b2c3d", "feat: 新增筛选器"),
                record("b2c3d4e", "fix(export): 修复导出"),
                record("c3d4e5f", "perf!: 重写大型表格渲染"),
                record("d4e5f6a", "revert: 回退不兼容的导入器"),
                record("e5f6a7b", "ci: 更新 Windows runner"),
                record("f6a7b8c", "test: 增加导出测试"),
                record("a7b8c9d", "docs: 更新开发文档"),
                record("b8c9d0e", "chore(release): refresh generated changelog"),
                record("c9d0e1f", "重排内部模块"),
                record("d0e1f2a", "docs: 发布用户迁移指南", "Changelog: include"),
                record("e1f2a3b", "fix: 隐藏实验功能", "Changelog: skip"),
                record(
                    "f2a3b4c",
                    "chore(storage): 迁移本地索引",
                    "BREAKING CHANGE: 旧索引格式不再支持",
                ),
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
        ("log", "--no-merges", "--format=%H%x1f%s%x1f%B%x1e", "v0.1.0..HEAD"),
    ]
    assert entries == [
        changelog.ChangelogEntry(subject="feat: 新增筛选器", commit="a1b2c3d0"),
        changelog.ChangelogEntry(subject="fix(export): 修复导出", commit="b2c3d4e0"),
        changelog.ChangelogEntry(
            subject="perf!: 重写大型表格渲染",
            commit="c3d4e5f0",
            category="breaking",
        ),
        changelog.ChangelogEntry(subject="revert: 回退不兼容的导入器", commit="d4e5f6a0"),
        changelog.ChangelogEntry(subject="docs: 发布用户迁移指南", commit="d0e1f2a0"),
        changelog.ChangelogEntry(
            subject="chore(storage): 迁移本地索引",
            commit="f2a3b4c0",
            category="breaking",
        ),
    ]
