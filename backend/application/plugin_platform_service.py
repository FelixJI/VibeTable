"""Closed, task-oriented façade for the Flow-first plugin platform."""

from __future__ import annotations

import asyncio
import contextlib
import hashlib
import json
import shutil
import time
import uuid
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, cast

from backend.application.flow_binding_manager import FlowBindingManager
from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.application.plugin_registry import PluginRegistry
from backend.contracts.plugin import (
    ActionAvailability,
    CommandContext,
    ExternalFlowAttestation,
    ExternalFlowCandidate,
    FlowBindingSnapshot,
    FlowRequirement,
    InstallPlan,
    InteractionDecision,
    InteractionResolveResult,
    PluginAuditEvent,
    PluginEventEnvelope,
    PluginManifest,
    PluginPackageRevision,
    PluginSnapshot,
    PluginTaskSnapshot,
    UninstallResult,
)
from backend.infrastructure.plugin_package import (
    inspect_plugin_package,
    pack_plugin,
    read_plugin_package_member,
)


class PluginPlatformService:
    """Coordinates package, Flow, registry, task and audit boundaries."""

    def __init__(
        self,
        *,
        store: Any,
        registry: PluginRegistry,
        bindings: FlowBindingManager,
        directus: Any,
        runtime: PluginExecutionRuntime | None,
        interactions: Any | None,
        package_cache: Path,
        local_confirmations: Any | None = None,
        local_files: Any | None = None,
    ) -> None:
        self._store = store
        self._registry = registry
        self._bindings = bindings
        self._directus = directus
        self._runtime = runtime
        self._interactions = interactions
        self._local_confirmations = local_confirmations
        self._local_files = local_files
        self._package_cache = package_cache
        self._plans: dict[str, InstallPlan] = {}
        self._plan_base_revisions: dict[str, str] = {}
        self._known_projects: set[str] = set()
        self._notification_sink: Any | None = None
        self._catalog_event_revisions: dict[str, int] = {}
        self._source_watch_task: asyncio.Task[None] | None = None
        self._audited_terminal_tasks: set[str] = set()
        self._task_started_at: dict[str, tuple[datetime, float]] = {}
        self._task_installations: dict[str, PluginSnapshot] = {}
        self._task_by_run: dict[str, str] = {}
        self._audited_interactions: set[tuple[str, str]] = set()

    def set_notification_sink(self, sink: Any) -> None:
        self._notification_sink = sink
        if self._source_watch_task is None or self._source_watch_task.done():
            self._source_watch_task = asyncio.create_task(self._watch_local_sources())

    async def close(self) -> None:
        task = self._source_watch_task
        self._source_watch_task = None
        if task is not None:
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await task

    async def inspect_install(
        self, *, project_key: str, project_revision: str, source_location: str
    ) -> InstallPlan:
        self._known_projects.add(project_key)
        effective_revision = self._project_revision(project_key, project_revision)
        plan = _inspect_plan(project_key, effective_revision, source_location)
        self._plans[plan.plan_id] = plan
        self._plan_base_revisions[plan.plan_id] = project_revision
        event_type = (
            "upgrade.inspect"
            if self._registry.get(project_key, plan.manifest.plugin_id) is not None
            else "install.inspect"
        )
        self._audit(plan, event_type, "succeeded")
        return plan

    async def commit_install(self, *, plan_id: str, project_revision: str) -> PluginSnapshot:
        plan = self._checked_plan(plan_id, project_revision)
        checked = _inspect_plan(
            plan.project_key, plan.project_revision, plan.source_location, plan_id=plan.plan_id
        )
        if checked.package_hash != plan.package_hash:
            raise ValueError("plugin source changed after inspection")
        retained_path = self._retain_package(plan)
        registry_plan = plan.model_copy(update={"source_location": str(retained_path)})
        provisioned = False
        installed: PluginSnapshot | None = None
        try:
            for requirement in plan.flow_requirements:
                if requirement.ownership == "managed":
                    await self._bindings.provision_managed(
                        project_key=plan.project_key,
                        plugin_id=plan.manifest.plugin_id,
                        requirement=requirement,
                        activate=requirement.trigger not in {"schedule", "event"},
                    )
                    provisioned = True
            installed = await self._registry.install(registry_plan)
            if installed.status == "enabled":
                await self._bindings.set_automatic_enabled(
                    plan.project_key, plan.manifest.plugin_id, True
                )
        except Exception:
            if installed is not None:
                with contextlib.suppress(Exception):
                    await self._registry.uninstall(
                        project_key=plan.project_key,
                        plugin_id=plan.manifest.plugin_id,
                        cleanup_private_settings=False,
                    )
            elif provisioned:
                await self._bindings.remove_owned(plan.project_key, plan.manifest.plugin_id)
            self._delete_retained_package(str(retained_path))
            self._audit(plan, "install.commit", "failed")
            raise
        if installed is None:
            raise RuntimeError("plugin installation did not produce a snapshot")
        if plan.source_type == "local-folder":
            updated = installed.model_copy(
                update={
                    "development_source_location": plan.source_location,
                    "source_changed": False,
                    "revision": installed.revision + 1,
                }
            )
            installed = self._store.save_installation(updated, expected_revision=installed.revision)
        self._store.save_package_revision(
            PluginPackageRevision(
                project_key=plan.project_key,
                plugin_id=plan.manifest.plugin_id,
                version=plan.manifest.version,
                package_hash=plan.package_hash,
                local_path=str(retained_path),
                manifest=plan.manifest,
                flow_bindings=self._store.list_bindings(plan.project_key, plan.manifest.plugin_id),
                state="current",
            )
        )
        self._prune_package_revisions(plan.project_key, plan.manifest.plugin_id)
        self._plans.pop(plan_id, None)
        self._plan_base_revisions.pop(plan_id, None)
        self._audit(plan, "install.commit", "succeeded")
        snapshot = self._with_bindings(cast(PluginSnapshot, installed))
        await self._emit_catalog(snapshot)
        return snapshot

    async def list_catalog(self, *, project_key: str) -> list[PluginSnapshot]:
        self._known_projects.add(project_key)
        snapshots: list[PluginSnapshot] = []
        for installation in self._registry.list(project_key):
            before = self._with_bindings(installation)
            installation = await self._refresh_plugin_health(project_key, installation.plugin_id)
            installation = self._refresh_local_source(installation)
            snapshot = self._with_bindings(installation)
            snapshots.append(snapshot)
            if snapshot.model_dump(mode="json") != before.model_dump(mode="json"):
                await self._emit_catalog(snapshot)
        return snapshots

    def list_audit(self, *, project_key: str, plugin_id: str) -> list[PluginAuditEvent]:
        return self._store.list_audit(project_key, plugin_id)

    def list_pending_cleanup(self, *, project_key: str) -> list[PluginAuditEvent]:
        latest: dict[str, PluginAuditEvent] = {}
        for event in self._store.list_project_audit(project_key):
            if event.event_type in {"uninstall", "uninstall-cleanup-retry"}:
                latest[event.plugin_id] = event
        return [event for event in latest.values() if event.outcome == "pending-cleanup"]

    async def list_external_flow_candidates(
        self, *, project_key: str, plugin_id: str, logical_flow_id: str
    ) -> list[ExternalFlowCandidate]:
        requirement = self._requirement(project_key, plugin_id, logical_flow_id)
        candidates: list[ExternalFlowCandidate] = []
        for flow in await self._directus.list_flows():
            reasons: list[str] = []
            if flow.trigger != requirement.trigger:
                reasons.append("trigger_mismatch")
            if flow.status != "active":
                reasons.append("flow_inactive")
            if set(requirement.requires_operations) - set(flow.operation_keys):
                reasons.append("required_operation_missing")
            candidates.append(
                ExternalFlowCandidate(
                    directus_flow_uuid=flow.flow_uuid,
                    name=str(flow.definition.get("name", flow.flow_uuid)),
                    trigger_type=flow.trigger,
                    status=flow.status,
                    operation_keys=list(flow.operation_keys),
                    compatible=not reasons,
                    reasons=reasons,
                )
            )
        return candidates

    async def bind_external_flow(
        self,
        *,
        project_key: str,
        plugin_id: str,
        logical_flow_id: str,
        directus_flow_uuid: str,
        accepts_unknown_side_effects: bool,
    ) -> FlowBindingSnapshot:
        requirement = self._requirement(project_key, plugin_id, logical_flow_id)
        binding = await self._bindings.bind_external(
            project_key=project_key,
            plugin_id=plugin_id,
            requirement=requirement,
            directus_uuid=directus_flow_uuid,
            attestation=ExternalFlowAttestation(
                accepts_unknown_side_effects=accepts_unknown_side_effects
            ),
        )
        current = await self._refresh_plugin_health(project_key, plugin_id)
        if current.status == "enabled":
            await self._bindings.set_automatic_enabled(project_key, plugin_id, True)
        self._audit_snapshot(current, "external-flow.bind", "succeeded")
        await self._emit_catalog(self._with_bindings(current))
        return binding

    async def set_enabled(
        self, *, project_key: str, plugin_id: str, enabled: bool
    ) -> PluginSnapshot:
        current = self._installed(project_key, plugin_id)
        if (current.status == "enabled") == enabled:
            return self._with_bindings(current)
        if enabled:
            current = await self._refresh_plugin_health(project_key, plugin_id)
            if self._registry.blocking_reasons(project_key, plugin_id):
                return await self._registry.set_enabled(
                    project_key=project_key, plugin_id=plugin_id, enabled=True
                )
        await self._bindings.set_automatic_enabled(project_key, plugin_id, enabled)
        try:
            snapshot = await self._registry.set_enabled(
                project_key=project_key, plugin_id=plugin_id, enabled=enabled
            )
        except Exception:
            with contextlib.suppress(Exception):
                await self._bindings.set_automatic_enabled(project_key, plugin_id, not enabled)
            self._audit_snapshot(
                current,
                "lifecycle.enable" if enabled else "lifecycle.disable",
                "failed",
            )
            raise
        self._audit_snapshot(
            snapshot, "lifecycle.enable" if enabled else "lifecycle.disable", "succeeded"
        )
        projected = self._with_bindings(snapshot)
        await self._emit_catalog(projected)
        return projected

    async def upgrade(
        self,
        *,
        project_key: str,
        plugin_id: str,
        plan_id: str,
        project_revision: str,
    ) -> PluginSnapshot:
        plan = self._checked_plan(plan_id, project_revision)
        if plan.project_key != project_key or plan.manifest.plugin_id != plugin_id:
            raise ValueError("upgrade plan does not match the installed plugin")
        checked = _inspect_plan(
            project_key, project_revision, plan.source_location, plan_id=plan_id
        )
        if checked.package_hash != plan.package_hash:
            raise ValueError("plugin source changed after inspection")
        retained_path = self._retain_package(plan)
        registry_plan = plan.model_copy(update={"source_location": str(retained_path)})
        previous_bindings = {
            binding.logical_flow_id: binding
            for binding in self._store.list_bindings(project_key, plugin_id)
        }
        previous_installation = self._installed(project_key, plugin_id)
        preserve_user_disabled = (
            previous_installation.status == "disabled"
            and previous_installation.disabled_reason == "user_disabled"
        )
        previous_requirements = {
            requirement.logical_flow_id: requirement
            for requirement in previous_installation.flow_requirements
        }
        changed_managed: list[tuple[str, str]] = []
        removed_requirements: list[tuple[FlowRequirement, FlowBindingSnapshot]] = []
        try:
            for requirement in plan.flow_requirements:
                if requirement.ownership == "managed":
                    existing = previous_bindings.get(requirement.logical_flow_id)
                    if existing is None:
                        await self._bindings.provision_managed(
                            project_key=project_key,
                            plugin_id=plugin_id,
                            requirement=requirement,
                            activate=requirement.trigger not in {"schedule", "event"},
                        )
                        changed_managed.append((requirement.logical_flow_id, "created"))
                    else:
                        await self._bindings.upgrade_managed(
                            project_key=project_key,
                            plugin_id=plugin_id,
                            requirement=requirement,
                            activate_new=requirement.trigger not in {"schedule", "event"},
                        )
                        changed_managed.append((requirement.logical_flow_id, "upgraded"))
                else:
                    await self._bindings.validate_external(
                        project_key=project_key,
                        plugin_id=plugin_id,
                        requirement=requirement,
                    )
            current_ids = {item.logical_flow_id for item in plan.flow_requirements}
            for logical_id in previous_bindings.keys() - current_ids:
                previous_requirement = previous_requirements[logical_id]
                removed_requirements.append((previous_requirement, previous_bindings[logical_id]))
                await self._bindings.remove_requirement(project_key, plugin_id, logical_id)
            should_enable_automatic = (
                not preserve_user_disabled
                and not self._registry.blocking_reasons_for(
                    project_key, plugin_id, plan.flow_requirements
                )
            )
            await self._bindings.set_automatic_enabled(
                project_key, plugin_id, should_enable_automatic
            )
            snapshot = await self._registry.commit_upgrade(registry_plan)
        except Exception:
            for requirement, previous_binding in reversed(removed_requirements):
                with contextlib.suppress(Exception):
                    await self._bindings.restore_requirement(
                        project_key=project_key,
                        plugin_id=plugin_id,
                        requirement=requirement,
                        previous_binding=previous_binding,
                        activate=requirement.trigger not in {"schedule", "event"},
                    )
            for logical_id, change in reversed(changed_managed):
                with contextlib.suppress(Exception):
                    if change == "upgraded":
                        await self._bindings.rollback_managed(
                            project_key,
                            plugin_id,
                            logical_id,
                            activate_restored=(
                                previous_requirements[logical_id].trigger
                                not in {"schedule", "event"}
                            ),
                        )
                    else:
                        await self._bindings.remove_requirement(project_key, plugin_id, logical_id)
            with contextlib.suppress(Exception):
                await self._bindings.set_automatic_enabled(
                    project_key, plugin_id, previous_installation.status == "enabled"
                )
            self._audit(plan, "lifecycle.upgrade", "failed")
            raise
        desired_development_source = (
            plan.source_location if plan.source_type == "local-folder" else None
        )
        if (
            snapshot.development_source_location != desired_development_source
            or snapshot.source_changed
        ):
            updated = snapshot.model_copy(
                update={
                    "development_source_location": desired_development_source,
                    "source_changed": False,
                    "revision": snapshot.revision + 1,
                }
            )
            snapshot = self._store.save_installation(updated, expected_revision=snapshot.revision)
        for revision in self._store.list_package_revisions(project_key, plugin_id):
            if revision.state == "current":
                self._store.save_package_revision(
                    revision.model_copy(
                        update={
                            "state": "rollback",
                            "flow_bindings": list(previous_bindings.values()),
                        }
                    )
                )
        self._store.save_package_revision(
            PluginPackageRevision(
                project_key=project_key,
                plugin_id=plugin_id,
                version=plan.manifest.version,
                package_hash=plan.package_hash,
                local_path=str(retained_path),
                manifest=plan.manifest,
                flow_bindings=self._store.list_bindings(project_key, plugin_id),
                state="current",
            )
        )
        self._prune_package_revisions(project_key, plugin_id)
        self._plans.pop(plan_id, None)
        self._plan_base_revisions.pop(plan_id, None)
        self._audit_snapshot(snapshot, "lifecycle.upgrade", "succeeded")
        projected = self._with_bindings(snapshot)
        await self._emit_catalog(projected)
        return projected

    async def rollback(self, *, project_key: str, plugin_id: str) -> PluginSnapshot:
        revisions = self._store.list_package_revisions(project_key, plugin_id)
        rollback = next((item for item in reversed(revisions) if item.state == "rollback"), None)
        current = next((item for item in reversed(revisions) if item.state == "current"), None)
        if rollback is None or current is None:
            raise ValueError("plugin rollback revision is unavailable")
        plan = _inspect_plan(
            project_key,
            f"rollback:{self._installed(project_key, plugin_id).revision}",
            rollback.local_path,
        )
        current_installation = self._installed(project_key, plugin_id)
        preserve_user_disabled = (
            current_installation.status == "disabled"
            and current_installation.disabled_reason == "user_disabled"
        )
        current_requirements = {
            requirement.logical_flow_id: requirement
            for requirement in current_installation.flow_requirements
        }
        current_bindings = {
            binding.logical_flow_id: binding
            for binding in self._store.list_bindings(project_key, plugin_id)
        }
        retained_bindings = {binding.logical_flow_id: binding for binding in rollback.flow_bindings}
        desired_ids = {requirement.logical_flow_id for requirement in plan.flow_requirements}
        changed_managed: list[tuple[str, str]] = []
        restored_external: list[str] = []
        removed_current: list[tuple[FlowRequirement, FlowBindingSnapshot]] = []
        try:
            for requirement in plan.flow_requirements:
                existing = self._bindings.resolve(
                    project_key, plugin_id, requirement.logical_flow_id
                )
                if requirement.ownership == "managed":
                    if existing is None:
                        await self._bindings.provision_managed(
                            project_key=project_key,
                            plugin_id=plugin_id,
                            requirement=requirement,
                            activate=requirement.trigger not in {"schedule", "event"},
                        )
                        changed_managed.append((requirement.logical_flow_id, "created"))
                    elif existing.rollback_flow_uuid is not None:
                        await self._bindings.rollback_managed(
                            project_key,
                            plugin_id,
                            requirement.logical_flow_id,
                            activate_restored=requirement.trigger not in {"schedule", "event"},
                        )
                        changed_managed.append((requirement.logical_flow_id, "swapped"))
                    else:
                        await self._bindings.upgrade_managed(
                            project_key=project_key,
                            plugin_id=plugin_id,
                            requirement=requirement,
                            activate_new=requirement.trigger not in {"schedule", "event"},
                        )
                        changed_managed.append((requirement.logical_flow_id, "swapped"))
                elif existing is not None:
                    await self._bindings.validate_external(
                        project_key=project_key,
                        plugin_id=plugin_id,
                        requirement=requirement,
                    )
                elif requirement.logical_flow_id in retained_bindings:
                    self._store.save_binding(retained_bindings[requirement.logical_flow_id])
                    restored_external.append(requirement.logical_flow_id)
                    await self._bindings.validate_external(
                        project_key=project_key,
                        plugin_id=plugin_id,
                        requirement=requirement,
                    )
            for logical_id in current_bindings.keys() - desired_ids:
                removed_current.append(
                    (current_requirements[logical_id], current_bindings[logical_id])
                )
                await self._bindings.remove_requirement(project_key, plugin_id, logical_id)
            should_enable_automatic = (
                not preserve_user_disabled
                and not self._registry.blocking_reasons_for(
                    project_key, plugin_id, plan.flow_requirements
                )
            )
            await self._bindings.set_automatic_enabled(
                project_key, plugin_id, should_enable_automatic
            )
            snapshot = await self._registry.commit_upgrade(plan)
        except Exception:
            for requirement, previous_binding in reversed(removed_current):
                with contextlib.suppress(Exception):
                    await self._bindings.restore_requirement(
                        project_key=project_key,
                        plugin_id=plugin_id,
                        requirement=requirement,
                        previous_binding=previous_binding,
                        activate=requirement.trigger not in {"schedule", "event"},
                    )
            for logical_id, change in reversed(changed_managed):
                with contextlib.suppress(Exception):
                    if change == "swapped":
                        await self._bindings.rollback_managed(
                            project_key,
                            plugin_id,
                            logical_id,
                            activate_restored=(
                                current_requirements[logical_id].trigger
                                not in {"schedule", "event"}
                            ),
                        )
                    else:
                        await self._bindings.remove_requirement(project_key, plugin_id, logical_id)
            for logical_id in restored_external:
                with contextlib.suppress(Exception):
                    await self._bindings.remove_requirement(project_key, plugin_id, logical_id)
            with contextlib.suppress(Exception):
                await self._bindings.set_automatic_enabled(
                    project_key, plugin_id, current_installation.status == "enabled"
                )
            self._audit_snapshot(current_installation, "lifecycle.rollback", "failed")
            raise
        self._store.save_package_revision(
            current.model_copy(
                update={
                    "state": "rollback",
                    "flow_bindings": list(current_bindings.values()),
                }
            )
        )
        self._store.save_package_revision(
            rollback.model_copy(
                update={
                    "state": "current",
                    "flow_bindings": self._store.list_bindings(project_key, plugin_id),
                }
            )
        )
        self._audit_snapshot(snapshot, "lifecycle.rollback", "succeeded")
        projected = self._with_bindings(snapshot)
        await self._emit_catalog(projected)
        return projected

    async def resolve_drift(
        self,
        *,
        project_key: str,
        plugin_id: str,
        logical_flow_id: str,
        strategy: str,
    ) -> PluginSnapshot:
        requirement = self._requirement(project_key, plugin_id, logical_flow_id)
        if strategy == "restore":
            installation = self._installed(project_key, plugin_id)
            binding = await self._bindings.restore_managed(
                project_key=project_key,
                plugin_id=plugin_id,
                requirement=requirement,
                activate=(
                    installation.status == "enabled"
                    or requirement.trigger not in {"schedule", "event"}
                ),
            )
        elif strategy == "detach":
            binding = self._bindings.detach_managed(project_key, plugin_id, logical_flow_id)
            self._registry.adopt_external_flow(project_key, plugin_id, logical_flow_id)
        else:
            raise ValueError("unknown managed Flow drift resolution")
        snapshot = await self._refresh_plugin_health(project_key, plugin_id)
        self._audit_snapshot(
            snapshot,
            "managed-flow.drift-resolve",
            "succeeded",
            {
                "logicalFlowId": logical_flow_id,
                "strategy": strategy,
                "directusFlowUuid": binding.directus_flow_uuid,
            },
        )
        projected = self._with_bindings(snapshot)
        await self._emit_catalog(projected)
        return projected

    async def uninstall(
        self,
        *,
        project_key: str,
        plugin_id: str,
        cleanup_private_settings: bool,
    ) -> UninstallResult:
        revisions = self._store.list_package_revisions(project_key, plugin_id)
        installation = self._registry.get(project_key, plugin_id)
        if installation is not None and installation.status != "disabled":
            await self.set_enabled(
                project_key=project_key,
                plugin_id=plugin_id,
                enabled=False,
            )
        if self._runtime is not None:
            await self._runtime.cancel_plugin_tasks(project_key, plugin_id)
        result = await self._registry.uninstall(
            project_key=project_key,
            plugin_id=plugin_id,
            cleanup_private_settings=cleanup_private_settings,
        )
        self._store.delete_package_revisions(project_key, plugin_id)
        for revision in revisions:
            self._delete_retained_package(revision.local_path)
        return result

    async def describe_action(
        self, *, project_key: str, plugin_id: str, action_id: str, context: CommandContext
    ) -> ActionAvailability:
        runtime = self._require_runtime()
        if context.project_key != project_key:
            raise ValueError("command context project does not match request project")
        await self._refresh_and_emit_health(project_key, plugin_id)
        return runtime.describe(plugin_id, action_id, context)

    async def start_action(
        self,
        *,
        project_key: str,
        plugin_id: str,
        action_id: str,
        context: CommandContext,
        input_payload: dict[str, Any],
    ) -> PluginTaskSnapshot:
        runtime = self._require_runtime()
        if context.project_key != project_key:
            raise ValueError("command context project does not match request project")
        installation = await self._refresh_and_emit_health(project_key, plugin_id)
        task = await runtime.start(plugin_id, action_id, context, input_payload)
        self._task_started_at[task.task_id] = (datetime.now(UTC), time.monotonic())
        self._task_installations[task.task_id] = installation
        self._task_by_run[task.run_id] = task.task_id
        self._audit_snapshot(
            installation,
            "action.start",
            "succeeded",
            {
                "actionId": action_id,
                "runId": task.run_id,
                "risk": task.risk,
                "targetCollection": task.collection,
                "targetCount": task.target_count,
                "startedAt": self._task_started_at[task.task_id][0],
            },
        )
        return task

    async def resolve_interaction(
        self, *, run_id: str, interaction_id: str, decision: InteractionDecision
    ) -> InteractionResolveResult:
        result: InteractionResolveResult | None = None
        if self._local_confirmations is not None:
            local = await self._local_confirmations.try_resolve(run_id, interaction_id, decision)
            if local is not None:
                result = local
        if result is None:
            if self._interactions is None:
                raise RuntimeError("plugin interaction bridge is unavailable")
            result = await self._interactions.resolve(run_id, interaction_id, decision)
        resolved = cast(InteractionResolveResult, result)
        audit_key = (run_id, interaction_id)
        task_id = self._task_by_run.get(run_id)
        if task_id is not None and audit_key not in self._audited_interactions:
            task = self._require_runtime().get_task(task_id)
            installation = self._task_installations.get(task_id) or self._registry.get(
                task.project_key, task.plugin_id
            )
            if installation is not None:
                self._audited_interactions.add(audit_key)
                self._audit_snapshot(
                    installation,
                    "interaction.resolve",
                    resolved.decision or resolved.status,
                    {
                        "actionId": task.action_id,
                        "runId": run_id,
                        "interactionId": interaction_id,
                        "decision": resolved.decision,
                        "risk": task.risk,
                        "targetCollection": task.collection,
                        "targetCount": task.target_count,
                    },
                )
        return resolved

    async def resolve_file(self, *, request_id: str, selected_path: str | None) -> bool:
        if self._local_files is None:
            raise RuntimeError("plugin file picker is unavailable")
        return await self._local_files.resolve(request_id, selected_path)

    async def cancel_task(self, *, task_id: str) -> PluginTaskSnapshot:
        runtime = self._require_runtime()
        task = await runtime.request_cancel(task_id)
        self._audit_snapshot(
            self._installed(task.project_key, task.plugin_id),
            "action.cancel-requested",
            "succeeded",
            {"actionId": task.action_id, "runId": task.run_id},
        )
        return task

    def get_task(self, *, task_id: str) -> PluginTaskSnapshot:
        runtime = self._require_runtime()
        task = runtime.get_task(task_id)
        if (
            task.state in {"succeeded", "failed", "cancelled", "aborted"}
            and task_id not in self._audited_terminal_tasks
        ):
            self._audited_terminal_tasks.add(task_id)
            started = self._task_started_at.pop(task_id, None)
            timing: dict[str, Any] = {}
            if started is not None:
                timing = {
                    "startedAt": started[0],
                    "finishedAt": datetime.now(UTC),
                    "durationMs": max(0, round((time.monotonic() - started[1]) * 1000)),
                }
            installation = self._task_installations.pop(task_id, None) or self._registry.get(
                task.project_key, task.plugin_id
            )
            if installation is not None:
                self._audit_snapshot(
                    installation,
                    "action.end",
                    task.state,
                    {
                        "actionId": task.action_id,
                        "runId": task.run_id,
                        "risk": task.risk,
                        "targetCollection": task.collection,
                        "targetCount": task.target_count,
                        "errorCode": task.error.code if task.error is not None else None,
                        **timing,
                    },
                )
            self._task_by_run.pop(task.run_id, None)
        return task

    def _checked_plan(self, plan_id: str, project_revision: str) -> InstallPlan:
        plan = self._plans.get(plan_id)
        if plan is None:
            raise ValueError("install plan is missing or expired")
        base_revision = self._plan_base_revisions.get(plan_id)
        if base_revision is None or project_revision not in {
            base_revision,
            plan.project_revision,
        }:
            raise ValueError("project changed after plugin inspection")
        if self._project_revision(plan.project_key, base_revision) != plan.project_revision:
            raise ValueError("project changed after plugin inspection")
        return plan

    def _retain_package(self, plan: InstallPlan) -> Path:
        self._package_cache.mkdir(parents=True, exist_ok=True)
        destination = self._package_cache / f"{plan.package_hash.replace(':', '-')}.vtplugin"
        source = Path(plan.source_location)
        if source.is_dir():
            actual_hash = pack_plugin(source, destination)
        else:
            shutil.copy2(source, destination)
            actual_hash = inspect_plugin_package(destination).package_hash
        if actual_hash != plan.package_hash:
            destination.unlink(missing_ok=True)
            raise ValueError("retained package hash does not match inspection")
        return destination

    def _requirement(
        self, project_key: str, plugin_id: str, logical_flow_id: str
    ) -> FlowRequirement:
        installation = self._installed(project_key, plugin_id)
        requirement = next(
            (
                item
                for item in installation.flow_requirements
                if item.logical_flow_id == logical_flow_id
            ),
            None,
        )
        if requirement is None:
            raise ValueError("plugin does not declare the requested Flow")
        return requirement

    def _installed(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        installation = self._registry.get(project_key, plugin_id)
        if installation is None:
            raise ValueError("plugin is not installed")
        return installation

    def _with_bindings(self, snapshot: PluginSnapshot) -> PluginSnapshot:
        return snapshot.model_copy(
            update={
                "flow_bindings": self._store.list_bindings(
                    snapshot.project_key,
                    snapshot.plugin_id,
                )
            }
        )

    def _project_revision(self, project_key: str, base_revision: str) -> str:
        state: list[dict[str, Any]] = []
        for installation in self._registry.list(project_key):
            state.append(
                {
                    "pluginId": installation.plugin_id,
                    "revision": installation.revision,
                    "packageHash": installation.package_hash,
                    "status": installation.status,
                    "bindings": [
                        {
                            "logicalFlowId": item.logical_flow_id,
                            "revision": item.revision,
                            "flowUuid": item.directus_flow_uuid,
                            "health": item.health,
                        }
                        for item in sorted(
                            self._store.list_bindings(project_key, installation.plugin_id),
                            key=lambda binding: binding.logical_flow_id,
                        )
                    ],
                }
            )
        digest = hashlib.sha256(
            json.dumps(state, sort_keys=True, separators=(",", ":")).encode("utf-8")
        ).hexdigest()[:24]
        return f"{base_revision}:{digest}"

    async def _refresh_plugin_health(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        before = self._installed(project_key, plugin_id)
        before_bindings = {
            item.logical_flow_id: item.model_dump(mode="json")
            for item in self._store.list_bindings(project_key, plugin_id)
        }
        await self._bindings.detect_drift(project_key, plugin_id)
        for requirement in before.flow_requirements:
            if requirement.ownership == "external":
                await self._bindings.validate_external(
                    project_key=project_key,
                    plugin_id=plugin_id,
                    requirement=requirement,
                )
        snapshot = self._registry.refresh_status(project_key, plugin_id)
        after_bindings = {
            item.logical_flow_id: item.model_dump(mode="json")
            for item in self._store.list_bindings(project_key, plugin_id)
        }
        if after_bindings != before_bindings and snapshot.revision == before.revision:
            snapshot = self._registry.touch(project_key, plugin_id)
        if snapshot.status == "enabled":
            try:
                await self._bindings.set_automatic_enabled(project_key, plugin_id, True)
            except Exception:
                # A Directus failure can race the validation above. Refresh once
                # more so callers never receive an enabled snapshot with broken
                # automatic Flows.
                await self._bindings.detect_drift(project_key, plugin_id)
                snapshot = self._registry.refresh_status(project_key, plugin_id)
                if snapshot.status == "enabled":
                    raise
        else:
            with contextlib.suppress(Exception):
                await self._bindings.set_automatic_enabled(project_key, plugin_id, False)
        return snapshot

    async def _refresh_and_emit_health(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        before = self._with_bindings(self._installed(project_key, plugin_id))
        snapshot = await self._refresh_plugin_health(project_key, plugin_id)
        projected = self._with_bindings(snapshot)
        if projected.model_dump(mode="json") != before.model_dump(mode="json"):
            await self._emit_catalog(projected)
        return snapshot

    def _refresh_local_source(self, snapshot: PluginSnapshot) -> PluginSnapshot:
        source = snapshot.development_source_location
        if snapshot.source_type != "local-folder" or source is None:
            return snapshot
        changed = True
        with contextlib.suppress(Exception):
            changed = inspect_plugin_package(source).package_hash != snapshot.package_hash
        if changed == snapshot.source_changed:
            return snapshot
        updated = snapshot.model_copy(
            update={"source_changed": changed, "revision": snapshot.revision + 1}
        )
        return self._store.save_installation(updated, expected_revision=snapshot.revision)

    async def _watch_local_sources(self) -> None:
        while True:
            await asyncio.sleep(1.0)
            for project_key in tuple(self._known_projects):
                for snapshot in self._registry.list(project_key):
                    refreshed = self._refresh_local_source(snapshot)
                    if refreshed.revision != snapshot.revision:
                        await self._emit_catalog(self._with_bindings(refreshed))

    async def _emit_catalog(self, snapshot: PluginSnapshot) -> None:
        if self._notification_sink is None:
            return
        revision = self._catalog_event_revisions.get(snapshot.project_key, 0) + 1
        self._catalog_event_revisions[snapshot.project_key] = revision
        envelope = PluginEventEnvelope(
            event_type="plugin.catalog.changed",
            project_key=snapshot.project_key,
            entity_id=snapshot.plugin_id,
            revision=revision,
            snapshot=snapshot.model_dump(mode="json", by_alias=True),
        )
        with contextlib.suppress(Exception):
            await self._notification_sink(envelope)

    def _audit(
        self,
        plan: InstallPlan,
        event_type: str,
        outcome: str,
        details: dict[str, Any] | None = None,
    ) -> None:
        self._store.record_audit(
            PluginAuditEvent(
                event_id=f"audit-{uuid.uuid4().hex}",
                project_key=plan.project_key,
                plugin_id=plan.manifest.plugin_id,
                plugin_version=plan.manifest.version,
                package_hash=plan.package_hash,
                event_type=event_type,
                outcome=outcome,
                details=details or {},
            )
        )

    def _audit_snapshot(
        self,
        snapshot: PluginSnapshot,
        event_type: str,
        outcome: str,
        details: dict[str, Any] | None = None,
    ) -> None:
        metadata = details or {}
        self._store.record_audit(
            PluginAuditEvent(
                event_id=f"audit-{uuid.uuid4().hex}",
                project_key=snapshot.project_key,
                plugin_id=snapshot.plugin_id,
                plugin_version=snapshot.version,
                package_hash=snapshot.package_hash,
                event_type=event_type,
                outcome=outcome,
                action_id=metadata.get("actionId"),
                run_id=metadata.get("runId"),
                risk=metadata.get("risk"),
                target_collection=metadata.get("targetCollection"),
                target_count=metadata.get("targetCount"),
                started_at=metadata.get("startedAt", datetime.now(UTC)),
                finished_at=metadata.get("finishedAt"),
                duration_ms=metadata.get("durationMs"),
                error_code=metadata.get("errorCode"),
                details=metadata,
            )
        )

    def _require_runtime(self) -> PluginExecutionRuntime:
        if self._runtime is None:
            raise RuntimeError("plugin execution runtime is unavailable")
        return self._runtime

    def _delete_retained_package(self, local_path: str) -> None:
        if self._store.is_package_path_referenced(local_path):
            return
        path = Path(local_path)
        try:
            path.resolve().relative_to(self._package_cache.resolve())
        except (OSError, ValueError):
            return
        if path.is_file():
            path.unlink(missing_ok=True)

    def _prune_package_revisions(self, project_key: str, plugin_id: str) -> None:
        revisions = self._store.list_package_revisions(project_key, plugin_id)
        current = next((item for item in reversed(revisions) if item.state == "current"), None)
        rollback = next((item for item in reversed(revisions) if item.state == "rollback"), None)
        retained = {
            (item.version, item.package_hash) for item in (current, rollback) if item is not None
        }
        for revision in revisions:
            if (revision.version, revision.package_hash) in retained:
                continue
            self._store.delete_package_revision(
                project_key,
                plugin_id,
                revision.version,
                revision.package_hash,
            )
            self._delete_retained_package(revision.local_path)


def _inspect_plan(
    project_key: str,
    project_revision: str,
    source_location: str,
    *,
    plan_id: str | None = None,
) -> InstallPlan:
    inspected = inspect_plugin_package(source_location)
    manifest = PluginManifest.model_validate(inspected.manifest)
    schemas: dict[str, dict[str, Any]] = {}

    def load_json(path: str) -> dict[str, Any]:
        existing = schemas.get(path)
        if existing is not None:
            return existing
        value = json.loads(read_plugin_package_member(source_location, path).decode("utf-8"))
        if not isinstance(value, dict):
            raise ValueError(f"plugin member {path!r} must contain a JSON object")
        schemas[path] = value
        return value

    requirements: list[FlowRequirement] = []
    for raw in inspected.manifest.get("flows", []):
        input_path = raw.get("inputSchema")
        output_path = raw.get("outputSchema")
        definition_path = raw.get("definition")
        definition = (
            json.loads(read_plugin_package_member(source_location, definition_path).decode("utf-8"))
            if isinstance(definition_path, str)
            else None
        )
        requirements.append(
            FlowRequirement(
                logical_flow_id=raw["logicalFlowId"],
                ownership=raw["ownership"],
                trigger=raw.get("trigger", "manual"),
                risk=raw["risk"],
                contract_version=raw.get("contractVersion", "1.0"),
                requires_operations=raw.get("requiresOperations", []),
                input_schema=load_json(input_path) if isinstance(input_path, str) else {},
                output_schema=load_json(output_path) if isinstance(output_path, str) else {},
                definition=definition,
            )
        )
    for action in inspected.manifest.get("actions", []):
        for key in ("formSchema", "inputSchema", "outputSchema"):
            path = action.get(key)
            if isinstance(path, str):
                load_json(path)
    return InstallPlan(
        plan_id=plan_id or f"plan-{uuid.uuid4().hex}",
        project_key=project_key,
        project_revision=project_revision,
        source_type="local-folder" if Path(source_location).is_dir() else "package",
        source_location=source_location,
        package_hash=inspected.package_hash,
        manifest=manifest,
        flow_requirements=requirements,
        schemas=schemas,
    )


__all__ = ["PluginPlatformService"]
