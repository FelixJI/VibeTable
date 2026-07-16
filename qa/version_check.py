#!/usr/bin/env python3
"""只读检查 VibeTable 所有发布组件的版本是否一致。"""

from __future__ import annotations

import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parent.parent
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from scripts.versioning import VersionError, check_versions, read_project_version


def main() -> int:
    try:
        errors = check_versions(PROJECT_ROOT)
        version = read_project_version(PROJECT_ROOT)
    except (OSError, ValueError, VersionError) as exc:
        print(f"[FAIL] 无法读取版本元数据：{exc}", file=sys.stderr)
        return 1
    if errors:
        print("[FAIL] 跨组件版本不一致：", file=sys.stderr)
        for error in errors:
            print(f"  - {error}", file=sys.stderr)
        return 1
    print(f"[OK] 所有发布组件版本一致：{version}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
