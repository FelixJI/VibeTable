"""C1 TaskRuntime + path-grant tests.

Covers the task lifecycle (queued/running/succeeded/failed/cancelled/aborted),
monotonic progress, cooperative cancellation, bounded history, and the
session path-grant issue/resolve/consume/expiry flow.
"""

from __future__ import annotations

import asyncio
import json
from typing import Any

import pytest

import backend.__main__ as backend_main
from backend.application.import_service import ImportFlowError
from backend.application.path_grant import PathGrantError
from backend.application.task_runtime import (
    CancellationToken,
    HostExecutionRuntime,
)
from backend.contracts.task import TaskStatus
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.framing import MAX_FRAME_BYTES, FrameTooLargeError, read_frame
from backend.rpc.server import RpcServer
from tests.backend.legacy_task_runtime import TaskRuntime
from tests.backend.path_grant_fixture import SessionPathGrantStore

# ---------------------------------------------------------------------------
# TaskRuntime
# ---------------------------------------------------------------------------


async def _noop_sink(_: Any) -> None:
    return None


@pytest.mark.asyncio
async def test_host_execution_reports_cancelled_export_even_before_worker_starts() -> None:
    reports = []

    async def sink(status):
        reports.append(status)

    async def export(_task_id, _reporter, _token, _params):
        raise AssertionError("revoked export must never start")

    runtime = HostExecutionRuntime(notification_sink=sink)
    runtime.register("data.export", export)
    await runtime.start("host-task", "data.export", {}, export_grant_id="grant")
    await runtime.settle_export_grant("grant")
    assert [status.state for status in reports] == ["cancelled"]
    assert await runtime.cancel("host-task") is False


@pytest.mark.asyncio
async def test_host_execution_reports_confirmed_result_after_late_cancel() -> None:
    reports = []
    entered = asyncio.Event()
    finish = asyncio.Event()

    async def sink(status):
        reports.append(status)

    async def import_data(_task_id, _reporter, _token, _params):
        entered.set()
        await finish.wait()
        return {"createdCount": 1}

    runtime = HostExecutionRuntime(notification_sink=sink)
    runtime.register("data.import", import_data)
    await runtime.start("host-task", "data.import", {})
    await entered.wait()
    assert await runtime.cancel("host-task") is True
    finish.set()
    for _ in range(10):
        if reports[-1].state == "succeeded":
            break
        await asyncio.sleep(0)
    assert reports[-1].state == "succeeded"
    assert reports[-1].result == {"createdCount": 1}


@pytest.mark.asyncio
async def test_host_execution_reports_unknown_import_without_zero_write_result() -> None:
    reports = []
    entered = asyncio.Event()
    lost_reply = asyncio.Event()

    async def sink(status):
        reports.append(status)

    async def import_data(_task_id, _reporter, _token, _params):
        entered.set()
        await lost_reply.wait()
        raise ImportFlowError("Import submission outcome is unknown", code="import_outcome_unknown")

    runtime = HostExecutionRuntime(notification_sink=sink)
    runtime.register("data.import", import_data)
    await runtime.start("host-task", "data.import", {})
    await entered.wait()
    assert await runtime.cancel("host-task") is True
    lost_reply.set()
    for _ in range(10):
        if reports and reports[-1].state == "failed":
            break
        await asyncio.sleep(0)
    assert reports[-1].state == "failed"
    assert "unknown" in reports[-1].error
    assert reports[-1].result is None


class _PipeWriter:
    """Real in-memory pipe transport: server frames flow to a host reader."""

    def __init__(self, host_reader: asyncio.StreamReader) -> None:
        self._host_reader = host_reader

    def write(self, data: bytes) -> None:
        self._host_reader.feed_data(data)

    async def drain(self) -> None:
        return None


class _BrokenDrainWriter(_PipeWriter):
    """A ruptured pipe: bytes are accepted, then the transport fails."""

    def __init__(self, host_reader: asyncio.StreamReader) -> None:
        super().__init__(host_reader)
        self.drain_calls = 0

    async def drain(self) -> None:
        self.drain_calls += 1
        raise OSError("host pipe broke after a partial write")


def _execution_report_pipe() -> tuple[asyncio.StreamReader, HostExecutionRuntime]:
    """Wire the runtime through the production notification composition.

    The sink mirrors ``backend/__main__.py::notify_task_status``: it hands the
    ``TaskStatus`` to the production ``_send_execution_report``, which drives
    the real ``RpcServer.notify``/``write_frame`` over an in-memory pipe, so
    the 4 MiB frame cap is enforced by the same code the host process uses.
    """
    host_reader = asyncio.StreamReader()
    server = RpcServer(asyncio.StreamReader(), _PipeWriter(host_reader), RpcDispatcher())

    async def sink(status: TaskStatus) -> None:
        await backend_main._send_execution_report(server, status)

    return host_reader, HostExecutionRuntime(notification_sink=sink)


async def _read_status(host_reader: asyncio.StreamReader) -> dict[str, Any]:
    frame = await asyncio.wait_for(read_frame(host_reader), timeout=5.0)
    assert frame is not None
    assert "id" not in frame
    assert frame["method"] == "task.executionReport"
    return frame["params"]


@pytest.mark.asyncio
async def test_oversize_terminal_downgrades_to_small_failure_and_releases_slot() -> None:
    """An oversized result must converge the host, not strand it "running".

    Before the fix the oversized terminal frame raised out of ``execute()``
    after the handle was popped: the host kept the last "running" report
    forever, ``cancel`` answered ``False`` for an unknown task, and the Web
    busy state never converged. Now a compact explicit failure terminal is
    delivered through the same pipe and only then is the slot released.
    """
    host_reader, runtime = _execution_report_pipe()

    async def preview(_task_id, _reporter, _token, _params):  # type: ignore[no-untyped-def]
        return {"rows": [{"payload": "a" * 8192} for _ in range(1024)]}

    runtime.register("data.previewImport", preview)
    assert await runtime.start("host-task", "data.previewImport", {}) is True

    running = await _read_status(host_reader)
    assert running["state"] == "running"

    terminal = await _read_status(host_reader)
    assert terminal["state"] == "failed"
    assert terminal["result"] is None
    # The failure names the real frame cap and claims neither success nor
    # cancellation, nor any business rollback.
    assert str(MAX_FRAME_BYTES) in (terminal["error"] or "")
    assert "取消" not in (terminal["error"] or "")
    assert "回滚" not in (terminal["error"] or "")
    # The degraded terminal itself fits the unchanged frame cap.
    assert len(json.dumps(terminal, ensure_ascii=False).encode("utf-8")) <= MAX_FRAME_BYTES
    # The execution slot is freed only after a terminal state was delivered.
    assert await runtime.cancel("host-task") is False


@pytest.mark.asyncio
async def test_real_pipe_success_semantics_unchanged() -> None:
    host_reader, runtime = _execution_report_pipe()

    async def apply_import(_task_id, _reporter, _token, _params):  # type: ignore[no-untyped-def]
        return {"createdCount": 1}

    runtime.register("data.import", apply_import)
    assert await runtime.start("host-task", "data.import", {}) is True

    assert (await _read_status(host_reader))["state"] == "running"
    terminal = await _read_status(host_reader)
    assert terminal["state"] == "succeeded"
    assert terminal["result"] == {"createdCount": 1}
    assert terminal["error"] is None
    assert await runtime.cancel("host-task") is False


@pytest.mark.asyncio
async def test_real_pipe_cancel_semantics_unchanged() -> None:
    host_reader, runtime = _execution_report_pipe()
    entered = asyncio.Event()

    async def preview(_task_id, _reporter, token, _params):  # type: ignore[no-untyped-def]
        entered.set()
        await token.wait()
        raise asyncio.CancelledError

    runtime.register("data.previewImport", preview)
    assert await runtime.start("host-task", "data.previewImport", {}) is True
    await entered.wait()
    assert await runtime.cancel("host-task") is True

    assert (await _read_status(host_reader))["state"] == "running"
    terminal = await _read_status(host_reader)
    assert terminal["state"] == "cancelled"
    assert terminal["result"] is None
    assert terminal["error"] is None
    assert await runtime.cancel("host-task") is False


@pytest.mark.asyncio
async def test_partial_write_failure_propagates_without_degraded_resend() -> None:
    """Only the provable pre-write oversize boundary degrades.

    A drain failure after ``write`` accepted bytes may already have delivered
    a partial frame; resending a terminal then could interleave frames. The
    original transport error must propagate unchanged (worker retirement
    semantics), and no degraded terminal may follow the partial write.
    """
    host_reader = asyncio.StreamReader()
    writer = _BrokenDrainWriter(host_reader)
    server = RpcServer(asyncio.StreamReader(), writer, RpcDispatcher())

    with pytest.raises(OSError, match="host pipe broke"):
        await backend_main._send_execution_report(
            server,
            TaskStatus(task_id="host-task", kind="data.previewImport", state="succeeded"),
        )

    assert writer.drain_calls == 1
    # Only the original (partial-wire) frame was accepted; nothing was resent.
    delivered = [line for line in bytes(host_reader._buffer).split(b"\n") if line]  # type: ignore[attr-defined]
    assert len(delivered) == 1
    assert json.loads(delivered[0])["params"]["state"] == "succeeded"


@pytest.mark.asyncio
async def test_oversize_non_terminal_status_is_never_degraded_to_a_terminal() -> None:
    """A non-terminal report must not be turned into a fabricated terminal."""
    host_reader = asyncio.StreamReader()
    server = RpcServer(asyncio.StreamReader(), _PipeWriter(host_reader), RpcDispatcher())
    oversized_running = TaskStatus(
        task_id="host-task",
        kind="data.previewImport",
        state="running",
        result={"rows": [{"payload": "a" * 8192} for _ in range(1024)]},
    )

    with pytest.raises(FrameTooLargeError):
        await backend_main._send_execution_report(server, oversized_running)

    assert bytes(host_reader._buffer) == b""  # type: ignore[attr-defined]


@pytest.mark.asyncio
async def test_create_runs_handler_and_reports_succeeded() -> None:
    runtime = TaskRuntime(notification_sink=_noop_sink)

    async def handler(task_id, reporter, token):  # type: ignore[no-untyped-def]
        await reporter.report(done=1, total=2, message="halfway")
        return {"created": 1}

    runtime.register("test.echo", handler)
    status = await runtime.create("test.echo", {})
    assert status.state in ("queued", "running")
    # Wait for the background task to finish.
    await asyncio.sleep(0.05)
    final = runtime.status(status.task_id)
    assert final.state == "succeeded"
    assert final.result == {"created": 1}


@pytest.mark.asyncio
async def test_create_passes_frozen_request_params_to_product_handler() -> None:
    runtime = TaskRuntime(notification_sink=_noop_sink)
    seen: list[dict[str, Any]] = []

    async def handler(task_id, reporter, token, params):  # type: ignore[no-untyped-def]
        del task_id, reporter, token
        seen.append(dict(params))
        params["collection"] = "changed-inside-handler"
        return {"accepted": True}

    runtime.register("data.export", handler)
    request = {"collection": "orders"}
    status = await runtime.create("data.export", request)
    request["collection"] = "changed-by-caller"
    await runtime.wait(status.task_id)

    assert seen == [{"collection": "orders"}]
    assert runtime.status(status.task_id).result == {"accepted": True}


@pytest.mark.asyncio
async def test_failed_task_carries_error_message() -> None:
    runtime = TaskRuntime(notification_sink=_noop_sink)

    async def handler(task_id, reporter, token):  # type: ignore[no-untyped-def]
        raise RuntimeError("boom")

    runtime.register("test.fail", handler)
    status = await runtime.create("test.fail", {})
    await asyncio.sleep(0.05)
    final = runtime.status(status.task_id)
    assert final.state == "failed"
    assert "boom" in (final.error or "")


@pytest.mark.asyncio
async def test_cancel_marks_running_task_cancelled() -> None:
    runtime = TaskRuntime(notification_sink=_noop_sink)

    async def handler(task_id, reporter, token):  # type: ignore[no-untyped-def]
        await token.wait()
        raise asyncio.CancelledError

    runtime.register("test.slow", handler)
    status = await runtime.create("test.slow", {})
    await asyncio.sleep(0.02)
    await runtime.cancel(status.task_id)
    # The cancel sets the flag; the handler wakes and the run loop finalizes.
    await asyncio.sleep(0.05)
    final = runtime.status(status.task_id)
    assert final.state == "cancelled"


@pytest.mark.asyncio
async def test_cancel_arriving_after_handler_commit_keeps_success_result() -> None:
    runtime = TaskRuntime(notification_sink=_noop_sink)

    async def handler(task_id, reporter, token):  # type: ignore[no-untyped-def]
        token.cancel()
        return {"committed": True}

    runtime.register("test.commit", handler)
    status = await runtime.create("test.commit", {})
    await asyncio.sleep(0.05)
    final = runtime.status(status.task_id)
    assert final.state == "succeeded"
    assert final.result == {"committed": True}


@pytest.mark.asyncio
async def test_progress_is_monotonic() -> None:
    runtime = TaskRuntime(notification_sink=_noop_sink)
    seen: list[int] = []

    async def handler(task_id, reporter, token):  # type: ignore[no-untyped-def]
        await reporter.report(done=5, total=10)
        await reporter.report(done=3, total=10)  # should NOT move backwards
        seen.append(reporter._done)  # type: ignore[attr-defined]
        return {}

    runtime.register("test.mono", handler)
    created = await runtime.create("test.mono", {})
    await asyncio.sleep(0.05)
    assert seen == [5]
    assert runtime.status(created.task_id).progress.done == 5


@pytest.mark.asyncio
async def test_abort_unresumable_marks_local_tasks() -> None:
    runtime = TaskRuntime(notification_sink=_noop_sink)

    async def handler(task_id, reporter, token):  # type: ignore[no-untyped-def]
        await asyncio.sleep(10)
        return {}

    runtime.register("data.export", handler)
    status = await runtime.create("data.export", {})
    # Simulate a restart: abort local-side-effect tasks still "running".
    count = runtime.abort_unresumable({"data.export"})
    assert count == 1
    assert runtime.status(status.task_id).state == "aborted"


@pytest.mark.asyncio
async def test_unknown_kind_rejected() -> None:
    runtime = TaskRuntime(notification_sink=_noop_sink)
    with pytest.raises(ValueError, match="unknown task kind"):
        await runtime.create("nope", {})


@pytest.mark.asyncio
async def test_completed_history_is_bounded() -> None:
    runtime = TaskRuntime(notification_sink=_noop_sink)

    async def handler(task_id, reporter, token):  # type: ignore[no-untyped-def]
        return {}

    runtime.register("test.bound", handler)
    from tests.backend.legacy_task_runtime import MAX_COMPLETED_TASKS

    for _ in range(MAX_COMPLETED_TASKS + 5):
        await runtime.create("test.bound", {})
    await asyncio.sleep(0.1)
    # The store must not grow unbounded.
    assert len(runtime._tasks) <= MAX_COMPLETED_TASKS + 2  # type: ignore[attr-defined]


# ---------------------------------------------------------------------------
# CancellationToken
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_cancellation_token_signals() -> None:
    token = CancellationToken()
    assert not token.cancelled
    token.cancel()
    assert token.cancelled
    # wait() returns immediately once cancelled.
    await asyncio.wait_for(token.wait(), timeout=1.0)


# ---------------------------------------------------------------------------
# SessionPathGrantStore
# ---------------------------------------------------------------------------


def test_grant_issue_and_resolve_returns_normalized_path(tmp_path: Any) -> None:
    store = SessionPathGrantStore()
    target = tmp_path / "contracts.xlsx"
    target.write_text("data")
    grant = store.issue(
        purpose="import_source",
        direction="read",
        path=str(target),
    )
    assert grant.purpose == "import_source"
    assert grant.display_name == "contracts.xlsx"
    resolved = store.resolve(grant.grant_id, purpose="import_source", direction="read")
    assert resolved.endswith("contracts.xlsx")


def test_grant_resolve_rejects_wrong_purpose(tmp_path: Any) -> None:
    store = SessionPathGrantStore()
    grant = store.issue(purpose="export_target", direction="write", path=str(tmp_path / "out.csv"))
    with pytest.raises(PathGrantError) as exc_info:
        store.resolve(grant.grant_id, purpose="import_source", direction="read")
    assert exc_info.value.code == "grant_purpose_mismatch"


def test_grant_resolve_rejects_wrong_direction(tmp_path: Any) -> None:
    store = SessionPathGrantStore()
    grant = store.issue(purpose="export_target", direction="write", path=str(tmp_path / "out.csv"))
    with pytest.raises(PathGrantError) as exc_info:
        store.resolve(grant.grant_id, purpose="export_target", direction="read")
    assert exc_info.value.code == "grant_direction_mismatch"


def test_grant_expired_after_ttl() -> None:
    clock = [1000.0]
    store = SessionPathGrantStore(clock=lambda: clock[0], ttl_seconds=60.0)
    grant = store.issue(purpose="import_source", direction="read", path="x.xlsx")
    clock[0] += 61.0
    with pytest.raises(PathGrantError) as exc_info:
        store.resolve(grant.grant_id, purpose="import_source", direction="read")
    assert exc_info.value.code == "grant_expired"


def test_grant_unknown_rejected() -> None:
    store = SessionPathGrantStore()
    with pytest.raises(PathGrantError) as exc_info:
        store.resolve("bogus", purpose="import_source", direction="read")
    assert exc_info.value.code == "grant_unknown"


def test_grant_consumed_cannot_be_replayed(tmp_path: Any) -> None:
    store = SessionPathGrantStore()
    grant = store.issue(
        purpose="import_source",
        direction="read",
        path=str(tmp_path / "contracts.xlsx"),
    )
    store.consume(grant.grant_id)

    with pytest.raises(PathGrantError) as exc_info:
        store.resolve(grant.grant_id, purpose="import_source", direction="read")

    assert exc_info.value.code == "grant_consumed"


def test_grant_descriptor_hides_raw_path(tmp_path: Any) -> None:
    store = SessionPathGrantStore()
    raw_path = str(tmp_path / "secret.xlsx")
    grant = store.issue(purpose="import_source", direction="read", path=raw_path)
    descriptor = store.descriptor(grant.grant_id)
    # The public descriptor must not leak the raw filesystem path.
    assert raw_path not in descriptor.model_dump_json()
    assert descriptor.display_name == "secret.xlsx"
