from __future__ import annotations

import io
import json
import subprocess
import sys
import threading
import xml.etree.ElementTree as ET
from contextlib import suppress
from pathlib import Path

import pytest

from qa import next as next_gate


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

    assert command[:2] == [
        sys.executable,
        str(next_gate.REPO_ROOT / "tests" / "e2e" / "packaged_runtime_baseline.py"),
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


def test_race_stage_uses_three_isolated_package_workers_by_default() -> None:
    assert next_gate.RACE_PACKAGE_WORKERS == 3


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

    def fake_command(command, *, cwd, environment, timeout):
        del environment
        observed.append((command, cwd, timeout))
        if command[1] == "list" and command[-1] == "./...":
            return 0, f"example/tests/integration\t{package_dir}\n", ""
        if "-list" in command:
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

    def fake_command(command, *, cwd, environment, timeout):
        del cwd, environment
        observed.append((command, timeout))
        if command[1] == "list" and command[-1] == "./...":
            return 0, f"example/tests/integration\t{package_dir}\n", ""
        if "-list" in command:
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

    def fake_command(command, *, cwd, environment, timeout):
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
        if "-list" in command:
            package = command[2]
            prefix = "Migration" if package.endswith("migrations") else "Integration"
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

    def fake_command(command, *, cwd, environment, timeout):
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
        if "-list" in command:
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

    next_gate.run_lane("race-a", tmp_path, tmp_path / "candidate.zip")
    next_gate.run_lane("race-b", tmp_path, tmp_path / "candidate.zip")

    assert observed == [(0, 2), (1, 2)]


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

    def fake_command(command, *, cwd, environment, timeout):
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

    def fake_command(command, *, cwd, environment, timeout):
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
    assert copied_files == [Path("product-e2e-report.json")]
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


def test_full_ci_report_is_bound_to_stable_release_candidate(
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

    assert next_gate.main(["--ci", *_candidate_args(tmp_path), "--json-report", str(report)]) == 0
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["schemaVersion"] == 2
    assert payload["releaseEligible"] is True
    assert payload["releaseCandidate"]["archive"]["sha256"]
    assert payload["releaseCandidate"]["packageTreeSha256"]


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
    source_directory = "p" if stage == "product-e2e" else "fault-injection"
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
    ("stage", "source_directory"),
    [("product-e2e", "p"), ("fault-injection", "fault-injection")],
)
def test_failed_product_lane_persists_product_diagnostics(
    monkeypatch,
    tmp_path: Path,
    stage: str,
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
    ) -> Path:
        assert not require_passing_report
        observed.append((source, destination))
        return destination / "20260817T010203Z"

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
    assert observed == [
        (
            qa_temp / source_directory,
            next_gate.REPO_ROOT / "build" / "automation" / "lane-evidence" / "resilience",
        )
    ]
    assert report.is_file()


def test_successful_product_lane_persists_report_before_qa_cleanup(
    monkeypatch,
    tmp_path: Path,
) -> None:
    qa_temp = _prepare_product_lane(
        monkeypatch,
        tmp_path,
        stage="product-e2e",
        returncode=0,
    )
    product_evidence = qa_temp / "p"
    observed: list[tuple[Path, Path]] = []

    def persist(
        source: Path,
        destination: Path,
        *,
        require_passing_report: bool,
    ) -> Path:
        assert source.is_dir()
        assert require_passing_report
        observed.append((source, destination))
        return destination / "20260817T010203Z"

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
        == 0
    )
    assert not qa_temp.exists()
    assert next_gate.QA_RUN_TEMP_DIR is None
    assert report.is_file()
    assert observed == [
        (
            product_evidence,
            next_gate.REPO_ROOT / "build" / "automation" / "lane-evidence" / "resilience",
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
        assert kwargs == {"require_passing_report": True}
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
    assert product_command == [
        next_gate.sys.executable,
        "qa/product_acceptance.py",
        "--evidence-root",
        str(next_gate.QA_RUN_TEMP_DIR / "p"),
    ]
    assert Path(product_cwd) == next_gate.REPO_ROOT


def test_dotnet_coverage_inventory_matches_coverlet_projects_bidirectionally() -> None:
    payload = json.loads(next_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    configured_projects = {
        entry["test_project"]
        for entry in payload["quality"]["dotnet_coverage"]["projects"].values()
    }
    discovered_projects = {
        project.relative_to(next_gate.REPO_ROOT).as_posix()
        for project in (next_gate.REPO_ROOT / "desktop" / "tests").rglob("*.csproj")
        if "coverlet.msbuild" in project.read_text(encoding="utf-8")
        and "CollectCoverage" in project.read_text(encoding="utf-8")
    }

    assert configured_projects == discovered_projects
    assert any("Contracts.Tests" in project for project in configured_projects)


def test_dotnet_coverage_inventory_registers_openxml_as_an_independent_assembly() -> None:
    payload = json.loads(next_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    inventory = payload["quality"]["dotnet_coverage"]["projects"]
    entry = inventory["VibeTable.DocumentDiff.OpenXml"]
    prefix = entry["msbuild_prefix"]
    properties = next_gate.dotnet_coverage_properties()

    assert properties[f"{prefix}CoverageInclude"] == "[VibeTable.DocumentDiff.OpenXml]*"
    assert isinstance(properties[f"{prefix}LineCoverageMinimum"], int)
    assert isinstance(properties[f"{prefix}BranchCoverageMinimum"], int)


def test_dotnet_coverage_inventory_registers_contracts_with_generated_only_denominator() -> None:
    payload = json.loads(next_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    inventory = payload["quality"]["dotnet_coverage"]["projects"]
    entry = inventory["VibeTable.Contracts"]
    prefix = entry["msbuild_prefix"]
    properties = next_gate.dotnet_coverage_properties()

    assert properties[f"{prefix}CoverageInclude"] == "[VibeTable.Contracts]*"
    assert isinstance(properties[f"{prefix}LineCoverageMinimum"], int)
    assert isinstance(properties[f"{prefix}BranchCoverageMinimum"], int)
    assert properties[f"{prefix}CoverageExcludeByFile"] == (
        "**/VibeTable.Contracts/Generated/*.g.cs"
    )
    assert entry["generated_source_files"] == [
        "Generated/SchemaV2Contracts.g.cs",
        "Generated/WorkbenchContracts.g.cs",
    ]


def test_dotnet_gate_emits_unique_coverage_properties_for_every_inventory_entry() -> None:
    command, cwd = next_gate.stage_command("dotnet")
    assert "/p:CollectCoverage=true" in command
    assert "/p:CoverletOutputFormat=cobertura" in command
    assert Path(cwd) == next_gate.REPO_ROOT

    payload = json.loads(next_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    inventory = payload["quality"]["dotnet_coverage"]["projects"]
    expected_properties = {}
    for assembly, entry in inventory.items():
        prefix = entry["msbuild_prefix"]
        expected_properties[f"{prefix}CoverageInclude"] = f"[{assembly}]*"
        expected_properties[f"{prefix}LineCoverageMinimum"] = entry["minimum"]["line"]
        expected_properties[f"{prefix}BranchCoverageMinimum"] = entry["minimum"]["branch"]
        if exclusion := entry.get("generated_source_exclusion"):
            expected_properties[f"{prefix}CoverageExcludeByFile"] = exclusion
    assert next_gate.dotnet_coverage_properties() == expected_properties
    expected_count = sum(
        3 + int("generated_source_exclusion" in entry) for entry in inventory.values()
    )
    assert len(expected_properties) == len(set(expected_properties)) == expected_count
    for property_name, value in expected_properties.items():
        assert f"/p:{property_name}={value}" in command


def test_dotnet_coverage_projects_consume_central_properties_and_fail_closed() -> None:
    payload = json.loads(next_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    inventory = payload["quality"]["dotnet_coverage"]["projects"]

    for entry in inventory.values():
        prefix = entry["msbuild_prefix"]
        project = next_gate.REPO_ROOT / entry["test_project"]
        root = ET.parse(project).getroot()
        properties = {
            child.tag: child.text for group in root.findall("PropertyGroup") for child in group
        }
        assert properties["Include"] == f"$({prefix}CoverageInclude)"
        assert properties["Threshold"] == (
            f"$({prefix}LineCoverageMinimum),$({prefix}BranchCoverageMinimum)"
        )
        assert properties["ThresholdType"] == "line,branch"
        assert properties["ThresholdStat"] == "total"
        exclusion_property = f"{prefix}CoverageExcludeByFile"
        if "generated_source_exclusion" in entry:
            assert properties["ExcludeByFile"] == f"$({exclusion_property})"
        else:
            assert "ExcludeByFile" not in properties
        target = root.find(f"Target[@Name='Validate{prefix}CoverageProperties']")
        assert target is not None
        assert target.attrib["BeforeTargets"] == "VSTest"
        error = target.find("Error")
        assert error is not None
        condition = error.attrib["Condition"]
        assert f"$({prefix}CoverageInclude)" in condition
        assert f"$({prefix}LineCoverageMinimum)" in condition
        assert f"$({prefix}BranchCoverageMinimum)" in condition
        if "generated_source_exclusion" in entry:
            assert f"$({exclusion_property})" in condition


def test_dotnet_coverage_projects_measure_the_complete_single_assembly_denominator() -> None:
    payload = json.loads(next_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    inventory = payload["quality"]["dotnet_coverage"]["projects"]

    for entry in inventory.values():
        root = ET.parse(next_gate.REPO_ROOT / entry["test_project"]).getroot()
        property_names = {child.tag for group in root.findall("PropertyGroup") for child in group}
        assert "SkipAutoProps" not in property_names
        assert "MergeWith" not in property_names
        allowed_exclusions = {"ExcludeByFile"} if "generated_source_exclusion" in entry else set()
        assert not any(
            name.startswith("Exclude") and name not in allowed_exclusions for name in property_names
        )


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
    payload = json.loads(next_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    payload["quality"]["dotnet_coverage"]["projects"]["VibeTable.Contracts"][
        "generated_source_exclusion"
    ] = exclusion
    config = tmp_path / "project.json"
    config.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(
        ValueError,
        match=r"invalid generated source exclusion for VibeTable\.Contracts",
    ):
        next_gate.dotnet_coverage_properties(config)


def test_dotnet_coverage_inventory_rejects_unregistered_generated_source(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    generated_dir = (next_gate.REPO_ROOT / "desktop/src/VibeTable.Contracts/Generated").resolve()
    unregistered = generated_dir / "Manual.g.cs"
    real_glob = Path.glob
    real_rglob = Path.rglob

    def glob_with_unregistered(path: Path, pattern: str, **kwargs: object):
        results = list(real_glob(path, pattern, **kwargs))
        if path.resolve() == generated_dir and pattern == "*.g.cs":
            results.append(unregistered)
        return iter(results)

    def rglob_with_unregistered(path: Path, pattern: str, **kwargs: object):
        results = list(real_rglob(path, pattern, **kwargs))
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
        next_gate.dotnet_coverage_properties()


def test_dotnet_coverage_inventory_rejects_source_binding_drift(
    tmp_path: Path,
) -> None:
    payload = json.loads(next_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    projects = payload["quality"]["dotnet_coverage"]["projects"]
    projects["VibeTable.Desktop"]["source_project"] = (
        "desktop/src/VibeTable.Workspace/VibeTable.Workspace.csproj"
    )
    config = tmp_path / "project.json"
    config.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(ValueError, match=r"source_project must match VibeTable\.Desktop"):
        next_gate.dotnet_coverage_properties(config)


@pytest.mark.parametrize(
    ("mutation", "message"),
    [
        ("coverage-condition", "coverage group must run only when CollectCoverage is true"),
        ("target-condition", "validation target must run only when CollectCoverage is true"),
        ("error-and", "validation target must reject each missing property"),
        ("package-condition", "coverlet.msbuild reference must be active"),
        ("package-exclude-attribute", "coverlet.msbuild reference must be active"),
        ("package-include-attribute", "coverlet.msbuild reference must be active"),
        ("external-exclude", "coverage exclusions are not allowed"),
    ],
)
def test_dotnet_coverage_inventory_rejects_inactive_or_partial_xml_wiring(
    monkeypatch: pytest.MonkeyPatch,
    mutation: str,
    message: str,
) -> None:
    project = (
        next_gate.REPO_ROOT / "desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj"
    ).resolve()
    real_parse = next_gate.ET.parse

    def parse_with_drift(path: Path) -> ET.ElementTree:
        tree = real_parse(path)
        if Path(path).resolve() != project:
            return tree
        root = tree.getroot()
        coverage_group = next(
            group
            for group in root.findall("PropertyGroup")
            if "CollectCoverage" in group.attrib.get("Condition", "")
        )
        target = root.find("Target[@Name='ValidateVibeTableDesktopCoverageProperties']")
        assert target is not None
        error = target.find("Error")
        assert error is not None
        package = next(
            node
            for node in root.findall(".//PackageReference")
            if node.attrib.get("Include") == "coverlet.msbuild"
        )
        if mutation == "coverage-condition":
            coverage_group.set("Condition", "'$(CollectCoverage)' != 'true'")
        elif mutation == "target-condition":
            target.set("Condition", "'$(CollectCoverage)' != 'true'")
        elif mutation == "error-and":
            error.set("Condition", error.attrib["Condition"].replace(" or ", " and "))
        elif mutation == "package-condition":
            package.set("Condition", "'$(NeverEnableCoverage)' == 'true'")
        elif mutation == "package-exclude-attribute":
            package.set("ExcludeAssets", "build")
        elif mutation == "package-include-attribute":
            include_assets = package.find("IncludeAssets")
            assert include_assets is not None
            package.remove(include_assets)
            package.set("IncludeAssets", "runtime")
        else:
            exclusion_group = ET.SubElement(root, "PropertyGroup")
            ET.SubElement(exclusion_group, "ExcludeByFile").text = "**/Models/*.cs"
        return tree

    monkeypatch.setattr(next_gate.ET, "parse", parse_with_drift)

    with pytest.raises(ValueError, match=message):
        next_gate.dotnet_coverage_properties()


@pytest.mark.parametrize(
    ("mutation", "message"),
    [
        ("adapter-drift", "coverage adapter does not match inventory"),
        ("unconditional-exclusion", "coverage exclusions are not allowed"),
        ("missing-fail-closed-property", "must reject each missing property"),
        ("extra-exclusion", "coverage exclusions are not allowed"),
    ],
)
def test_dotnet_coverage_inventory_rejects_generated_exclusion_wiring_drift(
    monkeypatch: pytest.MonkeyPatch,
    mutation: str,
    message: str,
) -> None:
    project = (
        next_gate.REPO_ROOT
        / "desktop/tests/VibeTable.Contracts.Tests/VibeTable.Contracts.Tests.csproj"
    ).resolve()
    real_parse = next_gate.ET.parse

    def parse_with_drift(path: Path) -> ET.ElementTree:
        tree = real_parse(path)
        if Path(path).resolve() != project:
            return tree
        root = tree.getroot()
        coverage_group = next(
            group
            for group in root.findall("PropertyGroup")
            if "CollectCoverage" in group.attrib.get("Condition", "")
        )
        target = root.find("Target[@Name='ValidateVibeTableContractsCoverageProperties']")
        assert target is not None
        error = target.find("Error")
        assert error is not None
        if mutation == "adapter-drift":
            exclusion = coverage_group.find("ExcludeByFile")
            assert exclusion is not None
            exclusion.text = "**/*.g.cs"
        elif mutation == "unconditional-exclusion":
            external_group = ET.SubElement(root, "PropertyGroup")
            ET.SubElement(external_group, "ExcludeByFile").text = "**/*.g.cs"
        elif mutation == "missing-fail-closed-property":
            error.set(
                "Condition",
                error.attrib["Condition"].replace(
                    " or '$(VibeTableContractsCoverageExcludeByFile)' == ''",
                    "",
                ),
            )
        else:
            ET.SubElement(coverage_group, "ExcludeByAttribute").text = "GeneratedCodeAttribute"
        return tree

    monkeypatch.setattr(next_gate.ET, "parse", parse_with_drift)

    with pytest.raises(ValueError, match=message):
        next_gate.dotnet_coverage_properties()


def test_dotnet_coverage_config_rejects_missing_metric_instead_of_disabling_gate(
    tmp_path: Path,
) -> None:
    payload = json.loads(next_gate.PROJECT_CONFIG.read_text(encoding="utf-8"))
    del payload["quality"]["dotnet_coverage"]["projects"]["VibeTable.Desktop"]["minimum"]["branch"]
    config = tmp_path / "project.json"
    config.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(ValueError, match=r"must declare line and branch for VibeTable\.Desktop"):
        next_gate.dotnet_coverage_properties(config)
