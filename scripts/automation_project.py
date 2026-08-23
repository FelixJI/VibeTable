#!/usr/bin/env python3
"""VibeTable-specific CI and release-candidate adapter.

The shared workflows call :mod:`scripts.automation`; this module keeps the
Windows desktop stack, toolchain bootstrap and release gate out of workflow
YAML.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import urllib.request
from pathlib import Path

try:
    from scripts.node_toolchain import ensure_node
    from scripts.toolchain_metadata import W64DEVKIT_DISTRIBUTION, resolve_executable
    from scripts.versioning import read_project_version
    from scripts.windows_doctor import (
        DoctorProfile,
        diagnose_windows_toolchain,
        render_report,
    )
except ModuleNotFoundError:  # pragma: no cover - direct script execution
    from node_toolchain import ensure_node
    from toolchain_metadata import W64DEVKIT_DISTRIBUTION, resolve_executable
    from versioning import read_project_version
    from windows_doctor import DoctorProfile, diagnose_windows_toolchain, render_report

REPO_ROOT = Path(__file__).resolve().parents[1]
GITHUB_REPOSITORY = "FelixJI/VibeTable"
NPM_PROJECTS = (
    Path("desktop/web-grid"),
    Path("sdk/plugin"),
    Path("examples/plugins/data-overview"),
    Path("examples/plugins/normalize-text"),
)
CI_PREPARE_MODE_ENV = "VIBETABLE_CI_PREPARE_MODE"


def _candidate_prepare_mode() -> bool:
    mode = os.environ.get(CI_PREPARE_MODE_ENV)
    if mode in {None, ""}:
        return False
    if mode == "candidate":
        return True
    raise RuntimeError(f"unsupported {CI_PREPARE_MODE_ENV}: {mode}")


def _resolve_executable(name: str, *, path: str | None = None) -> str:
    return resolve_executable(name, path=path) or name


def _run(
    *command: str,
    cwd: Path = REPO_ROOT,
    env: dict[str, str] | None = None,
) -> None:
    print(f"+ {' '.join(command)}", flush=True)
    merged_env = {**os.environ, **(env or {})}
    executable = _resolve_executable(command[0], path=merged_env.get("PATH"))
    resolved = (executable, *command[1:])
    subprocess.run(resolved, cwd=cwd, env=merged_env, check=True)


def _node_environment(extra: dict[str, str] | None = None) -> dict[str, str]:
    node = ensure_node(REPO_ROOT)
    return {
        **(extra or {}),
        "PATH": os.pathsep.join((str(node.parent), os.environ.get("PATH", ""))),
    }


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def _w64devkit_gcc() -> Path:
    return W64DEVKIT_DISTRIBUTION.gcc_path(REPO_ROOT)


def _install_w64devkit() -> None:
    if _w64devkit_gcc().is_file():
        return
    archive = REPO_ROOT / "build" / "tooling" / W64DEVKIT_DISTRIBUTION.archive_name
    archive.parent.mkdir(parents=True, exist_ok=True)
    if not archive.is_file() or _sha256(archive) != W64DEVKIT_DISTRIBUTION.archive_sha256:
        if archive.exists():
            archive.unlink()
        print(f"+ download {W64DEVKIT_DISTRIBUTION.url}", flush=True)
        with (
            urllib.request.urlopen(W64DEVKIT_DISTRIBUTION.url, timeout=120) as response,
            archive.open("wb") as output,
        ):
            shutil.copyfileobj(response, output)
    actual = _sha256(archive)
    if actual != W64DEVKIT_DISTRIBUTION.archive_sha256:
        raise RuntimeError(f"w64devkit checksum mismatch: {actual}")
    destination = REPO_ROOT / ".tools" / "w64devkit"
    destination.mkdir(parents=True, exist_ok=True)
    _run("7z", "x", str(archive), f"-o{destination}", "-y")
    if not _w64devkit_gcc().is_file():
        raise RuntimeError("w64devkit extraction did not produce gcc.exe")


def bootstrap() -> None:
    candidate_prepare = _candidate_prepare_mode()
    _run("uv", "sync", "--frozen", "--group", "dev", "--group", "build")
    node_env = _node_environment()
    projects = (Path("desktop/web-grid"),) if candidate_prepare else NPM_PROJECTS
    for project in projects:
        _run("npm", "ci", cwd=REPO_ROOT / project, env=node_env)
    _run("dotnet", "restore", "desktop/VibeTable.Desktop.sln")
    if not candidate_prepare:
        _install_w64devkit()


def contracts() -> None:
    """Verify the shared v2 wire contract through every runtime consumer."""
    _run("uv", "run", "python", "contracts/schema-v2/generate_dtos.py", "--check")
    _run("uv", "run", "python", "contracts/workbench/generate_dtos.py", "--check")
    _run("uv", "run", "python", "contracts/v2/generate_product_rpc_catalog.py", "--check")
    _run("uv", "run", "python", "contracts/v2/generate_rpc_catalog.py", "--check")
    _run(
        "uv",
        "run",
        "python",
        "scripts/generate_product_e2e_capability_index.py",
        "--check",
    )
    _run(
        "uv",
        "run",
        "python",
        "-m",
        "pytest",
        "tests/contract/test_v2_contracts.py",
        "tests/contract/test_product_contracts.py",
        "tests/contract/test_schema_v2_dto_codegen.py",
        "tests/contract/test_workbench_dto_codegen.py",
        "tests/contract/test_product_e2e_capability_index.py",
        "tests/backend/contracts/test_workspace_v2_models.py",
        "-q",
        "--no-cov",
    )
    _run(
        "npm",
        "run",
        "test",
        "--",
        "src/contracts/workspaceV2.test.ts",
        "src/contracts/workspaceV2Bridge.test.ts",
        "src/contracts/productContractV2.test.ts",
        cwd=REPO_ROOT / "desktop" / "web-grid",
        env=_node_environment(),
    )
    _run(
        "dotnet",
        "test",
        "desktop/tests/VibeTable.Contracts.Tests/VibeTable.Contracts.Tests.csproj",
        "--configuration",
        "Release",
        "--no-restore",
    )
    _run(
        "go",
        "test",
        "./internal/contracts/v2",
        "./internal/contracts",
        "./internal/contracts/schemav2wire",
        "./internal/contracts/workbench",
        "./internal/protocolv2",
        cwd=REPO_ROOT / "sidecar",
    )


def quality() -> None:
    if _candidate_prepare_mode():
        print(
            "+ defer quality to immutable-candidate CI shards",
            flush=True,
        )
        return
    contracts()
    commands = (
        ("uv", "run", "python", "scripts/release.py", "--check"),
        ("uv", "run", "python", "qa/version_check.py"),
        ("uv", "run", "python", "qa/package_check.py"),
        ("uv", "run", "ruff", "format", "--check", "."),
        ("uv", "run", "ruff", "check", "."),
        ("uv", "run", "python", "-m", "pyright", "backend"),
        ("uv", "run", "python", "-m", "mypy", "backend"),
        (
            "uv",
            "run",
            "python",
            "-m",
            "pytest",
            "--ignore=tests/e2e/test_next_readonly_smoke.py",
            "--junitxml=build/automation/python-junit.xml",
            "--cov-report=xml:build/automation/python-coverage.xml",
        ),
    )
    for command in commands:
        _run(*command)
    node_env = _node_environment()
    for project in NPM_PROJECTS:
        _run("npm", "run", "typecheck", cwd=REPO_ROOT / project, env=node_env)
        package = project / "package.json"
        package_text = (REPO_ROOT / package).read_text(encoding="utf-8")
        if '"test"' in package_text:
            _run("npm", "run", "test", cwd=REPO_ROOT / project, env=node_env)
        if '"build"' in package_text:
            _run("npm", "run", "build", cwd=REPO_ROOT / project, env=node_env)
    _run("uv", "run", "python", "qa/go_format_check.py")
    _run("go", "vet", "./...", cwd=REPO_ROOT / "sidecar")
    _run("uv", "run", "python", "qa/next.py", "--stage", "go-test")
    _run("uv", "run", "python", "qa/next.py", "--stage", "go-coverage")
    sidecar_output = REPO_ROOT / "build/automation/sidecar/vibetable-pb.exe"
    sidecar_output.parent.mkdir(parents=True, exist_ok=True)
    _run(
        "go",
        "build",
        "-trimpath",
        "-buildvcs=true",
        "-o",
        str(sidecar_output),
        "./cmd/vibetable-pb",
        cwd=REPO_ROOT / "sidecar",
    )
    _run("uv", "run", "python", "qa/next.py", "--stage", "dotnet")


def _artifacts_dir() -> Path:
    raw = os.environ.get("AUTOMATION_ARTIFACTS_DIR")
    if not raw:
        raise RuntimeError("AUTOMATION_ARTIFACTS_DIR is required")
    artifacts = Path(raw)
    if not artifacts.is_absolute():
        artifacts = REPO_ROOT / artifacts
    artifacts.mkdir(parents=True, exist_ok=True)
    return artifacts.resolve()


def build_candidate() -> None:
    version = read_project_version(REPO_ROOT)
    artifacts = _artifacts_dir()
    archive = artifacts / f"VibeTable-v{version}-win-x64.zip"
    _run(
        "uv",
        "run",
        "python",
        "scripts/build_next.py",
        "--release",
        env=_node_environment(),
    )
    _run(
        "uv",
        "run",
        "python",
        "qa/release_candidate.py",
        "create",
        "--package-root",
        "dist/VibeTable.Next",
        "--archive",
        str(archive),
    )
    _write_build_identity(artifacts / "build-identity.json", version, archive)
    _write_spdx(artifacts / "SBOM.spdx.json", version, archive)


def pr_e2e() -> None:
    """Build a package, run four tagged journeys, and qualify a real quick corpus."""
    _run(
        "uv",
        "run",
        "python",
        "scripts/build_next.py",
        env=_node_environment(),
    )
    _run(
        "go",
        "run",
        "./cmd/workbench-qualification",
        "--profile",
        "pr",
        "--records",
        "1000",
        "--files",
        "100",
        "--logical-bytes",
        str(64 << 20),
        "--report",
        "../build/qa/workbench-qualification-pr.json",
        "--work-root",
        "../build/qa/workbench-qualification-runs",
        cwd=REPO_ROOT / "sidecar",
    )
    _run(
        "uv",
        "run",
        "python",
        "qa/product_acceptance.py",
        "--package-root",
        "dist/VibeTable.Next",
        "--evidence-root",
        "build/qa/pr-product-e2e",
        "--capability",
        "release.smoke",
        env=_node_environment(),
    )


def _spdx_id(value: str) -> str:
    normalized = re.sub(r"[^A-Za-z0-9.-]+", "-", value).strip("-.")
    return normalized or "package"


def _write_build_identity(output: Path, version: str, archive: Path) -> None:
    package_identity_path = REPO_ROOT / "dist/VibeTable.Next/release.json"
    package_identity = json.loads(package_identity_path.read_text(encoding="utf-8"))
    identity = {
        "schema_version": 1,
        "project": {
            "component": "vibetable",
            "repository": GITHUB_REPOSITORY,
            "version": version,
            "source_sha": os.environ.get("AUTOMATION_SOURCE_SHA", "local"),
        },
        "build": {
            "archive": archive.name,
            "archive_sha256": _sha256(archive),
            "package_identity": package_identity,
            "package_identity_sha256": _sha256(package_identity_path),
        },
    }
    output.write_text(
        json.dumps(identity, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )


def _write_spdx(output: Path, version: str, archive: Path) -> None:
    source = REPO_ROOT / "dist/VibeTable.Next/resources/sidecar/sbom.cdx.json"
    cyclonedx = json.loads(source.read_text(encoding="utf-8"))
    packages: list[dict[str, object]] = []
    relationships: list[dict[str, str]] = []
    used: set[str] = set()
    for index, component in enumerate(cyclonedx.get("components", []), start=1):
        name = str(component.get("name") or f"component-{index}")
        identifier = f"SPDXRef-{_spdx_id(name)}"
        if identifier in used:
            identifier = f"{identifier}-{index}"
        used.add(identifier)
        package: dict[str, object] = {
            "SPDXID": identifier,
            "name": name,
            "versionInfo": str(component.get("version") or "NOASSERTION"),
            "downloadLocation": "NOASSERTION",
            "filesAnalyzed": False,
            "licenseConcluded": "NOASSERTION",
            "licenseDeclared": "NOASSERTION",
            "copyrightText": "NOASSERTION",
        }
        packages.append(package)
        relationships.append(
            {
                "spdxElementId": "SPDXRef-Package-VibeTable",
                "relationshipType": "CONTAINS",
                "relatedSpdxElement": identifier,
            }
        )
    archive_digest = _sha256(archive)
    packages.insert(
        0,
        {
            "SPDXID": "SPDXRef-Package-VibeTable",
            "name": "VibeTable",
            "versionInfo": version,
            "downloadLocation": "NOASSERTION",
            "filesAnalyzed": False,
            "licenseConcluded": "NOASSERTION",
            "licenseDeclared": "MIT",
            "copyrightText": "NOASSERTION",
            "checksums": [{"algorithm": "SHA256", "checksumValue": archive_digest}],
        },
    )
    relationships.insert(
        0,
        {
            "spdxElementId": "SPDXRef-DOCUMENT",
            "relationshipType": "DESCRIBES",
            "relatedSpdxElement": "SPDXRef-Package-VibeTable",
        },
    )
    document = {
        "spdxVersion": "SPDX-2.3",
        "dataLicense": "CC0-1.0",
        "SPDXID": "SPDXRef-DOCUMENT",
        "name": f"VibeTable-{version}",
        "documentNamespace": (
            f"https://github.com/{GITHUB_REPOSITORY}/releases/v{version}/sbom-{archive_digest}"
        ),
        "creationInfo": {
            "created": "1980-01-01T00:00:00Z",
            "creators": ["Tool: scripts/automation_project.py"],
        },
        "packages": packages,
        "relationships": relationships,
    }
    output.write_text(
        json.dumps(document, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )


def _verify_release_metadata(artifacts: Path, version: str, archive: Path) -> None:
    expected = {
        archive.name,
        f"{archive.name}.sha256",
        "build-identity.json",
        "SBOM.spdx.json",
    }
    actual = {path.name for path in artifacts.iterdir() if path.is_file()}
    if actual != expected:
        raise RuntimeError(f"release metadata assets differ: {sorted(actual ^ expected)}")
    archive_sha256 = _sha256(archive)
    sidecar = artifacts / f"{archive.name}.sha256"
    if sidecar.read_text(encoding="utf-8").strip() != f"{archive_sha256}  {archive.name}":
        raise RuntimeError("release archive checksum sidecar mismatch")

    package_identity_path = REPO_ROOT / "dist/VibeTable.Next/release.json"
    identity = json.loads((artifacts / "build-identity.json").read_text(encoding="utf-8"))
    project = identity.get("project", {})
    build = identity.get("build", {})
    if project != {
        "component": "vibetable",
        "repository": GITHUB_REPOSITORY,
        "version": version,
        "source_sha": os.environ.get("AUTOMATION_SOURCE_SHA", "local"),
    }:
        raise RuntimeError("build identity project/source mismatch")
    if (
        build.get("archive") != archive.name
        or build.get("archive_sha256") != archive_sha256
        or build.get("package_identity")
        != json.loads(package_identity_path.read_text(encoding="utf-8"))
        or build.get("package_identity_sha256") != _sha256(package_identity_path)
    ):
        raise RuntimeError("build identity does not bind the immutable package")

    sbom = json.loads((artifacts / "SBOM.spdx.json").read_text(encoding="utf-8"))
    if (
        sbom.get("spdxVersion") != "SPDX-2.3"
        or sbom.get("dataLicense") != "CC0-1.0"
        or not sbom.get("documentNamespace")
        or not sbom.get("creationInfo", {}).get("created")
    ):
        raise RuntimeError("SBOM is not a complete SPDX 2.3 document")
    if not any(
        package.get("name") == "VibeTable"
        and package.get("versionInfo") == version
        and {"algorithm": "SHA256", "checksumValue": archive_sha256} in package.get("checksums", [])
        for package in sbom.get("packages", [])
    ):
        raise RuntimeError("SBOM does not bind the immutable release archive")


def release_smoke() -> None:
    version = read_project_version(REPO_ROOT)
    artifacts = _artifacts_dir()
    archive = artifacts / f"VibeTable-v{version}-win-x64.zip"
    _verify_release_metadata(artifacts, version, archive)
    _install_w64devkit()
    _run(
        "uv",
        "run",
        "python",
        "qa/next.py",
        "--ci",
        "--package-root",
        "dist/VibeTable.Next",
        "--package-archive",
        str(archive),
        "--json-report",
        "build/automation/vibetable-release-eligibility.json",
        env=_node_environment({"VIBETABLE_TEST_WINDOWS_CREDENTIAL_MANAGER": "1"}),
    )


def _prepare_smoke_lane(lane: str) -> None:
    if lane == "core":
        bootstrap()
    elif lane in {"race-a", "race-b"}:
        _install_w64devkit()
    elif lane == "resilience":
        _run("uv", "sync", "--frozen", "--group", "dev", "--group", "build")
        _run(
            "npm",
            "ci",
            cwd=REPO_ROOT / "desktop" / "web-grid",
            env=_node_environment(),
        )
    elif lane != "release":
        raise RuntimeError(f"unknown release smoke lane: {lane}")


def release_smoke_lane(lane: str, json_report: Path) -> None:
    version = read_project_version(REPO_ROOT)
    artifacts = _artifacts_dir()
    archive = artifacts / f"VibeTable-v{version}-win-x64.zip"
    _verify_release_metadata(artifacts, version, archive)
    _prepare_smoke_lane(lane)
    python = ("uv", "run", "python") if lane in {"core", "resilience"} else (sys.executable,)
    smoke_env = {"VIBETABLE_TEST_WINDOWS_CREDENTIAL_MANAGER": "1"}
    if lane in {"core", "resilience"}:
        smoke_env = _node_environment(smoke_env)
    _run(
        *python,
        "qa/next.py",
        "--lane",
        lane,
        "--package-root",
        "dist/VibeTable.Next",
        "--package-archive",
        str(archive),
        "--json-report",
        str(json_report),
        env=smoke_env,
    )


def aggregate_release_smoke(reports_dir: Path) -> None:
    version = read_project_version(REPO_ROOT)
    artifacts = _artifacts_dir()
    archive = artifacts / f"VibeTable-v{version}-win-x64.zip"
    _verify_release_metadata(artifacts, version, archive)
    command = [
        sys.executable,
        "qa/release_eligibility.py",
        "--reports-dir",
        str(reports_dir),
        "--package-root",
        "dist/VibeTable.Next",
        "--package-archive",
        str(archive),
        "--json-report",
        "build/automation/vibetable-release-eligibility.json",
    ]
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        command.extend(("--github-summary", summary))
    _run(*command)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "command",
        choices=(
            "bootstrap",
            "doctor",
            "contracts",
            "quality",
            "pr-e2e",
            "build",
            "smoke",
            "smoke-lane",
            "smoke-aggregate",
        ),
    )
    parser.add_argument(
        "--profile",
        choices=tuple(profile.value for profile in DoctorProfile),
    )
    parser.add_argument(
        "--lane",
        choices=("core", "race-a", "race-b", "resilience", "release"),
    )
    parser.add_argument("--json-report", type=Path)
    parser.add_argument("--reports-dir", type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = _parser()
    args = parser.parse_args(argv)
    command = args.command
    actions = {
        "bootstrap": bootstrap,
        "contracts": contracts,
        "quality": quality,
        "pr-e2e": pr_e2e,
        "build": build_candidate,
        "smoke": release_smoke,
    }
    try:
        if command == "doctor":
            if args.profile is None:
                parser.error("doctor requires --profile")
            report = diagnose_windows_toolchain(REPO_ROOT, DoctorProfile(args.profile))
            print(render_report(report), end="")
            return 0 if report.passed else 1
        if args.profile is not None:
            parser.error("--profile is only valid with doctor")
        if command == "smoke-lane":
            if args.lane is None or args.json_report is None:
                parser.error("smoke-lane requires --lane and --json-report")
            release_smoke_lane(args.lane, args.json_report)
        elif command == "smoke-aggregate":
            if args.reports_dir is None:
                parser.error("smoke-aggregate requires --reports-dir")
            aggregate_release_smoke(args.reports_dir)
        else:
            actions[command]()
    except (OSError, RuntimeError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"[FAIL] VibeTable automation: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
