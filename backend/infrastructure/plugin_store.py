"""Async plugin shared-state fake with the frozen atomic commit semantics."""

from __future__ import annotations

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


class PluginStoreConflictError(Exception):
    pass


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

    async def delete_installation(self, project_key: str, plugin_id: str) -> bool:
        return self._installations.pop((project_key, plugin_id), None) is not None

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
    ) -> PluginPrivateSetting:
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
        snapshot = build_installation_snapshot(plan)
        revision_key = (
            package_revision.project_key,
            package_revision.plugin_id,
            package_revision.package_hash,
        )
        self._installations[key] = snapshot
        self._revisions[revision_key] = package_revision
        self._audit.append(lifecycle_audit_event(snapshot, "install"))
        return snapshot


__all__ = [
    "InMemoryPluginStore",
    "PluginStoreConflictError",
]
