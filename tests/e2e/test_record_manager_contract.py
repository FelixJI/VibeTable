"""Focused real-source DOM and package contracts; these do not replace WPF QA 53."""

from __future__ import annotations

import subprocess
from pathlib import Path

from backend.infrastructure.plugin_package import inspect_plugin_package
from qa.product_scenario_manifest import load_scenarios
from qa.release_eligibility import FILE_IO_SCENARIO_IDS
from scripts.node_toolchain import resolve_node
from tests.e2e.plugin_capability_packages import prepare_record_manager_package

ROOT = Path(__file__).resolve().parents[2]


def test_record_manager_real_source_dom_contract() -> None:
    node = resolve_node(ROOT)
    assert node is not None
    completed = subprocess.run(
        [
            node,
            "--test",
            str(ROOT / "tests/e2e/record_manager_ui.test.mjs"),
            str(ROOT / "tests/e2e/record_manager_product.test.mjs"),
        ],
        cwd=ROOT,
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
    )
    assert completed.returncode == 0, completed.stdout + completed.stderr


def test_record_manager_real_sandbox_contract() -> None:
    node = resolve_node(ROOT)
    assert node is not None
    completed = subprocess.run(
        [node, "--test", str(ROOT / "tests/e2e/record_manager_sandbox.test.mjs")],
        cwd=ROOT,
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
        timeout=60,
    )
    assert completed.returncode == 0, completed.stdout + completed.stderr


def test_record_manager_package_has_only_real_app_and_headless_entries(tmp_path: Path) -> None:
    prepare_record_manager_package(tmp_path)
    manifest = inspect_plugin_package(tmp_path / "record-manager.vtplugin").manifest
    assert [item["presentation"] for item in manifest["actions"]] == ["application", "headless"]
    assert manifest["ui"]["customViews"] == []
    assert all("workerEntry" not in item for item in manifest["actions"])


def test_record_manager_is_required_independent_file_lane() -> None:
    scenarios = {scenario.id: scenario for scenario in load_scenarios()}
    assert "53-record-manager" in FILE_IO_SCENARIO_IDS
    assert "plugin.applications" in scenarios["53-record-manager"].capabilities
