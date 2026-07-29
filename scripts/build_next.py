#!/usr/bin/env python3
"""Build an offline VibeTable release with the pinned PocketBase sidecar."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import stat
import subprocess
import sys
import time
from dataclasses import dataclass, field, replace
from pathlib import Path
from typing import Any

try:
    from scripts._host_paths import host_assembly_name
    from scripts.versioning import (
        check_versions,
        collect_release_versions,
        read_project_version,
    )
except ModuleNotFoundError:  # pragma: no cover - direct script execution
    from _host_paths import host_assembly_name
    from versioning import (
        check_versions,
        collect_release_versions,
        read_project_version,
    )

PROTOCOL_VERSION = "1.0"
WEBVIEW2_SDK = "1.0.4078.44"
TABULATOR_VERSION = "6.5.2"
HOST_EXE_NAME = "VibeTable.Next.exe"
BACKEND_EXE_NAME = "vibetable-backend.exe"
# The published product is a Windows desktop package even when source-contract
# checks run on Linux. Keep the manifest target-platform deterministic instead
# of letting the CI host silently rewrite package paths.
SIDECAR_EXE_NAME = "vibetable-pb.exe"
PREFERRED_DOTNET = Path(r"C:\Program Files\dotnet\dotnet.exe")
BACKEND_HIDDEN_IMPORTS = (
    "pydantic",
    "pydantic.deprecated.decorator",
    "openpyxl",
    "openpyxl.workbook",
    "websockets",
)
_DEV_PACKAGES_FORBIDDEN_IN_BUNDLE = frozenset({"mypy", "numpy", "pandas", "pytest", "_pytest"})
DEV_SKIP_FLAGS = (
    "--skip-web",
    "--skip-backend",
    "--skip-desktop",
    "--skip-sidecar",
)


def release_package_name(version: str) -> str:
    return f"VibeTable.Next-v{version}-win-x64"


class BuildError(RuntimeError):
    """A release stage failed before the atomic publish swap."""


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
    logs_dir: Path = field(init=False)
    host_exe: Path = field(init=False)
    backend_dir: Path = field(init=False)
    web_grid_publish_dir: Path = field(init=False)
    sidecar_assets_dir: Path = field(init=False)
    sidecar_binary: Path = field(init=False)
    sidecar_checksum: Path = field(init=False)
    sidecar_build_info: Path = field(init=False)
    sidecar_sbom: Path = field(init=False)
    sidecar_licenses: Path = field(init=False)
    manifest_path: Path = field(init=False)
    release_manifest: Path = field(init=False)

    def __post_init__(self) -> None:
        self.resources_dir = self.publish_root / "resources"
        self.logs_dir = self.publish_root / "logs"
        self.host_exe = self.publish_root / HOST_EXE_NAME
        self.backend_dir = self.resources_dir / "backend"
        self.web_grid_publish_dir = self.resources_dir / "web-grid"
        self.sidecar_assets_dir = self.resources_dir / "sidecar"
        self.sidecar_binary = self.sidecar_assets_dir / SIDECAR_EXE_NAME
        self.sidecar_checksum = self.sidecar_assets_dir / f"{SIDECAR_EXE_NAME}.sha256"
        self.sidecar_build_info = self.sidecar_assets_dir / "build-info.json"
        self.sidecar_sbom = self.sidecar_assets_dir / "sbom.cdx.json"
        self.sidecar_licenses = self.sidecar_assets_dir / "THIRD_PARTY_LICENSES.txt"
        self.manifest_path = self.resources_dir / "publish-layout.json"
        self.release_manifest = self.publish_root / "release.json"

    @classmethod
    def default(cls, repo_root: Path) -> RepoPaths:
        root = repo_root.resolve()
        package_name = release_package_name(read_project_version(root))
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
            staging_root=root / "dist" / f"{package_name}.staging",
            scratch_root=root / "build" / "next-scratch",
            publish_root=root / "dist" / package_name,
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


def _resolve_executable(name: str) -> str:
    if name.lower() == "dotnet" and PREFERRED_DOTNET.is_file():
        return str(PREFERRED_DOTNET)
    if os.path.sep in name or (os.path.altsep and os.path.altsep in name):
        return name
    return shutil.which(name) or name


def resolve_go(repo_root: Path) -> str:
    suffix = "go.exe" if os.name == "nt" else "go"
    candidates = (
        repo_root / ".tools" / "go-full" / "go" / "bin" / suffix,
        repo_root / ".tools" / "go" / "bin" / suffix,
    )
    return next(
        (str(path) for path in candidates if path.is_file()),
        shutil.which("go") or "go",
    )


def build_npm_build_command(_paths: RepoPaths) -> list[str]:
    return ["npm", "run", "build"]


def build_dotnet_publish_command(
    paths: RepoPaths,
    output_dir: str | os.PathLike[str] | None = None,
    *,
    project: Path | None = None,
) -> list[str]:
    command = [
        "dotnet",
        "publish",
        str(project or paths.desktop_csproj),
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
                "contractVersion": versions.contract,
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
        },
        "data": {
            "rootPolicy": "automatic-documents",
            "defaultBase": "user-documents",
            "fallbackBase": "per-user-local-app-data",
            "relativePath": "VibeTableData",
            "backupRelativePath": "backups",
            "preserveOnUninstall": True,
        },
        "writable": {
            "cacheBase": "per-user-local-app-data",
            "cacheRelativePath": "VibeTable/cache",
            "runtimeBase": "per-user-local-app-data",
            "runtimeRelativePath": "VibeTable/runtime",
            "logsBase": "program-directory",
            "logsRelativePath": "logs",
        },
    }
    return json.dumps(data, ensure_ascii=False, indent=2) + "\n"


def write_manifest(paths: RepoPaths) -> Path:
    paths.manifest_path.parent.mkdir(parents=True, exist_ok=True)
    paths.manifest_path.write_text(render_manifest(paths), encoding="utf-8")
    return paths.manifest_path


def render_release_manifest(paths: RepoPaths) -> str:
    return (
        json.dumps(
            {
                "product": "VibeTable.Next",
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
        and 'the software is provided "as is"' in normalized
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


def stage_sidecar_assets(
    paths: RepoPaths,
    *,
    build_info: dict[str, Any],
    modules: list[dict[str, Any]],
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
    components = [
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
        paths.sidecar_assets_dir / "migrations" / "manifest.json",
    )
    missing = [str(path) for path in required if not path.is_file()]
    if missing:
        raise BuildError("missing sidecar package assets: " + ", ".join(missing))
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
        if f"===== {name} " not in license_bundle:
            raise BuildError(f"sidecar license bundle is missing module: {name}")


def _run(
    command: list[str],
    *,
    cwd: Path,
    capture: bool = False,
) -> subprocess.CompletedProcess[str]:
    resolved = [_resolve_executable(command[0]), *command[1:]]
    try:
        return subprocess.run(
            resolved,
            cwd=cwd,
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
    build_info_result = _run(
        [str(paths.sidecar_binary), "--build-info"],
        cwd=paths.sidecar_assets_dir,
        capture=True,
    )
    packages_result = _run(
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
    modules = _modules_from_packages(_json_stream(packages_result.stdout))
    stage_sidecar_assets(
        paths,
        build_info=json.loads(build_info_result.stdout),
        modules=modules,
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
    return parser


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = _build_parser()
    result = parser.parse_args(argv)
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
    stage.logs_dir.mkdir(parents=True, exist_ok=True)
    write_manifest(stage)
    write_release_manifest(stage)
    _atomic_swap(paths.staging_root, paths.publish_root)
    if not args.keep_staging:
        shutil.rmtree(paths.scratch_root, ignore_errors=True)
    return 0


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    paths = RepoPaths.default(Path(__file__).resolve().parents[1])
    try:
        return run_build(paths, args)
    except (BuildError, OSError, ValueError) as exc:
        print(f"[build_next] FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
