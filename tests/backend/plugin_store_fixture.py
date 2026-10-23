"""In-memory plugin store fake for tests of the Go-owned plugin catalog."""

from __future__ import annotations

import uuid

from backend.application.plugin_registry import (
    PluginRegistryError,
    build_installation_snapshot,
    lifecycle_audit_event,
)
from backend.contracts.plugin import (
    InstallPlan,
    PluginAuditEvent,
    PluginPackageRevision,
    PluginPrivateSetting,
    PluginSnapshot,
)
from backend.infrastructure.plugin_store import PluginStoreConflictError


class InMemoryPluginStore:
    """In-memory test fake for the Go-owned plugin shared catalog.

    ``commit_install`` mirrors the fixed atomic operation: installation
    identity, the current package revision and the install audit event are
    applied together, and a duplicate installation fails closed.
    """

    def __init__(self) -> None:
        self._installations: dict[tuple[str, str], PluginSnapshot] = {}
        self._revisions: dict[tuple[str, str, str], PluginPackageRevision] = {}
        self._settings: dict[tuple[str, str, str], PluginPrivateSetting] = {}
        self._audit: list[PluginAuditEvent] = []

    async def get_installation(self, project_key: str, plugin_id: str) -> PluginSnapshot | None:
        return self._installations.get((project_key, plugin_id))

    async def save_installation(
        self,
        snapshot: PluginSnapshot,
        *,
        expected_revision: int | None,
    ) -> PluginSnapshot:
        key = (snapshot.project_key, snapshot.plugin_id)
        current = self._installations.get(key)
        actual = None if current is None else current.revision
        if actual != expected_revision:
            raise PluginStoreConflictError(
                f"plugin revision mismatch: expected {expected_revision}, found {actual}"
            )
        self._installations[key] = snapshot
        return snapshot

    async def list_installations(self, project_key: str) -> list[PluginSnapshot]:
        return sorted(
            (
                snapshot
                for (candidate, _), snapshot in self._installations.items()
                if candidate == project_key
            ),
            key=lambda snapshot: snapshot.plugin_id,
        )

    async def set_enabled(
        self,
        project_key: str,
        plugin_id: str,
        enabled: bool,
        *,
        installation_id: str,
        expected_revision: int,
    ) -> PluginSnapshot:
        current = self._installations.get((project_key, plugin_id))
        if (
            current is None
            or not installation_id
            or current.installation_id != installation_id
            or current.revision != expected_revision
        ):
            raise PluginStoreConflictError("plugin installation changed")
        if enabled and current.blocking_reasons:
            raise PluginRegistryError("plugin has blocking reasons", code="plugin_blocked")
        updated = current.model_copy(
            update={
                "status": "enabled" if enabled else "disabled",
                "disabled_reason": None if enabled else "disabled_by_user",
                "revision": current.revision + 1,
            }
        )
        self._installations[(project_key, plugin_id)] = updated
        self._audit.append(lifecycle_audit_event(updated, "enable" if enabled else "disable"))
        return updated

    async def delete_installation(
        self,
        project_key: str,
        plugin_id: str,
        *,
        installation_id: str,
        expected_revision: int,
        cleanup_private_settings: bool,
    ) -> bool:
        current = self._installations.get((project_key, plugin_id))
        if (
            not installation_id
            or current is None
            or current.installation_id != installation_id
            or current.revision != expected_revision
        ):
            raise PluginStoreConflictError("plugin installation changed")
        del self._installations[(project_key, plugin_id)]
        await self.delete_package_revisions(project_key, plugin_id)
        if cleanup_private_settings:
            await self.delete_private_settings(project_key, plugin_id)
        self._audit.append(lifecycle_audit_event(current, "uninstall"))
        return True

    async def save_package_revision(
        self,
        revision: PluginPackageRevision,
    ) -> PluginPackageRevision:
        self._revisions[(revision.project_key, revision.plugin_id, revision.package_hash)] = (
            revision
        )
        return revision

    async def list_package_revisions(
        self,
        project_key: str,
        plugin_id: str,
    ) -> list[PluginPackageRevision]:
        return [
            revision
            for (project, plugin, _), revision in self._revisions.items()
            if project == project_key and plugin == plugin_id
        ]

    async def delete_package_revision(
        self,
        project_key: str,
        plugin_id: str,
        package_hash: str,
    ) -> bool:
        return self._revisions.pop((project_key, plugin_id, package_hash), None) is not None

    async def delete_package_revisions(self, project_key: str, plugin_id: str) -> int:
        keys = [key for key in self._revisions if key[:2] == (project_key, plugin_id)]
        for key in keys:
            del self._revisions[key]
        return len(keys)

    async def is_package_path_referenced(self, local_path: str) -> bool:
        return any(item.local_path == local_path for item in self._revisions.values())

    async def save_private_setting(
        self,
        setting: PluginPrivateSetting,
        *,
        expected_revision: int | None,
        installation_id: str | None = None,
        installation_revision: int | None = None,
    ) -> PluginPrivateSetting:
        if installation_id is not None:
            installation = self._installations.get((setting.project_key, setting.plugin_id))
            if (
                installation is None
                or installation.installation_id != installation_id
                or installation.revision != installation_revision
            ):
                raise PluginStoreConflictError("plugin installation changed")
        key = (setting.project_key, setting.plugin_id, setting.setting_key)
        current = self._settings.get(key)
        actual = None if current is None else current.revision
        if actual != expected_revision:
            raise PluginStoreConflictError(
                f"setting revision mismatch: expected {expected_revision}, found {actual}"
            )
        self._settings[key] = setting
        return setting

    async def get_private_setting(
        self,
        project_key: str,
        plugin_id: str,
        setting_key: str,
    ) -> PluginPrivateSetting | None:
        return self._settings.get((project_key, plugin_id, setting_key))

    async def delete_private_settings(self, project_key: str, plugin_id: str) -> int:
        keys = [key for key in self._settings if key[:2] == (project_key, plugin_id)]
        for key in keys:
            del self._settings[key]
        return len(keys)

    async def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent:
        self._audit.append(event)
        return event

    async def list_audit(self, project_key: str, plugin_id: str) -> list[PluginAuditEvent]:
        return [
            event
            for event in self._audit
            if event.project_key == project_key and event.plugin_id == plugin_id
        ]

    async def list_project_audit(self, project_key: str) -> list[PluginAuditEvent]:
        return [event for event in self._audit if event.project_key == project_key]

    async def commit_install(
        self,
        plan: InstallPlan,
        *,
        package_revision: PluginPackageRevision,
    ) -> PluginSnapshot:
        key = (plan.project_key, plan.manifest.plugin_id)
        if self._installations.get(key) is not None:
            raise PluginRegistryError(
                "plugin is already installed",
                code="plugin_already_installed",
            )
        # The fake mirrors the existing PB record Id, not a production identity authority.
        snapshot = build_installation_snapshot(plan).model_copy(
            update={"installation_id": str(uuid.uuid4())}
        )
        revision_key = (
            package_revision.project_key,
            package_revision.plugin_id,
            package_revision.package_hash,
        )
        self._installations[key] = snapshot
        self._revisions[revision_key] = package_revision
        self._audit.append(lifecycle_audit_event(snapshot, "install"))
        return snapshot

    async def commit_upgrade(
        self, plan: InstallPlan, *, package_revision: PluginPackageRevision
    ) -> PluginSnapshot:
        return await self._commit_package(plan, package_revision, "upgrade")

    async def commit_rollback(
        self, plan: InstallPlan, *, package_revision: PluginPackageRevision
    ) -> PluginSnapshot:
        return await self._commit_package(plan, package_revision, "rollback")

    async def _commit_package(
        self, plan: InstallPlan, package_revision: PluginPackageRevision, event_type: str
    ) -> PluginSnapshot:
        key = (plan.project_key, plan.manifest.plugin_id)
        current = self._installations.get(key)
        if (
            current is None
            or not plan.target_installation_id
            or current.installation_id != plan.target_installation_id
            or current.revision != plan.target_revision
        ):
            raise PluginStoreConflictError("plugin installation changed")
        updated = current.model_copy(
            update={
                "version": plan.manifest.version,
                "package_hash": plan.package_hash,
                "source_type": plan.source_type,
                "source_location": plan.source_location,
                "development_source_location": plan.source_location
                if plan.source_type == "local-folder"
                else None,
                "manifest": plan.manifest,
                "schemas": plan.schemas,
                "source_changed": False,
                "revision": current.revision + 1,
            }
        )
        for revision_key, revision in list(self._revisions.items()):
            if revision_key[:2] != key:
                continue
            if revision.state == "current":
                self._revisions[revision_key] = revision.model_copy(update={"state": "rollback"})
            else:
                del self._revisions[revision_key]
        self._revisions[(*key, package_revision.package_hash)] = package_revision
        self._installations[key] = updated
        self._audit.append(lifecycle_audit_event(updated, event_type))
        return updated


__all__ = ["InMemoryPluginStore"]
