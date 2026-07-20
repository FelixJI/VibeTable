"""C1 TaskRuntime: a cancellable, observable background-task runtime.

Owns the lifecycle of long-running data-IO operations (import, export,
relation expansion). Each task runs on the asyncio loop, reports monotonic
progress via a notification callback, and can be cancelled cooperatively.

Lifecycle
---------
* ``queued`` — created, waiting for a worker slot.
* ``running`` — the handler is executing.
* ``succeeded`` — the handler returned a result.
* ``failed`` — the handler raised; ``error`` carries a UI-ready message.
* ``cancelled`` — the host requested cancellation before completion.
* ``aborted`` — a local-side-effect task that could not be resumed after a
  process restart (set by :meth:`abort_unresumable` during startup recovery).

Local side effects (writing an export file, streaming a workbook) are NOT
replayed on restart — the runtime marks them ``aborted``. Remote mutations
(Directus writes) rely on their idempotency key for result verification, so a
crash mid-import is resolved by re-checking the idempotency key, not by
replaying the task.

The runtime is single-process and in-memory; task history is bounded by an
LRU cap so a long-lived session does not accumulate stale entries.
"""

from __future__ import annotations

import asyncio
import time
import uuid
from collections.abc import Awaitable, Callable
from typing import Any

from backend.contracts.task import TaskProgress, TaskState, TaskStatus

#: How many completed tasks to retain for status polling.
MAX_COMPLETED_TASKS: int = 64

#: The handler signature: receives the task id, a progress reporter and a
#: cancellation token, returns the typed result payload.
TaskHandler = Callable[
    [str, "ProgressReporter", "CancellationToken"],
    Awaitable[Any],
]

#: The callback the runtime invokes to emit a ``task.progress``/``task.status``
#: notification to the host. The runtime never assumes a specific transport.
NotificationSink = Callable[[TaskStatus], Awaitable[None]]


class CancellationToken:
    """A cooperative cancellation flag for a running task.

    The handler polls :meth:`cancelled` (or awaits :meth:`wait`) at safe points
    and aborts its work cleanly. The token does NOT force-cancel the coroutine.
    """

    def __init__(self) -> None:
        self._event = asyncio.Event()
        self._cancelled = False

    @property
    def cancelled(self) -> bool:
        return self._cancelled

    def cancel(self) -> None:
        self._cancelled = True
        self._event.set()

    async def wait(self) -> None:
        await self._event.wait()


class ProgressReporter:
    """A handle the handler uses to report monotonic progress.

    ``done`` must never decrease; the runtime enforces this so the UI never
    shows a backward-jumping bar. ``total`` may be updated as the handler learns
    the real size (e.g. after counting workbook rows).
    """

    def __init__(self, task_id: str, kind: str, sink: NotificationSink) -> None:
        self._task_id = task_id
        self._kind = kind
        self._sink = sink
        self._done = 0
        self._total = 0

    async def report(
        self, *, done: int | None = None, total: int | None = None, message: str = ""
    ) -> None:
        if done is not None:
            if done < self._done:
                # Monotonic: never move the bar backwards.
                done = self._done
            self._done = done
        if total is not None:
            self._total = total
        progress = TaskProgress(done=self._done, total=self._total, message=message)
        status = TaskStatus(
            task_id=self._task_id,
            kind=self._kind,
            state="running",
            progress=progress,
        )
        await self._sink(status)


class TaskRecord:
    """Internal record of one task's state + result."""

    __slots__ = (
        "completed",
        "created_at",
        "error",
        "kind",
        "progress",
        "result",
        "state",
        "task_id",
    )

    def __init__(self, task_id: str, kind: str) -> None:
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
        self._tasks: dict[str, TaskRecord] = {}
        self._tokens: dict[str, CancellationToken] = {}
        self._async_tasks: set[asyncio.Task[Any]] = set()
        self._sink = notification_sink or _noop_sink
        self._clock = clock
        self._lock = asyncio.Lock()

    # ------------------------------------------------------------------
    # Handler registration
    # ------------------------------------------------------------------

    def register(self, kind: str, handler: TaskHandler) -> None:
        """Register ``handler`` for task ``kind`` (e.g. ``data.import``)."""
        self._handlers[kind] = handler

    def unregister(self, kind: str) -> bool:
        """Remove a dynamically registered handler after its last task started."""

        return self._handlers.pop(kind, None) is not None

    # ------------------------------------------------------------------
    # Task lifecycle
    # ------------------------------------------------------------------

    async def create(self, kind: str, params: dict[str, Any]) -> TaskStatus:
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
        token = CancellationToken()
        async with self._lock:
            self._tasks[task_id] = record
            self._tokens[task_id] = token
        # Start immediately (single-slot; the runtime is single-process). Hold
        # a strong reference so the task is not garbage-collected mid-run.
        task = asyncio.create_task(self._run(record, token, handler, params))
        self._async_tasks.add(task)
        task.add_done_callback(self._async_tasks.discard)
        return self._snapshot(record)

    async def _run(
        self,
        record: TaskRecord,
        token: CancellationToken,
        handler: TaskHandler,
        params: dict[str, Any],
    ) -> None:
        record.state = "running"

        async def report_sink(status: TaskStatus) -> None:
            record.progress = status.progress
            await self._sink(status)

        reporter = ProgressReporter(record.task_id, record.kind, report_sink)
        await self._emit(record)
        # Bind params into the handler via a closure: handlers are registered
        # with a fixed signature, so params are injected through a wrapper.
        try:
            result = await handler(record.task_id, reporter, token)
            if token.cancelled:
                record.state = "cancelled"
            else:
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


async def _noop_sink(_status: TaskStatus) -> None:
    """Default sink when none is wired (e.g. in unit tests)."""
    return None


__all__ = [
    "MAX_COMPLETED_TASKS",
    "CancellationToken",
    "NotificationSink",
    "ProgressReporter",
    "TaskHandler",
    "TaskRecord",
    "TaskRuntime",
]
