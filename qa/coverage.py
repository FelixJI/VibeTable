#!/usr/bin/env python3
"""
测试覆盖率脚本
使用 pytest-cov 生成覆盖率报告

用法:
    python qa/coverage.py           # 运行测试并生成覆盖率报告
    python qa/coverage.py --html    # 生成 HTML 报告
    python qa/coverage.py --xml     # 生成 XML 报告
    python qa/coverage.py --min 80  # 设置最低覆盖率阈值
"""

import argparse
import subprocess
import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).parent.parent


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


def run_coverage(
    *,
    html: bool = False,
    xml: bool = False,
    min_coverage: int | None = None,
    verbose: bool = True,
) -> int:
    """运行测试并生成覆盖率报告"""
    cmd = ["pytest"]

    if verbose:
        cmd.append("-v")

    cmd.extend(
        [
            "--cov=backend",
            "--cov-report=term-missing",
        ]
    )

    if html:
        cmd.append("--cov-report=html:htmlcov")
    if xml:
        cmd.append("--cov-report=xml:coverage.xml")
    if min_coverage is not None:
        cmd.append(f"--cov-fail-under={min_coverage}")

    cmd.append("tests/")

    return run_command(cmd).returncode


def run_quick_coverage() -> int:
    """快速覆盖率检查（无详细输出）"""
    cmd = [
        "pytest",
        "--cov=backend",
        "--cov-report=term",
        "-q",
        "tests/",
    ]
    return run_command(cmd).returncode


def main() -> int:
    parser = argparse.ArgumentParser(description="测试覆盖率工具")
    parser.add_argument(
        "--html",
        action="store_true",
        help="生成 HTML 覆盖率报告",
    )
    parser.add_argument(
        "--xml",
        action="store_true",
        help="生成 XML 覆盖率报告",
    )
    parser.add_argument(
        "--min",
        type=int,
        default=None,
        metavar="N",
        help="设置最低覆盖率阈值 (0-100)",
    )
    parser.add_argument(
        "--quick",
        action="store_true",
        help="快速模式（减少输出）",
    )
    args = parser.parse_args()

    print("=" * 60)
    print("测试覆盖率")
    print("=" * 60)

    if args.quick:
        result = run_quick_coverage()
    else:
        result = run_coverage(
            html=args.html,
            xml=args.xml,
            min_coverage=args.min,
        )

    print("\n" + "=" * 60)
    if result == 0:
        print("[OK] 覆盖率检查通过!")
        if args.html:
            print("[REPORT] HTML 报告已生成到 htmlcov/index.html")
        if args.xml:
            print("[REPORT] XML 报告已生成到 coverage.xml")
    else:
        print("[FAIL] 覆盖率检查未通过")
    print("=" * 60)

    return result


if __name__ == "__main__":
    sys.exit(main())
