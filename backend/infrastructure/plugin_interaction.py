"""In-memory interaction adapter with fail-closed non-recoverable semantics."""

from __future__ import annotations

import asyncio
import time
import uuid
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from typing import Any

from backend.contracts.plugin import (
    CancelFlag,
    ConfirmationPreview,
    InteractionDecision,
    InteractionResolveResult,
    InteractionSnapshot,
    PendingConfirmation,
    PluginEventEnvelope,
    PluginProgress,
    PluginRisk,
)


@dataclass
class _RunState:
    run_id: str
    project_key: str
    plugin_id: str
    action_id: str
    caller: str
    pending: PendingConfirmation | None = None
    cancel_requested: bool = False
    progress: PluginProgress | None = None
    waiter: asyncio.Future[bool] | None = None
    resolved: dict[str, InteractionDecision] = field(default_factory=dict)


class InMemoryInteractionAdapter:
    """Controllable boundary fake for confirmation, timeout and cancellation tests."""

    def __init__(self, *, clock: Callable[[], float] = time.monotonic) -> None:
        self._clock = clock
        self._runs: dict[str, _RunState] = {}
        self._changed = asyncio.Condition()
        self._counter = 0

    def open_run(
        self,
        *,
        run_id: str,
        project_key: str,
        plugin_id: str,
        action_id: str,
        caller: str,
    ) -> None:
        self._runs[run_id] = _RunState(
            run_id=run_id,
            project_key=project_key,
            plugin_id=plugin_id,
            action_id=action_id,
            caller=caller,
        )

    def has_run(self, run_id: str) -> bool:
        return run_id in self._runs

    def close_run(self, run_id: str) -> None:
        state = self._runs.pop(run_id, None)
        if state is not None and state.waiter is not None and not state.waiter.done():
            state.waiter.set_result(False)

    async def request_confirmation(
        self,
        *,
        run_id: str,
        risk: PluginRisk,
        title: str,
        preview: ConfirmationPreview,
        timeout_ms: int,
    ) -> bool:
        state = self._runs.get(run_id)
        if state is None:
            raise RuntimeError("confirmation requires an active VibeTable run")
        if state.pending is not None:
            raise RuntimeError("a confirmation is already pending for this run")
        self._counter += 1
        interaction_id = f"interaction-{self._counter}"
        state.pending = PendingConfirmation(
            interaction_id=interaction_id,
            risk=risk,
            title=title,
            preview=preview,
            expires_at=self._clock() + timeout_ms / 1000,
        )
        state.waiter = asyncio.get_running_loop().create_future()
        async with self._changed:
            self._changed.notify_all()
        try:
            return await asyncio.wait_for(state.waiter, timeout=timeout_ms / 1000)
        finally:
            state.pending = None
            state.waiter = None

    async def watch(self, run_id: str) -> InteractionSnapshot:
        state = self._require_run(run_id)
        if state.pending is None:
            async with self._changed:
                await self._changed.wait_for(
                    lambda: self._runs.get(run_id) is None or self._runs[run_id].pending is not None
                )
            state = self._require_run(run_id)
        return self._snapshot(state)

    async def resolve(
        self, run_id: str, interaction_id: str, decision: InteractionDecision
    ) -> InteractionResolveResult:
        state = self._require_run(run_id)
        previous = state.resolved.get(interaction_id)
        if previous is not None:
            return InteractionResolveResult(status="already-resolved", decision=previous)
        pending = state.pending
        if pending is None or pending.interaction_id != interaction_id:
            return InteractionResolveResult(status="expired")
        state.resolved[interaction_id] = decision
        if state.waiter is not None and not state.waiter.done():
            state.waiter.set_result(decision == "approved")
        return InteractionResolveResult(status="resolved", decision=decision)

    async def report_progress(
        self,
        run_id: str,
        *,
        current: int,
        total: int,
        message: str,
        cancellable: bool,
    ) -> CancelFlag:
        state = self._require_run(run_id)
        if current < 0 or total < 0 or (total > 0 and current > total):
            raise ValueError("progress is out of bounds")
        if state.progress is not None and current < state.progress.current:
            raise ValueError("progress must be monotonic")
        state.progress = PluginProgress(
            current=current,
            total=total,
            message=message,
            cancellable=cancellable,
        )
        return CancelFlag(cancel_requested=state.cancel_requested)

    async def request_cancel(self, run_id: str) -> CancelFlag:
        state = self._require_run(run_id)
        state.cancel_requested = True
        return CancelFlag(cancel_requested=True)

    async def get(self, run_id: str) -> InteractionSnapshot:
        return self._snapshot(self._require_run(run_id))

    def _require_run(self, run_id: str) -> _RunState:
        state = self._runs.get(run_id)
        if state is None:
            raise KeyError(f"unknown interaction run {run_id!r}")
        return state

    @staticmethod
    def _snapshot(state: _RunState) -> InteractionSnapshot:
        return InteractionSnapshot(
            run_id=state.run_id,
            project_key=state.project_key,
            plugin_id=state.plugin_id,
            action_id=state.action_id,
            caller=state.caller,
            progress=state.progress,
            pending_confirmation=state.pending,
            cancel_requested=state.cancel_requested,
        )


PluginInteractionNotificationSink = Callable[[PluginEventEnvelope], Awaitable[None]]


@dataclass
class _HostWaiter:
    interaction_id: str
    expires_at: float
    future: asyncio.Future[bool]


class HostConfirmationAdapter:
    """Execution-side future only; the Host owns public pending and decisions."""

    def __init__(
        self,
        *,
        timeout_seconds: float = 300.0,
        clock: Callable[[], float] = time.time,
    ) -> None:
        if timeout_seconds <= 0 or timeout_seconds > 900:
            raise ValueError("confirmation timeout must be between 0 and 900 seconds")
        self._timeout_seconds = timeout_seconds
        self._clock = clock
        self._sink: PluginInteractionNotificationSink | None = None
        self._waiters: dict[str, _HostWaiter] = {}
        self._revisions: dict[str, int] = {}

    @property
    def available(self) -> bool:
        return self._sink is not None

    def set_notification_sink(self, sink: PluginInteractionNotificationSink) -> None:
        self._sink = sink

    async def confirm(
        self,
        preview: ConfirmationPreview,
        risk: PluginRisk,
        *,
        execution: dict[str, Any] | None = None,
    ) -> bool:
        if self._sink is None:
            raise RuntimeError("host confirmation channel is unavailable")
        details = execution if isinstance(execution, dict) else {}
        identity = {
            key: details.get(key) for key in ("runId", "projectKey", "pluginId", "actionId")
        }
        if not all(isinstance(value, str) and value for value in identity.values()):
            raise RuntimeError("host confirmation execution identity is invalid")
        run_id = str(identity["runId"])
        if run_id in self._waiters:
            raise RuntimeError("a confirmation is already pending for this run")
        expires_at = self._clock() + self._timeout_seconds
        pending = PendingConfirmation(
            interaction_id=f"interaction-{uuid.uuid4().hex}",
            risk=risk,
            title=(
                f"确认影响 {preview.affected_count} 条记录"
                if risk == "write"
                else f"确认危险操作（{preview.affected_count} 条记录）"
            ),
            preview=preview,
            expires_at=expires_at,
        )
        future: asyncio.Future[bool] = asyncio.get_running_loop().create_future()
        self._waiters[run_id] = _HostWaiter(pending.interaction_id, expires_at, future)
        snapshot = InteractionSnapshot(
            run_id=run_id,
            project_key=str(identity["projectKey"]),
            plugin_id=str(identity["pluginId"]),
            action_id=str(identity["actionId"]),
            caller="desktop-host",
            pending_confirmation=pending,
        )
        try:
            revision = self._revisions.get(run_id, 1) + 1
            self._revisions[run_id] = revision
            await self._sink(
                PluginEventEnvelope(
                    event_type="plugin.interaction.requested",
                    project_key=snapshot.project_key,
                    entity_id=run_id,
                    revision=revision,
                    snapshot=snapshot.model_dump(mode="json", by_alias=True),
                )
            )
            return await asyncio.wait_for(future, timeout=self._timeout_seconds)
        finally:
            self._waiters.pop(run_id, None)
            self._revisions.pop(run_id, None)

    async def try_resolve(
        self,
        run_id: str,
        interaction_id: str,
        decision: InteractionDecision,
    ) -> InteractionResolveResult | None:
        waiter = self._waiters.get(run_id)
        if waiter is None:
            return None
        if (
            waiter.interaction_id != interaction_id
            or waiter.expires_at <= self._clock()
            or waiter.future.done()
        ):
            return InteractionResolveResult(status="expired")
        waiter.future.set_result(decision == "approved")
        return InteractionResolveResult(status="resolved", decision=decision)

    async def request_cancel(self, run_id: str) -> CancelFlag | None:
        waiter = self._waiters.get(run_id)
        if waiter is None:
            return None
        if not waiter.future.done():
            waiter.future.set_result(False)
        return CancelFlag(cancel_requested=True)


__all__ = ["HostConfirmationAdapter", "InMemoryInteractionAdapter"]
