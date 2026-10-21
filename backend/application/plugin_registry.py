"""Project-local registry for installed code plugins."""

from __future__ import annotations

import builtins
import uuid
from datetime import UTC, datetime
from typing import Protocol

from backend.contracts.plugin import (
    InstallPlan,
    PluginAuditEvent,
    PluginSnapshot,
    UninstallResult,
)


class PluginStore(Protocol):
    async def set_enabled(
        self,
        project_key: str,
        plugin_id: str,
        enabled: bool,
        *,
        installation_id: str,
        expected_revision: int,
    ) -> PluginSnapshot: ...
    async def get_installation(self, project_key: str, plugin_id: str) -> PluginSnapshot | None: ...

    async def save_installation(
        self,
        snapshot: PluginSnapshot,
        *,
        expected_revision: int | None,
    ) -> PluginSnapshot: ...

    async def list_installations(self, project_key: str) -> list[PluginSnapshot]: ...

    async def delete_installation(
        self,
        project_key: str,
        plugin_id: str,
        *,
        installation_id: str,
        expected_revision: int,
        cleanup_private_settings: bool,
    ) -> bool: ...

    async def delete_private_settings(self, project_key: str, plugin_id: str) -> int: ...

    async def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent: ...

    async def list_audit(self, project_key: str, plugin_id: str) -> list[PluginAuditEvent]: ...


class PluginRegistryError(Exception):
    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, str]:
        return {"code": self.code}


def validate_install_plan(plan: InstallPlan) -> None:
    """Reject plans whose manifest leaves the closed execution contract."""

    if (
        plan.manifest.schema_id != "vibetable.plugin-manifest.v2"
        or plan.manifest.compatibility.get("pluginApi") != "3.x"
    ):
        raise PluginRegistryError(
            "旧 Worker 插件需要迁移到 API 3.x；原包与私有设置保持保全。",
            code="plugin_worker_retired",
        )
    if any(
        action.mode != "local" or action.worker_entry is not None
        for action in plan.manifest.actions
    ):
        raise PluginRegistryError(
            "plugin actions must use the isolated surface API",
            code="plugin_manifest_legacy",
        )
    if plan.blocking_reasons:
        raise PluginRegistryError("plugin plan has blocking reasons", code="plugin_blocked")


def build_installation_snapshot(plan: InstallPlan) -> PluginSnapshot:
    """Project the initial installation snapshot for one install commit."""

    validate_install_plan(plan)
    return PluginSnapshot(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        version=plan.manifest.version,
        package_hash=plan.package_hash,
        source_type=plan.source_type,
        source_location=plan.source_location,
        development_source_location=(
            plan.source_location if plan.source_type == "local-folder" else None
        ),
        manifest=plan.manifest,
        schemas=plan.schemas,
        status="disabled",
        disabled_reason="disabled_by_user",
        revision=1,
    )


def lifecycle_audit_event(
    snapshot: PluginSnapshot,
    event_type: str,
    *,
    outcome: str = "succeeded",
) -> PluginAuditEvent:
    """Build one lifecycle audit event for an installation transition."""

    now = datetime.now(UTC).replace(microsecond=0)
    return PluginAuditEvent(
        event_id=str(uuid.uuid4()),
        project_key=snapshot.project_key,
        plugin_id=snapshot.plugin_id,
        plugin_version=snapshot.version,
        package_hash=snapshot.package_hash,
        event_type=event_type,
        outcome=outcome,
        started_at=now,
        finished_at=now,
        duration_ms=0,
    )


class PluginRegistry:
    """Owns one immutable code-plugin installation per project/plugin id.

    Install commits are atomic store operations; this registry keeps the
    remaining lifecycle transitions (enable/disable, upgrade, rollback,
    uninstall).
    """

    def __init__(self, *, store: PluginStore) -> None:
        self._store = store

    async def get(self, project_key: str, plugin_id: str) -> PluginSnapshot | None:
        return await self._store.get_installation(project_key, plugin_id)

    async def list(self, project_key: str) -> builtins.list[PluginSnapshot]:
        return await self._store.list_installations(project_key)

    async def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent:
        """Persist an execution/lifecycle audit event through the registry port."""
        return await self._store.record_audit(event)

    async def blocking_reasons(self, project_key: str, plugin_id: str) -> builtins.list[str]:
        snapshot = await self._required(project_key, plugin_id)
        return list(snapshot.blocking_reasons)

    async def touch(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        current = await self._required(project_key, plugin_id)
        updated = current.model_copy(update={"revision": current.revision + 1})
        return await self._store.save_installation(
            updated,
            expected_revision=current.revision,
        )

    async def set_enabled(
        self,
        project_key: str,
        plugin_id: str,
        enabled: bool,
    ) -> PluginSnapshot:
        """Apply the enable/disable transition over the shared store.

        The public ``plugin.setEnabled`` entry is owned by the Go catalog;
        this internal path keeps the frozen revision/audit semantics for
        the closed Python tests and hidden lifecycle flows.
        """
        current = await self._required(project_key, plugin_id)
        if enabled and (
            current.manifest.schema_id != "vibetable.plugin-manifest.v2"
            or current.manifest.compatibility.get("pluginApi") != "3.x"
        ):
            raise PluginRegistryError(
                "旧 Worker 插件需要迁移到 API 3.x。", code="plugin_worker_retired"
            )
        if enabled and current.blocking_reasons:
            raise PluginRegistryError(
                "plugin has blocking reasons",
                code="plugin_blocked",
            )
        return await self._store.set_enabled(
            project_key,
            plugin_id,
            enabled,
            installation_id=current.installation_id or "",
            expected_revision=current.revision,
        )

    async def refresh_status(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        return await self._required(project_key, plugin_id)

    async def commit_upgrade(self, plan: InstallPlan) -> PluginSnapshot:
        return await self._commit_package_change(plan, event_type="upgrade")

    async def commit_rollback(self, plan: InstallPlan) -> PluginSnapshot:
        return await self._commit_package_change(plan, event_type="rollback")

    async def _commit_package_change(
        self,
        plan: InstallPlan,
        *,
        event_type: str,
    ) -> PluginSnapshot:
        current = await self._required(plan.project_key, plan.manifest.plugin_id)
        validate_install_plan(plan)
        updated = current.model_copy(
            update={
                "version": plan.manifest.version,
                "package_hash": plan.package_hash,
                "source_type": plan.source_type,
                "source_location": plan.source_location,
                "development_source_location": (
                    plan.source_location if plan.source_type == "local-folder" else None
                ),
                "manifest": plan.manifest,
                "schemas": plan.schemas,
                "source_changed": False,
                "revision": current.revision + 1,
            }
        )
        saved = await self._store.save_installation(
            updated,
            expected_revision=current.revision,
        )
        await self._audit(saved, event_type, "succeeded")
        return saved

    async def uninstall(
        self,
        project_key: str,
        plugin_id: str,
        *,
        cleanup_private_settings: bool,
        installation_id: str,
        expected_revision: int,
    ) -> UninstallResult:
        current = await self._required(project_key, plugin_id)
        if (
            not installation_id
            or current.installation_id != installation_id
            or current.revision != expected_revision
        ):
            raise PluginRegistryError(
                "plugin installation changed", code="plugin_revision_conflict"
            )
        await self._store.delete_installation(
            project_key,
            plugin_id,
            installation_id=installation_id,
            expected_revision=expected_revision,
            cleanup_private_settings=cleanup_private_settings,
        )
        return UninstallResult(
            uninstalled=True,
            private_settings_retained=not cleanup_private_settings,
        )

    async def _required(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        current = await self._store.get_installation(project_key, plugin_id)
        if current is None:
            raise PluginRegistryError("plugin is not installed", code="plugin_not_found")
        return current

    async def _audit(self, snapshot: PluginSnapshot, event_type: str, outcome: str) -> None:
        await self._store.record_audit(lifecycle_audit_event(snapshot, event_type, outcome=outcome))


__all__ = [
    "PluginRegistry",
    "PluginRegistryError",
    "PluginStore",
    "build_installation_snapshot",
    "lifecycle_audit_event",
    "validate_install_plan",
]
