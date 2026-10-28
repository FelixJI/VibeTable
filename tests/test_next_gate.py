from __future__ import annotations

import io
import json
import os
import subprocess
import sys
import threading
from contextlib import suppress
from pathlib import Path

import pytest

from qa import next as next_gate
from qa import release_eligibility


def _candidate_args(tmp_path: Path) -> list[str]:
    package_root = tmp_path / "VibeTable.Next"
    package_root.mkdir()
    (package_root / "candidate.bin").write_bytes(b"candidate")
    (package_root / "release.json").write_text(
        json.dumps(
            {
                "product": "VibeTable",
                "version": "1.2.3",
                "platform": "windows",
                "architecture": "x64",
            }
        ),
        encoding="utf-8",
    )
    archive = tmp_path / "VibeTable-v1.2.3-win-x64.zip"
    next_gate.release_candidate.create_archive(package_root, archive)
    return [
        "--package-root",
        str(package_root),
        "--package-archive",
        str(archive),
    ]


def test_console_output_is_safe_on_legacy_windows_code_pages() -> None:
    raw = io.BytesIO()
    stream = io.TextIOWrapper(raw, encoding="ascii", errors="strict", newline="")
    try:
        next_gate._write_console_text(stream, "stage output: \ufffd / 汉字\n")
        assert raw.getvalue() == b"stage output: \\ufffd / \\u6c49\\u5b57\n"
    finally:
        stream.detach()


def test_ci_gate_runs_go_and_real_sidecar_before_desktop_stacks() -> None:
    removed_provider = "".join(["di", "rectus"])
    assert next_gate.STAGES[:9] == (
        "version",
        "package",
        "go-fmt",
        "go-vet",
        "go-test",
        "go-coverage",
        "go-race",
        "go-build",
        "sidecar-smoke",
    )
    assert all(removed_provider not in stage for stage in next_gate.STAGES)
    assert (
        tuple(next_gate.handoff_gate.load_dependencies()["requiredGateStages"]) == next_gate.STAGES
    )


def test_github_workflows_keep_release_build_on_windows() -> None:
    workflow_dir = next_gate.REPO_ROOT / ".github" / "workflows"
    workflow_paths = sorted([*workflow_dir.glob("*.yml"), *workflow_dir.glob("*.yaml")])
    workflows = {path.name: path.read_text(encoding="utf-8") for path in workflow_paths}
    assert set(workflows) == {"ci.yml", "cd.yml"}
    for workflow in workflows.values():
        runner_lines = [
            line.strip() for line in workflow.splitlines() if line.strip().startswith("runs-on:")
        ]
        assert runner_lines
        assert set(runner_lines) == {"runs-on: windows-latest"}
        assert "macos-" not in workflow

    ci_workflow = workflows["ci.yml"]
    cd_workflow = workflows["cd.yml"]
    assert "python scripts/automation.py ci" in ci_workflow
    assert "python scripts/automation_project.py" not in ci_workflow
    assert "environment: release" in cd_workflow
    assert "python scripts/automation.py release publish" in cd_workflow
    assert "release-please" not in (ci_workflow + cd_workflow).lower()

    command, cwd = next_gate.stage_command("smoke")
    assert str(next_gate.E2E_SMOKE) in command
    assert Path(cwd) == next_gate.REPO_ROOT


def test_quality_tooling_gate_includes_the_project_adapter_contracts() -> None:
    assert "tooling" in next_gate.LANE_STAGES["quality"]
    command, cwd = next_gate.stage_command("tooling")
    assert command[:3] == [sys.executable, "-m", "pytest"]
    assert "tests/test_automation_adapter.py" in command
    assert Path(cwd) == next_gate.REPO_ROOT


def test_go_commands_target_sidecar_module() -> None:
    command, cwd = next_gate.stage_command("go-fmt")
    assert command == [next_gate.sys.executable, str(next_gate.GO_FORMAT_CHECK)]
    assert Path(cwd) == next_gate.REPO_ROOT

    command, cwd = next_gate.stage_command("go-vet")
    assert command[-2:] == ["vet", "./..."]
    assert Path(cwd) == next_gate.SIDECAR_DIR

    command, cwd = next_gate.stage_command("go-race")
    assert command[:3] == [next_gate._resolve("go"), "test", "-race"]
    assert "isolated batches" in command[-1]
    assert Path(cwd) == next_gate.SIDECAR_DIR

    command, cwd = next_gate.stage_command("go-coverage")
    assert command == [
        next_gate.sys.executable,
        str(next_gate.REPO_ROOT / "qa" / "go_coverage.py"),
        "--go",
        next_gate._resolve("go"),
    ]
    assert Path(cwd) == next_gate.REPO_ROOT
    assert "go-coverage" in next_gate.LANE_STAGES["core"]

    command, cwd = next_gate.stage_command("sidecar-smoke")
    assert command[0] == next_gate.sys.executable
    assert "packaged_sidecar_matrix.py" in " ".join(command)
    assert "--json-report" in command
    assert Path(cwd) == next_gate.REPO_ROOT


def test_package_stage_binds_release_candidate_to_the_archive(
    tmp_path: Path,
) -> None:
    package_root = tmp_path / "VibeTable.Next"
    archive = tmp_path / "VibeTable-v1.2.3-win-x64.zip"

    command, cwd = next_gate.stage_command("package", package_root, archive)

    assert command == [
        next_gate.sys.executable,
        "qa/package_check.py",
        str(package_root),
        "--package-archive",
        str(archive),
    ]
    assert Path(cwd) == next_gate.REPO_ROOT


def test_windows_race_gate_resolves_an_existing_gcc_executable() -> None:
    if next_gate.os.name != "nt":
        return
    compiler = Path(next_gate._resolve("gcc"))
    assert compiler.name.casefold() == "gcc.exe"
    assert compiler.is_file()


def test_windows_race_gate_prefers_repository_local_mingw_when_available(
    monkeypatch,
    tmp_path: Path,
) -> None:
    if next_gate.os.name != "nt":
        return
    compiler = tmp_path / ".tools" / "w64devkit" / "bin" / "gcc.exe"
    compiler.parent.mkdir(parents=True)
    compiler.touch()
    monkeypatch.setattr(next_gate, "REPO_ROOT", tmp_path)

    assert Path(next_gate._resolve("gcc")) == compiler


def test_windows_race_stage_enables_cgo_with_the_resolved_compiler(
    monkeypatch,
) -> None:
    if next_gate.os.name != "nt":
        return
    observed: dict[str, str] = {}

    def fake_race(*, cwd, environment):
        del cwd
        observed.update(environment)
        return 0, "", ""

    monkeypatch.setattr(next_gate, "_run_go_race", fake_race)
    result = next_gate.run_stage("go-race")

    compiler = Path(next_gate._resolve("gcc"))
    assert result.returncode == 0
    assert observed["CGO_ENABLED"] == "1"
    assert Path(observed["CC"]) == compiler
    assert Path(observed["COMPILER_PATH"]) == compiler.parent
    assert observed["PATH"].split(next_gate.os.pathsep, 1)[0] == str(compiler.parent)


def test_stage_environment_uses_an_invocation_scoped_temp_root() -> None:
    next_gate._cleanup_qa_temp_dir()
    environment = next_gate._stage_environment("upgrade-smoke", ["pytest"])

    temp_root = Path(environment["TEMP"])
    expected_parent = Path(
        next_gate.os.environ.get(
            next_gate.QA_TEMP_PARENT_ENV,
            next_gate.tempfile.gettempdir(),
        )
    ).resolve()
    assert environment["TMP"] == environment["TEMP"]
    assert temp_root == next_gate.QA_RUN_TEMP_DIR
    assert temp_root.parent == expected_parent
    assert temp_root.name.startswith("vtqa-")
    assert temp_root.is_dir()
    assert environment[next_gate.QA_TEMP_PARENT_ENV] == str(expected_parent)


def test_release_smoke_requires_webview2_and_records_required_evidence(tmp_path: Path) -> None:
    environment = next_gate._release_stage_environment("smoke", {}, tmp_path / "candidate.zip")

    assert environment["VIBETABLE_REQUIRE_WEBVIEW2"] == "1"
    assert (
        next_gate.webview2_evidence(
            "smoke",
            environment,
            0,
            "VIBETABLE_WEBVIEW2_EVIDENCE=passed\n1 passed",
            "",
        )
        == "required-passed"
    )
    assert (
        next_gate.webview2_evidence("smoke", environment, 0, "1 passed", "") == "required-missing"
    )
    assert (
        next_gate.webview2_evidence("smoke", environment, 0, "1 skipped: unavailable", "")
        == "required-skipped"
    )


def test_smoke_stage_disables_pytest_capture_so_the_evidence_marker_is_observable() -> None:
    command, _cwd = next_gate.stage_command("smoke")

    assert "-s" in command


def test_nested_gate_temp_root_is_a_sibling_not_a_recursive_child(
    monkeypatch,
    tmp_path: Path,
) -> None:
    outer = tmp_path / "vtqa-outer"
    outer.mkdir()
    monkeypatch.setenv(next_gate.QA_TEMP_PARENT_ENV, str(tmp_path))
    monkeypatch.setenv("TMP", str(outer))
    monkeypatch.setenv("TEMP", str(outer))
    monkeypatch.setattr(next_gate, "QA_RUN_TEMP_DIR", None)

    nested = next_gate._qa_temp_dir()

    assert nested.parent == tmp_path
    assert not nested.is_relative_to(outer)


def test_upgrade_smoke_uses_a_short_explicit_pytest_temp_root() -> None:
    command, cwd = next_gate.stage_command("upgrade-smoke")

    basetemp_index = command.index("--basetemp")
    assert next_gate.QA_RUN_TEMP_DIR is not None
    assert command[basetemp_index + 1] == str(next_gate.QA_RUN_TEMP_DIR / "u")
    assert Path(cwd) == next_gate.REPO_ROOT


def test_product_e2e_deepest_plugin_cache_path_stays_below_windows_max_path() -> None:
    command, _cwd = next_gate.stage_command("product-e2e")
    evidence_root = Path(command[command.index("--evidence-root") + 1])
    deepest_path = (
        evidence_root
        / "20260729T153642Z"
        / "11-plugin-mutation"
        / "runtime"
        / "local-data"
        / "workspaces"
        / "6dcf9c24-5c36-4bf4-ace6-89408a260018"
        / ".vibetable"
        / "data"
        / "state"
        / "plugin-packages"
        / ("a" * 52 + ".vtplugin")
    )

    assert evidence_root.name == "p"
    assert len(str(deepest_path)) < 260


def test_product_e2e_stage_commands_select_exact_manifest_partition() -> None:
    scenarios = next_gate.handoff_gate.load_dependencies()
    assert scenarios["requiredGateStages"].count("product-e2e-data-io") == 1
    assert scenarios["requiredGateStages"].count("product-e2e-file-io") == 1
    commands = [
        next_gate.stage_command(stage)[0]
        for stage in ("product-e2e", "product-e2e-data-io", "product-e2e-file-io")
    ]
    selected = [
        [command[index + 1] for index, part in enumerate(command) if part == "--scenario"]
        for command in commands
    ]
    manifest_ids = {item.id for item in next_gate.load_scenarios()}

    assert all(len(ids) == len(set(ids)) for ids in selected)
    assert set(selected[0]).isdisjoint(selected[1])
    assert set(selected[0]).isdisjoint(selected[2])
    assert set(selected[1]).isdisjoint(selected[2])
    assert set(selected[0]) | set(selected[1]) | set(selected[2]) == manifest_ids
    assert set(selected[1]) == {
        "02-all-field-schema",
        "05-formula-lifecycle",
        "06-relation-fanout",
        "26-lookup-definition-read",
        "23-directory-replica-recovery",
        "24-directory-replica-conflict",
        "34-relation-lookup-data-io",
        "35-data-io-interoperability",
        "37-collection-formula-journey",
        "38-calculation-chain-journey",
    }
    assert set(selected[2]) == {
        "39-file-workflow-combination",
        "40-file-history-capacity",
        "41-file-document-operations",
        "42-file-document-native-operations",
        "43-file-revision-leaves",
        "44-file-restore-crash",
        "45-common-field-display",
        "48-plugin-applications",
        "49-plugin-application-security",
        "50-plugin-headless-budget",
        "51-plugin-application-commit-unknown",
        "52-plugin-capability-services",
        "53-record-manager",
        "54-plugin-models",
        "55-plugin-surface-outputs",
    }
    evidence_roots = [Path(command[command.index("--evidence-root") + 1]) for command in commands]
    assert [root.name for root in evidence_roots] == ["p", "d", "f"]
    assert next_gate.STAGE_TIMEOUT_SECONDS["product-e2e-data-io"] == 30 * 60
    assert next_gate.STAGE_TIMEOUT_SECONDS["product-e2e-file-io"] == 30 * 60


def test_workbench_qualification_stage_freezes_representative_scale_and_report() -> None:
    command, cwd = next_gate.stage_command("workbench-qualification")

    assert command[:3] == [next_gate._resolve("go"), "run", "./cmd/workbench-qualification"]
    assert command[command.index("--profile") + 1] == "release"
    assert command[command.index("--records") + 1] == "100000"
    assert command[command.index("--files") + 1] == "10000"
    assert command[command.index("--logical-bytes") + 1] == str(20 << 30)
    assert Path(command[command.index("--report") + 1]) == (
        next_gate.REPO_ROOT / "build" / "qa" / "workbench-qualification.json"
    )
    assert Path(cwd) == next_gate.SIDECAR_DIR
    assert "workbench-qualification" in next_gate.LANE_STAGES["resilience"]


def test_runtime_baseline_stage_uses_candidate_package_and_bounded_evidence_root(
    monkeypatch,
    tmp_path: Path,
) -> None:
    qa_temp = tmp_path / "q"
    package_root = tmp_path / "VibeTable.Next"
    package_archive = tmp_path / "VibeTable-v0.5.1-win-x64.zip"
    monkeypatch.setattr(next_gate, "QA_RUN_TEMP_DIR", qa_temp)
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)

    command, cwd = next_gate.stage_command(
        "runtime-baseline",
        package_root,
        package_archive,
    )

    assert command[:3] == [
        sys.executable,
        "-m",
        "tests.e2e.packaged_runtime_baseline",
    ]
    assert Path(command[command.index("--package-root") + 1]) == package_root
    assert Path(command[command.index("--package-archive") + 1]) == package_archive
    assert Path(command[command.index("--workspace-root") + 1]) == qa_temp / "r" / "workspace"
    assert Path(command[command.index("--evidence-root") + 1]) == qa_temp / "r" / "runtime"
    assert Path(command[command.index("--build-identity") + 1]) == (
        next_gate.REPO_ROOT / "build" / "automation" / "artifacts" / "build-identity.json"
    )
    assert command[command.index("--source-sha") + 1] == "c" * 40
    assert Path(command[command.index("--json-report") + 1]) == (
        qa_temp / "r" / "packaged-runtime-baseline.json"
    )
    assert Path(cwd) == next_gate.REPO_ROOT
    help_result = subprocess.run(
        [*command, "--help"],
        cwd=cwd,
        capture_output=True,
        text=True,
        check=False,
    )
    assert help_result.returncode == 0, help_result.stderr
    assert "Measure one real packaged workspace lifecycle" in help_result.stdout

    with pytest.raises(ValueError, match="--package-root and --package-archive"):
        next_gate.stage_command("runtime-baseline", package_root)


def test_packaged_recovery_tools_are_injected_into_go_interoperability_stages(
    tmp_path: Path,
) -> None:
    package_root = tmp_path / "VibeTable.Next"
    kopia = package_root / "resources/sidecar/tools/kopia.exe"
    age = package_root / "resources/sidecar/tools/age.exe"
    kopia.parent.mkdir(parents=True)
    kopia.touch()
    age.touch()
    (package_root / "resources/publish-layout.json").write_text(
        json.dumps(
            {
                "assets": {
                    "recoveryTools": {
                        "kopia": "resources/sidecar/tools/kopia.exe",
                        "age": "resources/sidecar/tools/age.exe",
                        "ageKeygen": "resources/sidecar/tools/age-keygen.exe",
                    }
                }
            }
        ),
        encoding="utf-8",
    )

    for stage in ("go-test", "go-race"):
        environment = next_gate._stage_environment(stage, ["go"], package_root)
        assert environment["VIBETABLE_KOPIA_CLI"] == str(kopia.resolve())
        assert environment["VIBETABLE_AGE_CLI"] == str(age.resolve())


def test_source_only_go_stages_do_not_fabricate_recovery_tool_paths() -> None:
    environment = next_gate._stage_environment("go-test", ["go"])

    assert "VIBETABLE_KOPIA_CLI" not in environment
    assert "VIBETABLE_AGE_CLI" not in environment


def test_go_stages_reuse_the_callers_restored_build_cache(monkeypatch) -> None:
    monkeypatch.setenv("GOCACHE", "restored-setup-go-cache")

    environment = next_gate._stage_environment("go-test", ["go"])

    assert environment["GOCACHE"] == "restored-setup-go-cache"
    assert environment["GOTMPDIR"] == str(next_gate.REPO_ROOT / "build" / "qa" / "go-tmp")


def test_go_stages_leave_the_default_build_cache_unset(monkeypatch) -> None:
    monkeypatch.delenv("GOCACHE", raising=False)

    environment = next_gate._stage_environment("go-race", ["go"])

    assert "GOCACHE" not in environment


def test_race_stage_isolates_packages_for_wall_clock_budgets() -> None:
    assert next_gate.RACE_PACKAGE_WORKERS == 1


def test_release_gate_enables_required_windows_credential_manager_tests() -> None:
    adapter = (next_gate.REPO_ROOT / "scripts/automation_project.py").read_text(encoding="utf-8")
    workflow = (next_gate.REPO_ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
    assert '_node_environment({"VIBETABLE_TEST_WINDOWS_CREDENTIAL_MANAGER": "1"})' in adapter
    assert "environment: release" in workflow
    assert "contents: write" in workflow


def test_release_workflow_runs_candidate_bound_shards_and_aggregates_them() -> None:
    adapter = (next_gate.REPO_ROOT / "scripts/automation_project.py").read_text(encoding="utf-8")
    assert '"qa/next.py",' in adapter
    assert '"--lane",' in adapter
    assert '"--package-root",' in adapter
    assert '"--package-archive",' in adapter
    assert '"qa/release_eligibility.py",' in adapter
    assert '"--reports-dir",' in adapter


def test_race_stage_compiles_each_package_once_and_runs_every_test_in_isolation(
    monkeypatch,
    tmp_path: Path,
) -> None:
    observed: list[tuple[list[str], str, int]] = []
    names = [f"TestCase{index}" for index in range(17)]
    package_dir = tmp_path / "integration"
    package_dir.mkdir()
    monkeypatch.setattr(next_gate, "RACE_BINARY_DIR", tmp_path / "race-binaries", raising=False)

    def fake_command(command, *, cwd, environment, timeout, race_build=False):
        assert race_build == (command[1:4] == ["test", "-c", "-race"])
        del environment
        observed.append((command, cwd, timeout))
        if command[1] == "list" and command[-1] == "./...":
            return 0, f"example/tests/integration\t{package_dir}\n", ""
        if command[1:4] == ["test", "-c", "-race"]:
            Path(command[command.index("-o") + 1]).touch()
            return 0, "", ""
        if any(arg.startswith("-test.list=") for arg in command):
            return 0, "\n".join(names) + "\nok\tpackage\n", ""
        return 0, "ok\n", ""

    monkeypatch.setattr(next_gate, "_run_command", fake_command)
    code, _stdout, _stderr = next_gate._run_go_race(
        cwd=str(next_gate.SIDECAR_DIR),
        environment={},
    )

    assert code == 0
    compile_commands = [
        command for command, _cwd, _timeout in observed if command[1:4] == ["test", "-c", "-race"]
    ]
    assert all("-x" in command for command in compile_commands)
    assert len(compile_commands) == 1
    assert compile_commands[0][-1] == "example/tests/integration"

    test_runs = [
        (command, cwd)
        for command, cwd, _timeout in observed
        if any(argument.startswith("-test.run=") for argument in command)
    ]
    assert len(test_runs) == len(names)
    assert {
        argument.removeprefix("-test.run=^").removesuffix("$")
        for command, _cwd in test_runs
        for argument in command
        if argument.startswith("-test.run=")
    } == set(names)
    assert all(cwd == str(package_dir) for _command, cwd in test_runs)
    assert all("-test.count=1" in command for command, _cwd in test_runs)
    assert all("-test.parallel=1" in command for command, _cwd in test_runs)


def test_known_slow_race_tests_run_individually_with_long_timeout(
    monkeypatch,
    tmp_path: Path,
) -> None:
    observed: list[tuple[list[str], int]] = []
    slow = sorted(next_gate.RACE_LONG_TESTS)
    package_dir = tmp_path / "integration"
    package_dir.mkdir()
    monkeypatch.setattr(next_gate, "RACE_BINARY_DIR", tmp_path / "race-binaries", raising=False)

    def fake_command(command, *, cwd, environment, timeout, race_build=False):
        assert race_build == (command[1:4] == ["test", "-c", "-race"])
        del cwd, environment
        observed.append((command, timeout))
        if command[1] == "list" and command[-1] == "./...":
            return 0, f"example/tests/integration\t{package_dir}\n", ""
        if command[1:4] == ["test", "-c", "-race"]:
            Path(command[command.index("-o") + 1]).touch()
            return 0, "", ""
        if any(arg.startswith("-test.list=") for arg in command):
            return 0, "\n".join([*slow, "TestRegular"]) + "\nok\tpackage\n", ""
        return 0, "ok\n", ""

    monkeypatch.setattr(next_gate, "_run_command", fake_command)
    code, _stdout, _stderr = next_gate._run_go_race(
        cwd=str(next_gate.SIDECAR_DIR),
        environment={},
    )

    assert code == 0
    for name in slow:
        matching = [
            (command, timeout) for command, timeout in observed if f"-test.run=^{name}$" in command
        ]
        assert len(matching) == 1
        command, timeout = matching[0]
        assert f"-test.timeout={next_gate.RACE_LONG_TEST_TIMEOUT}" in command
        assert timeout == next_gate.RACE_LONG_COMMAND_TIMEOUT_SECONDS
    regular = [
        (command, timeout) for command, timeout in observed if "-test.run=^TestRegular$" in command
    ]
    assert len(regular) == 1
    assert "-test.timeout=5m" in regular[0][0]
    assert regular[0][1] == next_gate.RACE_COMMAND_TIMEOUT_SECONDS


def test_formula_backfill_scale_retains_capacity_and_bounds_race_cost() -> None:
    integration = next_gate.SIDECAR_DIR / "tests" / "integration"
    normal = (integration / "formula_backfill_scale_default_test.go").read_text(encoding="utf-8")
    race = (integration / "formula_backfill_scale_race_test.go").read_text(encoding="utf-8")

    assert "//go:build !race" in normal
    assert "formulaBackfillScaleRows = 10_000" in normal
    assert "//go:build race" in race
    assert "formulaBackfillScaleRows = 1_000" in race
    assert "TestFormulaBackfillScaleCancelsResumesWithoutDuplicateAudit" in (
        next_gate.RACE_LONG_TESTS
    )


def test_race_stage_isolates_named_tests_in_every_package(
    monkeypatch,
    tmp_path: Path,
) -> None:
    observed: list[tuple[list[str], str]] = []
    migration_dir = tmp_path / "migrations"
    integration_dir = tmp_path / "integration"
    migration_dir.mkdir()
    integration_dir.mkdir()
    monkeypatch.setattr(next_gate, "RACE_BINARY_DIR", tmp_path / "race-binaries", raising=False)

    def fake_command(command, *, cwd, environment, timeout, race_build=False):
        assert race_build == (command[1:4] == ["test", "-c", "-race"])
        del environment, timeout
        observed.append((command, cwd))
        if command[1] == "list" and command[-1] == "./...":
            return (
                0,
                (
                    f"example/migrations\t{migration_dir}\n"
                    f"example/tests/integration\t{integration_dir}\n"
                ),
                "",
            )
        if command[1:4] == ["test", "-c", "-race"]:
            Path(command[command.index("-o") + 1]).touch()
            return 0, "", ""
        if any(arg.startswith("-test.list=") for arg in command):
            package = cwd
            prefix = "Migration" if cwd == str(migration_dir) else "Integration"
            return 0, f"Test{prefix}One\nTest{prefix}Two\nok\t{package}\n", ""
        return 0, "ok\n", ""

    monkeypatch.setattr(next_gate, "_run_command", fake_command)
    code, _stdout, _stderr = next_gate._run_go_race(
        cwd=str(next_gate.SIDECAR_DIR),
        environment={},
    )

    assert code == 0
    compile_commands = [
        command for command, _cwd in observed if command[1:4] == ["test", "-c", "-race"]
    ]
    assert len(compile_commands) == 2
    assert {command[-1] for command in compile_commands} == {
        "example/migrations",
        "example/tests/integration",
    }
    test_runs = [
        (command, cwd)
        for command, cwd in observed
        if any(argument.startswith("-test.run=") for argument in command)
    ]
    assert len(test_runs) == 4
    assert {
        (
            next(argument for argument in command if argument.startswith("-test.run=")),
            cwd,
        )
        for command, cwd in test_runs
    } == {
        ("-test.run=^TestMigrationOne$", str(migration_dir)),
        ("-test.run=^TestMigrationTwo$", str(migration_dir)),
        ("-test.run=^TestIntegrationOne$", str(integration_dir)),
        ("-test.run=^TestIntegrationTwo$", str(integration_dir)),
    }


def test_race_stage_runs_packages_in_parallel_but_tests_within_each_package_serially(
    monkeypatch,
    tmp_path: Path,
) -> None:
    first_dir = tmp_path / "first"
    second_dir = tmp_path / "second"
    third_dir = tmp_path / "third"
    first_dir.mkdir()
    second_dir.mkdir()
    third_dir.mkdir()
    monkeypatch.setattr(next_gate, "RACE_BINARY_DIR", tmp_path / "race-binaries")
    monkeypatch.setattr(next_gate, "RACE_PACKAGE_WORKERS", 2, raising=False)
    barrier = threading.Barrier(2)
    lock = threading.Lock()
    active = 0
    maximum_active = 0
    active_by_cwd: dict[str, int] = {}
    maximum_by_cwd: dict[str, int] = {}
    observed: list[tuple[list[str], str]] = []
    maximum_binaries = 0

    def fake_command(command, *, cwd, environment, timeout, race_build=False):
        assert race_build == (command[1:4] == ["test", "-c", "-race"])
        nonlocal active, maximum_active, maximum_binaries
        del environment, timeout
        with lock:
            observed.append((command, cwd))
        if command[1] == "list" and command[-1] == "./...":
            return (
                0,
                (
                    f"example/first\t{first_dir}\n"
                    f"example/second\t{second_dir}\n"
                    f"example/third\t{third_dir}\n"
                ),
                "",
            )
        if any(arg.startswith("-test.list=") for arg in command):
            return 0, "TestOne\nTestTwo\nok\tpackage\n", ""
        if command[1:4] == ["test", "-c", "-race"]:
            binary = Path(command[command.index("-o") + 1])
            binary.touch()
            with lock:
                maximum_binaries = max(
                    maximum_binaries,
                    len(list(next_gate.RACE_BINARY_DIR.glob("package-*"))),
                )
            return 0, "", ""
        with lock:
            active += 1
            maximum_active = max(maximum_active, active)
            active_by_cwd[cwd] = active_by_cwd.get(cwd, 0) + 1
            maximum_by_cwd[cwd] = max(
                maximum_by_cwd.get(cwd, 0),
                active_by_cwd[cwd],
            )
        try:
            if cwd != str(third_dir):
                with suppress(threading.BrokenBarrierError):
                    barrier.wait(timeout=0.5)
        finally:
            with lock:
                active -= 1
                active_by_cwd[cwd] -= 1
        return 0, "ok\n", ""

    monkeypatch.setattr(next_gate, "_run_command", fake_command)

    code, _stdout, _stderr = next_gate._run_go_race(
        cwd=str(next_gate.SIDECAR_DIR),
        environment={},
    )

    assert code == 0
    assert maximum_active == 2
    assert maximum_by_cwd == {
        str(first_dir): 1,
        str(second_dir): 1,
        str(third_dir): 1,
    }
    compile_indices = {
        command[command.index("-o") + 1]: index
        for index, (command, _cwd) in enumerate(observed)
        if command[1:4] == ["test", "-c", "-race"]
    }
    assert len(compile_indices) == 3
    for index, (command, _cwd) in enumerate(observed):
        if any(argument.startswith("-test.run=") for argument in command):
            assert compile_indices[command[0]] < index
    assert maximum_binaries <= next_gate.RACE_PACKAGE_WORKERS
    assert not list(next_gate.RACE_BINARY_DIR.glob("package-*"))


def test_race_package_plans_are_partitioned_once_by_recorded_duration(tmp_path: Path) -> None:
    def plan(package: str, tests: int) -> next_gate.RacePackagePlan:
        return next_gate.RacePackagePlan(
            package=package,
            commands=[([f"{package}.test"], 60, package) for _index in range(tests)],
            compile_command=None,
            binary=None,
        )

    plans = [plan("heavy", 1), plan("medium", 20), plan("small-a", 5), plan("small-b", 5)]
    weights = {"heavy": 30.0, "medium": 12.0, "small-a": 3.0, "small-b": 3.0}

    first = next_gate._select_race_package_plans(plans, 0, 2, weights)
    second = next_gate._select_race_package_plans(plans, 1, 2, weights)

    first_names = {item.package for item in first}
    second_names = {item.package for item in second}
    assert first_names.isdisjoint(second_names)
    assert first_names | second_names == {item.package for item in plans}
    assert first_names == {"heavy"}
    assert second_names == {"medium", "small-a", "small-b"}


def test_eight_race_shards_compile_only_selected_packages_and_cover_every_name_once(
    monkeypatch,
    tmp_path: Path,
) -> None:
    split = sorted(next_gate.RACE_SPLIT_PACKAGES)
    ordinary = ["example/known", "example/new-a", "example/new-b", "example/new-c"]
    packages = [*split, *ordinary]
    names = [
        "TestAlpha",
        "TestRaceOnly",
        "TestBeta",
        "Example",
        "FuzzInput",
        *sorted(next_gate.RACE_LONG_TESTS),
    ]
    observed: list[tuple[int | None, list[str], str]] = []
    active_shard = None
    monkeypatch.setattr(next_gate, "RACE_BINARY_DIR", tmp_path / "binaries")

    def run(command, *, cwd, **_kwargs):
        observed.append((active_shard, command, cwd))
        if command[1] == "list":
            return (
                0,
                "\n".join(
                    f"{package}\t{tmp_path / str(index)}" for index, package in enumerate(packages)
                ),
                "",
            )
        if command[1:4] == ["test", "-c", "-race"]:
            Path(command[command.index("-o") + 1]).touch()
            return 0, "", ""
        if command[1].startswith("-test.list="):
            return 0, "\n".join(reversed(names)), ""
        return 0, "PASS", ""

    monkeypatch.setattr(next_gate, "_run_command", run)
    expected = {
        (str(tmp_path / str(index)), f"-test.run=^{name}$")
        for index in range(len(packages))
        for name in names
    }
    assignments = []
    for index in range(8):
        active_shard = index
        code, _, stderr = next_gate._run_go_race(
            cwd=str(tmp_path), environment={}, shard=(index, 8)
        )
        assert code == 0, stderr
        assignments.append(
            {
                (cwd, command[-1])
                for lane, command, cwd in observed
                if lane == index and any(arg.startswith("-test.run=") for arg in command)
            }
        )
    assert set.union(*assignments) == expected
    assert sum(map(len, assignments)) == len(expected)
    compiled = [
        command[-1] for _, command, _ in observed if command[1:4] == ["test", "-c", "-race"]
    ]
    assert all(compiled.count(package) == (8 if package in split else 1) for package in packages)
    assert not any("-list" in command for _, command, _ in observed)
    assert not list((tmp_path / "binaries").iterdir())

    previous = [(command, cwd) for lane, command, cwd in observed if lane == 0]
    observed.clear()
    active_shard = 0
    assert next_gate._run_go_race(cwd=str(tmp_path), environment={}, shard=(0, 8))[0] == 0
    assert [(command, cwd) for _, command, cwd in observed] == previous


@pytest.mark.parametrize("empty_kind", ["no-files", "no-names", "missing-binary"])
def test_race_discovery_fails_closed_when_all_packages_have_no_named_tests(
    monkeypatch,
    tmp_path: Path,
    empty_kind: str,
) -> None:
    binary_dir = tmp_path / "binaries"
    binary_dir.mkdir()
    binary = binary_dir / ("package-000.exe" if os.name == "nt" else "package-000.test")
    binary.touch()
    commands = []
    monkeypatch.setattr(next_gate, "RACE_BINARY_DIR", binary_dir)

    def run(command, **_kwargs):
        commands.append(command)
        if command[1] == "list":
            return 0, f"example/empty\t{tmp_path}", ""
        if command[1:4] == ["test", "-c", "-race"]:
            assert not binary.exists(), "stale binary must be removed before compiling"
            if empty_kind == "no-names":
                binary.touch()
            return 0, "? example/empty [no test files]" if empty_kind == "no-files" else "", ""
        return 0, "", ""

    monkeypatch.setattr(next_gate, "_run_command", run)
    code, _, stderr = next_gate._run_go_race(cwd=str(tmp_path), environment={})
    assert code == 1
    assert (
        "zero named Go tests" in stderr
        if empty_kind != "missing-binary"
        else "no test binary" in stderr
    )
    assert not binary.exists()
    assert sum(any(arg.startswith("-test.run=") for arg in cmd) for cmd in commands) == (
        empty_kind == "no-names"
    )


@pytest.mark.parametrize("failure", ["compile", "list", "run", "exception"])
def test_race_package_failure_stops_work_and_cleans_binary(
    monkeypatch, tmp_path: Path, failure: str
) -> None:
    binary = tmp_path / "race.test.exe"
    stop = threading.Event()
    observed = []

    def run(command, **_kwargs):
        observed.append(command)
        if command == ["compile"]:
            binary.touch()
            return (42, "", "compile failed") if failure == "compile" else (0, "", "")
        if command[1].startswith("-test.list="):
            if failure == "exception":
                raise OSError("list interrupted")
            return (43, "", "list failed") if failure == "list" else (0, "TestOne\nTestTwo", "")
        return 44, "", "run failed"

    monkeypatch.setattr(next_gate, "_run_command", run)
    kwargs = {
        "environment": {},
        "stop_event": stop,
        "compile_command": ["compile"],
        "compile_cwd": str(tmp_path),
        "binary": binary,
        "package_cwd": str(tmp_path),
    }
    if failure == "exception":
        with pytest.raises(OSError, match="list interrupted"):
            next_gate._run_race_package([], **kwargs)
    else:
        code, _, _ = next_gate._run_race_package([], **kwargs)
        assert code == {"compile": 42, "list": 43, "run": 44}[failure]
    assert stop.is_set()
    assert not binary.exists()
    assert len(observed) == {"compile": 1, "list": 2, "exception": 2, "run": 3}[failure]


def test_measured_dominant_package_is_split_by_isolated_tests() -> None:
    package = "github.com/vibetable/vibetable/sidecar/tests/integration"
    names = ["TestAlpha", "TestBeta", "TestGamma", *sorted(next_gate.RACE_LONG_TESTS)]
    plan = next_gate.RacePackagePlan(
        package=package,
        commands=[
            (["integration.test.exe", f"-test.run=^{name}$"], 60, "integration") for name in names
        ],
        compile_command=["go", "test", "-c", "-race", package],
        binary=Path("integration.test.exe"),
    )

    first = next_gate._select_race_package_plans([plan], 0, 2, {package: 120.0})
    second = next_gate._select_race_package_plans([plan], 1, 2, {package: 120.0})

    assert len(first) == len(second) == 1
    assert first[0].package == second[0].package == package
    assert first[0].compile_command == second[0].compile_command == plan.compile_command
    first_commands = {tuple(command) for command, _timeout, _cwd in first[0].commands}
    second_commands = {tuple(command) for command, _timeout, _cwd in second[0].commands}
    assert first_commands.isdisjoint(second_commands)
    assert first_commands | second_commands == {
        tuple(command) for command, _timeout, _cwd in plan.commands
    }


def test_race_lanes_pass_distinct_shard_coordinates(monkeypatch, tmp_path: Path) -> None:
    observed: list[tuple[int, int] | None] = []

    def fake_stage(
        stage,
        package_root=None,
        package_archive=None,
        *,
        race_shard=None,
    ):
        del package_root, package_archive
        observed.append(race_shard)
        return next_gate.StageResult(stage, ["test"], 0, 0.1, "", "", str(tmp_path))

    monkeypatch.setattr(next_gate, "run_stage", fake_stage)

    for lane in next_gate.RACE_LANES:
        next_gate.run_lane(lane, tmp_path, tmp_path / "candidate.zip")

    assert observed == [(index, 8) for index in range(8)]


def test_race_package_emits_structured_timing_for_lane_balancing(monkeypatch) -> None:
    timestamps = iter((10.0, 12.5))
    monkeypatch.setattr(next_gate.time, "monotonic", lambda: next(timestamps))
    monkeypatch.setattr(
        next_gate,
        "_run_command",
        lambda *_args, **_kwargs: (0, "ok\n", ""),
    )

    code, stdout, _stderr = next_gate._run_race_package(
        [(["race.test.exe", "-test.run=^TestExample$"], 60, "package")],
        environment={},
        stop_event=threading.Event(),
        package_name="example/package",
    )

    assert code == 0
    timing = next(
        json.loads(line.removeprefix("RACE_PACKAGE_TIMING "))
        for line in stdout
        if line.startswith("RACE_PACKAGE_TIMING ")
    )
    assert timing == {
        "elapsedSeconds": 2.5,
        "package": "example/package",
        "testCount": 1,
    }


def test_compiled_race_test_retries_only_the_known_windows_cleanup_flake(
    monkeypatch,
) -> None:
    monkeypatch.setattr(next_gate.os, "name", "nt")
    calls = 0
    cleanup = (
        "--- FAIL: TestExample\n"
        "    testing.go:1464: TempDir RemoveAll cleanup: unlinkat path: "
        "The directory is not empty.\n"
    )

    def fake_command(command, *, cwd, environment, timeout, race_build=False):
        assert race_build == (command[1:4] == ["test", "-c", "-race"])
        nonlocal calls
        del command, cwd, environment, timeout
        calls += 1
        if calls == 1:
            return 1, cleanup, ""
        return 0, "ok\n", ""

    monkeypatch.setattr(next_gate, "_run_command", fake_command)
    stop_event = threading.Event()

    code, stdout, _stderr = next_gate._run_race_package(
        [(["race.test.exe", "-test.run=^TestExample$"], 60, "package")],
        environment={},
        stop_event=stop_event,
    )

    assert code == 0
    assert calls == 2
    assert "attempt 2/3" in "\n".join(stdout)
    assert not stop_event.is_set()


def test_compiled_race_test_never_retries_a_real_data_race(
    monkeypatch,
) -> None:
    monkeypatch.setattr(next_gate.os, "name", "nt")
    calls = 0

    def fake_command(command, *, cwd, environment, timeout, race_build=False):
        assert race_build == (command[1:4] == ["test", "-c", "-race"])
        nonlocal calls
        del command, cwd, environment, timeout
        calls += 1
        return 66, "WARNING: DATA RACE\n", ""

    monkeypatch.setattr(next_gate, "_run_command", fake_command)
    stop_event = threading.Event()

    code, _stdout, _stderr = next_gate._run_race_package(
        [(["race.test.exe", "-test.run=^TestExample$"], 60, "package")],
        environment={},
        stop_event=stop_event,
    )

    assert code == 66
    assert calls == 1
    assert stop_event.is_set()


def test_windows_tempdir_cleanup_retry_never_matches_data_race(
    monkeypatch,
) -> None:
    monkeypatch.setattr(next_gate.os, "name", "nt")
    cleanup = (
        "--- FAIL: TestExample\n"
        "    testing.go:1464: TempDir RemoveAll cleanup: unlinkat path: "
        "The directory is not empty.\n"
    )
    assert next_gate._is_windows_tempdir_cleanup_flake(cleanup)
    assert not next_gate._is_windows_tempdir_cleanup_flake(cleanup + "\nWARNING: DATA RACE\n")
    assert not next_gate._is_windows_tempdir_cleanup_flake(
        cleanup + "\n    product_test.go:42: assertion failed\n"
    )


@pytest.mark.parametrize(
    "toolchain_failure",
    [
        "fatal error: out of memory\n",
        "[build failed]\n",
        "fork/exec C:\\go\\compile.exe: Access is denied.\n",
        "The paging file is too small for this operation to be completed.\n",
    ],
)
def test_windows_tempdir_cleanup_retry_never_matches_mixed_toolchain_failure(
    monkeypatch,
    toolchain_failure: str,
) -> None:
    monkeypatch.setattr(next_gate.os, "name", "nt")
    cleanup = (
        "--- FAIL: TestExample\n"
        "    testing.go:1464: TempDir RemoveAll cleanup: unlinkat path: "
        "The directory is not empty.\n"
    )

    assert not next_gate._is_windows_tempdir_cleanup_flake(cleanup + toolchain_failure)


@pytest.mark.parametrize("stage", ["go-test", "go-coverage"])
def test_go_stage_never_retries_mixed_cleanup_and_toolchain_failure(
    monkeypatch,
    stage: str,
) -> None:
    monkeypatch.setattr(next_gate.os, "name", "nt")
    monkeypatch.setattr(
        next_gate,
        "stage_command",
        lambda _stage, package_root=None: (["go", "test", "./..."], "repo"),
    )
    monkeypatch.setattr(
        next_gate,
        "_stage_environment",
        lambda _stage, _command, package_root=None: {},
    )
    calls = 0

    def run(*_args, **_kwargs):
        nonlocal calls
        calls += 1
        return (
            1,
            "--- FAIL: TestExample\n"
            "    testing.go:1464: TempDir RemoveAll cleanup: unlinkat path: "
            "The directory is not empty.\n"
            "fatal error: out of memory\n",
            "fork/exec C:\\go\\pkg\\tool\\compile.exe: The paging file is too small "
            "for this operation to be completed.\n",
        )

    monkeypatch.setattr(next_gate, "_run_command", run)
    result = next_gate.run_stage(stage)

    assert result.returncode == 1
    assert calls == 1
    assert "attempt 2/3" not in result.stdout


def test_fault_injection_evidence_uses_isolated_gate_temp(
    monkeypatch,
    tmp_path: Path,
) -> None:
    monkeypatch.setattr(next_gate, "QA_RUN_TEMP_DIR", tmp_path)
    command, _cwd = next_gate.stage_command("fault-injection")

    environment = next_gate._stage_environment("fault-injection", command)

    assert environment["VIBETABLE_FAULT_EVIDENCE_ROOT"] == str(tmp_path / "fault-injection")


def test_fault_injection_uses_the_selected_immutable_candidate(tmp_path: Path) -> None:
    package_root = tmp_path / "candidate"

    command, _cwd = next_gate.stage_command("fault-injection", package_root)

    assert command == [
        next_gate.sys.executable,
        str(next_gate.FAULT_INJECTION),
        "--package-root",
        str(package_root),
    ]


@pytest.mark.parametrize("stage", ["go-test", "go-coverage"])
def test_go_stage_retries_only_the_narrow_windows_tempdir_cleanup_flake(
    monkeypatch,
    stage: str,
) -> None:
    monkeypatch.setattr(next_gate.os, "name", "nt")
    monkeypatch.setattr(
        next_gate,
        "stage_command",
        lambda _stage, package_root=None: (["go", "test", "./..."], "repo"),
    )
    monkeypatch.setattr(
        next_gate,
        "_stage_environment",
        lambda _stage, _command, package_root=None: {},
    )
    calls = 0

    def run(*_args, **_kwargs):
        nonlocal calls
        calls += 1
        if calls < next_gate.WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS:
            return (
                1,
                "--- FAIL: TestExample\n"
                "    testing.go:1464: TempDir RemoveAll cleanup: unlinkat path: "
                "The directory is not empty.\n",
                "",
            )
        return 0, "ok\n", ""

    monkeypatch.setattr(next_gate, "_run_command", run)
    result = next_gate.run_stage(stage)

    assert result.returncode == 0
    assert calls == next_gate.WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS
    assert "attempt 2/3" in result.stdout
    assert "attempt 3/3" in result.stdout


def test_run_command_timeout_terminates_process_tree(
    monkeypatch,
) -> None:
    class TimedOutProcess:
        pid = 321
        returncode = None
        calls = 0

        def communicate(self, timeout=None):
            self.calls += 1
            if self.calls == 1:
                raise subprocess.TimeoutExpired(["test"], timeout)
            return "", ""

        def poll(self):
            return self.returncode

    process = TimedOutProcess()
    terminated: list[object] = []
    monkeypatch.setattr(next_gate.subprocess, "Popen", lambda *_a, **_k: process)
    monkeypatch.setattr(
        next_gate,
        "_terminate_process_tree",
        lambda observed: terminated.append(observed),
    )

    code, _stdout, stderr = next_gate._run_command(
        ["test"],
        cwd=str(next_gate.REPO_ROOT),
        environment={},
        timeout=1,
    )

    assert code == next_gate.TIMEOUT_RETURNCODE
    assert "timed out" in stderr
    assert terminated == [process]


def test_stage_report_is_never_release_eligible(
    monkeypatch,
    tmp_path: Path,
    capsys,
) -> None:
    qa_temp = tmp_path / "qa-success"
    qa_temp.mkdir()
    monkeypatch.setattr(next_gate, "QA_RUN_TEMP_DIR", qa_temp)
    identity = {"sidecar": "a" * 64}
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "load_dependencies",
        lambda: {},
    )
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "artifact_hashes",
        lambda _deps: identity,
    )
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "release_source_hash",
        lambda _deps: "s" * 64,
    )
    monkeypatch.setattr(
        next_gate,
        "run_stage",
        lambda stage, package_root=None: next_gate.StageResult(
            stage=stage,
            command=["test"],
            returncode=0,
            elapsed=0.01,
            stdout="stage stdout\n",
            stderr="stage stderr\n",
            cwd=str(next_gate.REPO_ROOT),
        ),
    )
    report = tmp_path / "stage.json"

    assert next_gate.main(["--stage", "go-test", "--json-report", str(report)]) == 0
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["ok"] is True
    assert payload["releaseEligible"] is False
    assert payload["commit"] == "c" * 40
    assert payload["artifactHashes"] == identity
    assert payload["sourceHash"] == "s" * 64
    captured = capsys.readouterr()
    assert captured.out == "stage stdout\n"
    assert captured.err == "stage stderr\n"
    assert not qa_temp.exists()
    assert next_gate.QA_RUN_TEMP_DIR is None


def test_full_ci_report_is_release_eligible_only_when_identity_stays_stable(
    monkeypatch,
    tmp_path: Path,
    capsys,
) -> None:
    qa_temp = tmp_path / "qa-failure"
    qa_temp.mkdir()
    monkeypatch.setattr(next_gate, "QA_RUN_TEMP_DIR", qa_temp)
    hashes = iter(({"sidecar": "a" * 64}, {"sidecar": "b" * 64}))
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "artifact_hashes",
        lambda _deps: next(hashes),
    )
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "release_source_hash",
        lambda _deps: "s" * 64,
    )
    monkeypatch.setattr(
        next_gate,
        "run_ci",
        lambda package_root=None, package_archive=None: (
            0,
            [
                next_gate.StageResult(
                    "smoke",
                    ["pytest"],
                    0,
                    0.01,
                    "1 passed",
                    "",
                    str(tmp_path),
                    "required-passed",
                )
            ],
        ),
    )
    report = tmp_path / "ci.json"

    assert next_gate.main(["--ci", *_candidate_args(tmp_path), "--json-report", str(report)]) == 1
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["ok"] is False
    assert payload["releaseEligible"] is False
    assert qa_temp.is_dir()
    assert f"QA failure evidence retained at {qa_temp}" in capsys.readouterr().err


def test_main_reports_retained_evidence_when_a_stage_raises(
    monkeypatch,
    tmp_path: Path,
    capsys,
) -> None:
    qa_temp = tmp_path / "qa-exception"
    qa_temp.mkdir()
    monkeypatch.setattr(next_gate, "QA_RUN_TEMP_DIR", qa_temp)
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(next_gate.handoff_gate, "artifact_hashes", lambda _deps: {})
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "release_source_hash",
        lambda _deps: "s" * 64,
    )

    def raise_stage(_stage, _package_root=None):
        raise RuntimeError("stage exploded")

    monkeypatch.setattr(next_gate, "run_stage", raise_stage)

    with pytest.raises(RuntimeError, match="stage exploded"):
        next_gate.main(["--stage", "go-test"])

    assert qa_temp.is_dir()
    assert f"QA failure evidence retained at {qa_temp}" in capsys.readouterr().err


@pytest.mark.parametrize("helper", [False, True])
def test_product_e2e_failure_evidence_copies_scenario_desktop_log_only(
    helper: bool,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260817T010203Z"
    failed_id = "17-interface-lifecycle"
    failed_root = run_root / failed_id
    failed_root.mkdir(parents=True)
    report = {
        "status": "failed",
        "scenarios": [{"scenario": failed_id, "status": "failed"}],
    }
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(report),
        encoding="utf-8",
    )
    (failed_root / "runner-stdout.log").write_text("runner", encoding="utf-8")
    log_prefix = "preview-1234" if helper else "desktop"
    runtime_relative = Path("controls/preview-host") if helper else Path("host")
    runtime_root = run_root / "_runtime" / "17" / runtime_relative
    runtime_root.mkdir(parents=True)
    (runtime_root / "vibetable-trace.log").write_text("trace", encoding="utf-8")
    desktop_logs = runtime_root / "desktop-logs"
    desktop_logs.mkdir()
    (desktop_logs / f"{log_prefix}.log").write_text("current host diagnostics", encoding="utf-8")
    (desktop_logs / f"{log_prefix}-20260816-010203040.log").write_text(
        "rotated one", encoding="utf-8"
    )
    (desktop_logs / f"{log_prefix}-20260817-050607089.log").write_text(
        "rotated two", encoding="utf-8"
    )
    (desktop_logs / f"{log_prefix}-notes.log").write_text("unrelated", encoding="utf-8")
    (desktop_logs / f"{log_prefix}-20260816-01020304x.log").write_text(
        "bad stamp", encoding="utf-8"
    )
    nested = desktop_logs / "nested-logs"
    nested.mkdir()
    (nested / f"{log_prefix}-20260818-000000000.log").write_text(
        "nested must not be read", encoding="utf-8"
    )
    (runtime_root / f"{log_prefix}.log").write_text("stale readiness-root log", encoding="utf-8")
    outside_root = tmp_path / "elsewhere"
    outside_root.mkdir()
    (outside_root / f"{log_prefix}.log").write_text("external user log", encoding="utf-8")
    # Same-name re-archive overwrites like every other evidence path in this function.
    archived_runtime = (
        tmp_path / "destination" / run_root.name / "_runtime" / "17" / runtime_relative
    )
    archived_runtime.mkdir(parents=True)
    (archived_runtime / f"{log_prefix}-20260816-010203040.log").write_text(
        "archived first-phase evidence", encoding="utf-8"
    )
    copied_sources: list[Path] = []
    original_copy = next_gate._copy_if_file

    def recording_copy(source: Path, destination: Path) -> bool:
        copied_sources.append(source)
        return original_copy(source, destination)

    monkeypatch.setattr(next_gate, "_copy_if_file", recording_copy)

    destination = next_gate.persist_product_e2e_evidence(
        source_root,
        tmp_path / "destination",
    )

    assert destination is not None
    copied_runtime = destination / "_runtime" / "17" / runtime_relative
    assert (copied_runtime / f"{log_prefix}.log").read_text(encoding="utf-8") == (
        "current host diagnostics"
    )
    assert (copied_runtime / f"{log_prefix}-20260817-050607089.log").read_text(
        encoding="utf-8"
    ) == "rotated two"
    assert (copied_runtime / f"{log_prefix}-20260816-010203040.log").read_text(
        encoding="utf-8"
    ) == "rotated one"
    assert sorted(path.name for path in copied_runtime.glob(f"{log_prefix}*")) == [
        f"{log_prefix}-20260816-010203040.log",
        f"{log_prefix}-20260817-050607089.log",
        f"{log_prefix}.log",
    ]
    assert not (copied_runtime / f"{log_prefix}-notes.log").exists()
    assert not (copied_runtime / f"{log_prefix}-20260816-01020304x.log").exists()
    assert not (copied_runtime / "nested-logs").exists()
    assert not list(destination.rglob(f"{log_prefix}-20260818-000000000.log"))
    assert copied_sources, "failure evidence must have been archived"
    assert all(run_root == source or run_root in source.parents for source in copied_sources), (
        "scenario evidence must only be read from the scenario run directory"
    )


@pytest.mark.parametrize("helper", [False, True])
def test_product_e2e_failure_evidence_without_desktop_logs_still_archives_report_and_trace(
    helper: bool,
    tmp_path: Path,
) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260817T010203Z"
    failed_id = "17-interface-lifecycle"
    failed_root = run_root / failed_id
    failed_root.mkdir(parents=True)
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(
            {"status": "failed", "scenarios": [{"scenario": failed_id, "status": "failed"}]}
        ),
        encoding="utf-8",
    )
    log_prefix = "preview-1234" if helper else "desktop"
    runtime_relative = Path("controls/preview-host") if helper else Path("host")
    runtime_root = run_root / "_runtime" / "17" / runtime_relative
    runtime_root.mkdir(parents=True)
    (runtime_root / "vibetable-trace.log").write_text("trace", encoding="utf-8")
    assert not (runtime_root / "desktop-logs").exists()

    destination = next_gate.persist_product_e2e_evidence(source_root, tmp_path / "destination")

    assert destination is not None
    assert (destination / "product-e2e-report.json").is_file()
    copied_runtime = destination / "_runtime" / "17" / runtime_relative
    assert (copied_runtime / "vibetable-trace.log").read_text(encoding="utf-8") == "trace"
    assert not any(copied_runtime.glob(f"{log_prefix}*"))


def _create_junction(link: Path, target: Path) -> None:
    subprocess.run(
        [
            os.environ.get("COMSPEC", "cmd.exe"),
            "/d",
            "/c",
            "mklink",
            "/J",
            str(link),
            str(target),
        ],
        check=True,
        capture_output=True,
    )


@pytest.mark.parametrize("helper", [False, True])
def test_product_e2e_failure_evidence_rejects_a_junctioned_desktop_logs_directory(
    helper: bool,
    tmp_path: Path,
) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260817T010203Z"
    failed_id = "17-interface-lifecycle"
    failed_root = run_root / failed_id
    failed_root.mkdir(parents=True)
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(
            {"status": "failed", "scenarios": [{"scenario": failed_id, "status": "failed"}]}
        ),
        encoding="utf-8",
    )
    log_prefix = "preview-1234" if helper else "desktop"
    runtime_relative = Path("controls/preview-host") if helper else Path("host")
    runtime_root = run_root / "_runtime" / "17" / runtime_relative
    runtime_root.mkdir(parents=True)
    outside = tmp_path / "outside-evidence"
    outside.mkdir()
    (outside / f"{log_prefix}.log").write_text("outside current log", encoding="utf-8")
    (outside / f"{log_prefix}-20260101-010101111.log").write_text(
        "outside rotated log", encoding="utf-8"
    )
    junction = runtime_root / "desktop-logs"
    _create_junction(junction, outside)
    try:
        destination = next_gate.persist_product_e2e_evidence(source_root, tmp_path / "destination")
    finally:
        with suppress(OSError):
            os.rmdir(junction)

    assert destination is not None
    copied_runtime = destination / "_runtime" / "17" / runtime_relative
    assert not any(copied_runtime.glob(f"{log_prefix}*"))
    assert (outside / f"{log_prefix}.log").read_text(encoding="utf-8") == "outside current log"
    assert (outside / f"{log_prefix}-20260101-010101111.log").read_text(encoding="utf-8") == (
        "outside rotated log"
    )


@pytest.mark.parametrize("helper", [False, True])
def test_product_e2e_failure_evidence_rejects_a_symlinked_rotated_log(
    helper: bool,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260817T010203Z"
    failed_id = "17-interface-lifecycle"
    failed_root = run_root / failed_id
    failed_root.mkdir(parents=True)
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(
            {"status": "failed", "scenarios": [{"scenario": failed_id, "status": "failed"}]}
        ),
        encoding="utf-8",
    )
    log_prefix = "preview-1234" if helper else "desktop"
    runtime_relative = Path("controls/preview-host") if helper else Path("host")
    runtime_root = run_root / "_runtime" / "17" / runtime_relative
    runtime_root.mkdir(parents=True)
    desktop_logs = runtime_root / "desktop-logs"
    desktop_logs.mkdir()
    (desktop_logs / f"{log_prefix}.log").write_text("current host diagnostics", encoding="utf-8")
    (desktop_logs / f"{log_prefix}-20260102-020202222.log").write_text(
        "good rotated log", encoding="utf-8"
    )
    linked = desktop_logs / f"{log_prefix}-20260103-030303333.log"
    linked.write_text("link body would follow an external target", encoding="utf-8")
    real_is_symlink = Path.is_symlink

    def precise_is_symlink(path: Path) -> bool:
        return True if path == linked else real_is_symlink(path)

    monkeypatch.setattr(Path, "is_symlink", precise_is_symlink)

    destination = next_gate.persist_product_e2e_evidence(source_root, tmp_path / "destination")

    assert destination is not None
    copied_runtime = destination / "_runtime" / "17" / runtime_relative
    assert (copied_runtime / f"{log_prefix}.log").read_text(
        encoding="utf-8"
    ) == "current host diagnostics"
    assert (copied_runtime / f"{log_prefix}-20260102-020202222.log").read_text(
        encoding="utf-8"
    ) == ("good rotated log")
    assert not (copied_runtime / f"{log_prefix}-20260103-030303333.log").exists()


@pytest.mark.parametrize("report_status", ["passed", "failed"])
def test_product_e2e_passing_s42_retains_only_bounded_preview_logs(
    tmp_path: Path, report_status: str
) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260817T010203Z"
    scenario_id = "42-file-document-native-operations"
    scenario = run_root / scenario_id
    scenario.mkdir(parents=True)
    for filename in (
        "415-native-file-document.png",
        "42-file-document-native-operations-trace.zip",
    ):
        (scenario / filename).write_text("required S42 evidence", encoding="utf-8")
    scenarios = [{"scenario": scenario_id, "status": "passed"}]
    scenarios.append({"scenario": "16-healthy-scenario", "status": "passed"})
    if report_status == "failed":
        scenarios.append({"scenario": "17-interface-lifecycle", "status": "failed"})
    (run_root / "product-e2e-report.json").write_text(
        json.dumps({"status": report_status, "scenarios": scenarios}), encoding="utf-8"
    )
    runtime = run_root / "_runtime" / "42" / "controls" / "preview-host"
    logs = runtime / "desktop-logs"
    logs.mkdir(parents=True)
    retained = ("preview-1234.log", "preview-5678-20260817-010203040.log")
    for filename in (*retained, "preview-0.log", "preview-1234-notes.log", "desktop.log"):
        (logs / filename).write_text(filename, encoding="utf-8")
    (logs / "preview-9999.log").mkdir()
    (runtime / "vibetable-trace.log").write_text("not passing preview evidence", encoding="utf-8")
    workspace_logs = (
        runtime / "local-data" / "workspaces" / "workspace" / ".vibetable" / "temp" / "logs"
    )
    workspace_logs.mkdir(parents=True)
    for filename in next_gate.PRODUCT_E2E_RUNTIME_LOGS:
        (workspace_logs / filename).write_text("not passing preview evidence", encoding="utf-8")
    for relative in ("42/host", "16/controls/preview-host"):
        other = run_root / "_runtime" / relative / "desktop-logs"
        other.mkdir(parents=True)
        (other / "preview-3456.log").write_text("not S42 preview evidence", encoding="utf-8")

    destination = next_gate.persist_product_e2e_evidence(source_root, tmp_path / "destination")

    assert destination is not None
    copied = destination / "_runtime" / "42" / "controls" / "preview-host"
    assert sorted(path.name for path in copied.iterdir()) == sorted(retained)
    for filename in retained:
        assert (copied / filename).read_text(encoding="utf-8") == filename
    assert not (destination / "_runtime" / "42" / "host").exists()
    assert not list(destination.rglob("preview-3456.log"))


@pytest.mark.parametrize("linked_entry", ["desktop-logs", "preview-5678.log"])
@pytest.mark.parametrize("link_kind", ["symlink", "junction"])
def test_product_e2e_passing_s42_rejects_linked_preview_logs(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, linked_entry: str, link_kind: str
) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260817T010203Z"
    scenario_id = "42-file-document-native-operations"
    scenario = run_root / scenario_id
    scenario.mkdir(parents=True)
    for filename in ("415-native-file-document.png", f"{scenario_id}-trace.zip"):
        (scenario / filename).write_text("required S42 evidence", encoding="utf-8")
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(
            {"status": "passed", "scenarios": [{"scenario": scenario_id, "status": "passed"}]}
        ),
        encoding="utf-8",
    )
    runtime = run_root / "_runtime" / "42" / "controls" / "preview-host"
    logs = runtime / "desktop-logs"
    logs.mkdir(parents=True)
    for filename in ("preview-1234.log", "preview-5678.log"):
        (logs / filename).write_text("synthetic linked log", encoding="utf-8")
    linked = logs if linked_entry == "desktop-logs" else logs / linked_entry
    method = f"is_{link_kind}"
    real_check = getattr(Path, method)
    monkeypatch.setattr(Path, method, lambda path: path == linked or real_check(path))

    destination = next_gate.persist_product_e2e_evidence(source_root, tmp_path / "destination")

    assert destination is not None
    copied = list(destination.rglob("preview-*.log"))
    assert [path.name for path in copied] == ([] if linked == logs else ["preview-1234.log"])


@pytest.mark.parametrize("status", ["failed", "passed"])
@pytest.mark.parametrize("linked_directory", ["_runtime", "42", "controls", "preview-host"])
def test_product_e2e_failure_evidence_rejects_linked_preview_host_ancestors(
    tmp_path: Path, linked_directory: str, status: str
) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260817T010203Z"
    failed_id = "42-file-document-native-operations"
    scenario = run_root / failed_id
    scenario.mkdir(parents=True)
    for filename in ("415-native-file-document.png", f"{failed_id}-trace.zip"):
        (scenario / filename).write_text("required S42 evidence", encoding="utf-8")
    (run_root / "product-e2e-report.json").write_text(
        json.dumps({"status": status, "scenarios": [{"scenario": failed_id, "status": status}]}),
        encoding="utf-8",
    )
    parts = ("_runtime", "42", "controls", "preview-host")
    index = parts.index(linked_directory)
    link = run_root.joinpath(*parts[: index + 1])
    link.parent.mkdir(parents=True, exist_ok=True)
    outside = tmp_path / "outside-evidence"
    logs = outside.joinpath(*parts[index + 1 :], "desktop-logs")
    logs.mkdir(parents=True)
    (logs / "preview-1234.log").write_text("outside log", encoding="utf-8")
    _create_junction(link, outside)
    try:
        destination = next_gate.persist_product_e2e_evidence(source_root, tmp_path / "destination")
    finally:
        os.rmdir(link)
    assert destination is not None
    assert not list(destination.rglob("preview-*.log"))
    assert (logs / "preview-1234.log").read_text(encoding="utf-8") == "outside log"


def test_product_e2e_failure_evidence_copies_only_failed_scenario_diagnostics(
    tmp_path: Path,
) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260817T010203Z"
    failed_id = "17-interface-lifecycle"
    passed_id = "16-healthy-scenario"
    failed_root = run_root / failed_id
    passed_root = run_root / passed_id
    failed_root.mkdir(parents=True)
    passed_root.mkdir()
    report = {
        "status": "failed",
        "scenarios": [
            {"scenario": failed_id, "status": "failed"},
            {"scenario": passed_id, "status": "passed"},
        ],
    }
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(report),
        encoding="utf-8",
    )
    for filename in (
        f"{failed_id}-result.json",
        f"{failed_id}-trace.zip",
        f"{failed_id}.png",
        "host-stderr.log",
        "runner-stdout.log",
    ):
        (failed_root / filename).write_text(filename, encoding="utf-8")
    (failed_root / "workspace.db").write_text("do not copy", encoding="utf-8")
    (passed_root / f"{passed_id}-result.json").write_text("passed", encoding="utf-8")
    runtime_root = run_root / "_runtime" / "17" / "host"
    runtime_root.mkdir(parents=True)
    (runtime_root / "vibetable-trace.log").write_text("trace", encoding="utf-8")
    workspace_logs = (
        runtime_root / "local-data" / "workspaces" / "workspace-id" / ".vibetable" / "temp" / "logs"
    )
    workspace_logs.mkdir(parents=True)
    (workspace_logs / "backend.log").write_text("backend", encoding="utf-8")
    (workspace_logs / "pocketbase.log").write_text("pocketbase", encoding="utf-8")

    destination = next_gate.persist_product_e2e_evidence(
        source_root,
        tmp_path / "destination",
    )

    assert destination == tmp_path / "destination" / run_root.name
    assert (destination / "product-e2e-report.json").is_file()
    assert (destination / failed_id / f"{failed_id}-result.json").is_file()
    assert (destination / failed_id / f"{failed_id}-trace.zip").is_file()
    assert (destination / failed_id / f"{failed_id}.png").is_file()
    assert (destination / failed_id / "host-stderr.log").is_file()
    assert (destination / failed_id / "runner-stdout.log").is_file()
    assert not (destination / failed_id / "workspace.db").exists()
    assert not (destination / passed_id).exists()
    copied_runtime = destination / "_runtime" / "17" / "host"
    assert (copied_runtime / "vibetable-trace.log").is_file()
    assert (copied_runtime / "workspace-logs" / "workspace-id" / "backend.log").is_file()
    assert (copied_runtime / "workspace-logs" / "workspace-id" / "pocketbase.log").is_file()


@pytest.mark.parametrize(
    "scenario",
    [
        "11-plugin-mutation",
        "33-host-grid-presentation",
        "39-file-workflow-combination",
        "54-plugin-models",
    ],
)
@pytest.mark.parametrize("failed_phase", ["seed", "resume"])
def test_product_e2e_failure_evidence_retains_host_restart_phases(
    tmp_path: Path,
    failed_phase: str,
    scenario: str,
) -> None:
    run = tmp_path / "source" / "20260921T133903Z"
    run.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": "failed",
                "scenarios": [
                    {
                        "scenario": scenario,
                        "status": "failed",
                        "phases": {failed_phase: {"status": "failed"}},
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    expected = (
        "runner-stderr.log",
        f"{scenario}-result.json",
        f"{scenario}-trace.zip",
        f"{scenario}.png",
    )
    phases = ("seed",) if failed_phase == "seed" else ("seed", "resume")
    for phase in phases:
        source = run / scenario / phase / "20260921T140000Z"
        source.mkdir(parents=True)
        for filename in expected:
            (source / filename).write_text(filename, encoding="utf-8")
        (source / "workspace.db").write_text("private", encoding="utf-8")
    runtime = run / scenario / "persistent" / "host"
    runtime.mkdir(parents=True)
    (runtime / "vibetable-trace.log").write_text("host trace", encoding="utf-8")
    (runtime / "workspace.db").write_text("private", encoding="utf-8")
    (runtime.parent / "seed-state.json").write_text("private", encoding="utf-8")
    destination = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert destination is not None
    for phase in phases:
        retained = destination / scenario / phase / "20260921T140000Z"
        assert sorted(path.name for path in retained.iterdir()) == sorted(expected)
    assert (
        destination / scenario / "persistent" / "host" / "vibetable-trace.log"
    ).read_text() == "host trace"
    assert not list(destination.rglob("workspace.db"))
    assert not list(destination.rglob("seed-state.json"))


def test_product_e2e_failure_evidence_retains_replica_host_phases(tmp_path: Path) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260910T091449Z"
    scenario_id = "24-directory-replica-conflict"
    scenario_root = run_root / scenario_id
    run_root.mkdir(parents=True)
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": "failed",
                "scenarios": [
                    {"scenario": scenario_id, "status": "failed"},
                ],
            }
        ),
        encoding="utf-8",
    )
    expected = (
        f"{scenario_id}-result.json",
        f"{scenario_id}-trace.zip",
        f"{scenario_id}.png",
        "lifecycle.json",
        "readiness.json",
        "runner-stderr.log",
        "stage-result.json",
    )
    for phase in ("seed", "fork", "resolve", "reopen"):
        for host in ("left", "right"):
            phase_root = scenario_root / phase / "hosts" / host
            phase_root.mkdir(parents=True)
            for filename in expected:
                (phase_root / filename).write_text(filename, encoding="utf-8")
            (phase_root / "workspace.db").write_text("private", encoding="utf-8")
            (phase_root / "controls").mkdir()
            (phase_root / "controls" / "workspace-root.txt").write_text("private", encoding="utf-8")
    for host in ("left", "right"):
        runtime = run_root / "_runtime" / "24" / host / "host"
        runtime.mkdir(parents=True)
        (runtime / "vibetable-trace.log").write_text("host trace", encoding="utf-8")
        logs = (
            runtime / "local-data" / "workspaces" / "workspace-id" / ".vibetable" / "temp" / "logs"
        )
        logs.mkdir(parents=True)
        (logs / "backend.log").write_text("backend", encoding="utf-8")
        (logs / "pocketbase.log").write_text("pocketbase", encoding="utf-8")
        (runtime / "local-data" / "workspaces" / "workspace-id" / "workspace.db").write_text(
            "private", encoding="utf-8"
        )
    destination = next_gate.persist_product_e2e_evidence(source_root, tmp_path / "destination")
    assert destination is not None
    for phase in ("seed", "fork", "resolve", "reopen"):
        for host in ("left", "right"):
            retained = destination / scenario_id / phase / "hosts" / host
            assert sorted(path.name for path in retained.iterdir()) == sorted(expected)
            assert not (retained / "workspace.db").exists()
            assert not (retained / "controls").exists()
    for host in ("left", "right"):
        runtime = destination / "_runtime" / "24" / host / "host"
        assert (runtime / "vibetable-trace.log").is_file()
        assert (runtime / "workspace-logs" / "workspace-id" / "backend.log").is_file()
        assert (runtime / "workspace-logs" / "workspace-id" / "pocketbase.log").is_file()
        assert not (runtime / "local-data").exists()


def test_product_e2e_failure_evidence_does_not_copy_replica_layout_for_other_scenarios(
    tmp_path: Path,
) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260910T091449Z"
    scenario_id = "17-interface-lifecycle"
    scenario_root = run_root / scenario_id
    phase_root = scenario_root / "fork" / "hosts" / "left"
    phase_root.mkdir(parents=True)
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(
            {"status": "failed", "scenarios": [{"scenario": scenario_id, "status": "failed"}]}
        ),
        encoding="utf-8",
    )
    (phase_root / "stage-result.json").write_text("stage", encoding="utf-8")
    runtime = run_root / "_runtime" / "17" / "left" / "host"
    runtime.mkdir(parents=True)
    (runtime / "vibetable-trace.log").write_text("trace", encoding="utf-8")

    destination = next_gate.persist_product_e2e_evidence(source_root, tmp_path / "destination")

    assert destination is not None
    assert not (destination / scenario_id / "fork").exists()
    assert not (destination / "_runtime" / "17" / "left").exists()


def test_product_e2e_failure_evidence_finds_nested_fault_injection_run(
    tmp_path: Path,
) -> None:
    source_root = tmp_path / "fault-injection"
    product_run = source_root / "20260817T010000Z" / "real-product" / "20260817T010203Z"
    scenario_id = "10-sse-reconnect"
    scenario_root = product_run / scenario_id
    scenario_root.mkdir(parents=True)
    (product_run / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": "failed",
                "scenarios": [{"scenario": scenario_id, "status": "failed"}],
            }
        ),
        encoding="utf-8",
    )
    (scenario_root / f"{scenario_id}-result.json").write_text(
        "result",
        encoding="utf-8",
    )

    destination = next_gate.persist_product_e2e_evidence(
        source_root,
        tmp_path / "destination",
    )

    assert destination == tmp_path / "destination" / product_run.name
    assert (destination / "product-e2e-report.json").is_file()
    assert (destination / scenario_id / f"{scenario_id}-result.json").is_file()


def test_product_e2e_evidence_copies_only_the_latest_passed_report(
    tmp_path: Path,
) -> None:
    source_root = tmp_path / "source"
    older_run = source_root / "20260817T010000Z"
    latest_run = source_root / "20260817T010203Z"
    scenario_id = "19-gallery-lifecycle"
    scenario_root = latest_run / scenario_id
    scenario_root.mkdir(parents=True)
    older_run.mkdir()
    (older_run / "product-e2e-report.json").write_text(
        json.dumps({"status": "passed", "scenarios": [{"status": "passed"}]}),
        encoding="utf-8",
    )
    report = {
        "status": "passed",
        "scenarios": [{"scenario": scenario_id, "status": "passed"}],
    }
    (latest_run / "product-e2e-report.json").write_text(
        json.dumps(report),
        encoding="utf-8",
    )
    for filename in (
        f"{scenario_id}-result.json",
        f"{scenario_id}-trace.zip",
        f"{scenario_id}.png",
        "host-stderr.log",
        "runner-stdout.log",
    ):
        (scenario_root / filename).write_text(filename, encoding="utf-8")
    runtime_root = latest_run / "_runtime" / "19" / "host"
    runtime_root.mkdir(parents=True)
    (runtime_root / "vibetable-trace.log").write_text("trace", encoding="utf-8")
    workspace_logs = (
        runtime_root / "local-data" / "workspaces" / "workspace-id" / ".vibetable" / "temp" / "logs"
    )
    workspace_logs.mkdir(parents=True)
    (workspace_logs / "backend.log").write_text("backend", encoding="utf-8")
    (workspace_logs / "pocketbase.log").write_text("pocketbase", encoding="utf-8")

    destination = next_gate.persist_product_e2e_evidence(
        source_root,
        tmp_path / "destination",
    )

    expected_destination = tmp_path / "destination" / latest_run.name
    assert destination == expected_destination
    copied_report = expected_destination / "product-e2e-report.json"
    assert json.loads(copied_report.read_text(encoding="utf-8")) == report
    copied_files = [
        path.relative_to(expected_destination)
        for path in expected_destination.rglob("*")
        if path.is_file()
    ]
    assert set(copied_files) == {
        Path("product-e2e-report.json"),
        Path(scenario_id) / f"{scenario_id}.png",
    }
    assert not (tmp_path / "destination" / older_run.name).exists()


@pytest.mark.parametrize(
    "report",
    [
        [],
        {"status": "unknown", "scenarios": []},
        {"status": "passed", "scenarios": [None]},
        {
            "status": "passed",
            "scenarios": [{"scenario": "01-offline-first-launch", "status": "failed"}],
        },
    ],
    ids=("top-level-list", "unknown-status", "non-object-scenario", "failed-under-passed"),
)
def test_product_e2e_evidence_rejects_invalid_report_contract(
    tmp_path: Path,
    report: object,
) -> None:
    run_root = tmp_path / "source" / "20260817T010203Z"
    run_root.mkdir(parents=True)
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(report),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="product E2E report"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
        )


def test_product_e2e_evidence_rejects_failed_report_when_passing_is_required(
    tmp_path: Path,
) -> None:
    run_root = tmp_path / "source" / "20260817T010203Z"
    run_root.mkdir(parents=True)
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": "failed",
                "scenarios": [{"scenario": "01-offline-first-launch", "status": "passed"}],
            }
        ),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="passing report"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
            require_passing_report=True,
        )


@pytest.mark.parametrize(
    "actual_ids",
    [
        ["34-relation-lookup-data-io"],
        ["34-relation-lookup-data-io", "35-data-io-interoperability", "36-extra"],
        ["34-relation-lookup-data-io", "34-relation-lookup-data-io"],
    ],
    ids=("missing", "extra", "duplicate"),
)
def test_passing_product_e2e_report_requires_exact_scenario_evidence(
    tmp_path: Path,
    actual_ids: list[str],
) -> None:
    run_root = tmp_path / "source" / "20260817T010203Z"
    run_root.mkdir(parents=True)
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": "passed",
                "scenarios": [
                    {"scenario": scenario_id, "status": "passed"} for scenario_id in actual_ids
                ],
            }
        ),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="scenario coverage mismatch"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
            require_passing_report=True,
            expected_scenarios=("34-relation-lookup-data-io", "35-data-io-interoperability"),
        )
    archived = tmp_path / "destination" / run_root.name / "product-e2e-report.json"
    assert archived.is_file()
    assert json.loads(archived.read_text(encoding="utf-8"))["scenarios"] == [
        {"scenario": scenario_id, "status": "passed"} for scenario_id in actual_ids
    ]


def test_passing_product_e2e_report_accepts_exact_scenario_evidence(tmp_path: Path) -> None:
    expected = ("34-relation-lookup-data-io", "35-data-io-interoperability")
    run_root = tmp_path / "source" / "20260817T010203Z"
    run_root.mkdir(parents=True)
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": "passed",
                "scenarios": [
                    {"scenario": scenario_id, "status": "passed"} for scenario_id in expected
                ],
            }
        ),
        encoding="utf-8",
    )
    scenario_root = run_root / "35-data-io-interoperability"
    scenario_root.mkdir()
    for filename in SCENARIO_SCREENSHOTS["35-data-io-interoperability"]:
        (scenario_root / filename).write_bytes(b"synthetic unit-test image payload")
    _write_s35_audit_drain_log(run_root, ("business", "file"))

    destination = next_gate.persist_product_e2e_evidence(
        tmp_path / "source",
        tmp_path / "destination",
        require_passing_report=True,
        expected_scenarios=expected,
    )

    assert destination == tmp_path / "destination" / run_root.name
    assert (destination / "product-e2e-report.json").is_file()


def _s35_audit_drain_run(tmp_path: Path, status: str) -> Path:
    run = tmp_path / "source" / "20261021T125835Z"
    run.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": status,
                "scenarios": [{"scenario": "35-data-io-interoperability", "status": status}],
            }
        ),
        encoding="utf-8",
    )
    scenario_root = run / "35-data-io-interoperability"
    scenario_root.mkdir()
    for filename in SCENARIO_SCREENSHOTS["35-data-io-interoperability"]:
        (scenario_root / filename).write_bytes(b"synthetic unit-test image payload")
    return run


def _s35_pocketbase_log_lines() -> list[str]:
    full = json.dumps(
        {
            "durationMs": 12.5,
            "errorCode": None,
            "event": "snapshot.freeze.audit.drain.returned",
            "jobId": None,
            "level": "info",
            "module": "snapshot",
            "operationId": "op-full",
            "requestId": None,
            "sessionEpoch": None,
            "timestamp": "2026-10-21T13:12:44.5471276Z",
            "workspaceId": None,
            "auditDrain": {
                "source": "business",
                "phase": "completed",
                "appended": 3,
                "duplicates": 1,
                "acknowledged": 2,
                "pendingCalls": 4,
                "appendCalls": 5,
                "ackCalls": 6,
                "pbReadWaitCount": 7,
                "pbWriteWaitCount": 8,
                "pbReadInUseDelta": -2,
                "pbWriteInUseDelta": 1,
                "pendingMs": 0.25,
                "appendMs": 1,
                "appendMaxMs": 2.5,
                "ackMs": 0,
                "pbReadWaitMs": 0.5,
                "pbWriteWaitMs": 1.5,
            },
        }
    )
    return [
        "not json 正文 secret customer@example.com",
        '{"event":"snapshot.freeze.audit.drain.returned","timestamp":"2026-10-21T13:12:44.1Z",'
        '"auditDrain":{"source":"secret-source","phase":"ack"}}',
        '{"event":"snapshot.freeze.audit.drain.returned","timestamp":"2026-10-21T13:12:44.2Z",'
        '"auditDrain":{"source":"file","phase":"ack","appended":3,"query":"secret"}}',
        '{"event":"snapshot.freeze.audit.drain.returned","timestamp":"2026-10-21T13:12:44.3Z",'
        '"auditDrain":{"source":"business","phase":"ack","appended":true}}',
        '{"event":"snapshot.freeze.audit.drain.returned","timestamp":"2026-10-21T13:12:44.4Z",'
        '"auditDrain":{"source":"business","phase":"ack","appended":-1}}',
        full,
        '{"event":"snapshot.freeze.audit.drain.returned","timestamp":"2026-10-21T13:12:45.0Z",'
        '"operationId":"op-full","errorCode":null,'
        '"auditDrain":{"source":"file","phase":"completed","appended":2}}',
    ]


_S35_ERROR_CODE_MISSING = object()


def _s35_drain_line(
    source: str,
    phase: str,
    operation_id: str | None,
    error_code: object = _S35_ERROR_CODE_MISSING,
) -> str:
    record: dict[str, object] = {
        "event": "snapshot.freeze.audit.drain.returned",
        "timestamp": "2026-10-21T13:12:44Z",
        "operationId": operation_id,
        "auditDrain": {"source": source, "phase": phase},
    }
    if error_code is not _S35_ERROR_CODE_MISSING:
        record["errorCode"] = error_code
    return json.dumps(record)


def _write_s35_drain_lines(run: Path, lines: list[str]) -> None:
    logs = (
        run
        / "_runtime"
        / "35"
        / "host"
        / "local-data"
        / "workspaces"
        / "ws"
        / ".vibetable"
        / "temp"
        / "logs"
    )
    logs.mkdir(parents=True, exist_ok=True)
    (logs / "pocketbase.log").write_text("\n".join(lines) + "\n", encoding="utf-8")


def _write_s35_audit_drain_log(run: Path, sources: tuple[str, ...]) -> None:
    _write_s35_drain_lines(
        run,
        [_s35_drain_line(source, "completed", "op-close", error_code=None) for source in sources],
    )


@pytest.mark.parametrize(
    "phases",
    [("pending", "ack"), ("append", "between_records")],
    ids=("pending-ack", "append-between-records"),
)
def test_passing_s35_report_rejects_uncompleted_audit_drain_phases(
    tmp_path: Path,
    phases: tuple[str, str],
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    _write_s35_drain_lines(
        run,
        [
            _s35_drain_line("business", phases[0], "op-close", error_code=None),
            _s35_drain_line("file", phases[1], "op-close", error_code=None),
        ],
    )

    with pytest.raises(ValueError, match="audit-drain"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
        )


@pytest.mark.parametrize(
    "error_code",
    ["workspace.snapshot.lock_failed", "drain-error"],
)
def test_passing_s35_report_rejects_audit_drain_entries_with_errors(
    tmp_path: Path,
    error_code: str,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    _write_s35_drain_lines(
        run,
        [
            _s35_drain_line("business", "completed", "op-close", error_code=error_code),
            _s35_drain_line("file", "completed", "op-close", error_code=error_code),
        ],
    )

    with pytest.raises(ValueError, match="audit-drain"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
        )

    # Failed measurements stay archived and never carry errorCode text.
    serialized = (
        tmp_path / "destination" / run.name / "35-data-io-interoperability" / "35-audit-drain.json"
    ).read_text(encoding="utf-8")
    payload = json.loads(serialized)
    assert payload["status"] == "measured"
    assert payload["entriesTotal"] == 2
    assert error_code not in serialized


def test_passing_s35_report_rejects_cross_operation_audit_drain_pairs(
    tmp_path: Path,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    _write_s35_drain_lines(
        run,
        [
            _s35_drain_line("business", "completed", "op-a", error_code=None),
            _s35_drain_line("file", "completed", "op-b", error_code=None),
        ],
    )

    with pytest.raises(ValueError, match="audit-drain"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
        )


def test_passing_s35_report_rejects_audit_drain_pairs_without_operation_ids(
    tmp_path: Path,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    _write_s35_drain_lines(
        run,
        [
            _s35_drain_line("business", "completed", None, error_code=None),
            _s35_drain_line("file", "completed", None, error_code=None),
        ],
    )

    with pytest.raises(ValueError, match="audit-drain"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
        )


def test_passing_s35_report_rejects_audit_drain_entries_missing_error_code(
    tmp_path: Path,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    _write_s35_drain_lines(
        run,
        [
            _s35_drain_line("business", "completed", "op-close"),
            _s35_drain_line("file", "completed", "op-close"),
        ],
    )

    with pytest.raises(ValueError, match="audit-drain"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
        )


@pytest.mark.parametrize(
    "log_sources",
    [None, ("business",), ("file",), ("invalid",)],
    ids=("no-log", "business-only", "file-only", "invalid-only"),
)
def test_passing_s35_report_rejects_incomplete_audit_drain_sources(
    tmp_path: Path,
    log_sources: tuple[str, ...] | None,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    if log_sources is not None:
        _write_s35_audit_drain_log(run, log_sources)

    # require_passing_report defaults to False; a passed S35 without complete
    # closed-audit evidence must still not be treated as passing.
    with pytest.raises(ValueError, match="audit-drain"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
        )

    # The safe projection is archived before the gate rejects the report.
    payload = json.loads(
        (
            tmp_path
            / "destination"
            / run.name
            / "35-data-io-interoperability"
            / "35-audit-drain.json"
        ).read_text(encoding="utf-8")
    )
    assert payload["status"] == (
        "measured" if log_sources in (("business",), ("file",)) else "empty"
    )


def test_passing_s35_report_accepts_a_completed_audit_drain_pair(tmp_path: Path) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    _write_s35_audit_drain_log(run, ("business", "file"))

    destination = next_gate.persist_product_e2e_evidence(
        tmp_path / "source",
        tmp_path / "destination",
    )

    assert destination is not None
    payload = json.loads(
        (destination / "35-data-io-interoperability" / "35-audit-drain.json").read_text(
            encoding="utf-8"
        )
    )
    assert payload["status"] == "measured"
    assert {entry["source"] for entry in payload["entries"]} == {"business", "file"}
    assert all(entry["phase"] == "completed" for entry in payload["entries"])
    assert all(entry["operationId"] == "op-close" for entry in payload["entries"])


@pytest.mark.parametrize("log_sources", [None, ("business",), ("invalid",)])
def test_failed_s35_report_allows_empty_or_partial_audit_drain_evidence(
    tmp_path: Path,
    log_sources: tuple[str, ...] | None,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "failed")
    if log_sources is not None:
        _write_s35_audit_drain_log(run, log_sources)

    destination = next_gate.persist_product_e2e_evidence(
        tmp_path / "source",
        tmp_path / "destination",
    )

    assert destination is not None
    payload = json.loads(
        (destination / "35-data-io-interoperability" / "35-audit-drain.json").read_text(
            encoding="utf-8"
        )
    )
    assert payload["status"] == ("measured" if log_sources == ("business",) else "empty")


def test_passing_s35_audit_drain_source_gate_uses_the_full_scan_beyond_the_cap(
    tmp_path: Path,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    _write_s35_audit_drain_log(run, ("business",) * 64 + ("file",))

    destination = next_gate.persist_product_e2e_evidence(
        tmp_path / "source",
        tmp_path / "destination",
    )

    assert destination is not None
    payload = json.loads(
        (destination / "35-data-io-interoperability" / "35-audit-drain.json").read_text(
            encoding="utf-8"
        )
    )
    assert payload["truncated"] is True
    assert payload["entriesTotal"] == 65
    assert len(payload["entries"]) == 64
    assert {entry["source"] for entry in payload["entries"]} == {"business"}


@pytest.mark.parametrize("status", ["passed", "failed"])
def test_product_e2e_evidence_archives_bounded_s35_audit_drain_measurements(
    tmp_path: Path,
    status: str,
) -> None:
    run = _s35_audit_drain_run(tmp_path, status)
    logs = (
        run
        / "_runtime"
        / "35"
        / "host"
        / "local-data"
        / "workspaces"
        / "3eb51dfa-e249-44e1-bf06-2767f64ffd27"
        / ".vibetable"
        / "temp"
        / "logs"
    )
    logs.mkdir(parents=True)
    (logs / "backend.log").write_text("backend secret body", encoding="utf-8")
    (logs / "pocketbase.log").write_text(
        "\n".join(_s35_pocketbase_log_lines()) + "\n",
        encoding="utf-8",
    )

    destination = next_gate.persist_product_e2e_evidence(
        tmp_path / "source",
        tmp_path / "destination",
    )

    assert destination is not None
    evidence = destination / "35-data-io-interoperability" / "35-audit-drain.json"
    serialized = evidence.read_text(encoding="utf-8")
    payload = json.loads(serialized)
    assert payload["event"] == "snapshot.freeze.audit.drain.returned"
    assert payload["status"] == "measured"
    assert payload["logsRead"] == 1
    assert payload["entriesTotal"] == 2
    assert payload["truncated"] is False
    assert payload["maximumEntries"] == 64
    assert [entry["operationId"] for entry in payload["entries"]] == ["op-full", "op-full"]
    first = payload["entries"][0]
    assert first["source"] == "business"
    assert first["phase"] == "completed"
    assert first["appended"] == 3
    assert first["pbReadInUseDelta"] == -2
    assert first["pbWriteInUseDelta"] == 1
    assert first["pendingMs"] == 0.25
    second = payload["entries"][1]
    assert second["source"] == "file"
    assert second["phase"] == "completed"
    assert set(second) == {"timestamp", "operationId", "source", "phase", "appended"}
    # Dirty bodies, unknown keys, and illegal values never reach the archive.
    for forbidden in ("secret", "正文", "customer@", "query", "backend"):
        assert forbidden not in serialized
    if status == "passed":
        # A passing run never archives the runtime tree or raw logs.
        assert not (destination / "_runtime").exists()
        assert not list(destination.rglob("pocketbase.log"))
        assert not list(destination.rglob("backend.log"))
    else:
        # The existing failure diagnostics logic is unchanged and still
        # archives the fixed runtime log whitelist next to the projection.
        assert (
            destination
            / "_runtime"
            / "35"
            / "host"
            / "workspace-logs"
            / "3eb51dfa-e249-44e1-bf06-2767f64ffd27"
            / "pocketbase.log"
        ).is_file()


def test_product_e2e_evidence_marks_missing_s35_audit_drain_logs_as_empty(
    tmp_path: Path,
) -> None:
    _s35_audit_drain_run(tmp_path, "failed")

    destination = next_gate.persist_product_e2e_evidence(
        tmp_path / "source",
        tmp_path / "destination",
    )

    assert destination is not None
    payload = json.loads(
        (destination / "35-data-io-interoperability" / "35-audit-drain.json").read_text(
            encoding="utf-8"
        )
    )
    assert payload["status"] == "empty"
    assert payload["logsRead"] == 0
    assert payload["entriesTotal"] == 0
    assert payload["truncated"] is False
    assert payload["entries"] == []


@pytest.mark.parametrize(
    "audit_drain",
    [
        {"source": ["business"], "phase": "ack"},
        {"source": {"phase": "business"}, "phase": "ack"},
        {"source": "business", "phase": ["ack"]},
        {"source": "business", "phase": {"source": "ack"}},
        {"source": "business", "phase": "ack", "appended": 2**64},
        {"source": "business", "phase": "ack", "pbReadInUseDelta": -(2**63) - 1},
        {"source": "business", "phase": "ack", "pendingMs": 2**64},
        {"source": "business", "phase": "ack", "pendingMs": float("nan")},
        {"source": "business", "phase": "ack", "pendingMs": float("inf")},
    ],
)
def test_product_e2e_evidence_ignores_illegal_s35_audit_drain_values(
    tmp_path: Path,
    audit_drain: dict[str, object],
) -> None:
    run = _s35_audit_drain_run(tmp_path, "failed")
    logs = (
        run
        / "_runtime"
        / "35"
        / "host"
        / "local-data"
        / "workspaces"
        / "ws"
        / ".vibetable"
        / "temp"
        / "logs"
    )
    logs.mkdir(parents=True)
    (logs / "pocketbase.log").write_text(
        json.dumps(
            {
                "event": "snapshot.freeze.audit.drain.returned",
                "timestamp": "2026-10-21T13:12:44Z",
                "operationId": "op-illegal",
                "auditDrain": audit_drain,
            }
        )
        + "\n",
        encoding="utf-8",
    )

    destination = next_gate.persist_product_e2e_evidence(
        tmp_path / "source",
        tmp_path / "destination",
    )

    assert destination is not None
    evidence = destination / "35-data-io-interoperability" / "35-audit-drain.json"
    serialized = evidence.read_text(encoding="utf-8")
    payload = json.loads(serialized)
    assert payload["status"] == "empty"
    assert payload["logsRead"] == 1
    assert payload["entries"] == []
    assert "op-illegal" not in serialized


def test_product_e2e_evidence_reads_rotated_s35_audit_drain_logs(tmp_path: Path) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    logs = (
        run
        / "_runtime"
        / "35"
        / "host"
        / "local-data"
        / "workspaces"
        / "ws"
        / ".vibetable"
        / "temp"
        / "logs"
    )
    logs.mkdir(parents=True)
    (logs / "pocketbase.log").write_text(
        json.dumps({"event": "sidecar.starting", "timestamp": "2026-10-21T13:12:31Z"}) + "\n",
        encoding="utf-8",
    )
    for filename, source in (
        ("pocketbase-20261021-131244123.log", "business"),
        ("pocketbase-20261022-091500456.log", "file"),
    ):
        (logs / filename).write_text(
            json.dumps(
                {
                    "event": "snapshot.freeze.audit.drain.returned",
                    "timestamp": "2026-10-21T13:12:44Z",
                    "operationId": "op-rotated",
                    "errorCode": None,
                    "auditDrain": {"source": source, "phase": "completed"},
                }
            )
            + "\n",
            encoding="utf-8",
        )

    destination = next_gate.persist_product_e2e_evidence(
        tmp_path / "source",
        tmp_path / "destination",
    )

    assert destination is not None
    payload = json.loads(
        (destination / "35-data-io-interoperability" / "35-audit-drain.json").read_text(
            encoding="utf-8"
        )
    )
    assert payload["status"] == "measured"
    assert payload["logsRead"] == 3
    assert payload["entriesTotal"] == 2
    assert {entry["source"] for entry in payload["entries"]} == {"business", "file"}


def test_product_e2e_evidence_ignores_s35_logs_with_unrecognized_rotation_names(
    tmp_path: Path,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    logs = (
        run
        / "_runtime"
        / "35"
        / "host"
        / "local-data"
        / "workspaces"
        / "ws"
        / ".vibetable"
        / "temp"
        / "logs"
    )
    logs.mkdir(parents=True)

    def drain_line(source: str, operation_id: str) -> str:
        return (
            json.dumps(
                {
                    "event": "snapshot.freeze.audit.drain.returned",
                    "timestamp": "2026-10-21T13:12:44Z",
                    "operationId": operation_id,
                    "auditDrain": {"source": source, "phase": "ack"},
                }
            )
            + "\n"
        )

    (logs / "pocketbase-20261021-131244123.log").write_text(
        drain_line("business", "op-valid"), encoding="utf-8"
    )
    for filename in (
        "pocketbase-secret.log",
        "pocketbase-2026101-131244123.log",
        "pocketbase-20261021-1312441234.log",
    ):
        (logs / filename).write_text(drain_line("file", "op-poison"), encoding="utf-8")

    with pytest.raises(ValueError, match="audit-drain"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
        )

    serialized = (
        tmp_path / "destination" / run.name / "35-data-io-interoperability" / "35-audit-drain.json"
    ).read_text(encoding="utf-8")
    payload = json.loads(serialized)
    assert payload["status"] == "measured"
    assert payload["logsRead"] == 1
    assert payload["entries"][0]["operationId"] == "op-valid"
    assert "op-poison" not in serialized
    assert "pocketbase-secret" not in serialized


def test_product_e2e_evidence_skips_symlinked_rotated_s35_audit_drain_log(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    logs = (
        run
        / "_runtime"
        / "35"
        / "host"
        / "local-data"
        / "workspaces"
        / "ws"
        / ".vibetable"
        / "temp"
        / "logs"
    )
    logs.mkdir(parents=True)

    def drain_line(source: str, operation_id: str) -> str:
        return (
            json.dumps(
                {
                    "event": "snapshot.freeze.audit.drain.returned",
                    "timestamp": "2026-10-21T13:12:44Z",
                    "operationId": operation_id,
                    "auditDrain": {"source": source, "phase": "ack"},
                }
            )
            + "\n"
        )

    (logs / "pocketbase-20261021-131244123.log").write_text(
        drain_line("business", "op-real"), encoding="utf-8"
    )
    linked = logs / "pocketbase-20261022-091500456.log"
    linked.write_text(drain_line("file", "op-linked"), encoding="utf-8")
    real_is_symlink = Path.is_symlink

    def precise_is_symlink(path: Path) -> bool:
        return True if path == linked else real_is_symlink(path)

    monkeypatch.setattr(Path, "is_symlink", precise_is_symlink)

    with pytest.raises(ValueError, match="audit-drain"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
        )

    serialized = (
        tmp_path / "destination" / run.name / "35-data-io-interoperability" / "35-audit-drain.json"
    ).read_text(encoding="utf-8")
    payload = json.loads(serialized)
    assert payload["status"] == "measured"
    assert payload["logsRead"] == 1
    assert payload["entries"][0]["operationId"] == "op-real"
    assert "op-linked" not in serialized


S35_AUDIT_DRAIN_CHAIN = ("workspaces", "ws", ".vibetable", "temp", "logs")


@pytest.mark.parametrize("junctioned", S35_AUDIT_DRAIN_CHAIN)
def test_product_e2e_evidence_skips_s35_audit_drain_logs_behind_junctions(
    tmp_path: Path,
    junctioned: str,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    outside = tmp_path / "outside-evidence"
    index = S35_AUDIT_DRAIN_CHAIN.index(junctioned)
    outside_logs = outside.joinpath(*S35_AUDIT_DRAIN_CHAIN[index + 1 :])
    outside_logs.mkdir(parents=True)
    (outside_logs / "pocketbase.log").write_text(
        json.dumps(
            {
                "event": "snapshot.freeze.audit.drain.returned",
                "timestamp": "2026-10-21T13:12:44Z",
                "operationId": "op-outside",
                "auditDrain": {"source": "business", "phase": "ack"},
            }
        )
        + "\n",
        encoding="utf-8",
    )
    local_data = run / "_runtime" / "35" / "host" / "local-data"
    junction_parent = local_data.joinpath(*S35_AUDIT_DRAIN_CHAIN[:index])
    junction_parent.mkdir(parents=True, exist_ok=True)
    junction = junction_parent / junctioned
    _create_junction(junction, outside)
    try:
        # A passed S35 cannot satisfy the audit-drain gate through a junction:
        # the log is skipped, the projection stays empty, and the gate raises.
        with pytest.raises(ValueError, match="audit-drain"):
            next_gate.persist_product_e2e_evidence(
                tmp_path / "source",
                tmp_path / "destination",
            )
    finally:
        with suppress(OSError):
            os.rmdir(junction)

    evidence = (
        tmp_path / "destination" / run.name / "35-data-io-interoperability" / "35-audit-drain.json"
    )
    serialized = evidence.read_text(encoding="utf-8")
    payload = json.loads(serialized)
    assert payload["status"] == "empty"
    assert payload["logsRead"] == 0
    assert payload["entries"] == []
    assert "op-outside" not in serialized
    assert (outside_logs / "pocketbase.log").read_text(encoding="utf-8").count("op-outside") == 1


def test_product_e2e_evidence_caps_s35_audit_drain_entries_explicitly(
    tmp_path: Path,
) -> None:
    run = _s35_audit_drain_run(tmp_path, "passed")
    logs = (
        run
        / "_runtime"
        / "35"
        / "host"
        / "local-data"
        / "workspaces"
        / "ws"
        / ".vibetable"
        / "temp"
        / "logs"
    )
    logs.mkdir(parents=True)
    lines = [
        json.dumps(
            {
                "event": "snapshot.freeze.audit.drain.returned",
                "timestamp": f"2026-10-21T13:13:{index:02d}Z",
                "operationId": "op-close",
                "errorCode": None,
                "auditDrain": {
                    "source": "business" if index % 2 == 0 else "file",
                    "phase": "completed",
                },
            }
        )
        for index in range(70)
    ]
    (logs / "pocketbase.log").write_text("\n".join(lines) + "\n", encoding="utf-8")

    destination = next_gate.persist_product_e2e_evidence(
        tmp_path / "source",
        tmp_path / "destination",
    )

    assert destination is not None
    payload = json.loads(
        (destination / "35-data-io-interoperability" / "35-audit-drain.json").read_text(
            encoding="utf-8"
        )
    )
    assert payload["status"] == "measured"
    assert payload["entriesTotal"] == 70
    assert payload["truncated"] is True
    assert len(payload["entries"]) == 64
    assert payload["entries"][0]["operationId"] == "op-close"
    assert payload["entries"][-1]["operationId"] == "op-close"


def test_product_e2e_evidence_persists_interrupted_report_and_required_gate_rejects_it(
    tmp_path: Path,
) -> None:
    source_root = tmp_path / "source"
    run_root = source_root / "20260918T081855Z"
    in_progress_id = "12-backup-consistency"
    not_started_id = "13-protection-policy"
    in_progress_root = run_root / in_progress_id
    in_progress_root.mkdir(parents=True)
    interrupted_report = {
        "status": "failed",
        "scenarios": [
            {"scenario": "01-offline-first-start", "status": "passed"},
            {
                "scenario": in_progress_id,
                "status": "failed",
                "error": {
                    "code": "SCENARIO_IN_PROGRESS",
                    "message": "Runner was interrupted while this scenario was executing.",
                },
            },
            {
                "scenario": not_started_id,
                "status": "failed",
                "error": {
                    "code": "SCENARIO_NOT_STARTED",
                    "message": "Runner did not start this scenario.",
                },
            },
        ],
    }
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(interrupted_report),
        encoding="utf-8",
    )
    for filename in (f"{in_progress_id}-result.json", "runner-stdout.log"):
        (in_progress_root / filename).write_text(filename, encoding="utf-8")

    destination = next_gate.persist_product_e2e_evidence(
        source_root,
        tmp_path / "destination",
    )

    assert destination == tmp_path / "destination" / run_root.name
    persisted = json.loads((destination / "product-e2e-report.json").read_text(encoding="utf-8"))
    assert persisted == interrupted_report
    assert (destination / in_progress_id / f"{in_progress_id}-result.json").is_file()
    assert (destination / in_progress_id / "runner-stdout.log").is_file()
    assert not (destination / not_started_id).exists()

    with pytest.raises(ValueError, match="passing report"):
        next_gate.persist_product_e2e_evidence(
            source_root,
            tmp_path / "required-destination",
            require_passing_report=True,
        )


def test_product_e2e_evidence_fails_when_report_copy_is_lost(
    monkeypatch,
    tmp_path: Path,
) -> None:
    run_root = tmp_path / "source" / "20260817T010203Z"
    run_root.mkdir(parents=True)
    (run_root / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": "passed",
                "scenarios": [{"scenario": "01-offline-first-launch", "status": "passed"}],
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(next_gate, "_copy_if_file", lambda *_args: False)

    with pytest.raises(OSError, match="could not copy product E2E report"):
        next_gate.persist_product_e2e_evidence(
            tmp_path / "source",
            tmp_path / "destination",
        )


def test_full_ci_report_rejects_source_change_while_gate_is_running(
    monkeypatch,
    tmp_path: Path,
) -> None:
    source_hashes = iter(("a" * 64, "b" * 64))
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "artifact_hashes",
        lambda _deps: {"sidecar": "d" * 64},
    )
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "release_source_hash",
        lambda _deps: next(source_hashes),
    )
    monkeypatch.setattr(
        next_gate,
        "run_ci",
        lambda package_root=None, package_archive=None: (0, []),
    )
    report = tmp_path / "ci.json"

    assert next_gate.main(["--ci", *_candidate_args(tmp_path), "--json-report", str(report)]) == 1
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["ok"] is False
    assert payload["releaseEligible"] is False
    assert payload["sourceHash"] == "b" * 64


def _prepare_complete_product_reports(
    monkeypatch,
    tmp_path: Path,
) -> dict[str, list[str]]:
    qa_temp = tmp_path / "qa-full"
    monkeypatch.setattr(next_gate, "QA_RUN_TEMP_DIR", qa_temp)
    selected: dict[str, list[str]] = {}
    for stage, directory in (
        ("product-e2e", "p"),
        ("product-e2e-data-io", "d"),
        ("product-e2e-file-io", "f"),
    ):
        selected[stage] = list(next_gate.product_e2e_partition(next_gate.load_scenarios(), stage))
        run_root = qa_temp / directory / "20260817T010203Z"
        run_root.mkdir(parents=True)
        (run_root / "product-e2e-report.json").write_text(
            json.dumps(
                {
                    "status": "passed",
                    "scenarios": [
                        {"scenario": scenario_id, "status": "passed"}
                        for scenario_id in selected[stage]
                    ],
                }
            ),
            encoding="utf-8",
        )
        if "35-data-io-interoperability" in selected[stage]:
            _write_s35_audit_drain_log(run_root, ("business", "file"))
        if "55-plugin-surface-outputs" in selected[stage]:
            _write_output_artifacts(run_root)
        if "48-plugin-applications" in selected[stage]:
            for phase, filenames in {
                "seed": ("48-application-sidebar.png", "48-plugin-applications-trace.zip"),
                "resume": (
                    "48-application-sidebar.png",
                    "48-native-approved.png",
                    "48-native-rejected.png",
                    "48-plugin-print-scope.png",
                    "48-plugin-print-dialog-opened.png",
                    "48-plugin-print-preview-second.png",
                    "48-plugin-print-preview.json",
                    "48-plugin-applications-trace.zip",
                ),
            }.items():
                phase_root = run_root / "48-plugin-applications" / phase / "20260817T010204Z"
                phase_root.mkdir(parents=True)
                for filename in filenames:
                    (phase_root / filename).write_bytes(b"synthetic unit-test image/trace payload")
        if "54-plugin-models" in selected[stage]:
            for phase, filenames in S54_PHASE_EVIDENCE.items():
                phase_root = run_root / "54-plugin-models" / phase / "20260817T010204Z"
                phase_root.mkdir(parents=True)
                for filename in filenames:
                    (phase_root / filename).write_bytes(b"synthetic model qualification evidence")
        if "39-file-workflow-combination" in selected[stage]:
            for phase, filenames in S39_SCREENSHOTS.items():
                phase_root = run_root / "39-file-workflow-combination" / phase / "20260817T010204Z"
                phase_root.mkdir(parents=True)
                for filename in filenames:
                    (phase_root / filename).write_bytes(b"synthetic unit-test image payload")
        if "16-dashboard-lifecycle" in selected[stage]:
            for phase, filenames in S16_SCREENSHOTS.items():
                phase_root = run_root / "16-dashboard-lifecycle" / phase / "20260817T010204Z"
                phase_root.mkdir(parents=True)
                for filename in filenames:
                    (phase_root / filename).write_bytes(b"synthetic unit-test image payload")
        for scenario_id, filenames in {
            **NATIVE_RESTORE_PASS_EVIDENCE,
            **SCENARIO_SCREENSHOTS,
        }.items():
            if scenario_id not in selected[stage]:
                continue
            for filename in filenames:
                target = run_root / scenario_id / filename
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(b"synthetic unit-test image/trace payload")
        if "40-file-history-capacity" in selected[stage]:
            persistent = run_root / "40-file-history-capacity" / "persistent"
            (persistent / "fixtures").mkdir(parents=True)
            for filename in ("producer-stdout.log", "producer-stderr.log"):
                (persistent / filename).write_text("synthetic unit-test log", encoding="utf-8")
            for scale in ("near-limit-9980x9990", "depth-4096"):
                (persistent / "fixtures" / f"{scale}.json").write_text(
                    '{"operations": []}', encoding="utf-8"
                )
    return selected


@pytest.mark.parametrize(
    ("stage", "mutation"),
    [
        ("product-e2e", "missing"),
        ("product-e2e-data-io", "missing"),
        ("product-e2e-data-io", "duplicate"),
        ("product-e2e-data-io", "extra"),
        ("product-e2e-file-io", "missing"),
        ("product-e2e-file-io", "duplicate"),
        ("product-e2e-file-io", "extra"),
    ],
)
def test_full_ci_rejects_incomplete_actual_product_reports(
    monkeypatch,
    tmp_path: Path,
    stage: str,
    mutation: str,
) -> None:
    selected = _prepare_complete_product_reports(monkeypatch, tmp_path)
    ids = selected[stage]
    if mutation == "missing":
        ids.pop()
    elif mutation == "duplicate":
        ids.append(ids[-1])
    else:
        ids.append("36-extra")
    directory = {"product-e2e": "p", "product-e2e-data-io": "d", "product-e2e-file-io": "f"}[stage]
    (
        next_gate.QA_RUN_TEMP_DIR / directory / "20260817T010203Z" / "product-e2e-report.json"
    ).write_text(
        json.dumps(
            {
                "status": "passed",
                "scenarios": [{"scenario": scenario_id, "status": "passed"} for scenario_id in ids],
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(
        next_gate.handoff_gate, "artifact_hashes", lambda _deps: {"sidecar": "d" * 64}
    )
    monkeypatch.setattr(next_gate.handoff_gate, "release_source_hash", lambda _deps: "s" * 64)
    monkeypatch.setattr(
        next_gate,
        "run_ci",
        lambda *_args: (
            0,
            [
                next_gate.StageResult(
                    stage_name,
                    ["test"],
                    0,
                    0.01,
                    "",
                    "",
                    "repo",
                    "required-passed" if stage_name == "smoke" else None,
                )
                for stage_name in next_gate.STAGES
            ],
        ),
    )
    report = tmp_path / "ci.json"

    assert next_gate.main(["--ci", *_candidate_args(tmp_path), "--json-report", str(report)]) == 1
    assert json.loads(report.read_text(encoding="utf-8"))["releaseEligible"] is False


def test_full_ci_report_is_bound_to_stable_release_candidate(
    monkeypatch,
    tmp_path: Path,
) -> None:
    _prepare_complete_product_reports(monkeypatch, tmp_path)
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "artifact_hashes",
        lambda _deps: {"sidecar": "d" * 64},
    )
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "release_source_hash",
        lambda _deps: "s" * 64,
    )
    monkeypatch.setattr(
        next_gate,
        "run_ci",
        lambda package_root=None, package_archive=None: (
            0,
            [
                next_gate.StageResult(
                    stage,
                    ["test"],
                    0,
                    0.01,
                    "passed",
                    "",
                    str(tmp_path),
                    "required-passed" if stage == "smoke" else None,
                )
                for stage in next_gate.STAGES
            ],
        ),
    )
    report = tmp_path / "ci.json"

    assert next_gate.main(["--ci", *_candidate_args(tmp_path), "--json-report", str(report)]) == 0
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["schemaVersion"] == 2
    assert payload["releaseEligible"] is True
    assert payload["releaseCandidate"]["archive"]["sha256"]
    assert payload["releaseCandidate"]["packageTreeSha256"]


def test_full_ci_rejects_smoke_only_result_even_with_webview2_evidence(
    monkeypatch,
    tmp_path: Path,
) -> None:
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(
        next_gate.handoff_gate, "artifact_hashes", lambda _deps: {"sidecar": "d" * 64}
    )
    monkeypatch.setattr(next_gate.handoff_gate, "release_source_hash", lambda _deps: "s" * 64)
    monkeypatch.setattr(
        next_gate,
        "run_ci",
        lambda *_args: (
            0,
            [
                next_gate.StageResult(
                    "smoke", ["test"], 0, 0.01, "passed", "", "repo", "required-passed"
                )
            ],
        ),
    )
    report = tmp_path / "ci.json"

    assert next_gate.main(["--ci", *_candidate_args(tmp_path), "--json-report", str(report)]) == 1
    assert json.loads(report.read_text(encoding="utf-8"))["releaseEligible"] is False


def test_full_ci_report_rejects_missing_required_webview2_evidence(
    monkeypatch,
    tmp_path: Path,
) -> None:
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "artifact_hashes",
        lambda _deps: {"sidecar": "d" * 64},
    )
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "release_source_hash",
        lambda _deps: "s" * 64,
    )
    monkeypatch.setattr(
        next_gate,
        "run_ci",
        lambda package_root=None, package_archive=None: (
            0,
            [next_gate.StageResult("smoke", ["pytest"], 0, 0.01, "1 skipped", "", "repo")],
        ),
    )
    report = tmp_path / "ci.json"

    assert next_gate.main(["--ci", *_candidate_args(tmp_path), "--json-report", str(report)]) == 1
    assert json.loads(report.read_text(encoding="utf-8"))["releaseEligible"] is False


def test_lane_report_is_candidate_bound_but_never_release_eligible(
    monkeypatch,
    tmp_path: Path,
) -> None:
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "artifact_hashes",
        lambda _deps: {"sidecar": "d" * 64},
    )
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "release_source_hash",
        lambda _deps: "s" * 64,
    )
    monkeypatch.setattr(
        next_gate,
        "run_lane",
        lambda lane, package_root, package_archive: (
            0,
            [
                next_gate.StageResult(
                    stage=stage,
                    command=["test"],
                    returncode=0,
                    elapsed=0.01,
                    stdout="",
                    stderr="",
                    cwd=str(next_gate.REPO_ROOT),
                )
                for stage in next_gate.LANE_STAGES[lane]
            ],
        ),
    )
    report = tmp_path / "lane.json"

    assert (
        next_gate.main(
            ["--lane", "race-a", *_candidate_args(tmp_path), "--json-report", str(report)]
        )
        == 0
    )
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["reportKind"] == "lane"
    assert payload["lane"] == "race-a"
    assert payload["ok"] is True
    assert payload["releaseEligible"] is False
    assert payload["releaseCandidate"]["archive"]["sha256"]


def _prepare_product_lane(
    monkeypatch,
    tmp_path: Path,
    *,
    stage: str,
    returncode: int,
) -> Path:
    qa_temp = tmp_path / ("qa-success" if returncode == 0 else "qa-failure")
    source_directory = {
        "product-e2e": "p",
        "product-e2e-data-io": "d",
        "product-e2e-file-io": "f",
        "fault-injection": "fault-injection",
    }[stage]
    (qa_temp / source_directory).mkdir(parents=True)
    monkeypatch.setattr(next_gate, "QA_RUN_TEMP_DIR", qa_temp)
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "artifact_hashes",
        lambda _deps: {"sidecar": "d" * 64},
    )
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "release_source_hash",
        lambda _deps: "s" * 64,
    )
    monkeypatch.setattr(
        next_gate,
        "run_lane",
        lambda lane, package_root, package_archive: (
            returncode,
            [
                next_gate.StageResult(
                    stage=stage,
                    command=["test"],
                    returncode=returncode,
                    elapsed=0.01,
                    stdout="",
                    stderr="",
                    cwd=str(next_gate.REPO_ROOT),
                )
            ],
        ),
    )
    return qa_temp


@pytest.mark.parametrize(
    ("stage", "lane", "source_directory"),
    [
        ("product-e2e", "resilience", "p"),
        ("product-e2e-data-io", "data-io", "d"),
        ("product-e2e-file-io", "file-io", "f"),
        ("fault-injection", "resilience", "fault-injection"),
    ],
)
def test_failed_product_lane_persists_product_diagnostics(
    monkeypatch,
    tmp_path: Path,
    stage: str,
    lane: str,
    source_directory: str,
) -> None:
    qa_temp = _prepare_product_lane(
        monkeypatch,
        tmp_path,
        stage=stage,
        returncode=1,
    )
    observed: list[tuple[Path, Path]] = []

    def persist(
        source: Path,
        destination: Path,
        *,
        require_passing_report: bool,
        expected_scenarios: tuple[str, ...] | None,
    ) -> Path:
        assert not require_passing_report
        if stage == "fault-injection":
            assert expected_scenarios is None
        else:
            assert expected_scenarios == next_gate.product_e2e_partition(
                next_gate.load_scenarios(), stage
            )
        observed.append((source, destination))
        return destination / "20260817T010203Z"

    monkeypatch.setattr(next_gate, "persist_product_e2e_evidence", persist)
    report = tmp_path / "lane-reports" / f"{lane}.json"

    assert (
        next_gate.main(
            [
                "--lane",
                lane,
                *_candidate_args(tmp_path),
                "--json-report",
                str(report),
            ]
        )
        == 1
    )
    assert observed == [
        (
            qa_temp / source_directory,
            next_gate.REPO_ROOT / "build" / "automation" / "lane-evidence" / lane,
        )
    ]
    assert report.is_file()


@pytest.mark.parametrize(
    ("stage", "lane", "source_directory"),
    [
        ("product-e2e", "resilience", "p"),
        ("product-e2e-data-io", "data-io", "d"),
        ("product-e2e-file-io", "file-io", "f"),
    ],
)
def test_successful_product_lane_persists_report_before_qa_cleanup(
    monkeypatch,
    tmp_path: Path,
    stage: str,
    lane: str,
    source_directory: str,
) -> None:
    qa_temp = _prepare_product_lane(
        monkeypatch,
        tmp_path,
        stage=stage,
        returncode=0,
    )
    product_evidence = qa_temp / source_directory
    observed: list[tuple[Path, Path]] = []

    def persist(
        source: Path,
        destination: Path,
        *,
        require_passing_report: bool,
        expected_scenarios: tuple[str, ...] | None,
    ) -> Path:
        assert source.is_dir()
        assert require_passing_report
        assert expected_scenarios == next_gate.product_e2e_partition(
            next_gate.load_scenarios(), stage
        )
        observed.append((source, destination))
        return destination / "20260817T010203Z"

    monkeypatch.setattr(next_gate, "persist_product_e2e_evidence", persist)
    report = tmp_path / "lane-reports" / f"{lane}.json"

    assert (
        next_gate.main(
            [
                "--lane",
                lane,
                *_candidate_args(tmp_path),
                "--json-report",
                str(report),
            ]
        )
        == 0
    )
    assert not qa_temp.exists()
    assert next_gate.QA_RUN_TEMP_DIR is None
    assert report.is_file()
    assert observed == [
        (
            product_evidence,
            next_gate.REPO_ROOT / "build" / "automation" / "lane-evidence" / lane,
        )
    ]


@pytest.mark.parametrize(
    "persistence_failure",
    [None, OSError("copy failed")],
    ids=("report-missing", "copy-error"),
)
def test_successful_product_lane_fails_closed_when_report_cannot_be_persisted(
    monkeypatch,
    tmp_path: Path,
    persistence_failure: OSError | None,
) -> None:
    qa_temp = _prepare_product_lane(
        monkeypatch,
        tmp_path,
        stage="product-e2e",
        returncode=0,
    )

    def persist(*_args, **kwargs):
        assert kwargs == {
            "require_passing_report": True,
            "expected_scenarios": next_gate.product_e2e_partition(
                next_gate.load_scenarios(), "product-e2e"
            ),
        }
        if persistence_failure is not None:
            raise persistence_failure
        return None

    monkeypatch.setattr(next_gate, "persist_product_e2e_evidence", persist)
    report = tmp_path / "lane-reports" / "resilience.json"

    assert (
        next_gate.main(
            [
                "--lane",
                "resilience",
                *_candidate_args(tmp_path),
                "--json-report",
                str(report),
            ]
        )
        == 1
    )
    assert qa_temp.is_dir()
    assert qa_temp == next_gate.QA_RUN_TEMP_DIR
    assert json.loads(report.read_text(encoding="utf-8"))["ok"] is False


def _runtime_candidate_evidence() -> dict[str, object]:
    return {
        "schemaVersion": 2,
        "packageTreeSha256": "a" * 64,
        "packageFileCount": 6,
        "archive": {
            "name": "VibeTable-v1.2.3-win-x64.zip",
            "rootDirectory": "VibeTable",
            "sha256": "b" * 64,
            "size": 1024,
            "treeSha256": "a" * 64,
            "fileCount": 6,
            "checksumFile": "VibeTable-v1.2.3-win-x64.zip.sha256",
        },
    }


def _prepare_runtime_baseline_lane(
    monkeypatch,
    tmp_path: Path,
) -> tuple[Path, Path]:
    qa_temp = tmp_path / "qa-runtime-baseline"
    source_report = qa_temp / "r" / "packaged-runtime-baseline.json"
    source_report.parent.mkdir(parents=True)
    source_report.write_text(
        json.dumps(
            {
                "contractVersion": "1.0",
                "evidenceKind": "packaged-runtime-baseline",
                "status": "passed",
                "coverage": {},
                "identity": {},
                "releaseCandidate": _runtime_candidate_evidence(),
                "sampling": {},
                "workspace": {},
                "firstTable": {},
                "rpcLatency": {},
                "measurements": {},
                "lifecycle": {},
                "errors": [],
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(next_gate, "QA_RUN_TEMP_DIR", qa_temp)
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "artifact_hashes",
        lambda _deps: {"sidecar": "d" * 64},
    )
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "release_source_hash",
        lambda _deps: "s" * 64,
    )
    monkeypatch.setattr(
        next_gate,
        "run_lane",
        lambda lane, package_root, package_archive: (
            0,
            [
                next_gate.StageResult(
                    stage="runtime-baseline",
                    command=["test"],
                    returncode=0,
                    elapsed=0.01,
                    stdout="",
                    stderr="",
                    cwd=str(next_gate.REPO_ROOT),
                )
            ],
        ),
    )
    return qa_temp, source_report


def test_runtime_baseline_evidence_copies_one_exact_closed_report(tmp_path: Path) -> None:
    source = tmp_path / "source" / "packaged-runtime-baseline.json"
    source.parent.mkdir()
    candidate = _runtime_candidate_evidence()
    payload = {
        "contractVersion": "1.0",
        "evidenceKind": "packaged-runtime-baseline",
        "status": "passed",
        "coverage": {},
        "identity": {},
        "releaseCandidate": candidate,
        "sampling": {},
        "workspace": {},
        "firstTable": {},
        "rpcLatency": {},
        "measurements": {},
        "lifecycle": {},
        "errors": [],
    }
    source.write_text(json.dumps(payload), encoding="utf-8")

    destination = next_gate.persist_runtime_baseline_evidence(
        source,
        tmp_path / "destination",
        expected_candidate=candidate,
        require_passing_report=True,
    )

    assert destination == tmp_path / "destination" / source.name
    assert json.loads(destination.read_text(encoding="utf-8")) == payload


@pytest.mark.parametrize(
    "mutation",
    [
        lambda payload: payload.update(evidenceKind="wrong"),
        lambda payload: payload.update(status="unknown"),
        lambda payload: payload.update(status="failed", errors=[]),
        lambda payload: payload.pop("releaseCandidate"),
        lambda payload: payload.update(releaseCandidate={"schemaVersion": 2}),
        lambda payload: payload.update(extra="field"),
    ],
    ids=(
        "wrong-kind",
        "unknown-status",
        "failed-when-passing-required",
        "missing-candidate",
        "different-candidate",
        "extra-field",
    ),
)
def test_runtime_baseline_evidence_rejects_invalid_or_nonpassing_report(
    tmp_path: Path,
    mutation,
) -> None:
    source = tmp_path / "packaged-runtime-baseline.json"
    candidate = _runtime_candidate_evidence()
    payload = {
        "contractVersion": "1.0",
        "evidenceKind": "packaged-runtime-baseline",
        "status": "passed",
        "coverage": {},
        "identity": {},
        "releaseCandidate": candidate,
        "sampling": {},
        "workspace": {},
        "firstTable": {},
        "rpcLatency": {},
        "measurements": {},
        "lifecycle": {},
        "errors": [],
    }
    mutation(payload)
    source.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(ValueError, match="runtime baseline"):
        next_gate.persist_runtime_baseline_evidence(
            source,
            tmp_path / "destination",
            expected_candidate=candidate,
            require_passing_report=True,
        )


def test_runtime_baseline_lane_persists_closed_report_before_qa_cleanup(
    monkeypatch,
    tmp_path: Path,
) -> None:
    qa_temp, source_report = _prepare_runtime_baseline_lane(monkeypatch, tmp_path)
    observed: list[tuple[Path, Path]] = []

    def persist(
        source: Path,
        destination: Path,
        *,
        expected_candidate: dict[str, object] | None,
        require_passing_report: bool,
    ) -> Path:
        assert source == source_report
        assert source.is_file()
        assert require_passing_report
        assert expected_candidate is not None
        assert expected_candidate["schemaVersion"] == 2
        observed.append((source, destination))
        return destination / source.name

    monkeypatch.setattr(next_gate, "persist_runtime_baseline_evidence", persist)
    report = tmp_path / "lane-reports" / "resilience.json"

    assert (
        next_gate.main(
            [
                "--lane",
                "resilience",
                *_candidate_args(tmp_path),
                "--json-report",
                str(report),
            ]
        )
        == 0
    )
    assert observed == [
        (
            source_report,
            next_gate.REPO_ROOT / "build" / "automation" / "lane-evidence" / "resilience",
        )
    ]
    assert not qa_temp.exists()
    assert json.loads(report.read_text(encoding="utf-8"))["ok"] is True


def test_runtime_baseline_lane_rejects_report_for_another_candidate(
    monkeypatch,
    tmp_path: Path,
) -> None:
    qa_temp, _source_report = _prepare_runtime_baseline_lane(monkeypatch, tmp_path)
    report = tmp_path / "lane-reports" / "resilience.json"

    assert (
        next_gate.main(
            [
                "--lane",
                "resilience",
                *_candidate_args(tmp_path),
                "--json-report",
                str(report),
            ]
        )
        == 1
    )
    assert qa_temp.is_dir()
    assert json.loads(report.read_text(encoding="utf-8"))["ok"] is False


@pytest.mark.parametrize(
    "persistence_failure",
    [None, OSError("copy failed")],
    ids=("report-missing", "copy-error"),
)
def test_runtime_baseline_lane_fails_closed_when_report_cannot_be_persisted(
    monkeypatch,
    tmp_path: Path,
    persistence_failure: OSError | None,
) -> None:
    qa_temp, _source_report = _prepare_runtime_baseline_lane(monkeypatch, tmp_path)

    def persist(*_args, **kwargs):
        assert kwargs["require_passing_report"] is True
        assert kwargs["expected_candidate"] is not None
        assert set(kwargs) == {"expected_candidate", "require_passing_report"}
        if persistence_failure is not None:
            raise persistence_failure
        return None

    monkeypatch.setattr(next_gate, "persist_runtime_baseline_evidence", persist)
    report = tmp_path / "lane-reports" / "resilience.json"

    assert (
        next_gate.main(
            [
                "--lane",
                "resilience",
                *_candidate_args(tmp_path),
                "--json-report",
                str(report),
            ]
        )
        == 1
    )
    assert qa_temp.is_dir()
    assert json.loads(report.read_text(encoding="utf-8"))["ok"] is False


def test_full_ci_report_rejects_candidate_mutation(
    monkeypatch,
    tmp_path: Path,
) -> None:
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "artifact_hashes",
        lambda _deps: {"sidecar": "d" * 64},
    )
    monkeypatch.setattr(
        next_gate.handoff_gate,
        "release_source_hash",
        lambda _deps: "s" * 64,
    )
    candidate_args = _candidate_args(tmp_path)
    package_root = Path(candidate_args[1])

    def mutate_candidate(_package_root=None, _package_archive=None):
        (package_root / "candidate.bin").write_bytes(b"mutated")
        return 0, []

    monkeypatch.setattr(next_gate, "run_ci", mutate_candidate)
    report = tmp_path / "ci.json"
    assert next_gate.main(["--ci", *candidate_args, "--json-report", str(report)]) == 1
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["releaseEligible"] is False
    assert payload["releaseCandidate"] is None


def test_release_fault_gate_is_strict_and_precedes_real_product_e2e() -> None:
    assert next_gate.STAGES.index("fault-injection") < next_gate.STAGES.index("product-e2e")
    command, cwd = next_gate.stage_command("fault-injection")
    assert command == [next_gate.sys.executable, str(next_gate.FAULT_INJECTION)]
    assert "--component-only" not in command
    assert Path(cwd) == next_gate.REPO_ROOT

    product_command, product_cwd = next_gate.stage_command("product-e2e")
    assert product_command[:4] == [
        next_gate.sys.executable,
        "qa/product_acceptance.py",
        "--evidence-root",
        str(next_gate.QA_RUN_TEMP_DIR / "p"),
    ]
    assert product_command.count("--scenario") == len(next_gate.load_scenarios()) - len(
        release_eligibility.DATA_IO_SCENARIO_IDS
    ) - len(release_eligibility.FILE_IO_SCENARIO_IDS)
    assert Path(product_cwd) == next_gate.REPO_ROOT


@pytest.mark.parametrize("status", ["passed", "failed"])
@pytest.mark.parametrize("missing_measurement", [False, True])
def test_capacity_evidence_retains_measurements_and_producer_logs(
    tmp_path: Path, status: str, missing_measurement: bool
) -> None:
    source_root = tmp_path / "source"
    run = source_root / "20261001T210000Z"
    scenario_id = "40-file-history-capacity"
    persistent = run / scenario_id / "persistent"
    (persistent / "fixtures").mkdir(parents=True)
    report = {"status": status, "scenarios": [{"scenario": scenario_id, "status": status}]}
    (run / "product-e2e-report.json").write_text(json.dumps(report), encoding="utf-8")
    evidence = {
        "producer-stdout.log": b"real producer partial stdout\n",
        "producer-stderr.log": b"real producer partial stderr\n",
        "fixtures/near-limit-9980x9990.json": b'{"operations":[{"totalAllocBytes":123456}]}',
        "fixtures/depth-4096.json": b'{"operations":[{"totalAllocBytes":654321}]}',
    }
    for name, raw in evidence.items():
        (persistent / name).write_bytes(raw)
    (persistent / "workspace.db").write_bytes(b"must not be archived")
    if missing_measurement:
        missing_name = "fixtures/depth-4096.json"
        (persistent / missing_name).unlink()
        evidence.pop(missing_name)
        if status == "passed":
            with pytest.raises(ValueError, match=r"depth-4096\.json"):
                next_gate.persist_product_e2e_evidence(source_root, tmp_path / "destination")
            return

    retained = next_gate.persist_product_e2e_evidence(source_root, tmp_path / "destination")

    assert retained is not None
    for name, raw in evidence.items():
        (persistent / name).unlink()
        assert (retained / scenario_id / "persistent" / name).read_bytes() == raw
    assert {
        path.relative_to(retained).as_posix() for path in retained.rglob("*") if path.is_file()
    } == {
        "product-e2e-report.json",
        *(f"{scenario_id}/persistent/{name}" for name in evidence),
    }


S54_PHASE_EVIDENCE = {
    "seed": (
        "54-models.json",
        "54-model-native-create.png",
        "54-model-native-map.png",
        "54-model-native-upgrade.png",
        "54-plugin-models-trace.zip",
    ),
    "resume": ("54-models-reopened.json", "54-plugin-models-trace.zip"),
}


@pytest.mark.parametrize("status", ["passed", "failed"])
def test_model_native_diagnostics_are_retained_only_for_failed_phases(
    tmp_path: Path, status: str
) -> None:
    run = tmp_path / "source" / "20261027T050000Z"
    run.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": status,
                "scenarios": [{"scenario": "54-plugin-models", "status": status}],
            }
        ),
        encoding="utf-8",
    )
    fixed = (
        "model-native-combos.json",
        "plugin-native-result.json",
        "plugin-native-observation.json",
    )
    for phase, names in S54_PHASE_EVIDENCE.items():
        directory = run / "54-plugin-models" / phase / "20261027T050001Z"
        directory.mkdir(parents=True)
        for name in (*names, *fixed, "workspace.db", "unrelated.json"):
            (directory / name).write_bytes(f"{phase}:{name}".encode())
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    for phase in S54_PHASE_EVIDENCE:
        destination = retained / "54-plugin-models" / phase / "20261027T050001Z"
        for name in fixed:
            if status == "failed":
                assert (destination / name).read_bytes() == f"{phase}:{name}".encode()
            else:
                assert not (destination / name).exists()
        assert not (destination / "workspace.db").exists()
        assert not (destination / "unrelated.json").exists()


OUTPUT_ARTIFACTS = (
    "small.xlsx",
    "large.xlsx",
    "reprint.xlsx",
    "new-template.xlsx",
    "output-proof.json",
)


def _write_output_artifacts(run: Path) -> None:
    controls = run / "_runtime" / "55" / "controls"
    controls.mkdir(parents=True)
    for filename in OUTPUT_ARTIFACTS:
        (controls / filename).write_bytes(filename.encode())


SCENARIO_SCREENSHOTS = {
    "55-plugin-surface-outputs": (
        "55-output-conformance.png",
        "55-plugin-surface-outputs-trace.zip",
        "55-archive-confirmation.png",
        "55-picker-output-small-cancel.png",
        "55-picker-output-small-select.png",
        "55-picker-output-large-select.png",
        "55-picker-output-reprint-select.png",
        "55-picker-output-next-select.png",
    ),
    "07-attachment-history": ("07-named-comparison.png", "07-named-revisions.png"),
    "19-gallery-lifecycle": ("19-gallery-lifecycle.png",),
    "32-shared-work-calendar": ("32-shared-work-calendar.png",),
    "49-plugin-application-security": (
        "49-application-dark.png",
        "49-application-light.png",
        "49-application-scaled.png",
        "49-application-contrast.png",
        "49-application-contrast-restored.png",
        "49-loop-recovered.png",
        "49-crash-recovered.png",
        "plugin-contrast-original.json",
        "plugin-contrast-restoration.json",
        "49-security-observations.json",
        "49-plugin-application-security-trace.zip",
    ),
    "50-plugin-headless-budget": (
        "50-headless-recovered.png",
        "50-plugin-headless-budget-trace.zip",
    ),
    "53-record-manager": (
        "53-record-manager.json",
        "53-record-manager-overview.png",
        "53-record-manager-list.png",
        "53-record-manager-reopened.png",
        "53-record-manager-trace.zip",
        "53-confirm-create.png",
        "53-confirm-update.png",
        "53-confirm-normalize.png",
        "53-confirm-delete-rejected.png",
        "53-confirm-delete-approved.png",
        "52-picker-export-select.png",
    ),
    "52-plugin-capability-services": (
        "52-capability-services.json",
        "52-capability-services.png",
        "52-ui-context.png",
        "52-plugin-capability-services-trace.zip",
        "52-picker-read-cancel.png",
        "52-picker-read-select.png",
        "52-picker-write-select.png",
        "52-picker-import-select.png",
        "52-picker-export-select.png",
        "52-confirm-update-rejected.png",
        "52-confirm-delete-approved.png",
        "52-confirm-attachment-approved.png",
        "52-confirm-import-approved.png",
    ),
    "51-plugin-application-commit-unknown": (
        "48-native-approved.png",
        "51-commit-recovered.png",
        "51-unknown-commit.json",
        "51-plugin-application-commit-unknown-trace.zip",
    ),
    "02-all-field-schema": (
        "02-edit-undo-redo.png",
        "02-number-display-fixed.png",
        "02-number-display-currency.png",
        "02-number-display-percent.png",
        "02-computed-display-grid.png",
        "02-computed-display-cards.png",
        "02-computed-display-percent-cards.png",
        "02-computed-display-reopened.png",
        "02-auto-number-readonly.png",
    ),
    "12-backup-consistency": (
        "12-dark-table.png",
        "12-dark-modal.png",
        "12-dark-popover.png",
    ),
    "28-relation-delta-preview": (
        "28-contract-grid.png",
        "28-contract-picker-refresh.png",
        "28-contract-cards.png",
        "28-contract-offline-reopened.png",
    ),
    "45-common-field-display": (
        "45-common-field-display.png",
        "45-common-field-display-reopened.png",
        "45-common-field-display-restored.png",
        "45-common-field-display-cards.png",
        "45-common-field-display-lookup-cards.png",
        "45-common-field-display-lookup-kanban.png",
    ),
    "46-dashboard-native-output": (
        "48-plugin-print-scope.png",
        "48-plugin-print-dialog-opened.png",
        "48-plugin-print-preview-second.png",
        "48-plugin-print-preview.json",
        "46-dashboard-native-output-trace.zip",
    ),
    "17-interface-lifecycle": (
        "53-record-manager.json",
        "53-record-manager-overview.png",
        "53-record-manager-list.png",
        "53-record-manager-reopened.png",
        "53-confirm-create.png",
        "53-confirm-update.png",
        "53-confirm-normalize.png",
        "53-confirm-delete-rejected.png",
        "53-confirm-delete-approved.png",
        "52-picker-export-select.png",
        "17-interface-lifecycle-trace.zip",
    ),
    "47-plugin-surface-api": (
        "47-surface-read.png",
        "47-surface-confirm.png",
        "47-surface-saved.png",
        "47-plugin-surface-api-trace.zip",
    ),
    "35-data-io-interoperability": (
        "35-file-preview-progress.png",
        "35-file-preview-cancel.json",
        "35-picker-import-unicode-csv-select.png",
        "35-picker-import-unicode-xlsx-select.png",
        "35-native-import-picker.json",
        "35-import-management-history.png",
        "35-import-management-reopened.png",
        "35-source-import-history.png",
        "35-source-import-targets.png",
        "35-source-import-reopened.png",
        "35-source-import-negative.png",
        "35-source-import-attachment-download.png",
        "35-base-import-cancel.png",
        "35-base-import-cancel.json",
        "35-base-import-window.png",
        "35-base-import-preview.png",
        "35-base-import.json",
        "35-base-import-history.png",
        "35-base-import-reopened.png",
    ),
    "36-backend-import-exit": (
        "36-import-aborted-after-backend-exit.png",
        "36-import-management-worker-exit.png",
    ),
}


def _scenario_screenshot_run(tmp_path: Path, scenario_id: str, status: str) -> Path:
    run = tmp_path / "source" / "20261004T050000Z"
    directory = run / scenario_id
    directory.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps({"status": status, "scenarios": [{"scenario": scenario_id, "status": status}]}),
        encoding="utf-8",
    )
    for filename in SCENARIO_SCREENSHOTS[scenario_id]:
        (directory / filename).write_bytes(filename.encode())
    if scenario_id == "55-plugin-surface-outputs":
        _write_output_artifacts(run)
    (directory / "unrelated.png").write_bytes(b"unrelated")
    (directory / "workspace.db").write_bytes(b"private workspace")
    return run


@pytest.mark.parametrize("scenario_id", SCENARIO_SCREENSHOTS)
def test_scenario_screenshots_archive_only_declared_images(
    tmp_path: Path, scenario_id: str
) -> None:
    run = _scenario_screenshot_run(tmp_path, scenario_id, "passed")
    if scenario_id == "35-data-io-interoperability":
        _write_s35_audit_drain_log(run, ("business", "file"))
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    expected = {"product-e2e-report.json"}
    for filename in SCENARIO_SCREENSHOTS[scenario_id]:
        relative = f"{scenario_id}/{filename}"
        expected.add(relative)
        assert (retained / relative).read_bytes() == filename.encode()
    if scenario_id == "35-data-io-interoperability":
        # The bounded close-audit measurement projection is always archived,
        # for passing and failed S35 runs alike.
        expected.add(f"{scenario_id}/35-audit-drain.json")
    if scenario_id == "55-plugin-surface-outputs":
        for filename in OUTPUT_ARTIFACTS:
            relative = f"_runtime/55/controls/{filename}"
            expected.add(relative)
            assert (retained / relative).read_bytes() == filename.encode()
    assert {
        path.relative_to(retained).as_posix() for path in retained.rglob("*") if path.is_file()
    } == expected


@pytest.mark.parametrize("scenario_id", SCENARIO_SCREENSHOTS)
def test_scenario_screenshots_missing_image_rejects_passed_report(
    tmp_path: Path, scenario_id: str
) -> None:
    run = _scenario_screenshot_run(tmp_path, scenario_id, "passed")
    filename = SCENARIO_SCREENSHOTS[scenario_id][-1]
    (run / scenario_id / filename).unlink()
    with pytest.raises(ValueError, match=filename):
        next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")


def test_output_evidence_requires_saved_artifact(tmp_path: Path) -> None:
    run = _scenario_screenshot_run(tmp_path, "55-plugin-surface-outputs", "passed")
    (run / "_runtime/55/controls/reprint.xlsx").unlink()
    with pytest.raises(ValueError, match=r"reprint\.xlsx"):
        next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")


@pytest.mark.parametrize(
    ("scenario_id", "current", "retired"),
    [
        ("49-plugin-application-security", "49-application-dark.png", "48-native-dark.png"),
        ("50-plugin-headless-budget", "50-headless-recovered.png", "48-native-recovered.png"),
        (
            "51-plugin-application-commit-unknown",
            "51-unknown-commit.json",
            "48-native-cancelled.png",
        ),
    ],
)
def test_plugin_current_surface_and_raw_outcome_cannot_be_replaced_by_retired_chrome(
    tmp_path: Path, scenario_id: str, current: str, retired: str
) -> None:
    run = _scenario_screenshot_run(tmp_path, scenario_id, "passed")
    (run / scenario_id / current).unlink()
    (run / scenario_id / retired).write_bytes(b"obsolete native host row")
    with pytest.raises(ValueError, match=current):
        next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")


def test_plugin_application_restart_archives_current_sidebar_and_native_confirmations(
    tmp_path: Path,
) -> None:
    scenario_id = "48-plugin-applications"
    run = tmp_path / "source" / "20261027T050000Z"
    run.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps(
            {"status": "passed", "scenarios": [{"scenario": scenario_id, "status": "passed"}]}
        ),
        encoding="utf-8",
    )
    phases = {
        "seed": ("48-application-sidebar.png", "48-plugin-applications-trace.zip"),
        "resume": (
            "48-application-sidebar.png",
            "48-native-approved.png",
            "48-native-rejected.png",
            "48-plugin-print-scope.png",
            "48-plugin-print-dialog-opened.png",
            "48-plugin-print-preview-second.png",
            "48-plugin-print-preview.json",
            "48-plugin-applications-trace.zip",
        ),
    }
    expected = {"product-e2e-report.json"}
    for phase, names in phases.items():
        directory = run / scenario_id / phase / "20261027T050001Z"
        directory.mkdir(parents=True)
        for name in names:
            (directory / name).write_bytes(f"{phase}:{name}".encode())
            expected.add(f"{scenario_id}/{phase}/{directory.name}/{name}")
        (directory / "48-native-application.png").write_bytes(b"obsolete native host row")
        (directory / "workspace.db").write_bytes(b"private workspace")

    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    assert {
        file.relative_to(retained).as_posix() for file in retained.rglob("*") if file.is_file()
    } == expected
    # A restart phase must have its own new surface; the old row image cannot stand in for it.
    (run / scenario_id / "resume/20261027T050001Z/48-application-sidebar.png").unlink()
    with pytest.raises(ValueError, match=r"48-application-sidebar\.png"):
        next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "missing-sidebar")


def test_scenario_screenshots_preserve_partial_failed_evidence(tmp_path: Path) -> None:
    scenario_id = "36-backend-import-exit"
    run = _scenario_screenshot_run(tmp_path, scenario_id, "failed")
    present, missing = SCENARIO_SCREENSHOTS[scenario_id]
    (run / scenario_id / missing).unlink()
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    assert (retained / scenario_id / present).read_bytes() == present.encode()
    assert not (retained / scenario_id / missing).exists()
    assert not (retained / scenario_id / "workspace.db").exists()
    assert not (retained / scenario_id / "unrelated.png").exists()


S39_SCREENSHOTS = {
    "seed": ("39-record-file-entry.png", "39-restored-revision-tree.png", "39-restored-search.png"),
    "resume": ("39-resumed.png",),
}


def _s39_screenshot_run(tmp_path: Path, status: str) -> Path:
    run = tmp_path / "source" / "20261001T231000Z"
    run.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": status,
                "scenarios": [
                    {
                        "scenario": "39-file-workflow-combination",
                        "status": status,
                        "screenshot": str(tmp_path / "outside.png"),
                    },
                ],
            }
        ),
        encoding="utf-8",
    )
    for phase, filenames in S39_SCREENSHOTS.items():
        for timestamp in ("20261001T231001Z", "20261001T231002Z", "20261399T235959Z"):
            directory = run / "39-file-workflow-combination" / phase / timestamp
            directory.mkdir(parents=True)
            for filename in filenames:
                (directory / filename).write_bytes(f"{timestamp}:{filename}".encode())
            (directory / "unrelated.png").write_bytes(b"not requested")
            (directory / "workspace.db").write_bytes(b"private")
            (directory / "seed-state.json").write_bytes(b"private")
    (tmp_path / "outside.png").write_bytes(b"outside")
    return run


def test_s39_screenshots_archive_only_the_four_current_phase_images(tmp_path: Path) -> None:
    run = _s39_screenshot_run(tmp_path, "passed")
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    expected = {"product-e2e-report.json"}
    for phase, filenames in S39_SCREENSHOTS.items():
        for filename in filenames:
            relative = f"39-file-workflow-combination/{phase}/20261001T231002Z/{filename}"
            expected.add(relative)
            assert (retained / relative).read_bytes() == f"20261001T231002Z:{filename}".encode()
    assert {
        path.relative_to(retained).as_posix() for path in retained.rglob("*") if path.is_file()
    } == expected


@pytest.mark.parametrize("phase", ["seed", "resume"])
def test_s39_screenshots_missing_current_image_cannot_fall_back_to_old_phase(
    tmp_path: Path,
    phase: str,
) -> None:
    run = _s39_screenshot_run(tmp_path, "passed")
    filename = S39_SCREENSHOTS[phase][0]
    (run / "39-file-workflow-combination" / phase / "20261001T231002Z" / filename).unlink()
    with pytest.raises(ValueError, match=filename):
        next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")


def test_s39_screenshots_missing_resume_phase_rejects_passing_report(tmp_path: Path) -> None:
    run = _s39_screenshot_run(tmp_path, "passed")
    for filename in S39_SCREENSHOTS["resume"]:
        for source in (run / "39-file-workflow-combination" / "resume").glob(f"*/{filename}"):
            source.unlink()
    with pytest.raises(ValueError, match=r"39-resumed\.png"):
        next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")


@pytest.mark.parametrize("raises", [False, True])
def test_s39_screenshots_copy_failure_rejects_passing_report(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    raises: bool,
) -> None:
    run = _s39_screenshot_run(tmp_path, "passed")
    copy = next_gate._copy_if_file

    def fail_image(source: Path, destination: Path) -> bool:
        if source.name == "39-restored-search.png":
            if raises:
                raise OSError("synthetic screenshot copy failure")
            return False
        return copy(source, destination)

    monkeypatch.setattr(next_gate, "_copy_if_file", fail_image)
    with pytest.raises(
        OSError if raises else ValueError,
        match="copy failure" if raises else "39-restored-search.png",
    ):
        next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")


def test_s39_screenshots_failed_report_keeps_only_images_produced_in_current_phase(
    tmp_path: Path,
) -> None:
    run = _s39_screenshot_run(tmp_path, "failed")
    for phase, filenames in S39_SCREENSHOTS.items():
        for filename in filenames:
            if filename != "39-record-file-entry.png":
                (
                    run / "39-file-workflow-combination" / phase / "20261001T231002Z" / filename
                ).unlink()
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    images = {path.relative_to(retained).as_posix() for path in retained.rglob("*.png")}
    assert images == {"39-file-workflow-combination/seed/20261001T231002Z/39-record-file-entry.png"}


S16_SCREENSHOTS = {
    "resume": (
        "16-legacy-layout-warning.png",
        "16-replacement-business-page.png",
        "16-legacy-reconciliation.json",
        "16-dashboard-lifecycle-trace.zip",
    ),
    "reopened": (
        "16-legacy-layout-warning.png",
        "16-replacement-business-page.png",
        "16-legacy-reconciliation.json",
        "16-dashboard-lifecycle-trace.zip",
    ),
}
S16_PHASE_RUNS = {
    "seed": "20261019T013053Z",
    "resume": "20261019T013121Z",
    "reopened": "20261019T013147Z",
}


def _s16_screenshot_run(tmp_path: Path, status: str) -> Path:
    run = tmp_path / "source" / "20261019T013000Z"
    run.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": status,
                "scenarios": [
                    {"scenario": "16-dashboard-lifecycle", "status": status},
                ],
            }
        ),
        encoding="utf-8",
    )
    for phase, latest in S16_PHASE_RUNS.items():
        for timestamp in (latest, "20261019T013059Z"):
            directory = run / "16-dashboard-lifecycle" / phase / timestamp
            directory.mkdir(parents=True)
            (directory / "16-dashboard-lifecycle-result.json").write_bytes(b"result")
            if phase in S16_SCREENSHOTS:
                for filename in S16_SCREENSHOTS[phase]:
                    prefix = "decoy" if timestamp != latest else "current"
                    (directory / filename).write_bytes(f"{prefix}:{timestamp}:{filename}".encode())
            (directory / "unrelated.png").write_bytes(b"unrelated")
            (directory / "workspace.db").write_bytes(b"private")
    (run / "16-dashboard-lifecycle" / "not-a-phase-run").mkdir()
    (tmp_path / "outside.png").write_bytes(b"outside")
    return run


def test_s16_preservation_archives_both_resume_and_reopened_runs(tmp_path: Path) -> None:
    run = _s16_screenshot_run(tmp_path, "passed")
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    expected = {"product-e2e-report.json"}
    for phase, latest in S16_PHASE_RUNS.items():
        if phase not in S16_SCREENSHOTS:
            continue
        for filename in S16_SCREENSHOTS[phase]:
            relative = f"16-dashboard-lifecycle/{phase}/{latest}/{filename}"
            expected.add(relative)
            assert (retained / relative).read_bytes() == f"current:{latest}:{filename}".encode()
    assert {
        path.relative_to(retained).as_posix() for path in retained.rglob("*") if path.is_file()
    } == expected


@pytest.mark.parametrize("phase", ["resume", "reopened"])
@pytest.mark.parametrize(
    "filename",
    [
        "16-legacy-layout-warning.png",
        "16-replacement-business-page.png",
        "16-legacy-reconciliation.json",
        "16-dashboard-lifecycle-trace.zip",
    ],
)
def test_s16_missing_restart_evidence_in_either_phase_rejects_passing_report(
    tmp_path: Path, phase: str, filename: str
) -> None:
    run = _s16_screenshot_run(tmp_path, "passed")
    (run / "16-dashboard-lifecycle" / phase / S16_PHASE_RUNS[phase] / filename).unlink()
    with pytest.raises(ValueError, match=filename):
        next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")


def test_s16_missing_reopened_phase_directory_rejects_passing_report(tmp_path: Path) -> None:
    run = _s16_screenshot_run(tmp_path, "passed")
    (run / "16-dashboard-lifecycle" / "reopened").rename(run.parent / "moved-reopened")
    with pytest.raises(ValueError, match=r"16-legacy-layout-warning\.png"):
        next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")


def test_s46_retired_names_are_neither_required_nor_archived(tmp_path: Path) -> None:
    retired = (
        "46-dashboard-export-csv.png",
        "46-dashboard-export-png.png",
        "46-dashboard-export-cancelled.png",
        "46-dashboard-print-dialog-opened.png",
    )
    run = _scenario_screenshot_run(tmp_path, "46-dashboard-native-output", "passed")
    scenario_directory = run / "46-dashboard-native-output"
    for filename in retired:
        (scenario_directory / filename).write_bytes(b"retired artifact")
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    archived = {
        path.relative_to(retained).as_posix() for path in retained.rglob("*") if path.is_file()
    }
    assert all(f"46-dashboard-native-output/{filename}" not in archived for filename in retired)
    assert all(
        f"46-dashboard-native-output/{filename}" in archived
        for filename in SCENARIO_SCREENSHOTS["46-dashboard-native-output"]
    )


NATIVE_RESTORE_PASS_EVIDENCE = {
    "42-file-document-native-operations": (
        "415-native-file-document.png",
        "42-file-document-native-operations-trace.zip",
    ),
    "44-file-restore-crash": (
        "seed/20261002T010002Z/44-file-restore-crash-before-kill.png",
        "seed/20261002T010002Z/44-file-restore-crash-before-kill-trace.zip",
        "resume/20261002T010002Z/44-file-restore-crash.png",
        "resume/20261002T010002Z/44-file-restore-crash-trace.zip",
    ),
}


def _native_restore_evidence_run(tmp_path: Path, scenario_id: str, status: str) -> Path:
    run = tmp_path / "source" / "20261002T010000Z"
    scenario = run / scenario_id
    scenario.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps({"status": status, "scenarios": [{"scenario": scenario_id, "status": status}]}),
        encoding="utf-8",
    )
    for relative in NATIVE_RESTORE_PASS_EVIDENCE[scenario_id]:
        target = scenario / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(f"raw:{relative}".encode())
        if scenario_id == "44-file-restore-crash":
            for timestamp in ("20261002T010001Z", "20261399T235959Z"):
                old = scenario / relative.replace("20261002T010002Z", timestamp)
                old.parent.mkdir(parents=True, exist_ok=True)
                old.write_bytes(b"must not replace current evidence")
        (target.parent / "unrelated.png").write_bytes(b"unrelated")
        (target.parent / "workspace.db").write_bytes(b"private")
    return run


@pytest.mark.parametrize("scenario_id", list(NATIVE_RESTORE_PASS_EVIDENCE))
def test_native_restore_passing_evidence_retains_only_current_fixed_images_and_traces(
    tmp_path: Path, scenario_id: str
) -> None:
    run = _native_restore_evidence_run(tmp_path, scenario_id, "passed")
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    expected = {"product-e2e-report.json"}
    for relative in NATIVE_RESTORE_PASS_EVIDENCE[scenario_id]:
        archived = retained / scenario_id / relative
        (run / scenario_id / relative).unlink()
        assert archived.read_bytes() == f"raw:{relative}".encode()
        expected.add(f"{scenario_id}/{relative}")
    assert {
        path.relative_to(retained).as_posix() for path in retained.rglob("*") if path.is_file()
    } == expected


@pytest.mark.parametrize(
    ("scenario_id", "missing"),
    [
        (scenario, name)
        for scenario, names in NATIVE_RESTORE_PASS_EVIDENCE.items()
        for name in names
    ],
)
def test_native_restore_passing_evidence_rejects_each_missing_current_image_or_trace(
    tmp_path: Path, scenario_id: str, missing: str
) -> None:
    run = _native_restore_evidence_run(tmp_path, scenario_id, "passed")
    (run / scenario_id / missing).unlink()
    with pytest.raises(ValueError, match=Path(missing).name):
        next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")


def test_restore_crash_failure_retains_bounded_phase_protocol_evidence(tmp_path: Path) -> None:
    scenario_id = "44-file-restore-crash"
    run = tmp_path / "source" / "20261002T020000Z"
    run.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps(
            {"status": "failed", "scenarios": [{"scenario": scenario_id, "status": "failed"}]}
        ),
        encoding="utf-8",
    )
    expected = {"product-e2e-report.json"}
    for phase in ("seed", "resume"):
        phase_run = run / scenario_id / phase / "20261002T020001Z"
        phase_run.mkdir(parents=True)
        names = (
            "host-stderr.log",
            f"{scenario_id}-result.json",
            "fault-request.json",
            "fault-result.json",
            *(
                (
                    "44-restore-crash-checkpoint.json",
                    "44-file-restore-crash-before-kill.png",
                    "44-file-restore-crash-before-kill-trace.zip",
                )
                if phase == "seed"
                else ("44-file-restore-crash.png", "44-file-restore-crash-trace.zip")
            ),
        )
        for name in names:
            (phase_run / name).write_bytes(f"{phase}:{name}".encode())
            expected.add(f"{scenario_id}/{phase}/{phase_run.name}/{name}")
        (phase_run / "workspace.db").write_bytes(b"private")
        (phase_run / "unrelated.json").write_bytes(b"unrelated")
        invalid = run / scenario_id / phase / "not-a-phase-run"
        invalid.mkdir()
        (invalid / "fault-result.json").write_bytes(b"outside phase layout")
    private = run / scenario_id / "persistent" / "workspace"
    private.mkdir(parents=True)
    (private / "workspace.db").write_bytes(b"private")
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    assert {
        path.relative_to(retained).as_posix() for path in retained.rglob("*") if path.is_file()
    } == expected
    for relative in expected - {"product-e2e-report.json"}:
        assert (retained / relative).read_bytes() == (run / relative).read_bytes()


def test_native_failure_retains_only_current_request_and_fixed_control_records(
    tmp_path: Path,
) -> None:
    scenario_id = "42-file-document-native-operations"
    run = tmp_path / "source" / "20261002T030000Z"
    scenario = run / scenario_id
    controls = run / "_runtime" / "42" / "controls"
    scenario.mkdir(parents=True)
    controls.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps(
            {"status": "failed", "scenarios": [{"scenario": scenario_id, "status": "failed"}]}
        ),
        encoding="utf-8",
    )
    evidence = {
        f"{scenario_id}/file-document-native-request.json": b'{"action":"copy"}',
        f"{scenario_id}/file-document-native-result.json": b'{"status":"unverified"}',
        "_runtime/42/controls/document-native-arm.json": b'{"mode":"copy"}',
        "_runtime/42/controls/document-native-target.json": b'{"hwnd":123}',
        "_runtime/42/controls/document-native-drag-started.json": b'{"source":"synthetic"}',
        "_runtime/42/controls/document-native-drag-completed.json": b'{"effect":"Copy"}',
        "_runtime/42/controls/document-native-drop-result.json": b'{"received":["synthetic"]}',
        "_runtime/42/controls/document-native-preview-result.json": b'{"outcome":"do-preview-returned"}',
        "_runtime/42/controls/document-native-open-baseline.json": b'{"windows":[]}',
    }
    for relative, raw in evidence.items():
        (run / relative).write_bytes(raw)
    (controls / "document-native-unrelated.json").write_bytes(b"unrelated")
    private = controls / "document-drop"
    private.mkdir()
    (private / "synthetic.txt").write_bytes(b"do not copy source/destination trees")
    (scenario / "workspace.db").write_bytes(b"private")
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    assert {
        path.relative_to(retained).as_posix() for path in retained.rglob("*") if path.is_file()
    } == {"product-e2e-report.json", *evidence}
    for relative, raw in evidence.items():
        assert (retained / relative).read_bytes() == raw


@pytest.mark.parametrize(
    ("coverage_code", "evidence", "expected"),
    [
        (0, "present", 0),
        (0, "missing", 1),
        (0, "copy-false", 1),
        (0, "copy-error", 1),
        (1, "present", 1),
        (None, "present", 0),
    ],
)
def test_core_lane_retains_capacity_record_only_after_passing_coverage(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    coverage_code: int | None,
    evidence: str,
    expected: int,
) -> None:
    monkeypatch.setattr(next_gate, "REPO_ROOT", tmp_path / "repo")
    qa_temp = tmp_path / "qa-temp"
    qa_temp.mkdir()
    monkeypatch.setattr(next_gate, "QA_RUN_TEMP_DIR", qa_temp)
    monkeypatch.setattr(next_gate.handoff_gate, "git_head_sha", lambda: "c" * 40)
    monkeypatch.setattr(next_gate.handoff_gate, "load_dependencies", lambda: {})
    monkeypatch.setattr(next_gate.handoff_gate, "artifact_hashes", lambda _deps: {})
    monkeypatch.setattr(next_gate.handoff_gate, "release_source_hash", lambda _deps: "s" * 64)
    results = (
        []
        if coverage_code is None
        else [next_gate.StageResult("go-coverage", ["test"], coverage_code, 0.01, "", "", "repo")]
    )
    monkeypatch.setattr(next_gate, "run_lane", lambda *_args: (coverage_code or 0, results))
    source = next_gate.REPO_ROOT / "build/qa/415-capacity/capacity-qualification.json"
    source.parent.mkdir(parents=True)
    raw = b'{"operations":[{"scale":"s32","elapsedMillis":12}]}'
    if evidence != "missing":
        source.write_bytes(raw)
    if evidence in {"copy-false", "copy-error"}:

        def failed_copy(_source: Path, _destination: Path) -> bool:
            if evidence == "copy-error":
                raise OSError("synthetic capacity copy failure")
            return False

        monkeypatch.setattr(next_gate, "_copy_if_file", failed_copy)
    report = tmp_path / "lane.json"
    assert (
        next_gate.main(
            [
                "--lane",
                "core",
                *_candidate_args(tmp_path),
                "--json-report",
                str(report),
            ]
        )
        == expected
    )
    assert json.loads(report.read_text(encoding="utf-8"))["ok"] is (expected == 0)
    retained = next_gate.REPO_ROOT / (
        "build/automation/lane-evidence/core/415-capacity/capacity-qualification.json"
    )
    if coverage_code == 0 and evidence == "present":
        assert retained.read_bytes() == raw
    else:
        assert not retained.exists()


def test_dashboard_download_hardening_rebinds_the_handler_on_load_retry() -> None:
    bridge = Path(__file__).resolve().parents[1] / (
        "desktop/src/VibeTable.Desktop/Services/ProductWebViewBridge.cs"
    )
    source = bridge.read_text(encoding="utf-8")
    hardening = source.split("private void ApplyHardening(", 1)[1].split(
        "private async void OnDownloadStarting(", 1
    )[0]
    assert "core.DownloadStarting -= OnDownloadStarting;" in hardening
    assert hardening.index("core.DownloadStarting -= OnDownloadStarting;") < hardening.index(
        "core.DownloadStarting += OnDownloadStarting;"
    )


def test_go_gate_uses_the_same_pinned_toolchain_as_package_build(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    from scripts import build_next

    suffix = "go.exe" if next_gate.os.name == "nt" else "go"
    pinned = tmp_path / ".tools" / f"go-{build_next.RECOVERY_GO_VERSION}" / "go/bin" / suffix
    stale = tmp_path / ".tools/go-full/go/bin" / suffix
    for executable in (pinned, stale):
        executable.parent.mkdir(parents=True)
        executable.touch()
    monkeypatch.setattr(next_gate, "REPO_ROOT", tmp_path)
    assert next_gate._resolve("go") == str(pinned)


def test_plugin_application_failure_retains_closed_phase_and_contrast_evidence(tmp_path):
    run = tmp_path / "source" / "20261015T000000Z"
    run.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps(
            {
                "status": "failed",
                "scenarios": [
                    {"scenario": "48-plugin-applications", "status": "failed"},
                    {"scenario": "49-plugin-application-security", "status": "failed"},
                ],
            }
        )
    )
    expected = []
    phase = "48-plugin-applications/seed/20261015T000001Z"
    for name in (*next_gate.PRODUCT_E2E_EVIDENCE_FILES, "48-plugin-applications-result.json"):
        expected.append(f"{phase}/{name}")
    for filename in (
        "48-plugin-print-preview.json",
        "48-plugin-print-preview-unobservable.json",
        "48-plugin-print-preview-failed.png",
    ):
        expected.append(f"{phase}/{filename}")
    expected.append("48-plugin-applications/persistent/host/vibetable-trace.log")
    for name in (
        "plugin-contrast-original.json",
        "plugin-contrast-restoration.json",
        "plugin-contrast-cleanup.json",
        "plugin-native-result.json",
        "plugin-native-observation.json",
    ):
        expected.append(f"49-plugin-application-security/{name}")
    for relative in expected:
        target = run / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(f"raw:{relative}")
    for relative in (f"{phase}/workspace.db", "49-plugin-application-security/unrelated.json"):
        (run / relative).write_text("private")
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    for relative in expected:
        assert (retained / relative).read_text() == f"raw:{relative}"
    assert not (retained / phase / "workspace.db").exists()
    assert not (retained / "49-plugin-application-security/unrelated.json").exists()


def test_capability_services_failure_retains_picker_proof_without_private_state(tmp_path):
    scenario_id = "52-plugin-capability-services"
    run = _scenario_screenshot_run(tmp_path, scenario_id, "failed")
    for filename in ("plugin-file-dialog-result.json", "plugin-file-dialog-observation.json"):
        (run / scenario_id / filename).write_text("raw-native-proof")
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    assert (retained / scenario_id / "52-capability-services.json").exists()
    for filename in ("plugin-file-dialog-result.json", "plugin-file-dialog-observation.json"):
        assert (retained / scenario_id / filename).read_text() == "raw-native-proof"
    assert not (retained / scenario_id / "workspace.db").exists()
    assert not (retained / scenario_id / "unrelated.png").exists()


def test_non_build_timeout_with_inherited_pipe_has_bounded_drain(monkeypatch):
    class ExitedProcess:
        pid = 987
        returncode = 0
        calls = 0

        def poll(self):
            return 0

        def communicate(self, timeout=None):
            self.calls += 1
            if self.calls == 1:
                raise subprocess.TimeoutExpired("test", timeout)
            assert timeout == 5, "an inherited pipe must not hang the CI job"
            raise subprocess.TimeoutExpired("test", timeout)

    process = ExitedProcess()
    monkeypatch.setattr(next_gate.subprocess, "Popen", lambda *_a, **_k: process)
    monkeypatch.setattr(
        next_gate.subprocess, "run", lambda *_a, **_k: pytest.fail("must not kill an exited PID")
    )
    code, stdout, stderr = next_gate._run_command(["test"], cwd=".", environment={}, timeout=1)
    assert code == next_gate.TIMEOUT_RETURNCODE
    assert process.calls == 2
    assert stdout == ""
    assert "output may be incomplete" in stderr


def test_race_discovery_uses_the_same_build_tags_as_execution(monkeypatch, tmp_path):
    commands = []
    monkeypatch.setattr(next_gate, "RACE_BINARY_DIR", tmp_path / "binaries")

    def run(command, **kwargs):
        commands.append(command)
        if command[1] == "list":
            return 0, f"example/tagged\t{tmp_path}\n", ""
        if command[1:4] == ["test", "-c", "-race"]:
            assert "-race" in command
            Path(command[command.index("-o") + 1]).touch()
            return 0, "", ""
        if any(arg.startswith("-test.list=") for arg in command):
            return 0, "TestShared\nTestRaceOnly\n", ""
        return 0, "ok", ""

    monkeypatch.setattr(next_gate, "_run_command", run)
    code, _, _ = next_gate._run_go_race(cwd=str(tmp_path), environment={})
    assert code == 0
    selected = {arg for command in commands for arg in command if arg.startswith("-test.run=")}
    assert selected == {"-test.run=^TestShared$", "-test.run=^TestRaceOnly$"}


@pytest.mark.parametrize("missing_phase", [None, "seed", "resume"])
def test_model_restart_requires_both_processes_evidence(
    tmp_path: Path, missing_phase: str | None
) -> None:
    scenario = "54-plugin-models"
    run = tmp_path / "source" / "20261027T050000Z"
    run.mkdir(parents=True)
    (run / "product-e2e-report.json").write_text(
        json.dumps({"status": "passed", "scenarios": [{"scenario": scenario, "status": "passed"}]}),
        encoding="utf-8",
    )
    expected = {"product-e2e-report.json"}
    for phase, filenames in S54_PHASE_EVIDENCE.items():
        directory = run / scenario / phase / "20261027T050001Z"
        directory.mkdir(parents=True)
        for filename in filenames:
            (directory / filename).write_bytes(f"{phase}:{filename}".encode())
            expected.add(f"{scenario}/{phase}/{directory.name}/{filename}")
        (directory / "workspace.db").write_bytes(b"private workspace")
    if missing_phase is not None:
        filename = S54_PHASE_EVIDENCE[missing_phase][0]
        (run / scenario / missing_phase / "20261027T050001Z" / filename).unlink()
        with pytest.raises(ValueError, match=filename):
            next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
        return
    retained = next_gate.persist_product_e2e_evidence(run.parent, tmp_path / "destination")
    assert retained is not None
    assert {
        file.relative_to(retained).as_posix() for file in retained.rglob("*") if file.is_file()
    } == expected
