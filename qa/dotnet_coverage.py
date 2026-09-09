"""Collect coverage before testhost shutdown and enforce the existing assembly ratchets."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import tempfile
import xml.etree.ElementTree as ET
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
PROJECT_CONFIG = REPO_ROOT / ".ci/project.json"
DESKTOP_SLN = REPO_ROOT / "desktop/VibeTable.Desktop.sln"
COLLECTOR_VERSION = "10.0.1"
SETTINGS_CONDITION = "'$(VibeTableCoverageSettingsDirectory)' != ''"
PROJECT_SETTINGS = {
    "RunSettingsFilePath": "$(VibeTableCoverageSettingsDirectory)/$(MSBuildProjectName).runsettings",
    "VSTestResultsDirectory": "$(VibeTableCoverageResultsDirectory)/$(MSBuildProjectName)",
}


@dataclass(frozen=True)
class CoverageProject:
    assembly: str
    source_project: Path
    test_project: Path
    line_minimum: int
    branch_minimum: int
    exclude_by_file: str | None = None


def load_projects(config_path: Path = PROJECT_CONFIG) -> tuple[CoverageProject, ...]:
    """Bind every collector project to its authoritative source and independent ratchet."""

    try:
        payload = json.loads(config_path.read_text(encoding="utf-8"))
        projects = payload["quality"]["dotnet_coverage"]["projects"]
    except (OSError, json.JSONDecodeError, KeyError, TypeError) as exc:
        raise ValueError(f"invalid dotnet coverage configuration: {exc}") from exc
    if not isinstance(projects, dict) or not projects:
        raise ValueError("dotnet coverage projects must be a non-empty object")

    projects_to_run: list[CoverageProject] = []
    prefixes: set[str] = set()
    source_projects: set[Path] = set()
    test_projects: set[Path] = set()
    source_root = (REPO_ROOT / "desktop" / "src").resolve()
    tests_root = (REPO_ROOT / "desktop" / "tests").resolve()
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
                if valid_source_files and isinstance(generated_source_files, list)
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
            if node.attrib.get("Include", "").casefold() == "coverlet.collector"
        ]
        if len(coverlet_bindings) != 1:
            raise ValueError(
                f"dotnet coverage test_project must use coverlet.collector once: {assembly}"
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
            raise ValueError(f"coverlet.collector reference must be active for {assembly}")
        project_references = {
            (project_path.parent / node.attrib["Include"]).resolve()
            for node in project_root.findall(".//ProjectReference")
            if "Include" in node.attrib
        }
        if source_path not in project_references:
            raise ValueError(
                f"dotnet coverage test_project must reference source_project: {assembly}"
            )

        validate_collector_project(project_root, assembly)

        for metric in ("line", "branch"):
            value = minimum[metric]
            if isinstance(value, bool) or not isinstance(value, int) or not 0 < value <= 100:
                raise ValueError(f"invalid dotnet {metric} coverage minimum for {assembly}")
        projects_to_run.append(
            CoverageProject(
                assembly,
                source_path,
                project_path,
                minimum["line"],
                minimum["branch"],
                generated_source_exclusion,
            )
        )

    discovered_projects = {
        project.resolve()
        for project in tests_root.rglob("*.csproj")
        if any(
            node.attrib.get("Include", "").casefold() in {"coverlet.collector", "coverlet.msbuild"}
            for node in ET.parse(project).getroot().findall(".//PackageReference")
        )
    }
    if test_projects != discovered_projects:
        raise ValueError("dotnet coverage inventory must match all coverlet test projects")
    return tuple(projects_to_run)


def validate_collector_project(root: ET.Element, assembly: str) -> None:
    packages = root.findall(".//PackageReference")
    if any(node.attrib.get("Include", "").casefold() == "coverlet.msbuild" for node in packages):
        raise ValueError(f"double coverage instrumentation is not allowed: {assembly}")
    collector = [node for node in packages if node.attrib.get("Include") == "coverlet.collector"]
    if len(collector) != 1 or collector[0].attrib.get("Version") != COLLECTOR_VERSION:
        raise ValueError(f"collector must use the pinned supported version: {assembly}")
    groups = [
        group
        for group in root.findall("PropertyGroup")
        if any(child.tag in PROJECT_SETTINGS for child in group)
    ]
    if len(groups) != 1 or groups[0].attrib.get("Condition") != SETTINGS_CONDITION:
        raise ValueError(
            f"collector settings must be scoped to the coverage invocation: {assembly}"
        )
    if {child.tag: child.text for child in groups[0]} != PROJECT_SETTINGS:
        raise ValueError(f"collector settings must bind the current test project: {assembly}")
    forbidden = {
        child.tag
        for group in root.findall("PropertyGroup")
        for child in group
        if child.tag
        in {
            "CollectCoverage",
            "Include",
            "MergeWith",
            "SkipAutoProps",
            "Threshold",
            "ThresholdType",
            "ThresholdStat",
            "TestingPlatformDotnetTestSupport",
        }
        or child.tag.startswith("Exclude")
    }
    if forbidden:
        raise ValueError(f"coverage overrides are not allowed for {assembly}: {forbidden}")


def write_settings(project: CoverageProject, path: Path) -> None:
    root = ET.Element("RunSettings")
    run = ET.SubElement(root, "RunConfiguration")
    environment = ET.SubElement(run, "EnvironmentVariables")
    ET.SubElement(environment, "COVERLET_DATACOLLECTOR_INPROC_EXCEPTIONLOG_ENABLED").text = "1"
    collection = ET.SubElement(root, "DataCollectionRunSettings")
    collectors = ET.SubElement(collection, "DataCollectors")
    collector = ET.SubElement(collectors, "DataCollector", friendlyName="XPlat Code Coverage")
    configuration = ET.SubElement(collector, "Configuration")
    ET.SubElement(configuration, "Format").text = "cobertura"
    ET.SubElement(configuration, "Include").text = f"[{project.assembly}]*"
    if project.exclude_by_file is not None:
        ET.SubElement(configuration, "ExcludeByFile").text = project.exclude_by_file
    ET.indent(root)
    ET.ElementTree(root).write(path, encoding="utf-8", xml_declaration=True)


def _read_xml(path: Path) -> ET.Element:
    root = ET.parse(path).getroot()
    for element in root.iter():
        element.tag = element.tag.rsplit("}", 1)[-1]
    return root


def _integer(element: ET.Element, name: str) -> int:
    raw = element.get(name)
    if raw is None or re.fullmatch(r"[0-9]+", raw) is None:
        raise ValueError(f"missing or invalid coverage count: {name}")
    return int(raw)


def verify_report(project: CoverageProject, results: Path) -> dict[str, int]:
    """Accept only this test project's successful TRX and its one collector attachment."""
    reports = list(results.glob("*.trx"))
    if len(reports) != 1:
        raise ValueError(f"expected one current TRX for {project.assembly}")
    trx = _read_xml(reports[0])
    summary = trx.find("ResultSummary")
    counters = summary.find("Counters") if summary is not None else None
    if (
        summary is None
        or summary.get("outcome") not in {"Completed", "Passed"}
        or counters is None
        or _integer(counters, "total") == 0
        or _integer(counters, "executed") == 0
        or any(_integer(counters, name) != 0 for name in ("failed", "error", "aborted"))
    ):
        raise ValueError(f"test run did not complete successfully: {project.assembly}")
    if any(
        node.get("outcome") in {"Error", "Failed", "Aborted"}
        for node in summary.findall("./RunInfos/RunInfo")
    ):
        raise ValueError(f"test platform or collector reported an error: {project.assembly}")
    tests = trx.findall("./TestDefinitions/UnitTest")
    expected_dll = f"{project.test_project.stem}.dll".casefold()
    if not tests or any(
        (node.get("storage") or "").replace("\\", "/").split("/")[-1].casefold() != expected_dll
        for node in tests
    ):
        raise ValueError(f"TRX belongs to another test project: {project.assembly}")
    entries = summary.findall("./CollectorDataEntries/Collector")
    collectors = [
        node
        for node in entries
        if (node.get("collectorDisplayName") or node.get("friendlyName") or "").casefold()
        == "xplat code coverage"
    ]
    if len(collectors) != 1:
        raise ValueError(f"missing or duplicate coverage collector: {project.assembly}")
    attachments = collectors[0].findall("./UriAttachments/UriAttachment/A")
    if len(attachments) != 1 or not attachments[0].get("href"):
        raise ValueError(f"expected one coverage attachment: {project.assembly}")
    attachment = attachments[0].get("href", "").replace("\\", "/")
    deployment = trx.find("./TestSettings/Deployment")
    deployment_name = deployment.get("runDeploymentRoot") if deployment is not None else None
    if not deployment_name or re.fullmatch(r"[^/\\:]+", deployment_name) is None:
        raise ValueError(f"missing or invalid TRX deployment binding: {project.assembly}")
    attachment_root = (results / deployment_name / "In").resolve()
    coverage_path = (attachment_root / attachment).resolve()
    if (
        not attachment_root.is_relative_to(results.resolve())
        or not coverage_path.is_relative_to(attachment_root)
        or not coverage_path.is_file()
    ):
        raise ValueError(
            f"coverage attachment is outside the current test project: {project.assembly}"
        )
    coverage = _read_xml(coverage_path)
    packages = coverage.findall("./packages/package")
    if (
        coverage.tag != "coverage"
        or len(packages) != 1
        or packages[0].get("name") != project.assembly
    ):
        raise ValueError(f"coverage must contain only {project.assembly}")
    counts: dict[str, int] = {}
    for metric, minimum in (("lines", project.line_minimum), ("branches", project.branch_minimum)):
        covered, valid = (
            _integer(coverage, f"{metric}-{suffix}") for suffix in ("covered", "valid")
        )
        if valid == 0 or covered > valid:
            raise ValueError(f"invalid {metric} denominator for {project.assembly}")
        if covered * 100 < minimum * valid:
            raise ValueError(
                f"{project.assembly} {metric} coverage {covered}/{valid} is below {minimum}%"
            )
        counts[f"{metric}-covered"] = covered
        counts[f"{metric}-valid"] = valid
    return counts


def run_coverage(dotnet: str, project_path: Path | None = None) -> int:
    projects = load_projects()
    if project_path is not None:
        projects = tuple(
            project for project in projects if project.test_project == project_path.resolve()
        )
        if not projects:
            raise ValueError("requested test project is not in the coverage inventory")
    evidence = REPO_ROOT / "build/qa/dotnet-coverage"
    evidence.mkdir(parents=True, exist_ok=True)
    run = Path(tempfile.mkdtemp(prefix="run-", dir=evidence))
    settings, results = run / "settings", run / "results"
    settings.mkdir()
    results.mkdir()
    for project in projects:
        write_settings(project, settings / f"{project.test_project.stem}.runsettings")
    command = [
        dotnet,
        "test",
        str(project_path.resolve() if project_path else DESKTOP_SLN),
        "--configuration",
        "Release",
        "--collect:XPlat Code Coverage",
        "--logger",
        "trx",
        f"/p:VibeTableCoverageSettingsDirectory={settings}",
        f"/p:VibeTableCoverageResultsDirectory={results}",
    ]
    print(f"Coverage evidence: {run}", flush=True)
    (run / "command.json").write_text(json.dumps(command, indent=2), encoding="utf-8")
    completed = subprocess.run(command, cwd=REPO_ROOT, check=False)
    if completed.returncode:
        return completed.returncode
    outcomes: dict[str, dict[str, int]] = {}
    for project in projects:
        outcomes[project.assembly] = verify_report(project, results / project.test_project.stem)
        print(f"{project.assembly}: {outcomes[project.assembly]}", flush=True)
    (run / "coverage-summary.json").write_text(json.dumps(outcomes, indent=2), encoding="utf-8")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dotnet", default="dotnet")
    parser.add_argument("--project", type=Path)
    args = parser.parse_args()
    try:
        return run_coverage(args.dotnet, args.project)
    except (OSError, ValueError, ET.ParseError) as error:
        print(f"Coverage failed: {error}", flush=True)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
