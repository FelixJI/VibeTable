from __future__ import annotations

import json
import subprocess
from pathlib import Path
from unittest.mock import Mock

import pytest

from qa import go_test_schedule as schedule
from scripts.qa.windows_process_scope import ScopeWaitResult

APP = schedule.APP_PACKAGE
PEER = f"{schedule.SIDECAR_PACKAGE}/migrations"
CLEANUP = (
    "--- FAIL: TestExample\n"
    "    testing.go:1464: TempDir RemoveAll cleanup: unlinkat path: "
    "The directory is not empty.\n"
)


@pytest.mark.parametrize(
    "listing", [PEER, f"{APP}\n{PEER}\n{PEER}", f"{APP}\n../outside", f"{APP}\n{PEER}/..."]
)
def test_rejects_invalid_full_package_inventory(listing: str) -> None:
    with pytest.raises(ValueError, match="invalid full sidecar package inventory"):
        schedule._remaining_packages(listing)


@pytest.mark.parametrize("listing", ["", "TestA\nTestA\n", "TestA\nBenchmarkA\n"])
def test_rejects_invalid_binary_inventory(listing: str) -> None:
    with pytest.raises(ValueError, match="invalid Test/Example/Fuzz inventory"):
        schedule._app_inventory(listing)


def test_schedule_builds_once_and_completes_app_before_remaining(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    inventory = ("TestA", "Example", "FuzzSeed", "TestSkip", "TestLast")
    observed: list[str] = []
    coverage = tmp_path / "coverage"
    coverage.mkdir()
    sidecar = tmp_path / "sidecar"

    def supervise(
        commands: tuple[schedule.Command, ...], cwd: Path, *, timeout: int | None = None
    ) -> None:
        observed.extend(command.label for command in commands)
        command = commands[0]
        if command.label == "packages":
            command.stdout.write_text(f"{PEER}\n{APP}\n", encoding="utf-8")
        elif command.label == "app-build":
            assert command.environment is not None
            build = Path(command.environment["GOTMPDIR"]) / "go-build1" / "b001"
            build.mkdir(parents=True)
            (build / "_testmain.go").write_text(
                "package main\n\nfunc main() {\n"
                "    m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)\n"
                "    os.Exit(m.Run())\n}\n",
                encoding="utf-8",
            )
            assert command.args[:6] == (
                "go",
                "test",
                "-c",
                "-work",
                "-covermode=count",
                "-coverpkg=./internal/query",
            )
            assert command.args[-1] == APP
        elif command.label == "app-inventory":
            assert cwd == sidecar / "internal/app"
            assert command.args[1:] == ("-test.list=^(Test|Example|Fuzz)",)
            command.stdout.write_text("\n".join(inventory) + "\n", encoding="utf-8")
        elif command.label == "app-worker-1":
            assert cwd == sidecar / "internal/app"
            assert timeout == 600
            assert len(commands) == 2
            assert commands[0].args[0] == commands[1].args[0]
            for index, worker in enumerate(commands):
                assert worker.args[1:5] == (
                    "-test.count=1",
                    "-test.timeout=10m",
                    "-test.v=test2json",
                    "-test.paniconexit0",
                )
                assert worker.args[5] == f"-test.run=^({'|'.join(inventory[index::2])})$"
                assert worker.args[6] == f"-test.coverprofile={coverage / f'app-{index + 1}.out'}"
                assert worker.environment is not None
                assert worker.args[7] == f"-test.gocoverdir={worker.environment['GOCOVERDIR']}"
            assert commands[0].environment != commands[1].environment
        elif command.label.endswith("-results"):
            assert command.stdin == tmp_path / f"{command.label.removesuffix('-results')}.log"
            index = 0 if command.label == "app-worker-1-results" else 1
            command.stdout.write_text(_results(inventory[index::2]), encoding="utf-8")
        elif command.label == "remaining-test":
            assert cwd == sidecar
            assert command.args == (
                "go",
                "test",
                "-count=1",
                "-covermode=count",
                "-coverpkg=./internal/query",
                f"-coverprofile={coverage / 'remaining.out'}",
                PEER,
            )

    monkeypatch.setattr(schedule, "_supervise", supervise)
    parts = schedule.run_full_suite(
        "go",
        sidecar,
        tmp_path,
        cover_options=("-covermode=count", "-coverpkg=./internal/query"),
        profile_dir=coverage,
    )

    assert observed == [
        "packages",
        "app-build",
        "app-inventory",
        "app-worker-1",
        "app-worker-2",
        "app-worker-1-results",
        "app-worker-2-results",
        "remaining-test",
    ]
    assert tuple(path.name for path in parts) == ("app-1.out", "app-2.out", "remaining.out")


@pytest.mark.parametrize("source", ["", "    TestMain(m)\n}\n"])
def test_rejects_nondefault_generated_test_main(tmp_path: Path, source: str) -> None:
    if source:
        build = tmp_path / "go-build1" / "b001"
        build.mkdir(parents=True)
        (build / "_testmain.go").write_text(
            f"package main\nfunc main() {{\n{source}", encoding="utf-8"
        )
    with pytest.raises(ValueError, match="test main"):
        schedule._require_default_test_main(tmp_path)


@pytest.mark.parametrize("name", schedule._DIAGNOSTIC_ENV)
def test_rejects_diagnostic_environment_without_starting_app(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, name: str
) -> None:
    monkeypatch.setenv(name, "capture")
    supervise = Mock()
    monkeypatch.setattr(schedule, "_supervise", supervise)
    with pytest.raises(ValueError, match=name):
        schedule._run_app("go", tmp_path, tmp_path, (), ())
    supervise.assert_not_called()


def _results(tests: tuple[str, ...]) -> str:
    events = []
    for test in tests:
        events.extend(
            (
                {"Action": "run", "Test": test},
                {"Action": "output", "Output": "=== RUN   TestPretend\n"},
                {"Action": "run", "Test": f"{test}/nested"},
                {"Action": "pass", "Test": f"{test}/nested"},
                {"Action": "skip" if test == "TestSkip" else "pass", "Test": test},
            )
        )
    return "\n".join(map(json.dumps, [*events, {"Action": "pass"}]))


@pytest.mark.parametrize(
    "events",
    [
        _results(("TestOther",)),
        _results(("TestExpected", "TestExpected")),
        _results(("TestExpected",)).replace('"pass"', '"fail"'),
        '{"Action":"pass","Test":"TestExpected"}\n{"Action":"pass"}',
        _results(("TestExpected",)) + '\n{"Action":"fail","Test":"TestExpected"}',
        _results(("TestExpected",)) + '\n{"Action":"pass"}',
    ],
)
def test_result_verification_rejects_missing_duplicate_and_failed_runs(events: str) -> None:
    with pytest.raises(ValueError, match="exactly once"):
        schedule._verify_results(events, ("TestExpected",), "worker")


def _scope(pid: int, *, exit_code: int | None, members: tuple[int, ...] | None) -> Mock:
    scope = Mock()
    scope.root.pid = pid
    scope.root.poll.return_value = exit_code
    scope.wait_empty.side_effect = lambda *, timeout: ScopeWaitResult(
        () if timeout else members, ("query failed",) if members is None and not timeout else ()
    )
    scope.terminate_all.return_value.termination_requested = True
    return scope


@pytest.mark.parametrize("members", [(1337,), None])
def test_root_exit_with_remaining_or_unverified_job_reclaims_both_workers(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, members: tuple[int, ...] | None
) -> None:
    first = _scope(101, exit_code=0, members=members)
    second = _scope(102, exit_code=None, members=(102,))
    order = Mock()
    order.attach_mock(first.terminate_all, "terminate_first")
    order.attach_mock(second.terminate_all, "terminate_second")
    order.attach_mock(second.wait_empty, "wait_second")
    monkeypatch.setattr(schedule.Command, "launch", Mock(side_effect=[first, second]))

    commands = tuple(
        schedule.Command(f"worker-{n}", ("test.exe",), tmp_path / str(n)) for n in (1, 2)
    )
    with pytest.raises(RuntimeError, match="unverified Job"):
        schedule._supervise(commands, tmp_path, timeout=600)

    assert [call[0] for call in order.mock_calls] == [
        "terminate_first",
        "terminate_second",
        "wait_second",
    ]
    first.close.assert_called_once()
    second.close.assert_called_once()


def test_slow_second_launch_does_not_get_another_deadline(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    now = 0.0
    first = _scope(101, exit_code=None, members=(101,))
    second = _scope(102, exit_code=0, members=())

    def launch(command: schedule.Command, _cwd: Path) -> Mock:
        nonlocal now
        now += 301
        return first if command.label == "worker-1" else second

    monkeypatch.setattr(schedule.Command, "launch", launch)
    monkeypatch.setattr(schedule.time, "monotonic", lambda: now)
    commands = tuple(
        schedule.Command(f"worker-{n}", ("test.exe",), tmp_path / str(n)) for n in (1, 2)
    )
    with pytest.raises(TimeoutError, match="shared 600s deadline"):
        schedule._supervise(commands, tmp_path, timeout=600)

    first.terminate_all.assert_called_once_with(timeout=0)
    second.terminate_all.assert_called_once_with(timeout=0)
    first.close.assert_called_once()
    second.close.assert_called_once()
    second.root.poll.assert_not_called()


@pytest.mark.parametrize(
    "output", [CLEANUP, CLEANUP + "\n    feature_test.go:31: assertion failed"]
)
def test_narrow_retry_repeats_only_the_failed_non_app_command(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, output: str
) -> None:
    commands = []

    def run(command: tuple[str, ...], _cwd: Path, directory: Path, label: str) -> str:
        commands.append((command, label))
        (directory / f"{label}.stdout.log").write_text(output, encoding="utf-8")
        raise subprocess.CalledProcessError(1, command)

    monkeypatch.setattr(schedule, "run_command", run)
    monkeypatch.setattr(schedule.os, "name", "nt")
    command = ("go", "test", "peer")
    with pytest.raises(subprocess.CalledProcessError):
        schedule.run_test_command(command, tmp_path, tmp_path, "remaining")
    assert commands == (
        [(command, "remaining"), (command, "remaining-retry")]
        if output == CLEANUP
        else [(command, "remaining")]
    )
