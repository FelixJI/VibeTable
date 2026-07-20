"""C1 TaskRuntime + path-grant tests.

Covers the task lifecycle (queued/running/succeeded/failed/cancelled/aborted),
monotonic progress, cooperative cancellation, bounded history, and the
session path-grant issue/resolve/consume/expiry flow.
"""

from __future__ import annotations

import asyncio
from typing import Any

import pytest

from backend.application.path_grant import PathGrantError, SessionPathGrantStore
from backend.application.task_runtime import (
    CancellationToken,
    TaskRuntime,
)

# ---------------------------------------------------------------------------
# TaskRuntime
# ---------------------------------------------------------------------------


async def _noop_sink(_: Any) -> None:
    return None


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
        return {}

    runtime.register("test.slow", handler)
    status = await runtime.create("test.slow", {})
    await asyncio.sleep(0.02)
    await runtime.cancel(status.task_id)
    # The cancel sets the flag; the handler wakes and the run loop finalizes.
    await asyncio.sleep(0.05)
    final = runtime.status(status.task_id)
    assert final.state == "cancelled"


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
    from backend.application.task_runtime import MAX_COMPLETED_TASKS

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
