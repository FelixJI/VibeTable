from __future__ import annotations

import json
import subprocess
import xml.etree.ElementTree as ET
from pathlib import Path

import pytest

from qa import dotnet_coverage as coverage_gate
from qa import next as next_gate


def test_dotnet_stage_collects_during_the_test_session_before_enforcing_ratchets() -> None:
    command, cwd = next_gate.stage_command("dotnet")
    assert command[:3] == [next_gate.sys.executable, "-m", "qa.dotnet_coverage"]
    assert "/p:CollectCoverage=true" not in command
    assert Path(cwd) == next_gate.REPO_ROOT


@pytest.mark.parametrize(
    "exclusion",
    [
        "**/*.g.cs",
        "**/VibeTable.Contracts/Generated/*.cs",
        "../VibeTable.Contracts/Generated/*.g.cs",
        "**/Generated/*.g.cs,**/Models/*.cs",
    ],
)
def test_dotnet_coverage_inventory_rejects_broad_generated_source_exclusion(
    tmp_path: Path,
    exclusion: str,
) -> None:
    payload = json.loads(coverage_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    payload["quality"]["dotnet_coverage"]["projects"]["VibeTable.Contracts"][
        "generated_source_exclusion"
    ] = exclusion
    config = tmp_path / "project.json"
    config.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(
        ValueError,
        match=r"invalid generated source exclusion for VibeTable\.Contracts",
    ):
        coverage_gate.load_projects(config)


def test_dotnet_coverage_inventory_rejects_unregistered_generated_source(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    generated_dir = (
        coverage_gate.REPO_ROOT / "desktop/src/VibeTable.Contracts/Generated"
    ).resolve()
    unregistered = generated_dir / "Manual.g.cs"
    real_glob = Path.glob
    real_rglob = Path.rglob

    def glob_with_unregistered(
        path: Path,
        pattern: str,
        *,
        case_sensitive: bool | None = None,
        recurse_symlinks: bool = False,
    ):
        results = list(
            real_glob(
                path, pattern, case_sensitive=case_sensitive, recurse_symlinks=recurse_symlinks
            )
        )
        if path.resolve() == generated_dir and pattern == "*.g.cs":
            results.append(unregistered)
        return iter(results)

    def rglob_with_unregistered(
        path: Path,
        pattern: str,
        *,
        case_sensitive: bool | None = None,
        recurse_symlinks: bool = False,
    ):
        results = list(
            real_rglob(
                path, pattern, case_sensitive=case_sensitive, recurse_symlinks=recurse_symlinks
            )
        )
        contracts_root = generated_dir.parent
        if path.resolve() == contracts_root and pattern == "*.g.cs":
            results.append(unregistered)
        return iter(results)

    monkeypatch.setattr(Path, "glob", glob_with_unregistered)
    monkeypatch.setattr(Path, "rglob", rglob_with_unregistered)

    with pytest.raises(
        ValueError,
        match=r"invalid generated source exclusion for VibeTable\.Contracts",
    ):
        coverage_gate.load_projects()


def test_dotnet_coverage_inventory_rejects_source_binding_drift(
    tmp_path: Path,
) -> None:
    payload = json.loads(coverage_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    projects = payload["quality"]["dotnet_coverage"]["projects"]
    projects["VibeTable.Desktop"]["source_project"] = (
        "desktop/src/VibeTable.Workspace/VibeTable.Workspace.csproj"
    )
    config = tmp_path / "project.json"
    config.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(ValueError, match=r"source_project must match VibeTable\.Desktop"):
        coverage_gate.load_projects(config)


def test_dotnet_coverage_config_rejects_missing_metric_instead_of_disabling_gate(
    tmp_path: Path,
) -> None:
    payload = json.loads(coverage_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    del payload["quality"]["dotnet_coverage"]["projects"]["VibeTable.Desktop"]["minimum"]["branch"]
    config = tmp_path / "project.json"
    config.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(ValueError, match=r"must declare line and branch for VibeTable\.Desktop"):
        coverage_gate.load_projects(config)


def test_inventory_binds_all_six_projects_and_original_ratchets() -> None:
    projects = coverage_gate.load_projects()
    assert {p.assembly: (p.line_minimum, p.branch_minimum) for p in projects} == {
        "VibeTable.Desktop": (63, 53),
        "VibeTable.Contracts": (49, 56),
        "VibeTable.PreviewHost": (41, 50),
        "VibeTable.Workspace": (92, 85),
        "VibeTable.Infrastructure": (74, 64),
        "VibeTable.DocumentDiff.OpenXml": (78, 79),
    }
    configured = {p.test_project for p in projects}
    discovered = {
        path.resolve()
        for path in (coverage_gate.REPO_ROOT / "desktop/tests").glob("*/*.csproj")
        if "coverlet.collector" in path.read_text(encoding="utf-8")
    }
    assert configured == discovered
    contracts = next(p for p in projects if p.assembly == "VibeTable.Contracts")
    assert contracts.exclude_by_file == "**/VibeTable.Contracts/Generated/*.g.cs"
    assert all(p.exclude_by_file is None for p in projects if p != contracts)


@pytest.mark.parametrize(
    "mutation",
    [
        "conditional-package",
        "exclude-assets",
        "missing-build",
        "wrong-version",
        "double-instrument",
        "wrong-settings-condition",
        "wrong-settings-project",
        "external-settings",
        "extra-exclusion",
        "skip-auto",
        "merge",
        "old-threshold",
    ],
)
def test_inventory_rejects_inactive_collector_or_changed_denominator(
    monkeypatch: pytest.MonkeyPatch,
    mutation: str,
) -> None:
    project = (
        coverage_gate.REPO_ROOT
        / "desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj"
    )
    parse = ET.parse

    def changed(path: Path) -> ET.ElementTree[ET.Element]:
        tree = parse(path)
        if Path(path).resolve() != project.resolve():
            return tree
        root = tree.getroot()
        package = root.find(".//PackageReference[@Include='coverlet.collector']")
        assert package is not None
        group = next(
            g for g in root.findall("PropertyGroup") if g.find("RunSettingsFilePath") is not None
        )
        if mutation == "conditional-package":
            package.set("Condition", "false")
        elif mutation == "exclude-assets":
            package.set("ExcludeAssets", "build")
        elif mutation == "missing-build":
            assets = package.find("IncludeAssets")
            assert assets is not None
            assets.text = "runtime"
        elif mutation == "wrong-version":
            package.set("Version", "6.0.4")
        elif mutation == "double-instrument":
            ET.SubElement(
                _required(root, "ItemGroup"), "PackageReference", Include="coverlet.msbuild"
            )
        elif mutation == "wrong-settings-condition":
            group.set("Condition", "true")
        elif mutation == "wrong-settings-project":
            _required(group, "RunSettingsFilePath").text = "other.runsettings"
        elif mutation == "external-settings":
            ET.SubElement(ET.SubElement(root, "PropertyGroup"), "RunSettingsFilePath").text = "old"
        else:
            field = {
                "extra-exclusion": "ExcludeByAttribute",
                "skip-auto": "SkipAutoProps",
                "merge": "MergeWith",
                "old-threshold": "Threshold",
            }[mutation]
            ET.SubElement(ET.SubElement(root, "PropertyGroup"), field).text = "true"
        return tree

    monkeypatch.setattr(coverage_gate.ET, "parse", changed)
    with pytest.raises(ValueError, match=r"collector|coverage|instrumentation"):
        coverage_gate.load_projects()


def test_settings_preserve_exact_include_exclusion_and_synchronous_flush_errors(
    tmp_path: Path,
) -> None:
    for project in coverage_gate.load_projects():
        path = tmp_path / f"{project.assembly}.runsettings"
        coverage_gate.write_settings(project, path)
        root = ET.parse(path).getroot()
        configuration = root.find(".//DataCollector/Configuration")
        assert configuration is not None
        expected = {"Format": "cobertura", "Include": f"[{project.assembly}]*"}
        if project.exclude_by_file:
            expected["ExcludeByFile"] = project.exclude_by_file
        assert {node.tag: node.text for node in configuration} == expected
        assert (
            root.findtext(
                "./RunConfiguration/EnvironmentVariables/COVERLET_DATACOLLECTOR_INPROC_EXCEPTIONLOG_ENABLED"
            )
            == "1"
        )


def _report(results: Path, project: coverage_gate.CoverageProject) -> tuple[Path, Path]:
    results.mkdir(parents=True)
    attachment = results / "test-run" / "In" / "collector-session" / "coverage.cobertura.xml"
    attachment.parent.mkdir(parents=True)
    attachment.write_text(
        f'''<coverage lines-covered="80" lines-valid="100" branches-covered="70" branches-valid="100" line-rate="0.8" branch-rate="0.7"><packages><package name="{project.assembly}"/></packages></coverage>''',
        encoding="utf-8",
    )
    trx = results / "result.trx"
    trx.write_text(
        f'''<TestRun xmlns="http://microsoft.com/schemas/VisualStudio/TeamTest/2010"><TestSettings><Deployment runDeploymentRoot="test-run"/></TestSettings><TestDefinitions><UnitTest storage="{project.test_project.stem}.dll"/></TestDefinitions><ResultSummary outcome="Completed"><Counters total="1" executed="1" failed="0" error="0" aborted="0"/><CollectorDataEntries><Collector collectorDisplayName="XPlat Code Coverage"><UriAttachments><UriAttachment><A href="collector-session/coverage.cobertura.xml"/></UriAttachment></UriAttachments></Collector></CollectorDataEntries></ResultSummary></TestRun>''',
        encoding="utf-8",
    )
    return trx, attachment


@pytest.fixture
def preview() -> coverage_gate.CoverageProject:
    return next(p for p in coverage_gate.load_projects() if p.assembly == "VibeTable.PreviewHost")


@pytest.mark.parametrize("deployment", [None, "../old", "..\\old", "C:/old"])
def test_trx_deployment_must_bind_inside_current_project(
    tmp_path: Path, preview: coverage_gate.CoverageProject, deployment: str | None
) -> None:
    current = tmp_path / "current"
    trx, _ = _report(current, preview)
    tree = coverage_gate._read_xml(trx)
    node = _required(tree, "./TestSettings/Deployment")
    if deployment is None:
        _required(tree, "TestSettings").remove(node)
    else:
        node.set("runDeploymentRoot", deployment)
    ET.ElementTree(tree).write(trx)
    with pytest.raises(ValueError, match="TRX deployment binding"):
        coverage_gate.verify_report(preview, current)


def test_success_uses_only_current_project_attachment_counts(
    tmp_path: Path, preview: coverage_gate.CoverageProject
) -> None:
    _report(tmp_path / "old", preview)
    current = tmp_path / "current"
    _report(current, preview)
    assert coverage_gate.verify_report(preview, current) == {
        "lines-covered": 80,
        "lines-valid": 100,
        "branches-covered": 70,
        "branches-valid": 100,
    }


@pytest.mark.parametrize(
    "mutation",
    [
        "missing-trx",
        "duplicate-trx",
        "wrong-test-project",
        "failed",
        "aborted",
        "zero-tests",
        "missing-collector",
        "duplicate-collector",
        "missing-attachment",
        "outside-attachment",
        "missing-package",
        "duplicate-package",
        "other-package",
        "wrong-package",
        "missing-branch",
        "zero-branch",
        "zero-line",
        "negative",
        "fractional",
        "over-total",
        "branch-below",
        "line-rounding",
    ],
)
def test_reports_fail_closed(
    tmp_path: Path, preview: coverage_gate.CoverageProject, mutation: str
) -> None:
    current = tmp_path / "current"
    trx, report = _report(current, preview)
    if mutation == "missing-trx":
        trx.unlink()
    elif mutation == "duplicate-trx":
        (current / "other.trx").write_bytes(trx.read_bytes())
    elif mutation in {
        "wrong-test-project",
        "failed",
        "aborted",
        "zero-tests",
        "missing-collector",
        "duplicate-collector",
        "missing-attachment",
        "outside-attachment",
    }:
        tree = coverage_gate._read_xml(trx)
        summary = _required(tree, "ResultSummary")
        counters = _required(summary, "Counters")
        collector = _required(summary, "CollectorDataEntries/Collector")
        if mutation == "wrong-test-project":
            _required(tree, ".//UnitTest").set("storage", "Other.Tests.dll")
        elif mutation == "failed":
            counters.set("failed", "1")
        elif mutation == "aborted":
            summary.set("outcome", "Aborted")
        elif mutation == "zero-tests":
            counters.set("executed", "0")
        elif mutation == "missing-collector":
            _required(summary, "CollectorDataEntries").remove(collector)
        elif mutation == "duplicate-collector":
            _required(summary, "CollectorDataEntries").append(collector)
        elif mutation == "missing-attachment":
            _required(collector, ".//A").set("href", "missing.xml")
        else:
            _required(collector, ".//A").set("href", "../old/coverage.cobertura.xml")
        ET.ElementTree(tree).write(trx)
    else:
        report_tree = ET.parse(report)
        root = report_tree.getroot()
        packages = _required(root, "packages")
        package = _required(packages, "package")
        if mutation == "missing-package":
            packages.remove(package)
        elif mutation == "duplicate-package":
            packages.append(package)
        elif mutation == "other-package":
            ET.SubElement(packages, "package", {"name": "Other", "line-rate": "1"})
        elif mutation == "wrong-package":
            package.set("name", "Other")
        elif mutation == "missing-branch":
            del root.attrib["branches-valid"]
        elif mutation == "zero-branch":
            root.set("branches-valid", "0")
        elif mutation == "zero-line":
            root.set("lines-valid", "0")
        elif mutation == "negative":
            root.set("lines-covered", "-1")
        elif mutation == "fractional":
            root.set("lines-covered", "80.5")
        elif mutation == "over-total":
            root.set("lines-covered", "101")
        elif mutation == "branch-below":
            root.set("branches-covered", "49")
        else:
            root.set("lines-covered", "40999")
            root.set("lines-valid", "100000")
            root.set("line-rate", "0.41")
        report_tree.write(report)
    with pytest.raises((ValueError, OSError)):
        coverage_gate.verify_report(preview, current)


def test_nonzero_test_exit_never_becomes_success_from_an_old_report(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    preview: coverage_gate.CoverageProject,
) -> None:
    _report(tmp_path / "build/qa/dotnet-coverage/old/results" / preview.test_project.stem, preview)
    monkeypatch.setattr(coverage_gate, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(coverage_gate, "load_projects", lambda: (preview,))
    calls = []

    def run(command: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(command)
        return subprocess.CompletedProcess(command, 9)

    monkeypatch.setattr(coverage_gate.subprocess, "run", run)
    assert coverage_gate.run_coverage("dotnet") == 9
    assert len(calls) == 1
    assert "--collect:XPlat Code Coverage" in calls[0]
    assert str(coverage_gate.DESKTOP_SLN) in calls[0]


def _required(node: ET.Element, path: str) -> ET.Element:
    found = node.find(path)
    assert found is not None
    return found


def test_collector_error_cannot_be_hidden_by_passing_test_counters(
    tmp_path: Path,
    preview: coverage_gate.CoverageProject,
) -> None:
    results = tmp_path / "current"
    trx, _ = _report(results, preview)
    root = coverage_gate._read_xml(trx)
    infos = ET.SubElement(_required(root, "ResultSummary"), "RunInfos")
    error = ET.SubElement(infos, "RunInfo", outcome="Error")
    ET.SubElement(error, "Text").text = "Data collector failed to unload module"
    ET.ElementTree(root).write(trx)
    with pytest.raises(ValueError, match="collector reported"):
        coverage_gate.verify_report(preview, results)
