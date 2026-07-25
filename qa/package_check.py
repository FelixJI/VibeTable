#!/usr/bin/env python3
"""Validate source inputs or a staged VibeTable offline package."""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[1]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from scripts.build_next import BuildError, RepoPaths, render_manifest, sha256_file
from scripts.versioning import check_versions, collect_release_versions

FORBIDDEN_LEGACY_PROVIDER = "".join(["di", "rectus"])
FORBIDDEN_NAMES = frozenset(
    {
        "local-" + FORBIDDEN_LEGACY_PROVIDER,
        "node_modules",
        FORBIDDEN_LEGACY_PROVIDER,
        "npm",
        "npm.cmd",
    }
)


def _inside(root: Path, candidate: Path) -> bool:
    try:
        candidate.resolve().relative_to(root.resolve())
        return True
    except ValueError:
        return False


def check_source(root: Path = PROJECT_ROOT) -> list[str]:
    errors = check_versions(root)
    required = (
        root / "pyproject.toml",
        root / "sidecar" / "go.mod",
        root / "sidecar" / "go.sum",
        root / "sidecar" / "migrations" / "manifest.json",
        root / "sidecar" / "internal" / "buildinfo" / "info.go",
        root / "desktop" / "publish-layout.json",
    )
    errors.extend(
        f"missing package input: {path.relative_to(root)}"
        for path in required
        if not path.is_file()
    )
    layout_path = root / "desktop" / "publish-layout.json"
    if layout_path.is_file():
        committed = json.loads(layout_path.read_text(encoding="utf-8"))
        generated = json.loads(
            render_manifest(
                RepoPaths.default(root),
                sidecar_sha256="0" * 64,
            )
        )
        if committed != generated:
            errors.append("desktop/publish-layout.json differs from generated layout")
        encoded = json.dumps(committed).lower()
        for forbidden in FORBIDDEN_NAMES:
            if forbidden in encoded:
                errors.append(f"layout contains forbidden runtime asset: {forbidden}")
    return errors


def check_package(package_root: Path, source_root: Path = PROJECT_ROOT) -> list[str]:
    root = package_root.resolve()
    errors: list[str] = []
    layout_path = root / "publish-layout.json"
    if not layout_path.is_file():
        return ["missing publish-layout.json"]
    try:
        layout = json.loads(layout_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        return [f"invalid publish-layout.json: {exc}"]

    encoded = json.dumps(layout).lower()
    errors.extend(
        f"package layout contains forbidden runtime asset: {name}"
        for name in FORBIDDEN_NAMES
        if name in encoded
    )
    for path in root.rglob("*"):
        if path.name.lower() in FORBIDDEN_NAMES:
            errors.append(f"forbidden packaged runtime: {path.relative_to(root)}")

    launch = layout.get("launch", {})
    assets = layout.get("assets", {})
    relative = {
        "host executable": launch.get("host"),
        "backend executable": launch.get("backend"),
        "sidecar binary": launch.get("sidecar"),
        "migration manifest": assets.get("migrations"),
        "build info": assets.get("buildInfo"),
        "sidecar checksum": assets.get("sidecarChecksum"),
        "licenses": assets.get("licenses"),
        "SBOM": assets.get("sbom"),
    }
    resolved: dict[str, Path] = {}
    for label, value in relative.items():
        if not isinstance(value, str) or not value:
            errors.append(f"missing {label} path")
            continue
        candidate = (root / value).resolve()
        if not _inside(root, candidate):
            errors.append(f"{label} escapes package root")
        elif not candidate.is_file():
            errors.append(f"missing {label}: {value}")
        else:
            resolved[label] = candidate
    web_grid_value = launch.get("webGrid")
    if not isinstance(web_grid_value, str) or not web_grid_value:
        errors.append("missing web grid path")
    else:
        web_grid = (root / web_grid_value).resolve()
        if not _inside(root, web_grid):
            errors.append("web grid escapes package root")
        elif not web_grid.is_dir():
            errors.append(f"missing web grid directory: {web_grid_value}")
        elif not (web_grid / "index.html").is_file():
            errors.append(f"web grid entrypoint is missing: {web_grid_value}/index.html")
    if "sidecar binary" not in resolved:
        return errors

    binary = resolved["sidecar binary"]
    checksum = resolved.get("sidecar checksum")
    digest = sha256_file(binary)
    if checksum and checksum.read_text(encoding="utf-8").strip() != digest:
        errors.append("sidecar SHA-256 file mismatch")
    sidecar_meta = layout.get("components", {}).get("sidecar", {})
    if sidecar_meta.get("sha256") != digest:
        errors.append("sidecar manifest SHA-256 mismatch")
    if os.name != "nt" and not os.access(binary, os.X_OK):
        errors.append("sidecar binary is not executable")

    expected = collect_release_versions(source_root)
    if "build info" in resolved:
        info = json.loads(resolved["build info"].read_text(encoding="utf-8"))
        expected_info = {
            "version": expected.app,
            "pocketBaseVersion": expected.pocketbase,
            "celVersion": expected.cel,
            "contractVersion": expected.contract,
            "schemaVersion": expected.schema,
            "migrationHash": expected.migration_hash,
        }
        errors.extend(
            f"build info mismatch: {key}"
            for key, value in expected_info.items()
            if info.get(key) != value
        )
    if "migration manifest" in resolved:
        migration_digest = sha256_file(resolved["migration manifest"])
        if migration_digest != expected.migration_hash:
            errors.append("migration manifest hash mismatch")
    if "SBOM" in resolved:
        sbom = json.loads(resolved["SBOM"].read_text(encoding="utf-8"))
        components = sbom.get("components", [])
        names = {item.get("name") for item in components}
        required_modules = {
            "github.com/pocketbase/pocketbase",
            "github.com/google/cel-go",
        }
        if not required_modules.issubset(names):
            errors.append("SBOM is missing pinned PocketBase/CEL dependencies")
        for item in components:
            licenses = item.get("licenses")
            ids = (
                [
                    license_entry.get("license", {}).get("id")
                    for license_entry in licenses
                    if isinstance(license_entry, dict)
                ]
                if isinstance(licenses, list)
                else []
            )
            if not ids or any(
                not isinstance(value, str) or not value or value == "UNKNOWN" for value in ids
            ):
                errors.append(f"SBOM component has no resolved license: {item.get('name')}")
    if "licenses" in resolved:
        license_text = resolved["licenses"].read_text(encoding="utf-8", errors="replace")
        if "UNKNOWN" in license_text or "===== " not in license_text:
            errors.append("third-party license bundle is incomplete")
        if "SBOM" in resolved:
            for item in sbom.get("components", []):
                name = item.get("name")
                if isinstance(name, str) and f"===== {name} " not in license_text:
                    errors.append(f"third-party license bundle is missing module: {name}")
    if (root / "data").exists() or (root / "pb_data").exists():
        errors.append("mutable user data must not be stored in the install directory")
    return errors


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("package_root", nargs="?", type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        errors = (
            check_package(args.package_root) if args.package_root is not None else check_source()
        )
    except (BuildError, OSError, json.JSONDecodeError) as exc:
        errors = [str(exc)]
    if errors:
        print("[FAIL] package contract:", file=sys.stderr)
        for error in errors:
            print(f"  - {error}", file=sys.stderr)
        return 1
    print("[OK] package contract")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
