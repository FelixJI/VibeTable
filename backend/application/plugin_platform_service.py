"""Closed application surface for local-worker plugins."""

from __future__ import annotations

import base64
import contextlib
import shutil
import uuid
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import Any, cast

from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.application.plugin_registry import PluginRegistry
from backend.contracts.plugin import (
    ActionAvailability,
    CommandContext,
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
from backend.infrastructure.plugin_package import inspect_plugin_package, pack_plugin

NotificationSink = Callable[[PluginEventEnvelope], Awaitable[None]]


class PluginPlatformService:
    """Coordinates package validation, registry state and local execution."""

    def __init__(
        self,
        *,
        registry: PluginRegistry,
        runtime: PluginExecutionRuntime,
        store: Any,
        package_cache: Path | None = None,
        confirmation_adapter: Any | None = None,
        file_adapter: Any | None = None,
        **_unused: Any,
    ) -> None:
        self._registry = registry
        self._runtime = runtime
        self._store = store
        default_cache = getattr(store, "package_cache", None)
        self._package_cache = Path(
            package_cache
            if package_cache is not None
            else default_cache
            if default_cache is not None
            else Path.cwd() / ".vibetable-plugin-cache"
        ).resolve()
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
        close = getattr(self._store, "close", None)
        if callable(close):
            close()

    async def inspect_install(
        self,
        *,
        project_key: str,
        project_revision: str,
        source_location: str,
    ) -> InstallPlan:
        source = Path(source_location).resolve()
        inspected = inspect_plugin_package(source)
        manifest = PluginManifest.model_validate(inspected.manifest)
        plan = InstallPlan(
            plan_id=f"plugin-plan-{uuid.uuid4().hex[:12]}",
            project_key=project_key,
            project_revision=project_revision,
            source_type="local-folder" if source.is_dir() else "package",
            source_location=str(source),
            package_hash=inspected.package_hash,
            manifest=manifest,
        )
        self._plans[plan.plan_id] = plan
        return plan

    async def commit_install(
        self,
        *,
        plan_id: str,
        project_revision: str,
    ) -> PluginSnapshot:
        plan = self._checked_plan(plan_id, project_revision)
        self._recheck_plan_source(plan)
        retained_path = self._retain_package(plan)
        installed: PluginSnapshot | None = None
        try:
            installed = await self._registry.install(plan)
            self._store.save_package_revision(
                PluginPackageRevision(
                    project_key=plan.project_key,
                    plugin_id=plan.manifest.plugin_id,
                    version=plan.manifest.version,
                    package_hash=plan.package_hash,
                    local_path=str(retained_path),
                    manifest=plan.manifest,
                    state="current",
                )
            )
        except Exception:
            if installed is not None:
                self._store.delete_installation(
                    installed.project_key,
                    installed.plugin_id,
                )
            self._delete_package_if_unreferenced(retained_path)
            raise
        self._plans.pop(plan_id, None)
        await self._emit_catalog(installed)
        return installed

    async def list_catalog(self, *, project_key: str) -> list[PluginSnapshot]:
        return self._registry.list(project_key)

    def list_audit(
        self,
        *,
        project_key: str,
        plugin_id: str,
    ) -> list[PluginAuditEvent]:
        return self._store.list_audit(project_key, plugin_id)

    def list_pending_cleanup(self, *, project_key: str) -> list[PluginAuditEvent]:
        del project_key
        return []

    async def set_enabled(
        self,
        *,
        project_key: str,
        plugin_id: str,
        enabled: bool,
    ) -> PluginSnapshot:
        snapshot = await self._registry.set_enabled(project_key, plugin_id, enabled)
        await self._emit_catalog(snapshot)
        return snapshot

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
            raise ValueError("upgrade plan identity does not match the installation")
        self._recheck_plan_source(plan)
        retained_path = self._retain_package(plan)
        previous = list(self._store.list_package_revisions(project_key, plugin_id))
        previous_installation = self._registry.get(project_key, plugin_id)
        snapshot: PluginSnapshot | None = None
        try:
            snapshot = await self._registry.commit_upgrade(plan)
            for revision in previous:
                if revision.state == "current":
                    self._store.save_package_revision(
                        revision.model_copy(update={"state": "rollback"})
                    )
            self._store.save_package_revision(
                PluginPackageRevision(
                    project_key=project_key,
                    plugin_id=plugin_id,
                    version=plan.manifest.version,
                    package_hash=plan.package_hash,
                    local_path=str(retained_path),
                    manifest=plan.manifest,
                    state="current",
                )
            )
            self._prune_package_revisions(project_key, plugin_id)
        except Exception:
            if snapshot is not None and previous_installation is not None:
                self._store.save_installation(
                    previous_installation,
                    expected_revision=snapshot.revision,
                )
            self._store.delete_package_revision(
                project_key,
                plugin_id,
                plan.package_hash,
            )
            for revision in previous:
                self._store.save_package_revision(revision)
            self._delete_package_if_unreferenced(retained_path)
            raise
        self._plans.pop(plan_id, None)
        await self._emit_catalog(snapshot)
        return snapshot

    async def rollback(self, *, project_key: str, plugin_id: str) -> PluginSnapshot:
        revisions = list(self._store.list_package_revisions(project_key, plugin_id))
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
        rollback_path = Path(rollback_revision.local_path).resolve()
        if not rollback_path.is_file():
            raise ValueError("plugin rollback package is missing")
        inspected = inspect_plugin_package(rollback_path)
        if (
            inspected.package_hash != rollback_revision.package_hash
            or PluginManifest.model_validate(inspected.manifest) != rollback_revision.manifest
        ):
            raise ValueError("plugin rollback package failed integrity verification")
        previous_installation = self._registry.get(project_key, plugin_id)
        if previous_installation is None:
            raise ValueError("plugin is not installed")
        plan = InstallPlan(
            plan_id=f"plugin-rollback-{uuid.uuid4().hex[:12]}",
            project_key=project_key,
            project_revision=str(previous_installation.revision),
            source_type="package",
            source_location=str(rollback_path),
            package_hash=rollback_revision.package_hash,
            manifest=rollback_revision.manifest,
            schemas=previous_installation.schemas,
        )
        snapshot: PluginSnapshot | None = None
        try:
            snapshot = await self._registry.commit_rollback(plan)
            self._store.save_package_revision(
                current_revision.model_copy(update={"state": "rollback"})
            )
            self._store.save_package_revision(
                rollback_revision.model_copy(update={"state": "current"})
            )
        except Exception:
            if snapshot is not None:
                self._store.save_installation(
                    previous_installation,
                    expected_revision=snapshot.revision,
                )
            for revision in revisions:
                self._store.save_package_revision(revision)
            raise
        await self._emit_catalog(snapshot)
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
            Path(revision.local_path)
            for revision in self._store.list_package_revisions(project_key, plugin_id)
        ]
        result = await self._registry.uninstall(
            project_key,
            plugin_id,
            cleanup_private_settings=cleanup_private_settings,
        )
        self._store.delete_package_revisions(project_key, plugin_id)
        for retained_path in retained_paths:
            self._delete_package_if_unreferenced(retained_path)
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
        return self._runtime.describe(plugin_id, action_id, context)

    async def start_action(
        self,
        *,
        project_key: str,
        plugin_id: str,
        action_id: str,
        context: CommandContext,
        input_payload: dict[str, Any],
    ) -> PluginTaskSnapshot:
        if context.project_key != project_key:
            raise ValueError("plugin context project does not match request")
        return await self._runtime.start(plugin_id, action_id, context, input_payload)

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

    async def resolve_file(self, *, request_id: str, selected_path: str | None) -> bool:
        resolver = getattr(self._file_adapter, "resolve", None)
        if not callable(resolver):
            return False
        return bool(
            await cast(
                Callable[[str, str | None], Awaitable[bool]],
                resolver,
            )(request_id, selected_path)
        )

    async def cancel_task(self, *, task_id: str) -> PluginTaskSnapshot:
        return await self._runtime.request_cancel(task_id)

    def get_task(self, *, task_id: str) -> PluginTaskSnapshot:
        return self._runtime.get_task(task_id)

    def _checked_plan(self, plan_id: str, project_revision: str) -> InstallPlan:
        try:
            plan = self._plans[plan_id]
        except KeyError as exc:
            raise ValueError("plugin install plan was not found") from exc
        if plan.project_revision != project_revision:
            raise ValueError("plugin project revision changed")
        return plan

    @staticmethod
    def _recheck_plan_source(plan: InstallPlan) -> None:
        checked = inspect_plugin_package(Path(plan.source_location).resolve())
        if checked.package_hash != plan.package_hash:
            raise ValueError("plugin source changed after inspection")

    def _retain_package(self, plan: InstallPlan) -> Path:
        digest = plan.package_hash.removeprefix("sha256:")
        # The package hash already binds the manifest (including pluginId), so
        # a flat content-addressed cache is collision-safe. Lowercase Base32
        # retains all 256 digest bits and remains unique on case-insensitive
        # Windows filesystems while saving 12 characters versus hex.
        compact_digest = (
            base64.b32encode(bytes.fromhex(digest)).rstrip(b"=").decode("ascii").lower()
        )
        destination = self._package_cache / f"{compact_digest}.vtplugin"
        destination.parent.mkdir(parents=True, exist_ok=True)
        if destination.is_file():
            if inspect_plugin_package(destination).package_hash != plan.package_hash:
                raise ValueError("retained plugin package failed integrity verification")
            return destination
        # Keep the transient component deliberately short. On Windows a
        # pytest/user cache root can already be close to MAX_PATH; repeating
        # the 64-character digest plus a UUID made an otherwise valid retained
        # package fail to open.
        temporary = destination.with_name(f".tmp-{uuid.uuid4().hex[:16]}")
        try:
            source = Path(plan.source_location).resolve()
            if source.is_dir():
                retained_hash = pack_plugin(source, temporary)
            else:
                shutil.copyfile(source, temporary)
                retained_hash = inspect_plugin_package(temporary).package_hash
            if retained_hash != plan.package_hash:
                raise ValueError("retained plugin package hash does not match the plan")
            temporary.replace(destination)
        finally:
            temporary.unlink(missing_ok=True)
        return destination

    def _prune_package_revisions(self, project_key: str, plugin_id: str) -> None:
        revisions = self._store.list_package_revisions(project_key, plugin_id)
        rollback = [revision for revision in revisions if revision.state == "rollback"]
        for retired in rollback[:-1]:
            self._store.delete_package_revision(
                project_key,
                plugin_id,
                retired.package_hash,
            )
            self._delete_package_if_unreferenced(Path(retired.local_path))

    def _delete_package_if_unreferenced(self, path: Path) -> None:
        if self._store.is_package_path_referenced(str(path)):
            return
        with contextlib.suppress(OSError):
            path.unlink()

    async def _emit_catalog(self, snapshot: PluginSnapshot) -> None:
        await self._emit(
            PluginEventEnvelope(
                event_type="plugin.catalog.changed",
                project_key=snapshot.project_key,
                entity_id=snapshot.plugin_id,
                revision=snapshot.revision,
                snapshot=snapshot.model_dump(mode="json", by_alias=True),
            )
        )

    async def _emit(self, event: PluginEventEnvelope) -> None:
        if self._notification_sink is not None:
            await self._notification_sink(event)


__all__ = ["PluginPlatformService"]
