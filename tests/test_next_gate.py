from __future__ import annotations

import json
import sys
import xml.etree.ElementTree as ET
from pathlib import Path

from qa import next as next_gate

REPO_ROOT = Path(__file__).resolve().parent.parent


def test_dev_build_runs_after_package_checks_before_stack_tests() -> None:
    assert next_gate.STAGES[:4] == ("version", "package", "dev-build", "python")


def test_dev_build_stage_uses_real_launcher_build_only_mode() -> None:
    command, cwd = next_gate.stage_command("dev-build")

    assert command == [sys.executable, str(REPO_ROOT / "scripts" / "dev.py"), "--build-only"]
    assert cwd == str(REPO_ROOT)


def test_dotnet_stage_collects_coverage_for_project_specific_gates() -> None:
    command, cwd = next_gate.stage_command("dotnet")

    assert "/p:CollectCoverage=true" in command
    assert "/p:CoverletOutputFormat=cobertura" in command
    assert cwd == str(REPO_ROOT)


def test_web_and_directus_test_stages_use_coverage_scripts() -> None:
    web_command, web_cwd = next_gate.stage_command("web-test")
    directus_command, directus_cwd = next_gate.stage_command("directus-test")

    assert web_command[-2:] == ["run", "test:coverage"]
    assert web_cwd == str(REPO_ROOT / "desktop" / "web-grid")
    assert directus_command[-2:] == ["run", "test:coverage"]
    assert directus_cwd == str(REPO_ROOT / "directus" / "extensions" / "vibetable-bulk-mutation")


def test_dotnet_projects_keep_their_measured_line_coverage_ratchets() -> None:
    expected_thresholds = {
        "VibeTable.Desktop.Tests": "45",
        "VibeTable.Infrastructure.Tests": "65",
        "VibeTable.Workspace.Tests": "80",
    }

    for project_name, expected in expected_thresholds.items():
        project = REPO_ROOT / "desktop" / "tests" / project_name / f"{project_name}.csproj"
        properties = {
            child.tag: child.text
            for group in ET.parse(project).getroot().findall("PropertyGroup")
            for child in group
        }
        assert properties["Threshold"] == expected
        assert properties["ThresholdType"] == "line"
        assert properties["ThresholdStat"] == "total"


def test_every_directus_extension_keeps_the_native_coverage_gate() -> None:
    extensions_root = REPO_ROOT / "directus" / "extensions"
    for package_file in extensions_root.glob("*/package.json"):
        package = json.loads(package_file.read_text(encoding="utf-8"))
        command = package["scripts"]["test:coverage"]
        assert "--test-coverage-lines=80" in command
        assert "--test-coverage-branches=65" in command
        assert "--test-coverage-functions=75" in command
