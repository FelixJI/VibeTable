"""Closed application surface for local-worker plugins."""

from __future__ import annotations

import contextlib
import uuid
from collections.abc import Awaitable, Callable
from typing import Any, Protocol, cast

from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.application.plugin_package_lifecycle import PluginPackageLifecycle
from backend.application.plugin_registry import PluginRegistry, validate_install_plan
from backend.contracts.plugin import (
    ActionAvailability,
    CommandContext,
    InstallPlan,
    InteractionDecision,
    InteractionResolveResult,
    PluginEventEnvelope,
    PluginPackageRevision,
    PluginSnapshot,
    PluginTaskSnapshot,
    UninstallResult,
)
from backend.contracts.task import SessionPathGrant

NotificationSink = Callable[[PluginEventEnvelope], Awaitable[None]]


class PluginStorePort(Protocol):
    """The shared-state operations the platform orchestration needs."""

    async def commit_install(
        self,
        plan: InstallPlan,
        *,
        package_revision: PluginPackageRevision,
    ) -> PluginSnapshot: ...

    async def save_installation(
        self,
        snapshot: PluginSnapshot,
        *,
        expected_revision: int | None,
    ) -> PluginSnapshot: ...

    async def list_package_revisions(
        self,
        project_key: str,
        plugin_id: str,
    ) -> list[PluginPackageRevision]: ...

    async def save_package_revision(
        self,
        revision: PluginPackageRevision,
    ) -> PluginPackageRevision: ...

    async def delete_package_revision(
        self,
        project_key: str,
        plugin_id: str,
        package_hash: str,
    ) -> bool: ...

    async def delete_package_revisions(self, project_key: str, plugin_id: str) -> int: ...

    async def is_package_path_referenced(self, local_path: str) -> bool: ...


class PluginPlatformService:
    """Coordinates package validation, registry state and local execution.

    The Go-owned shared catalog answers the public catalog, audit and
    enable/disable surface; install commits are fixed atomic store
    operations, so this service never compensates with unconditional
    save/delete sequences.
    """

    def __init__(
        self,
        *,
        registry: PluginRegistry,
        runtime: PluginExecutionRuntime,
        store: PluginStorePort,
        package_lifecycle: PluginPackageLifecycle,
        confirmation_adapter: Any | None = None,
        file_adapter: Any | None = None,
        **_unused: Any,
    ) -> None:
        self._registry = registry
        self._runtime = runtime
        self._store = store
        self._package_lifecycle = package_lifecycle
        self._plans: dict[str, InstallPlan] = {}
        self._notification_sink: NotificationSink | None = None
        self._confirmation_adapter = confirmation_adapter
        self._file_adapter = file_adapter
        runtime.set_notification_sink(self._emit)

    def set_notification_sink(self, sink: NotificationSink) -> None:
        self._notification_sink = sink
        for adapter in (self._confirmation_adapter, self._file_adapter):
            configure = getattr(adapter, "set_notification_sink", None)
            if callable(configure):
                configure(self._emit)

    async def close(self) -> None:
        """No local resources: the Go-owned shared store owns its lifecycle."""

        return None

    async def inspect_install(
        self,
        *,
        project_key: str,
        project_revision: str,
        source_location: str,
    ) -> InstallPlan:
        inspected = self._package_lifecycle.inspect(source_location)
        plan = InstallPlan(
            plan_id=f"plugin-plan-{uuid.uuid4().hex[:12]}",
            project_key=project_key,
            project_revision=project_revision,
            source_type=inspected.source_type,
            source_location=inspected.source_location,
            package_hash=inspected.package_hash,
            manifest=inspected.manifest,
        )
        self._plans[plan.plan_id] = plan
        return plan

    async def commit_install(
        self,
        *,
        plan_id: str,
        project_revision: str,
    ) -> PluginSnapshot:
        plan = self._consume_plan(plan_id, project_revision)
        validate_install_plan(plan)
        self._recheck_plan_source(plan)
        retained_path = self._package_lifecycle.retain(
            source_location=plan.source_location,
            expected_hash=plan.package_hash,
        )
        try:
            # One fixed atomic store operation: installation identity,
            # the current package revision and the install audit event are
            # committed together and the durable catalog change is emitted by
            # the Go catalog, never duplicated here.
            installed = await self._store.commit_install(
                plan,
                package_revision=PluginPackageRevision(
                    project_key=plan.project_key,
                    plugin_id=plan.manifest.plugin_id,
                    version=plan.manifest.version,
                    package_hash=plan.package_hash,
                    local_path=retained_path,
                    manifest=plan.manifest,
                    state="current",
                ),
            )
        except Exception as commit_error:
            # Cleanup must never mask the original commit failure: if the
            # shared catalog cannot answer the reference query, the cached
            # package is kept (the safest outcome) and the original error is
            # re-raised unchanged.
            with contextlib.suppress(Exception):
                await self._delete_package_if_unreferenced(retained_path)
            raise commit_error
        return installed

    def cancel_install(self, *, plan_id: str) -> bool:
        return self._plans.pop(plan_id, None) is not None

    async def upgrade(
        self,
        *,
        project_key: str,
        plugin_id: str,
        plan_id: str,
        project_revision: str,
    ) -> PluginSnapshot:
        plan = self._consume_plan(plan_id, project_revision)
        if plan.project_key != project_key or plan.manifest.plugin_id != plugin_id:
            raise ValueError("upgrade plan identity does not match the installation")
        self._recheck_plan_source(plan)
        retained_path = self._package_lifecycle.retain(
            source_location=plan.source_location,
            expected_hash=plan.package_hash,
        )
        previous = list(await self._store.list_package_revisions(project_key, plugin_id))
        previous_installation = await self._registry.get(project_key, plugin_id)
        snapshot: PluginSnapshot | None = None
        try:
            snapshot = await self._registry.commit_upgrade(plan)
            for revision in previous:
                if revision.state == "current":
                    await self._store.save_package_revision(
                        revision.model_copy(update={"state": "rollback"})
                    )
            await self._store.save_package_revision(
                PluginPackageRevision(
                    project_key=project_key,
                    plugin_id=plugin_id,
                    version=plan.manifest.version,
                    package_hash=plan.package_hash,
                    local_path=retained_path,
                    manifest=plan.manifest,
                    state="current",
                )
            )
            await self._prune_package_revisions(project_key, plugin_id)
        except Exception:
            if snapshot is not None and previous_installation is not None:
                await self._store.save_installation(
                    previous_installation,
                    expected_revision=snapshot.revision,
                )
            await self._store.delete_package_revision(
                project_key,
                plugin_id,
                plan.package_hash,
            )
            for revision in previous:
                await self._store.save_package_revision(revision)
            await self._delete_package_if_unreferenced(retained_path)
            raise
        return snapshot

    async def rollback(self, *, project_key: str, plugin_id: str) -> PluginSnapshot:
        revisions = list(await self._store.list_package_revisions(project_key, plugin_id))
        current_revision = next(
            (item for item in revisions if item.state == "current"),
            None,
        )
        rollback_revision = next(
            (item for item in reversed(revisions) if item.state == "rollback"),
            None,
        )
        if current_revision is None or rollback_revision is None:
            raise ValueError("plugin rollback package is unavailable")
        rollback_path = rollback_revision.local_path
        if not self._package_lifecycle.is_available(rollback_path):
            raise ValueError("plugin rollback package is missing")
        inspected = self._package_lifecycle.inspect(rollback_path)
        if (
            inspected.package_hash != rollback_revision.package_hash
            or inspected.manifest != rollback_revision.manifest
        ):
            raise ValueError("plugin rollback package failed integrity verification")
        previous_installation = await self._registry.get(project_key, plugin_id)
        if previous_installation is None:
            raise ValueError("plugin is not installed")
        plan = InstallPlan(
            plan_id=f"plugin-rollback-{uuid.uuid4().hex[:12]}",
            project_key=project_key,
            project_revision=str(previous_installation.revision),
            source_type="package",
            source_location=rollback_path,
            package_hash=rollback_revision.package_hash,
            manifest=rollback_revision.manifest,
            schemas=previous_installation.schemas,
        )
        snapshot: PluginSnapshot | None = None
        try:
            snapshot = await self._registry.commit_rollback(plan)
            await self._store.save_package_revision(
                current_revision.model_copy(update={"state": "rollback"})
            )
            await self._store.save_package_revision(
                rollback_revision.model_copy(update={"state": "current"})
            )
        except Exception:
            if snapshot is not None:
                await self._store.save_installation(
                    previous_installation,
                    expected_revision=snapshot.revision,
                )
            for revision in revisions:
                await self._store.save_package_revision(revision)
            raise
        return snapshot

    async def uninstall(
        self,
        *,
        project_key: str,
        plugin_id: str,
        cleanup_private_settings: bool,
    ) -> UninstallResult:
        await self._runtime.cancel_plugin_tasks(project_key, plugin_id)
        retained_paths = [
            revision.local_path
            for revision in await self._store.list_package_revisions(project_key, plugin_id)
        ]
        result = await self._registry.uninstall(
            project_key,
            plugin_id,
            cleanup_private_settings=cleanup_private_settings,
        )
        await self._store.delete_package_revisions(project_key, plugin_id)
        for retained_path in retained_paths:
            await self._delete_package_if_unreferenced(retained_path)
        return result

    async def describe_action(
        self,
        *,
        project_key: str,
        plugin_id: str,
        action_id: str,
        context: CommandContext,
    ) -> ActionAvailability:
        if context.project_key != project_key:
            raise ValueError("plugin context project does not match request")
        return await self._runtime.describe(plugin_id, action_id, context)

    async def start_action(
        self,
        *,
        project_key: str,
        plugin_id: str,
        action_id: str,
        context: CommandContext,
        input_payload: dict[str, Any],
        task_id: str,
        run_id: str,
    ) -> PluginTaskSnapshot:
        if context.project_key != project_key:
            raise ValueError("plugin context project does not match request")
        return await self._runtime.start(
            plugin_id,
            action_id,
            context,
            input_payload,
            task_id=task_id,
            run_id=run_id,
        )

    async def resolve_interaction(
        self,
        *,
        run_id: str,
        interaction_id: str,
        decision: InteractionDecision,
    ) -> InteractionResolveResult:
        resolver = getattr(self._confirmation_adapter, "try_resolve", None)
        if not callable(resolver):
            return InteractionResolveResult(status="expired")
        result = await cast(
            Callable[
                [str, str, InteractionDecision],
                Awaitable[InteractionResolveResult | None],
            ],
            resolver,
        )(run_id, interaction_id, decision)
        return result or InteractionResolveResult(status="expired")

    async def resolve_file(self, *, request_id: str, grant: SessionPathGrant | None) -> bool:
        resolver = getattr(self._file_adapter, "resolve", None)
        if not callable(resolver):
            return False
        return bool(
            await cast(
                Callable[[str, SessionPathGrant | None], Awaitable[bool]],
                resolver,
            )(request_id, grant)
        )

    async def cancel_task(self, *, task_id: str) -> bool:
        """Triggers the host-owned task's execution cancel handle.

        Returns whether an active execution handle was found. Public task
        state is owned by the WPF host registry; this is never a status query.
        """
        return await self._runtime.request_cancel(task_id)

    def _consume_plan(self, plan_id: str, project_revision: str) -> InstallPlan:
        try:
            plan = self._plans[plan_id]
        except KeyError as exc:
            raise ValueError("plugin install plan was not found") from exc
        if plan.project_revision != project_revision:
            raise ValueError("plugin project revision changed")
        del self._plans[plan_id]
        return plan

    def _recheck_plan_source(self, plan: InstallPlan) -> None:
        checked = self._package_lifecycle.inspect(plan.source_location)
        if checked.package_hash != plan.package_hash:
            raise ValueError("plugin source changed after inspection")

    async def _prune_package_revisions(self, project_key: str, plugin_id: str) -> None:
        revisions = await self._store.list_package_revisions(project_key, plugin_id)
        rollback = [revision for revision in revisions if revision.state == "rollback"]
        for retired in rollback[:-1]:
            await self._store.delete_package_revision(
                project_key,
                plugin_id,
                retired.package_hash,
            )
            await self._delete_package_if_unreferenced(retired.local_path)

    async def _delete_package_if_unreferenced(self, path: str) -> None:
        if await self._store.is_package_path_referenced(path):
            return
        self._package_lifecycle.discard(path)

    async def _emit(self, event: PluginEventEnvelope) -> None:
        if self._notification_sink is not None:
            await self._notification_sink(event)


__all__ = ["PluginPlatformService"]
