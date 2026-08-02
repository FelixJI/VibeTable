from __future__ import annotations

import io
import json
import subprocess
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
    assert next_gate.STAGES[:8] == (
        "version",
        "package",
        "go-fmt",
        "go-vet",
        "go-test",
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
    assert workflows
    for workflow in workflows.values():
        runner_lines = [
            line.strip() for line in workflow.splitlines() if line.strip().startswith("runs-on:")
        ]
        assert runner_lines
        assert set(runner_lines) <= {
            "runs-on: windows-latest",
            "runs-on: ubuntu-latest",
        }
        assert "macos-" not in workflow

    release_workflow = workflows["release.yml"]
    release_runner_lines = [
        line.strip()
        for line in release_workflow.splitlines()
        if line.strip().startswith("runs-on:")
    ]
    assert set(release_runner_lines) == {"runs-on: windows-latest"}
    assert "runs-on: ubuntu-latest" not in release_workflow
    for workflow_name in ("mirror.yml", "release-cleanup.yml", "release-please.yml"):
        assert "runs-on: ubuntu-latest" in workflows[workflow_name]
    assert "\ndefaults:\n  run:\n    shell: pwsh\n" in release_workflow

    ci_workflow = workflows["ci.yml"]
    python_job = ci_workflow.split("\n  web:\n", maxsplit=1)[0]
    assert "--ignore=tests/e2e/test_next_readonly_smoke.py" in python_job
    assert ci_workflow.count("--ignore=tests/e2e/") == 1
    assert "mkdir -p" not in ci_workflow
    assert "build/ci/vibetable-pb.exe" in ci_workflow

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

    command, cwd = next_gate.stage_command("sidecar-smoke")
    assert command[0] == next_gate.sys.executable
    assert "packaged_sidecar_matrix.py" in " ".join(command)
    assert "--json-report" in command
    assert Path(cwd) == next_gate.REPO_ROOT


def test_package_stage_binds_provider_evidence_to_the_release_archive(
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


def test_release_gate_enables_required_windows_credential_manager_tests() -> None:
    workflow = (next_gate.REPO_ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
    build_job = workflow.split("  build:", maxsplit=1)[1].split("\n  core:", maxsplit=1)[0]
    assert "timeout-minutes: 45" in build_job.split("    steps:", maxsplit=1)[0]
    assert "environment: release" not in build_job
    assert "contents: write" not in build_job
    assert workflow.count('VIBETABLE_TEST_WINDOWS_CREDENTIAL_MANAGER: "1"') == 2
    protected_step = workflow.split(
        "- name: Run protected package eligibility lane",
        maxsplit=1,
    )[1].split("- name:", maxsplit=1)[0]
    assert "VIBETABLE_PROVIDER_EVIDENCE_HMAC_KEY" in protected_step
    assert "environment: release" in workflow.split("  release:", maxsplit=1)[1]
    gate_job = workflow.split("  gate:", maxsplit=1)[1].split("\n  release:", maxsplit=1)[0]
    assert "if: always()" in gate_job
    assert "environment: release" not in gate_job
    assert "contents: write" not in gate_job
    release_header = workflow.split("  release:", maxsplit=1)[1].split("    steps:", maxsplit=1)[0]
    assert "needs: [build, gate]" in release_header
    assert "needs.gate.result == 'success'" in release_header


def test_release_workflow_runs_each_stage_in_one_candidate_bound_lane() -> None:
    workflow = (next_gate.REPO_ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")

    for lane in next_gate.LANE_STAGES:
        assert workflow.count(f"qa/next.py --lane {lane}") == 1
    assert "qa/next.py --ci" not in workflow
    assert workflow.count("qa/release_eligibility.py") == 1
    assert workflow.index("Aggregate immutable candidate evidence") < workflow.index(
        "Verify eligibility is bound to the immutable candidate"
    )


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


def test_go_test_retries_only_the_narrow_windows_tempdir_cleanup_flake(
    monkeypatch,
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
    result = next_gate.run_stage("go-test")

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
        lambda package_root=None, package_archive=None: (0, []),
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
        lambda package_root=None, package_archive=None: (0, []),
    )
    report = tmp_path / "ci.json"

    assert next_gate.main(["--ci", *_candidate_args(tmp_path), "--json-report", str(report)]) == 0
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["schemaVersion"] == 2
    assert payload["releaseEligible"] is True
    assert payload["releaseCandidate"]["archive"]["sha256"]
    assert payload["releaseCandidate"]["packageTreeSha256"]


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
        next_gate.main(["--lane", "race", *_candidate_args(tmp_path), "--json-report", str(report)])
        == 0
    )
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["reportKind"] == "lane"
    assert payload["lane"] == "race"
    assert payload["ok"] is True
    assert payload["releaseEligible"] is False
    assert payload["releaseCandidate"]["archive"]["sha256"]


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


def test_dotnet_gate_keeps_project_coverage_ratchets_enabled() -> None:
    command, cwd = next_gate.stage_command("dotnet")
    assert "/p:CollectCoverage=true" in command
    assert "/p:CoverletOutputFormat=cobertura" in command
    assert Path(cwd) == next_gate.REPO_ROOT

    expected_thresholds = {
        "VibeTable.Desktop.Tests": "45",
        "VibeTable.Infrastructure.Tests": "65",
        "VibeTable.Workspace.Tests": "80",
    }
    for project_name, threshold in expected_thresholds.items():
        project = (
            next_gate.REPO_ROOT / "desktop" / "tests" / project_name / f"{project_name}.csproj"
        )
        properties = {
            child.tag: child.text
            for group in ET.parse(project).getroot().findall("PropertyGroup")
            for child in group
        }
        assert properties["Threshold"] == threshold
        assert properties["ThresholdType"] == "line"
        assert properties["ThresholdStat"] == "total"
