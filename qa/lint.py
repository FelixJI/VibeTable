#!/usr/bin/env python3
"""
代码检查脚本
使用 ruff 检查代码问题

用法:
    python qa/lint.py           # 检查代码
    python qa/lint.py --fix     # 自动修复问题
    python qa/lint.py --stats   # 显示统计信息
"""

import argparse
import json
import subprocess
import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).parent.parent
TARGET_DIRS = ["backend", "qa", "scripts", "tests"]


def run_command(cmd: list[str], *, silent: bool = False) -> subprocess.CompletedProcess:
    """运行命令并返回结果

    Args:
        cmd: 要运行的命令
        silent: 如果为 True，不打印输出（用于需要解析输出的场景）
    """
    print(f"运行: {' '.join(cmd)}")
    result = subprocess.run(
        cmd,
        cwd=PROJECT_ROOT,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    # 始终打印输出，确保在被父脚本调用时输出能被捕获
    if not silent:
        if result.stdout:
            print(result.stdout, end="")
        if result.stderr:
            print(result.stderr, end="", file=sys.stderr)
    return result


def run_ruff(*, fix: bool = False, output_format: str = "concise") -> int:
    """运行 Ruff linter"""
    cmd = ["ruff", "check"]
    if fix:
        cmd.append("--fix")
    cmd.extend(["--output-format", output_format])
    cmd.extend(TARGET_DIRS)
    return run_command(cmd).returncode


def get_ruff_stats() -> dict:
    """获取 Ruff 统计信息"""
    cmd = ["ruff", "check", "--output-format", "json"]
    cmd.extend(TARGET_DIRS)
    result = run_command(cmd, silent=True)

    if result.returncode == 0:
        return {"total": 0, "by_code": {}}

    try:
        issues = json.loads(result.stdout) if result.stdout else []
    except json.JSONDecodeError:
        return {"total": 0, "by_code": {}}

    stats = {"total": len(issues), "by_code": {}}
    for issue in issues:
        code = issue.get("code", "UNKNOWN")
        stats["by_code"][code] = stats["by_code"].get(code, 0) + 1

    return stats


def print_stats(stats: dict) -> None:
    """打印统计信息"""
    print("\n" + "=" * 60)
    print("问题统计")
    print("=" * 60)
    print(f"总问题数: {stats['total']}")

    if stats["by_code"]:
        print("\n按错误代码分类:")
        for code, count in sorted(stats["by_code"].items(), key=lambda x: x[1], reverse=True):
            print(f"  {code}: {count}")

    print("=" * 60)


def main() -> int:
    parser = argparse.ArgumentParser(description="代码检查工具")
    parser.add_argument(
        "--fix",
        action="store_true",
        help="自动修复问题",
    )
    parser.add_argument(
        "--stats",
        action="store_true",
        help="显示统计信息",
    )
    parser.add_argument(
        "--strict",
        action="store_true",
        help="严格模式（任何警告都返回错误）",
    )
    args = parser.parse_args()

    print("=" * 60)
    print("代码检查 (Ruff)")
    print("=" * 60)

    if args.stats:
        stats = get_ruff_stats()
        print_stats(stats)
        return 0 if stats["total"] == 0 else 1

    output_format = "concise" if not args.fix else "text"
    result = run_ruff(fix=args.fix, output_format=output_format)

    print("\n" + "=" * 60)
    if result == 0:
        print("[OK] 代码检查通过!")
    else:
        print("[FAIL] 发现代码问题")
    print("=" * 60)

    return result


if __name__ == "__main__":
    sys.exit(main())
