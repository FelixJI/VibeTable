"""Single action execution interface for flow, local and hybrid plugins."""

from __future__ import annotations

import asyncio
import contextlib
import uuid
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any, cast

from backend.application.flow_binding_manager import FlowBindingManager
from backend.application.plugin_registry import PluginRegistry
from backend.application.task_runtime import TaskRuntime
from backend.contracts.plugin import (
    ActionAvailability,
    CommandContext,
    ConfirmationPreview,
    InteractionSnapshot,
    MutationPlan,
    PluginAction,
    PluginEventEnvelope,
    PluginProgress,
    PluginResult,
    PluginRisk,
    PluginSafeError,
    PluginTaskSnapshot,
)
from backend.infrastructure.plugin_schema import PluginSchemaError, validate_plugin_json


@dataclass(frozen=True)
class _PluginRun:
    run_id: str
    plugin_id: str
    plugin_version: str
    action_id: str
    project_key: str
    collection: str | None
    target_count: int
    risk: PluginRisk
    mode: str


PluginNotificationSink = Callable[[PluginEventEnvelope], Awaitable[None]]


class PluginExecutionRuntime:
    """Centralizes availability and execution policy instead of duplicating it in UI."""

    def __init__(
        self,
        *,
        registry: PluginRegistry,
        bindings: FlowBindingManager,
        tasks: TaskRuntime,
        flow_adapter: Any,
        worker_adapter: Any | None = None,
        confirmation_adapter: Any | None = None,
        bulk_mutation_adapter: Any | None = None,
        interaction_adapter: Any | None = None,
        notification_sink: PluginNotificationSink | None = None,
    ) -> None:
        self._registry = registry
        self._bindings = bindings
        self._tasks = tasks
        self._flow_adapter = flow_adapter
        self._worker_adapter = worker_adapter
        self._confirmation_adapter = confirmation_adapter
        self._bulk_mutation_adapter = bulk_mutation_adapter
        self._interaction_adapter = interaction_adapter
        self._notification_sink = notification_sink
        self._runs: dict[str, _PluginRun] = {}
        self._cancel_requested: set[str] = set()
        self._non_cancellable_tasks: set[str] = set()
        self._worker_active_tasks: set[str] = set()
        self._directus_active_tasks: set[str] = set()

    def set_notification_sink(self, sink: PluginNotificationSink) -> None:
        self._notification_sink = sink

    def describe(
        self, plugin_id: str, action_id: str, context: CommandContext
    ) -> ActionAvailability:
        installation = self._registry.get(context.project_key, plugin_id)
        if installation is None:
            return ActionAvailability(available=False, reasons=["plugin_not_installed"])
        if installation.status != "enabled":
            return ActionAvailability(
                available=False,
                reasons=installation.blocking_reasons
                or [installation.disabled_reason or "plugin_disabled"],
            )
        action = next(
            (item for item in installation.manifest.actions if item.action_id == action_id),
            None,
        )
        if action is None:
            return ActionAvailability(available=False, reasons=["action_not_found"])
        reasons = self._context_reasons(action, context)
        if action.mode in ("local", "hybrid"):
            worker_available = self._worker_adapter is not None and bool(
                getattr(self._worker_adapter, "available", True)
            )
            if not worker_available:
                reasons.append("local_worker_unavailable")
            if action.risk in ("write", "destructive") and (
                self._confirmation_adapter is None
                or not bool(getattr(self._confirmation_adapter, "available", True))
                or self._bulk_mutation_adapter is None
            ):
                reasons.append("host_confirmation_unavailable")
        if (
            action.mode in ("flow", "hybrid")
            and action.risk in ("write", "destructive")
            and self._interaction_adapter is not None
            and bool(getattr(self._interaction_adapter, "requires_host_notifications", False))
            and self._notification_sink is None
        ):
            reasons.append("interaction_host_unavailable")
        if action.entry_flow:
            binding = self._bindings.resolve(context.project_key, plugin_id, action.entry_flow)
            if binding is None:
                reasons.append(f"flow_unbound:{action.entry_flow}")
            elif binding.health not in ("healthy", "drifted"):
                reasons.append(f"flow_invalid:{action.entry_flow}")
        return ActionAvailability(available=not reasons, reasons=reasons)

    async def start(
        self,
        plugin_id: str,
        action_id: str,
        context: CommandContext,
        input_payload: dict[str, Any],
    ) -> PluginTaskSnapshot:
        availability = self.describe(plugin_id, action_id, context)
        if not availability.available:
            raise ValueError(f"plugin action is unavailable: {', '.join(availability.reasons)}")
        installation = self._registry.get(context.project_key, plugin_id)
        if installation is None:  # Defensive after describe.
            raise ValueError("plugin is not installed")
        action = next(item for item in installation.manifest.actions if item.action_id == action_id)
        if action.input_schema:
            schema = installation.schemas.get(action.input_schema)
            if schema is None:
                raise ValueError("plugin action input schema is unavailable")
            try:
                validate_plugin_json(input_payload, schema, label="plugin action input schema")
            except PluginSchemaError as exc:
                raise ValueError(str(exc)) from exc
        run_id = f"run-{uuid.uuid4().hex}"
        task_kind = f"plugin.action.{run_id}"

        async def run_action(_task_id: str, reporter: Any, cancel: Any) -> dict[str, Any]:
            await reporter.report(done=0, total=0, message="正在启动插件动作")
            _raise_if_cancelled(cancel)
            context_payload = context.model_dump(mode="json", by_alias=True)
            worker_execution = {
                "runId": run_id,
                "projectKey": context.project_key,
                "pluginId": plugin_id,
                "pluginVersion": installation.version,
                "packageHash": installation.package_hash,
                "actionId": action_id,
                "context": context_payload,
                "_hostReporter": reporter,
                "_hostCancel": cancel,
            }
            prepared = input_payload
            if action.mode == "hybrid":
                if self._worker_adapter is None or not action.worker_entry:
                    raise ValueError("hybrid action has no Worker adapter")
                self._worker_active_tasks.add(_task_id)
                try:
                    prepared = await self._worker_adapter.prepare(
                        action.worker_entry,
                        context_payload,
                        input_payload,
                        execution=worker_execution,
                    )
                finally:
                    self._worker_active_tasks.discard(_task_id)
                _raise_if_cancelled(cancel)
            if action.mode in ("flow", "hybrid"):
                if not action.entry_flow:
                    raise ValueError("flow action has no entry Flow")
                binding = self._bindings.resolve(context.project_key, plugin_id, action.entry_flow)
                if binding is None:
                    raise ValueError(f"Flow {action.entry_flow!r} is not bound")
                requirement = next(
                    item
                    for item in installation.flow_requirements
                    if item.logical_flow_id == action.entry_flow
                )
                if requirement.input_schema:
                    validate_plugin_json(
                        prepared,
                        requirement.input_schema,
                        label="plugin Flow input schema",
                    )
                body = {
                    "collection": context.collection or "",
                    "keys": list(context.selected_keys),
                    "payload": {
                        "contract": "vibetable.plugin-action-input.v1",
                        "runId": run_id,
                        "pluginId": plugin_id,
                        "actionId": action_id,
                        "input": prepared,
                        "context": context_payload,
                    },
                }
                if self._interaction_adapter is not None:
                    await self._interaction_adapter.register_run(
                        run_id=run_id,
                        plugin_id=plugin_id,
                        action_id=action_id,
                    )
                terminal_hint = "failed"
                watcher: asyncio.Task[None] | None = None
                self._directus_active_tasks.add(_task_id)
                try:
                    _raise_if_cancelled(cancel)
                    if (
                        self._interaction_adapter is not None
                        and self._notification_sink is not None
                    ):
                        watcher = asyncio.create_task(self._watch_interactions(run_id))
                    trigger = asyncio.create_task(
                        self._flow_adapter.trigger_manual(binding.directus_flow_uuid, body)
                    )
                    if watcher is None:
                        raw = await trigger
                    else:
                        done, _ = await asyncio.wait(
                            {trigger, watcher}, return_when=asyncio.FIRST_COMPLETED
                        )
                        if trigger in done:
                            raw = await trigger
                        else:
                            trigger.cancel()
                            with contextlib.suppress(asyncio.CancelledError):
                                await trigger
                            await watcher
                            raise RuntimeError("plugin interaction watch ended unexpectedly")
                    terminal_hint = "succeeded"
                finally:
                    self._directus_active_tasks.discard(_task_id)
                    if watcher is not None:
                        watcher.cancel()
                        with contextlib.suppress(asyncio.CancelledError):
                            await watcher
                    if self._interaction_adapter is not None:
                        await self._interaction_adapter.complete_run(run_id, terminal_hint)
                if requirement.output_schema:
                    validate_plugin_json(
                        _schema_payload(raw),
                        requirement.output_schema,
                        label="plugin Flow output schema",
                    )
                if action.mode == "hybrid":
                    assert self._worker_adapter is not None
                    self._worker_active_tasks.add(_task_id)
                    try:
                        raw = await self._worker_adapter.present(
                            action.worker_entry,
                            raw,
                            execution=worker_execution,
                        )
                    finally:
                        self._worker_active_tasks.discard(_task_id)
                    _raise_if_cancelled(cancel)
            elif action.mode == "local":
                if self._worker_adapter is None or not action.worker_entry:
                    raise ValueError("local action has no Worker adapter")
                self._worker_active_tasks.add(_task_id)
                try:
                    raw = await self._worker_adapter.run(
                        action.worker_entry,
                        context_payload,
                        input_payload,
                        execution=worker_execution,
                    )
                finally:
                    self._worker_active_tasks.discard(_task_id)
                _raise_if_cancelled(cancel)
            else:
                raise ValueError(f"execution mode {action.mode!r} is not available")
            if isinstance(raw, dict) and raw.get("contract") == "vibetable.mutation-plan.v1":
                _raise_if_cancelled(cancel)
                if action.risk not in ("write", "destructive"):
                    raise ValueError("read action returned a mutation plan")
                if self._confirmation_adapter is None or self._bulk_mutation_adapter is None:
                    raise ValueError("write action has no confirmation/bulk mutation adapters")
                mutation_plan = MutationPlan.model_validate(raw)
                approved = await self._confirmation_adapter.confirm(
                    ConfirmationPreview.model_validate(mutation_plan.preview),
                    action.risk,
                    execution=worker_execution,
                )
                if not approved:
                    raise ValueError("plugin confirmation was rejected")
                _raise_if_cancelled(cancel)
                # Once the mutation request starts it cannot be cancelled
                # truthfully. Late requests remain visible, while the final
                # state follows the actual Directus result.
                self._non_cancellable_tasks.add(_task_id)
                raw = await self._bulk_mutation_adapter.apply(mutation_plan)
            result = PluginResult.model_validate(raw)
            if action.output_schema:
                schema = installation.schemas.get(action.output_schema)
                if schema is None:
                    raise ValueError("plugin action output schema is unavailable")
                validate_plugin_json(
                    _schema_payload(result.model_dump(mode="json", by_alias=True)),
                    schema,
                    label="plugin action output schema",
                )
            return result.model_dump(mode="json", by_alias=True)

        async def execute(task_id: str, reporter: Any, cancel: Any) -> dict[str, Any]:
            try:
                return await run_action(task_id, reporter, cancel)
            finally:
                self._non_cancellable_tasks.discard(task_id)
                self._tasks.unregister(task_kind)

        self._tasks.register(task_kind, execute)
        status = await self._tasks.create(task_kind, {})
        self._runs[status.task_id] = _PluginRun(
            run_id=run_id,
            plugin_id=plugin_id,
            plugin_version=installation.version,
            action_id=action_id,
            project_key=context.project_key,
            collection=context.collection,
            target_count=len(context.selected_keys),
            risk=action.risk,
            mode=action.mode,
        )
        return self.get_task(status.task_id)

    def get_task(self, task_id: str) -> PluginTaskSnapshot:
        run = self._runs.get(task_id)
        if run is None:
            raise KeyError(f"unknown plugin task {task_id!r}")
        status = self._tasks.status(task_id)
        result = None
        if status.state == "succeeded" and isinstance(status.result, dict):
            result = PluginResult.model_validate(status.result)
        return PluginTaskSnapshot(
            task_id=task_id,
            run_id=run.run_id,
            plugin_id=run.plugin_id,
            plugin_version=run.plugin_version,
            action_id=run.action_id,
            project_key=run.project_key,
            collection=run.collection,
            target_count=run.target_count,
            risk=run.risk,
            state=status.state,
            cancel_requested=task_id in self._cancel_requested,
            progress=PluginProgress(
                current=status.progress.done,
                total=status.progress.total,
                message=status.progress.message,
                cancellable=(
                    status.state in {"queued", "running"}
                    and task_id not in self._non_cancellable_tasks
                ),
            ),
            result=result,
            error=_safe_error(status.error, run) if status.error else None,
        )

    async def request_cancel(self, task_id: str) -> PluginTaskSnapshot:
        if task_id not in self._runs:
            raise KeyError(f"unknown plugin task {task_id!r}")
        # A running Directus Operation cannot be force-cancelled truthfully.
        # Record the request; the interaction bridge/Flow may observe it at its
        # next cooperative progress point, while the eventual task state still
        # follows the real server result.
        self._cancel_requested.add(task_id)
        run = self._runs[task_id]
        cancellable_before_remote = (
            task_id not in self._directus_active_tasks
            and task_id not in self._non_cancellable_tasks
        )
        if cancellable_before_remote:
            await self._tasks.cancel(task_id)
        if task_id in self._worker_active_tasks:
            cancel_worker = cast(
                Callable[[str], Awaitable[Any]] | None,
                getattr(self._worker_adapter, "cancel", None),
            )
            if callable(cancel_worker):
                await cancel_worker(run.run_id)
        request_local_cancel = cast(
            Callable[[str], Awaitable[Any]] | None,
            getattr(self._confirmation_adapter, "request_cancel", None),
        )
        if callable(request_local_cancel) and task_id not in self._non_cancellable_tasks:
            await request_local_cancel(run.run_id)
        if self._interaction_adapter is not None and task_id in self._directus_active_tasks:
            await self._interaction_adapter.request_cancel(run.run_id)
        if cancellable_before_remote:
            await self._tasks.wait(task_id)
        return self.get_task(task_id)

    async def cancel_plugin_tasks(self, project_key: str, plugin_id: str) -> int:
        """Hide/cancel every active task before a plugin is removed locally."""

        cancelled = 0
        for task_id, run in list(self._runs.items()):
            if run.project_key != project_key or run.plugin_id != plugin_id:
                continue
            try:
                status = self._tasks.status(task_id)
            except KeyError:
                continue
            if status.state not in {"queued", "running"}:
                continue
            await self.request_cancel(task_id)
            cancelled += 1
        return cancelled

    async def _watch_interactions(self, run_id: str) -> None:
        if self._interaction_adapter is None or self._notification_sink is None:
            return
        previous: str | None = None
        revision = 1
        while True:
            snapshot: InteractionSnapshot = await self._interaction_adapter.watch(run_id)
            serialized = snapshot.model_dump_json(by_alias=True)
            if serialized != previous and (
                snapshot.pending_confirmation is not None or snapshot.progress is not None
            ):
                revision += 1
                await self._notification_sink(
                    PluginEventEnvelope(
                        event_type="plugin.interaction.requested",
                        project_key=snapshot.project_key,
                        entity_id=run_id,
                        revision=revision,
                        snapshot=snapshot.model_dump(mode="json", by_alias=True),
                    )
                )
                previous = serialized
            await asyncio.sleep(0.2)

    @staticmethod
    def _context_reasons(action: PluginAction, context: CommandContext) -> list[str]:
        selection = action.requires.get("selection")
        count = len(context.selected_keys)
        if selection == "one-or-more" and count == 0:
            return ["selection_required"]
        if selection == "exactly-one" and count != 1:
            return ["single_selection_required"]
        if (
            action.placements
            and any(place.startswith("table.") for place in action.placements)
            and not context.collection
        ):
            return ["collection_required"]
        return []


def _schema_payload(raw: Any) -> Any:
    if not isinstance(raw, dict):
        return raw
    table = raw.get("table")
    if isinstance(table, dict) and "data" in table:
        return table["data"]
    return raw


def _raise_if_cancelled(cancel: Any) -> None:
    if bool(getattr(cancel, "cancelled", False)):
        raise asyncio.CancelledError


def _safe_error(message: str, run: _PluginRun) -> PluginSafeError:
    lowered = message.lower()
    code = "plugin_action_failed"
    recoverability: str = "retry"
    if "worker" in lowered:
        code = "plugin_worker_terminated"
    elif "schema" in lowered or "output" in lowered:
        code = "plugin_output_invalid"
        recoverability = "reinstall"
    elif "flow" in lowered and ("missing" in lowered or "bound" in lowered):
        code = "plugin_flow_unavailable"
        recoverability = "rebind"
    elif "confirm" in lowered:
        code = "plugin_confirmation_failed"
        recoverability = "retry"
    elif "directus" in lowered or "connection" in lowered:
        code = "plugin_directus_unavailable"
    return PluginSafeError(
        code=code,
        message=message,
        recoverability=cast(Any, recoverability),
        plugin_id=run.plugin_id,
        action_id=run.action_id,
        run_id=run.run_id,
        cause_id=f"cause-{uuid.uuid4().hex[:12]}",
    )


__all__ = ["PluginExecutionRuntime"]
