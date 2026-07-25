"""Allowlisted adapter for PocketBase-owned internal product metadata."""

from __future__ import annotations

import hashlib
import json
import uuid
from collections.abc import Mapping
from typing import Any, Protocol


class _InternalMetadataClient(Protocol):
    async def list_internal_metadata(self, namespace: str) -> dict[str, Any]: ...

    async def upsert_internal_metadata(
        self,
        namespace: str,
        request: Mapping[str, Any],
    ) -> dict[str, Any]: ...

    async def delete_internal_metadata(
        self,
        namespace: str,
        request: Mapping[str, Any],
    ) -> dict[str, Any]: ...

    async def commit_dashboard_metadata(
        self,
        request: Mapping[str, Any],
    ) -> dict[str, Any]: ...


_NAMESPACES = frozenset(
    {
        "shared_settings",
        "dashboards",
        "panels",
        "presets",
        "identifier_mappings",
        "content_versions",
    }
)


class PocketBaseInternalMetadataPort:
    """Translate logical metadata operations to fixed sidecar use cases."""

    def __init__(
        self,
        *,
        client: _InternalMetadataClient,
        schema_revisions: Mapping[str, str] | None = None,
    ) -> None:
        self._client = client
        # Retained only as a source-compatible constructor argument for older
        # composition tests. Internal collections no longer use schema routes.
        del schema_revisions

    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[dict[str, Any]]:
        self._namespace(namespace)
        response = await self._client.list_internal_metadata(namespace)
        raw_items = response.get("items")
        if not isinstance(raw_items, list):
            raise ValueError("internal metadata list returned invalid items")
        items = [_project_item(item) for item in raw_items]
        if scope is not None:
            items = [item for item in items if item.get("scope") == scope]
        if keys:
            wanted = set(keys)
            items = [
                item for item in items if item.get("id") in wanted or item.get("key") in wanted
            ]
        return sorted(
            items,
            key=lambda item: str(item.get("key") or item.get("id") or ""),
        )

    async def upsert_metadata(
        self,
        namespace: str,
        *,
        record_id: str | None,
        values: Mapping[str, Any],
        expected_revision: str | None,
        idempotency_key: str,
    ) -> dict[str, Any]:
        self._namespace(namespace)
        logical_id = record_id or _logical_id(values)
        current = await self._current_item(namespace, logical_id)
        revision = expected_revision
        if revision is None:
            revision = str(current.get("revision", "")) if current else ""
        payload = {
            key: value for key, value in (current or {}).items() if key not in {"id", "revision"}
        }
        payload.update(_freeze(values))
        response = await self._client.upsert_internal_metadata(
            namespace,
            {
                "logicalId": logical_id,
                "payload": payload,
                "expectedRevision": revision or "",
                "idempotencyKey": idempotency_key,
            },
        )
        item = response.get("item")
        if not isinstance(item, dict):
            raise ValueError("internal metadata mutation returned an invalid item")
        return {
            "status": response.get("status"),
            "changeSetId": response.get("changeSetId"),
            "emittedEvents": _freeze(response.get("emittedEvents", [])),
            "item": _project_item(item),
        }

    async def delete_metadata(
        self,
        namespace: str,
        *,
        record_id: str,
        expected_revision: str | None,
        idempotency_key: str,
    ) -> dict[str, Any]:
        self._namespace(namespace)
        revision = expected_revision
        if revision is None:
            revision = await self._current_revision(namespace, record_id)
        if not revision:
            raise ValueError("internal metadata record was not found")
        response = await self._client.delete_internal_metadata(
            namespace,
            {
                "logicalId": record_id,
                "expectedRevision": revision,
                "idempotencyKey": idempotency_key,
            },
        )
        return _freeze(response)

    async def commit_dashboard(self, payload: Mapping[str, Any]) -> dict[str, Any]:
        """Atomically persist one complete dashboard and its panel mutations."""

        frozen = _freeze(payload)
        dashboard_id = frozen.get("dashboardId") or str(uuid.uuid4())
        if not isinstance(dashboard_id, str) or not dashboard_id:
            raise ValueError("dashboardId is invalid")
        dashboards = await self.list_metadata("dashboards")
        panels = await self.list_metadata("panels")
        current_dashboard = next(
            (item for item in dashboards if item.get("id") == dashboard_id),
            None,
        )
        current_panels = [item for item in panels if item.get("dashboardId") == dashboard_id]
        expected_workspace = frozen.get("expectedRevision")
        if current_dashboard is None:
            if expected_workspace is not None:
                raise ValueError("dashboard revision does not match")
        else:
            current_workspace = _workspace(
                dashboard_id=dashboard_id,
                dashboard=current_dashboard,
                panels=current_panels,
                config=current_dashboard.get("config", {}),
            )
            if expected_workspace != current_workspace["revision"]:
                raise ValueError("dashboard revision does not match")

        current_panel_by_id = {str(item["id"]): item for item in current_panels if item.get("id")}
        panel_mutations: list[dict[str, Any]] = []
        desired_panels: list[dict[str, Any]] = []
        client_panel_ids: dict[str, str] = {}
        for raw_panel in frozen.get("panels", []):
            if not isinstance(raw_panel, dict):
                raise ValueError("dashboard panels must contain objects")
            client_id = raw_panel.get("clientId")
            if not isinstance(client_id, str) or not client_id:
                raise ValueError("dashboard panel clientId is invalid")
            panel_id = raw_panel.get("panelId") or str(uuid.uuid4())
            if not isinstance(panel_id, str) or not panel_id:
                raise ValueError("dashboard panel id is invalid")
            client_panel_ids[client_id] = panel_id
            panel = _panel_payload(dashboard_id, panel_id, raw_panel)
            current = current_panel_by_id.get(panel_id)
            panel_mutations.append(
                {
                    "logicalId": panel_id,
                    "payload": panel,
                    "expectedRevision": (str(current.get("revision", "")) if current else ""),
                }
            )
            desired_panels.append(panel)

        deleted = frozen.get("deletedPanelIds", [])
        if not isinstance(deleted, list):
            raise ValueError("deletedPanelIds must be an array")
        delete_mutations: list[dict[str, Any]] = []
        desired_ids = {str(item["id"]) for item in desired_panels}
        for panel_id in deleted:
            if not isinstance(panel_id, str) or not panel_id or panel_id in desired_ids:
                raise ValueError("deleted panel id is invalid")
            current = current_panel_by_id.get(panel_id)
            if current is None:
                raise ValueError("deleted panel was not found")
            delete_mutations.append(
                {
                    "logicalId": panel_id,
                    "expectedRevision": str(current.get("revision", "")),
                }
            )

        config = frozen.get("config")
        if not isinstance(config, dict):
            raise ValueError("dashboard config is invalid")
        dashboard_payload = {
            "id": dashboard_id,
            "name": frozen.get("name", ""),
            "note": frozen.get("note", ""),
            "icon": frozen.get("icon"),
            "color": frozen.get("color"),
            "config": config,
        }
        idempotency_key = frozen.get("idempotencyKey")
        if not isinstance(idempotency_key, str) or not idempotency_key:
            raise ValueError("dashboard idempotencyKey is invalid")
        await self._client.commit_dashboard_metadata(
            {
                "idempotencyKey": idempotency_key,
                "dashboard": {
                    "logicalId": dashboard_id,
                    "payload": dashboard_payload,
                    "expectedRevision": (
                        str(current_dashboard.get("revision", "")) if current_dashboard else ""
                    ),
                },
                "panels": panel_mutations,
                "deletePanels": delete_mutations,
            }
        )
        workspace = _workspace(
            dashboard_id=dashboard_id,
            dashboard=dashboard_payload,
            panels=desired_panels,
            config=config,
        )
        return {
            "workspace": workspace,
            "clientPanelIds": client_panel_ids,
            "atomic": True,
        }

    async def _current_revision(self, namespace: str, logical_id: str) -> str:
        item = await self._current_item(namespace, logical_id)
        if item is None:
            return ""
        revision = item.get("revision")
        if not isinstance(revision, str) or not revision:
            raise ValueError("internal metadata revision is invalid")
        return revision

    async def _current_item(
        self,
        namespace: str,
        logical_id: str,
    ) -> dict[str, Any] | None:
        items = await self.list_metadata(namespace, keys=[logical_id])
        return items[0] if items else None

    @staticmethod
    def _namespace(namespace: str) -> None:
        if namespace not in _NAMESPACES:
            raise ValueError(f"unknown internal metadata namespace {namespace!r}")


def _logical_id(values: Mapping[str, Any]) -> str:
    for key in ("id", "key"):
        value = values.get(key)
        if isinstance(value, str) and value:
            return value
    raise ValueError("internal metadata logical id is required")


def _project_item(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ValueError("internal metadata item is invalid")
    logical_id = value.get("logicalId")
    revision = value.get("revision")
    payload = value.get("payload")
    if (
        not isinstance(logical_id, str)
        or not logical_id
        or not isinstance(revision, str)
        or not revision
        or not isinstance(payload, dict)
    ):
        raise ValueError("internal metadata item is invalid")
    result = _freeze(payload)
    result["id"] = logical_id
    result["revision"] = revision
    return result


def _panel_payload(
    dashboard_id: str,
    panel_id: str,
    source: Mapping[str, Any],
) -> dict[str, Any]:
    query = source.get("query")
    return {
        "id": panel_id,
        "dashboardId": dashboard_id,
        "name": source.get("name", ""),
        "note": source.get("note") or "",
        "icon": source.get("icon"),
        "color": source.get("color"),
        "showHeader": source.get("showHeader") is not False,
        "type": source.get("type", "metric"),
        "position": source.get("position", {"x": 0, "y": 0, "width": 4, "height": 4}),
        "options": source.get("options", {}),
        "query": query if isinstance(query, dict) else {},
    }


def _workspace(
    *,
    dashboard_id: str,
    dashboard: Mapping[str, Any],
    panels: list[dict[str, Any]],
    config: Any,
) -> dict[str, Any]:
    normalized_config = config if isinstance(config, dict) else {}
    normalized_panels = [
        _panel_payload(dashboard_id, str(panel.get("id", "")), panel)
        for panel in panels
        if isinstance(panel.get("id"), str) and panel.get("id")
    ]
    normalized_panels.sort(key=lambda panel: str(panel["id"]))
    entry = {
        "id": dashboard_id,
        "name": dashboard.get("name", ""),
        "note": dashboard.get("note", ""),
        "icon": dashboard.get("icon"),
        "color": dashboard.get("color"),
        "panels": normalized_panels,
    }
    revision = _digest({"dashboard": entry, "config": normalized_config})
    return {
        "dashboard": entry,
        "config": _freeze(normalized_config),
        "revision": revision,
        "atomicSaveEndpoint": "vibetable-dashboard-atomic.v1",
        "queryLimits": {
            "maxConcurrentRequests": 6,
            "maxSeriesPoints": 50_000,
            "maxPanelPoints": 100_000,
            "maxCategoryPoints": 5_000,
            "defaultTopN": 100,
            "maxPieSlices": 50,
            "maxListRows": 100,
        },
    }


def _digest(value: Any) -> str:
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode()
    return hashlib.sha256(encoded).hexdigest()


def _freeze(value: Any) -> Any:
    return json.loads(
        json.dumps(
            value,
            ensure_ascii=False,
            allow_nan=False,
            sort_keys=True,
            separators=(",", ":"),
        )
    )


__all__ = ["PocketBaseInternalMetadataPort"]
