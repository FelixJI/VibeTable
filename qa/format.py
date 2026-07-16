#!/usr/bin/env python3
"""
代码格式化脚本
使用 ruff format 格式化代码（默认），也支持 black + isort

用法:
    python qa/format.py           # 格式化代码（使用 ruff）
    python qa/format.py --check   # 检查格式（不修改）
    python qa/format.py --diff    # 显示差异
    python qa/format.py --black   # 使用 black + isort 替代 ruff
"""

import argparse
import subprocess
import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).parent.parent
TARGET_DIRS = ["backend", "qa", "scripts", "tests"]


def run_command(cmd: list[str]) -> subprocess.CompletedProcess:
    """运行命令并返回结果"""
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
    if result.stdout:
        print(result.stdout, end="")
    if result.stderr:
        print(result.stderr, end="", file=sys.stderr)
    return result


def run_black(*, check: bool = False, diff: bool = False) -> int:
    """运行 Black 格式化"""
    cmd = ["black"]
    if check:
        cmd.extend(["--check"])
    if diff:
        cmd.extend(["--diff"])
    cmd.extend(TARGET_DIRS)
    return run_command(cmd).returncode


def run_isort(*, check: bool = False, diff: bool = False) -> int:
    """运行 isort 导入排序"""
    cmd = ["isort"]
    if check:
        cmd.extend(["--check-only"])
    if diff:
        cmd.extend(["--diff"])
    cmd.extend(TARGET_DIRS)
    return run_command(cmd).returncode


def run_ruff_format(*, check: bool = False, diff: bool = False) -> int:
    """运行 Ruff 格式化（可选，作为 black 的替代）"""
    cmd = ["ruff", "format"]
    if check:
        cmd.extend(["--check"])
    if diff:
        cmd.extend(["--diff"])
    cmd.extend(TARGET_DIRS)
    return run_command(cmd).returncode


def main() -> int:
    parser = argparse.ArgumentParser(description="代码格式化工具")
    parser.add_argument(
        "--check",
        action="store_true",
        help="检查格式（不修改文件）",
    )
    parser.add_argument(
        "--diff",
        action="store_true",
        help="显示格式差异",
    )
    parser.add_argument(
        "--ruff",
        action="store_true",
        default=True,
        help="使用 ruff format（默认）",
    )
    parser.add_argument(
        "--black",
        action="store_true",
        help="使用 black 替代 ruff format",
    )
    args = parser.parse_args()

    total_errors = 0

    print("=" * 60)
    print("代码格式化")
    print("=" * 60)

    # 默认使用 ruff，除非指定 --black
    use_ruff = not args.black

    if use_ruff:
        total_errors += run_ruff_format(check=args.check, diff=args.diff)
    else:
        total_errors += run_black(check=args.check, diff=args.diff)
        total_errors += run_isort(check=args.check, diff=args.diff)

    print("\n" + "=" * 60)
    if total_errors == 0:
        print("[OK] 格式检查通过!")
    else:
        print(f"[FAIL] 发现 {total_errors} 个格式问题")
    print("=" * 60)

    return min(total_errors, 1)


if __name__ == "__main__":
    sys.exit(main())
