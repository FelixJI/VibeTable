#!/usr/bin/env python3
"""
代码质量控制主脚本
支持交互式选择检查项并生成报告

用法:
    python qa/run.py              # 交互式选择检查项
    python qa/run.py --all        # 运行所有检查
    python qa/run.py --fix        # 自动修复问题
    python qa/run.py --quick      # 快速检查（跳过测试）
    python qa/run.py --ci         # CI 模式（严格检查）
    python qa/run.py --report     # 生成报告文件
"""

import argparse
import json
import subprocess
import sys
import time
from datetime import datetime
from pathlib import Path

PROJECT_ROOT = Path(__file__).parent.parent
QA_DIR = PROJECT_ROOT / "qa"
REPORTS_DIR = PROJECT_ROOT / "reports"

# 检查项配置
CHECKS = {
    "version": {
        "name": "版本一致性",
        "script": "version_check.py",
        "description": "检查 Python、WPF、Web、Directus 与发布清单版本",
    },
    "package": {
        "name": "打包契约",
        "script": "package_check.py",
        "description": "只读检查打包输入和发布清单",
    },
    "format": {
        "name": "代码格式化",
        "script": "format.py",
        "description": "使用 ruff/black 检查代码格式",
    },
    "lint": {
        "name": "代码检查",
        "script": "lint.py",
        "description": "使用 ruff 检查代码问题",
    },
    "type_check": {
        "name": "类型检查",
        "script": "type_check.py",
        "description": "使用 pyright 进行静态类型检查",
    },
    "coverage": {
        "name": "测试覆盖率",
        "script": "coverage.py",
        "description": "运行测试并生成覆盖率报告",
    },
    "upgrade_deps": {
        "name": "依赖升级",
        "script": "upgrade_deps.py",
        "description": "升级依赖并同步 pyproject.toml",
    },
}

# “全部质量检查”必须保持只读；依赖升级只能由用户显式选择。
QUALITY_CHECKS: tuple[str, ...] = (
    "version",
    "package",
    "format",
    "lint",
    "type_check",
    "coverage",
)


def ensure_reports_dir() -> None:
    """确保报告目录存在"""
    REPORTS_DIR.mkdir(exist_ok=True)


def run_script(script_name: str, args: list[str] | None = None) -> tuple[int, str, str]:
    """运行 QA 目录下的脚本，返回 (返回码, stdout, stderr)"""
    cmd = [sys.executable, str(QA_DIR / script_name)]
    if args:
        cmd.extend(args)

    result = subprocess.run(
        cmd,
        cwd=PROJECT_ROOT,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    return result.returncode, result.stdout, result.stderr


def select_checks_interactively() -> list[str]:
    """交互式选择检查项"""
    print("\n" + "=" * 60)
    print("  代码质量检查工具")
    print("=" * 60)
    print("\n可用的检查项:\n")

    check_list = list(CHECKS.keys())
    for i, key in enumerate(check_list, 1):
        check = CHECKS[key]
        print(f"  [{i}] {check['name']}")
        print(f"      {check['description']}")
        print()

    print("  [A] 全部选择")
    print("  [N] 全不选")
    print("  [Q] 退出")
    print()

    # 显示默认选择
    default_selection = set(QUALITY_CHECKS)
    print("  默认: 全部选中")
    print()

    while True:
        try:
            user_input = input("请选择检查项 (输入数字/字母，用空格分隔，回车确认): ").strip()
        except (EOFError, KeyboardInterrupt):
            print("\n已取消")
            sys.exit(0)

        if not user_input:
            # 回车使用默认选择
            return list(default_selection)

        selected = set()

        for part in user_input.upper().split():
            if part == "A":
                selected = set(QUALITY_CHECKS)
            elif part == "N":
                selected = set()
            elif part == "Q":
                print("已退出")
                sys.exit(0)
            else:
                try:
                    idx = int(part)
                    if 1 <= idx <= len(check_list):
                        selected.add(check_list[idx - 1])
                    else:
                        print(f"  无效选择: {idx}")
                except ValueError:
                    # 切换选择状态
                    if part.lower() in check_list:
                        if part.lower() in selected:
                            selected.remove(part.lower())
                        else:
                            selected.add(part.lower())
                    else:
                        print(f"  无效输入: {part}")

        if selected:
            print(f"\n已选择: {', '.join(CHECKS[k]['name'] for k in selected if k in CHECKS)}")
            confirm = input("确认执行? [Y/n]: ").strip().lower()
            if confirm in ("", "y", "yes"):
                return list(selected)
            print()
        else:
            print("  请至少选择一个检查项\n")


def run_selected_checks(
    selected_checks: list[str],
    *,
    fix: bool = False,
    quick: bool = False,
    ci: bool = False,
) -> dict:
    """运行选中的检查并返回结果"""
    start_time = time.time()
    results = {}

    for check_key in selected_checks:
        if check_key not in CHECKS:
            continue

        check = CHECKS[check_key]
        script = check["script"]
        check_start = time.time()

        print(f"\n{'=' * 60}")
        print(f"  运行: {check['name']}")
        print("=" * 60)

        # 构建参数
        args = []
        if check_key == "format":
            args = [] if fix else ["--check"]
        elif fix and check_key == "lint":
            args = ["--fix"]

        # 类型检查默认使用严格模式
        if check_key == "type_check":
            args.append("--strict")

        if ci and check_key == "lint":
            args.append("--strict")

        if quick and check_key == "coverage":
            args.append("--quick")

        returncode, stdout, stderr = run_script(script, args if args else None)
        elapsed = time.time() - check_start

        # 打印输出到控制台（处理 Windows 控制台编码问题）
        if stdout:
            try:
                print(stdout)
            except UnicodeEncodeError:
                # Windows 控制台可能无法编码某些 Unicode 字符
                print(
                    stdout.encode(sys.stdout.encoding, errors="replace").decode(sys.stdout.encoding)
                )
        if stderr:
            try:
                print(stderr)
            except UnicodeEncodeError:
                print(
                    stderr.encode(sys.stderr.encoding, errors="replace").decode(sys.stderr.encoding)
                )

        # 合并 stdout 和 stderr 作为详细输出
        output = (stdout or "") + (stderr or "")

        results[check_key] = {
            "name": check["name"],
            "success": returncode == 0,
            "returncode": returncode,
            "elapsed": elapsed,
            "output": output.strip(),
        }

    total_elapsed = time.time() - start_time
    results["_meta"] = {
        "total_elapsed": total_elapsed,
        "timestamp": datetime.now().isoformat(),
    }

    return results


def print_summary(results: dict) -> int:
    """打印结果汇总"""
    meta = results.get("_meta", {})

    print("\n" + "=" * 60)
    print("  检查结果汇总")
    print("=" * 60)

    total_errors = 0
    for key, data in results.items():
        if key == "_meta":
            continue
        status = "[OK] 通过" if data["success"] else "[FAIL] 失败"
        elapsed = data.get("elapsed", 0)
        print(f"  {data['name']}: {status} ({elapsed:.2f}s)")
        if not data["success"]:
            total_errors += 1

    print(f"\n  总耗时: {meta.get('total_elapsed', 0):.2f}s")
    print("=" * 60)

    if total_errors == 0:
        print("\n[OK] 所有检查通过!\n")
        return 0
    else:
        print(f"\n[FAIL] 发现 {total_errors} 项检查未通过\n")
        return 1


def generate_report(results: dict, output_format: str = "text") -> str:
    """生成检查报告"""
    meta = results.get("_meta", {})
    timestamp = meta.get("timestamp", datetime.now().isoformat())

    if output_format == "json":
        return json.dumps(results, indent=2, ensure_ascii=False)

    # 文本报告
    lines = [
        "=" * 80,
        "  代码质量检查报告",
        "=" * 80,
        f"  时间: {timestamp}",
        f"  总耗时: {meta.get('total_elapsed', 0):.2f}s",
        "",
        "-" * 80,
        "  检查结果汇总",
        "-" * 80,
    ]

    passed = 0
    failed = 0

    for key, data in results.items():
        if key == "_meta":
            continue
        status = "[OK] 通过" if data["success"] else "[FAIL] 失败"
        elapsed = data.get("elapsed", 0)
        lines.append(f"  {data['name']}: {status} ({elapsed:.2f}s)")
        if data["success"]:
            passed += 1
        else:
            failed += 1

    lines.extend(
        [
            "",
            "-" * 80,
            "  统计",
            "-" * 80,
            f"  通过: {passed}",
            f"  失败: {failed}",
            f"  总计: {passed + failed}",
        ]
    )

    # 添加详细问题
    if failed > 0:
        lines.extend(
            [
                "",
                "",
                "=" * 80,
                "  详细问题",
                "=" * 80,
            ]
        )

        for key, data in results.items():
            if key == "_meta":
                continue
            if not data["success"] and data.get("output"):
                lines.extend(
                    [
                        "",
                        "-" * 80,
                        f"  [{data['name']}]",
                        "-" * 80,
                    ]
                )
                # 添加原始输出
                lines.append(data["output"])

    lines.append("")
    lines.append("=" * 80)

    return "\n".join(lines)


def save_report(results: dict, format_type: str = "all") -> None:
    """保存报告到文件"""
    ensure_reports_dir()
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")

    if format_type in ("text", "all"):
        report_text = generate_report(results, "text")
        text_file = REPORTS_DIR / f"report_{timestamp}.txt"
        text_file.write_text(report_text, encoding="utf-8")
        print(f"[REPORT] 文本报告: {text_file}")

    if format_type in ("json", "all"):
        report_json = generate_report(results, "json")
        json_file = REPORTS_DIR / f"report_{timestamp}.json"
        json_file.write_text(report_json, encoding="utf-8")
        print(f"[REPORT] JSON报告: {json_file}")


def main() -> int:
    parser = argparse.ArgumentParser(
        description="代码质量控制工具",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
示例:
  python qa/run.py              # 交互式选择检查项
  python qa/run.py --all        # 运行所有检查
  python qa/run.py format lint  # 只运行格式化和代码检查
  python qa/run.py --fix        # 自动修复问题并生成报告
  python qa/run.py --report     # 生成报告文件
        """,
    )
    parser.add_argument(
        "checks",
        nargs="*",
        choices=[*list(CHECKS.keys()), "all"],
        help="要运行的检查项",
    )
    parser.add_argument(
        "--all",
        dest="run_all",
        action="store_true",
        help="运行全部只读质量检查（不包含依赖升级）",
    )
    parser.add_argument(
        "--fix",
        action="store_true",
        help="自动修复问题",
    )
    parser.add_argument(
        "--quick",
        action="store_true",
        help="快速检查（跳过测试）",
    )
    parser.add_argument(
        "--ci",
        action="store_true",
        help="CI 模式（严格检查）",
    )
    parser.add_argument(
        "--report",
        action="store_true",
        default=True,
        help="生成报告文件（默认启用）",
    )
    parser.add_argument(
        "--no-report",
        action="store_true",
        help="不生成报告文件",
    )
    parser.add_argument(
        "--report-format",
        choices=["text", "json", "all"],
        default="all",
        help="报告格式 (默认: all)",
    )
    parser.add_argument(
        "--no-interactive",
        action="store_true",
        help="禁用交互模式（用于脚本调用）",
    )
    args = parser.parse_args()

    # 确定要运行的检查项
    selected: list[str]
    if args.run_all or "all" in args.checks:
        selected = list(QUALITY_CHECKS)
    elif args.checks:
        selected = args.checks
    elif args.no_interactive or args.ci:
        selected = list(QUALITY_CHECKS)
    else:
        # 交互式选择
        selected = select_checks_interactively()

    if not selected:
        print("未选择任何检查项")
        return 0

    # 运行检查
    results = run_selected_checks(
        selected,
        fix=args.fix,
        quick=args.quick,
        ci=args.ci,
    )

    # 打印汇总
    exit_code = print_summary(results)

    # 默认保存报告（除非明确指定 --no-report）
    if not hasattr(args, "no_report") or not args.no_report:
        save_report(results, args.report_format)

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
