#!/usr/bin/env python3
"""不执行编译的发布打包契约检查。"""

from __future__ import annotations

import json
import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parent.parent
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from scripts.build_next import RepoPaths, render_manifest
from scripts.versioning import check_versions


def _required_files(paths: RepoPaths) -> list[Path]:
    files = [
        paths.repo_root / "pyproject.toml",
        paths.backend_main,
        paths.desktop_csproj,
        paths.web_grid_dir / "package.json",
        paths.web_grid_dir / "package-lock.json",
    ]
    # G0.2: every declared extension must have package.json + package-lock.json.
    for ext_dir in paths.directus_extension_dirs:
        files.append(ext_dir / "package.json")
        files.append(ext_dir / "package-lock.json")
    return files


def main() -> int:
    paths = RepoPaths.default(PROJECT_ROOT)
    errors = check_versions(PROJECT_ROOT)
    errors.extend(
        f"缺少打包输入：{path.relative_to(PROJECT_ROOT)}"
        for path in _required_files(paths)
        if not path.is_file()
    )

    # G0.2: validate every declared extension's package metadata.
    required_scripts = {"build", "test", "typecheck"}
    for ext_dir in paths.directus_extension_dirs:
        package_path = ext_dir / "package.json"
        if not package_path.is_file():
            continue
        extension_package = json.loads(package_path.read_text(encoding="utf-8"))
        extension_metadata = extension_package.get("directus:extension", {})
        if extension_metadata.get("path") != "dist/index.js":
            errors.append(f"Directus 扩展 {ext_dir.name} 入口必须是 dist/index.js")
        if not required_scripts.issubset(extension_package.get("scripts", {})):
            errors.append(f"Directus 扩展 {ext_dir.name} 缺少 build/test/typecheck 脚本")

    committed_layout = json.loads(
        (PROJECT_ROOT / "desktop" / "publish-layout.json").read_text(encoding="utf-8")
    )
    generated_layout = json.loads(render_manifest(paths))
    if committed_layout != generated_layout:
        errors.append("desktop/publish-layout.json 与打包脚本生成清单不一致")

    if errors:
        print("[FAIL] 发布打包契约不完整：", file=sys.stderr)
        for error in errors:
            print(f"  - {error}", file=sys.stderr)
        return 1
    print("[OK] 发布打包输入、版本与清单契约有效")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
