from __future__ import annotations

import json
import subprocess

import pytest

from qa import next as gate


@pytest.fixture(autouse=True)
def isolate_diagnostic_files(monkeypatch, tmp_path):
    monkeypatch.setattr(gate, "RACE_BINARY_DIR", tmp_path)


@pytest.mark.parametrize("timeout", [False, True])
def test_race_build_observes_timeout_before_kill(monkeypatch, timeout):
    calls = []

    class Process:
        pid = 123
        returncode = 7
        count = 0

        def poll(self):
            return None

        def communicate(self, timeout=None):
            self.count += 1
            if self.count == 1 and timeout is not None and should_timeout:
                raise subprocess.TimeoutExpired("go", timeout)
            return "compiler output", "compiler failure"

    should_timeout = timeout
    monkeypatch.setattr(gate.subprocess, "Popen", lambda *a, **kw: Process())
    monkeypatch.setattr(
        gate,
        "_race_build_process_snapshot",
        lambda pid: calls.append(("snapshot", pid)) or {"status": "root_missing", "processes": []},
    )

    def terminate(p):
        assert list(gate.RACE_BINARY_DIR.glob("timeouts/*.json"))
        calls.append(("kill", p.pid))

    monkeypatch.setattr(gate, "_terminate_process_tree", terminate)
    code, stdout, stderr = gate._run_command(
        ["go", "test", "-c", "-race"], cwd=".", environment={}, timeout=420, race_build=True
    )
    assert code == (124 if timeout else 7)
    assert "compiler failure" in stderr
    events = [
        json.loads(line.removeprefix("RACE_BUILD "))
        for line in stdout.splitlines()
        if line.startswith("RACE_BUILD ")
    ]
    assert [event["phase"] for event in events] == (
        ["started", "timeout", "evidence", "finished"] if timeout else ["started", "finished"]
    )
    assert all(event["pid"] == 123 and event["atUtc"] for event in events)
    assert events[-1]["returncode"] == code
    assert calls == ([("snapshot", 123), ("kill", 123)] if timeout else [])
    if timeout:
        assert events[1]["snapshot"]["status"] == "root_missing"


def test_race_build_snapshot_failure_preserves_timeout(monkeypatch):
    class Process:
        pid = 123
        count = 0

        def poll(self):
            return None

        def communicate(self, timeout=None):
            self.count += 1
            if self.count == 1:
                raise subprocess.TimeoutExpired("go", timeout)
            return "", ""

    killed = []
    monkeypatch.setattr(gate.subprocess, "Popen", lambda *a, **kw: Process())

    def unavailable(pid):
        raise OSError("unavailable")

    monkeypatch.setattr(gate, "_race_build_process_snapshot", unavailable)
    monkeypatch.setattr(gate, "_terminate_process_tree", lambda p: killed.append(p.pid))
    code, stdout, _ = gate._run_command(
        ["go"], cwd=".", environment={}, timeout=420, race_build=True
    )
    assert code == 124
    assert killed == [123]
    assert '"status": "collection_failed"' in stdout


def test_ordinary_command_does_not_collect_or_emit_build_events(monkeypatch):
    class Process:
        returncode = 0

        def communicate(self, timeout=None):
            return "ok", ""

    monkeypatch.setattr(gate.subprocess, "Popen", lambda *a, **kw: Process())
    monkeypatch.setattr(
        gate, "_race_build_process_snapshot", lambda pid: pytest.fail("unexpected snapshot")
    )
    assert gate._run_command(["test"], cwd=".", environment={}, timeout=1) == (0, "ok", "")


def test_race_build_preserves_bounded_compiler_tail(monkeypatch):
    class Process:
        pid = 321
        returncode = 1

        def communicate(self, timeout=None):
            return "x" * 20000, "y" * 20000 + "last failing compiler step"

    monkeypatch.setattr(gate.subprocess, "Popen", lambda *a, **kw: Process())
    code, stdout, stderr = gate._run_command(
        ["go"], cwd=".", environment={}, timeout=1, race_build=True
    )
    assert code == 1
    assert len(stderr) == 16384
    assert stderr.endswith("last failing compiler step")
    assert len(stdout.splitlines()[-1]) == 16384


def test_windows_snapshot_contains_only_requested_tree():
    import os
    import sys

    if os.name != "nt":
        pytest.skip("Windows CIM collector")
    script = "import subprocess,sys; p=subprocess.Popen([sys.executable,'-c','import time; time.sleep(30)']); print(p.pid,flush=True); sys.stdin.readline(); p.terminate(); p.wait()"
    with subprocess.Popen(
        [sys.executable, "-c", script], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True
    ) as process:
        assert process.stdout is not None
        assert process.stdin is not None
        child_pid = int(process.stdout.readline())
        try:
            snapshot = gate._race_build_process_snapshot(process.pid)
            assert snapshot["status"] == "captured"
            rows = snapshot["processes"]
            parents = {row["pid"]: row["parentPid"] for row in rows}
            assert {process.pid, child_pid} <= parents.keys()
            assert os.getpid() not in parents
            for pid in parents:
                visited = set()
                while pid != process.pid:
                    assert pid not in visited
                    visited.add(pid)
                    pid = parents[pid]
            assert all(
                set(row)
                == {"pid", "parentPid", "name", "createdAtUtc", "cpuSeconds", "workingSetBytes"}
                for row in rows
            )
        finally:
            process.stdin.write("done\n")
            process.stdin.flush()
            process.wait(timeout=5)
    assert gate._race_build_process_snapshot(process.pid) == {
        "status": "root_missing",
        "processes": [],
    }


def test_race_compile_timeout_stops_package_before_tests(monkeypatch):
    from threading import Event

    stop = Event()
    observed = []

    def compile_only(command, **kwargs):
        observed.append(command)
        assert kwargs["race_build"] is True
        assert kwargs["timeout"] == 420
        return 124, "compile diagnostics", "timed out"

    monkeypatch.setattr(gate, "_run_command", compile_only)
    code, output, errors = gate._run_race_package(
        [(["test-binary"], 420, ".")],
        environment={},
        stop_event=stop,
        compile_command=["go", "test", "-c", "-race", "-x"],
        compile_cwd=".",
    )
    assert code == 124
    assert stop.is_set()
    assert observed == [["go", "test", "-c", "-race", "-x"]]
    assert "compile diagnostics" in output
    assert errors == ["timed out"]


def test_exited_build_with_inherited_pipe_persists_before_bounded_drain(monkeypatch, tmp_path):
    class ExitedBuild:
        pid = 987
        returncode = 0
        calls = 0

        def poll(self):
            return 0

        def communicate(self, timeout=None):
            self.calls += 1
            if self.calls == 1:
                raise subprocess.TimeoutExpired("go", timeout, output="compile step")
            evidence = list(tmp_path.glob("timeouts/*.json"))
            assert len(evidence) == 1, "evidence must exist before drain"
            payload = json.loads(evidence[0].read_text(encoding="utf-8"))
            assert set(payload) == {"pid", "events"}
            assert '"phase": "timeout"' in payload["events"][-1]
            assert payload["pid"] == self.pid
            assert timeout == 5, "inherited pipe must never cause an unbounded drain"
            raise subprocess.TimeoutExpired("go", timeout)

    process = ExitedBuild()
    monkeypatch.setattr(gate, "RACE_BINARY_DIR", tmp_path)
    monkeypatch.setattr(gate.subprocess, "Popen", lambda *a, **kw: process)
    monkeypatch.setattr(
        gate.subprocess, "run", lambda *a, **kw: pytest.fail("must not kill an exited/reused PID")
    )
    monkeypatch.setattr(
        gate,
        "_race_build_process_snapshot",
        lambda pid: pytest.fail("must not inspect an exited PID"),
    )
    code, stdout, stderr = gate._run_command(
        ["go"], cwd=".", environment={}, timeout=420, race_build=True
    )
    assert code == 124
    assert process.calls == 2
    assert '"phase": "drain_timeout"' in stdout
    assert "output may be incomplete" in stderr
    assert "compile step" in stdout


def test_evidence_write_failure_preserves_existing_file(monkeypatch, tmp_path):
    blocked = tmp_path / "not-a-directory"
    blocked.write_text("existing evidence", encoding="utf-8")
    monkeypatch.setattr(gate, "RACE_BINARY_DIR", blocked)
    result = gate._persist_race_build_timeout(123, ["structured phase"])
    assert result["status"] == "save_failed"
    assert result["errorType"]
    assert blocked.read_text(encoding="utf-8") == "existing evidence"
