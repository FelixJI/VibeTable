"""Fail-closed execution runtime for local-worker plugin actions."""

from __future__ import annotations

import asyncio
import time
import uuid
from collections.abc import Awaitable, Callable
from datetime import UTC, datetime
from typing import Any, Protocol

from backend.contracts.plugin import (
    ActionAvailability,
    CommandContext,
    ConfirmationPreview,
    MutationPlan,
    PluginAction,
    PluginAuditEvent,
    PluginEventEnvelope,
    PluginResult,
    PluginRisk,
    PluginSafeError,
    PluginSnapshot,
    PluginTaskSnapshot,
)

PluginNotificationSink = Callable[[PluginEventEnvelope], Awaitable[None]]


class RegistryPort(Protocol):
    def get(self, project_key: str, plugin_id: str) -> PluginSnapshot | None: ...

    def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent: ...


class WorkerPort(Protocol):
    @property
    def available(self) -> bool: ...

    async def run(
        self,
        worker_entry: str,
        context: dict[str, Any],
        input_payload: dict[str, Any],
        *,
        execution: dict[str, Any] | None = None,
    ) -> dict[str, Any]: ...


class ConfirmationPort(Protocol):
    async def confirm(
        self,
        preview: ConfirmationPreview,
        risk: PluginRisk,
        *,
        execution: dict[str, Any] | None = None,
    ) -> bool: ...


class MutationPort(Protocol):
    async def apply(self, plan: MutationPlan) -> dict[str, Any]: ...


class PluginExecutionRuntime:
    def __init__(
        self,
        *,
        registry: RegistryPort,
        worker_adapter: WorkerPort | None = None,
        confirmation_adapter: ConfirmationPort | None = None,
        mutation_adapter: MutationPort | None = None,
    ) -> None:
        self._registry = registry
        self._worker = worker_adapter
        self._confirmation = confirmation_adapter
        self._mutation = mutation_adapter
        self._task_snapshots: dict[str, PluginTaskSnapshot] = {}
        self._async_tasks: dict[str, asyncio.Task[None]] = {}
        self._notification_sink: PluginNotificationSink | None = None
        self._revision = 0

    def set_notification_sink(self, sink: PluginNotificationSink) -> None:
        self._notification_sink = sink

    def describe(
        self,
        plugin_id: str,
        action_id: str,
        context: CommandContext,
    ) -> ActionAvailability:
        installation = self._registry.get(context.project_key, plugin_id)
        if installation is None:
            return ActionAvailability(available=False, reasons=["plugin_not_installed"])
        reasons = list(installation.blocking_reasons)
        if installation.status != "enabled":
            reasons.append("plugin_disabled")
        action = _find_action(installation, action_id)
        if action is None:
            reasons.append("plugin_action_not_found")
        else:
            reasons.extend(self._context_reasons(action, context))
        if self._worker is None or not self._worker.available:
            reasons.append("plugin_worker_unavailable")
        return ActionAvailability(available=not reasons, reasons=list(dict.fromkeys(reasons)))

    async def start(
        self,
        plugin_id: str,
        action_id: str,
        context: CommandContext,
        input_payload: dict[str, Any],
    ) -> PluginTaskSnapshot:
        availability = self.describe(plugin_id, action_id, context)
        if not availability.available:
            raise ValueError(",".join(availability.reasons))
        installation = self._registry.get(context.project_key, plugin_id)
        if installation is None:
            raise ValueError("plugin_not_installed")
        action = _find_action(installation, action_id)
        if action is None:
            raise ValueError("plugin_action_not_found")
        task_id = f"plugin-task-{uuid.uuid4().hex[:12]}"
        run_id = f"plugin-run-{uuid.uuid4().hex[:12]}"
        snapshot = PluginTaskSnapshot(
            task_id=task_id,
            run_id=run_id,
            plugin_id=plugin_id,
            plugin_version=installation.version,
            action_id=action_id,
            project_key=context.project_key,
            collection=context.collection,
            target_count=len(context.selected_keys),
            risk=action.risk,
            state="queued",
        )
        self._task_snapshots[task_id] = snapshot
        task = asyncio.create_task(
            self._run(
                snapshot,
                action,
                context,
                input_payload,
                package_hash=installation.package_hash,
            ),
            name=task_id,
        )
        self._async_tasks[task_id] = task
        task.add_done_callback(lambda _task: self._async_tasks.pop(task_id, None))
        # Give the newly-created task one scheduler turn before returning the
        # RPC response. Without this explicit handoff, a fast sequence of
        # plugin.action.start / plugin.task.get requests can keep the task at
        # its initial queued snapshot long enough for the renderer to time
        # out even though a worker slot is available.
        await asyncio.sleep(0)
        return self._task_snapshots[task_id]

    async def _run(
        self,
        initial: PluginTaskSnapshot,
        action: PluginAction,
        context: CommandContext,
        input_payload: dict[str, Any],
        *,
        package_hash: str,
    ) -> None:
        started_at = datetime.now(UTC).replace(microsecond=0)
        started_monotonic = time.monotonic()
        running = initial.model_copy(update={"state": "running"})
        self._task_snapshots[initial.task_id] = running
        await self._emit(running)
        execution = {
            "taskId": initial.task_id,
            "runId": initial.run_id,
            "pluginId": initial.plugin_id,
            "pluginVersion": initial.plugin_version,
            "packageHash": package_hash,
            "actionId": initial.action_id,
            "projectKey": context.project_key,
            "context": context.model_dump(mode="json", by_alias=True),
        }
        try:
            if self._worker is None:
                raise ValueError("plugin worker is unavailable")
            raw = await self._worker.run(
                action.worker_entry,
                context.model_dump(mode="json", by_alias=True),
                input_payload,
                execution=execution,
            )
            result = await self._finalize_result(action, context, raw, execution)
            completed = running.model_copy(update={"state": "succeeded", "result": result})
        except asyncio.CancelledError:
            completed = running.model_copy(update={"state": "cancelled", "cancel_requested": True})
        except Exception as exc:
            completed = running.model_copy(
                update={
                    "state": "failed",
                    "error": PluginSafeError(
                        code="plugin_action_failed",
                        message=str(exc) or exc.__class__.__name__,
                        recoverability="reconfigure",
                        plugin_id=initial.plugin_id,
                        action_id=initial.action_id,
                        run_id=initial.run_id,
                    ),
                }
            )
        self._task_snapshots[initial.task_id] = completed
        finished_at = datetime.now(UTC).replace(microsecond=0)
        self._registry.record_audit(
            PluginAuditEvent(
                event_id=str(uuid.uuid4()),
                project_key=initial.project_key,
                plugin_id=initial.plugin_id,
                plugin_version=initial.plugin_version,
                package_hash=package_hash,
                event_type="action",
                outcome=completed.state,
                action_id=initial.action_id,
                run_id=initial.run_id,
                actor=_actor_id(context),
                risk=initial.risk,
                target_collection=initial.collection,
                target_count=initial.target_count,
                started_at=started_at,
                finished_at=finished_at,
                duration_ms=max(0, round((time.monotonic() - started_monotonic) * 1000)),
                error_code=completed.error.code if completed.error is not None else None,
                details={
                    "taskId": initial.task_id,
                    "state": completed.state,
                    "resultStatus": (
                        completed.result.status if completed.result is not None else None
                    ),
                },
            )
        )
        await self._emit(completed)

    async def _finalize_result(
        self,
        action: PluginAction,
        context: CommandContext,
        raw: dict[str, Any],
        execution: dict[str, Any],
    ) -> PluginResult:
        if action.risk == "read":
            return PluginResult.model_validate(raw)
        if raw.get("contract") != "vibetable.mutation-plan.v1":
            raise ValueError("write plugin must return a mutation plan")
        plan = MutationPlan.model_validate(raw)
        if context.collection is not None and plan.collection != context.collection:
            raise ValueError("mutation plan collection is outside the action context")
        if self._confirmation is None or self._mutation is None:
            raise ValueError("mutation confirmation capability is unavailable")
        approved = await self._confirmation.confirm(
            plan.preview,
            action.risk,
            execution=execution,
        )
        if not approved:
            raise ValueError("mutation plan was rejected")
        return PluginResult.model_validate(await self._mutation.apply(plan))

    def get_task(self, task_id: str) -> PluginTaskSnapshot:
        try:
            return self._task_snapshots[task_id]
        except KeyError as exc:
            raise KeyError(f"unknown plugin task {task_id!r}") from exc

    async def request_cancel(self, task_id: str) -> PluginTaskSnapshot:
        current = self.get_task(task_id)
        task = self._async_tasks.get(task_id)
        if task is not None:
            task.cancel()
        updated = current.model_copy(update={"cancel_requested": True})
        self._task_snapshots[task_id] = updated
        return updated

    async def cancel_plugin_tasks(self, project_key: str, plugin_id: str) -> int:
        targets = [
            task_id
            for task_id, snapshot in self._task_snapshots.items()
            if snapshot.project_key == project_key
            and snapshot.plugin_id == plugin_id
            and snapshot.state in {"queued", "running"}
        ]
        for task_id in targets:
            await self.request_cancel(task_id)
        return len(targets)

    async def _emit(self, snapshot: PluginTaskSnapshot) -> None:
        if self._notification_sink is None:
            return
        self._revision += 1
        await self._notification_sink(
            PluginEventEnvelope(
                event_type="plugin.task.changed",
                project_key=snapshot.project_key,
                entity_id=snapshot.task_id,
                revision=self._revision,
                snapshot=snapshot.model_dump(mode="json", by_alias=True),
            )
        )

    @staticmethod
    def _context_reasons(
        action: PluginAction,
        context: CommandContext,
    ) -> list[str]:
        reasons: list[str] = []
        selection = action.requires.get("selection")
        if selection == "one-or-more" and not context.selected_keys:
            reasons.append("selection_required")
        if selection == "exactly-one" and len(context.selected_keys) != 1:
            reasons.append("single_selection_required")
        return reasons


def _find_action(snapshot: PluginSnapshot, action_id: str) -> PluginAction | None:
    return next(
        (action for action in snapshot.manifest.actions if action.action_id == action_id),
        None,
    )


def _actor_id(context: CommandContext) -> str:
    candidate = context.user.get("id")
    return candidate if isinstance(candidate, str) and candidate else "local-user"


__all__ = ["PluginExecutionRuntime", "PluginNotificationSink"]
