"""Allowlisted adapter for PocketBase-owned internal product metadata."""

from __future__ import annotations

import hashlib
import json
import math
import uuid
from collections.abc import Mapping
from typing import Protocol

from pydantic import BaseModel

from backend.application.revisioned_metadata_port import (
    DashboardRevisionConflictError,
    JsonObject,
    JsonValue,
)
from backend.contracts.presets_versions_dashboards import (
    DashboardManagedConfig,
    DashboardPanelDraft,
    SaveDashboardDraftParams,
    SaveDashboardDraftResult,
)


class _InternalMetadataClient(Protocol):
    async def list_internal_metadata(self, namespace: str) -> JsonObject: ...

    async def upsert_internal_metadata(
        self,
        namespace: str,
        request: Mapping[str, JsonValue],
    ) -> JsonObject: ...

    async def delete_internal_metadata(
        self,
        namespace: str,
        request: Mapping[str, JsonValue],
    ) -> JsonObject: ...

    async def commit_dashboard_metadata(
        self,
        request: JsonObject,
    ) -> JsonObject: ...


_NAMESPACES = frozenset(
    {
        "shared_settings",
        "dashboards",
        "panels",
        "presets",
        "content_versions",
        "interfaces",
        "content_profiles",
        "record_document_links",
    }
)


class PocketBaseInternalMetadataPort:
    """Translate logical metadata operations to fixed sidecar use cases."""

    def __init__(
        self,
        *,
        client: _InternalMetadataClient,
    ) -> None:
        self._client = client

    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[JsonObject]:
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
        values: Mapping[str, JsonValue],
        expected_revision: str | None,
        idempotency_key: str,
    ) -> JsonObject:
        self._namespace(namespace)
        logical_id = record_id or _logical_id(values)
        current = await self._current_item(namespace, logical_id)
        revision = expected_revision
        if revision is None:
            revision = str(current.get("revision", "")) if current else ""
        payload = {
            key: value for key, value in (current or {}).items() if key not in {"id", "revision"}
        }
        payload.update(_json_object(values, "metadata values"))
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
    ) -> JsonObject:
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
        return _json_object(response, "metadata deletion")

    async def commit_dashboard(
        self,
        params: SaveDashboardDraftParams,
    ) -> SaveDashboardDraftResult:
        """Atomically persist one complete dashboard and its panel mutations."""

        dashboard_id = str(params.dashboard_id or uuid.uuid4())
        dashboards = await self.list_metadata("dashboards")
        panels = await self.list_metadata("panels")
        current_dashboard = next(
            (item for item in dashboards if item.get("id") == dashboard_id),
            None,
        )
        current_panels = [item for item in panels if item.get("dashboardId") == dashboard_id]
        expected_workspace = params.expected_revision
        if current_dashboard is None:
            if expected_workspace is not None:
                raise DashboardRevisionConflictError()
        else:
            current_workspace = _workspace(
                dashboard_id=dashboard_id,
                dashboard=current_dashboard,
                panels=current_panels,
                config=DashboardManagedConfig.model_validate(current_dashboard.get("config", {})),
            )
            if expected_workspace != current_workspace["revision"]:
                raise DashboardRevisionConflictError()

        current_panel_by_id = {str(item["id"]): item for item in current_panels if item.get("id")}
        panel_mutations: list[JsonObject] = []
        desired_panels: list[JsonObject] = []
        client_panel_ids: dict[str, str] = {}
        for panel_draft in params.panels:
            client_id = panel_draft.client_id
            panel_id = str(panel_draft.panel_id or uuid.uuid4())
            client_panel_ids[client_id] = panel_id
            panel = _draft_panel_payload(dashboard_id, panel_id, panel_draft)
            current = current_panel_by_id.get(panel_id)
            panel_mutations.append(
                {
                    "logicalId": panel_id,
                    "payload": panel,
                    "expectedRevision": (str(current.get("revision", "")) if current else ""),
                }
            )
            desired_panels.append(panel)

        delete_mutations: list[JsonObject] = []
        desired_ids = {str(item["id"]) for item in desired_panels}
        for raw_panel_id in params.deleted_panel_ids:
            panel_id = str(raw_panel_id)
            if panel_id in desired_ids:
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

        config = _remap_dashboard_panel_references(params.config, client_panel_ids, desired_ids)
        config_payload = _model_object(config)
        dashboard_payload: JsonObject = {
            "id": dashboard_id,
            "name": params.name,
            "note": params.note,
            "icon": params.icon,
            "color": params.color,
            "config": config_payload,
        }
        idempotency_key = str(params.idempotency_key)
        await self._client.commit_dashboard_metadata(
            _json_object(
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
                },
                "dashboard commit request",
            )
        )
        workspace = _workspace(
            dashboard_id=dashboard_id,
            dashboard=dashboard_payload,
            panels=desired_panels,
            config=config,
        )
        return SaveDashboardDraftResult.model_validate(
            {
                "workspace": workspace,
                "clientPanelIds": client_panel_ids,
                "atomic": True,
            }
        )

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
    ) -> JsonObject | None:
        items = await self.list_metadata(namespace, keys=[logical_id])
        return items[0] if items else None

    @staticmethod
    def _namespace(namespace: str) -> None:
        if namespace not in _NAMESPACES:
            raise ValueError(f"unknown internal metadata namespace {namespace!r}")


def _logical_id(values: Mapping[str, JsonValue]) -> str:
    for key in ("id", "key"):
        value = values.get(key)
        if isinstance(value, str) and value:
            return value
    raise ValueError("internal metadata logical id is required")


def _project_item(value: object) -> JsonObject:
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
    result = _json_object(payload, "internal metadata payload")
    result["id"] = logical_id
    result["revision"] = revision
    return result


def _panel_payload(
    dashboard_id: str,
    panel_id: str,
    source: Mapping[str, JsonValue],
) -> JsonObject:
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


def _draft_panel_payload(
    dashboard_id: str,
    panel_id: str,
    draft: DashboardPanelDraft,
) -> JsonObject:
    return {
        "id": panel_id,
        "dashboardId": dashboard_id,
        "name": draft.name,
        "note": draft.note or "",
        "icon": draft.icon,
        "color": draft.color,
        "showHeader": draft.show_header,
        "type": draft.type,
        "position": _model_object(draft.position),
        "options": _json_object(draft.options, "dashboard panel options"),
        "query": _model_object(draft.query) if draft.query is not None else {},
    }


def _remap_dashboard_panel_references(
    config: DashboardManagedConfig,
    client_panel_ids: Mapping[str, str],
    desired_panel_ids: set[str],
) -> DashboardManagedConfig:
    """Replace transient editor IDs before the Dashboard aggregate is persisted."""

    def panel_id(value: str, path: str) -> str:
        resolved = client_panel_ids.get(value, value)
        if resolved not in desired_panel_ids:
            raise ValueError(f"{path} references an unknown dashboard panel")
        return resolved

    global_filters = []
    for index, item in enumerate(config.global_filters):
        target_panels = [
            panel_id(value, f"dashboard config globalFilters[{index}].targetPanels")
            for value in item.target_panels
        ]
        field_bindings = {
            panel_id(value, f"dashboard config globalFilters[{index}].fieldBindings"): field
            for value, field in item.field_bindings.items()
        }
        global_filters.append(
            item.model_copy(
                update={"target_panels": target_panels, "field_bindings": field_bindings}
            )
        )

    interactions = [
        item.model_copy(
            update={
                "source_panel_id": panel_id(
                    item.source_panel_id,
                    f"dashboard config interactions[{index}].sourcePanelId",
                ),
                "target_panel_ids": [
                    panel_id(value, f"dashboard config interactions[{index}].targetPanelIds")
                    for value in item.target_panel_ids
                ],
            }
        )
        for index, item in enumerate(config.interactions)
    ]
    return config.model_copy(
        update={"global_filters": global_filters, "interactions": interactions}
    )


def _workspace(
    *,
    dashboard_id: str,
    dashboard: Mapping[str, JsonValue],
    panels: list[JsonObject],
    config: DashboardManagedConfig,
) -> JsonObject:
    normalized_config = _model_object(config)
    normalized_panels = [
        _panel_payload(dashboard_id, str(panel.get("id", "")), panel)
        for panel in panels
        if isinstance(panel.get("id"), str) and panel.get("id")
    ]
    normalized_panels.sort(key=lambda panel: str(panel["id"]))
    entry = _json_object(
        {
            "id": dashboard_id,
            "name": dashboard.get("name", ""),
            "note": dashboard.get("note", ""),
            "icon": dashboard.get("icon"),
            "color": dashboard.get("color"),
            "panels": normalized_panels,
        },
        "dashboard workspace entry",
    )
    revision = _digest(
        _json_object(
            {"dashboard": entry, "config": normalized_config},
            "dashboard revision payload",
        )
    )
    return _json_object(
        {
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
        },
        "dashboard workspace",
    )


def _digest(value: JsonValue) -> str:
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode()
    return hashlib.sha256(encoded).hexdigest()


def _model_object(value: BaseModel) -> JsonObject:
    return _json_object(value.model_dump(by_alias=True, mode="json"), "dashboard model")


def _json_object(value: object, label: str) -> JsonObject:
    frozen = _freeze(value)
    if not isinstance(frozen, dict):
        raise ValueError(f"{label} is not a JSON object")
    return frozen


def _freeze(value: object) -> JsonValue:
    if value is None or isinstance(value, (str, bool, int)):
        return value
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError("metadata value contains a non-finite number")
        return value
    if isinstance(value, list):
        return [_freeze(item) for item in value]
    if isinstance(value, Mapping):
        if not all(isinstance(key, str) for key in value):
            raise ValueError("JSON object keys must be strings")
        return {str(key): _freeze(item) for key, item in value.items()}
    raise ValueError("metadata value is not JSON-compatible")


__all__ = ["PocketBaseInternalMetadataPort"]
