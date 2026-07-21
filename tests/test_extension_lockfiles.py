from __future__ import annotations

import json
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
EXTENSIONS_ROOT = REPO_ROOT / "directus" / "extensions"


def _resolve_locked_dependency(
    packages: dict[str, dict[str, object]], package_path: str, dependency: str
) -> dict[str, object] | None:
    search_root = f"/{package_path}"
    while True:
        candidate = f"{search_root}/node_modules/{dependency}".lstrip("/")
        if candidate in packages:
            return packages[candidate]
        if "/node_modules/" not in search_root:
            return None
        search_root = search_root.rsplit("/node_modules/", 1)[0]


def _find_platform_optional_dependency_gaps(
    packages: dict[str, dict[str, object]],
) -> dict[str, list[str]]:
    missing: dict[str, list[str]] = {}

    for package_path, package in packages.items():
        optional_dependencies = package.get("optionalDependencies")
        if not isinstance(optional_dependencies, dict) or not optional_dependencies:
            continue

        locked_dependencies = {
            dependency: _resolve_locked_dependency(packages, package_path, dependency)
            for dependency in optional_dependencies
        }
        has_platform_matrix = any(
            locked is not None and (locked.get("os") or locked.get("cpu"))
            for locked in locked_dependencies.values()
        )
        if not has_platform_matrix:
            continue

        missing_dependencies = sorted(
            dependency for dependency, locked in locked_dependencies.items() if locked is None
        )
        if missing_dependencies:
            missing[package_path] = missing_dependencies

    return missing


def test_platform_optional_dependency_gaps_are_detected_by_structure() -> None:
    packages: dict[str, dict[str, object]] = {
        "node_modules/native-parent": {
            "optionalDependencies": {
                "native-linux-x64": "1.0.0",
                "native-win32-x64": "1.0.0",
            }
        },
        "node_modules/native-win32-x64": {
            "cpu": ["x64"],
            "optional": True,
            "os": ["win32"],
        },
    }

    assert _find_platform_optional_dependency_gaps(packages) == {
        "node_modules/native-parent": ["native-linux-x64"]
    }


def test_manifest_extension_lockfiles_keep_all_platform_optional_dependencies() -> None:
    manifest = json.loads((EXTENSIONS_ROOT / "manifest.json").read_text(encoding="utf-8"))
    missing: dict[str, dict[str, list[str]]] = {}

    for extension in manifest["extensions"]:
        lockfile = json.loads(
            (EXTENSIONS_ROOT / extension["name"] / "package-lock.json").read_text(encoding="utf-8")
        )
        packages = lockfile["packages"]

        gaps = _find_platform_optional_dependency_gaps(packages)
        if gaps:
            missing[extension["name"]] = gaps

    assert missing == {}, (
        f"extension lockfiles contain platform-pruned optional dependencies: {missing}"
    )
