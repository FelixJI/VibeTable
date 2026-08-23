from __future__ import annotations

import ast
import io
import json
import os
import subprocess
import sys
import threading
import traceback
from pathlib import Path
from typing import Any

import pytest

from tests.integration import _sidecar_process_output as process_output
from tests.integration import packaged_sidecar_matrix as matrix


class _FakeResponse:
    def __init__(self, payload: dict[str, Any]) -> None:
        self.status = 200
        self.headers: dict[str, str] = {}
        self._body = json.dumps(payload).encode()

    def __enter__(self) -> _FakeResponse:
        return self

    def __exit__(self, *_args: object) -> None:
        return None

    def read(self) -> bytes:
        return self._body


class _ExitedFakeProcess:
    def __init__(self, stdout: str, stderr: str) -> None:
        self.stdout = io.StringIO(stdout)
        self.stderr = io.StringIO(stderr)
        self.returncode: int | None = None
        self.kill_calls = 0
        self.wait_calls = 0

    def poll(self) -> int | None:
        return self.returncode

    def wait(self, timeout: float) -> int:
        del timeout
        self.wait_calls += 1
        assert self.returncode is not None
        return self.returncode

    def kill(self) -> None:
        self.kill_calls += 1
        self.returncode = -9


class _DelayedLineStream:
    def __init__(self, line: str, release: threading.Event) -> None:
        self._line = line
        self._release = release
        self._delivered = False
        self.closed = False

    def readline(self, _size: int = -1) -> str:
        if self._delivered:
            return ""
        assert self._release.wait(1)
        self._delivered = True
        return self._line

    def close(self) -> None:
        self.closed = True


def _safe_sidecar_event(event: str) -> str:
    return json.dumps(
        {
            "timestamp": "2026-08-24T01:02:03Z",
            "level": "error",
            "module": "sidecar",
            "event": event,
            "errorCode": "sidecar.test_failure",
            "requestId": "must-not-escape",
            "operationId": None,
            "workspaceId": "must-not-escape",
            "sessionEpoch": 7,
            "jobId": None,
            "durationMs": 12,
        },
        separators=(",", ":"),
    )


def test_sidecar_transport_failure_preserves_safe_process_evidence_after_stop(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    ready = json.dumps(
        {
            "contract": "vibetable.sidecar.ready.v1",
            "event": "sidecar.ready",
            "address": "127.0.0.1:43123",
        }
    )
    process = _ExitedFakeProcess(
        ready + "\nignored stdout value\n",
        _safe_sidecar_event("sidecar.transport_failed") + "\n",
    )
    monkeypatch.setattr(matrix.subprocess, "Popen", lambda *_args, **_kwargs: process)
    calls = 0

    def fake_urlopen(_request: object, *, timeout: float) -> _FakeResponse:
        nonlocal calls
        del timeout
        calls += 1
        if calls == 1:
            return _FakeResponse({"status": "ok"})
        process.returncode = 23
        error = ConnectionResetError("injected secret reset detail")
        error.winerror = 10054
        raise error

    monkeypatch.setattr(matrix.urllib.request, "urlopen", fake_urlopen)
    data_dir = tmp_path / "data"
    data_dir.mkdir()
    sidecar = matrix.Sidecar(tmp_path / "vibetable-pb.exe", data_dir)

    sidecar.start()
    dynamic_id = "business" + "-record-secret"
    dynamic_path = f"/api/vibetable/v2/field-settings/{dynamic_id}?secret=value"
    with pytest.raises(AssertionError) as caught:
        sidecar.request("GET", dynamic_path)
    sidecar.stop()

    message = str(caught.value)
    assert "GET /api/vibetable/v2/field-settings/{tableId}" in message
    assert "transport=ConnectionResetError" in message
    assert "winerror=10054" in message
    assert "exit=23" in message
    assert "sidecar.transport_failed" in message
    assert "secret=value" not in message
    assert "injected secret reset detail" not in message
    assert "must-not-escape" not in message
    formatted = "".join(traceback.format_exception(caught.type, caught.value, caught.tb))
    assert "injected secret reset detail" not in formatted
    assert dynamic_id not in formatted
    assert "secret=value" not in formatted
    assert caught.value.__cause__ is None
    assert caught.value.__suppress_context__ is True

    assert sidecar.process_evidence.exit_code == 23
    assert process.stdout.closed
    assert process.stderr.closed


def test_transport_error_renders_evidence_finalized_after_raise(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    ready = json.dumps(
        {
            "contract": "vibetable.sidecar.ready.v1",
            "event": "sidecar.ready",
            "address": "127.0.0.1:43123",
        }
    )
    process = _ExitedFakeProcess(
        ready + "\n",
        _safe_sidecar_event("sidecar.late_exit") + "\n",
    )
    monkeypatch.setattr(matrix.subprocess, "Popen", lambda *_args, **_kwargs: process)
    calls = 0

    def fake_urlopen(_request: object, *, timeout: float) -> _FakeResponse:
        nonlocal calls
        del timeout
        calls += 1
        if calls == 1:
            return _FakeResponse({"status": "ok"})
        raise ConnectionResetError("reset before process exit")

    monkeypatch.setattr(matrix.urllib.request, "urlopen", fake_urlopen)
    data_dir = tmp_path / "data"
    data_dir.mkdir()
    sidecar = matrix.Sidecar(tmp_path / "vibetable-pb.exe", data_dir)

    sidecar.start()
    with pytest.raises(AssertionError) as caught:
        sidecar.request("POST", "/api/vibetable/v1/history/restore-apply")
    assert process.returncode is None

    process.returncode = 23
    sidecar.stop()

    message = str(caught.value)
    assert "POST /api/vibetable/v1/history/restore-apply" in message
    assert "transport=ConnectionResetError" in message
    assert "exit=23" in message
    assert "sidecar.late_exit" in message


def test_transport_error_waits_for_final_event_after_process_exit(tmp_path: Path) -> None:
    release = threading.Event()
    delayed_stderr = _DelayedLineStream(
        _safe_sidecar_event("sidecar.final_event") + "\n",
        release,
    )
    process = _ExitedFakeProcess("", "")
    process.stderr = delayed_stderr
    process.returncode = 23
    output = process_output.SidecarProcessOutput.attach(process.stdout, delayed_stderr)
    sidecar = matrix.Sidecar(tmp_path / "vibetable-pb.exe", tmp_path / "data")
    sidecar.process = process
    sidecar._output = output
    error = matrix.SidecarTransportError(
        "GET /api/vibetable/v2/capabilities",
        sidecar.process_evidence,
    )
    sidecar._pending_transport_errors.append(error)

    initial_message = str(error)
    assert "exit=23" in initial_message
    assert "sidecar.final_event" not in initial_message
    assert output.readers_alive == ("packaged-sidecar-stderr",)

    release.set()
    sidecar.stop()
    message = str(error)
    assert "exit=23" in message
    assert "sidecar.final_event" in message
    assert delayed_stderr.closed


def test_process_output_rejects_malformed_level_and_continues_draining() -> None:
    malformed = json.loads(_safe_sidecar_event("sidecar.malformed"))
    malformed["level"] = []
    stderr = io.StringIO(
        json.dumps(malformed) + "\n" + _safe_sidecar_event("sidecar.after_malformed") + "\n"
    )
    owner = process_output.SidecarProcessOutput.attach(io.StringIO("ready\n"), stderr)

    assert owner.wait_readiness(timeout=1) == "ready\n"
    owner.finish_after_process_exit()
    snapshot = owner.snapshot()

    assert snapshot.stderr_rejected == 1
    assert snapshot.reader_errors == 0
    assert "sidecar.after_malformed" in snapshot.diagnostic_text()


def test_process_output_drains_full_real_pipes_and_owns_cleanup() -> None:
    ready = {
        "contract": "vibetable.sidecar.ready.v1",
        "event": "sidecar.ready",
        "address": "127.0.0.1:43123",
    }
    script = (
        "import json,sys\n"
        f"print(json.dumps({ready!r}), flush=True)\n"
        "chunk = 'x' * 8192\n"
        "for _ in range(256):\n"
        "    sys.stdout.write(chunk)\n"
        "    sys.stdout.flush()\n"
        "    sys.stderr.write(chunk + '\\n')\n"
        "    sys.stderr.flush()\n"
    )
    process = subprocess.Popen(
        [sys.executable, "-c", script],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    assert process.stdout is not None
    assert process.stderr is not None
    owner = process_output.SidecarProcessOutput.attach(process.stdout, process.stderr)

    assert json.loads(owner.wait_readiness(timeout=5)) == ready
    assert process.wait(timeout=10) == 0
    owner.finish_after_process_exit()
    snapshot = owner.snapshot()

    assert snapshot.stdout_discarded_chars == 256 * 8192
    assert snapshot.stderr_overlong == 256
    assert owner.readers_alive == ()
    assert process.stdout.closed
    assert process.stderr.closed


def test_process_output_is_bounded_and_rejects_unapproved_content() -> None:
    ready = json.dumps(
        {
            "contract": "vibetable.sidecar.ready.v1",
            "event": "sidecar.ready",
            "address": "127.0.0.1:43123",
        }
    )
    safe_events = "".join(
        _safe_sidecar_event(f"sidecar.event_{index:04d}") + "\n" for index in range(500)
    )
    unknown = json.dumps({"event": "business-secret-marker"}) + "\n"
    overlong = (
        "private-workspace-value".ljust(
            process_output.MAX_STDERR_LINE_CHARS,
            "x",
        )
        + "\n"
    )
    stdout = io.StringIO(ready + "\ndiscarded-business-value\n")
    stderr = io.StringIO(unknown + overlong + safe_events)

    owner = process_output.SidecarProcessOutput.attach(stdout, stderr)

    assert owner.wait_readiness(timeout=1) == ready + "\n"
    owner.finish_after_process_exit()
    snapshot = owner.snapshot()
    diagnostic = snapshot.diagnostic_text()

    assert snapshot.stdout_discarded_chars == len("discarded-business-value\n")
    assert snapshot.stderr_rejected == 1
    assert snapshot.stderr_overlong == 1
    assert snapshot.stderr_dropped > 0
    serialized_tail_chars = (
        2 + sum(map(len, snapshot.stderr_events)) + max(0, len(snapshot.stderr_events) - 1)
    )
    assert serialized_tail_chars <= process_output.MAX_STDERR_TAIL_CHARS
    assert "sidecar.event_0499" in diagnostic
    assert "business-secret-marker" not in diagnostic
    assert "private-workspace-value" not in diagnostic
    assert "must-not-escape" not in diagnostic


def test_process_output_rejects_readiness_line_over_limit() -> None:
    stdout = io.StringIO("r" * process_output.MAX_READINESS_CHARS + "\n")
    owner = process_output.SidecarProcessOutput.attach(stdout, io.StringIO(""))

    with pytest.raises(ValueError, match="readiness line exceeded limit"):
        owner.wait_readiness(timeout=1)

    owner.finish_after_process_exit()
    snapshot = owner.snapshot()
    assert snapshot.readiness_phase == "overlong"
    assert "r" * 100 not in snapshot.diagnostic_text()


def test_sidecar_invalid_readiness_does_not_echo_process_output(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    process = _ExitedFakeProcess("invalid-readiness-business-secret\n", "")
    monkeypatch.setattr(matrix.subprocess, "Popen", lambda *_args, **_kwargs: process)
    data_dir = tmp_path / "data"
    data_dir.mkdir()
    sidecar = matrix.Sidecar(tmp_path / "vibetable-pb.exe", data_dir)

    with pytest.raises(AssertionError, match="invalid readiness line") as caught:
        sidecar.start()

    assert "business-secret" not in str(caught.value)
    assert sidecar.process is None


def test_sidecar_stop_serializes_concurrent_callers(tmp_path: Path) -> None:
    process = _ExitedFakeProcess("", "")
    process.returncode = 0
    first_poll_entered = threading.Event()
    release_first_poll = threading.Event()
    poll_calls = 0
    poll_lock = threading.Lock()

    def coordinated_poll() -> int:
        nonlocal poll_calls
        with poll_lock:
            poll_calls += 1
            current_call = poll_calls
        if current_call == 1:
            first_poll_entered.set()
            assert release_first_poll.wait(1)
        return 0

    process.poll = coordinated_poll
    sidecar = matrix.Sidecar(tmp_path / "vibetable-pb.exe", tmp_path / "data")
    sidecar.process = process
    first = threading.Thread(target=sidecar.stop)
    second = threading.Thread(target=sidecar.stop)

    first.start()
    assert first_poll_entered.wait(1)
    second.start()
    release_first_poll.set()
    first.join(1)
    second.join(1)

    assert not first.is_alive()
    assert not second.is_alive()
    assert poll_calls == 1
    assert process.wait_calls == 1
    assert sidecar.process is None


def test_sidecar_start_failure_stops_process_and_closes_pipes(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binary = Path(os.environ["SYSTEMROOT"]) / "System32" / "where.exe"
    data_dir = tmp_path / "data"
    data_dir.mkdir()
    real_popen = subprocess.Popen
    started: list[subprocess.Popen[str]] = []

    def capture_popen(
        args: list[str],
        *,
        cwd: Path,
        env: dict[str, str],
        stdout: int,
        stderr: int,
        text: bool,
        encoding: str,
        errors: str,
    ) -> subprocess.Popen[str]:
        process = real_popen(
            args,
            cwd=cwd,
            env=env,
            stdout=stdout,
            stderr=stderr,
            text=text,
            encoding=encoding,
            errors=errors,
        )
        started.append(process)
        return process

    monkeypatch.setattr(matrix.subprocess, "Popen", capture_popen)
    sidecar = matrix.Sidecar(binary, data_dir)

    with pytest.raises(AssertionError, match="invalid readiness line"):
        sidecar.start()

    assert len(started) == 1
    process = started[0]
    assert process.poll() is not None
    assert process.stdout is not None
    assert process.stdout.closed
    assert process.stderr is not None
    assert process.stderr.closed
    assert sidecar.process is None
    assert sidecar.address == ""
    data_dir.rmdir()


def test_strict_entry_builds_release_and_uses_only_published_binary() -> None:
    source = Path(matrix.__file__).read_text(encoding="utf-8")
    tree = ast.parse(source)
    imports = {
        alias.name
        for node in ast.walk(tree)
        if isinstance(node, ast.Import)
        for alias in node.names
    }
    assert "pocketbase" not in imports
    assert matrix.PUBLISH_ROOT == (matrix.REPO_ROOT / "dist" / "VibeTable.Next")
    assert "scripts/build_next.py" in source
    assert 'command.append("--release")' in source
    assert "NewWithConfig" not in source


def test_matrix_resolves_sidecar_from_publish_layout(tmp_path: Path) -> None:
    package_root = tmp_path / "VibeTable.Next"
    binary = package_root / "resources" / "sidecar" / matrix.SIDECAR_NAME
    binary.parent.mkdir(parents=True)
    binary.write_bytes(b"sidecar")
    layout = package_root / "resources" / "publish-layout.json"
    layout.write_text(
        json.dumps({"launch": {"sidecar": f"resources/sidecar/{matrix.SIDECAR_NAME}"}}),
        encoding="utf-8",
    )

    assert matrix.published_sidecar_binary(package_root) == binary.resolve()


def test_matrix_rejects_sidecar_path_outside_package_root(tmp_path: Path) -> None:
    package_root = tmp_path / "VibeTable.Next"
    resources = package_root / "resources"
    resources.mkdir(parents=True)
    (resources / "publish-layout.json").write_text(
        json.dumps({"launch": {"sidecar": f"../{matrix.SIDECAR_NAME}"}}),
        encoding="utf-8",
    )

    with pytest.raises(AssertionError, match="escapes package root"):
        matrix.published_sidecar_binary(package_root)


def test_matrix_declares_every_plan_12_4_coverage_axis() -> None:
    source = Path(matrix.__file__).read_text(encoding="utf-8")
    expected = {
        "fresh-data+migrations",
        "schema-create-alter-index",
        "record-crud-query+relation-lookup",
        "atomic-batch-rollback",
        "formula-preview-save-backfill",
        "file-upload-download-delete-thumb-protected",
        "audit+restore",
        "sse",
        "process-restart",
        "record-delete",
        "workspace-v2-build-info",
        "workspace-v2-capabilities",
        "workspace-v2-legacy-write-rejection",
        "workspace-v2-snapshot-package",
    }
    assert all(axis in source for axis in expected)
    assert '"backup+restore"' not in source


def test_matrix_creates_schema_only_through_v2_lifecycle_and_field_change() -> None:
    source = Path(matrix.__file__).read_text(encoding="utf-8")

    assert '"/api/vibetable/v2/schema/tables"' in source
    assert '"/api/vibetable/v2/field-change/plan"' in source
    assert '"/api/vibetable/v2/field-change/apply"' in source
    assert "/api/vibetable/v1/schema/apply" not in source
    assert 'receipt["tableId"]' in source
    assert 'receipt["physicalName"]' not in source
    assert 'described["physicalName"]' not in source
