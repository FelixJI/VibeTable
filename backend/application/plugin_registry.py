"""Project-local registry for installed local-worker plugins."""

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
    def get_installation(self, project_key: str, plugin_id: str) -> PluginSnapshot | None: ...

    def save_installation(
        self,
        snapshot: PluginSnapshot,
        *,
        expected_revision: int | None,
    ) -> PluginSnapshot: ...

    def list_installations(self, project_key: str) -> list[PluginSnapshot]: ...

    def delete_installation(self, project_key: str, plugin_id: str) -> bool: ...

    def delete_private_settings(self, project_key: str, plugin_id: str) -> int: ...

    def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent: ...

    def list_audit(self, project_key: str, plugin_id: str) -> list[PluginAuditEvent]: ...


class PluginRegistryError(Exception):
    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, str]:
        return {"code": self.code}


class PluginRegistry:
    """Owns one immutable local-worker installation per project/plugin id."""

    def __init__(self, *, store: PluginStore) -> None:
        self._store = store

    async def install(self, plan: InstallPlan) -> PluginSnapshot:
        plugin_id = plan.manifest.plugin_id
        if self._store.get_installation(plan.project_key, plugin_id) is not None:
            raise PluginRegistryError(
                "plugin is already installed",
                code="plugin_already_installed",
            )
        self._validate_plan(plan)
        snapshot = PluginSnapshot(
            project_key=plan.project_key,
            plugin_id=plugin_id,
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
        saved = self._store.save_installation(snapshot, expected_revision=None)
        self._audit(saved, "install", "succeeded")
        return saved

    def get(self, project_key: str, plugin_id: str) -> PluginSnapshot | None:
        return self._store.get_installation(project_key, plugin_id)

    def list(self, project_key: str) -> builtins.list[PluginSnapshot]:
        return self._store.list_installations(project_key)

    def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent:
        """Persist an execution/lifecycle audit event through the registry port."""
        return self._store.record_audit(event)

    def blocking_reasons(self, project_key: str, plugin_id: str) -> builtins.list[str]:
        snapshot = self._required(project_key, plugin_id)
        return list(snapshot.blocking_reasons)

    def touch(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        current = self._required(project_key, plugin_id)
        updated = current.model_copy(update={"revision": current.revision + 1})
        return self._store.save_installation(
            updated,
            expected_revision=current.revision,
        )

    async def set_enabled(
        self,
        project_key: str,
        plugin_id: str,
        enabled: bool,
    ) -> PluginSnapshot:
        current = self._required(project_key, plugin_id)
        if enabled and current.blocking_reasons:
            raise PluginRegistryError(
                "plugin has blocking reasons",
                code="plugin_blocked",
            )
        updated = current.model_copy(
            update={
                "status": "enabled" if enabled else "disabled",
                "disabled_reason": None if enabled else "disabled_by_user",
                "revision": current.revision + 1,
            }
        )
        saved = self._store.save_installation(
            updated,
            expected_revision=current.revision,
        )
        self._audit(saved, "enable" if enabled else "disable", "succeeded")
        return saved

    def refresh_status(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        return self._required(project_key, plugin_id)

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
        current = self._required(plan.project_key, plan.manifest.plugin_id)
        self._validate_plan(plan)
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
        saved = self._store.save_installation(
            updated,
            expected_revision=current.revision,
        )
        self._audit(saved, event_type, "succeeded")
        return saved

    async def uninstall(
        self,
        project_key: str,
        plugin_id: str,
        *,
        cleanup_private_settings: bool,
    ) -> UninstallResult:
        current = self._required(project_key, plugin_id)
        self._store.delete_installation(project_key, plugin_id)
        if cleanup_private_settings:
            self._store.delete_private_settings(project_key, plugin_id)
        self._audit(current, "uninstall", "succeeded")
        return UninstallResult(
            uninstalled=True,
            private_settings_retained=not cleanup_private_settings,
        )

    def _required(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        current = self._store.get_installation(project_key, plugin_id)
        if current is None:
            raise PluginRegistryError("plugin is not installed", code="plugin_not_found")
        return current

    @staticmethod
    def _validate_plan(plan: InstallPlan) -> None:
        if any(action.mode != "local" or not action.worker_entry for action in plan.manifest.actions):
            raise PluginRegistryError(
                "only local-worker actions are supported",
                code="plugin_manifest_legacy",
            )

    def _audit(self, snapshot: PluginSnapshot, event_type: str, outcome: str) -> None:
        now = datetime.now(UTC).replace(microsecond=0)
        self._store.record_audit(
            PluginAuditEvent(
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
        )


__all__ = ["PluginRegistry", "PluginRegistryError", "PluginStore"]
