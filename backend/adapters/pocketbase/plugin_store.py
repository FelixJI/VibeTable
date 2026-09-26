"""Async plugin shared-state transport for the Go-owned PocketBase catalog."""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Protocol

from pydantic import JsonValue

from backend.adapters.pocketbase.client import PocketBaseProductError
from backend.application.plugin_registry import PluginRegistryError
from backend.contracts.plugin import (
    InstallPlan,
    PluginAuditEvent,
    PluginPackageRevision,
    PluginPrivateSetting,
    PluginSnapshot,
)
from backend.infrastructure.plugin_store import PluginStoreConflictError

_REVISION_CONFLICT_CODE = "plugin.revision_conflict"
_ALREADY_INSTALLED_CODE = "plugin_already_installed"
_NOT_FOUND_CODE = "plugin_not_found"
_BLOCKED_CODE = "plugin_blocked"


class _PluginStoreClient(Protocol):
    async def plugin_store(
        self,
        operation: str,
        request: Mapping[str, JsonValue],
    ) -> JsonValue: ...


def _translate(error: PocketBaseProductError) -> Exception:
    """Map the frozen public store errors; internal errors pass through."""

    if error.code == _REVISION_CONFLICT_CODE:
        return PluginStoreConflictError(str(error))
    if error.code in ("plugin.already_installed", _ALREADY_INSTALLED_CODE):
        return PluginRegistryError("plugin is already installed", code=_ALREADY_INSTALLED_CODE)
    if error.code in ("plugin.not_found", _NOT_FOUND_CODE):
        return PluginRegistryError("plugin is not installed", code=_NOT_FOUND_CODE)
    if error.code == _BLOCKED_CODE:
        return PluginRegistryError("plugin has blocking reasons", code=_BLOCKED_CODE)
    return error


class PocketBasePluginStore:
    """Async store over ``POST /api/vibetable/v1/plugins/store``.

    Every operation is one fixed request/response round trip with camelCase
    identity fields; ``commit_install`` is the single atomic install commit.
    No production SQLite path and no authoritative local cache is wired
    here: only the content-addressed package cache root is held for the
    composition root.
    """

    def __init__(self, *, client: _PluginStoreClient, package_cache: Path) -> None:
        self._client = client
        self.package_cache = package_cache

    async def _call(self, operation: str, request: Mapping[str, JsonValue]) -> JsonValue:
        try:
            return await self._client.plugin_store(operation, request)
        except PocketBaseProductError as error:
            raise _translate(error) from error

    async def get_installation(self, project_key: str, plugin_id: str) -> PluginSnapshot | None:
        result = await self._call(
            "get_installation",
            {"projectKey": project_key, "pluginId": plugin_id},
        )
        if result is None:
            return None
        return PluginSnapshot.model_validate(_object(result, "plugin installation"))

    async def list_installations(self, project_key: str) -> list[PluginSnapshot]:
        result = await self._call("list_installations", {"projectKey": project_key})
        return [
            PluginSnapshot.model_validate(_object(item, "plugin installation"))
            for item in _array(result, "plugin installations")
        ]

    async def save_installation(
        self,
        snapshot: PluginSnapshot,
        *,
        expected_revision: int | None,
    ) -> PluginSnapshot:
        result = await self._call(
            "save_installation",
            {
                "projectKey": snapshot.project_key,
                "pluginId": snapshot.plugin_id,
                "payload": snapshot.model_dump(mode="json", by_alias=True),
                "expectedRevision": expected_revision,
            },
        )
        return PluginSnapshot.model_validate(_object(result, "plugin installation"))

    async def delete_installation(self, project_key: str, plugin_id: str) -> bool:
        result = await self._call(
            "delete_installation",
            {"projectKey": project_key, "pluginId": plugin_id},
        )
        return _boolean(result, "installation deletion")

    async def list_package_revisions(
        self,
        project_key: str,
        plugin_id: str,
    ) -> list[PluginPackageRevision]:
        result = await self._call(
            "list_package_revisions",
            {"projectKey": project_key, "pluginId": plugin_id},
        )
        return [
            PluginPackageRevision.model_validate(_object(item, "plugin package revision"))
            for item in _array(result, "plugin package revisions")
        ]

    async def save_package_revision(
        self,
        revision: PluginPackageRevision,
    ) -> PluginPackageRevision:
        result = await self._call(
            "save_package_revision",
            {
                "projectKey": revision.project_key,
                "pluginId": revision.plugin_id,
                "payload": revision.model_dump(mode="json", by_alias=True),
            },
        )
        return PluginPackageRevision.model_validate(_object(result, "plugin package revision"))

    async def delete_package_revision(
        self,
        project_key: str,
        plugin_id: str,
        package_hash: str,
    ) -> bool:
        result = await self._call(
            "delete_package_revision",
            {"projectKey": project_key, "pluginId": plugin_id, "itemKey": package_hash},
        )
        return _boolean(result, "package revision deletion")

    async def delete_package_revisions(self, project_key: str, plugin_id: str) -> int:
        result = await self._call(
            "delete_package_revisions",
            {"projectKey": project_key, "pluginId": plugin_id},
        )
        return _integer(result, "package revisions deletion")

    async def is_package_path_referenced(self, local_path: str) -> bool:
        result = await self._call("is_package_path_referenced", {"localPath": local_path})
        return _boolean(result, "package path reference")

    async def get_private_setting(
        self,
        project_key: str,
        plugin_id: str,
        setting_key: str,
    ) -> PluginPrivateSetting | None:
        result = await self._call(
            "get_private_setting",
            {"projectKey": project_key, "pluginId": plugin_id, "itemKey": setting_key},
        )
        if result is None:
            return None
        return PluginPrivateSetting.model_validate(_object(result, "plugin private setting"))

    async def save_private_setting(
        self,
        setting: PluginPrivateSetting,
        *,
        expected_revision: int | None,
    ) -> PluginPrivateSetting:
        result = await self._call(
            "save_private_setting",
            {
                "projectKey": setting.project_key,
                "pluginId": setting.plugin_id,
                "payload": setting.model_dump(mode="json", by_alias=True),
                "expectedRevision": expected_revision,
            },
        )
        return PluginPrivateSetting.model_validate(_object(result, "plugin private setting"))

    async def delete_private_settings(self, project_key: str, plugin_id: str) -> int:
        result = await self._call(
            "delete_private_settings",
            {"projectKey": project_key, "pluginId": plugin_id},
        )
        return _integer(result, "private settings deletion")

    async def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent:
        result = await self._call(
            "record_audit",
            {
                "projectKey": event.project_key,
                "pluginId": event.plugin_id,
                "payload": event.model_dump(mode="json", by_alias=True),
            },
        )
        return PluginAuditEvent.model_validate(_object(result, "plugin audit event"))

    async def list_audit(self, project_key: str, plugin_id: str) -> list[PluginAuditEvent]:
        result = await self._call(
            "list_audit",
            {"projectKey": project_key, "pluginId": plugin_id},
        )
        return [
            PluginAuditEvent.model_validate(_object(item, "plugin audit event"))
            for item in _array(result, "plugin audit events")
        ]

    async def list_project_audit(self, project_key: str) -> list[PluginAuditEvent]:
        result = await self._call("list_project_audit", {"projectKey": project_key})
        return [
            PluginAuditEvent.model_validate(_object(item, "plugin audit event"))
            for item in _array(result, "plugin audit events")
        ]

    async def commit_install(
        self,
        plan: InstallPlan,
        *,
        package_revision: PluginPackageRevision,
    ) -> PluginSnapshot:
        result = await self._call(
            "commit_install",
            {
                "plan": plan.model_dump(mode="json", by_alias=True),
                "packageRevision": package_revision.model_dump(mode="json", by_alias=True),
            },
        )
        return PluginSnapshot.model_validate(_object(result, "plugin installation"))


def _object(value: object, label: str) -> dict[str, JsonValue]:
    if not isinstance(value, dict):
        raise ValueError(f"PocketBase returned an invalid {label}")
    return dict(value)


def _array(value: object, label: str) -> list[JsonValue]:
    if not isinstance(value, list):
        raise ValueError(f"PocketBase returned an invalid {label}")
    return list(value)


def _boolean(value: object, label: str) -> bool:
    if not isinstance(value, bool):
        raise ValueError(f"PocketBase returned an invalid {label}")
    return value


def _integer(value: object, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ValueError(f"PocketBase returned an invalid {label}")
    return value


__all__ = ["PocketBasePluginStore"]
