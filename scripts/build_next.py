#!/usr/bin/env python3
"""Build an offline VibeTable release with the pinned PocketBase sidecar."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import secrets
import shutil
import stat
import subprocess
import sys
import time
from collections.abc import Callable
from dataclasses import dataclass, field, replace
from datetime import datetime
from pathlib import Path, PureWindowsPath
from typing import TYPE_CHECKING, Any, cast

try:
    from scripts._host_paths import host_assembly_name
    from scripts.toolchain_metadata import resolve_executable as resolve_toolchain_executable
    from scripts.versioning import (
        check_versions,
        collect_release_versions,
        read_project_version,
    )
except ModuleNotFoundError:  # pragma: no cover - direct script execution
    from _host_paths import host_assembly_name
    from toolchain_metadata import resolve_executable as resolve_toolchain_executable
    from versioning import (
        check_versions,
        collect_release_versions,
        read_project_version,
    )


if TYPE_CHECKING:
    from scripts.qa.windows_process_scope import WindowsProcessScope

PROTOCOL_VERSION = "2.0"
WEBVIEW2_SDK = "1.0.4129.50"
TABULATOR_VERSION = "6.5.2"
HOST_EXE_NAME = "VibeTable.Next.exe"
ARCHIVE_ROOT_NAME = "VibeTable"
RELEASE_PLATFORM = "win-x64"
BACKEND_EXE_NAME = "vibetable-backend.exe"
# The published product is a Windows desktop package even when source-contract
# checks run on Linux. Keep the manifest target-platform deterministic instead
# of letting the CI host silently rewrite package paths.
SIDECAR_EXE_NAME = "vibetable-pb.exe"
KOPIA_EXE_NAME = "kopia.exe"
AGE_EXE_NAME = "age.exe"
AGE_KEYGEN_EXE_NAME = "age-keygen.exe"
RECOVERY_TOOLS_RELATIVE_ROOT = Path("tools") / "recovery-tools"
RECOVERY_PROVENANCE_NAME = "recovery-tools.provenance.json"
RECOVERY_TOOL_SIZE_LIMIT = 220 * 1024 * 1024
CONTRACT_COPY_IGNORES = ("__pycache__", ".pytest_cache", "*.pyc", "*.pyo")
BACKEND_HIDDEN_IMPORTS = (
    "pydantic",
    "pydantic.deprecated.decorator",
    "openpyxl",
    "openpyxl.workbook",
)
_DEV_PACKAGES_FORBIDDEN_IN_BUNDLE = frozenset({"mypy", "numpy", "pandas", "pytest", "_pytest"})
DEV_SKIP_FLAGS = (
    "--skip-web",
    "--skip-backend",
    "--skip-desktop",
    "--skip-sidecar",
)
SELF_UPDATE_SMOKE_TOKEN_ENV = "VIBETABLE_SELF_UPDATE_SMOKE_TOKEN"
SELF_UPDATE_SMOKE_COMPLETION_FILE = ".self-update-smoke-complete"
SELF_UPDATE_SMOKE_PROCESS_FILE = ".self-update-smoke-process.json"
SELF_UPDATE_SMOKE_READINESS_DIR = "self-update-readiness"
SELF_UPDATE_UPDATED_CONTROLS_DIR = "self-update-updated-controls"
SELF_UPDATE_RESTORED_READINESS_DIR = "self-update-restored-readiness"
SELF_UPDATE_RESTORED_CONTROLS_DIR = "self-update-restored-controls"
SHELL_READINESS_FILE = "vibetable-readiness.json"
PENDING_UPDATE_ACTIVATION_POINTER = ".VibeTable.Next.update-pending.json"
SELF_UPDATE_ROLLBACK_RECEIPT_GLOB = ".VibeTable.Next.update-rollback-*.json"
_SELF_UPDATE_CLOSE_DIAGNOSTIC_ITEM_LIMIT = 8
_SELF_UPDATE_CLOSE_DIAGNOSTIC_ERROR_LIMIT = 4
_SELF_UPDATE_CLOSE_DIAGNOSTIC_TEXT_LIMIT = 160
_SELF_UPDATE_ACTIVATION_POINTER_V1_FIELDS = frozenset(
    {
        "schemaVersion",
        "state",
        "targetRoot",
        "stagingRoot",
        "currentVersion",
        "targetVersion",
        "token",
        "smokeTest",
        "updaterProcessId",
        "updaterStartedAtUtc",
        "createdAtUtc",
        "confirmedAt",
    }
)
_SELF_UPDATE_ACTIVATION_POINTER_V2_FIELDS = _SELF_UPDATE_ACTIVATION_POINTER_V1_FIELDS | {
    "watchdogProcessId",
    "watchdogStartedAtUtc",
    "ownedGroupId",
    "launchNonce",
    "updatedProcessId",
    "updatedStartedAtUtc",
    "failureCode",
    "rollbackRequestedAtUtc",
    "ownedGroupQuiescedAtUtc",
    "workerLaunchNonce",
    "workerProcessId",
    "workerStartedAtUtc",
    "workerReplacementCount",
    "ownedEntryLedger",
    "rollbackAttempt",
    "rollbackErrorCode",
    "rolledBackAtUtc",
}
_SELF_UPDATE_ACTIVATION_POINTER_V2_PREPARED_NULL_FIELDS = frozenset(
    {
        "watchdogProcessId",
        "watchdogStartedAtUtc",
        "ownedGroupId",
        "launchNonce",
        "updatedProcessId",
        "updatedStartedAtUtc",
        "failureCode",
        "rollbackRequestedAtUtc",
        "ownedGroupQuiescedAtUtc",
        "workerLaunchNonce",
        "workerProcessId",
        "workerStartedAtUtc",
        "rollbackAttempt",
        "rollbackErrorCode",
        "rolledBackAtUtc",
    }
)


def release_archive_name(version: str) -> str:
    return f"VibeTable-v{version}-{RELEASE_PLATFORM}.zip"


class BuildError(RuntimeError):
    """A release stage failed before the atomic publish swap."""


@dataclass(frozen=True)
class RecoveryToolLock:
    go_version: str
    kopia_version: str
    kopia_sum: str
    age_version: str
    age_sum: str


def load_recovery_tool_lock(repo_root: Path) -> RecoveryToolLock:
    module_root = repo_root / RECOVERY_TOOLS_RELATIVE_ROOT
    go_mod = (module_root / "go.mod").read_text(encoding="utf-8")
    go_sum = (module_root / "go.sum").read_text(encoding="utf-8")
    go_match = re.search(r"(?m)^go\s+(\S+)\s*$", go_mod)
    requires = dict(
        re.findall(
            r"(?m)^\s*(filippo\.io/age|github\.com/kopia/kopia)\s+(v\S+)\s*$",
            go_mod,
        )
    )
    if go_match is None or set(requires) != {"filippo.io/age", "github.com/kopia/kopia"}:
        raise BuildError("recovery-tools/go.mod is missing the exact Go, Kopia or age lock")

    def module_sum(module: str, version: str) -> str:
        match = re.search(
            rf"(?m)^{re.escape(module)}\s+{re.escape(version)}\s+(h1:\S+)\s*$",
            go_sum,
        )
        if match is None:
            raise BuildError(f"recovery-tools/go.sum is missing {module} {version}")
        return match.group(1)

    kopia_version = requires["github.com/kopia/kopia"]
    age_version = requires["filippo.io/age"]
    return RecoveryToolLock(
        go_version=go_match.group(1),
        kopia_version=kopia_version,
        kopia_sum=module_sum("github.com/kopia/kopia", kopia_version),
        age_version=age_version,
        age_sum=module_sum("filippo.io/age", age_version),
    )


_RECOVERY_TOOL_LOCK = load_recovery_tool_lock(Path(__file__).resolve().parents[1])
KOPIA_VERSION = _RECOVERY_TOOL_LOCK.kopia_version
AGE_VERSION = _RECOVERY_TOOL_LOCK.age_version
RECOVERY_GO_VERSION = _RECOVERY_TOOL_LOCK.go_version


@dataclass
class RepoPaths:
    repo_root: Path
    web_grid_dir: Path
    sidecar_source_dir: Path
    sidecar_migrations_dir: Path
    desktop_csproj: Path
    backend_main: Path
    staging_root: Path
    scratch_root: Path
    publish_root: Path
    resources_dir: Path = field(init=False)
    host_exe: Path = field(init=False)
    backend_dir: Path = field(init=False)
    web_grid_publish_dir: Path = field(init=False)
    sidecar_assets_dir: Path = field(init=False)
    sidecar_binary: Path = field(init=False)
    sidecar_checksum: Path = field(init=False)
    sidecar_build_info: Path = field(init=False)
    sidecar_sbom: Path = field(init=False)
    sidecar_licenses: Path = field(init=False)
    recovery_provenance: Path = field(init=False)
    kopia_binary: Path = field(init=False)
    age_binary: Path = field(init=False)
    age_keygen_binary: Path = field(init=False)
    manifest_path: Path = field(init=False)
    release_manifest: Path = field(init=False)

    def __post_init__(self) -> None:
        self.resources_dir = self.publish_root / "resources"
        self.host_exe = self.publish_root / HOST_EXE_NAME
        self.backend_dir = self.resources_dir / "backend"
        self.web_grid_publish_dir = self.resources_dir / "web-grid"
        self.sidecar_assets_dir = self.resources_dir / "sidecar"
        self.sidecar_binary = self.sidecar_assets_dir / SIDECAR_EXE_NAME
        self.sidecar_checksum = self.sidecar_assets_dir / f"{SIDECAR_EXE_NAME}.sha256"
        self.sidecar_build_info = self.sidecar_assets_dir / "build-info.json"
        self.sidecar_sbom = self.sidecar_assets_dir / "sbom.cdx.json"
        self.sidecar_licenses = self.sidecar_assets_dir / "THIRD_PARTY_LICENSES.txt"
        self.recovery_provenance = self.sidecar_assets_dir / RECOVERY_PROVENANCE_NAME
        self.kopia_binary = self.sidecar_assets_dir / "tools" / KOPIA_EXE_NAME
        self.age_binary = self.sidecar_assets_dir / "tools" / AGE_EXE_NAME
        self.age_keygen_binary = self.sidecar_assets_dir / "tools" / AGE_KEYGEN_EXE_NAME
        self.manifest_path = self.resources_dir / "publish-layout.json"
        self.release_manifest = self.publish_root / "release.json"

    @classmethod
    def default(cls, repo_root: Path) -> RepoPaths:
        root = repo_root.resolve()
        return cls(
            repo_root=root,
            web_grid_dir=root / "desktop" / "web-grid",
            sidecar_source_dir=root / "sidecar",
            sidecar_migrations_dir=root / "sidecar" / "migrations",
            desktop_csproj=(
                root / "desktop" / "src" / "VibeTable.Desktop" / "VibeTable.Desktop.csproj"
            ),
            backend_main=root / "backend" / "__main__.py",
            # A leading-dot staging directory can be built successfully on
            # Windows but `os.replace(staging, publish)` may then be rejected
            # with WinError 5. A regular sibling name preserves the same
            # atomic same-volume swap without that platform-specific trap.
            staging_root=root / "dist" / "VibeTable.Next.staging",
            scratch_root=root / "build" / "next-scratch",
            publish_root=root / "dist" / "VibeTable.Next",
        )

    def staging_mirror(self) -> RepoPaths:
        return replace(self, publish_root=self.staging_root)

    def with_output_roots(
        self,
        *,
        staging_root: Path,
        scratch_root: Path,
        publish_root: Path,
    ) -> RepoPaths:
        return replace(
            self,
            staging_root=staging_root,
            scratch_root=scratch_root,
            publish_root=publish_root,
        )


def _resolve_executable(name: str, *, repo_root: Path | None = None) -> str:
    if os.path.sep in name or (os.path.altsep and os.path.altsep in name):
        return name
    return resolve_toolchain_executable(name, repo_root=repo_root) or name


def resolve_go(repo_root: Path) -> str:
    suffix = "go.exe" if os.name == "nt" else "go"
    candidates = (
        repo_root / ".tools" / f"go-{RECOVERY_GO_VERSION}" / "go" / "bin" / suffix,
        repo_root / ".tools" / "go" / "bin" / suffix,
    )
    local = next((str(path) for path in candidates if path.is_file()), None)
    if local is not None:
        return local
    on_path = shutil.which("go")
    if on_path is not None:
        return on_path
    legacy = repo_root / ".tools" / "go-full" / "go" / "bin" / suffix
    return str(legacy) if legacy.is_file() else "go"


def build_npm_build_command(_paths: RepoPaths) -> list[str]:
    return ["npm", "run", "build"]


def build_dotnet_publish_command(
    paths: RepoPaths, output_dir: str | os.PathLike[str] | None = None
) -> list[str]:
    command = [
        "dotnet",
        "publish",
        str(paths.desktop_csproj),
        "--configuration",
        "Release",
        "--runtime",
        "win-x64",
        "--self-contained",
        "true",
        "-p:PublishSingleFile=true",
        "-p:IncludeNativeLibrariesForSelfExtract=true",
        "-p:EnableCompressionInSingleFile=true",
        "-p:DebugType=None",
        "-p:DebugSymbols=false",
        "-p:SatelliteResourceLanguages=en-US",
    ]
    if output_dir is not None:
        command.extend(["--output", os.fsdecode(Path(output_dir))])
    return command


def build_pyinstaller_backend_command(
    paths: RepoPaths, output_dir: str | os.PathLike[str]
) -> list[str]:
    output = Path(output_dir)
    command = [
        sys.executable,
        "-m",
        "PyInstaller",
        "--noconfirm",
        "--clean",
        "--distpath",
        os.fsdecode(output),
        "--workpath",
        os.fsdecode(output.parent / "_pyinstaller_build"),
        "--specpath",
        os.fsdecode(output.parent / "_pyinstaller_build"),
        "--name",
        BACKEND_EXE_NAME.removesuffix(".exe"),
        "--onedir",
        "--console",
    ]
    for hidden in BACKEND_HIDDEN_IMPORTS:
        command.extend(["--hidden-import", hidden])
    for excluded in sorted(_DEV_PACKAGES_FORBIDDEN_IN_BUNDLE):
        command.extend(["--exclude-module", excluded])
    command.extend(["--collect-data", "pydantic", str(paths.backend_main)])
    return command


def build_sidecar_command(
    paths: RepoPaths,
    *,
    output: Path,
    commit: str,
    build_time: str,
) -> list[str]:
    version = read_project_version(paths.repo_root)
    package = "github.com/vibetable/vibetable/sidecar/internal/buildinfo"
    ldflags = " ".join(
        (
            "-s",
            "-w",
            f"-X {package}.Version={version}",
            f"-X {package}.Commit={commit}",
            f"-X {package}.BuildTime={build_time}",
        )
    )
    return [
        resolve_go(paths.repo_root),
        "build",
        "-trimpath",
        "-buildvcs=true",
        "-ldflags",
        ldflags,
        "-o",
        str(output),
        "./cmd/vibetable-pb",
    ]


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def render_manifest(
    paths: RepoPaths,
    *,
    sidecar_sha256: str | None = None,
) -> str:
    versions = collect_release_versions(paths.repo_root)
    digest = sidecar_sha256
    if digest is None and paths.sidecar_checksum.is_file():
        digest = paths.sidecar_checksum.read_text(encoding="utf-8").strip()
    digest = digest or ("0" * 64)
    data = {
        "protocolVersion": PROTOCOL_VERSION,
        "components": {
            "host": {"version": versions.app},
            "backend": {"version": versions.app},
            "web": {"version": versions.app},
            "sidecar": {
                "version": versions.app,
                "pocketBaseVersion": versions.pocketbase,
                "celVersion": versions.cel,
                "contractVersion": "2.0",
                "schemaVersion": versions.schema,
                "migrationHash": versions.migration_hash,
                "sha256": digest,
            },
        },
        "webview2": {"sdk": WEBVIEW2_SDK},
        "webGrid": {"tabulator": TABULATOR_VERSION},
        "launch": {
            "host": HOST_EXE_NAME,
            "backend": f"resources/backend/{BACKEND_EXE_NAME}",
            "webGrid": "resources/web-grid",
            "sidecar": f"resources/sidecar/{SIDECAR_EXE_NAME}",
            "previewHost": HOST_EXE_NAME,
        },
        "assets": {
            "migrations": "resources/sidecar/migrations/manifest.json",
            "buildInfo": "resources/sidecar/build-info.json",
            "sidecarChecksum": f"resources/sidecar/{SIDECAR_EXE_NAME}.sha256",
            "licenses": "resources/sidecar/THIRD_PARTY_LICENSES.txt",
            "sbom": "resources/sidecar/sbom.cdx.json",
            "recoveryToolProvenance": f"resources/sidecar/{RECOVERY_PROVENANCE_NAME}",
            "recoveryGuide": "resources/RECOVERY.md",
            "workspaceContracts": "resources/contracts/v2",
            "recoveryTools": {
                "kopia": "resources/sidecar/tools/kopia.exe",
                "age": "resources/sidecar/tools/age.exe",
                "ageKeygen": "resources/sidecar/tools/age-keygen.exe",
            },
            "recoveryToolChecksums": {
                "kopia": "resources/sidecar/tools/kopia.exe.sha256",
                "age": "resources/sidecar/tools/age.exe.sha256",
                "ageKeygen": "resources/sidecar/tools/age-keygen.exe.sha256",
            },
        },
        "formats": {
            "workspace": 2,
            "repository": "kopia-v3",
            "snapshot": 2,
            "package": 2,
            "contracts": "2.0",
        },
        "data": {
            "shellRoot": "%LOCALAPPDATA%/VibeTable/shell",
            "managedWorkspaceRoot": ("%LOCALAPPDATA%/VibeTable/shell/workspaces/<workspaceId>"),
            "mirroredActivityRoot": ("%LOCALAPPDATA%/VibeTable/activity/<workspaceId>"),
            "workspaceIdentity": "manifest-uuid",
            "preserveOnUninstall": True,
        },
    }
    return json.dumps(data, ensure_ascii=False, indent=2) + "\n"


def write_manifest(paths: RepoPaths) -> Path:
    paths.manifest_path.parent.mkdir(parents=True, exist_ok=True)
    paths.manifest_path.write_text(render_manifest(paths), encoding="utf-8")
    return paths.manifest_path


def write_source_manifest(paths: RepoPaths) -> Path:
    target = paths.repo_root / "desktop" / "publish-layout.json"
    target.write_text(
        render_manifest(paths, sidecar_sha256="0" * 64),
        encoding="utf-8",
    )
    return target


def render_release_manifest(paths: RepoPaths) -> str:
    return (
        json.dumps(
            {
                "product": "VibeTable",
                "version": read_project_version(paths.repo_root),
                "platform": "windows",
                "architecture": "x64",
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n"
    )


def write_release_manifest(paths: RepoPaths) -> Path:
    paths.release_manifest.parent.mkdir(parents=True, exist_ok=True)
    paths.release_manifest.write_text(
        render_release_manifest(paths),
        encoding="utf-8",
    )
    return paths.release_manifest


_LICENSE_FILE_PREFIXES = ("license", "copying", "notice")


def _license_files(module: dict[str, Any]) -> list[Path]:
    directory = Path(str(module.get("dir", "")))
    if not directory.is_dir():
        return []
    return sorted(
        (
            item
            for item in directory.iterdir()
            if item.is_file() and item.name.casefold().startswith(_LICENSE_FILE_PREFIXES)
        ),
        key=lambda item: item.name.casefold(),
    )


def _classify_licenses(text: str) -> set[str]:
    normalized = " ".join(text.casefold().split())
    detected: set[str] = set()
    if "mozilla public license version 2.0" in normalized:
        detected.add("MPL-2.0")
    if "apache license version 2.0" in normalized:
        detected.add("Apache-2.0")
    if (
        "permission is hereby granted, free of charge" in normalized
        and "software is provided" in normalized
        and "as is" in normalized
    ):
        detected.add("MIT")
    if "redistribution and use in source and binary forms" in normalized:
        if "neither the name" in normalized:
            detected.add("BSD-3-Clause")
        else:
            detected.add("BSD-2-Clause")
    if "isc license" in normalized or (
        "permission to use, copy, modify, and/or distribute" in normalized
        and 'the software is provided "as is"' in normalized
    ):
        detected.add("ISC")
    if "this is free and unencumbered software released into the public domain" in normalized:
        detected.add("Unlicense")
    if "cc0 1.0 universal" in normalized:
        detected.add("CC0-1.0")
    if (
        "zlib license" in normalized
        and "altered source versions must be plainly marked" in normalized
    ):
        detected.add("Zlib")
    return detected


def _module_licenses(module: dict[str, Any]) -> list[str]:
    explicit = module.get("license")
    if isinstance(explicit, str) and explicit and explicit != "UNKNOWN":
        return [explicit]
    detected: set[str] = set()
    for path in _license_files(module):
        detected.update(_classify_licenses(path.read_text(encoding="utf-8", errors="replace")))
    if detected:
        return sorted(detected)
    module_path = str(module.get("path", ""))
    raise BuildError(f"missing or unrecognized license text for Go module: {module_path}")


def _module_license(module: dict[str, Any]) -> str:
    return " AND ".join(_module_licenses(module))


def _third_party_license_text(modules: list[dict[str, Any]]) -> str:
    sections = ["VibeTable sidecar third-party dependency licenses", ""]
    for module in modules:
        module_path = str(module.get("path", ""))
        if not module_path:
            continue
        license_id = _module_license(module)
        files = _license_files(module)
        if not files:
            raise BuildError(f"missing license text for Go module: {module_path}")
        sections.append(f"===== {module_path} {module.get('version', '')} ({license_id}) =====")
        for path in files:
            sections.extend(
                (
                    f"--- {path.name} ---",
                    path.read_text(encoding="utf-8", errors="replace").rstrip(),
                    "",
                )
            )
    return "\n".join(sections).rstrip() + "\n"


def _modules_from_packages(packages: list[dict[str, Any]]) -> list[dict[str, Any]]:
    by_path: dict[str, dict[str, Any]] = {}
    for package in packages:
        module = package.get("Module")
        if not isinstance(module, dict) or module.get("Main") is True:
            continue
        replacement = module.get("Replace")
        if isinstance(replacement, dict):
            module = {**module, **replacement, "Path": module.get("Path")}
        path = module.get("Path")
        if not isinstance(path, str) or not path:
            continue
        by_path[path] = {
            "path": path,
            "version": str(module.get("Version", "")),
            "dir": str(module.get("Dir", "")),
        }
    return [by_path[path] for path in sorted(by_path)]


def go_binary_metadata(go: str, binary: Path) -> dict[str, Any]:
    result = _run(
        [go, "version", "-m", str(binary)],
        cwd=binary.parent,
        capture=True,
    )
    metadata: dict[str, Any] = {"dependencies": {}, "build": {}}
    for raw_line in result.stdout.splitlines()[1:]:
        fields = raw_line.strip().split("\t")
        if not fields:
            continue
        if fields[0] == "path" and len(fields) >= 2:
            metadata["package"] = fields[1]
        elif fields[0] == "mod" and len(fields) >= 4:
            metadata["module"] = fields[1]
            metadata["version"] = fields[2]
            metadata["moduleSum"] = fields[3]
        elif fields[0] == "dep" and len(fields) >= 4:
            metadata["dependencies"][fields[1]] = {
                "version": fields[2],
                "sum": fields[3],
            }
        elif fields[0] == "build" and len(fields) >= 2 and "=" in fields[1]:
            key, value = fields[1].split("=", 1)
            metadata["build"][key] = value
    version_match = re.search(r":\s+(go\S+)\s*$", result.stdout.splitlines()[0])
    metadata["goVersion"] = version_match.group(1) if version_match else ""
    return metadata


def verify_recovery_tool_versions(
    tools: tuple[tuple[Path, str], ...],
) -> None:
    """Smoke-test only binaries built in this release staging process."""
    if os.name != "nt":
        return
    environment = {
        key: value
        for key, value in os.environ.items()
        if key.upper() in {"SYSTEMROOT", "WINDIR", "COMSPEC", "TEMP", "TMP"}
    }
    environment["PATH"] = ""
    for tool, expected_version in tools:
        try:
            result = subprocess.run(
                [str(tool), "--version"],
                cwd=tool.parent,
                env=environment,
                check=True,
                capture_output=True,
                text=True,
                encoding="utf-8",
                errors="replace",
                timeout=30,
            )
        except (OSError, subprocess.SubprocessError) as exc:
            raise BuildError(
                f"built recovery tool failed empty-PATH --version smoke: {tool.name}: {exc}"
            ) from exc
        output = (result.stdout + result.stderr).strip()
        if not output.startswith(expected_version):
            raise BuildError(
                "built recovery tool version mismatch: "
                f"{tool.name}: expected {expected_version}, got {output!r}"
            )


def _verify_recovery_tool_builds(paths: RepoPaths, go: str) -> list[dict[str, Any]]:
    lock = load_recovery_tool_lock(paths.repo_root)
    expected_tools = (
        (
            paths.kopia_binary,
            "github.com/kopia/kopia",
            "github.com/kopia/kopia",
            lock.kopia_version,
            lock.kopia_sum,
        ),
        (
            paths.age_binary,
            "filippo.io/age/cmd/age",
            "filippo.io/age",
            lock.age_version,
            lock.age_sum,
        ),
        (
            paths.age_keygen_binary,
            "filippo.io/age/cmd/age-keygen",
            "filippo.io/age",
            lock.age_version,
            lock.age_sum,
        ),
    )
    provenance: list[dict[str, Any]] = []
    for binary, package, module, version, module_sum in expected_tools:
        metadata = go_binary_metadata(go, binary)
        expected = {
            "package": package,
            "module": module,
            "version": version,
            "moduleSum": module_sum,
            "goVersion": f"go{lock.go_version}",
        }
        for key, value in expected.items():
            if metadata.get(key) != value:
                raise BuildError(
                    f"{binary.name} Go build metadata mismatch for {key}: "
                    f"expected {value!r}, got {metadata.get(key)!r}"
                )
        expected_build = {"GOOS": "windows", "GOARCH": "amd64", "CGO_ENABLED": "0"}
        for key, value in expected_build.items():
            if metadata["build"].get(key) != value:
                raise BuildError(
                    f"{binary.name} target mismatch for {key}: "
                    f"expected {value!r}, got {metadata['build'].get(key)!r}"
                )
        provenance.append(
            {
                "name": binary.name,
                "path": f"resources/sidecar/tools/{binary.name}",
                "package": package,
                "module": module,
                "version": version,
                "moduleSum": module_sum,
                "sha256": sha256_file(binary),
                "goVersion": metadata["goVersion"],
                "target": {"goos": "windows", "goarch": "amd64", "cgoEnabled": False},
            }
        )
    return provenance


def stage_sidecar_assets(
    paths: RepoPaths,
    *,
    build_info: dict[str, Any],
    modules: list[dict[str, Any]],
    recovery_tool_provenance: list[dict[str, Any]] | None = None,
) -> None:
    if not paths.sidecar_binary.is_file():
        raise BuildError(f"missing sidecar binary: {paths.sidecar_binary}")
    paths.sidecar_assets_dir.mkdir(parents=True, exist_ok=True)
    digest = sha256_file(paths.sidecar_binary)
    paths.sidecar_checksum.write_text(digest + "\n", encoding="utf-8")
    paths.sidecar_build_info.write_text(
        json.dumps(build_info, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    migrations = paths.sidecar_assets_dir / "migrations"
    if migrations.exists():
        shutil.rmtree(migrations)
    migrations.mkdir()
    for source in sorted(paths.sidecar_migrations_dir.glob("*")):
        if source.is_file() and (
            source.name == "manifest.json" or (source.suffix == ".go" and source.name[:1].isdigit())
        ):
            shutil.copy2(source, migrations / source.name)
    tool_paths = (paths.kopia_binary, paths.age_binary, paths.age_keygen_binary)
    for tool in tool_paths:
        if not tool.is_file():
            raise BuildError(f"missing recovery tool: {tool}")
        tool.with_suffix(tool.suffix + ".sha256").write_text(
            sha256_file(tool) + "\n",
            encoding="utf-8",
        )
    if recovery_tool_provenance is None:
        lock = load_recovery_tool_lock(paths.repo_root)
        recovery_tool_provenance = [
            {
                "name": tool.name,
                "path": f"resources/sidecar/tools/{tool.name}",
                "package": package,
                "module": module,
                "version": version,
                "moduleSum": module_sum,
                "sha256": sha256_file(tool),
                "goVersion": f"go{lock.go_version}",
                "target": {"goos": "windows", "goarch": "amd64", "cgoEnabled": False},
            }
            for tool, package, module, version, module_sum in (
                (
                    paths.kopia_binary,
                    "github.com/kopia/kopia",
                    "github.com/kopia/kopia",
                    lock.kopia_version,
                    lock.kopia_sum,
                ),
                (
                    paths.age_binary,
                    "filippo.io/age/cmd/age",
                    "filippo.io/age",
                    lock.age_version,
                    lock.age_sum,
                ),
                (
                    paths.age_keygen_binary,
                    "filippo.io/age/cmd/age-keygen",
                    "filippo.io/age",
                    lock.age_version,
                    lock.age_sum,
                ),
            )
        ]
    paths.recovery_provenance.write_text(
        json.dumps(
            {
                "schemaVersion": 1,
                "lock": {
                    "path": f"{RECOVERY_TOOLS_RELATIVE_ROOT.as_posix()}/go.mod",
                    "goSum": f"{RECOVERY_TOOLS_RELATIVE_ROOT.as_posix()}/go.sum",
                },
                "tools": recovery_tool_provenance,
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    library_components = [
        {
            "type": "library",
            "name": str(module.get("path", "")),
            "version": str(module.get("version", "")),
            "licenses": [
                {"license": {"id": license_id}} for license_id in _module_licenses(module)
            ],
        }
        for module in modules
        if module.get("path")
    ]
    modules_by_path = {str(module.get("path", "")): module for module in modules}
    tool_components = []
    for tool in recovery_tool_provenance:
        module_name = str(tool["module"])
        module = modules_by_path.get(module_name)
        if module is None:
            raise BuildError(f"recovery tool module is missing from SBOM inputs: {module_name}")
        tool_components.append(
            {
                "type": "application",
                "name": str(tool["name"]),
                "version": str(tool["version"]),
                "purl": f"pkg:golang/{module_name}@{tool['version']}",
                "hashes": [{"alg": "SHA-256", "content": str(tool["sha256"])}],
                "licenses": [
                    {"license": {"id": license_id}} for license_id in _module_licenses(module)
                ],
                "properties": [
                    {"name": "vibetable:package", "value": str(tool["package"])},
                    {"name": "vibetable:module", "value": module_name},
                    {"name": "vibetable:moduleSum", "value": str(tool["moduleSum"])},
                    {"name": "vibetable:goVersion", "value": str(tool["goVersion"])},
                    {"name": "vibetable:goos", "value": str(tool["target"]["goos"])},
                    {"name": "vibetable:goarch", "value": str(tool["target"]["goarch"])},
                    {
                        "name": "vibetable:cgoEnabled",
                        "value": str(tool["target"]["cgoEnabled"]).lower(),
                    },
                    {"name": "vibetable:licenseModule", "value": module_name},
                ],
            }
        )
    components = library_components + tool_components
    paths.sidecar_sbom.write_text(
        json.dumps(
            {
                "bomFormat": "CycloneDX",
                "specVersion": "1.5",
                "version": 1,
                "metadata": {
                    "component": {
                        "type": "application",
                        "name": "vibetable-pb",
                        "version": build_info.get("version", ""),
                        "hashes": [{"alg": "SHA-256", "content": digest}],
                    }
                },
                "components": components,
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    paths.sidecar_licenses.write_text(
        _third_party_license_text(modules),
        encoding="utf-8",
    )
    if os.name != "nt":
        paths.sidecar_binary.chmod(
            paths.sidecar_binary.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH
        )


def verify_sidecar_package(paths: RepoPaths) -> None:
    required = (
        paths.sidecar_binary,
        paths.sidecar_checksum,
        paths.sidecar_build_info,
        paths.sidecar_sbom,
        paths.sidecar_licenses,
        paths.recovery_provenance,
        paths.kopia_binary,
        paths.age_binary,
        paths.age_keygen_binary,
        paths.kopia_binary.with_suffix(paths.kopia_binary.suffix + ".sha256"),
        paths.age_binary.with_suffix(paths.age_binary.suffix + ".sha256"),
        paths.age_keygen_binary.with_suffix(paths.age_keygen_binary.suffix + ".sha256"),
        paths.sidecar_assets_dir / "migrations" / "manifest.json",
    )
    missing = [str(path) for path in required if not path.is_file()]
    if missing:
        raise BuildError("missing sidecar package assets: " + ", ".join(missing))
    recovery_tools = (paths.kopia_binary, paths.age_binary, paths.age_keygen_binary)
    if sum(path.stat().st_size for path in recovery_tools) > RECOVERY_TOOL_SIZE_LIMIT:
        raise BuildError("bundled Kopia/age recovery tools exceed the size threshold")
    for tool in recovery_tools:
        expected_tool_hash = (
            tool.with_suffix(tool.suffix + ".sha256").read_text(encoding="utf-8").strip()
        )
        if sha256_file(tool) != expected_tool_hash:
            raise BuildError(f"recovery tool SHA-256 mismatch: {tool.name}")
    lock = load_recovery_tool_lock(paths.repo_root)
    provenance = json.loads(paths.recovery_provenance.read_text(encoding="utf-8"))
    provenance_tools = {
        str(item.get("name")): item
        for item in provenance.get("tools", [])
        if isinstance(item, dict)
    }
    expected_provenance = {
        paths.kopia_binary.name: (
            "github.com/kopia/kopia",
            "github.com/kopia/kopia",
            lock.kopia_version,
            lock.kopia_sum,
        ),
        paths.age_binary.name: (
            "filippo.io/age/cmd/age",
            "filippo.io/age",
            lock.age_version,
            lock.age_sum,
        ),
        paths.age_keygen_binary.name: (
            "filippo.io/age/cmd/age-keygen",
            "filippo.io/age",
            lock.age_version,
            lock.age_sum,
        ),
    }
    for tool in recovery_tools:
        item = provenance_tools.get(tool.name)
        package, module, version, module_sum = expected_provenance[tool.name]
        if item is None:
            raise BuildError(f"recovery tool provenance is missing: {tool.name}")
        expected_values = {
            "path": f"resources/sidecar/tools/{tool.name}",
            "package": package,
            "module": module,
            "version": version,
            "moduleSum": module_sum,
            "sha256": sha256_file(tool),
            "goVersion": f"go{lock.go_version}",
        }
        for key, value in expected_values.items():
            if item.get(key) != value:
                raise BuildError(f"recovery tool provenance mismatch: {tool.name}.{key}")
        if item.get("target") != {
            "goos": "windows",
            "goarch": "amd64",
            "cgoEnabled": False,
        }:
            raise BuildError(f"recovery tool target mismatch: {tool.name}")
    expected = paths.sidecar_checksum.read_text(encoding="utf-8").strip()
    actual = sha256_file(paths.sidecar_binary)
    if expected != actual:
        raise BuildError("sidecar SHA-256 mismatch")
    versions = collect_release_versions(paths.repo_root)
    info = json.loads(paths.sidecar_build_info.read_text(encoding="utf-8"))
    expected_info = {
        "version": versions.app,
        "pocketBaseVersion": versions.pocketbase,
        "celVersion": versions.cel,
        "contractVersion": versions.contract,
        "schemaVersion": versions.schema,
        "migrationHash": versions.migration_hash,
        "protocolV2Version": "2.0",
        "workspaceFormat": "2",
        "repositoryFormat": "kopia-v3",
        "snapshotFormat": "2",
        "packageFormat": "2",
        "kopiaVersion": KOPIA_VERSION,
        "ageVersion": AGE_VERSION,
    }
    for key, value in expected_info.items():
        if info.get(key) != value:
            raise BuildError(f"sidecar build info mismatch: {key}")
    if os.name != "nt" and not os.access(paths.sidecar_binary, os.X_OK):
        raise BuildError("sidecar binary is not executable")
    sbom = json.loads(paths.sidecar_sbom.read_text(encoding="utf-8"))
    if sbom.get("bomFormat") != "CycloneDX" or not isinstance(sbom.get("components"), list):
        raise BuildError("sidecar SBOM is invalid")
    license_bundle = paths.sidecar_licenses.read_text(encoding="utf-8", errors="replace")
    for component in sbom["components"]:
        name = component.get("name")
        licenses = component.get("licenses")
        ids = (
            [item.get("license", {}).get("id") for item in licenses if isinstance(item, dict)]
            if isinstance(licenses, list)
            else []
        )
        if (
            not isinstance(name, str)
            or not name
            or not ids
            or any(not isinstance(value, str) or not value or value == "UNKNOWN" for value in ids)
        ):
            raise BuildError("sidecar SBOM contains an unresolved license")
        properties = {
            item.get("name"): item.get("value")
            for item in component.get("properties", [])
            if isinstance(item, dict)
        }
        license_name = properties.get("vibetable:licenseModule", name)
        if f"===== {license_name} " not in license_bundle:
            raise BuildError(f"sidecar license bundle is missing module: {license_name}")
    sbom_by_name = {
        str(component.get("name")): component
        for component in sbom["components"]
        if isinstance(component, dict)
    }
    for tool in recovery_tools:
        item = provenance_tools[tool.name]
        component = sbom_by_name.get(tool.name)
        if component is None or component.get("type") != "application":
            raise BuildError(f"SBOM is missing recovery tool artifact: {tool.name}")
        hashes = {
            entry.get("alg"): entry.get("content")
            for entry in component.get("hashes", [])
            if isinstance(entry, dict)
        }
        if hashes.get("SHA-256") != item["sha256"] or component.get("version") != item["version"]:
            raise BuildError(f"SBOM recovery tool artifact mismatch: {tool.name}")


def stage_workspace_contracts(paths: RepoPaths) -> None:
    shutil.copytree(
        paths.repo_root / "contracts" / "v2",
        paths.resources_dir / "contracts" / "v2",
        ignore=shutil.ignore_patterns(*CONTRACT_COPY_IGNORES),
        dirs_exist_ok=True,
    )


def _run(
    command: list[str],
    *,
    cwd: Path,
    capture: bool = False,
    env: dict[str, str] | None = None,
) -> subprocess.CompletedProcess[str]:
    resolved = [_resolve_executable(command[0], repo_root=cwd), *command[1:]]
    try:
        return subprocess.run(
            resolved,
            cwd=cwd,
            env=env,
            check=True,
            capture_output=capture,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
    except (OSError, subprocess.CalledProcessError) as exc:
        raise BuildError(f"command failed: {' '.join(command)}") from exc


def _json_stream(text: str) -> list[dict[str, Any]]:
    decoder = json.JSONDecoder()
    values: list[dict[str, Any]] = []
    index = 0
    while index < len(text):
        while index < len(text) and text[index].isspace():
            index += 1
        if index >= len(text):
            break
        value, index = decoder.raw_decode(text, index)
        if isinstance(value, dict):
            values.append(value)
    return values


def _git_value(paths: RepoPaths, *args: str, fallback: str) -> str:
    try:
        result = subprocess.run(
            ["git", *args],
            cwd=paths.repo_root,
            check=True,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
        return result.stdout.strip() or fallback
    except (OSError, subprocess.CalledProcessError):
        return fallback


def _build_sidecar(paths: RepoPaths, *, skip: bool) -> None:
    if skip:
        return
    paths.sidecar_binary.parent.mkdir(parents=True, exist_ok=True)
    commit = _git_value(paths, "rev-parse", "--short=12", "HEAD", fallback="unknown")
    build_time = os.environ.get("SOURCE_DATE_EPOCH")
    if build_time:
        import datetime

        timestamp = (
            datetime.datetime.fromtimestamp(int(build_time), tz=datetime.UTC)
            .isoformat()
            .replace("+00:00", "Z")
        )
    else:
        timestamp = _git_value(
            paths,
            "show",
            "-s",
            "--format=%cI",
            "HEAD",
            fallback="unknown",
        )
    _run(
        build_sidecar_command(
            paths,
            output=paths.sidecar_binary,
            commit=commit,
            build_time=timestamp,
        ),
        cwd=paths.sidecar_source_dir,
    )
    paths.kopia_binary.parent.mkdir(parents=True, exist_ok=True)
    tool_module = paths.repo_root / RECOVERY_TOOLS_RELATIVE_ROOT
    lock = load_recovery_tool_lock(paths.repo_root)
    go = resolve_go(paths.repo_root)
    recovery_go_env = {
        **os.environ,
        "GOOS": "windows",
        "GOARCH": "amd64",
        "CGO_ENABLED": "0",
        "GOTOOLCHAIN": "local",
    }
    go_version = _run(
        [go, "env", "GOVERSION"],
        cwd=tool_module,
        capture=True,
        env=recovery_go_env,
    ).stdout.strip()
    if go_version != f"go{lock.go_version}":
        raise BuildError(
            f"recovery tools require Go {lock.go_version}, got {go_version or 'unknown'}"
        )
    for package, output in (
        ("github.com/kopia/kopia", paths.kopia_binary),
        ("filippo.io/age/cmd/age", paths.age_binary),
        ("filippo.io/age/cmd/age-keygen", paths.age_keygen_binary),
    ):
        _run(
            [
                go,
                "build",
                "-mod=readonly",
                "-trimpath",
                "-buildvcs=false",
                "-o",
                str(output),
                package,
            ],
            cwd=tool_module,
            env=recovery_go_env,
        )
    recovery_tool_provenance = _verify_recovery_tool_builds(paths, go)
    verify_recovery_tool_versions(
        (
            (paths.kopia_binary, lock.kopia_version),
            (paths.age_binary, lock.age_version),
            (paths.age_keygen_binary, lock.age_version),
        )
    )
    build_info_result = _run(
        [str(paths.sidecar_binary), "--build-info"],
        cwd=paths.sidecar_assets_dir,
        capture=True,
    )
    sidecar_packages_result = _run(
        [
            resolve_go(paths.repo_root),
            "list",
            "-deps",
            "-json",
            "./cmd/vibetable-pb",
        ],
        cwd=paths.sidecar_source_dir,
        capture=True,
    )
    package_metadata = sidecar_packages_result.stdout
    for package in (
        "github.com/kopia/kopia",
        "filippo.io/age/cmd/age",
        "filippo.io/age/cmd/age-keygen",
    ):
        packages_result = _run(
            [
                go,
                "list",
                "-mod=readonly",
                "-deps",
                "-json",
                package,
            ],
            cwd=tool_module,
            capture=True,
            env=recovery_go_env,
        )
        package_metadata += packages_result.stdout
    modules = _modules_from_packages(_json_stream(package_metadata))
    stage_sidecar_assets(
        paths,
        build_info=json.loads(build_info_result.stdout),
        modules=modules,
        recovery_tool_provenance=recovery_tool_provenance,
    )


def _build_web(paths: RepoPaths, *, skip: bool) -> None:
    if skip:
        return
    _run(build_npm_build_command(paths), cwd=paths.web_grid_dir)
    source = paths.web_grid_dir / "dist"
    if not source.is_dir():
        raise BuildError("web build produced no dist directory")
    shutil.copytree(source, paths.web_grid_publish_dir)


def _build_backend(paths: RepoPaths, *, skip: bool) -> None:
    if skip:
        return
    output = paths.scratch_root / "backend"
    output.mkdir(parents=True, exist_ok=True)
    _run(
        build_pyinstaller_backend_command(paths, output),
        cwd=paths.repo_root,
    )
    produced = output / BACKEND_EXE_NAME.removesuffix(".exe")
    if not produced.is_dir():
        raise BuildError("PyInstaller backend output is missing")
    shutil.move(str(produced), str(paths.backend_dir))


def _build_desktop(paths: RepoPaths, *, skip: bool) -> None:
    if skip:
        return
    output = paths.scratch_root / "desktop"
    output.mkdir(parents=True, exist_ok=True)
    _run(
        build_dotnet_publish_command(paths, output),
        cwd=paths.repo_root,
    )
    assembly = host_assembly_name(paths.repo_root, paths.desktop_csproj.parent)
    source = output / f"{assembly}.exe"
    if not source.is_file():
        raise BuildError("desktop publish output is missing")
    paths.host_exe.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, paths.host_exe)


def _stage_self_update_smoke_package(
    root: Path,
    package_root: Path,
    *,
    version: str,
    resource_marker: str,
) -> None:
    def link_or_copy(source: str, destination: str) -> str:
        try:
            os.link(source, destination)
            return destination
        except OSError:
            return shutil.copy2(source, destination)

    shutil.copytree(package_root, root, copy_function=link_or_copy)
    identity = root / "release.json"
    identity.unlink()
    identity.write_text(
        json.dumps(
            {
                "product": "VibeTable",
                "version": version,
                "platform": "windows",
                "architecture": "x64",
            },
            separators=(",", ":"),
        ),
        encoding="utf-8",
    )
    resources = root / "resources"
    (resources / "self-update-smoke.txt").write_text(resource_marker, encoding="utf-8")


def wait_for_self_update_activation(
    completion: Path,
    readiness: Path,
    token: str,
    target_version: str,
    process_id: int,
    *,
    timeout_seconds: float = 120,
) -> dict[str, Any]:
    """Wait until full shell readiness authorizes the updater cleanup."""
    deadline = time.monotonic() + timeout_seconds
    readiness_payload: dict[str, Any] | None = None
    while time.monotonic() < deadline:
        if completion.is_file() and not readiness.is_file():
            raise BuildError("desktop self-update smoke cleaned staging before shell readiness")
        if readiness.is_file():
            try:
                payload = json.loads(readiness.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as exc:
                raise BuildError("desktop self-update smoke readiness is invalid") from exc
            if payload.get("ready") is not True:
                raise BuildError(
                    "desktop self-update smoke shell failed: "
                    + str(payload.get("error") or "unknown readiness failure")
                )
            if (
                not all(
                    payload.get(field) is True
                    for field in ("backendReady", "webViewReady", "rendererReady")
                )
                or payload.get("mode") != "shell"
            ):
                raise BuildError("desktop self-update smoke readiness is incomplete")
            workspace_probe = payload.get("workspaceProbe")
            workspace_probe_valid = False
            if isinstance(workspace_probe, dict):
                status = workspace_probe.get("status")
                if status == "skippedNoRegisteredWorkspace":
                    workspace_probe_valid = all(
                        workspace_probe.get(field) is None
                        for field in ("workspaceId", "sessionEpoch", "tableCount")
                    )
                elif status == "healthy":
                    session_epoch = workspace_probe.get("sessionEpoch")
                    table_count = workspace_probe.get("tableCount")
                    workspace_probe_valid = (
                        isinstance(workspace_probe.get("workspaceId"), str)
                        and bool(workspace_probe["workspaceId"])
                        and type(session_epoch) is int
                        and session_epoch > 0
                        and type(table_count) is int
                        and table_count >= 0
                    )
            if not workspace_probe_valid:
                raise BuildError("desktop self-update smoke workspace probe evidence is invalid")
            readiness_payload = payload
            if completion.is_file():
                try:
                    completion_payload = json.loads(completion.read_text(encoding="utf-8-sig"))
                    readiness_at = datetime.fromisoformat(str(payload["writtenAt"]))
                    confirmed_at = datetime.fromisoformat(str(completion_payload["confirmedAt"]))
                except (KeyError, OSError, ValueError, TypeError, json.JSONDecodeError) as exc:
                    raise BuildError(
                        "desktop self-update smoke completion evidence is invalid"
                    ) from exc
                if (
                    completion_payload.get("token") != token
                    or completion_payload.get("targetVersion") != target_version
                    or completion_payload.get("processId") != process_id
                ):
                    raise BuildError("desktop self-update smoke completion identity is invalid")
                if readiness_at.utcoffset() is None or confirmed_at.utcoffset() is None:
                    raise BuildError(
                        "desktop self-update smoke evidence timestamps require offsets"
                    )
                if confirmed_at < readiness_at:
                    raise BuildError("desktop self-update smoke completed before shell readiness")
                return payload
        if wait_for_windows_process_exit(process_id, timeout_seconds=0):
            raise BuildError("desktop self-update smoke process exited before activation completed")
        time.sleep(0.1)
    if readiness_payload is None:
        raise BuildError("desktop self-update smoke shell did not become ready")
    raise BuildError("desktop self-update smoke restart handoff did not complete")


def wait_for_windows_process_exit(process_id: int, *, timeout_seconds: float) -> bool:
    """Wait for one exact Windows process without guessing by executable name."""
    if os.name != "nt":
        raise BuildError("desktop self-update process waiting requires Windows")
    import ctypes
    from ctypes import wintypes

    synchronize = 0x00100000
    wait_object_0 = 0
    wait_timeout = 0x00000102
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel32.OpenProcess.restype = wintypes.HANDLE
    kernel32.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
    kernel32.WaitForSingleObject.restype = wintypes.DWORD
    kernel32.CloseHandle.argtypes = [wintypes.HANDLE]
    handle = kernel32.OpenProcess(synchronize, False, process_id)
    if not handle:
        return windows_process_exited_after_open_failure(ctypes.get_last_error())
    try:
        milliseconds = max(0, min(int(timeout_seconds * 1000), 0xFFFFFFFE))
        result = kernel32.WaitForSingleObject(handle, milliseconds)
        if result == wait_object_0:
            return True
        if result == wait_timeout:
            return False
        raise BuildError("desktop self-update process wait failed")
    finally:
        kernel32.CloseHandle(handle)


def windows_process_exited_after_open_failure(error_code: int) -> bool:
    """Interpret only a nonexistent PID as an already-exited process."""
    error_invalid_parameter = 87
    if error_code == error_invalid_parameter:
        return True
    raise BuildError(f"desktop self-update process open failed: Win32 error {error_code}")


def terminate_windows_process_tree(process_id: int) -> None:
    """Best-effort cleanup for the exact smoke process and its child tree."""
    subprocess.run(
        ["taskkill", "/PID", str(process_id), "/T", "/F"],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        timeout=30,
    )


def read_self_update_process_evidence(
    path: Path,
    *,
    token: str,
    target_version: str,
) -> int:
    try:
        payload = json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, json.JSONDecodeError) as exc:
        raise BuildError("desktop self-update smoke process evidence is missing") from exc
    process_id = payload.get("processId")
    if (
        payload.get("token") != token
        or payload.get("targetVersion") != target_version
        or not isinstance(process_id, int)
        or process_id <= 0
    ):
        raise BuildError("desktop self-update smoke process evidence is invalid")
    return process_id


def _offset_datetime(value: object) -> datetime | None:
    if type(value) is not str:
        return None
    try:
        parsed = datetime.fromisoformat(value)
    except ValueError:
        return None
    return parsed if parsed.utcoffset() is not None else None


def _is_lower_hex(value: object, *, length: int) -> bool:
    return (
        type(value) is str
        and len(value) == length
        and all(character in "0123456789abcdef" for character in value)
    )


def _is_valid_self_update_rollback_receipt(
    receipt: object,
    *,
    receipt_name: str,
    target: Path,
    stage: Path,
    token: str,
    expected_updater_process_id: int,
    updated_process_id: int,
    expected_failure_code: str,
) -> bool:
    if not isinstance(receipt, dict) or set(receipt) != _SELF_UPDATE_ACTIVATION_POINTER_V2_FIELDS:
        return False
    receipt_updater_process_id = receipt.get("updaterProcessId")
    watchdog_process_id = receipt.get("watchdogProcessId")
    receipt_updated_process_id = receipt.get("updatedProcessId")
    worker_process_id = receipt.get("workerProcessId")
    process_ids = (receipt_updater_process_id, watchdog_process_id, worker_process_id)
    if any(type(process_id) is not int or process_id <= 0 for process_id in process_ids):
        return False
    if type(expected_updater_process_id) is not int or expected_updater_process_id <= 0:
        return False
    if receipt_updater_process_id != expected_updater_process_id:
        return False
    if type(updated_process_id) is not int or updated_process_id <= 0:
        return False
    if (
        type(receipt_updated_process_id) is not int
        or receipt_updated_process_id != updated_process_id
    ):
        return False

    timestamp_fields = (
        "updaterStartedAtUtc",
        "createdAtUtc",
        "watchdogStartedAtUtc",
        "updatedStartedAtUtc",
        "rollbackRequestedAtUtc",
        "ownedGroupQuiescedAtUtc",
        "workerStartedAtUtc",
        "rolledBackAtUtc",
    )
    timestamps = {field: _offset_datetime(receipt.get(field)) for field in timestamp_fields}
    if any(timestamp is None for timestamp in timestamps.values()):
        return False
    updater_started = cast(datetime, timestamps["updaterStartedAtUtc"])
    created_at = cast(datetime, timestamps["createdAtUtc"])
    watchdog_started = cast(datetime, timestamps["watchdogStartedAtUtc"])
    updated_started = cast(datetime, timestamps["updatedStartedAtUtc"])
    rollback_requested = cast(datetime, timestamps["rollbackRequestedAtUtc"])
    group_quiesced = cast(datetime, timestamps["ownedGroupQuiescedAtUtc"])
    worker_started = cast(datetime, timestamps["workerStartedAtUtc"])
    rolled_back = cast(datetime, timestamps["rolledBackAtUtc"])
    if not (
        updater_started
        <= created_at
        <= updated_started
        <= rollback_requested
        <= group_quiesced
        <= worker_started
        <= rolled_back
    ):
        return False
    if watchdog_process_id != receipt_updater_process_id or watchdog_started != updater_started:
        return False
    process_identities = {
        (receipt_updater_process_id, updater_started),
        (updated_process_id, updated_started),
        (worker_process_id, worker_started),
    }
    if len(process_identities) != 3:
        return False

    rollback_attempt = receipt.get("rollbackAttempt")
    ledger = receipt.get("ownedEntryLedger")
    expected_ledger = {
        ("resources", "restored"),
        ("release.json", "restored"),
        (HOST_EXE_NAME, "restored"),
    }
    if not isinstance(ledger, list) or len(ledger) != len(expected_ledger):
        return False
    if any(
        not isinstance(entry, dict)
        or set(entry) != {"name", "phase"}
        or type(entry["name"]) is not str
        or type(entry["phase"]) is not str
        for entry in ledger
    ):
        return False
    actual_ledger = {(entry["name"], entry["phase"]) for entry in ledger}
    worker_replacement_count = receipt.get("workerReplacementCount")
    return (
        type(receipt.get("schemaVersion")) is int
        and receipt.get("schemaVersion") == 2
        and receipt.get("state") == "rollbackComplete"
        and type(receipt.get("targetRoot")) is str
        and Path(receipt["targetRoot"]).resolve() == target.resolve()
        and type(receipt.get("stagingRoot")) is str
        and Path(receipt["stagingRoot"]).resolve() == stage.resolve()
        and receipt.get("currentVersion") == "1.0.0"
        and receipt.get("targetVersion") == "1.0.1"
        and receipt.get("token") == token
        and receipt.get("smokeTest") is True
        and receipt.get("confirmedAt") is None
        and _is_lower_hex(receipt.get("ownedGroupId"), length=32)
        and receipt.get("launchNonce") is None
        and receipt.get("failureCode") == expected_failure_code
        and receipt.get("workerLaunchNonce") is None
        and type(worker_replacement_count) is int
        and worker_replacement_count in (0, 1)
        and actual_ledger == expected_ledger
        and _is_lower_hex(rollback_attempt, length=32)
        and receipt_name == f".VibeTable.Next.update-rollback-{rollback_attempt}.json"
        and receipt.get("rollbackErrorCode") is None
    )


@dataclass(frozen=True)
class _SelfUpdateConsumedRequest:
    path: Path
    description: str


def _wait_for_self_update_rollback(
    root: Path,
    *,
    process_scope: WindowsProcessScope,
    target: Path,
    stage: Path,
    token: str,
    updater_process_id: int,
    updated_process_id: int,
    scenario_slug: str,
    expected_failure_code: str,
    health_failure_readiness: Path | None,
    consumed_request: _SelfUpdateConsumedRequest | None,
    timeout_seconds: float = 120,
) -> int:
    """Validate shared terminal rollback evidence and return the restored host PID."""
    deadline = time.monotonic() + timeout_seconds
    restored_readiness = root / SELF_UPDATE_RESTORED_READINESS_DIR / SHELL_READINESS_FILE
    restored_state = root / SELF_UPDATE_RESTORED_CONTROLS_DIR / "host-lifecycle-state.json"
    while time.monotonic() < deadline:
        receipts = sorted(root.glob(SELF_UPDATE_ROLLBACK_RECEIPT_GLOB))
        if len(receipts) > 1:
            raise BuildError("desktop self-update smoke produced ambiguous rollback receipts")
        if not (
            (health_failure_readiness is None or health_failure_readiness.is_file())
            and len(receipts) == 1
            and restored_readiness.is_file()
            and restored_state.is_file()
        ):
            time.sleep(0.05)
            continue
        evidence_role = "failed-readiness"
        try:
            failed = (
                json.loads(health_failure_readiness.read_text(encoding="utf-8"))
                if health_failure_readiness is not None
                else None
            )
            evidence_role = "rollback-receipt"
            receipt = json.loads(receipts[0].read_text(encoding="utf-8"))
            evidence_role = "restored-readiness"
            restored = json.loads(restored_readiness.read_text(encoding="utf-8"))
            evidence_role = "restored-state"
            state = json.loads(restored_state.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            raise BuildError(
                "desktop self-update smoke rollback evidence is invalid: "
                f"{scenario_slug}/{evidence_role}: "
                f"JSONDecodeError line={exc.lineno} column={exc.colno}"
            ) from exc
        except OSError as exc:
            raise BuildError(
                "desktop self-update smoke rollback evidence is invalid: "
                f"{scenario_slug}/{evidence_role}: {type(exc).__name__} "
                f"errno={exc.errno} winerror={getattr(exc, 'winerror', None)}"
            ) from exc
        if health_failure_readiness is not None and (
            not isinstance(failed, dict)
            or failed.get("ready") is not False
            or not isinstance(failed.get("error"), str)
            or not failed["error"]
        ):
            raise BuildError("desktop self-update smoke did not observe a health failure")
        if not _is_valid_self_update_rollback_receipt(
            receipt,
            receipt_name=receipts[0].name,
            target=target,
            stage=stage,
            token=token,
            expected_updater_process_id=updater_process_id,
            updated_process_id=updated_process_id,
            expected_failure_code=expected_failure_code,
        ):
            raise BuildError("desktop self-update smoke rollback receipt identity is invalid")
        if consumed_request is not None and consumed_request.path.exists():
            raise BuildError(
                f"desktop self-update smoke did not consume {consumed_request.description}"
            )
        if (
            restored.get("ready") is not True
            or restored.get("mode") != "shell"
            or not all(
                restored.get(field) is True
                for field in ("backendReady", "webViewReady", "rendererReady")
            )
        ):
            raise BuildError("desktop self-update smoke restored shell readiness is invalid")
        restored_process_id = state.get("hostProcessId")
        if (
            state.get("evidenceKind") != "packaged-host-control"
            or state.get("hostExecutable") != HOST_EXE_NAME
            or type(restored_process_id) is not int
            or restored_process_id <= 0
            or restored_process_id == updated_process_id
        ):
            raise BuildError("desktop self-update smoke restored process evidence is invalid")
        members = process_scope.snapshot().members
        member_pids = {member.pid for member in members}
        restored_member = next(
            (member for member in members if member.pid == restored_process_id),
            None,
        )
        if (
            updated_process_id not in member_pids
            and restored_member is not None
            and restored_member.identity_verified
            and restored_member.executable_name == HOST_EXE_NAME
        ):
            return restored_process_id
        time.sleep(0.05)
    raise BuildError(f"desktop self-update smoke {scenario_slug} rollback did not complete")


def wait_for_self_update_health_failure_rollback(
    root: Path,
    *,
    process_scope: WindowsProcessScope,
    target: Path,
    stage: Path,
    token: str,
    updater_process_id: int,
    updated_process_id: int,
    timeout_seconds: float = 120,
) -> int:
    """Validate a health-failure rollback and return the restored host PID."""
    return _wait_for_self_update_rollback(
        root,
        process_scope=process_scope,
        target=target,
        stage=stage,
        token=token,
        updater_process_id=updater_process_id,
        updated_process_id=updated_process_id,
        scenario_slug="health-failure",
        expected_failure_code="workspaceHealthProbeFailed",
        health_failure_readiness=(root / SELF_UPDATE_SMOKE_READINESS_DIR / SHELL_READINESS_FILE),
        consumed_request=None,
        timeout_seconds=timeout_seconds,
    )


def wait_for_self_update_updated_exit_rollback(
    root: Path,
    *,
    process_scope: WindowsProcessScope,
    target: Path,
    stage: Path,
    token: str,
    updater_process_id: int,
    updated_process_id: int,
    consumed_request: Path,
    timeout_seconds: float = 120,
) -> int:
    """Validate an updated-host controlled-exit rollback and return the restored host PID."""
    return _wait_for_self_update_rollback(
        root,
        process_scope=process_scope,
        target=target,
        stage=stage,
        token=token,
        updater_process_id=updater_process_id,
        updated_process_id=updated_process_id,
        scenario_slug="updated-exit",
        expected_failure_code="updatedProcessExited",
        health_failure_readiness=None,
        consumed_request=_SelfUpdateConsumedRequest(
            consumed_request,
            "updated close request",
        ),
        timeout_seconds=timeout_seconds,
    )


def wait_for_self_update_activation_pointer(
    path: Path,
    *,
    target: Path,
    stage: Path,
    token: str,
    updater_process_id: int,
    timeout_seconds: float = 30,
) -> None:
    """Wait for the updater's durable prepared journal and validate its exact identity."""
    deadline = time.monotonic() + timeout_seconds
    last_error: Exception | None = None
    while time.monotonic() < deadline:
        try:
            payload = json.loads(path.read_text(encoding="utf-8"))
            schema_version = payload.get("schemaVersion")
            expected_fields = (
                {
                    1: _SELF_UPDATE_ACTIVATION_POINTER_V1_FIELDS,
                    2: _SELF_UPDATE_ACTIVATION_POINTER_V2_FIELDS,
                }.get(schema_version)
                if type(schema_version) is int
                else None
            )
            if expected_fields is None or set(payload) != expected_fields:
                raise BuildError("desktop self-update activation pointer fields are invalid")
            if (
                payload.get("state") != "prepared"
                or Path(payload.get("targetRoot", "")).resolve() != target.resolve()
                or Path(payload.get("stagingRoot", "")).resolve() != stage.resolve()
                or payload.get("currentVersion") != "1.0.0"
                or payload.get("targetVersion") != "1.0.1"
                or payload.get("token") != token
                or payload.get("smokeTest") is not True
                or type(payload.get("updaterProcessId")) is not int
                or payload.get("updaterProcessId") != updater_process_id
                or payload.get("confirmedAt") is not None
            ):
                raise BuildError("desktop self-update activation pointer identity is invalid")
            if schema_version == 2 and (
                any(
                    payload.get(field) is not None
                    for field in _SELF_UPDATE_ACTIVATION_POINTER_V2_PREPARED_NULL_FIELDS
                )
                or type(payload.get("workerReplacementCount")) is not int
                or payload.get("workerReplacementCount") != 0
                or type(payload.get("ownedEntryLedger")) is not list
                or payload.get("ownedEntryLedger") != []
            ):
                raise BuildError("desktop self-update activation pointer identity is invalid")
            updater_started = datetime.fromisoformat(str(payload["updaterStartedAtUtc"]))
            created_at = datetime.fromisoformat(str(payload["createdAtUtc"]))
            if updater_started.utcoffset() is None or created_at.utcoffset() is None:
                raise BuildError(
                    "desktop self-update activation pointer timestamps require offsets"
                )
            return
        except (OSError, json.JSONDecodeError, KeyError, TypeError, ValueError) as exc:
            last_error = exc
            time.sleep(0.05)
    raise BuildError("desktop self-update activation pointer was not persisted") from last_error


def self_update_smoke_local_data_root(root: Path) -> Path:
    """Return the isolated LocalApplicationData root used by the packaged host."""
    return root / SELF_UPDATE_SMOKE_READINESS_DIR / "local-data"


def _seed_self_update_health_failure_registry(root: Path) -> None:
    registry_root = self_update_smoke_local_data_root(root) / "VibeTable" / "shell"
    registry_root.mkdir(parents=True)
    missing_workspace = root / "missing-health-probe-workspace"
    registry = {
        "formatVersion": 2,
        "workspaces": [
            {
                "contractVersion": "2.0",
                "workspaceId": "11111111-1111-4111-8111-111111111111",
                "displayName": "Updater health-failure smoke",
                "selectedRoot": str(missing_workspace.resolve()),
                "activityRoot": None,
                "storageKind": "fixed",
                "coordinationStrength": "strong",
                "lastOpenedAt": "2026-08-28T04:00:00+00:00",
                "lastKnownHealth": "offline",
                "lastSnapshotAt": None,
                "lastSyncAt": None,
                "pendingSync": False,
            }
        ],
    }
    (registry_root / "workspace-registry-v2.json").write_text(
        json.dumps(registry, ensure_ascii=False, separators=(",", ":")),
        encoding="utf-8",
    )


@dataclass(frozen=True)
class _SelfUpdateRollbackEvidence:
    health_failure_readiness: Path | None = None
    consumed_request: _SelfUpdateConsumedRequest | None = None


@dataclass(frozen=True)
class _SelfUpdateRollbackScenario:
    slug: str
    failure_code: str
    arrange_failure: Callable[[Path], _SelfUpdateRollbackEvidence]
    updater_wait_timeout_seconds: float


def _arrange_self_update_health_failure(root: Path) -> _SelfUpdateRollbackEvidence:
    _seed_self_update_health_failure_registry(root)
    return _SelfUpdateRollbackEvidence(
        health_failure_readiness=(root / SELF_UPDATE_SMOKE_READINESS_DIR / SHELL_READINESS_FILE)
    )


def _arrange_self_update_updated_exit(root: Path) -> _SelfUpdateRollbackEvidence:
    updated_controls = root / SELF_UPDATE_UPDATED_CONTROLS_DIR
    updated_controls.mkdir()
    close_request = updated_controls / "host-normal-close.request"
    close_request.write_text("", encoding="utf-8")
    return _SelfUpdateRollbackEvidence(
        consumed_request=_SelfUpdateConsumedRequest(
            close_request,
            "updated close request",
        )
    )


def _arrange_self_update_health_timeout(root: Path) -> _SelfUpdateRollbackEvidence:
    updated_controls = root / SELF_UPDATE_UPDATED_CONTROLS_DIR
    updated_controls.mkdir()
    hold_request = updated_controls / "self-update-health-timeout-hold.request"
    hold_request.write_text("", encoding="utf-8")
    return _SelfUpdateRollbackEvidence(
        consumed_request=_SelfUpdateConsumedRequest(
            hold_request,
            "health-timeout hold request",
        )
    )


_HEALTH_FAILURE_ROLLBACK_SCENARIO = _SelfUpdateRollbackScenario(
    slug="health-failure",
    failure_code="workspaceHealthProbeFailed",
    arrange_failure=_arrange_self_update_health_failure,
    updater_wait_timeout_seconds=120,
)
_UPDATED_EXIT_ROLLBACK_SCENARIO = _SelfUpdateRollbackScenario(
    slug="updated-exit",
    failure_code="updatedProcessExited",
    arrange_failure=_arrange_self_update_updated_exit,
    updater_wait_timeout_seconds=120,
)
_HEALTH_TIMEOUT_ROLLBACK_SCENARIO = _SelfUpdateRollbackScenario(
    slug="health-timeout",
    failure_code="healthTimeout",
    arrange_failure=_arrange_self_update_health_timeout,
    updater_wait_timeout_seconds=180,
)
_SELF_UPDATE_ROLLBACK_SCENARIOS = (
    _HEALTH_FAILURE_ROLLBACK_SCENARIO,
    _UPDATED_EXIT_ROLLBACK_SCENARIO,
    _HEALTH_TIMEOUT_ROLLBACK_SCENARIO,
)


def _bounded_self_update_close_diagnostic_text(value: str) -> str:
    if len(value) <= _SELF_UPDATE_CLOSE_DIAGNOSTIC_TEXT_LIMIT:
        return value
    omitted = len(value) - _SELF_UPDATE_CLOSE_DIAGNOSTIC_TEXT_LIMIT
    return value[:_SELF_UPDATE_CLOSE_DIAGNOSTIC_TEXT_LIMIT] + f"...<truncated={omitted}>"


def request_restored_self_update_host_close(
    controls: Path,
    process_id: int,
    process_scope: WindowsProcessScope,
) -> None:
    """Request a close only while the exact restored host is still running."""
    if process_id not in {member.pid for member in process_scope.snapshot().members}:
        raise BuildError("desktop self-update smoke restored process exited before close request")
    request = controls / "host-normal-close.request"
    request.write_text("", encoding="utf-8")
    wait_result = process_scope.wait_empty(timeout=30)
    if not wait_result.success:
        snapshot_unavailable = False
        snapshot_error = "none"
        try:
            remaining_members = process_scope.snapshot().members
        except (OSError, RuntimeError) as exc:
            remaining_members = ()
            snapshot_unavailable = True
            snapshot_error = _bounded_self_update_close_diagnostic_text(type(exc).__name__)
        shown_members = remaining_members[:_SELF_UPDATE_CLOSE_DIAGNOSTIC_ITEM_LIMIT]
        member_details = "; ".join(
            (
                f"pid={member.pid}, "
                "executable="
                f"{_bounded_self_update_close_diagnostic_text(PureWindowsPath(member.executable_name).name)}, "
                f"identityVerified={str(member.identity_verified).lower()}"
            )
            for member in shown_members
        )
        omitted_members = len(remaining_members) - len(shown_members)
        if omitted_members:
            member_details = f"{member_details}; omitted={omitted_members}"
        wait_remaining = (
            "unknown"
            if wait_result.remaining_pids is None
            else ", ".join(
                str(pid)
                for pid in wait_result.remaining_pids[:_SELF_UPDATE_CLOSE_DIAGNOSTIC_ITEM_LIMIT]
            )
        )
        if wait_result.remaining_pids is not None:
            omitted_pids = max(
                0,
                len(wait_result.remaining_pids) - _SELF_UPDATE_CLOSE_DIAGNOSTIC_ITEM_LIMIT,
            )
            if omitted_pids:
                wait_remaining = f"{wait_remaining}; omitted={omitted_pids}"
        shown_errors = wait_result.errors[:_SELF_UPDATE_CLOSE_DIAGNOSTIC_ERROR_LIMIT]
        wait_errors = (
            "; ".join(_bounded_self_update_close_diagnostic_text(error) for error in shown_errors)
            or "none"
        )
        omitted_errors = len(wait_result.errors) - len(shown_errors)
        restored_process_in_job = (
            (
                "unknown"
                if wait_result.remaining_pids is None
                else str(process_id in wait_result.remaining_pids).lower()
            )
            if snapshot_unavailable
            else str(any(member.pid == process_id for member in remaining_members)).lower()
        )
        raise BuildError(
            "desktop self-update smoke restored process did not exit "
            f"(restoredProcessId={process_id}; "
            f"restoredProcessInJob={restored_process_in_job}; "
            f"waitRemainingPids=[{wait_remaining}]; "
            f"remainingMembers=[{member_details}]; "
            f"waitErrors=[{wait_errors}]; "
            f"waitErrorsOmitted={omitted_errors}; "
            f"snapshotUnavailable={str(snapshot_unavailable).lower()}; "
            f"snapshotError={snapshot_error})"
        )
    if request.exists():
        raise BuildError("desktop self-update smoke restored process did not consume close request")


def cleanup_self_update_process_scope(process_scope: WindowsProcessScope) -> None:
    """Clean the atomically Job-owned updater tree without reopening bare PIDs."""
    if process_scope.wait_empty(timeout=0).success:
        return
    result = process_scope.terminate_all(timeout=30)
    if not result.success:
        raise BuildError("desktop self-update smoke could not clean its process scope")


def _run_desktop_self_update_rollback_smoke(
    package: Path,
    root: Path,
    *,
    scenario: _SelfUpdateRollbackScenario,
) -> None:
    """Exercise one packaged rollback scenario through the shared process-safe harness."""
    try:
        from scripts.qa.windows_process_scope import ProcessLaunchSpec, WindowsProcessScope
    except ModuleNotFoundError:  # pragma: no cover - direct script execution
        from qa.windows_process_scope import ProcessLaunchSpec, WindowsProcessScope

    root.mkdir()
    target = root / "VibeTable.Next"
    stage = root / f".VibeTable.Next.update-{scenario.slug}"
    source = stage / "package" / ARCHIVE_ROOT_NAME
    _stage_self_update_smoke_package(
        target,
        package,
        version="1.0.0",
        resource_marker="old",
    )
    _stage_self_update_smoke_package(
        source,
        package,
        version="1.0.1",
        resource_marker="new",
    )
    install_sentinel = target / "user-data.db"
    external_sentinel = self_update_smoke_local_data_root(root) / "user-data.db"
    install_sentinel.write_text("preserve-install-root", encoding="utf-8")
    external_sentinel.parent.mkdir(parents=True)
    external_sentinel.write_text("preserve-user-data", encoding="utf-8")
    expected_evidence = scenario.arrange_failure(root)
    restored_readiness = root / SELF_UPDATE_RESTORED_READINESS_DIR
    restored_controls = root / SELF_UPDATE_RESTORED_CONTROLS_DIR
    restored_readiness.mkdir()
    restored_controls.mkdir()

    applied_scope: WindowsProcessScope | None = None
    blocking_parent = subprocess.Popen(
        [sys.executable, "-c", "import time; time.sleep(120)"],
        cwd=root,
    )
    try:
        token = secrets.token_hex(32)
        plan = {
            "SchemaVersion": 1,
            "TargetRoot": str(target),
            "SourceRoot": str(source),
            "StagingRoot": str(stage),
            "ParentProcessId": blocking_parent.pid,
            "CurrentVersion": "1.0.0",
            "TargetVersion": "1.0.1",
            "Token": token,
            "SmokeTest": True,
        }
        plan_path = stage / "update-plan.json"
        plan_path.write_text(json.dumps(plan), encoding="utf-8")
        environment = os.environ.copy()
        environment[SELF_UPDATE_SMOKE_TOKEN_ENV] = token
        applied_scope = WindowsProcessScope.launch(
            ProcessLaunchSpec(
                [str(source / HOST_EXE_NAME), "--apply-update", str(plan_path)],
                cwd=source,
                env=environment,
            )
        )
        applied = applied_scope.root
        wait_for_self_update_activation_pointer(
            root / PENDING_UPDATE_ACTIVATION_POINTER,
            target=target,
            stage=stage,
            token=token,
            updater_process_id=applied.pid,
        )
        blocking_parent.terminate()
        blocking_parent.wait(timeout=30)
        applied_returncode = applied.wait(timeout=scenario.updater_wait_timeout_seconds)
        if applied_returncode != 0:
            raise BuildError(
                f"desktop self-update {scenario.slug} updater exited with {applied_returncode}"
            )
        updated_process_id = read_self_update_process_evidence(
            root / SELF_UPDATE_SMOKE_READINESS_DIR / SELF_UPDATE_SMOKE_PROCESS_FILE,
            token=token,
            target_version="1.0.1",
        )
        restored_process_id = _wait_for_self_update_rollback(
            root,
            process_scope=applied_scope,
            target=target,
            stage=stage,
            token=token,
            updater_process_id=applied.pid,
            updated_process_id=updated_process_id,
            scenario_slug=scenario.slug,
            expected_failure_code=scenario.failure_code,
            health_failure_readiness=expected_evidence.health_failure_readiness,
            consumed_request=expected_evidence.consumed_request,
        )
        identity = json.loads((target / "release.json").read_text(encoding="utf-8"))
        if identity.get("version") != "1.0.0":
            raise BuildError("desktop self-update smoke did not restore the old package identity")
        marker = (target / "resources" / "self-update-smoke.txt").read_text(encoding="utf-8")
        if marker != "old":
            raise BuildError("desktop self-update smoke did not restore old package resources")
        if install_sentinel.read_text(encoding="utf-8") != "preserve-install-root":
            raise BuildError("desktop self-update smoke overwrote an unknown install-root file")
        if external_sentinel.read_text(encoding="utf-8") != "preserve-user-data":
            raise BuildError("desktop self-update smoke overwrote external user data")
        request_restored_self_update_host_close(
            restored_controls,
            restored_process_id,
            applied_scope,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise BuildError(
            f"desktop self-update smoke could not run the {scenario.slug} package"
        ) from exc
    finally:
        try:
            if blocking_parent.poll() is None:
                blocking_parent.terminate()
                blocking_parent.wait(timeout=30)
        finally:
            if applied_scope is not None:
                try:
                    cleanup_self_update_process_scope(applied_scope)
                finally:
                    applied_scope.close()


def _run_desktop_self_update_rollback_smokes(package: Path, root: Path) -> None:
    for scenario in _SELF_UPDATE_ROLLBACK_SCENARIOS:
        _run_desktop_self_update_rollback_smoke(
            package,
            root / scenario.slug,
            scenario=scenario,
        )


def run_desktop_self_update_smoke(
    package_root: Path,
    smoke_root: Path,
    *,
    repo_root: Path,
) -> None:
    """Exercise the published host's process-out update path without user data access."""
    if os.name != "nt":
        raise BuildError("desktop self-update smoke requires Windows")
    package = package_root.resolve()
    executable = package / HOST_EXE_NAME
    if not package.is_dir() or not executable.is_file():
        raise BuildError("desktop self-update smoke package is missing")
    repository = Path(os.path.abspath(repo_root))
    root = Path(os.path.abspath(smoke_root))
    expected_root = repository / "build" / "self-update-smoke"
    if root != expected_root:
        raise BuildError("desktop self-update smoke root must be build/self-update-smoke")
    if root.is_symlink() or (hasattr(os.path, "isjunction") and os.path.isjunction(root)):
        raise BuildError("desktop self-update smoke root must not be a link")
    if root.exists():
        shutil.rmtree(root)
    root.mkdir(parents=True)

    target = root / "VibeTable.Next"
    stage = root / ".VibeTable.Next.update-smoke"
    source = stage / "package" / ARCHIVE_ROOT_NAME
    _stage_self_update_smoke_package(
        target,
        package,
        version="1.0.0",
        resource_marker="old",
    )
    _stage_self_update_smoke_package(
        source,
        package,
        version="1.0.1",
        resource_marker="new",
    )
    install_sentinel = target / "user-data.db"
    external_sentinel = root / "local-app-data" / "user-data.db"
    install_sentinel.write_text("preserve-install-root", encoding="utf-8")
    external_sentinel.parent.mkdir()
    external_sentinel.write_text("preserve-user-data", encoding="utf-8")

    blocking_parent = subprocess.Popen(
        [sys.executable, "-c", "import time; time.sleep(120)"],
        cwd=root,
    )
    token = secrets.token_hex(32)
    plan = {
        "SchemaVersion": 1,
        "TargetRoot": str(target),
        "SourceRoot": str(source),
        "StagingRoot": str(stage),
        "ParentProcessId": blocking_parent.pid,
        "CurrentVersion": "1.0.0",
        "TargetVersion": "1.0.1",
        "Token": token,
        "SmokeTest": True,
    }
    plan_path = stage / "update-plan.json"
    plan_path.write_text(json.dumps(plan), encoding="utf-8")
    environment = os.environ.copy()
    environment[SELF_UPDATE_SMOKE_TOKEN_ENV] = token
    applied: subprocess.Popen[bytes] | None = None
    try:
        applied = subprocess.Popen(
            [str(source / HOST_EXE_NAME), "--apply-update", str(plan_path)],
            cwd=source,
            env=environment,
        )
        wait_for_self_update_activation_pointer(
            root / PENDING_UPDATE_ACTIVATION_POINTER,
            target=target,
            stage=stage,
            token=token,
            updater_process_id=applied.pid,
        )
        blocking_parent.terminate()
        blocking_parent.wait(timeout=30)
        applied_returncode = applied.wait(timeout=120)
    except (OSError, subprocess.SubprocessError) as exc:
        raise BuildError("desktop self-update smoke could not run the published host") from exc
    finally:
        try:
            if blocking_parent.poll() is None:
                blocking_parent.terminate()
                blocking_parent.wait(timeout=30)
        finally:
            if applied is not None and applied.poll() is None:
                terminate_windows_process_tree(applied.pid)
    if applied_returncode != 0:
        raise BuildError(f"desktop self-update smoke updater exited with {applied_returncode}")

    completion = target / SELF_UPDATE_SMOKE_COMPLETION_FILE
    readiness_root = root / SELF_UPDATE_SMOKE_READINESS_DIR
    readiness = readiness_root / SHELL_READINESS_FILE
    process_id: int | None = None
    try:
        process_id = read_self_update_process_evidence(
            readiness_root / SELF_UPDATE_SMOKE_PROCESS_FILE,
            token=token,
            target_version="1.0.1",
        )
        wait_for_self_update_activation(
            completion,
            readiness,
            token,
            "1.0.1",
            process_id,
        )
        if not wait_for_windows_process_exit(process_id, timeout_seconds=30):
            raise BuildError("desktop self-update smoke process did not exit")
    except Exception:
        if process_id is not None:
            terminate_windows_process_tree(process_id)
        raise
    if stage.exists():
        raise BuildError("desktop self-update smoke did not clean its staging directory")
    if (root / PENDING_UPDATE_ACTIVATION_POINTER).exists():
        raise BuildError("desktop self-update smoke retained its activation pointer")
    identity = json.loads((target / "release.json").read_text(encoding="utf-8"))
    if identity.get("version") != "1.0.1":
        raise BuildError("desktop self-update smoke retained the old package identity")
    if (target / "resources" / "self-update-smoke.txt").read_text(encoding="utf-8") != "new":
        raise BuildError("desktop self-update smoke did not replace package resources")
    if install_sentinel.read_text(encoding="utf-8") != "preserve-install-root":
        raise BuildError("desktop self-update smoke overwrote an unknown install-root file")
    if external_sentinel.read_text(encoding="utf-8") != "preserve-user-data":
        raise BuildError("desktop self-update smoke overwrote external user data")
    _run_desktop_self_update_rollback_smokes(package, root)


def _atomic_swap(staging: Path, publish: Path) -> None:
    def replace_with_retry(source: Path, destination: Path) -> None:
        last_error: OSError | None = None
        for attempt in range(8):
            try:
                os.replace(source, destination)
                return
            except PermissionError as error:
                last_error = error
                if attempt == 7:
                    break
                # Windows Defender/indexers can briefly retain a handle after
                # the final publisher exits. Keep the swap bounded and atomic
                # while allowing that transient handle to drain.
                time.sleep(min(0.15 * (attempt + 1), 0.75))
        if last_error is not None:
            raise last_error

    publish.parent.mkdir(parents=True, exist_ok=True)
    previous = publish.with_name(publish.name + ".previous")
    if previous.exists():
        shutil.rmtree(previous)
    if publish.exists():
        replace_with_retry(publish, previous)
    try:
        replace_with_retry(staging, publish)
    except OSError:
        if previous.exists():
            replace_with_retry(previous, publish)
        raise
    if previous.exists():
        shutil.rmtree(previous)


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", action="store_true")
    parser.add_argument("--skip-web", action="store_true")
    parser.add_argument("--skip-backend", action="store_true")
    parser.add_argument("--skip-desktop", action="store_true")
    parser.add_argument("--skip-sidecar", action="store_true")
    parser.add_argument("--keep-staging", action="store_true")
    parser.add_argument("--write-source-layout", action="store_true")
    return parser


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = _build_parser()
    result = parser.parse_args(argv)
    if result.write_source_layout and any(
        (
            result.release,
            result.skip_web,
            result.skip_backend,
            result.skip_desktop,
            result.skip_sidecar,
            result.keep_staging,
        )
    ):
        parser.error("--write-source-layout cannot be combined with build flags")
    if result.release:
        flags = [
            name
            for name, active in (
                ("--skip-web", result.skip_web),
                ("--skip-backend", result.skip_backend),
                ("--skip-desktop", result.skip_desktop),
                ("--skip-sidecar", result.skip_sidecar),
            )
            if active
        ]
        if flags:
            parser.error("release builds must not skip stages: " + ", ".join(flags))
    return result


def run_build(paths: RepoPaths, args: argparse.Namespace) -> int:
    errors = check_versions(paths.repo_root)
    if errors:
        raise BuildError("version mismatch: " + "; ".join(errors))
    for target in (paths.staging_root, paths.scratch_root):
        if target.exists():
            shutil.rmtree(target)
        target.mkdir(parents=True)
    stage = paths.staging_mirror()
    _build_sidecar(stage, skip=args.skip_sidecar)
    _build_web(stage, skip=args.skip_web)
    _build_backend(stage, skip=args.skip_backend)
    _build_desktop(stage, skip=args.skip_desktop)
    if not args.skip_sidecar:
        verify_sidecar_package(stage)
    stage.resources_dir.mkdir(parents=True, exist_ok=True)
    shutil.copy2(paths.repo_root / "docs" / "RECOVERY.md", stage.resources_dir / "RECOVERY.md")
    stage_workspace_contracts(stage)
    write_manifest(stage)
    write_release_manifest(stage)
    if args.release:
        run_desktop_self_update_smoke(
            stage.publish_root,
            paths.repo_root / "build" / "self-update-smoke",
            repo_root=paths.repo_root,
        )
    _atomic_swap(paths.staging_root, paths.publish_root)
    if not args.keep_staging:
        shutil.rmtree(paths.scratch_root, ignore_errors=True)
    return 0


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    paths = RepoPaths.default(Path(__file__).resolve().parents[1])
    try:
        if args.write_source_layout:
            write_source_manifest(paths)
            return 0
        return run_build(paths, args)
    except (BuildError, OSError, ValueError) as exc:
        print(f"[build_next] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
