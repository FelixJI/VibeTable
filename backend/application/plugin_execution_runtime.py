"""Fail-closed execution runtime for local-worker plugin actions.

The WPF host owns the public plugin task lifecycle: it generates the task/run
identity, registers every task before execution starts and answers all public
queries. This runtime is the closed host-only executor: it receives the host
identity, keeps only the execution context, the cancel handles and the futures
waiting for host replies, and reports progress/terminal transitions back as
execution reports. It never owns or serves queryable lifecycle state.
"""

from __future__ import annotations

import asyncio
import time
import uuid
from collections.abc import Awaitable, Callable
from datetime import UTC, datetime
from typing import Any, Protocol

from backend.application.task_runtime import CancellationToken, ProgressReporter
from backend.contracts.plugin import (
    ActionAvailability,
    CommandContext,
    ConfirmationPreview,
    MutationPlan,
    PluginAction,
    PluginAuditEvent,
    PluginCommitUnknownError,
    PluginEventEnvelope,
    PluginExecutionError,
    PluginProgress,
    PluginResult,
    PluginRisk,
    PluginSafeError,
    PluginSnapshot,
    PluginTaskSnapshot,
)
from backend.contracts.task import TaskStatus

PluginNotificationSink = Callable[[PluginEventEnvelope], Awaitable[None]]


class RegistryPort(Protocol):
    async def get(self, project_key: str, plugin_id: str) -> PluginSnapshot | None: ...

    async def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent: ...


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
    async def apply(
        self, plan: MutationPlan, *, on_submit: Callable[[], None] | None = None
    ) -> dict[str, Any]: ...


class _ExecutionHandle:
    """Internal execution context for one host-owned task."""

    __slots__ = (
        "cancel",
        "cancel_timer",
        "plugin_id",
        "project_key",
        "run_id",
        "task",
        "task_id",
        "worker_running",
    )

    def __init__(
        self,
        *,
        task_id: str,
        run_id: str,
        plugin_id: str,
        project_key: str,
        task: asyncio.Task[None],
        cancel: CancellationToken,
    ) -> None:
        self.task_id = task_id
        self.run_id = run_id
        self.plugin_id = plugin_id
        self.project_key = project_key
        self.task = task
        self.cancel = cancel
        self.cancel_timer: asyncio.TimerHandle | None = None
        self.worker_running = True


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
        self._executions: dict[str, _ExecutionHandle] = {}
        self._notification_sink: PluginNotificationSink | None = None
        self._revision = 0

    def set_notification_sink(self, sink: PluginNotificationSink) -> None:
        self._notification_sink = sink

    async def describe(
        self,
        plugin_id: str,
        action_id: str,
        context: CommandContext,
    ) -> ActionAvailability:
        installation = await self._registry.get(context.project_key, plugin_id)
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
        *,
        task_id: str,
        run_id: str,
    ) -> PluginTaskSnapshot:
        """Starts one execution for a task identity owned by the WPF host.

        The returned snapshot is the execution's initial report for the host
        registry; it is never a queryable lifecycle source.
        """
        if not task_id or not run_id:
            raise ValueError("host execution identity is required")
        if task_id in self._executions:
            raise ValueError("plugin task identity is already executing")
        availability = await self.describe(plugin_id, action_id, context)
        if not availability.available:
            raise ValueError(",".join(availability.reasons))
        installation = await self._registry.get(context.project_key, plugin_id)
        if installation is None:
            raise ValueError("plugin_not_installed")
        action = _find_action(installation, action_id)
        if action is None:
            raise ValueError("plugin_action_not_found")
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
        cancel = CancellationToken()
        task = asyncio.create_task(
            self._run(
                snapshot,
                action,
                context,
                input_payload,
                package_hash=installation.package_hash,
                cancel=cancel,
            ),
            name=task_id,
        )
        handle = _ExecutionHandle(
            task_id=task_id,
            run_id=run_id,
            plugin_id=plugin_id,
            project_key=context.project_key,
            task=task,
            cancel=cancel,
        )
        self._executions[task_id] = handle
        task.add_done_callback(lambda _task: self._executions.pop(task_id, None))
        # Give the newly-created task one scheduler turn before returning the
        # RPC response. Without this explicit handoff, a fast sequence of
        # plugin.action.start / plugin.task.get requests can keep the task at
        # its initial queued snapshot long enough for the renderer to time
        # out even though a worker slot is available.
        await asyncio.sleep(0)
        return snapshot

    async def _run(
        self,
        initial: PluginTaskSnapshot,
        action: PluginAction,
        context: CommandContext,
        input_payload: dict[str, Any],
        *,
        package_hash: str,
        cancel: CancellationToken,
    ) -> None:
        started_at = datetime.now(UTC).replace(microsecond=0)
        started_monotonic = time.monotonic()
        running = initial.model_copy(update={"state": "running"})
        await self._emit(running)

        async def report(status: TaskStatus) -> None:
            nonlocal running
            running = running.model_copy(
                update={
                    "progress": PluginProgress(
                        current=status.progress.done,
                        total=status.progress.total,
                        message=status.progress.message,
                        cancellable=bool(execution.get("_hostCancellable", False)),
                    ),
                    "cancel_requested": cancel.cancelled,
                }
            )
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
            "_hostReporter": ProgressReporter(initial.task_id, "plugin", report),
            "_hostCancel": cancel,
        }
        try:
            if self._worker is None:
                raise ValueError("plugin worker is unavailable")
            handle = self._executions[initial.task_id]
            try:
                if cancel.cancelled:
                    raise asyncio.CancelledError
                try:
                    raw = await self._worker.run(
                        action.worker_entry,
                        context.model_dump(mode="json", by_alias=True),
                        input_payload,
                        execution=execution,
                    )
                except Exception:
                    if cancel.cancelled:
                        raise asyncio.CancelledError from None
                    raise
                if cancel.cancelled:
                    raise asyncio.CancelledError
            finally:
                handle.worker_running = False
                if handle.cancel_timer is not None:
                    handle.cancel_timer.cancel()
            result = await self._finalize_result(action, context, raw, execution)
            completed = running.model_copy(update={"state": "succeeded", "result": result})
        except asyncio.CancelledError:
            if execution.get("_commitStarted"):
                completed = running.model_copy(
                    update={
                        "state": "aborted",
                        "cancel_requested": True,
                        "error": PluginSafeError(
                            code="plugin_commit_unknown",
                            message="Cancellation after submission cannot establish rollback.",
                            recoverability="none",
                        ),
                    }
                )
            else:
                completed = running.model_copy(
                    update={"state": "cancelled", "cancel_requested": True}
                )
        except Exception as exc:
            completed = running.model_copy(
                update={
                    "state": "aborted" if isinstance(exc, PluginCommitUnknownError) else "failed",
                    "error": PluginSafeError(
                        code=getattr(exc, "code", "plugin_action_failed"),
                        message=str(exc) or exc.__class__.__name__,
                        recoverability=(
                            "none" if isinstance(exc, PluginCommitUnknownError) else "reconfigure"
                        ),
                        plugin_id=initial.plugin_id,
                        action_id=initial.action_id,
                        run_id=initial.run_id,
                    ),
                }
            )
        finished_at = datetime.now(UTC).replace(microsecond=0)
        await self._registry.record_audit(
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
            raise PluginExecutionError(
                "mutation plan was rejected", code="plugin_mutation_rejected"
            )
        if getattr(execution.get("_hostCancel"), "cancelled", False):
            raise asyncio.CancelledError

        def submitted() -> None:
            execution["_commitStarted"] = True

        return PluginResult.model_validate(await self._mutation.apply(plan, on_submit=submitted))

    async def request_cancel(self, task_id: str) -> bool:
        """Triggers the host-owned task's local cancel handle.

        Returns whether an active execution handle was cancelled. Public task
        state after cancellation is owned by the host registry.
        """
        handle = self._executions.get(task_id)
        if handle is None:
            return False
        if handle.cancel.cancelled:
            return True
        handle.cancel.cancel()
        if handle.worker_running:
            # Allow one progress receipt and cooperative cleanup before killing Node.
            handle.cancel_timer = asyncio.get_running_loop().call_later(1.0, handle.task.cancel)
        else:
            # Confirmation/submission keeps its existing cancellation boundary.
            handle.task.cancel()
        return True

    async def cancel_plugin_tasks(self, project_key: str, plugin_id: str) -> int:
        targets = [
            handle
            for handle in self._executions.values()
            if handle.project_key == project_key and handle.plugin_id == plugin_id
        ]
        for handle in targets:
            await self.request_cancel(handle.task_id)
        # Uninstall must not remove settings/packages while cooperative cleanup uses them.
        await asyncio.gather(*(handle.task for handle in targets))
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
