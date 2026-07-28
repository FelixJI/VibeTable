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

from qa.legacy_surface_check import check as check_legacy_surface
from qa.provider_evidence_check import check as check_provider_evidence
from scripts.build_next import (
    AGE_VERSION,
    KOPIA_VERSION,
    BuildError,
    RepoPaths,
    render_manifest,
    sha256_file,
)
from scripts.changelog import check_changelog
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
    errors.extend(check_legacy_surface(root))
    errors.extend(check_provider_evidence(root))
    required = (
        root / "backend" / "_version.py",
        root / "CHANGELOG.md",
        root / "pyproject.toml",
        root / "sidecar" / "go.mod",
        root / "sidecar" / "go.sum",
        root / "sidecar" / "migrations" / "manifest.json",
        root / "sidecar" / "internal" / "buildinfo" / "info.go",
        root / "desktop" / "publish-layout.json",
        root / "desktop" / "web-grid" / "src" / "generated" / "changelog.json",
    )
    errors.extend(
        f"missing package input: {path.relative_to(root)}"
        for path in required
        if not path.is_file()
    )
    errors.extend(check_changelog(root, collect_release_versions(root).app))
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
    expected = collect_release_versions(source_root)
    if layout.get("protocolVersion") != "2.0":
        errors.append("package protocol version is not workspace v2")
    expected_formats = {
        "workspace": 1,
        "repository": "kopia-v3",
        "snapshot": 2,
        "package": 2,
        "contracts": "2.0",
    }
    if layout.get("formats") != expected_formats:
        errors.append("package format versions do not match workspace v2")

    encoded = json.dumps(layout).lower()
    errors.extend(
        f"package layout contains forbidden runtime asset: {name}"
        for name in FORBIDDEN_NAMES
        if name in encoded
    )
    for path in root.rglob("*"):
        if path.name.lower() in FORBIDDEN_NAMES:
            errors.append(f"forbidden packaged runtime: {path.relative_to(root)}")
        if path.name.casefold() in {"__pycache__", ".pytest_cache"} or path.suffix.casefold() in {
            ".pyc",
            ".pyo",
        }:
            errors.append(f"packaged build cache is forbidden: {path.relative_to(root)}")

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
        "recovery guide": assets.get("recoveryGuide"),
    }
    recovery_tools = assets.get("recoveryTools", {})
    if not isinstance(recovery_tools, dict):
        errors.append("missing recoveryTools map")
        recovery_tools = {}
    relative.update(
        {
            "Kopia recovery tool": recovery_tools.get("kopia"),
            "age recovery tool": recovery_tools.get("age"),
            "age-keygen recovery tool": recovery_tools.get("ageKeygen"),
        }
    )
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
    contracts_value = assets.get("workspaceContracts")
    if not isinstance(contracts_value, str) or not contracts_value:
        errors.append("missing workspace contracts path")
    else:
        contracts_root = (root / contracts_value).resolve()
        if not _inside(root, contracts_root):
            errors.append("workspace contracts path escapes package root")
        elif not contracts_root.is_dir():
            errors.append(f"missing workspace contracts directory: {contracts_value}")
        else:
            for required_contract in (
                "contracts.schema.json",
                "fixtures/rpc-catalog.json",
                "v1-frozen.sha256",
                "negative-fixtures.json",
                "provider-support.json",
                "provider-lab-evidence.schema.json",
                "compatibility-corpus.json",
                "legacy-surface.json",
            ):
                if not (contracts_root / required_contract).is_file():
                    errors.append(f"missing workspace contract asset: {required_contract}")
            provider_path = contracts_root / "provider-support.json"
            if provider_path.is_file():
                try:
                    provider_support = json.loads(provider_path.read_text(encoding="utf-8"))
                    providers = provider_support["providers"]
                    if provider_support.get("contractVersion") != "2.0":
                        errors.append("provider support contract version is invalid")
                    if providers["fixed"].get("creation") != "enabled":
                        errors.append("fixed provider must remain enabled")
                    for name in (
                        "network",
                        "registeredCloud",
                        "userMarkedSync",
                        "removable",
                    ):
                        if providers[name].get("creation") != "blockedPendingLab":
                            errors.append(f"unverified provider is not release-blocked: {name}")
                except (KeyError, OSError, json.JSONDecodeError, TypeError):
                    errors.append("provider support matrix is invalid")
            corpus_path = contracts_root / "compatibility-corpus.json"
            if corpus_path.is_file():
                try:
                    corpus = json.loads(corpus_path.read_text(encoding="utf-8"))
                    if corpus.get("appendOnly") is not True:
                        errors.append("workspace compatibility corpus is not append-only")
                    for baseline in corpus["baselines"]:
                        for artifact in baseline["artifacts"]:
                            relative_artifact = Path(artifact["path"])
                            candidate = (contracts_root / relative_artifact).resolve()
                            if (
                                relative_artifact.is_absolute()
                                or not _inside(contracts_root, candidate)
                                or not candidate.is_file()
                                or sha256_file(candidate) != artifact["sha256"]
                            ):
                                errors.append(
                                    "workspace compatibility corpus artifact is missing "
                                    f"or changed: {artifact.get('path')}"
                                )
                except (KeyError, OSError, json.JSONDecodeError, TypeError):
                    errors.append("workspace compatibility corpus is invalid")
    if "sidecar binary" not in resolved:
        return errors
    tool_labels = (
        "Kopia recovery tool",
        "age recovery tool",
        "age-keygen recovery tool",
    )
    tool_bytes = sum(resolved[label].stat().st_size for label in tool_labels if label in resolved)
    if tool_bytes > 220 * 1024 * 1024:
        errors.append("bundled Kopia/age recovery tools exceed the size threshold")
    for label in tool_labels:
        tool = resolved.get(label)
        if tool is None:
            continue
        checksum_path = tool.with_suffix(tool.suffix + ".sha256")
        if not checksum_path.is_file():
            errors.append(f"missing {label} checksum: {checksum_path.name}")
        elif checksum_path.read_text(encoding="utf-8").strip() != sha256_file(tool):
            errors.append(f"{label} SHA-256 mismatch")
    recovery_guide = resolved.get("recovery guide")
    if recovery_guide is not None:
        recovery_text = recovery_guide.read_text(encoding="utf-8", errors="replace")
        for required_term in (
            "Workspace Center",
            ".vtsnapshot",
            "age.exe",
            "kopia.exe",
            "password",
            "AuditLedger",
        ):
            if required_term not in recovery_text:
                errors.append(f"recovery guide is missing required guidance: {required_term}")

    binary = resolved["sidecar binary"]
    checksum = resolved.get("sidecar checksum")
    digest = sha256_file(binary)
    if checksum and checksum.read_text(encoding="utf-8").strip() != digest:
        errors.append("sidecar SHA-256 file mismatch")
    sidecar_meta = layout.get("components", {}).get("sidecar", {})
    if sidecar_meta.get("sha256") != digest:
        errors.append("sidecar manifest SHA-256 mismatch")
    expected_sidecar_meta = {
        "version": expected.app,
        "pocketBaseVersion": expected.pocketbase,
        "celVersion": expected.cel,
        "contractVersion": "2.0",
        "schemaVersion": expected.schema,
        "migrationHash": expected.migration_hash,
    }
    errors.extend(
        f"sidecar manifest mismatch: {key}"
        for key, value in expected_sidecar_meta.items()
        if sidecar_meta.get(key) != value
    )
    if os.name != "nt" and not os.access(binary, os.X_OK):
        errors.append("sidecar binary is not executable")

    if "build info" in resolved:
        info = json.loads(resolved["build info"].read_text(encoding="utf-8"))
        expected_info = {
            "version": expected.app,
            "pocketBaseVersion": expected.pocketbase,
            "celVersion": expected.cel,
            "contractVersion": expected.contract,
            "schemaVersion": expected.schema,
            "migrationHash": expected.migration_hash,
            "protocolV2Version": "2.0",
            "workspaceFormat": "1",
            "repositoryFormat": "kopia-v3",
            "snapshotFormat": "2",
            "packageFormat": "2",
            "kopiaVersion": KOPIA_VERSION,
            "ageVersion": AGE_VERSION,
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
        component_versions = {
            item.get("name"): item.get("version") for item in components if isinstance(item, dict)
        }
        required_modules = {
            "github.com/pocketbase/pocketbase": f"v{expected.pocketbase}",
            "github.com/google/cel-go": f"v{expected.cel}",
            "github.com/kopia/kopia": KOPIA_VERSION,
            "filippo.io/age": AGE_VERSION,
        }
        for name, version in required_modules.items():
            actual_version = component_versions.get(name)
            if actual_version is None:
                errors.append(f"SBOM is missing pinned dependency: {name}")
            elif actual_version != version:
                errors.append(
                    f"SBOM dependency version mismatch: {name} "
                    f"(expected {version}, got {actual_version})"
                )
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
