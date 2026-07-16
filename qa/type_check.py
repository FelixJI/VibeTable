#!/usr/bin/env python3
"""
类型检查脚本
使用 pyright 进行静态类型检查（默认），也支持 mypy

用法:
    python qa/type_check.py           # 运行 pyright 类型检查
    python qa/type_check.py --pyright # 只运行 pyright
    python qa/type_check.py --mypy    # 只运行 mypy（需额外配置）
"""

import argparse
import shutil
import subprocess
import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).parent.parent
# Directus-first 生产 Python 运行时；scripts/tests 由配置显式排除。
TARGET_DIRS = ["backend"]


def run_command(cmd: list[str]) -> subprocess.CompletedProcess | None:
    """运行命令并返回结果，如果命令不存在则返回 None"""
    executable = shutil.which(cmd[0])
    if not executable:
        print(f"跳过: {cmd[0]} 未安装")
        return None
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


def run_pyright(*, strict: bool = False) -> int:
    """运行 Pyright 类型检查"""
    cmd = ["pyright"]
    if strict:
        cmd.append("--warnings")
    cmd.extend(TARGET_DIRS)
    result = run_command(cmd)
    return result.returncode if result else 1


def run_mypy() -> int:
    """运行 Mypy 类型检查"""
    cmd = ["mypy"]
    cmd.extend(TARGET_DIRS)
    result = run_command(cmd)
    return result.returncode if result else 1


def main() -> int:
    parser = argparse.ArgumentParser(description="类型检查工具")
    parser.add_argument(
        "--pyright",
        action="store_true",
        help="只运行 pyright",
    )
    parser.add_argument(
        "--mypy",
        action="store_true",
        help="只运行 mypy",
    )
    parser.add_argument(
        "--strict",
        action="store_true",
        help="严格模式（任何警告都返回错误）",
    )
    args = parser.parse_args()

    total_errors = 0

    print("=" * 60)
    print("类型检查")
    print("=" * 60)

    # 默认运行两个检查器
    run_both = not (args.pyright or args.mypy)

    if run_both or args.pyright:
        print("\n--- Pyright ---")
        total_errors += run_pyright(strict=args.strict)

    if run_both or args.mypy:
        print("\n--- Mypy ---")
        total_errors += run_mypy()

    print("\n" + "=" * 60)
    if total_errors == 0:
        print("[OK] 类型检查通过!")
    else:
        print(f"[FAIL] 发现 {total_errors} 个类型问题")
    print("=" * 60)

    return min(total_errors, 1)


if __name__ == "__main__":
    sys.exit(main())
