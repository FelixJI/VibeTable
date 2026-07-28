from __future__ import annotations

import io
import json
import subprocess
import xml.etree.ElementTree as ET
from pathlib import Path

from qa import next as next_gate


def _candidate_args(tmp_path: Path) -> list[str]:
    package_root = tmp_path / "VibeTable.Next"
    package_root.mkdir()
    (package_root / "candidate.bin").write_bytes(b"candidate")
    archive = tmp_path / "VibeTable.Next.zip"
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


def test_github_workflows_are_windows_only_and_keep_desktop_smoke_heavy() -> None:
    workflow_dir = next_gate.REPO_ROOT / ".github" / "workflows"
    workflow_paths = sorted([*workflow_dir.glob("*.yml"), *workflow_dir.glob("*.yaml")])
    workflows = {path.name: path.read_text(encoding="utf-8") for path in workflow_paths}
    assert workflows
    for workflow in workflows.values():
        runner_lines = [
            line.strip() for line in workflow.splitlines() if line.strip().startswith("runs-on:")
        ]
        assert runner_lines
        assert set(runner_lines) == {"runs-on: windows-latest"}
        assert "ubuntu-latest" not in workflow
        assert "macos-" not in workflow
        assert "\ndefaults:\n  run:\n    shell: pwsh\n" in workflow

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
    environment = next_gate._stage_environment("upgrade-smoke", ["pytest"])

    temp_root = Path(environment["TEMP"])
    assert environment["TMP"] == environment["TEMP"]
    assert temp_root == next_gate.QA_RUN_TEMP_DIR
    assert temp_root.parent == next_gate.REPO_ROOT / "build" / "qa" / "tmp"
    assert temp_root.name.startswith(f"run-{next_gate.os.getpid()}-")
    assert temp_root.is_dir()


def test_race_stage_batches_every_discovered_integration_test(
    monkeypatch,
) -> None:
    observed: list[list[str]] = []
    names = [f"TestCase{index}" for index in range(17)]

    def fake_command(command, *, cwd, environment, timeout):
        del cwd, environment, timeout
        observed.append(command)
        if command[1:3] == ["list", "./..."]:
            return 0, "example/tests/integration\n", ""
        if "-list" in command:
            return 0, "\n".join(names) + "\nok\tpackage\n", ""
        return 0, "ok\n", ""

    monkeypatch.setattr(next_gate, "_run_command", fake_command)
    code, _stdout, _stderr = next_gate._run_go_race(
        cwd=str(next_gate.SIDECAR_DIR),
        environment={},
    )

    assert code == 0
    race_batches = [
        command
        for command in observed
        if "example/tests/integration" in command and "-race" in command
    ]
    assert len(race_batches) == len(names)
    covered = set()
    for command in race_batches:
        pattern = command[command.index("-run") + 1]
        covered.update(name for name in names if name in pattern)
    assert covered == set(names)


def test_known_slow_race_tests_run_individually_with_long_timeout(
    monkeypatch,
) -> None:
    observed: list[tuple[list[str], int]] = []
    slow = sorted(next_gate.RACE_LONG_TESTS)

    def fake_command(command, *, cwd, environment, timeout):
        del cwd, environment
        observed.append((command, timeout))
        if command[1:3] == ["list", "./..."]:
            return 0, "example/tests/integration\n", ""
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
            (command, timeout)
            for command, timeout in observed
            if "-run" in command and command[command.index("-run") + 1] == f"^{name}$"
        ]
        assert len(matching) == 1
        command, timeout = matching[0]
        assert f"-timeout={next_gate.RACE_LONG_TEST_TIMEOUT}" in command
        assert timeout == next_gate.RACE_LONG_COMMAND_TIMEOUT_SECONDS


def test_race_stage_isolates_named_tests_in_every_package(
    monkeypatch,
) -> None:
    observed: list[list[str]] = []

    def fake_command(command, *, cwd, environment, timeout):
        del cwd, environment, timeout
        observed.append(command)
        if command[1:3] == ["list", "./..."]:
            return 0, "example/migrations\nexample/tests/integration\n", ""
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
    race_commands = [command for command in observed if "-race" in command]
    assert len(race_commands) == 4
    assert {(command[command.index("-run") + 1], command[-3]) for command in race_commands} == {
        ("^TestMigrationOne$", "example/migrations"),
        ("^TestMigrationTwo$", "example/migrations"),
        ("^TestIntegrationOne$", "example/tests/integration"),
        ("^TestIntegrationTwo$", "example/tests/integration"),
    }


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


def test_full_ci_report_is_release_eligible_only_when_identity_stays_stable(
    monkeypatch,
    tmp_path: Path,
) -> None:
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
    monkeypatch.setattr(next_gate, "run_ci", lambda package_root=None: (0, []))
    report = tmp_path / "ci.json"

    assert next_gate.main(["--ci", *_candidate_args(tmp_path), "--json-report", str(report)]) == 1
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["ok"] is False
    assert payload["releaseEligible"] is False


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
    monkeypatch.setattr(next_gate, "run_ci", lambda package_root=None: (0, []))
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
    monkeypatch.setattr(next_gate, "run_ci", lambda package_root=None: (0, []))
    report = tmp_path / "ci.json"

    assert next_gate.main(["--ci", *_candidate_args(tmp_path), "--json-report", str(report)]) == 0
    payload = json.loads(report.read_text(encoding="utf-8"))
    assert payload["schemaVersion"] == 2
    assert payload["releaseEligible"] is True
    assert payload["releaseCandidate"]["archive"]["sha256"]
    assert payload["releaseCandidate"]["packageTreeSha256"]


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

    def mutate_candidate(_package_root=None):
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
