"""Legacy Python task lifecycle oracle for worker format and grant tests."""

from __future__ import annotations

import asyncio
import copy
import inspect
import time
import uuid
from collections.abc import Callable
from typing import Any

from backend.application.task_runtime import (
    CancellationToken,
    NotificationSink,
    ProgressReporter,
    TaskHandler,
)
from backend.contracts.task import TaskProgress, TaskState, TaskStatus

MAX_COMPLETED_TASKS = 64


async def _noop_sink(_status: TaskStatus) -> None:
    return None


class TaskRecord:
    """Internal record of one task's state + result."""

    __slots__ = (
        "completed",
        "created_at",
        "error",
        "export_grant_id",
        "kind",
        "progress",
        "result",
        "state",
        "task_id",
    )

    def __init__(self, task_id: str, kind: str) -> None:
        self.export_grant_id: str | None = None
        self.task_id = task_id
        self.kind = kind
        self.state: TaskState = "queued"
        self.progress = TaskProgress()
        self.result: Any = None
        self.error: str | None = None
        self.created_at = time.time()
        self.completed = asyncio.Event()


class TaskRuntime:
    """Registry + executor for cancellable background tasks."""

    def __init__(
        self,
        *,
        notification_sink: NotificationSink | None = None,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self._handlers: dict[str, TaskHandler] = {}
        self._handlers_accept_params: dict[str, bool] = {}
        self._tasks: dict[str, TaskRecord] = {}
        self._tokens: dict[str, CancellationToken] = {}
        self._async_tasks: dict[str, asyncio.Task[Any]] = {}
        self._sink = notification_sink or _noop_sink
        self._clock = clock
        self._lock = asyncio.Lock()

    # ------------------------------------------------------------------
    # Handler registration
    # ------------------------------------------------------------------

    def register(self, kind: str, handler: TaskHandler) -> None:
        """Register ``handler`` for task ``kind`` (e.g. ``data.import``)."""
        self._handlers[kind] = handler
        self._handlers_accept_params[kind] = len(inspect.signature(handler).parameters) >= 4

    def unregister(self, kind: str) -> bool:
        """Remove a dynamically registered handler after its last task started."""

        self._handlers_accept_params.pop(kind, None)
        return self._handlers.pop(kind, None) is not None

    # ------------------------------------------------------------------
    # Task lifecycle
    # ------------------------------------------------------------------

    async def create(
        self, kind: str, params: dict[str, Any], *, export_grant_id: str | None = None
    ) -> TaskStatus:
        """Create and start a task of ``kind`` with ``params``.

        Returns the initial ``queued``/``running`` status. The task runs in the
        background; the host learns of completion via ``task.status``
        notifications and may poll :meth:`status`.
        """
        handler = self._handlers.get(kind)
        if handler is None:
            raise ValueError(f"unknown task kind {kind!r}")
        task_id = f"task-{uuid.uuid4().hex[:12]}"
        record = TaskRecord(task_id, kind)
        record.export_grant_id = export_grant_id
        token = CancellationToken()
        async with self._lock:
            self._tasks[task_id] = record
            self._tokens[task_id] = token
        # Start immediately (single-slot; the runtime is single-process). Hold
        # a strong reference so the task is not garbage-collected mid-run.
        task = asyncio.create_task(
            self._run(
                record,
                token,
                handler,
                copy.deepcopy(params),
                accepts_params=self._handlers_accept_params[kind],
            )
        )
        self._async_tasks[task_id] = task
        task.add_done_callback(lambda _: self._async_tasks.pop(task_id, None))
        return self._snapshot(record)

    async def _run(
        self,
        record: TaskRecord,
        token: CancellationToken,
        handler: TaskHandler,
        params: dict[str, Any],
        *,
        accepts_params: bool,
    ) -> None:
        record.state = "running"

        async def report_sink(status: TaskStatus) -> None:
            record.progress = status.progress
            await self._sink(status)

        reporter = ProgressReporter(record.task_id, record.kind, report_sink)
        # Bind params into the handler via a closure: handlers are registered
        # with a fixed signature, so params are injected through a wrapper.
        try:
            await self._emit(record)
            if accepts_params:
                result = await handler(record.task_id, reporter, token, dict(params))
            else:
                result = await handler(record.task_id, reporter, token)
            # A handler that returns has crossed its commit point. Cancellation
            # arriving after an atomic side effect committed is "too late" and
            # must not hide the successful result from the caller.
            record.state = "succeeded"
            record.result = result
        except asyncio.CancelledError:
            record.state = "cancelled"
            raise
        except Exception as exc:
            if token.cancelled:
                record.state = "cancelled"
            else:
                record.state = "failed"
                record.error = str(exc) or exc.__class__.__name__
        finally:
            record.completed.set()
            await self._emit(record)
            await self._evict_if_needed()

    async def cancel(self, task_id: str) -> TaskStatus:
        """Request cooperative cancellation of ``task_id``."""
        record = self._tasks.get(task_id)
        token = self._tokens.get(task_id)
        if record is None:
            raise KeyError(f"unknown task {task_id!r}")
        if token is not None and record.state in ("queued", "running"):
            token.cancel()
        return self._snapshot(record)

    async def settle_export_grant(self, grant_id: str) -> None:
        """Join only exports admitted by this grant, including a lost create reply.

        Export cancellation is safe at await points: its atomic replace has no
        intervening await and the writer closes before this method returns.
        Import cancellation/commit semantics remain cooperative.
        """
        records = [
            record
            for record in self._tasks.values()
            if record.kind == "data.export" and record.export_grant_id == grant_id
        ]
        for record in records:
            task = self._async_tasks.get(record.task_id)
            if task is None or task.done():
                continue
            self._tokens[record.task_id].cancel()
            task.cancel()
            try:
                await task
            except asyncio.CancelledError:
                # A coroutine cancelled before its first instruction has no finally.
                if not record.completed.is_set():
                    record.state = "cancelled"
                    record.completed.set()
        for record in records:
            await record.completed.wait()

    def status(self, task_id: str) -> TaskStatus:
        """Return the current status snapshot of ``task_id``."""
        record = self._tasks.get(task_id)
        if record is None:
            raise KeyError(f"unknown task {task_id!r}")
        return self._snapshot(record)

    async def wait(self, task_id: str) -> TaskStatus:
        """Wait until ``task_id`` reaches its truthful terminal state."""

        record = self._tasks.get(task_id)
        if record is None:
            raise KeyError(f"unknown task {task_id!r}")
        await record.completed.wait()
        return self._snapshot(record)

    def abort_unresumable(self, kinds: set[str]) -> int:
        """Mark local-side-effect tasks ``aborted`` after a process restart.

        Called once during startup recovery. Returns the count of aborted
        tasks. Remote-mutation tasks (whose idempotency key verifies the
        result) are NOT aborted here — their result is resolved lazily.
        """
        count = 0
        for record in self._tasks.values():
            if record.state in ("queued", "running") and record.kind in kinds:
                record.state = "aborted"
                count += 1
        return count

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    def _snapshot(self, record: TaskRecord) -> TaskStatus:
        return TaskStatus(
            task_id=record.task_id,
            kind=record.kind,
            state=record.state,
            progress=record.progress,
            result=record.result,
            error=record.error,
        )

    async def _emit(self, record: TaskRecord) -> None:
        await self._sink(self._snapshot(record))

    async def _evict_if_needed(self) -> None:
        """Bound completed-task history (LRU by creation time)."""
        completed = sorted(
            (
                r
                for r in self._tasks.values()
                if r.state in ("succeeded", "failed", "cancelled", "aborted")
            ),
            key=lambda r: r.created_at,
        )
        if len(completed) <= MAX_COMPLETED_TASKS:
            return
        for record in completed[: len(completed) - MAX_COMPLETED_TASKS]:
            self._tasks.pop(record.task_id, None)
            self._tokens.pop(record.task_id, None)
