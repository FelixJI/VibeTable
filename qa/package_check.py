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
from qa.provider_policy_check import check as check_provider_policy
from qa.release_candidate import CandidateError, candidate_evidence
from scripts.build_next import (
    AGE_VERSION,
    KOPIA_VERSION,
    BuildError,
    RepoPaths,
    go_binary_metadata,
    load_recovery_tool_lock,
    render_manifest,
    resolve_go,
    sha256_file,
)
from scripts.versioning import (
    check_versions,
    collect_release_versions,
    validate_workspace_version_policy_document,
)

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
    errors.extend(check_provider_policy(root))
    required = (
        root / "backend" / "_version.py",
        root / "CHANGELOG.md",
        root / "pyproject.toml",
        root / "sidecar" / "go.mod",
        root / "sidecar" / "go.sum",
        root / "tools" / "recovery-tools" / "go.mod",
        root / "tools" / "recovery-tools" / "go.sum",
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
    # Changelog freshness is intentionally not enforced here: release-please
    # regenerates CHANGELOG.md and the generated JSON via `scripts/changelog.py
    # --write` when it opens the Release PR. Forcing per-PR freshness would
    # block every non-chore commit until that release step runs.
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


def _pe_machine(path: Path) -> int | None:
    try:
        with path.open("rb") as stream:
            if stream.read(2) != b"MZ":
                return None
            stream.seek(0x3C)
            offset = int.from_bytes(stream.read(4), "little")
            stream.seek(offset)
            if stream.read(4) != b"PE\0\0":
                return None
            return int.from_bytes(stream.read(2), "little")
    except OSError:
        return None


def check_packaged_provider_support(
    provider_path: Path,
    source_root: Path,
) -> list[str]:
    errors: list[str] = []
    try:
        provider_support = json.loads(provider_path.read_text(encoding="utf-8"))
        providers = provider_support["providers"]
        if provider_support.get("contractVersion") != "2.0":
            errors.append("provider support contract version is invalid")
        if providers["fixed"].get("creation") != "enabled":
            errors.append("fixed provider must remain enabled")
    except (KeyError, OSError, json.JSONDecodeError, TypeError):
        return ["provider support matrix is invalid"]
    errors.extend(
        check_provider_policy(
            source_root,
            support_path=provider_path,
        )
    )
    return errors


def _release_artifact_hashes(package_root: Path, package_archive: Path) -> dict[str, str]:
    evidence = candidate_evidence(package_root, package_archive)
    archive = evidence["archive"]
    if not isinstance(archive, dict):
        raise CandidateError("release candidate archive evidence is invalid")
    archive_name = archive.get("name")
    archive_hash = archive.get("sha256")
    package_tree_hash = evidence.get("packageTreeSha256")
    if (
        not isinstance(archive_name, str)
        or not isinstance(archive_hash, str)
        or not isinstance(package_tree_hash, str)
    ):
        raise CandidateError("release candidate hashes are invalid")
    return {
        "packageTree": package_tree_hash,
        archive_name: archive_hash,
    }


def check_package(
    package_root: Path,
    source_root: Path = PROJECT_ROOT,
    *,
    package_archive: Path | None = None,
) -> list[str]:
    root = package_root.resolve()
    errors: list[str] = []
    if package_archive is not None:
        try:
            _release_artifact_hashes(root, package_archive)
        except CandidateError as exc:
            errors.append(f"release candidate identity is invalid: {exc}")
    root_entries = {path.name: path for path in root.iterdir()}
    allowed_root_entries = {"VibeTable.Next.exe", "release.json", "resources"}
    errors.extend(
        f"unexpected package-root entry: {name}"
        for name in sorted(root_entries.keys() - allowed_root_entries)
    )
    release_path = root / "release.json"
    try:
        release = json.loads(release_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        errors.append(f"invalid release.json: {exc}")
        release = {}
    expected = collect_release_versions(source_root)
    expected_release = {
        "product": "VibeTable",
        "version": expected.app,
        "platform": "windows",
        "architecture": "x64",
    }
    if release != expected_release:
        errors.append("release.json does not match the package identity")

    layout_path = root / "resources" / "publish-layout.json"
    if not layout_path.is_file():
        return [*errors, "missing resources/publish-layout.json"]
    try:
        layout = json.loads(layout_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        return [f"invalid publish-layout.json: {exc}"]
    if layout.get("protocolVersion") != "2.0":
        errors.append("package protocol version is not workspace v2")
    expected_formats = {
        "workspace": 2,
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
        "preview host": launch.get("previewHost"),
        "migration manifest": assets.get("migrations"),
        "build info": assets.get("buildInfo"),
        "sidecar checksum": assets.get("sidecarChecksum"),
        "licenses": assets.get("licenses"),
        "SBOM": assets.get("sbom"),
        "recovery tool provenance": assets.get("recoveryToolProvenance"),
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
    recovery_checksums = assets.get("recoveryToolChecksums", {})
    if not isinstance(recovery_checksums, dict):
        errors.append("missing recoveryToolChecksums map")
        recovery_checksums = {}
    relative.update(
        {
            "Kopia recovery tool checksum": recovery_checksums.get("kopia"),
            "age recovery tool checksum": recovery_checksums.get("age"),
            "age-keygen recovery tool checksum": recovery_checksums.get("ageKeygen"),
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
                "product-contracts.schema.json",
                "fixtures/rpc-catalog.json",
                "fixtures/product-rpc-catalog.json",
                "negative-fixtures.json",
                "provider-support.json",
                "compatibility-corpus.json",
                "workspace-version-policy.json",
                "workspace-version-policy.schema.json",
                "legacy-surface.json",
            ):
                if not (contracts_root / required_contract).is_file():
                    errors.append(f"missing workspace contract asset: {required_contract}")
            provider_path = contracts_root / "provider-support.json"
            if provider_path.is_file():
                errors.extend(
                    check_packaged_provider_support(
                        provider_path,
                        source_root,
                    )
                )
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
            policy_path = contracts_root / "workspace-version-policy.json"
            policy_schema_path = contracts_root / "workspace-version-policy.schema.json"
            if policy_path.is_file() and policy_schema_path.is_file() and corpus_path.is_file():
                try:
                    policy = json.loads(policy_path.read_text(encoding="utf-8"))
                    policy_schema = json.loads(policy_schema_path.read_text(encoding="utf-8"))
                    corpus = json.loads(corpus_path.read_text(encoding="utf-8"))
                    source_contracts_root = source_root / "contracts" / "v2"
                    source_policy = json.loads(
                        (source_contracts_root / "workspace-version-policy.json").read_text(
                            encoding="utf-8"
                        )
                    )
                    source_corpus = json.loads(
                        (source_contracts_root / "compatibility-corpus.json").read_text(
                            encoding="utf-8"
                        )
                    )
                    source_schema = json.loads(
                        (source_contracts_root / "workspace-version-policy.schema.json").read_text(
                            encoding="utf-8"
                        )
                    )
                    if policy_schema != source_schema:
                        errors.append(
                            "packaged workspace version policy schema differs from source"
                        )
                    if not all(
                        isinstance(value, dict)
                        for value in (
                            policy,
                            policy_schema,
                            corpus,
                            source_policy,
                            source_corpus,
                        )
                    ):
                        raise TypeError
                    if policy != source_policy:
                        errors.append("packaged workspace version policy does not match source")
                    if corpus != source_corpus:
                        errors.append(
                            "packaged workspace compatibility corpus does not match source"
                        )
                    current_writer = policy.get("currentWriter")
                    packaged_writer_version = (
                        current_writer.get("appVersion")
                        if isinstance(current_writer, dict)
                        else None
                    )
                    release_version = release.get("version") if isinstance(release, dict) else None
                    if (
                        packaged_writer_version != expected.app
                        or packaged_writer_version != release_version
                    ):
                        errors.append(
                            "packaged workspace version policy does not match release identity"
                        )
                    policy_errors = validate_workspace_version_policy_document(
                        policy,
                        policy_schema,
                        corpus,
                        contracts_root,
                    )
                    if any("violates its closed schema" in error for error in policy_errors):
                        errors.append(
                            "packaged workspace version policy violates its closed schema"
                        )
                    errors.extend(
                        f"packaged workspace version policy is invalid: {error}"
                        for error in policy_errors
                        if "violates its closed schema" not in error
                    )
                except (OSError, json.JSONDecodeError, TypeError):
                    errors.append("packaged workspace version policy is invalid")
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
    lock = load_recovery_tool_lock(source_root)
    expected_tools = {
        "Kopia recovery tool": (
            "Kopia recovery tool checksum",
            "kopia.exe",
            "github.com/kopia/kopia",
            "github.com/kopia/kopia",
            lock.kopia_version,
            lock.kopia_sum,
        ),
        "age recovery tool": (
            "age recovery tool checksum",
            "age.exe",
            "filippo.io/age/cmd/age",
            "filippo.io/age",
            lock.age_version,
            lock.age_sum,
        ),
        "age-keygen recovery tool": (
            "age-keygen recovery tool checksum",
            "age-keygen.exe",
            "filippo.io/age/cmd/age-keygen",
            "filippo.io/age",
            lock.age_version,
            lock.age_sum,
        ),
    }
    provenance_tools: dict[str, dict[str, object]] = {}
    provenance_path = resolved.get("recovery tool provenance")
    if provenance_path is not None:
        try:
            provenance = json.loads(provenance_path.read_text(encoding="utf-8"))
            provenance_tools = {
                str(item.get("name")): item
                for item in provenance.get("tools", [])
                if isinstance(item, dict)
            }
        except (OSError, json.JSONDecodeError, AttributeError):
            errors.append("recovery tool provenance is invalid")
    go = resolve_go(source_root)
    for label, expected_tool in expected_tools.items():
        tool = resolved.get(label)
        if tool is None:
            continue
        checksum_label, name, package, module, version, module_sum = expected_tool
        checksum_path = resolved.get(checksum_label)
        digest = sha256_file(tool)
        if (
            checksum_path is not None
            and checksum_path.read_text(encoding="utf-8").strip() != digest
        ):
            errors.append(f"{label} SHA-256 mismatch")
        if _pe_machine(tool) != 0x8664:
            errors.append(f"{label} is not a Windows amd64 PE executable")
        provenance_item = provenance_tools.get(name)
        expected_provenance = {
            "path": f"resources/sidecar/tools/{name}",
            "package": package,
            "module": module,
            "version": version,
            "moduleSum": module_sum,
            "sha256": digest,
            "goVersion": f"go{lock.go_version}",
            "target": {"goos": "windows", "goarch": "amd64", "cgoEnabled": False},
        }
        if provenance_item is None:
            errors.append(f"recovery tool provenance is missing: {name}")
        else:
            for key, value in expected_provenance.items():
                if provenance_item.get(key) != value:
                    errors.append(f"recovery tool provenance mismatch: {name}.{key}")
        try:
            metadata = go_binary_metadata(go, tool)
        except BuildError as exc:
            errors.append(f"{label} Go build metadata is unreadable: {exc}")
        else:
            expected_metadata = {
                "package": package,
                "module": module,
                "version": version,
                "moduleSum": module_sum,
                "goVersion": f"go{lock.go_version}",
            }
            for key, value in expected_metadata.items():
                if metadata.get(key) != value:
                    errors.append(f"{label} Go build metadata mismatch: {key}")
            if metadata.get("build") != {
                **metadata.get("build", {}),
                "GOOS": "windows",
                "GOARCH": "amd64",
                "CGO_ENABLED": "0",
            }:
                errors.append(f"{label} Go build target mismatch")
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
            "workspaceFormat": "2",
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
        components_by_name = {
            str(item.get("name")): item for item in components if isinstance(item, dict)
        }
        for label, expected_tool in expected_tools.items():
            tool = resolved.get(label)
            if tool is None:
                continue
            _, name, package, module, version, module_sum = expected_tool
            component = components_by_name.get(name)
            if component is None or component.get("type") != "application":
                errors.append(f"SBOM is missing recovery tool artifact: {name}")
                continue
            hashes = {
                item.get("alg"): item.get("content")
                for item in component.get("hashes", [])
                if isinstance(item, dict)
            }
            properties = {
                item.get("name"): item.get("value")
                for item in component.get("properties", [])
                if isinstance(item, dict)
            }
            expected_properties = {
                "vibetable:package": package,
                "vibetable:module": module,
                "vibetable:moduleSum": module_sum,
                "vibetable:goVersion": f"go{lock.go_version}",
                "vibetable:goos": "windows",
                "vibetable:goarch": "amd64",
                "vibetable:cgoEnabled": "false",
                "vibetable:licenseModule": module,
            }
            if (
                component.get("version") != version
                or hashes.get("SHA-256") != sha256_file(tool)
                or any(properties.get(key) != value for key, value in expected_properties.items())
            ):
                errors.append(f"SBOM recovery tool artifact mismatch: {name}")
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
                properties = {
                    entry.get("name"): entry.get("value")
                    for entry in item.get("properties", [])
                    if isinstance(entry, dict)
                }
                license_name = properties.get("vibetable:licenseModule", name)
                if isinstance(license_name, str) and f"===== {license_name} " not in license_text:
                    errors.append(f"third-party license bundle is missing module: {license_name}")
    if (root / "data").exists() or (root / "pb_data").exists():
        errors.append("mutable user data must not be stored in the install directory")
    return errors


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("package_root", nargs="?", type=Path)
    parser.add_argument("--package-archive", type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        errors = (
            check_package(args.package_root, package_archive=args.package_archive)
            if args.package_root is not None
            else check_source()
        )
    except (BuildError, CandidateError, OSError, json.JSONDecodeError) as exc:
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
