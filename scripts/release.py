#!/usr/bin/env python3
"""安全的 VibeTable 版本更新入口。

版本只从 ``pyproject.toml`` 读取，再同步到所有发布组件。默认行为仅修改版本文件；
构建、提交和标签都必须显式启用，本脚本不会推送远程仓库。
"""

from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path

try:
    from scripts.versioning import (
        VersionError,
        bump_version,
        check_versions,
        read_project_version,
        update_versions,
        validate_version,
    )
except ModuleNotFoundError:  # direct execution: python scripts/release.py
    from versioning import (
        VersionError,
        bump_version,
        check_versions,
        read_project_version,
        update_versions,
        validate_version,
    )

REPO_ROOT = Path(__file__).resolve().parent.parent


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="同步 VibeTable 跨组件版本；默认不提交、不打标签、不推送。"
    )
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--major", action="store_true", help="递增主版本")
    action.add_argument("--minor", action="store_true", help="递增次版本")
    action.add_argument("--patch", action="store_true", help="递增修订版本")
    action.add_argument("--version", metavar="X.Y.Z", help="指定严格 SemVer 版本")
    action.add_argument("--check", action="store_true", help="只检查版本一致性")
    action.add_argument("--current", action="store_true", help="只输出当前项目版本")
    parser.add_argument("--dry-run", action="store_true", help="预览会修改的文件")
    parser.add_argument("--build", action="store_true", help="同步成功后执行完整 release 构建")
    parser.add_argument("--commit", action="store_true", help="构建成功后创建版本提交")
    parser.add_argument("--tag", action="store_true", help="创建 vX.Y.Z 标签（要求 --commit）")
    return parser


def _run(command: list[str]) -> None:
    print("$ " + " ".join(command), flush=True)
    subprocess.run(command, cwd=REPO_ROOT, check=True)


def _target_version(args: argparse.Namespace, current: str) -> str:
    if args.version:
        return validate_version(args.version)
    for part in ("major", "minor", "patch"):
        if getattr(args, part):
            return bump_version(current, part)
    raise VersionError("未指定版本更新方式")


def _print_check() -> int:
    errors = check_versions(REPO_ROOT)
    if errors:
        print("版本不一致：", file=sys.stderr)
        for error in errors:
            print(f"  - {error}", file=sys.stderr)
        return 1
    print(f"版本一致：{read_project_version(REPO_ROOT)}")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = _parser()
    args = parser.parse_args(argv)
    if args.tag and not args.commit:
        parser.error("--tag 必须与 --commit 一起使用")
    if args.dry_run and (args.build or args.commit or args.tag):
        parser.error("--dry-run 不能与 --build/--commit/--tag 组合")

    current = read_project_version(REPO_ROOT)
    if args.current:
        print(current)
        return 0
    if args.check:
        return _print_check()

    try:
        target = _target_version(args, current)
        changed = update_versions(REPO_ROOT, target, dry_run=args.dry_run)
    except (OSError, VersionError, ValueError) as exc:
        print(f"版本更新失败：{exc}", file=sys.stderr)
        return 1

    verb = "将更新" if args.dry_run else "已更新"
    print(f"{current} -> {target}；{verb} {len(changed)} 个文件：")
    for path in changed:
        print(f"  - {path.relative_to(REPO_ROOT)}")
    if args.dry_run:
        return 0
    if _print_check() != 0:
        return 1

    try:
        if args.build:
            _run([sys.executable, "scripts/build_next.py", "--release"])
        if args.commit:
            _run(["git", "add", *[str(path.relative_to(REPO_ROOT)) for path in changed]])
            _run(["git", "commit", "-m", f"chore: release v{target}"])
        if args.tag:
            _run(["git", "tag", "-a", f"v{target}", "-m", f"VibeTable v{target}"])
    except (OSError, subprocess.CalledProcessError) as exc:
        print(f"发布步骤失败：{exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
