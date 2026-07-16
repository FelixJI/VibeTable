"""C2 Presets + Content Versions + Insights Dashboards service.

Three related Directus-native content surfaces, all reading/writing system
collections (``directus_presets``, content versions, ``directus_dashboards``,
``directus_panels``) under the current user's permissions.

* **Presets** map shared business views; device-local details stay in B3 Grid
  State.
* **Content Versions** are unpublished working copies (distinct from Revisions).
* **Dashboards/Panels** implement the full Insights designer with a locked
  built-in panel manifest; custom panels are NOT executed.

The locked panel manifest (:data:`BUILT_IN_PANEL_MANIFEST`) pins the Directus
host version's built-in panel types so the renderer only executes known,
audited panel types. An unknown/``custom`` panel returns a safe-fallback entry.
"""

from __future__ import annotations

import uuid
from typing import Any

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusSchemaError
from backend.adapters.directus.profile import CollectionProfile
from backend.contracts.presets_versions_dashboards import (
    ContentVersionEntry,
    DashboardEntry,
    DashboardFilterState,
    DashboardFilterVariable,
    DashboardsResult,
    PanelEntry,
    PanelManifestEntry,
    PanelManifestResult,
    PanelPosition,
    PanelSelection,
    PanelType,
    PresetEntry,
    PresetScope,
    PresetsResult,
    PresetView,
    VersionCompareResult,
    VersionsResult,
)

#: The locked panel-manifest version (pins the Directus host compatibility).
PANEL_MANIFEST_VERSION = "dashboard-panel-manifest.v1"

#: The locked built-in panel manifest. The renderer only executes panels whose
#: ``type`` is in this list. A ``custom`` or unknown type gets a safe fallback.
BUILT_IN_PANEL_MANIFEST: list[PanelManifestEntry] = [
    PanelManifestEntry(
        type="label",
        min_size=PanelPosition(x=0, y=0, width=1, height=1),
        options_schema={"text": {"type": "string"}, "style": {"type": "string"}},
        renderer_version="1",
    ),
    PanelManifestEntry(
        type="metric",
        min_size=PanelPosition(x=0, y=0, width=2, height=2),
        options_schema={
            "collection": {"type": "string"},
            "field": {"type": "string"},
            "aggregate": {"type": "string"},
        },
        renderer_version="1",
    ),
    PanelManifestEntry(
        type="metric-list",
        min_size=PanelPosition(x=0, y=0, width=3, height=3),
        options_schema={
            "collection": {"type": "string"},
            "fields": {"type": "array"},
            "limit": {"type": "integer"},
        },
        renderer_version="1",
    ),
    PanelManifestEntry(
        type="list",
        min_size=PanelPosition(x=0, y=0, width=4, height=3),
        options_schema={
            "collection": {"type": "string"},
            "fields": {"type": "array"},
            "limit": {"type": "integer"},
        },
        renderer_version="1",
    ),
    PanelManifestEntry(
        type="time-series",
        min_size=PanelPosition(x=0, y=0, width=6, height=4),
        options_schema={
            "collection": {"type": "string"},
            "dateField": {"type": "string"},
            "valueField": {"type": "string"},
            "granularity": {"type": "string"},
        },
        renderer_version="1",
    ),
]


class InsightsError(Exception):
    """An insights-flow error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


class InsightsService:
    """C2 Presets + Content Versions + Dashboards surface."""

    def __init__(
        self,
        *,
        client: DirectusClient,
        auth: DirectusAuthBroker,
        profiles: dict[str, CollectionProfile],
        transport: Any,
    ) -> None:
        self._client = client
        self._auth = auth
        self._profiles = profiles
        self._transport = transport

    # ------------------------------------------------------------------
    # Presets (Task 4)
    # ------------------------------------------------------------------

    async def list_presets(self, collection: str) -> PresetsResult:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            "/presets",
            access_token=token,
            query={
                "filter": {"collection": {"_eq": collection}},
                "fields": ["id", "collection", "name", "scope", "user", "filter", "layout"],
                "limit": 100,
            },
        )
        raw = _list(payload)
        presets = [
            PresetEntry(
                id=str(p.get("id", "")),
                collection=str(p.get("collection", collection)),
                name=str(p.get("name", "")),
                scope=_preset_scope(p.get("scope")),
                view=PresetView(
                    filters=_filter_list(p.get("filter")),
                    sorts=[],
                    search="",
                ),
                user_id=str(p.get("user", "")) or None,
            )
            for p in raw
        ]
        return PresetsResult(collection=collection, presets=presets)

    async def save_preset(
        self,
        collection: str,
        name: str,
        view: PresetView,
        preset_id: str | None = None,
    ) -> PresetEntry:
        token = await self._auth.access_token()
        body: dict[str, Any] = {
            "collection": collection,
            "name": name,
            "filter": view.filters,
            "layout": view.layout,
        }
        if preset_id:
            payload = await self._transport.request(
                "PATCH", f"/presets/{preset_id}", access_token=token, json_body=body
            )
        else:
            payload = await self._transport.request(
                "POST", "/presets", access_token=token, json_body=body
            )
        saved = _object(payload)
        return PresetEntry(
            id=str(saved.get("id", preset_id or "")),
            collection=str(saved.get("collection", collection)),
            name=str(saved.get("name", name)),
        )

    async def delete_preset(self, preset_id: str) -> dict[str, Any]:
        token = await self._auth.access_token()
        await self._transport.request(
            "DELETE", f"/presets/{preset_id}", access_token=token, expected_status=(204,)
        )
        return {"deleted": preset_id}

    # ------------------------------------------------------------------
    # Content Versions (Task 5)
    # ------------------------------------------------------------------

    async def list_versions(self, collection: str, item_id: str) -> VersionsResult:
        profile = self._profile(collection)
        if not profile.allow_versions:
            raise InsightsError(
                f"collection {collection!r} does not have content versioning enabled",
                code="versions_not_enabled",
            )
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            "/versions",
            access_token=token,
            query={
                "filter": {
                    "collection": {"_eq": collection},
                    "item": {"_eq": item_id},
                },
                "fields": ["id", "key", "name", "outdated", "hash"],
                "limit": 50,
            },
        )
        raw = _list(payload)
        versions = [
            ContentVersionEntry(
                id=str(v.get("id", "")),
                key=str(v.get("key", "")),
                name=str(v.get("name", "")),
                outdated=bool(v.get("outdated", False)),
                main_hash=str(v.get("hash", "")),
            )
            for v in raw
        ]
        return VersionsResult(collection=collection, item_id=item_id, versions=versions)

    async def create_version(
        self, collection: str, item_id: str, key: str, name: str
    ) -> ContentVersionEntry:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "POST",
            "/versions",
            access_token=token,
            json_body={
                "collection": collection,
                "item": item_id,
                "key": key or str(uuid.uuid4())[:8],
                "name": name,
            },
        )
        saved = _object(payload)
        return ContentVersionEntry(
            id=str(saved.get("id", "")),
            key=str(saved.get("key", key)),
            name=str(saved.get("name", name)),
        )

    async def save_version(
        self, collection: str, item_id: str, version_id: str, values: dict[str, Any]
    ) -> dict[str, Any]:
        token = await self._auth.access_token()
        await self._transport.request(
            "PATCH",
            f"/versions/{version_id}",
            access_token=token,
            json_body={"data": values},
        )
        return {"saved": version_id}

    async def compare_version(
        self, collection: str, item_id: str, version_id: str
    ) -> VersionCompareResult:
        token = await self._auth.access_token()
        main_item = await self._client.read_item(self._profile(collection), item_id)
        version_payload = await self._transport.request(
            "GET",
            f"/versions/{version_id}",
            access_token=token,
            query={"fields": ["id", "outdated", "hash", "data"]},
        )
        version_data = _object(version_payload)
        version_values = version_data.get("data") or {}
        diffs: dict[str, dict[str, Any]] = {}
        for field, value in version_values.items():
            if main_item.get(field) != value:
                diffs[field] = {"main": main_item.get(field), "version": value}
        return VersionCompareResult(
            collection=collection,
            item_id=item_id,
            version_id=version_id,
            outdated=bool(version_data.get("outdated", False)),
            main_hash=str(version_data.get("hash", "")),
            differences=diffs,
        )

    async def promote_version(
        self, collection: str, item_id: str, version_id: str, main_hash: str
    ) -> dict[str, Any]:
        token = await self._auth.access_token()
        await self._transport.request(
            "POST",
            f"/versions/{version_id}/promote",
            access_token=token,
            json_body={"mainHash": main_hash},
        )
        return {"promoted": version_id}

    async def delete_version(
        self, collection: str, item_id: str, version_id: str
    ) -> dict[str, Any]:
        token = await self._auth.access_token()
        await self._transport.request(
            "DELETE",
            f"/versions/{version_id}",
            access_token=token,
            expected_status=(204,),
        )
        return {"deleted": version_id}

    # ------------------------------------------------------------------
    # Dashboards / Panels (Tasks 6-7)
    # ------------------------------------------------------------------

    async def list_dashboards(self) -> DashboardsResult:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            "/dashboards",
            access_token=token,
            query={
                "fields": [
                    "id",
                    "name",
                    "note",
                    "panels.id",
                    "panels.name",
                    "panels.icon",
                    "panels.color",
                    "panels.width",
                    "panels.height",
                    "panels.position_x",
                    "panels.position_y",
                    "panels.type",
                    "panels.options",
                ],
                "limit": 100,
            },
        )
        raw = _list(payload)
        dashboards = [_dashboard_from_raw(d) for d in raw]
        return DashboardsResult(dashboards=dashboards)

    async def read_dashboard(self, dashboard_id: str) -> DashboardEntry:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            f"/dashboards/{dashboard_id}",
            access_token=token,
            query={
                "fields": [
                    "id",
                    "name",
                    "note",
                    "panels.id",
                    "panels.name",
                    "panels.width",
                    "panels.height",
                    "panels.position_x",
                    "panels.position_y",
                    "panels.type",
                    "panels.options",
                ]
            },
        )
        return _dashboard_from_raw(_object(payload))

    async def save_dashboard(
        self, name: str, note: str, dashboard_id: str | None = None
    ) -> DashboardEntry:
        token = await self._auth.access_token()
        body = {"name": name, "note": note}
        if dashboard_id:
            payload = await self._transport.request(
                "PATCH", f"/dashboards/{dashboard_id}", access_token=token, json_body=body
            )
        else:
            payload = await self._transport.request(
                "POST", "/dashboards", access_token=token, json_body=body
            )
        saved = _object(payload)
        return DashboardEntry(
            id=str(saved.get("id", dashboard_id or "")),
            name=str(saved.get("name", name)),
            note=str(saved.get("note", note)),
            panels=[],
        )

    async def delete_dashboard(self, dashboard_id: str) -> dict[str, Any]:
        token = await self._auth.access_token()
        await self._transport.request(
            "DELETE", f"/dashboards/{dashboard_id}", access_token=token, expected_status=(204,)
        )
        return {"deleted": dashboard_id}

    async def save_panel(
        self,
        dashboard_id: str,
        name: str,
        panel_type: PanelType,
        position: PanelPosition,
        options: dict[str, Any],
        query: dict[str, Any],
        panel_id: str | None = None,
    ) -> PanelEntry:
        self._validate_panel_type(panel_type)
        token = await self._auth.access_token()
        body = {
            "dashboard": dashboard_id,
            "name": name,
            "type": panel_type,
            "position_x": position.x,
            "position_y": position.y,
            "width": position.width,
            "height": position.height,
            "options": options,
        }
        if panel_id:
            payload = await self._transport.request(
                "PATCH", f"/panels/{panel_id}", access_token=token, json_body=body
            )
        else:
            payload = await self._transport.request(
                "POST", "/panels", access_token=token, json_body=body
            )
        saved = _object(payload)
        return PanelEntry(
            id=str(saved.get("id", panel_id or "")),
            dashboard_id=dashboard_id,
            name=str(saved.get("name", name)),
            type=panel_type,
            position=position,
            options=options,
            query=query,
        )

    async def delete_panel(self, dashboard_id: str, panel_id: str) -> dict[str, Any]:
        token = await self._auth.access_token()
        await self._transport.request(
            "DELETE", f"/panels/{panel_id}", access_token=token, expected_status=(204,)
        )
        return {"deleted": panel_id}

    def panel_manifest(self) -> PanelManifestResult:
        return PanelManifestResult(
            manifest_version=PANEL_MANIFEST_VERSION,
            directus_compatibility=">=12 <13",
            panels=list(BUILT_IN_PANEL_MANIFEST),
        )

    def is_known_panel_type(self, panel_type: str) -> bool:
        """Whether ``panel_type`` is a locked built-in (executable) panel type."""
        return any(entry.type == panel_type for entry in BUILT_IN_PANEL_MANIFEST)

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    def _profile(self, collection: str) -> CollectionProfile:
        profile = self._profiles.get(collection)
        if profile is None:
            raise DirectusSchemaError(f"collection {collection!r} is not in capability manifest")
        return profile

    def _validate_panel_type(self, panel_type: PanelType) -> None:
        if panel_type == "custom":
            return
        if not self.is_known_panel_type(panel_type):
            raise InsightsError(
                f"panel type {panel_type!r} is not in the locked built-in manifest",
                code="panel_type_unknown",
            )


# ---------------------------------------------------------------------------
# Dashboard interactive filter (Task 7) — pure merge helpers
# ---------------------------------------------------------------------------


def merge_global_filter(
    panel_filter: dict[str, Any],
    global_state: DashboardFilterState,
    variables: list[DashboardFilterVariable],
) -> dict[str, Any]:
    """Merge dashboard-level filter values into a panel's filter via ``_and``.

    The user never injects raw Directus filter JSON; the merge is driven by the
    declared :class:`DashboardFilterVariable` list. Each variable's
    ``allowed_fields`` constrains where its value applies.
    """
    clauses: list[dict[str, Any]] = []
    if panel_filter:
        clauses.append(panel_filter)
    var_by_key = {v.key: v for v in variables}
    for key, value in global_state.values.items():
        var = var_by_key.get(key)
        if var is None or value is None:
            continue
        for field in var.allowed_fields:
            clauses.append({field: {"_eq": value}})
    if not clauses:
        return {}
    if len(clauses) == 1:
        return clauses[0]
    return {"_and": clauses}


def resolve_selection_targets(selection: PanelSelection, panels: list[PanelEntry]) -> list[str]:
    """Resolve which panels a typed selection should drive (cycle-safe).

    A panel never drives itself; targets not in the dashboard are dropped.
    """
    panel_ids = {p.id for p in panels}
    return [t for t in selection.target_panels if t in panel_ids and t != selection.panel_id]


# ---------------------------------------------------------------------------
# Module helpers
# ---------------------------------------------------------------------------


def _dashboard_from_raw(raw: dict[str, Any]) -> DashboardEntry:
    panels_raw = raw.get("panels") or []
    panels = [
        PanelEntry(
            id=str(p.get("id", "")),
            dashboard_id=str(raw.get("id", "")),
            name=str(p.get("name", "")),
            type=p.get("type", "metric"),
            position=PanelPosition(
                x=p.get("position_x", 0),
                y=p.get("position_y", 0),
                width=p.get("width", 4),
                height=p.get("height", 4),
            ),
            options=_dict_value(p.get("options")),
        )
        for p in panels_raw
        if isinstance(p, dict)
    ]
    return DashboardEntry(
        id=str(raw.get("id", "")),
        name=str(raw.get("name", "")),
        note=str(raw.get("note", "")),
        panels=panels,
    )


def _list(payload: Any) -> list[dict[str, Any]]:
    if isinstance(payload, dict) and isinstance(payload.get("data"), list):
        return [item for item in payload["data"] if isinstance(item, dict)]
    return []


def _preset_scope(value: Any) -> PresetScope:
    if value == "system":
        return "system"
    if value == "role":
        return "role"
    return "personal"


def _filter_list(value: Any) -> list[dict[str, Any]]:
    if not isinstance(value, list):
        return []
    return [item for item in value if isinstance(item, dict)]


def _dict_value(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict):
        return {}
    return {str(key): item for key, item in value.items()}


def _object(payload: Any) -> dict[str, Any]:
    if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
        return payload["data"]
    return {}


__all__ = [
    "BUILT_IN_PANEL_MANIFEST",
    "PANEL_MANIFEST_VERSION",
    "InsightsError",
    "InsightsService",
    "merge_global_filter",
    "resolve_selection_targets",
]
