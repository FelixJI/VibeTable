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

import asyncio
import json
import uuid
from copy import deepcopy
from typing import Any, cast
from urllib.parse import quote

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import (
    DirectusQueryError,
    DirectusSchemaError,
    DirectusTransportError,
)
from backend.adapters.directus.profile import CollectionProfile
from backend.adapters.directus.query import compile_directus_query
from backend.contracts.presets_versions_dashboards import (
    CompiledDashboardQuery,
    ContentVersionEntry,
    DashboardAggregateQuery,
    DashboardEntry,
    DashboardFilterState,
    DashboardFilterVariable,
    DashboardManagedConfig,
    DashboardPanelQuery,
    DashboardQueryLimits,
    DashboardQueryResult,
    DashboardRecordQuery,
    DashboardsResult,
    DashboardWorkspaceParams,
    DashboardWorkspaceResult,
    ExecuteDashboardQueryParams,
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
    SaveDashboardDraftParams,
    SaveDashboardDraftResult,
    VersionCompareResult,
    VersionsResult,
)
from backend.contracts.query import TableQuery

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
    PanelManifestEntry(
        type="bar",
        min_size=PanelPosition(x=0, y=0, width=4, height=3),
        options_schema={"collection": {"type": "string"}},
        renderer_version="1",
    ),
    PanelManifestEntry(
        type="line",
        min_size=PanelPosition(x=0, y=0, width=4, height=3),
        options_schema={"collection": {"type": "string"}},
        renderer_version="1",
    ),
    PanelManifestEntry(
        type="donut",
        min_size=PanelPosition(x=0, y=0, width=4, height=3),
        options_schema={"collection": {"type": "string"}},
        renderer_version="1",
    ),
    PanelManifestEntry(
        type="pie",
        min_size=PanelPosition(x=0, y=0, width=4, height=3),
        options_schema={"collection": {"type": "string"}},
        renderer_version="1",
    ),
]


_VIBETABLE_OPTIONS_KEY = "_vibetable"
_PRODUCT_TO_DIRECTUS_PANEL_TYPE: dict[str, str] = {
    "label": "label",
    "metric": "metric",
    "metric-list": "metric-list",
    "list": "list",
    "time-series": "time-series",
    "bar": "bar-chart",
    "line": "line-chart",
    "donut": "pie-chart",
    "pie": "pie-chart",
    "custom": "custom",
}

DASHBOARD_QUERY_LIMITS = DashboardQueryLimits()
_NUMERIC_FIELD_TYPES = frozenset({"integer", "bigInteger", "float", "decimal"})
_ORDERABLE_FIELD_TYPES = _NUMERIC_FIELD_TYPES | frozenset({"date", "dateTime", "timestamp", "time"})
_TEMPORAL_FIELD_TYPES = frozenset({"date", "dateTime", "timestamp"})


def compile_dashboard_query(
    query: DashboardPanelQuery,
    *,
    panel_type: PanelType,
    profile: CollectionProfile,
    field_types: dict[str, str],
) -> CompiledDashboardQuery:
    """Compile a typed dashboard query through capability and live-schema gates."""

    if not profile.allow_dashboards:
        raise InsightsError(
            f"collection {profile.collection!r} is not enabled as a dashboard data source",
            code="dashboard_collection_disabled",
        )
    if query.collection != profile.collection:
        raise InsightsError(
            "dashboard query collection does not match the selected capability profile",
            code="dashboard_collection_mismatch",
        )

    referenced: list[str] = []

    def require_field(field: str) -> str:
        if field not in profile.approved_fields or field not in field_types:
            raise InsightsError(
                f"field {field!r} is not available in collection {profile.collection!r}",
                code="dashboard_field_unavailable",
            )
        if field not in referenced:
            referenced.append(field)
        return field

    try:
        table_plan = compile_directus_query(
            TableQuery(filters=query.filters, limit=1),
            approved_fields=profile.approved_fields,
            primary_key=profile.primary_key,
        )
    except DirectusQueryError as exc:
        raise InsightsError(
            str(exc),
            code="dashboard_filter_invalid",
            field=exc.field,
        ) from exc
    for field in table_plan.referenced_fields:
        require_field(field)
    filter_tree = table_plan.params.get("filter")
    if profile.archive_field:
        require_field(profile.archive_field)
        archive_filter = {profile.archive_field: {"_neq": profile.archive_value}}
        filter_tree = (
            archive_filter if filter_tree is None else {"_and": [filter_tree, archive_filter]}
        )

    if isinstance(query, DashboardRecordQuery):
        fields = [require_field(field) for field in query.fields]
        try:
            record_plan = compile_directus_query(
                TableQuery(filters=query.filters, sorts=query.sorts, limit=query.limit),
                approved_fields=profile.approved_fields,
                primary_key=profile.primary_key,
            )
        except DirectusQueryError as exc:
            raise InsightsError(
                str(exc),
                code="dashboard_query_invalid",
                field=exc.field,
            ) from exc
        for field in record_plan.referenced_fields:
            require_field(field)
        record_params = dict(record_plan.params)
        record_params["fields"] = fields
        # Fetch one sentinel row beyond the visible cap so truncation is
        # truthful without a second count query.
        record_params["limit"] = min(query.limit, DASHBOARD_QUERY_LIMITS.max_list_rows) + 1
        if filter_tree is not None:
            record_params["filter"] = filter_tree
        return CompiledDashboardQuery(
            params=record_params,
            referenced_fields=referenced,
            max_rows=min(query.limit, DASHBOARD_QUERY_LIMITS.max_list_rows),
            max_points=min(query.limit, DASHBOARD_QUERY_LIMITS.max_list_rows),
        )

    if not isinstance(query, DashboardAggregateQuery):  # pragma: no cover - discriminated DTO
        raise InsightsError("unsupported dashboard query kind", code="dashboard_query_invalid")

    aggregate: dict[str, list[str]] = {}
    for measure in query.measures:
        measure_field = measure.field
        if measure_field is None:
            if measure.op != "count":
                raise InsightsError(
                    f"aggregate {measure.op!r} requires a field",
                    code="dashboard_measure_invalid",
                )
            storage_field = "*"
        else:
            storage_field = require_field(measure_field)
            field_type = field_types[measure_field]
            if measure.op in {"sum", "avg"} and field_type not in _NUMERIC_FIELD_TYPES:
                raise InsightsError(
                    f"aggregate {measure.op!r} requires a numeric field; {measure_field!r} is {field_type}",
                    code="dashboard_measure_type_invalid",
                )
            if measure.op in {"min", "max"} and field_type not in _ORDERABLE_FIELD_TYPES:
                raise InsightsError(
                    f"aggregate {measure.op!r} requires a numeric or temporal field",
                    code="dashboard_measure_type_invalid",
                )
        aggregate.setdefault(measure.op, []).append(storage_field)

    group_by = [require_field(field) for field in query.dimensions]
    if query.time_bucket is not None:
        bucket_field = require_field(query.time_bucket.field)
        if field_types[bucket_field] not in _TEMPORAL_FIELD_TYPES:
            raise InsightsError(
                f"time bucket requires a date/datetime field; {bucket_field!r} is not temporal",
                code="dashboard_time_bucket_invalid",
            )
        if query.time_bucket.timezone != "UTC":
            raise InsightsError(
                "Directus dashboard time bucketing currently supports UTC only",
                code="dashboard_time_zone_unsupported",
            )
        group_by.append(f"{query.time_bucket.unit}({bucket_field})")

    hard_cap = _panel_point_cap(
        panel_type,
        has_series_dimension=bool(query.dimensions),
        measure_count=len(query.measures),
    )
    max_points = min(query.limit, query.top_n or query.limit, hard_cap)
    # One sentinel group makes `truncated` observable. The extra row is never
    # returned to the renderer and does not change the advertised point cap.
    aggregate_params: dict[str, Any] = {"aggregate": aggregate, "limit": max_points + 1}
    if group_by:
        aggregate_params["groupBy"] = group_by
    if filter_tree is not None:
        aggregate_params["filter"] = filter_tree
    return CompiledDashboardQuery(
        params=aggregate_params,
        referenced_fields=referenced,
        max_rows=max_points,
        max_points=min(
            DASHBOARD_QUERY_LIMITS.max_panel_points,
            max_points * max(1, len(query.measures)),
        ),
    )


def _panel_point_cap(
    panel_type: PanelType, *, has_series_dimension: bool, measure_count: int
) -> int:
    measures = max(1, measure_count)
    total_row_cap = DASHBOARD_QUERY_LIMITS.max_panel_points // measures
    if panel_type in {"line", "time-series"}:
        # One aggregate row contributes one point per measure. With a declared
        # series dimension the total panel budget is the hard query-side guard;
        # without one, every measure is a single series and also receives the
        # per-series cap.
        return (
            total_row_cap
            if has_series_dimension
            else min(total_row_cap, DASHBOARD_QUERY_LIMITS.max_series_points)
        )
    if panel_type == "bar":
        return min(total_row_cap, DASHBOARD_QUERY_LIMITS.max_category_points)
    if panel_type in {"pie", "donut"}:
        return min(total_row_cap, DASHBOARD_QUERY_LIMITS.max_pie_slices)
    if panel_type in {"list", "metric-list"}:
        return DASHBOARD_QUERY_LIMITS.max_list_rows
    return 1


def to_directus_panel_type(
    panel_type: PanelType, options: dict[str, Any]
) -> tuple[str, dict[str, Any]]:
    """Map a VibeTable product type to a Directus-native panel type/options."""

    mapped_options = deepcopy(options)
    metadata = _dict_value(mapped_options.get(_VIBETABLE_OPTIONS_KEY))
    metadata["productType"] = panel_type
    mapped_options[_VIBETABLE_OPTIONS_KEY] = metadata
    preserved_directus_type = metadata.get("directusType")
    if panel_type == "custom" and isinstance(preserved_directus_type, str):
        return preserved_directus_type, mapped_options
    if panel_type == "donut":
        mapped_options["donut"] = True
    elif panel_type == "pie":
        mapped_options["donut"] = False
    return _PRODUCT_TO_DIRECTUS_PANEL_TYPE[panel_type], mapped_options


def from_directus_panel_type(
    directus_type: str, options: dict[str, Any]
) -> tuple[PanelType, dict[str, Any]]:
    """Map a Directus panel back to the safe VibeTable product vocabulary."""

    mapped_options = deepcopy(options)
    metadata = _dict_value(mapped_options.get(_VIBETABLE_OPTIONS_KEY))
    hinted = metadata.get("productType")
    product_type: PanelType
    if (
        isinstance(hinted, str)
        and hinted in _PRODUCT_TO_DIRECTUS_PANEL_TYPE
        and _PRODUCT_TO_DIRECTUS_PANEL_TYPE[hinted] == directus_type
    ):
        product_type = cast(PanelType, hinted)
    elif directus_type == "bar-chart":
        product_type = "bar"
    elif directus_type == "line-chart":
        product_type = "line"
    elif directus_type == "pie-chart":
        product_type = "donut" if mapped_options.get("donut") is True else "pie"
    elif directus_type in _PRODUCT_TO_DIRECTUS_PANEL_TYPE:
        product_type = cast(PanelType, directus_type)
    else:
        product_type = "custom"
        metadata["directusType"] = directus_type
        mapped_options[_VIBETABLE_OPTIONS_KEY] = metadata
    return product_type, mapped_options


class InsightsError(Exception):
    """An insights-flow error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str, field: str | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.field = field

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        data: dict[str, Any] = {"code": self.code}
        if self.field is not None:
            data["field"] = self.field
        return data


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
        self._query_slots = asyncio.Semaphore(DASHBOARD_QUERY_LIMITS.max_concurrent_requests)

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

    async def list_dashboards(self, _params: Any = None) -> DashboardsResult:
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
                    "icon",
                    "color",
                    "panels.id",
                    "panels.name",
                    "panels.note",
                    "panels.icon",
                    "panels.color",
                    "panels.show_header",
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
                    "icon",
                    "color",
                    "panels.id",
                    "panels.name",
                    "panels.note",
                    "panels.icon",
                    "panels.color",
                    "panels.show_header",
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

    async def read_dashboard_workspace(self, dashboard_id: str) -> DashboardWorkspaceResult:
        """Read the transaction endpoint's canonical dashboard revision."""

        token = await self._auth.access_token()
        try:
            payload = await self._transport.request(
                "GET",
                f"/vibetable-bulk-mutation/dashboard/{quote(dashboard_id, safe='')}",
                access_token=token,
            )
        except DirectusTransportError as exc:
            if exc.status == 404:
                if exc.code == "NOT_FOUND":
                    raise InsightsError(
                        f"dashboard {dashboard_id!r} was not found",
                        code="dashboard_not_found",
                    ) from exc
                raise InsightsError(
                    "the required atomic dashboard endpoint is not installed or the dashboard is unavailable",
                    code="dashboard_atomic_endpoint_unavailable",
                ) from exc
            raise
        result = _object(payload)
        dashboard_raw = result.get("dashboard")
        if not isinstance(dashboard_raw, dict):
            raise InsightsError(
                "atomic dashboard endpoint returned no dashboard snapshot",
                code="dashboard_atomic_response_invalid",
            )
        config = DashboardManagedConfig.model_validate(result.get("config") or {})
        return DashboardWorkspaceResult(
            dashboard=_dashboard_from_raw(dashboard_raw),
            config=config,
            revision=str(result.get("revision", "")),
            query_limits=DASHBOARD_QUERY_LIMITS,
        )

    async def save_dashboard_draft(
        self, params: SaveDashboardDraftParams
    ) -> SaveDashboardDraftResult:
        """Commit a complete draft through the required server-side atomic seam.

        This method deliberately has no client-side loop fallback. Cross-system-
        collection writes are only truthful when Directus confirms them from the
        versioned transaction endpoint.
        """

        if params.dashboard_id is not None:
            current = await self.read_dashboard_workspace(params.dashboard_id)
            if params.expected_revision is None:
                raise InsightsError(
                    "editing an existing dashboard requires expectedRevision",
                    code="dashboard_revision_required",
                )
            if current.revision != params.expected_revision:
                raise InsightsError(
                    "dashboard was changed by another editor",
                    code="dashboard_edit_conflict",
                )
            existing_ids = {panel.id for panel in current.dashboard.panels}
            submitted_ids = {
                panel.panel_id for panel in params.panels if panel.panel_id is not None
            }
            foreign = (submitted_ids | set(params.deleted_panel_ids)) - existing_ids
            if foreign:
                raise InsightsError(
                    f"panel {sorted(foreign)[0]!r} does not belong to dashboard {params.dashboard_id!r}",
                    code="dashboard_panel_membership_invalid",
                )
            new_panel_count = sum(panel.panel_id is None for panel in params.panels)
            final_panel_count = (
                len(existing_ids) - len(set(params.deleted_panel_ids)) + new_panel_count
            )
            if final_panel_count > 100:
                raise InsightsError(
                    "dashboard cannot contain more than 100 panels",
                    code="dashboard_panel_limit_exceeded",
                )

        panels: list[dict[str, Any]] = []
        for panel in params.panels:
            directus_type, options = to_directus_panel_type(panel.type, panel.options)
            metadata = _dict_value(options.get(_VIBETABLE_OPTIONS_KEY))
            if panel.query is not None:
                metadata["query"] = panel.query.model_dump(by_alias=True, mode="json")
            options[_VIBETABLE_OPTIONS_KEY] = metadata
            if (
                len(json.dumps(options, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))
                > 64 * 1024
            ):
                raise InsightsError(
                    f"panel {panel.client_id!r} options exceed 65536 bytes after query encoding",
                    code="dashboard_panel_options_too_large",
                )
            panels.append(
                {
                    "clientId": panel.client_id,
                    "panelId": panel.panel_id,
                    "name": panel.name,
                    "note": panel.note,
                    "icon": panel.icon,
                    "color": panel.color,
                    "showHeader": panel.show_header,
                    "type": directus_type,
                    "position": panel.position.model_dump(by_alias=True, mode="json"),
                    "options": options,
                }
            )

        token = await self._auth.access_token()
        body = {
            "contract": "vibetable-dashboard-atomic.v1",
            "dashboardId": params.dashboard_id,
            "expectedRevision": params.expected_revision,
            "idempotencyKey": params.idempotency_key,
            "dashboard": {
                "name": params.name,
                "note": params.note,
                "icon": params.icon,
                "color": params.color,
            },
            "panels": panels,
            "deletedPanelIds": params.deleted_panel_ids,
            "config": params.config.model_dump(by_alias=True, mode="json"),
        }
        try:
            payload = await self._transport.request(
                "POST",
                "/vibetable-bulk-mutation/dashboard/apply",
                access_token=token,
                json_body=body,
                headers={"Idempotency-Key": params.idempotency_key},
            )
        except DirectusTransportError as exc:
            if exc.status == 409 and exc.code == "DASHBOARD_EDIT_CONFLICT":
                raise InsightsError(
                    "dashboard was changed by another editor",
                    code="dashboard_edit_conflict",
                ) from exc
            if exc.status == 404:
                raise InsightsError(
                    "the required atomic dashboard endpoint is not installed",
                    code="dashboard_atomic_endpoint_unavailable",
                ) from exc
            raise
        result = _object(payload)
        dashboard_raw = result.get("dashboard")
        if not isinstance(dashboard_raw, dict):
            raise InsightsError(
                "atomic dashboard endpoint returned no dashboard snapshot",
                code="dashboard_atomic_response_invalid",
            )
        config = DashboardManagedConfig.model_validate(result.get("config") or {})
        workspace = DashboardWorkspaceResult(
            dashboard=_dashboard_from_raw(dashboard_raw),
            config=config,
            revision=str(result.get("revision", "")),
            query_limits=DASHBOARD_QUERY_LIMITS,
        )
        client_ids = result.get("clientPanelIds")
        return SaveDashboardDraftResult(
            workspace=workspace,
            client_panel_ids=(
                {str(key): str(value) for key, value in client_ids.items()}
                if isinstance(client_ids, dict)
                else {}
            ),
        )

    async def delete_dashboard_workspace(self, params: DashboardWorkspaceParams) -> dict[str, str]:
        """Delete the dashboard, its panels and managed config atomically."""

        token = await self._auth.access_token()
        try:
            payload = await self._transport.request(
                "DELETE",
                f"/vibetable-bulk-mutation/dashboard/{quote(params.dashboard_id, safe='')}",
                access_token=token,
            )
        except DirectusTransportError as exc:
            if exc.status == 404:
                if exc.code == "NOT_FOUND":
                    raise InsightsError(
                        f"dashboard {params.dashboard_id!r} was not found",
                        code="dashboard_not_found",
                    ) from exc
                raise InsightsError(
                    "the required atomic dashboard endpoint is not installed",
                    code="dashboard_atomic_endpoint_unavailable",
                ) from exc
            raise
        result = _object(payload)
        deleted = str(result.get("deleted", ""))
        if deleted != params.dashboard_id:
            raise InsightsError(
                "atomic dashboard endpoint returned an invalid delete result",
                code="dashboard_atomic_response_invalid",
            )
        return {"deleted": deleted}

    async def execute_dashboard_query(
        self, params: ExecuteDashboardQueryParams
    ) -> DashboardQueryResult:
        """Execute a schema-validated query under the six-request service limit."""

        profile = self._profile(params.query.collection)
        if not profile.allow_dashboards:
            raise InsightsError(
                f"collection {profile.collection!r} is not enabled as a dashboard data source",
                code="dashboard_collection_disabled",
            )
        fields = await self._client.fields(profile)
        field_types = {
            str(field["field"]): str(
                field.get("type")
                or (
                    field.get("schema", {}).get("data_type")
                    if isinstance(field.get("schema"), dict)
                    else "string"
                )
            )
            for field in fields
            if isinstance(field, dict) and isinstance(field.get("field"), str)
        }
        plan = compile_dashboard_query(
            params.query,
            panel_type=params.panel_type,
            profile=profile,
            field_types=field_types,
        )
        token = await self._auth.access_token()
        async with self._query_slots:
            payload = await self._transport.request(
                "GET",
                f"/items/{profile.collection}",
                access_token=token,
                query=plan.params,
                headers=(
                    {"X-Request-ID": params.request_id} if params.request_id is not None else None
                ),
            )
        rows = _list(payload)
        truncated = len(rows) > plan.max_rows
        visible_rows = rows[: plan.max_rows]
        if isinstance(params.query, DashboardAggregateQuery):
            visible_rows = _shape_dashboard_aggregate_rows(visible_rows, params.query)
            if params.panel_type in {"line", "time-series"} and params.query.dimensions:
                limited_rows = _limit_dashboard_series_rows(visible_rows, params.query.dimensions)
                truncated = truncated or len(limited_rows) < len(visible_rows)
                visible_rows = limited_rows
        return DashboardQueryResult(
            rows=visible_rows,
            truncated=truncated,
            max_points=plan.max_points,
        )

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
        directus_type, directus_options = to_directus_panel_type(panel_type, options)
        metadata = _dict_value(directus_options.get(_VIBETABLE_OPTIONS_KEY))
        metadata["query"] = deepcopy(query)
        directus_options[_VIBETABLE_OPTIONS_KEY] = metadata
        body = {
            "dashboard": dashboard_id,
            "name": name,
            "type": directus_type,
            "position_x": position.x,
            "position_y": position.y,
            "width": position.width,
            "height": position.height,
            "options": directus_options,
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
            options=directus_options,
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

    def dashboard_query_limits(self, _params: Any = None) -> DashboardQueryLimits:
        return DASHBOARD_QUERY_LIMITS

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


def _shape_dashboard_aggregate_rows(
    rows: list[dict[str, Any]], query: DashboardAggregateQuery
) -> list[dict[str, Any]]:
    """Project Directus aggregate buckets onto stable client measure keys."""

    shaped: list[dict[str, Any]] = []
    aggregate_names = {measure.op for measure in query.measures}
    for row in rows:
        projected = {key: value for key, value in row.items() if key not in aggregate_names}
        for measure in query.measures:
            aggregate_value = row.get(measure.op)
            if isinstance(aggregate_value, dict):
                if measure.field is not None and measure.field in aggregate_value:
                    value = aggregate_value[measure.field]
                elif "*" in aggregate_value:
                    value = aggregate_value["*"]
                else:
                    value = next(iter(aggregate_value.values()), None)
            else:
                value = aggregate_value
            projected[measure.key] = value
        shaped.append(projected)
    return shaped


def _limit_dashboard_series_rows(
    rows: list[dict[str, Any]], dimensions: list[str]
) -> list[dict[str, Any]]:
    """Enforce the per-series limit after Directus returns grouped rows."""

    counts: dict[tuple[str, ...], int] = {}
    visible: list[dict[str, Any]] = []
    for row in rows:
        key = tuple(str(row.get(field, "")) for field in dimensions)
        count = counts.get(key, 0)
        if count >= DASHBOARD_QUERY_LIMITS.max_series_points:
            continue
        counts[key] = count + 1
        visible.append(row)
    return visible


# ---------------------------------------------------------------------------
# Dashboard interactive filter (Task 7) — pure merge helpers
# ---------------------------------------------------------------------------


def merge_global_filter(
    panel_filter: dict[str, Any],
    global_state: DashboardFilterState,
    variables: list[DashboardFilterVariable],
    panel_id: str | None = None,
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
        if panel_id is not None:
            if var.target_panels and panel_id not in var.target_panels:
                continue
            bound = var.field_bindings.get(panel_id)
            fields = (
                [bound] if bound else (var.allowed_fields if len(var.allowed_fields) == 1 else [])
            )
        else:
            # Compatibility for the pure helper's pre-dashboard call sites.
            fields = var.allowed_fields
        for field in fields:
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
    panels: list[PanelEntry] = []
    for p in panels_raw:
        if not isinstance(p, dict):
            continue
        directus_options = _dict_value(p.get("options"))
        panel_type, options = from_directus_panel_type(
            str(p.get("type", "metric")), directus_options
        )
        metadata = _dict_value(options.get(_VIBETABLE_OPTIONS_KEY))
        panels.append(
            PanelEntry(
                id=str(p.get("id", "")),
                dashboard_id=str(raw.get("id", "")),
                name=str(p.get("name", "")),
                note=str(p.get("note") or ""),
                icon=(str(p["icon"]) if p.get("icon") is not None else None),
                color=(str(p["color"]) if p.get("color") is not None else None),
                show_header=bool(p.get("show_header", True)),
                type=panel_type,
                position=PanelPosition(
                    x=p.get("position_x", 0),
                    y=p.get("position_y", 0),
                    width=p.get("width", 4),
                    height=p.get("height", 4),
                ),
                options=options,
                query=_dict_value(metadata.get("query")),
            )
        )
    return DashboardEntry(
        id=str(raw.get("id", "")),
        name=str(raw.get("name", "")),
        note=str(raw.get("note", "")),
        icon=(str(raw["icon"]) if raw.get("icon") is not None else None),
        color=(str(raw["color"]) if raw.get("color") is not None else None),
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
    "from_directus_panel_type",
    "merge_global_filter",
    "resolve_selection_targets",
    "to_directus_panel_type",
]
