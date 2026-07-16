#!/usr/bin/env python3
"""
依赖升级脚本
使用 uv 升级依赖并同步 pyproject.toml

用法:
    python qa/upgrade_deps.py           # 升级依赖
    python qa/upgrade_deps.py --dry-run # 预览变更
    python qa/upgrade_deps.py --sync    # 升级后同步环境
"""

import argparse
import re
import subprocess
import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).parent.parent
PYPROJECT_PATH = PROJECT_ROOT / "pyproject.toml"
UV_LOCK_PATH = PROJECT_ROOT / "uv.lock"


def run_command(cmd: list[str], *, check: bool = True) -> subprocess.CompletedProcess:
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
    if result.stdout:
        print(result.stdout, end="")
    if result.stderr:
        print(result.stderr, end="", file=sys.stderr)
    if check and result.returncode != 0:
        print(f"[ERROR] 命令失败: {' '.join(cmd)}")
        sys.exit(result.returncode)
    return result


def get_locked_versions() -> dict[str, str]:
    """从 uv.lock 解析已锁定的包版本"""
    if not UV_LOCK_PATH.exists():
        print("[ERROR] uv.lock 文件不存在，请先运行 uv lock")
        return {}

    content = UV_LOCK_PATH.read_text(encoding="utf-8")
    versions = {}

    # 解析 uv.lock 格式 (TOML-like)
    # [[package]]
    # name = "xxx"
    # version = "x.x.x"
    current_name = None
    for line in content.splitlines():
        line = line.strip()
        if line == "[[package]]":
            current_name = None
        elif line.startswith("name = "):
            current_name = line.split('"')[1]
        elif line.startswith("version = ") and current_name:
            version = line.split('"')[1]
            versions[current_name] = version

    return versions


def _is_in_deps_array(lines: list[str], idx: int) -> bool:
    """判断行 idx 是否位于依赖数组内

    支持以下位置：
    - [project] dependencies = [...]
    - [project.optional-dependencies] xxx = [...]
    - [dependency-groups] xxx = [...]
    """
    in_deps = False
    section_type = None  # "project" | "named-array" | None
    for i in range(idx):
        stripped = lines[i].strip()
        if stripped == "[project]":
            section_type = "project"
            in_deps = False
        elif stripped in ("[project.optional-dependencies]", "[dependency-groups]"):
            section_type = "named-array"
            in_deps = False
        elif stripped.startswith("["):
            section_type = None
            in_deps = False
        elif (section_type == "project" and stripped == "dependencies = [") or (
            section_type == "named-array" and re.match(r"^\w+\s*=\s*\[$", stripped)
        ):
            in_deps = True
        elif in_deps and stripped == "]":
            in_deps = False
    return in_deps


def parse_pyproject_dependencies() -> list[tuple[str, str, int, int]]:
    """解析 pyproject.toml 中的依赖（包括 optional-dependencies）

    返回: [(包名, 原始行, 行号, 缩进长度), ...]
    """
    content = PYPROJECT_PATH.read_text(encoding="utf-8")
    lines = content.splitlines()
    dependencies = []

    for i, line in enumerate(lines):
        if _is_in_deps_array(lines, i) and line.strip().startswith('"'):
            indent = len(line) - len(line.lstrip())
            dep_line = line.strip().strip(",").strip('"')
            dependencies.append((dep_line, line, i, indent))

    return dependencies


def update_pyproject_versions(
    locked_versions: dict[str, str], *, dry_run: bool = False
) -> list[str]:
    """更新 pyproject.toml 中的依赖版本到锁定版本（包括 optional-dependencies）

    返回: 变更列表
    """
    content = PYPROJECT_PATH.read_text(encoding="utf-8")
    lines = content.splitlines()
    changes = []

    for i, line in enumerate(lines):
        if not _is_in_deps_array(lines, i):
            continue

        if not line.strip().startswith('"'):
            continue

        indent = len(line) - len(line.lstrip())
        dep_line = line.strip().strip(",").strip('"')

        # 解析包名和版本约束
        # 支持格式: package, package>=x.x.x, package[x,y]>=x.x.x
        match = re.match(r"^([a-zA-Z0-9_-]+)(\[[^\]]+\])?([<>=!]+.+)?$", dep_line)
        if match:
            pkg_name = match.group(1)
            extras = match.group(2) or ""
            # 查找锁定版本
            locked_version = locked_versions.get(pkg_name)
            if locked_version:
                # 生成新的版本约束
                new_dep_line = f"{pkg_name}{extras}>={locked_version}"

                if new_dep_line != dep_line:
                    # 保留原有的逗号和引号格式
                    has_comma = line.strip().endswith(",")
                    new_line = " " * indent + f'"{new_dep_line}"'
                    if has_comma:
                        new_line += ","
                    lines[i] = new_line
                    changes.append(f"  {pkg_name}: {dep_line} -> {new_dep_line}")

    if changes and not dry_run:
        PYPROJECT_PATH.write_text("\n".join(lines) + "\n", encoding="utf-8")

    return changes


def run_uv_lock_upgrade(*, dry_run: bool = False) -> int:
    """运行 uv lock --upgrade"""
    if dry_run:
        print("[DRY-RUN] 将运行: uv lock --upgrade")
        return 0

    result = run_command(["uv", "lock", "--upgrade"], check=False)
    return result.returncode


def run_uv_sync(*, dry_run: bool = False) -> int:
    """运行 uv sync（含 dev/build 依赖组）"""
    if dry_run:
        print("[DRY-RUN] 将运行: uv sync --group dev --group build")
        return 0

    result = run_command(["uv", "sync", "--group", "dev", "--group", "build"], check=False)
    return result.returncode


def main() -> int:
    parser = argparse.ArgumentParser(
        description="依赖升级工具",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
示例:
  python qa/upgrade_deps.py           # 升级依赖并更新 pyproject.toml
  python qa/upgrade_deps.py --dry-run # 预览变更
  python qa/upgrade_deps.py --sync    # 升级后同步环境
        """,
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="预览变更（不实际修改文件）",
    )
    parser.add_argument(
        "--sync",
        action="store_true",
        help="升级后同步环境",
    )
    parser.add_argument(
        "--skip-lock",
        action="store_true",
        help="跳过 uv lock --upgrade（仅更新 pyproject.toml）",
    )
    args = parser.parse_args()

    print("=" * 60)
    print("  依赖升级工具")
    print("=" * 60)

    # Step 1: 运行 uv lock --upgrade
    if not args.skip_lock:
        print("\n[Step 1] 运行 uv lock --upgrade...")
        result = run_uv_lock_upgrade(dry_run=args.dry_run)
        if result != 0:
            print(f"[FAIL] uv lock --upgrade 失败 (code: {result})")
            return result

    # Step 2: 读取锁定版本
    print("\n[Step 2] 读取锁定版本...")
    locked_versions = get_locked_versions()
    if not locked_versions:
        print("[WARN] 未找到锁定版本")
        return 0

    print(f"[INFO] 找到 {len(locked_versions)} 个锁定包")

    # Step 3: 更新 pyproject.toml
    print("\n[Step 3] 更新 pyproject.toml...")
    changes = update_pyproject_versions(locked_versions, dry_run=args.dry_run)

    if changes:
        print(f"\n变更列表 ({len(changes)} 项):")
        for change in changes:
            print(change)

        if args.dry_run:
            print("\n[DRY-RUN] 未实际修改文件")
        else:
            print("\n[OK] pyproject.toml 已更新")
    else:
        print("\n[OK] 无需更新，所有依赖已是最新")

    # Step 4: 同步环境（可选）
    if args.sync:
        print("\n[Step 4] 同步环境...")
        result = run_uv_sync(dry_run=args.dry_run)
        if result != 0:
            print(f"[FAIL] uv sync 失败 (code: {result})")
            return result
        print("[OK] 环境已同步")

    print("\n" + "=" * 60)
    print("[OK] 依赖升级完成!")
    print("=" * 60)

    return 0


if __name__ == "__main__":
    sys.exit(main())
