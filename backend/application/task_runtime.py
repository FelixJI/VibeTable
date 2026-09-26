"""Data IO execution handles; public task identity and history belong to Host."""

from __future__ import annotations

import asyncio
import copy
from collections.abc import Awaitable, Callable
from typing import Any

from backend.contracts.task import TaskProgress, TaskState, TaskStatus

#: The handler signature: receives the task id, a progress reporter and a
#: cancellation token, returns the typed result payload.
TaskHandler = Callable[..., Awaitable[Any]]

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


class HostExecutionRuntime:
    """Executor handles only; public snapshots and history live in the Host."""

    def __init__(self, *, notification_sink: NotificationSink | None = None) -> None:
        self._handlers: dict[str, TaskHandler] = {}
        self._sink = notification_sink or _noop_sink
        self._handles: dict[str, tuple[CancellationToken, asyncio.Task[None], str | None]] = {}

    def register(self, kind: str, handler: TaskHandler) -> None:
        self._handlers[kind] = handler

    async def start(
        self,
        task_id: str,
        kind: str,
        params: dict[str, Any],
        *,
        export_grant_id: str | None = None,
    ) -> bool:
        if kind not in ("data.import", "data.export") or kind not in self._handlers:
            raise ValueError(f"unknown task kind {kind!r}")
        if task_id in self._handles:
            raise ValueError("task execution identity is already active")
        token = CancellationToken()
        handler = self._handlers[kind]
        copied = copy.deepcopy(params)

        async def execute() -> None:
            progress = TaskProgress()

            async def emit(status: TaskStatus) -> None:
                nonlocal progress
                progress = status.progress
                await self._sink(status)

            reporter = ProgressReporter(task_id, kind, emit)
            try:
                await emit(TaskStatus(task_id=task_id, kind=kind, state="running"))
                result = await handler(task_id, reporter, token, copied)
                final = TaskStatus(
                    task_id=task_id, kind=kind, state="succeeded", progress=progress, result=result
                )
            except asyncio.CancelledError:
                final = TaskStatus(task_id=task_id, kind=kind, state="cancelled", progress=progress)
            except Exception as exc:
                outcome_unknown = getattr(exc, "code", None) == "import_outcome_unknown"
                state: TaskState = (
                    "cancelled" if token.cancelled and not outcome_unknown else "failed"
                )
                final = TaskStatus(
                    task_id=task_id,
                    kind=kind,
                    state=state,
                    progress=progress,
                    error=(None if state == "cancelled" else str(exc) or exc.__class__.__name__),
                )
            try:
                await self._sink(final)
            finally:
                self._handles.pop(task_id, None)

        worker = asyncio.create_task(execute())
        self._handles[task_id] = (token, worker, export_grant_id)
        return True

    async def cancel(self, task_id: str) -> bool:
        handle = self._handles.get(task_id)
        if handle is None:
            return False
        handle[0].cancel()
        return True

    async def settle_export_grant(self, grant_id: str) -> None:
        matching = [
            (task_id, handle) for task_id, handle in self._handles.items() if handle[2] == grant_id
        ]
        for task_id, (token, worker, _) in matching:
            token.cancel()
            worker.cancel()
            await asyncio.gather(worker, return_exceptions=True)
            # A coroutine cancelled before its first instruction has no finally.
            if self._handles.pop(task_id, None) is not None:
                await self._sink(TaskStatus(task_id=task_id, kind="data.export", state="cancelled"))


async def _noop_sink(_status: TaskStatus) -> None:
    """Default sink when none is wired (e.g. in unit tests)."""
    return None


__all__ = [
    "CancellationToken",
    "HostExecutionRuntime",
    "NotificationSink",
    "ProgressReporter",
    "TaskHandler",
]
