#!/usr/bin/env python3
"""Cross-stack CI gate with real PocketBase sidecar verification."""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import xml.etree.ElementTree as ET
from concurrent.futures import ThreadPoolExecutor
from contextlib import suppress
from dataclasses import asdict, dataclass
from datetime import UTC, datetime
from pathlib import Path
from threading import Event

try:
    from qa import handoff as handoff_gate
    from qa import release_candidate
    from qa.release_eligibility import LANE_STAGES, RACE_LANES, REQUIRED_STAGES
except ModuleNotFoundError:  # pragma: no cover - direct ``python qa/next.py``
    import handoff as handoff_gate  # type: ignore[no-redef]
    import release_candidate  # type: ignore[no-redef]
    from release_eligibility import (  # type: ignore[no-redef]
        LANE_STAGES,
        RACE_LANES,
        REQUIRED_STAGES,
    )

REPO_ROOT = Path(__file__).resolve().parents[1]
SIDECAR_DIR = REPO_ROOT / "sidecar"
WEB_GRID_DIR = REPO_ROOT / "desktop" / "web-grid"
DESKTOP_SLN = REPO_ROOT / "desktop" / "VibeTable.Desktop.sln"
PROJECT_CONFIG = REPO_ROOT / ".ci" / "project.json"
DEV_LAUNCHER = REPO_ROOT / "scripts" / "dev.py"
SIDECAR_MATRIX = REPO_ROOT / "tests" / "integration" / "packaged_sidecar_matrix.py"
FAULT_INJECTION = REPO_ROOT / "qa" / "fault_injection.py"
GO_FORMAT_CHECK = REPO_ROOT / "qa" / "go_format_check.py"
E2E_SMOKE = REPO_ROOT / "tests" / "e2e" / "test_next_readonly_smoke.py"
UPGRADE_SMOKE = REPO_ROOT / "tests" / "integration" / "test_upgrade_activation_smoke.py"

STAGES = REQUIRED_STAGES
DEFAULT_STAGE_TIMEOUT_SECONDS = 15 * 60
STAGE_TIMEOUT_SECONDS = {
    "fault-injection": 30 * 60,
    "product-e2e": 30 * 60,
}
RACE_COMMAND_TIMEOUT_SECONDS = 7 * 60
RACE_LONG_COMMAND_TIMEOUT_SECONDS = 16 * 60
RACE_LONG_TEST_TIMEOUT = "15m"
RACE_BINARY_DIR = REPO_ROOT / "build" / "qa" / "race-tests"
RACE_PACKAGE_WORKERS = 3
# Candidate-bound timings from successful PR run 31102199882. They only guide
# scheduling; new packages safely fall back to discovered test counts.
RACE_PACKAGE_SECONDS = {
    "github.com/vibetable/vibetable/sidecar/cmd/vibetable-pb": 50.641,
    "github.com/vibetable/vibetable/sidecar/internal/app": 41.922,
    "github.com/vibetable/vibetable/sidecar/internal/attachments": 9.610,
    "github.com/vibetable/vibetable/sidecar/internal/audit": 9.687,
    "github.com/vibetable/vibetable/sidecar/internal/auditledger": 11.735,
    "github.com/vibetable/vibetable/sidecar/internal/auth": 4.828,
    "github.com/vibetable/vibetable/sidecar/internal/autodateobs": 0.641,
    "github.com/vibetable/vibetable/sidecar/internal/backupreceipt": 7.219,
    "github.com/vibetable/vibetable/sidecar/internal/buildinfo": 6.407,
    "github.com/vibetable/vibetable/sidecar/internal/computed": 0.812,
    "github.com/vibetable/vibetable/sidecar/internal/config": 11.234,
    "github.com/vibetable/vibetable/sidecar/internal/conflict": 21.250,
    "github.com/vibetable/vibetable/sidecar/internal/contracts": 11.141,
    "github.com/vibetable/vibetable/sidecar/internal/contracts/v2": 9.640,
    "github.com/vibetable/vibetable/sidecar/internal/fieldchange": 185.797,
    "github.com/vibetable/vibetable/sidecar/internal/fieldprojection": 7.000,
    "github.com/vibetable/vibetable/sidecar/internal/fieldresource": 0.875,
    "github.com/vibetable/vibetable/sidecar/internal/fieldvalue": 12.797,
    "github.com/vibetable/vibetable/sidecar/internal/filehistory": 236.110,
    "github.com/vibetable/vibetable/sidecar/internal/formula": 28.422,
    "github.com/vibetable/vibetable/sidecar/internal/health": 5.172,
    "github.com/vibetable/vibetable/sidecar/internal/importvalue": 7.797,
    "github.com/vibetable/vibetable/sidecar/internal/jobs": 13.812,
    "github.com/vibetable/vibetable/sidecar/internal/launch": 8.094,
    "github.com/vibetable/vibetable/sidecar/internal/lookup": 15.891,
    "github.com/vibetable/vibetable/sidecar/internal/metadata": 10.906,
    "github.com/vibetable/vibetable/sidecar/internal/mutation": 28.031,
    "github.com/vibetable/vibetable/sidecar/internal/objectrepo": 122.390,
    "github.com/vibetable/vibetable/sidecar/internal/productrow": 5.469,
    "github.com/vibetable/vibetable/sidecar/internal/protocolv2": 8.203,
    "github.com/vibetable/vibetable/sidecar/internal/query": 25.313,
    "github.com/vibetable/vibetable/sidecar/internal/queryschema": 10.406,
    "github.com/vibetable/vibetable/sidecar/internal/realtime": 9.844,
    "github.com/vibetable/vibetable/sidecar/internal/relation": 9.235,
    "github.com/vibetable/vibetable/sidecar/internal/replica": 35.516,
    "github.com/vibetable/vibetable/sidecar/internal/restore": 8.547,
    "github.com/vibetable/vibetable/sidecar/internal/retention": 20.906,
    "github.com/vibetable/vibetable/sidecar/internal/schema": 29.812,
    "github.com/vibetable/vibetable/sidecar/internal/schema/v2": 25.312,
    "github.com/vibetable/vibetable/sidecar/internal/schemaapi": 9.422,
    "github.com/vibetable/vibetable/sidecar/internal/snapshot": 22.078,
    "github.com/vibetable/vibetable/sidecar/internal/snapshotpkg": 33.968,
    "github.com/vibetable/vibetable/sidecar/internal/startup": 9.032,
    "github.com/vibetable/vibetable/sidecar/internal/workspacedb": 11.765,
    "github.com/vibetable/vibetable/sidecar/internal/workspacev2": 741.750,
    "github.com/vibetable/vibetable/sidecar/internal/writecoordinator": 17.344,
    "github.com/vibetable/vibetable/sidecar/migrations": 99.656,
    "github.com/vibetable/vibetable/sidecar/tests/integration": 1566.266,
}
RACE_SPLIT_PACKAGES = frozenset({"github.com/vibetable/vibetable/sidecar/tests/integration"})
RACE_LONG_TEST_WEIGHT = 10.0
RACE_LONG_TESTS = frozenset(
    {
        "TestFormulaBackfillScaleCancelsResumesWithoutDuplicateAudit",
        "TestMutationKernelOneThousandOperationsCommitOrFullyRollback",
        "TestQueryPortPagesFiltersAndSortsTwentyFiveThousandRows",
    }
)
TIMEOUT_RETURNCODE = 124
WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS = 3
QA_TEMP_PARENT_ENV = "VIBETABLE_QA_TEMP_PARENT"
QA_RUN_TEMP_DIR: Path | None = None
PRODUCT_E2E_EVIDENCE_FILES = (
    "host-stderr.log",
    "host-stdout.log",
    "launch.json",
    "process-network-observations.json",
    "runner-stderr.log",
    "runner-stdout.log",
)
PRODUCT_E2E_RUNTIME_LOGS = ("backend.log", "pocketbase.log")


def _qa_temp_dir() -> Path:
    """Return this invocation's secure, short-lived QA workspace."""

    global QA_RUN_TEMP_DIR
    if QA_RUN_TEMP_DIR is None:
        # Several real tests add pytest, backup, timestamp and previous-install
        # segments below this root. A short system location avoids legacy
        # Windows MAX_PATH failures; mkdtemp also creates it exclusively. An
        # outer QA process exports its original parent so nested gate tests
        # create a sibling instead of recursively nesting under TMP.
        parent = Path(os.environ.get(QA_TEMP_PARENT_ENV, tempfile.gettempdir())).resolve()
        if not parent.is_dir():
            raise RuntimeError(f"QA temporary parent does not exist: {parent}")
        QA_RUN_TEMP_DIR = Path(tempfile.mkdtemp(prefix="vtqa-", dir=parent))
    return QA_RUN_TEMP_DIR


def _cleanup_qa_temp_dir() -> None:
    global QA_RUN_TEMP_DIR
    if QA_RUN_TEMP_DIR is None:
        return
    shutil.rmtree(QA_RUN_TEMP_DIR)
    QA_RUN_TEMP_DIR = None


@dataclass
class StageResult:
    stage: str
    command: list[str]
    returncode: int
    elapsed: float
    stdout: str
    stderr: str
    cwd: str
    webview2_evidence: str | None = None

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass(frozen=True)
class RacePackagePlan:
    package: str
    commands: list[tuple[list[str], int, str]]
    compile_command: list[str] | None
    binary: Path | None


def _select_race_package_plans(
    plans: list[RacePackagePlan],
    shard_index: int,
    shard_count: int,
    weights: dict[str, float],
) -> list[RacePackagePlan]:
    if shard_count < 1 or shard_index not in range(shard_count):
        raise ValueError("invalid race shard coordinates")
    work_items: list[tuple[float, str, int | None, RacePackagePlan]] = []
    for plan in plans:
        package_weight = weights.get(plan.package, float(max(1, len(plan.commands))))
        if plan.package not in RACE_SPLIT_PACKAGES or len(plan.commands) < 2:
            work_items.append((package_weight, plan.package, None, plan))
            continue
        command_weights = []
        for command, _timeout, _cwd in plan.commands:
            test_name = next(
                (
                    argument.removeprefix("-test.run=^").removesuffix("$")
                    for argument in command
                    if argument.startswith("-test.run=^")
                ),
                "",
            )
            command_weights.append(RACE_LONG_TEST_WEIGHT if test_name in RACE_LONG_TESTS else 1.0)
        weight_total = sum(command_weights)
        for command_index, command_weight in enumerate(command_weights):
            work_items.append(
                (
                    package_weight * command_weight / weight_total,
                    plan.package,
                    command_index,
                    plan,
                )
            )

    assignments: list[list[tuple[float, str, int | None, RacePackagePlan]]] = [
        [] for _index in range(shard_count)
    ]
    totals = [0.0] * shard_count
    ordered = sorted(
        work_items,
        key=lambda item: (-item[0], item[1], -1 if item[2] is None else item[2]),
    )
    for item in ordered:
        target = min(range(shard_count), key=lambda index: (totals[index], index))
        assignments[target].append(item)
        totals[target] += item[0]

    grouped: dict[str, tuple[RacePackagePlan, list[int] | None]] = {}
    for _weight, package, command_index, plan in assignments[shard_index]:
        if command_index is None:
            grouped[package] = (plan, None)
            continue
        current = grouped.get(package)
        indexes = [] if current is None or current[1] is None else current[1]
        indexes.append(command_index)
        grouped[package] = (plan, indexes)

    selected: list[RacePackagePlan] = []
    for package in sorted(grouped):
        plan, indexes = grouped[package]
        commands = (
            plan.commands
            if indexes is None
            else [plan.commands[index] for index in sorted(indexes)]
        )
        selected.append(
            RacePackagePlan(
                package=plan.package,
                commands=commands,
                compile_command=plan.compile_command,
                binary=plan.binary,
            )
        )
    return selected


def _resolve(name: str) -> str:
    tool_roots = [REPO_ROOT]
    git_marker = REPO_ROOT / ".git"
    if git_marker.is_file():
        try:
            marker = git_marker.read_text(encoding="utf-8").strip()
            if marker.startswith("gitdir:"):
                common_root = Path(marker.removeprefix("gitdir:").strip()).resolve().parents[2]
                tool_roots.append(common_root)
        except (OSError, IndexError):
            pass
    if name == "go":
        suffix = "go.exe" if os.name == "nt" else "go"
        for tool_root in tool_roots:
            candidate = tool_root / ".tools" / "go-full" / "go" / "bin" / suffix
            if candidate.is_file():
                return str(candidate)
    if name == "gcc" and os.name == "nt":
        for tool_root in tool_roots:
            for candidate in (
                tool_root / ".tools" / "w64devkit" / "bin" / "gcc.exe",
                tool_root / ".tools" / "w64devkit" / "w64devkit" / "bin" / "gcc.exe",
            ):
                if candidate.is_file():
                    return str(candidate)
    if name == "dotnet":
        for tool_root in tool_roots:
            bundled = tool_root / ".tools" / "dotnet" / "dotnet.exe"
            if bundled.is_file():
                return str(bundled)
        system = Path(r"C:\Program Files\dotnet\dotnet.exe")
        if system.is_file():
            return str(system)
    return shutil.which(name) or name


def dotnet_coverage_properties(config_path: Path = PROJECT_CONFIG) -> dict[str, int | str]:
    """Validate the per-assembly Coverlet inventory and emit its MSBuild properties."""

    try:
        payload = json.loads(config_path.read_text(encoding="utf-8"))
        projects = payload["quality"]["dotnet_coverage"]["projects"]
    except (OSError, json.JSONDecodeError, KeyError, TypeError) as exc:
        raise ValueError(f"invalid dotnet coverage configuration: {exc}") from exc
    if not isinstance(projects, dict) or not projects:
        raise ValueError("dotnet coverage projects must be a non-empty object")

    properties: dict[str, int | str] = {}
    prefixes: set[str] = set()
    source_projects: set[Path] = set()
    test_projects: set[Path] = set()
    source_root = (REPO_ROOT / "desktop" / "src").resolve()
    tests_root = (REPO_ROOT / "desktop" / "tests").resolve()
    active_coverage_condition = "'$(CollectCoverage)' == 'true'"
    solution_text = DESKTOP_SLN.read_text(encoding="utf-8")
    solution_projects = {
        (DESKTOP_SLN.parent / project).resolve()
        for project in re.findall(
            r'^Project\("[^"]+"\) = "[^"]+", "([^"]+\.csproj)",',
            solution_text,
            re.MULTILINE,
        )
    }
    for assembly, raw in projects.items():
        if not isinstance(assembly, str) or not assembly or not isinstance(raw, dict):
            raise ValueError("dotnet coverage project entries must be named objects")
        prefix = raw.get("msbuild_prefix")
        source_project = raw.get("source_project")
        test_project = raw.get("test_project")
        minimum = raw.get("minimum")
        generated_source_exclusion = raw.get("generated_source_exclusion")
        generated_source_files = raw.get("generated_source_files")
        allowed_entry_fields = {
            "source_project",
            "test_project",
            "msbuild_prefix",
            "minimum",
            "generated_source_exclusion",
            "generated_source_files",
        }
        if not set(raw) <= allowed_entry_fields:
            raise ValueError(f"unknown dotnet coverage configuration for {assembly}")
        if not isinstance(prefix, str) or not re.fullmatch(r"[A-Za-z][A-Za-z0-9]*", prefix):
            raise ValueError(f"invalid dotnet coverage msbuild_prefix for {assembly}")
        if prefix in prefixes:
            raise ValueError(f"duplicate dotnet coverage msbuild_prefix: {prefix}")
        prefixes.add(prefix)
        if not isinstance(source_project, str) or not source_project:
            raise ValueError(f"invalid dotnet coverage source_project for {assembly}")
        if not isinstance(test_project, str) or not test_project:
            raise ValueError(f"invalid dotnet coverage test_project for {assembly}")
        source_path = (REPO_ROOT / source_project).resolve()
        project_path = (REPO_ROOT / test_project).resolve()
        if (
            not source_path.is_relative_to(source_root)
            or source_path.suffix != ".csproj"
            or not source_path.is_file()
            or source_path in source_projects
        ):
            raise ValueError(f"invalid dotnet coverage source_project for {assembly}")
        source_root_xml = ET.parse(source_path).getroot()
        assembly_name = source_root_xml.findtext(".//AssemblyName") or source_path.stem
        if assembly_name != assembly:
            raise ValueError(f"dotnet coverage source_project must match {assembly}")
        source_projects.add(source_path)
        exclusion_property: str | None = None
        has_generated_exclusion = "generated_source_exclusion" in raw
        has_generated_files = "generated_source_files" in raw
        if has_generated_exclusion != has_generated_files:
            raise ValueError(f"invalid generated source exclusion for {assembly}")
        if has_generated_exclusion:
            expected_exclusion = f"**/{source_path.parent.name}/Generated/*.g.cs"
            generated_dir = source_path.parent / "Generated"
            generated_files = {path.resolve() for path in generated_dir.glob("*.g.cs")}
            all_generated_files = {
                path.resolve()
                for path in source_path.parent.rglob("*.g.cs")
                if not {"bin", "obj"}.intersection(path.relative_to(source_path.parent).parts)
            }
            valid_source_files = (
                isinstance(generated_source_files, list)
                and bool(generated_source_files)
                and all(isinstance(path, str) for path in generated_source_files)
                and len(generated_source_files) == len(set(generated_source_files))
                and generated_source_files == sorted(generated_source_files)
                and all(
                    path.startswith("Generated/")
                    and path.count("/") == 1
                    and path.endswith(".g.cs")
                    and not any(token in path for token in ("..", "\\"))
                    for path in generated_source_files
                )
            )
            declared_generated_files = (
                {(source_path.parent / path).resolve() for path in generated_source_files}
                if valid_source_files
                else set()
            )
            if (
                not isinstance(generated_source_exclusion, str)
                or generated_source_exclusion != expected_exclusion
                or any(token in generated_source_exclusion for token in (",", ";", "..", "\\"))
                or not generated_files
                or generated_files != all_generated_files
                or generated_files != declared_generated_files
            ):
                raise ValueError(f"invalid generated source exclusion for {assembly}")
            exclusion_property = f"{prefix}CoverageExcludeByFile"
        if (
            not project_path.is_relative_to(tests_root)
            or project_path.suffix != ".csproj"
            or not project_path.is_file()
            or project_path in test_projects
        ):
            raise ValueError(f"invalid dotnet coverage test_project for {assembly}")
        test_projects.add(project_path)
        for bound_path in (source_path, project_path):
            if bound_path not in solution_projects:
                raise ValueError(f"dotnet coverage project is missing from solution: {bound_path}")
        if not isinstance(minimum, dict) or set(minimum) != {"line", "branch"}:
            raise ValueError(f"dotnet coverage minimum must declare line and branch for {assembly}")

        project_root = ET.parse(project_path).getroot()
        coverlet_bindings = [
            (group, node)
            for group in project_root.findall("ItemGroup")
            for node in group.findall("PackageReference")
            if node.attrib.get("Include", "").casefold() == "coverlet.msbuild"
        ]
        if len(coverlet_bindings) != 1:
            raise ValueError(
                f"dotnet coverage test_project must use coverlet.msbuild once: {assembly}"
            )
        coverlet_group, coverlet_reference = coverlet_bindings[0]
        include_assets_values = {
            value.strip()
            for value in (
                coverlet_reference.attrib.get("IncludeAssets"),
                coverlet_reference.findtext("IncludeAssets"),
            )
            if value and value.strip()
        }
        exclude_assets_values = {
            value.strip()
            for value in (
                coverlet_reference.attrib.get("ExcludeAssets"),
                coverlet_reference.findtext("ExcludeAssets"),
            )
            if value and value.strip()
        }
        include_assets = next(iter(include_assets_values), None)
        active_assets = (
            {asset.strip().casefold() for asset in include_assets.split(";")}
            if include_assets
            else None
        )
        if (
            coverlet_group.attrib.get("Condition")
            or coverlet_reference.attrib.get("Condition")
            or len(include_assets_values) > 1
            or exclude_assets_values
            or (active_assets is not None and "build" not in active_assets)
        ):
            raise ValueError(f"coverlet.msbuild reference must be active for {assembly}")
        project_references = {
            (project_path.parent / node.attrib["Include"]).resolve()
            for node in project_root.findall(".//ProjectReference")
            if "Include" in node.attrib
        }
        if source_path not in project_references:
            raise ValueError(
                f"dotnet coverage test_project must reference source_project: {assembly}"
            )

        coverage_groups = [
            group
            for group in project_root.findall("PropertyGroup")
            if "CollectCoverage" in group.attrib.get("Condition", "")
        ]
        if len(coverage_groups) != 1:
            raise ValueError(
                f"dotnet coverage test_project must have one coverage group: {assembly}"
            )
        if coverage_groups[0].attrib.get("Condition") != active_coverage_condition:
            raise ValueError(
                f"dotnet coverage group must run only when CollectCoverage is true: {assembly}"
            )
        coverage_group = coverage_groups[0]
        coverage_values = {child.tag: child.text for child in coverage_group}
        forbidden = {
            child.tag
            for group in project_root.findall("PropertyGroup")
            for child in group
            if child.tag == "MergeWith"
            or child.tag == "SkipAutoProps"
            or (
                child.tag.startswith("Exclude")
                and not (
                    child.tag == "ExcludeByFile"
                    and exclusion_property is not None
                    and group is coverage_group
                )
            )
        }
        if forbidden:
            raise ValueError(
                f"dotnet coverage exclusions are not allowed for {assembly}: {forbidden}"
            )

        include_property = f"{prefix}CoverageInclude"
        line_property = f"{prefix}LineCoverageMinimum"
        branch_property = f"{prefix}BranchCoverageMinimum"
        expected_values = {
            "Include": f"$({include_property})",
            "Threshold": f"$({line_property}),$({branch_property})",
            "ThresholdType": "line,branch",
            "ThresholdStat": "total",
        }
        if exclusion_property is not None:
            expected_values["ExcludeByFile"] = f"$({exclusion_property})"
        if coverage_values != expected_values:
            raise ValueError(f"dotnet coverage adapter does not match inventory: {assembly}")

        target = project_root.find(f"Target[@Name='Validate{prefix}CoverageProperties']")
        error = target.find("Error") if target is not None else None
        if target is None or target.attrib.get("BeforeTargets") != "VSTest" or error is None:
            raise ValueError(f"dotnet coverage adapter must fail closed before VSTest: {assembly}")
        if target.attrib.get("Condition") != active_coverage_condition:
            raise ValueError(
                f"dotnet coverage validation target must run only when CollectCoverage is true: {assembly}"
            )
        required_properties = [include_property, line_property, branch_property]
        if exclusion_property is not None:
            required_properties.append(exclusion_property)
        expected_error_condition = " or ".join(f"'$({name})' == ''" for name in required_properties)
        condition = " ".join(error.attrib.get("Condition", "").split())
        if condition != expected_error_condition:
            raise ValueError(
                f"dotnet coverage validation target must reject each missing property: {assembly}"
            )

        properties[include_property] = f"[{assembly}]*"
        if exclusion_property is not None:
            properties[exclusion_property] = generated_source_exclusion
        for metric in ("line", "branch"):
            value = minimum[metric]
            if isinstance(value, bool) or not isinstance(value, int) or not 0 < value <= 100:
                raise ValueError(f"invalid dotnet {metric} coverage minimum for {assembly}")
            property_name = f"{prefix}{metric.title()}CoverageMinimum"
            if property_name in properties:
                raise ValueError(f"duplicate dotnet coverage property: {property_name}")
            properties[property_name] = value

    discovered_projects = {
        project.resolve()
        for project in tests_root.rglob("*.csproj")
        if any(
            node.attrib.get("Include", "").casefold() == "coverlet.msbuild"
            for node in ET.parse(project).getroot().findall(".//PackageReference")
        )
    }
    if test_projects != discovered_projects:
        raise ValueError("dotnet coverage inventory must match all coverlet test projects")
    return properties


def stage_command(
    stage: str,
    package_root: Path | None = None,
    package_archive: Path | None = None,
) -> tuple[list[str], str]:
    go = _resolve("go")
    if stage == "version":
        return [sys.executable, "qa/version_check.py"], str(REPO_ROOT)
    if stage == "package":
        command = [sys.executable, "qa/package_check.py"]
        if package_root is not None:
            command.append(str(package_root))
        if package_archive is not None:
            command.extend(["--package-archive", str(package_archive)])
        return command, str(REPO_ROOT)
    if stage == "go-fmt":
        return [sys.executable, str(GO_FORMAT_CHECK)], str(REPO_ROOT)
    if stage == "go-vet":
        return [go, "vet", "./..."], str(SIDECAR_DIR)
    if stage == "go-test":
        return [go, "test", "./..."], str(SIDECAR_DIR)
    if stage == "go-coverage":
        return [
            sys.executable,
            str(REPO_ROOT / "qa" / "go_coverage.py"),
            "--go",
            go,
        ], str(REPO_ROOT)
    if stage == "go-race":
        return [
            go,
            "test",
            "-race",
            "all non-integration packages, then integration tests in isolated batches",
        ], str(SIDECAR_DIR)
    if stage == "go-build":
        output = (
            REPO_ROOT / "build" / "qa" / ("vibetable-pb.exe" if os.name == "nt" else "vibetable-pb")
        )
        return [
            go,
            "build",
            "-trimpath",
            "-buildvcs=true",
            "-o",
            str(output),
            "./cmd/vibetable-pb",
        ], str(SIDECAR_DIR)
    if stage == "sidecar-smoke":
        command = [
            sys.executable,
            str(SIDECAR_MATRIX),
            "--json-report",
            str(REPO_ROOT / "build" / "qa" / "packaged-sidecar-matrix.json"),
        ]
        if package_root is not None:
            command.extend(["--skip-build", "--package-root", str(package_root)])
        return command, str(REPO_ROOT)
    if stage == "upgrade-smoke":
        return [
            sys.executable,
            "-m",
            "pytest",
            str(UPGRADE_SMOKE),
            "-q",
            "--basetemp",
            str(_qa_temp_dir() / "u"),
            "-o",
            "addopts=",
            "-p",
            "no:cacheprovider",
        ], str(REPO_ROOT)
    if stage == "dev-build":
        return [sys.executable, str(DEV_LAUNCHER), "--build-only"], str(REPO_ROOT)
    if stage == "python":
        return [
            sys.executable,
            "-m",
            "pytest",
            "tests/backend",
            "-q",
            "-o",
            "addopts=",
            "-p",
            "no:cacheprovider",
        ], str(REPO_ROOT)
    if stage == "contracts":
        return [
            sys.executable,
            "-m",
            "pytest",
            "tests/contract",
            "-q",
            "-o",
            "addopts=",
            "-p",
            "no:cacheprovider",
        ], str(REPO_ROOT)
    if stage == "tooling":
        return [
            sys.executable,
            "-m",
            "pytest",
            "tests/test_release_tooling.py",
            "tests/test_architecture.py",
            "tests/test_dev.py",
            "tests/test_handoff_artifacts.py",
            "tests/test_next_gate.py",
            "-q",
            "-o",
            "addopts=",
            "-p",
            "no:cacheprovider",
        ], str(REPO_ROOT)
    if stage == "dotnet":
        command = [
            _resolve("dotnet"),
            "test",
            str(DESKTOP_SLN),
            "--configuration",
            "Release",
            "/p:CollectCoverage=true",
            "/p:CoverletOutputFormat=cobertura",
        ]
        command.extend(f"/p:{name}={value}" for name, value in dotnet_coverage_properties().items())
        return command, str(REPO_ROOT)
    if stage == "web-test":
        return [_resolve("npm"), "run", "test:coverage"], str(WEB_GRID_DIR)
    if stage == "web-build":
        return [_resolve("npm"), "run", "build"], str(WEB_GRID_DIR)
    if stage == "fault-injection":
        command = [sys.executable, str(FAULT_INJECTION)]
        if package_root is not None:
            command.extend(["--package-root", str(package_root)])
        return command, str(REPO_ROOT)
    if stage == "product-e2e":
        command = [
            sys.executable,
            "qa/product_acceptance.py",
            "--evidence-root",
            # Product acceptance creates timestamp, scenario, runtime,
            # workspace and content-addressed package-cache descendants. Keep
            # this segment minimal so the deepest real Windows path remains
            # below the legacy MAX_PATH boundary.
            str(_qa_temp_dir() / "p"),
        ]
        if package_root is not None:
            command.extend(["--package-root", str(package_root)])
        return command, str(REPO_ROOT)
    if stage == "workbench-qualification":
        return [
            go,
            "run",
            "./cmd/workbench-qualification",
            "--profile",
            "release",
            "--records",
            "100000",
            "--files",
            "10000",
            "--logical-bytes",
            str(20 << 30),
            "--report",
            str(REPO_ROOT / "build" / "qa" / "workbench-qualification.json"),
            "--work-root",
            str(REPO_ROOT / "build" / "qa" / "workbench-qualification-runs"),
        ], str(SIDECAR_DIR)
    if stage == "smoke":
        return [
            sys.executable,
            "-m",
            "pytest",
            str(E2E_SMOKE),
            "-q",
            "-s",
            "-o",
            "addopts=",
            "-p",
            "no:cacheprovider",
        ], str(REPO_ROOT)
    raise ValueError(f"unknown stage: {stage}")


def _stage_environment(
    stage: str,
    command: list[str],
    package_root: Path | None = None,
) -> dict[str, str]:
    # Keep every gate invocation isolated. Reusing the parent directory lets
    # pytest discover a stale ``pytest-of-<user>`` folder whose ACL may belong
    # to a previous Windows sandbox account, failing before any test executes.
    qa_tmp = _qa_temp_dir()
    qa_tmp.mkdir(parents=True, exist_ok=True)
    environment = os.environ.copy()
    environment["TMP"] = str(qa_tmp)
    environment["TEMP"] = str(qa_tmp)
    environment[QA_TEMP_PARENT_ENV] = str(qa_tmp.parent)
    if stage == "fault-injection":
        environment["VIBETABLE_FAULT_EVIDENCE_ROOT"] = str(qa_tmp / "fault-injection")
    if stage.startswith("go-"):
        go_tmp = REPO_ROOT / "build" / "qa" / "go-tmp"
        go_tmp.mkdir(parents=True, exist_ok=True)
        environment["GOTMPDIR"] = str(go_tmp)
    if stage in {"go-test", "go-race"} and package_root is not None:
        layout_path = package_root / "resources" / "publish-layout.json"
        try:
            layout = json.loads(layout_path.read_text(encoding="utf-8"))
            recovery_tools = layout["assets"]["recoveryTools"]
            tool_paths = {
                "VIBETABLE_KOPIA_CLI": package_root / recovery_tools["kopia"],
                "VIBETABLE_AGE_CLI": package_root / recovery_tools["age"],
            }
            resolved_root = package_root.resolve()
            resolved_tools = {
                name: path.resolve() for name, path in tool_paths.items() if path.is_file()
            }
            if len(resolved_tools) == len(tool_paths) and all(
                path.is_relative_to(resolved_root) for path in resolved_tools.values()
            ):
                environment.update({name: str(path) for name, path in resolved_tools.items()})
        except (KeyError, OSError, json.JSONDecodeError, TypeError):
            pass
    if stage == "go-race" and os.name == "nt":
        # Go's Windows race runtime requires cgo plus a MinGW-w64 runtime that
        # provides libsynchronization.a. Prefer the repository-local,
        # hash-verified w64devkit toolchain while still allowing a system gcc.
        compiler = _resolve("gcc")
        environment["CGO_ENABLED"] = "1"
        environment["CC"] = compiler
        compiler_dir = str(Path(compiler).parent)
        # w64devkit's collect2 resolves ld through COMPILER_PATH when invoked
        # indirectly by the Go linker. PATH alone is not reliable in a clean
        # Windows process environment.
        environment["COMPILER_PATH"] = compiler_dir
        environment["PATH"] = compiler_dir + os.pathsep + environment.get("PATH", "")
    if stage == "go-build":
        Path(command[command.index("-o") + 1]).parent.mkdir(parents=True, exist_ok=True)
    if stage == "smoke" and package_root is not None:
        environment["VIBETABLE_E2E_HOST"] = str(package_root / "VibeTable.Next.exe")
    return environment


def _release_stage_environment(
    stage: str,
    environment: dict[str, str],
    package_archive: Path | None,
) -> dict[str, str]:
    """Apply requirements that only hold for immutable-candidate smoke runs."""
    release_environment = environment.copy()
    if stage == "smoke" and package_archive is not None:
        release_environment["VIBETABLE_REQUIRE_WEBVIEW2"] = "1"
    return release_environment


def webview2_evidence(
    stage: str,
    environment: dict[str, str],
    returncode: int,
    stdout: str,
    stderr: str,
) -> str | None:
    if stage != "smoke":
        return None
    if environment.get("VIBETABLE_REQUIRE_WEBVIEW2") != "1":
        return "not-required"
    if returncode != 0:
        return "required-failed"
    if re.search(r"\bskipped\b", stdout + stderr, flags=re.IGNORECASE):
        return "required-skipped"
    if "VIBETABLE_WEBVIEW2_EVIDENCE=passed" in stdout:
        return "required-passed"
    return "required-missing"


def _terminate_process_tree(process: subprocess.Popen[str]) -> None:
    if process.poll() is not None:
        return
    if os.name == "nt":
        with suppress(OSError, subprocess.TimeoutExpired):
            subprocess.run(
                ["taskkill", "/PID", str(process.pid), "/T", "/F"],
                check=False,
                capture_output=True,
                timeout=30,
            )
    else:
        with suppress(ProcessLookupError):
            os.killpg(process.pid, signal.SIGKILL)
    try:
        process.wait(timeout=30)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=30)


def _timeout_text(value: str | bytes | None) -> str:
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value or ""


def _run_command(
    command: list[str],
    *,
    cwd: str,
    environment: dict[str, str],
    timeout: int,
) -> tuple[int, str, str]:
    popen_kwargs: dict[str, object] = {}
    if os.name == "nt":
        popen_kwargs["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP
    else:
        popen_kwargs["start_new_session"] = True
    try:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
            **popen_kwargs,
        )
    except OSError as exc:
        return 127, "", str(exc)
    try:
        stdout, stderr = process.communicate(timeout=timeout)
        return process.returncode, stdout or "", stderr or ""
    except subprocess.TimeoutExpired as exc:
        _terminate_process_tree(process)
        final_stdout, final_stderr = process.communicate()
        stdout = _timeout_text(exc.output) + (final_stdout or "")
        stderr = _timeout_text(exc.stderr) + (final_stderr or "")
        stderr += f"\nprocess tree timed out after {timeout}s and was terminated\n"
        return TIMEOUT_RETURNCODE, stdout, stderr


def _run_race_package(
    commands: list[tuple[list[str], int, str]],
    *,
    environment: dict[str, str],
    stop_event: Event,
    package_name: str = "unknown",
    compile_command: list[str] | None = None,
    compile_cwd: str | None = None,
    binary: Path | None = None,
) -> tuple[int, list[str], list[str]]:
    output: list[str] = []
    errors: list[str] = []
    started = time.monotonic()
    try:
        if stop_event.is_set():
            output.append("race package stopped after another package failed")
            return 0, output, errors
        if compile_command is not None:
            if compile_cwd is None:
                raise ValueError("race package compile command requires compile cwd")
            output.append("$ " + subprocess.list2cmdline(compile_command))
            compile_code, compile_stdout, compile_stderr = _run_command(
                compile_command,
                cwd=compile_cwd,
                environment=environment,
                timeout=RACE_COMMAND_TIMEOUT_SECONDS,
            )
            output.append(compile_stdout)
            errors.append(compile_stderr)
            if compile_code:
                stop_event.set()
                return compile_code, output, errors

        for command, timeout, command_cwd in commands:
            if stop_event.is_set():
                output.append("race package stopped after another package failed")
                return 0, output, errors
            output.append("$ " + subprocess.list2cmdline(command))
            code, stdout, stderr = _run_command(
                command,
                cwd=command_cwd,
                environment=environment,
                timeout=timeout,
            )
            output.append(stdout)
            errors.append(stderr)
            combined = stdout + "\n" + stderr
            attempt = 1
            while (
                code
                and attempt < WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS
                and _is_windows_tempdir_cleanup_flake(combined)
            ):
                attempt += 1
                output.append(
                    "known Windows TempDir watcher cleanup flake; "
                    "retrying this exact race command in a fresh process "
                    f"(attempt {attempt}/{WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS})"
                )
                code, stdout, stderr = _run_command(
                    command,
                    cwd=command_cwd,
                    environment=environment,
                    timeout=timeout,
                )
                output.append(stdout)
                errors.append(stderr)
                combined = stdout + "\n" + stderr
            if code:
                stop_event.set()
                return code, output, errors
        return 0, output, errors
    finally:
        output.append(
            "RACE_PACKAGE_TIMING "
            + json.dumps(
                {
                    "elapsedSeconds": round(time.monotonic() - started, 3),
                    "package": package_name,
                    "testCount": len(commands),
                },
                ensure_ascii=False,
                separators=(",", ":"),
                sort_keys=True,
            )
        )
        if binary is not None:
            try:
                binary.unlink(missing_ok=True)
            except OSError as exc:
                errors.append(f"race binary cleanup failed for {binary}: {exc}")


def _run_go_race(
    *,
    cwd: str,
    environment: dict[str, str],
    shard: tuple[int, int] | None = None,
) -> tuple[int, str, str]:
    """Compile race tests once per package, then isolate every named test process."""

    go = _resolve("go")
    package_command = [
        go,
        "list",
        "-f",
        "{{.ImportPath}}\t{{.Dir}}",
        "./...",
    ]
    package_code, package_stdout, package_stderr = _run_command(
        package_command,
        cwd=cwd,
        environment=environment,
        timeout=RACE_COMMAND_TIMEOUT_SECONDS,
    )
    output = ["$ " + subprocess.list2cmdline(package_command), package_stdout]
    errors = [package_stderr]
    if package_code:
        return package_code, "\n".join(output), "\n".join(errors)
    packages: list[tuple[str, str]] = []
    for line in package_stdout.splitlines():
        if not line.strip():
            continue
        package, separator, package_dir = line.partition("\t")
        if not separator or not package.strip() or not package_dir.strip():
            return (
                1,
                "\n".join(output),
                f"invalid race package discovery row: {line!r}",
            )
        packages.append((package.strip(), package_dir.strip()))
    if not packages:
        return 1, "\n".join(output), "race package discovery found zero packages"

    RACE_BINARY_DIR.mkdir(parents=True, exist_ok=True)
    package_plans: list[RacePackagePlan] = []
    discovered_count = 0
    for package_index, (package, package_dir) in enumerate(packages):
        list_command = [
            go,
            "test",
            package,
            "-list",
            "^(Test|Example|Fuzz)",
        ]
        listed_code, listed_stdout, listed_stderr = _run_command(
            list_command,
            cwd=cwd,
            environment=environment,
            timeout=RACE_COMMAND_TIMEOUT_SECONDS,
        )
        output.extend(("$ " + subprocess.list2cmdline(list_command), listed_stdout))
        errors.append(listed_stderr)
        if listed_code:
            return listed_code, "\n".join(output), "\n".join(errors)
        names = [
            line.strip()
            for line in listed_stdout.splitlines()
            if re.fullmatch(r"(?:Test|Example|Fuzz)[A-Za-z0-9_]+", line.strip())
        ]
        if not names:
            package_plans.append(
                RacePackagePlan(
                    package=package,
                    commands=[
                        (
                            [
                                go,
                                "test",
                                "-race",
                                "-count=1",
                                "-timeout=5m",
                                package,
                            ],
                            RACE_COMMAND_TIMEOUT_SECONDS,
                            cwd,
                        )
                    ],
                    compile_command=None,
                    binary=None,
                )
            )
            continue

        binary_suffix = ".exe" if os.name == "nt" else ".test"
        binary = (RACE_BINARY_DIR / f"package-{package_index:03d}{binary_suffix}").resolve()
        compile_command = [
            go,
            "test",
            "-c",
            "-race",
            "-o",
            str(binary),
            package,
        ]

        discovered_count += len(names)
        regular = [name for name in names if name not in RACE_LONG_TESTS]
        long_running = [name for name in names if name in RACE_LONG_TESTS]
        package_commands: list[tuple[list[str], int, str]] = []
        for name in (*regular, *long_running):
            is_long = name in RACE_LONG_TESTS
            go_timeout = RACE_LONG_TEST_TIMEOUT if is_long else "5m"
            package_commands.append(
                (
                    [
                        str(binary),
                        "-test.count=1",
                        "-test.parallel=1",
                        f"-test.timeout={go_timeout}",
                        f"-test.run=^{re.escape(name)}$",
                    ],
                    (
                        RACE_LONG_COMMAND_TIMEOUT_SECONDS
                        if is_long
                        else RACE_COMMAND_TIMEOUT_SECONDS
                    ),
                    package_dir,
                )
            )
        package_plans.append(
            RacePackagePlan(
                package=package,
                commands=package_commands,
                compile_command=compile_command,
                binary=binary,
            )
        )
    if not discovered_count:
        return 1, "\n".join(output), "race discovery found zero named Go tests"

    if shard is not None:
        shard_index, shard_count = shard
        package_plans = _select_race_package_plans(
            package_plans,
            shard_index,
            shard_count,
            RACE_PACKAGE_SECONDS,
        )
        if not package_plans:
            return 1, "\n".join(output), "race shard selected zero packages"
        output.append(
            "RACE_SHARD_ASSIGNMENT "
            + json.dumps(
                {
                    "index": shard_index,
                    "packages": [plan.package for plan in package_plans],
                    "total": shard_count,
                },
                ensure_ascii=False,
                separators=(",", ":"),
                sort_keys=True,
            )
        )
    package_plans.sort(key=lambda plan: len(plan.commands), reverse=True)
    stop_event = Event()
    worker_count = min(RACE_PACKAGE_WORKERS, len(package_plans))
    with ThreadPoolExecutor(
        max_workers=worker_count,
        thread_name_prefix="vibetable-go-race",
    ) as executor:
        futures = [
            executor.submit(
                _run_race_package,
                plan.commands,
                environment=environment,
                stop_event=stop_event,
                package_name=plan.package,
                compile_command=plan.compile_command,
                compile_cwd=cwd,
                binary=plan.binary,
            )
            for plan in package_plans
        ]
        package_results = [future.result() for future in futures]
    returncode = 0
    for code, package_output, package_errors in package_results:
        output.extend(package_output)
        errors.extend(package_errors)
        if code and not returncode:
            returncode = code
    return returncode, "\n".join(output), "\n".join(errors)


def _is_windows_tempdir_cleanup_flake(output: str) -> bool:
    """Recognize only the narrow Windows TempDir cleanup race, never Go races."""

    if os.name != "nt":
        return False
    if "WARNING: DATA RACE" in output or "panic:" in output:
        return False
    if "TempDir RemoveAll cleanup:" not in output:
        return False
    if "The directory is not empty" not in output:
        return False
    diagnostics = re.findall(r"^\s+([^:\r\n]+\.go):\d+:", output, flags=re.MULTILINE)
    return bool(diagnostics) and all(Path(source).name == "testing.go" for source in diagnostics)


def run_stage(
    stage: str,
    package_root: Path | None = None,
    package_archive: Path | None = None,
    *,
    race_shard: tuple[int, int] | None = None,
) -> StageResult:
    command, cwd = (
        stage_command(stage, package_root)
        if package_archive is None
        else stage_command(stage, package_root, package_archive)
    )
    environment = _release_stage_environment(
        stage,
        _stage_environment(stage, command, package_root),
        package_archive,
    )
    started = time.monotonic()
    if stage == "go-race":
        if race_shard is None:
            returncode, stdout, stderr = _run_go_race(
                cwd=cwd,
                environment=environment,
            )
        else:
            returncode, stdout, stderr = _run_go_race(
                cwd=cwd,
                environment=environment,
                shard=race_shard,
            )
    else:
        returncode, stdout, stderr = _run_command(
            command,
            cwd=cwd,
            environment=environment,
            timeout=STAGE_TIMEOUT_SECONDS.get(
                stage,
                DEFAULT_STAGE_TIMEOUT_SECONDS,
            ),
        )
        attempt = 1
        while (
            stage in {"go-test", "go-coverage"}
            and returncode
            and attempt < WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS
            and _is_windows_tempdir_cleanup_flake(stdout + "\n" + stderr)
        ):
            attempt += 1
            previous_stdout, previous_stderr = stdout, stderr
            returncode, stdout, stderr = _run_command(
                command,
                cwd=cwd,
                environment=environment,
                timeout=STAGE_TIMEOUT_SECONDS.get(
                    stage,
                    DEFAULT_STAGE_TIMEOUT_SECONDS,
                ),
            )
            stdout = "\n".join(
                (
                    previous_stdout,
                    "known Windows TempDir watcher cleanup flake; "
                    "retried the exact Go stage command in a fresh process "
                    f"(attempt {attempt}/"
                    f"{WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS})",
                    stdout,
                )
            )
            stderr = "\n".join((previous_stderr, stderr))
    return StageResult(
        stage=stage,
        command=command,
        returncode=returncode,
        elapsed=time.monotonic() - started,
        stdout=stdout,
        stderr=stderr,
        cwd=cwd,
        webview2_evidence=webview2_evidence(stage, environment, returncode, stdout, stderr),
    )


def _write_console_text(stream: object, value: str) -> None:
    """Write captured UTF-8 output without crashing on a legacy Windows code page."""

    if not value:
        return
    try:
        stream.write(value)  # type: ignore[attr-defined]
    except UnicodeEncodeError:
        encoding = getattr(stream, "encoding", None) or "utf-8"
        safe_value = value.encode(encoding, errors="backslashreplace").decode(encoding)
        stream.write(safe_value)  # type: ignore[attr-defined]
    stream.flush()  # type: ignore[attr-defined]


def run_ci(
    package_root: Path | None = None,
    package_archive: Path | None = None,
) -> tuple[int, list[StageResult]]:
    results: list[StageResult] = []
    for stage in STAGES:
        result = run_stage(stage, package_root, package_archive)
        results.append(result)
        _write_console_text(sys.stdout, result.stdout)
        _write_console_text(sys.stderr, result.stderr)
        if result.returncode:
            return result.returncode, results
    return 0, results


def run_lane(
    lane: str,
    package_root: Path,
    package_archive: Path,
) -> tuple[int, list[StageResult]]:
    results: list[StageResult] = []
    for stage in LANE_STAGES[lane]:
        if stage == "go-race":
            result = run_stage(
                stage,
                package_root,
                package_archive,
                race_shard=(RACE_LANES.index(lane), len(RACE_LANES)),
            )
        else:
            result = run_stage(stage, package_root, package_archive)
        results.append(result)
        _write_console_text(sys.stdout, result.stdout)
        _write_console_text(sys.stderr, result.stderr)
        if result.returncode:
            return result.returncode, results
    return 0, results


def has_required_webview2_evidence(results: list[StageResult]) -> bool:
    smoke_results = [result for result in results if result.stage == "smoke"]
    return len(smoke_results) == 1 and smoke_results[0].webview2_evidence == "required-passed"


def _copy_if_file(source: Path, destination: Path) -> bool:
    if not source.is_file():
        return False
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, destination)
    return True


def persist_product_e2e_evidence(
    source_root: Path,
    destination_root: Path,
    *,
    require_passing_report: bool = False,
) -> Path | None:
    """Persist the newest product E2E report and bounded diagnostics for failures."""

    reports = sorted(
        (
            *source_root.glob("*/product-e2e-report.json"),
            *source_root.glob("*/real-product/*/product-e2e-report.json"),
        ),
        reverse=True,
    )
    if not reports:
        return None
    report_path = reports[0]
    report = json.loads(report_path.read_text(encoding="utf-8"))
    if not isinstance(report, dict):
        raise ValueError(f"product E2E report must be an object: {report_path}")
    report_status = report.get("status")
    if report_status not in {"passed", "failed"}:
        raise ValueError(f"product E2E report has invalid status: {report_path}")
    scenarios = report.get("scenarios")
    if not isinstance(scenarios, list) or not scenarios:
        raise ValueError(f"product E2E report has invalid scenarios: {report_path}")
    for item in scenarios:
        if (
            not isinstance(item, dict)
            or not isinstance(item.get("scenario"), str)
            or not item["scenario"]
            or item.get("status") not in {"passed", "failed"}
        ):
            raise ValueError(f"product E2E report has invalid scenario entries: {report_path}")
    failed_scenarios = [item for item in scenarios if item["status"] != "passed"]
    if report_status == "passed" and failed_scenarios:
        raise ValueError(f"product E2E report passed with failed scenarios: {report_path}")
    if require_passing_report and report_status != "passed":
        raise ValueError(f"product E2E stage did not produce a passing report: {report_path}")

    run_source = report_path.parent
    run_destination = destination_root / run_source.name
    if not _copy_if_file(report_path, run_destination / report_path.name):
        raise OSError(f"could not copy product E2E report: {report_path}")
    for item in failed_scenarios:
        scenario_id = item.get("scenario")
        if (
            not isinstance(scenario_id, str)
            or re.fullmatch(r"\d{2}-[a-z0-9]+(?:-[a-z0-9]+)*", scenario_id) is None
        ):
            continue
        scenario_source = run_source / scenario_id
        scenario_destination = run_destination / scenario_id
        for filename in PRODUCT_E2E_EVIDENCE_FILES:
            _copy_if_file(scenario_source / filename, scenario_destination / filename)
        for filename in (
            f"{scenario_id}-result.json",
            f"{scenario_id}-trace.zip",
            f"{scenario_id}.png",
            "fault-result.json",
            "storage-proof-result.json",
        ):
            _copy_if_file(scenario_source / filename, scenario_destination / filename)

        scenario_number = scenario_id.partition("-")[0]
        runtime_source = run_source / "_runtime" / scenario_number / "host"
        runtime_destination = run_destination / "_runtime" / scenario_number / "host"
        _copy_if_file(
            runtime_source / "vibetable-trace.log",
            runtime_destination / "vibetable-trace.log",
        )
        workspace_root = runtime_source / "local-data" / "workspaces"
        for log_name in PRODUCT_E2E_RUNTIME_LOGS:
            for log_path in sorted(workspace_root.glob(f"*/.vibetable/temp/logs/{log_name}")):
                workspace_id = log_path.parents[3].name
                _copy_if_file(
                    log_path,
                    runtime_destination / "workspace-logs" / workspace_id / log_name,
                )
    return run_destination


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--list", action="store_true")
    mode.add_argument("--ci", action="store_true")
    mode.add_argument("--stage", choices=STAGES)
    mode.add_argument("--lane", choices=tuple(LANE_STAGES))
    parser.add_argument("--json-report", type=Path)
    parser.add_argument("--package-root", type=Path)
    parser.add_argument("--package-archive", type=Path)
    return parser


def _main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    if args.list:
        print("\n".join(STAGES))
        return 0
    try:
        starting_commit = handoff_gate.git_head_sha()
        dependencies = handoff_gate.load_dependencies()
        starting_hashes = handoff_gate.artifact_hashes(dependencies)
        starting_source_hash = handoff_gate.release_source_hash(dependencies)
    except (OSError, RuntimeError, ValueError) as exc:
        print(f"failed to capture gate identity: {exc}", file=sys.stderr)
        return 2
    starting_candidate: dict[str, object] | None = None
    if args.ci or args.lane:
        if args.package_root is None or args.package_archive is None:
            print(
                "--ci/--lane requires --package-root and --package-archive "
                "for immutable candidate binding",
                file=sys.stderr,
            )
            return 2
        try:
            starting_candidate = release_candidate.candidate_evidence(
                args.package_root,
                args.package_archive,
            )
        except release_candidate.CandidateError as exc:
            print(f"failed to capture release candidate: {exc}", file=sys.stderr)
            return 2
    if args.stage:
        result = (
            run_stage(args.stage, args.package_root)
            if args.package_archive is None
            else run_stage(args.stage, args.package_root, args.package_archive)
        )
        results = [result]
        _write_console_text(sys.stdout, result.stdout)
        _write_console_text(sys.stderr, result.stderr)
        code = result.returncode
    elif args.lane:
        code, results = run_lane(args.lane, args.package_root, args.package_archive)
    elif args.ci:
        code, results = run_ci(args.package_root, args.package_archive)
    else:
        _parser().error("choose --list, --stage, --lane, or --ci")
    ending_commit = handoff_gate.git_head_sha()
    ending_dependencies = handoff_gate.load_dependencies()
    ending_hashes = handoff_gate.artifact_hashes(ending_dependencies)
    ending_source_hash = handoff_gate.release_source_hash(ending_dependencies)
    identity_stable = (
        starting_commit == ending_commit
        and starting_hashes == ending_hashes
        and starting_source_hash == ending_source_hash
    )
    if not identity_stable:
        print(
            "release identity changed while the gate was running",
            file=sys.stderr,
        )
        code = code or 1
    if args.ci and not has_required_webview2_evidence(results):
        print("required WebView2 evidence is missing, skipped, or failed", file=sys.stderr)
        code = code or 1
    ending_candidate: dict[str, object] | None = None
    candidate_stable = not (args.ci or args.lane)
    if (
        (args.ci or args.lane)
        and args.package_root is not None
        and args.package_archive is not None
    ):
        try:
            ending_candidate = release_candidate.candidate_evidence(
                args.package_root,
                args.package_archive,
            )
            candidate_stable = starting_candidate == ending_candidate
        except release_candidate.CandidateError as exc:
            print(f"release candidate verification failed: {exc}", file=sys.stderr)
            candidate_stable = False
        if not candidate_stable:
            print("release candidate changed while the gate was running", file=sys.stderr)
            code = code or 1
    release_eligible = bool(
        args.ci
        and code == 0
        and identity_stable
        and candidate_stable
        and ending_candidate is not None
        and has_required_webview2_evidence(results)
    )
    if args.lane:
        evidence_sources: list[tuple[Path, bool]] = []
        product_e2e_result = next(
            (result for result in results if result.stage == "product-e2e"),
            None,
        )
        if product_e2e_result is not None:
            evidence_sources.append((_qa_temp_dir() / "p", product_e2e_result.returncode == 0))
        if any(result.stage == "fault-injection" and result.returncode != 0 for result in results):
            evidence_sources.append((_qa_temp_dir() / "fault-injection", False))

        for source_root, required in evidence_sources:
            try:
                evidence_path = persist_product_e2e_evidence(
                    source_root,
                    REPO_ROOT / "build" / "automation" / "lane-evidence" / args.lane,
                    require_passing_report=required,
                )
                if evidence_path is None:
                    print(
                        f"no product E2E report found under {source_root}",
                        file=sys.stderr,
                    )
                    if required:
                        code = code or 1
                else:
                    print(
                        f"product E2E evidence persisted at {evidence_path}",
                        file=sys.stderr,
                    )
            except (OSError, ValueError, json.JSONDecodeError) as exc:
                print(f"could not persist product E2E evidence: {exc}", file=sys.stderr)
                if required:
                    code = code or 1
    if args.json_report:
        args.json_report.parent.mkdir(parents=True, exist_ok=True)
        args.json_report.write_text(
            json.dumps(
                {
                    "schemaVersion": 2,
                    "reportKind": "lane" if args.lane else ("complete" if args.ci else "stage"),
                    "lane": args.lane,
                    "ok": code == 0,
                    "releaseEligible": release_eligible,
                    "generatedAt": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
                    "commit": ending_commit,
                    "artifactHashes": ending_hashes,
                    "sourceHash": ending_source_hash,
                    "releaseCandidate": ending_candidate,
                    "results": [item.to_dict() for item in results],
                },
                ensure_ascii=False,
                indent=2,
            )
            + "\n",
            encoding="utf-8",
        )
    return code


def main(argv: list[str] | None = None) -> int:
    code: int | None = None
    try:
        code = _main(argv)
        return code
    finally:
        if QA_RUN_TEMP_DIR is not None:
            if code == 0:
                try:
                    _cleanup_qa_temp_dir()
                except OSError as exc:
                    print(
                        f"could not clean QA temporary evidence at {QA_RUN_TEMP_DIR}: {exc}",
                        file=sys.stderr,
                    )
            else:
                print(f"QA failure evidence retained at {QA_RUN_TEMP_DIR}", file=sys.stderr)


if __name__ == "__main__":
    raise SystemExit(main())
