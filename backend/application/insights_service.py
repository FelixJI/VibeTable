"""Provider-neutral presets, content versions, and dashboard orchestration.

The application layer addresses logical metadata namespaces and the product
QueryPort.  Physical PocketBase collection names and HTTP details belong only
to adapters.
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import math
import uuid
from collections.abc import Mapping
from typing import Protocol, TypeGuard

from pydantic import BaseModel

from backend.application.revisioned_metadata_port import (
    DashboardRevisionConflictError,
    JsonObject,
    JsonValue,
)
from backend.contracts.presets_versions_dashboards import (
    CompiledDashboardQuery,
    ContentVersionEntry,
    DashboardEntry,
    DashboardFilterState,
    DashboardFilterVariable,
    DashboardManagedConfig,
    DashboardPanelDraft,
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

PANEL_MANIFEST_VERSION = "dashboard-panel-manifest.v2"
_CHART_PANEL_TYPES: tuple[PanelType, ...] = ("bar", "line", "donut", "pie")
_EMPTY_OPTIONS_SCHEMA: JsonObject = {
    "type": "object",
    "properties": {},
    "additionalProperties": False,
}
_DRILLDOWN_OPTIONS_SCHEMA: JsonObject = {
    "type": "object",
    "properties": {
        "drilldownFields": {"type": "array", "items": {"type": "string"}},
    },
    "additionalProperties": False,
}
BUILT_IN_PANEL_MANIFEST: list[PanelManifestEntry] = [
    PanelManifestEntry(
        type="label",
        min_size=PanelPosition(x=0, y=0, width=1, height=1),
        options_schema={
            "type": "object",
            "properties": {"text": {"type": "string"}},
            "additionalProperties": False,
        },
        renderer_version="2",
    ),
    PanelManifestEntry(
        type="metric",
        min_size=PanelPosition(x=0, y=0, width=2, height=2),
        options_schema=_EMPTY_OPTIONS_SCHEMA,
        renderer_version="2",
    ),
    PanelManifestEntry(
        type="metric-list",
        min_size=PanelPosition(x=0, y=0, width=3, height=3),
        options_schema=_EMPTY_OPTIONS_SCHEMA,
        renderer_version="2",
    ),
    PanelManifestEntry(
        type="list",
        min_size=PanelPosition(x=0, y=0, width=4, height=3),
        options_schema=_DRILLDOWN_OPTIONS_SCHEMA,
        renderer_version="2",
    ),
    PanelManifestEntry(
        type="time-series",
        min_size=PanelPosition(x=0, y=0, width=6, height=4),
        options_schema={
            "type": "object",
            "properties": {
                "fillType": {"type": "string", "enum": ["solid", "gradient"]},
                "drilldownFields": {"type": "array", "items": {"type": "string"}},
            },
            "additionalProperties": False,
        },
        renderer_version="2",
    ),
    *[
        PanelManifestEntry(
            type=panel_type,
            min_size=PanelPosition(x=0, y=0, width=4, height=3),
            options_schema=(
                {
                    "type": "object",
                    "properties": {
                        "fillType": {"type": "string", "enum": ["solid", "gradient"]},
                        "drilldownFields": {"type": "array", "items": {"type": "string"}},
                    },
                    "additionalProperties": False,
                }
                if panel_type == "line"
                else _DRILLDOWN_OPTIONS_SCHEMA
            ),
            renderer_version="2",
        )
        for panel_type in _CHART_PANEL_TYPES
    ],
]
DASHBOARD_QUERY_LIMITS = DashboardQueryLimits()


class InternalMetadataPort(Protocol):
    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[JsonObject]: ...

    async def upsert_metadata(
        self,
        namespace: str,
        *,
        record_id: str | None,
        values: Mapping[str, JsonValue],
        expected_revision: str | None,
        idempotency_key: str,
    ) -> JsonObject: ...

    async def delete_metadata(
        self,
        namespace: str,
        *,
        record_id: str,
        expected_revision: str | None,
        idempotency_key: str,
    ) -> JsonObject: ...

    async def commit_dashboard(
        self,
        params: SaveDashboardDraftParams,
    ) -> SaveDashboardDraftResult: ...


class InsightsPage(Protocol):
    @property
    def rows(self) -> list[JsonObject]: ...


class InsightsQueryPort(Protocol):
    async def query_page(self, *, table_id: str, query: JsonObject) -> InsightsPage: ...

    async def aggregate(self, *, table_id: str, query: JsonObject) -> list[JsonObject]: ...

    async def read_history(
        self, *, collection: str, item_id: str, limit: int = 50
    ) -> JsonObject: ...

    async def preview_history_restore(
        self, *, collection: str, item_id: str, target_revision: str
    ) -> JsonObject: ...

    async def apply_history_restore(
        self, *, collection: str, item_id: str, token: str
    ) -> JsonObject: ...


class InsightsError(Exception):
    def __init__(self, message: str, *, code: str, field: str | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.field = field

    @property
    def rpc_error_data(self) -> JsonObject:
        result: JsonObject = {"code": self.code}
        if self.field is not None:
            result["field"] = self.field
        return result


class InsightsService:
    """Orchestrate insights through product ports only."""

    def __init__(
        self,
        *,
        metadata_port: InternalMetadataPort,
        query_port: InsightsQueryPort,
    ) -> None:
        self._metadata = metadata_port
        self._query = query_port
        self._query_slots = asyncio.Semaphore(DASHBOARD_QUERY_LIMITS.max_concurrent_requests)

    async def list_presets(self, collection: str) -> PresetsResult:
        rows = await self._metadata.list_metadata("presets", scope=collection)
        return PresetsResult(
            collection=collection,
            presets=[
                _preset(row, collection)
                for row in rows
                if _text(row.get("scope"), collection) == collection
            ],
        )

    async def save_preset(
        self,
        collection: str,
        name: str,
        view: PresetView,
        preset_id: str | None = None,
        operation_id: str = "",
    ) -> PresetEntry:
        if not operation_id:
            raise InsightsError("operationId is required", code="operation_id_required")
        record_id = preset_id or str(
            uuid.uuid5(uuid.NAMESPACE_URL, f"vibetable:preset:{operation_id}")
        )
        values: JsonObject = {
            "scope": collection,
            "name": name,
            "presetScope": "system",
            "view": _model_object(view),
        }
        receipt = await self._metadata.upsert_metadata(
            "presets",
            record_id=record_id,
            values=values,
            expected_revision=None,
            idempotency_key=f"preset:save:{operation_id}",
        )
        return PresetEntry(
            id=record_id,
            collection=collection,
            name=name,
            scope="system",
            view=view,
            revision=_receipt_revision(receipt),
            change_set_id=_optional_text(receipt.get("changeSetId")),
            emitted_events=_string_list(receipt.get("emittedEvents")),
        )

    async def delete_preset(
        self,
        preset_id: str,
        expected_revision: str,
        operation_id: str,
    ) -> JsonObject:
        receipt = await self._delete(
            "presets",
            preset_id,
            expected_revision=expected_revision,
            operation_id=operation_id,
            label="preset",
        )
        return {"deleted": preset_id, **_receipt_trace(receipt)}

    async def list_versions(self, collection: str, item_id: str) -> VersionsResult:
        rows = await self._metadata.list_metadata(
            "content_versions", scope=f"{collection}:{item_id}"
        )
        return VersionsResult(
            collection=collection,
            item_id=item_id,
            versions=[_version(row) for row in rows],
        )

    async def create_version(
        self,
        collection: str,
        item_id: str,
        key: str,
        name: str,
        operation_id: str = "",
    ) -> ContentVersionEntry:
        if not operation_id:
            raise InsightsError("operationId is required", code="operation_id_required")
        record_id = str(uuid.uuid5(uuid.NAMESPACE_URL, f"vibetable:version:{operation_id}"))
        change_set_id, revision_id = await self._latest_audit_revision(collection, item_id)
        preview = await self._query.preview_history_restore(
            collection=collection,
            item_id=item_id,
            target_revision=revision_id,
        )
        main_hash = _text(preview.get("currentHash"))
        entry = ContentVersionEntry(
            id=record_id,
            key=key or record_id[:8],
            name=name,
            main_hash=main_hash,
        )
        receipt = await self._metadata.upsert_metadata(
            "content_versions",
            record_id=record_id,
            values={
                **entry.model_dump(by_alias=True, mode="json"),
                "scope": f"{collection}:{item_id}",
                "changeSetId": change_set_id,
                "revisionId": revision_id,
            },
            expected_revision=None,
            idempotency_key=f"version:create:{operation_id}",
        )
        return entry.model_copy(
            update={
                "revision": _receipt_revision(receipt),
                "change_set_id": _optional_text(receipt.get("changeSetId")),
                "emitted_events": _string_list(receipt.get("emittedEvents")),
            }
        )

    async def save_version(
        self,
        collection: str,
        item_id: str,
        version_id: str,
        values: JsonObject,
        operation_id: str,
    ) -> JsonObject:
        if values:
            raise InsightsError(
                "content versions point to audit snapshots and do not accept arbitrary values",
                code="version_values_not_allowed",
            )
        change_set_id, revision_id = await self._latest_audit_revision(collection, item_id)
        preview = await self._query.preview_history_restore(
            collection=collection,
            item_id=item_id,
            target_revision=revision_id,
        )
        receipt = await self._metadata.upsert_metadata(
            "content_versions",
            record_id=version_id,
            values={
                "scope": f"{collection}:{item_id}",
                "changeSetId": change_set_id,
                "revisionId": revision_id,
                "mainHash": _text(preview.get("currentHash")),
            },
            expected_revision=None,
            idempotency_key=f"version:save:{operation_id}",
        )
        return {
            "saved": version_id,
            "changeSetId": change_set_id,
            "revisionId": revision_id,
            "metadataRevision": _receipt_revision(receipt),
            **_receipt_trace(receipt),
        }

    async def compare_version(
        self, collection: str, item_id: str, version_id: str
    ) -> VersionCompareResult:
        rows = await self._metadata.list_metadata(
            "content_versions", scope=f"{collection}:{item_id}", keys=[version_id]
        )
        if not rows:
            raise InsightsError("content version was not found", code="version_not_found")
        version = rows[0]
        revision_id = version.get("revisionId")
        if not isinstance(revision_id, str) or not revision_id:
            raise InsightsError(
                "content version has no audit revision",
                code="version_revision_missing",
            )
        preview = await self._query.preview_history_restore(
            collection=collection,
            item_id=item_id,
            target_revision=revision_id,
        )
        differences: dict[str, JsonObject] = {}
        scalar_changes = preview.get("scalarChanges", [])
        if isinstance(scalar_changes, list):
            for change in scalar_changes:
                if not isinstance(change, dict):
                    continue
                field = change.get("field")
                if isinstance(field, str):
                    differences[field] = {
                        "main": change.get("before"),
                        "version": change.get("after"),
                    }
        relation_changes = preview.get("relationChanges", [])
        if isinstance(relation_changes, list):
            for change in relation_changes:
                if not isinstance(change, dict):
                    continue
                field = change.get("field")
                if isinstance(field, str):
                    differences[field] = {
                        "main": change.get("beforeItemId"),
                        "version": change.get("afterItemId"),
                    }
        current_hash = _text(preview.get("currentHash"))
        return VersionCompareResult(
            collection=collection,
            item_id=item_id,
            version_id=version_id,
            outdated=current_hash != _text(version.get("mainHash")),
            main_hash=current_hash,
            differences=differences,
        )

    async def promote_version(
        self,
        collection: str,
        item_id: str,
        version_id: str,
        main_hash: str,
        operation_id: str,
    ) -> JsonObject:
        if not operation_id:
            raise InsightsError("operationId is required", code="operation_id_required")
        rows = await self._metadata.list_metadata(
            "content_versions", scope=f"{collection}:{item_id}", keys=[version_id]
        )
        if not rows:
            raise InsightsError("content version was not found", code="version_not_found")
        revision_id = rows[0].get("revisionId")
        if not isinstance(revision_id, str) or not revision_id:
            raise InsightsError(
                "content version has no audit revision",
                code="version_revision_missing",
            )
        preview = await self._query.preview_history_restore(
            collection=collection,
            item_id=item_id,
            target_revision=revision_id,
        )
        if _text(preview.get("currentHash")) != main_hash:
            raise InsightsError(
                "main item changed after the version comparison",
                code="version_main_conflict",
            )
        token = preview.get("token")
        if not isinstance(token, str) or not token or preview.get("canApply") is not True:
            raise InsightsError(
                "content version cannot be promoted",
                code="version_not_restorable",
            )
        result = await self._query.apply_history_restore(
            collection=collection,
            item_id=item_id,
            token=token,
        )
        return {
            "promoted": version_id,
            "restoredToRevision": revision_id,
            "result": _freeze(result),
        }

    async def delete_version(
        self,
        collection: str,
        item_id: str,
        version_id: str,
        expected_revision: str,
        operation_id: str,
    ) -> JsonObject:
        del collection, item_id
        receipt = await self._delete(
            "content_versions",
            version_id,
            expected_revision=expected_revision,
            operation_id=operation_id,
            label="version",
        )
        return {"deleted": version_id, **_receipt_trace(receipt)}

    async def _latest_audit_revision(
        self,
        collection: str,
        item_id: str,
    ) -> tuple[str, str]:
        page = await self._query.read_history(
            collection=collection,
            item_id=item_id,
            limit=1,
        )
        change_sets = page.get("changeSets")
        if not isinstance(change_sets, list) or not change_sets:
            raise InsightsError(
                "an audit revision is required before naming a content version",
                code="version_audit_missing",
            )
        change_set = change_sets[0]
        if not isinstance(change_set, dict):
            raise InsightsError(
                "audit history returned an invalid change set",
                code="version_audit_invalid",
            )
        change_set_id = change_set.get("changeSetId")
        if not isinstance(change_set_id, str) or not change_set_id:
            raise InsightsError(
                "audit change set has no identity",
                code="version_audit_invalid",
            )
        root_revision_id = change_set.get("rootRevisionId")
        revision_id = (
            root_revision_id if isinstance(root_revision_id, str) and root_revision_id else ""
        )
        record_changes = change_set.get("recordChanges")
        if isinstance(record_changes, list):
            for change in record_changes:
                candidate_revision = change.get("revisionId") if isinstance(change, dict) else None
                if (
                    isinstance(change, dict)
                    and change.get("itemId") == item_id
                    and isinstance(candidate_revision, str)
                    and candidate_revision
                ):
                    revision_id = candidate_revision
                    break
        return change_set_id, revision_id

    async def list_dashboards(self, _params: object | None = None) -> DashboardsResult:
        dashboards, panels = await asyncio.gather(
            self._metadata.list_metadata("dashboards"),
            self._metadata.list_metadata("panels"),
        )
        return DashboardsResult(dashboards=[_dashboard(row, panels) for row in dashboards])

    async def read_dashboard(self, dashboard_id: str) -> DashboardEntry:
        result = await self.list_dashboards()
        for dashboard in result.dashboards:
            if dashboard.id == dashboard_id:
                return dashboard
        raise InsightsError("dashboard was not found", code="dashboard_not_found")

    async def read_dashboard_workspace(self, dashboard_id: str) -> DashboardWorkspaceResult:
        dashboard = await self.read_dashboard(dashboard_id)
        rows = await self._metadata.list_metadata("dashboards", keys=[dashboard_id])
        config_raw = rows[0].get("config") if rows else {}
        config = DashboardManagedConfig.model_validate(config_raw or {})
        revision = _revision(
            {
                "dashboard": dashboard.model_dump(by_alias=True, mode="json"),
                "config": config.model_dump(by_alias=True, mode="json"),
            }
        )
        return DashboardWorkspaceResult(
            dashboard=dashboard,
            config=config,
            revision=revision,
            query_limits=DASHBOARD_QUERY_LIMITS,
        )

    async def save_dashboard_draft(
        self, params: SaveDashboardDraftParams
    ) -> SaveDashboardDraftResult:
        for panel in params.panels:
            _validate_panel_manifest(panel)
        try:
            return await self._metadata.commit_dashboard(params)
        except DashboardRevisionConflictError as error:
            raise InsightsError(
                "dashboard revision does not match",
                code="dashboard_edit_conflict",
            ) from error

    async def delete_dashboard_workspace(self, params: DashboardWorkspaceParams) -> dict[str, str]:
        await self.delete_dashboard(params.dashboard_id)
        return {"deleted": params.dashboard_id}

    async def execute_dashboard_query(
        self, params: ExecuteDashboardQueryParams
    ) -> DashboardQueryResult:
        if params.panel_type == "custom" or not self.is_known_panel_type(params.panel_type):
            raise InsightsError(
                "panel type cannot execute queries",
                code="dashboard_panel_type_unknown",
            )
        async with self._query_slots:
            if isinstance(params.query, DashboardRecordQuery):
                page = await self._query.query_page(
                    table_id=params.query.collection,
                    query={
                        "keyword": None,
                        "filters": [
                            item.model_dump(by_alias=True, mode="json")
                            for item in params.query.filters
                        ],
                        "sorts": [
                            item.model_dump(by_alias=True, mode="json")
                            for item in params.query.sorts
                        ],
                        "offset": 0,
                        "limit": min(
                            params.query.limit,
                            DASHBOARD_QUERY_LIMITS.max_list_rows,
                        ),
                    },
                )
                rows = [
                    {field: row.get(field) for field in params.query.fields}
                    for row in getattr(page, "rows", [])
                ]
                limit = min(params.query.limit, DASHBOARD_QUERY_LIMITS.max_list_rows)
                return DashboardQueryResult(
                    rows=rows[:limit],
                    truncated=len(rows) > limit,
                    max_points=limit,
                )
            query = params.query
            aggregate: JsonObject = {
                "filters": [item.model_dump(by_alias=True, mode="json") for item in query.filters],
                "groupBy": list(query.dimensions),
                "metrics": [
                    {
                        "function": measure.op,
                        "field": measure.field or "",
                        "alias": measure.key,
                    }
                    for measure in query.measures
                ],
                "limit": min(
                    query.limit,
                    DASHBOARD_QUERY_LIMITS.max_panel_points,
                ),
            }
            if query.time_bucket is not None:
                aggregate["timeBucket"] = query.time_bucket.model_dump(
                    by_alias=True,
                    mode="json",
                )
            if query.top_n is not None:
                aggregate["topN"] = query.top_n
            rows = await self._query.aggregate(table_id=query.collection, query=aggregate)
            limit = min(query.limit, DASHBOARD_QUERY_LIMITS.max_panel_points)
            return DashboardQueryResult(
                rows=rows[:limit],
                truncated=len(rows) > limit,
                max_points=limit,
            )

    async def save_dashboard(
        self, name: str, note: str, dashboard_id: str | None = None
    ) -> DashboardEntry:
        record_id = dashboard_id or str(uuid.uuid4())
        await self._metadata.upsert_metadata(
            "dashboards",
            record_id=record_id,
            values={"name": name, "note": note},
            expected_revision=None,
            idempotency_key=f"dashboard:save:{record_id}",
        )
        return DashboardEntry(id=record_id, name=name, note=note)

    async def delete_dashboard(self, dashboard_id: str) -> JsonObject:
        await self._delete("dashboards", dashboard_id, label="dashboard")
        return {"deleted": dashboard_id}

    async def save_panel(
        self,
        dashboard_id: str,
        name: str,
        panel_type: PanelType,
        position: PanelPosition,
        options: Mapping[str, object],
        query: Mapping[str, object],
        panel_id: str | None = None,
    ) -> PanelEntry:
        self._validate_panel_type(panel_type)
        record_id = panel_id or str(uuid.uuid4())
        frozen_options = _json_object(options, "panel options")
        frozen_query = _json_object(query, "panel query")
        values: JsonObject = {
            "dashboardId": dashboard_id,
            "name": name,
            "type": panel_type,
            "position": position.model_dump(by_alias=True, mode="json"),
            "options": frozen_options,
            "query": frozen_query,
        }
        await self._metadata.upsert_metadata(
            "panels",
            record_id=record_id,
            values=values,
            expected_revision=None,
            idempotency_key=f"panel:save:{record_id}",
        )
        return PanelEntry(
            id=record_id,
            dashboard_id=dashboard_id,
            name=name,
            type=panel_type,
            position=position,
            options=frozen_options,
            query=frozen_query,
        )

    async def delete_panel(self, dashboard_id: str, panel_id: str) -> JsonObject:
        del dashboard_id
        await self._delete("panels", panel_id, label="panel")
        return {"deleted": panel_id}

    def panel_manifest(self, _params: object | None = None) -> PanelManifestResult:
        return PanelManifestResult(
            manifest_version=PANEL_MANIFEST_VERSION,
            query_contract="product-query-port.v1",
            panels=list(BUILT_IN_PANEL_MANIFEST),
        )

    def dashboard_query_limits(self, _params: object | None = None) -> DashboardQueryLimits:
        return DASHBOARD_QUERY_LIMITS

    def is_known_panel_type(self, panel_type: str) -> bool:
        return any(entry.type == panel_type for entry in BUILT_IN_PANEL_MANIFEST)

    def _validate_panel_type(self, panel_type: PanelType) -> None:
        if panel_type == "custom" or not self.is_known_panel_type(panel_type):
            raise InsightsError(
                "panel type is not in the locked manifest",
                code="dashboard_panel_type_unknown",
            )

    async def _delete(
        self,
        namespace: str,
        record_id: str,
        *,
        expected_revision: str | None = None,
        operation_id: str | None = None,
        label: str,
    ) -> JsonObject:
        return await self._metadata.delete_metadata(
            namespace,
            record_id=record_id,
            expected_revision=expected_revision,
            idempotency_key=f"{label}:delete:{operation_id or record_id}",
        )


def compile_dashboard_query(
    query: DashboardPanelQuery,
    *,
    panel_type: PanelType,
    profile: object | None = None,
    field_types: dict[str, str] | None = None,
) -> CompiledDashboardQuery:
    """Compatibility helper that now emits the normalized product query AST."""
    del profile, field_types
    if isinstance(query, DashboardRecordQuery):
        record_query: JsonObject = {
            "keyword": None,
            "filters": [_model_object(item) for item in query.filters],
            "sorts": [_model_object(item) for item in query.sorts],
            "offset": 0,
            "limit": query.limit,
        }
        record_params: JsonObject = {
            "operation": "page",
            "tableId": query.collection,
            "query": record_query,
        }
        return CompiledDashboardQuery(
            params=record_params,
            referenced_fields=list(query.fields),
            max_rows=query.limit,
            max_points=query.limit,
        )
    max_points = min(query.limit, _panel_point_cap(panel_type))
    aggregate: JsonObject = {
        "filters": [_model_object(item) for item in query.filters],
        "groupBy": list(query.dimensions),
        "metrics": [
            {
                "function": item.op,
                "field": item.field or "",
                "alias": item.key,
            }
            for item in query.measures
        ],
        "limit": max_points,
    }
    aggregate_params: JsonObject = {
        "operation": "aggregate",
        "tableId": query.collection,
        "aggregate": aggregate,
    }
    if query.time_bucket is not None:
        aggregate["timeBucket"] = _model_object(query.time_bucket)
    if query.top_n is not None:
        aggregate["topN"] = query.top_n
    fields = list(query.dimensions)
    fields.extend(item.field for item in query.measures if item.field)
    return CompiledDashboardQuery(
        params=aggregate_params,
        referenced_fields=fields,
        max_rows=max_points,
        max_points=max_points,
    )


def _panel_point_cap(panel_type: PanelType) -> int:
    if panel_type == "list":
        return DASHBOARD_QUERY_LIMITS.max_list_rows
    if panel_type in {"pie", "donut"}:
        return DASHBOARD_QUERY_LIMITS.max_pie_slices
    return DASHBOARD_QUERY_LIMITS.max_panel_points


def merge_global_filter(
    panel_filters: list[JsonObject],
    variables: list[DashboardFilterVariable],
    state: DashboardFilterState,
    panel_id: str,
) -> list[JsonObject]:
    """Merge permitted dashboard variables into typed flat filter conditions."""
    merged = list(panel_filters)
    values = state.values
    for variable in variables:
        targets = variable.target_panels
        if targets and panel_id not in targets:
            continue
        value = values.get(variable.key, variable.default_value)
        if value is None:
            continue
        field = variable.field_bindings.get(panel_id)
        if not field:
            continue
        allowed = variable.allowed_fields
        if allowed and field not in allowed:
            continue
        merged.append({"field": field, "operator": "eq", "value": value, "logic": "AND"})
    return merged


def resolve_selection_targets(selection: PanelSelection, panels: list[PanelEntry]) -> list[str]:
    known = {panel.id for panel in panels}
    return [
        panel_id
        for panel_id in selection.target_panels
        if panel_id in known and panel_id != selection.panel_id
    ]


def normalize_panel_type(
    panel_type: PanelType, options: Mapping[str, object]
) -> tuple[str, JsonObject]:
    """Return a canonical product panel type and frozen options."""
    return panel_type, _json_object(options, "panel options")


def _is_panel_type(value: str, known: set[PanelType]) -> TypeGuard[PanelType]:
    return value in known


def parse_panel_type(
    panel_type: str, options: Mapping[str, object]
) -> tuple[PanelType, JsonObject]:
    """Parse a panel type, falling back safely for unknown renderers."""
    known: set[PanelType] = {entry.type for entry in BUILT_IN_PANEL_MANIFEST}
    canonical = panel_type if _is_panel_type(panel_type, known) else "custom"
    return (
        canonical,
        _json_object(options, "panel options"),
    )


def _validate_panel_manifest(panel: DashboardPanelDraft) -> None:
    entry = next((item for item in BUILT_IN_PANEL_MANIFEST if item.type == panel.type), None)
    if entry is None:
        raise InsightsError(
            "panel type is not in the runtime manifest", code="dashboard_panel_type_unknown"
        )
    if panel.position.width < entry.min_size.width or panel.position.height < entry.min_size.height:
        raise InsightsError(
            "panel position is smaller than the renderer minimum",
            code="dashboard_panel_size_invalid",
            field="position",
        )
    schema = entry.options_schema
    properties = schema.get("properties")
    if schema.get("type") != "object" or not isinstance(properties, dict):
        raise InsightsError(
            "panel manifest options schema is invalid", code="dashboard_manifest_invalid"
        )
    unknown = set(panel.options) - set(properties)
    if schema.get("additionalProperties") is False and unknown:
        raise InsightsError(
            "panel options contain fields not declared by the renderer",
            code="dashboard_panel_options_invalid",
            field=f"options.{sorted(unknown)[0]}",
        )
    for key, value in panel.options.items():
        rule = properties.get(key)
        if not isinstance(rule, dict) or not _matches_option_rule(value, rule):
            raise InsightsError(
                "panel option does not match the renderer schema",
                code="dashboard_panel_options_invalid",
                field=f"options.{key}",
            )


def _matches_option_rule(value: object, rule: Mapping[str, JsonValue]) -> bool:
    expected = rule.get("type")
    if expected == "string":
        if not isinstance(value, str):
            return False
        allowed = rule.get("enum")
        return not isinstance(allowed, list) or value in allowed
    if expected == "array":
        if not isinstance(value, list):
            return False
        item_rule = rule.get("items")
        return not isinstance(item_rule, dict) or all(
            _matches_option_rule(item, item_rule) for item in value
        )
    return False


def _preset(row: Mapping[str, JsonValue], collection: str) -> PresetEntry:
    view = row.get("view")
    scope = row.get("presetScope")
    return PresetEntry(
        id=_text(row.get("id")),
        collection=collection,
        name=_text(row.get("name")),
        scope=_preset_scope(scope),
        view=PresetView.model_validate(view if isinstance(view, dict) else {}),
        user_id=_optional_text(row.get("userId")),
        revision=_text(row.get("revision")),
    )


def _preset_scope(value: object) -> PresetScope:
    if value == "system":
        return "system"
    if value == "role":
        return "role"
    return "personal"


def _version(row: Mapping[str, JsonValue]) -> ContentVersionEntry:
    return ContentVersionEntry(
        id=_text(row.get("id")),
        key=_text(row.get("key")),
        name=_text(row.get("name")),
        outdated=bool(row.get("outdated", False)),
        main_hash=_text(row.get("mainHash")),
        revision=_text(row.get("revision")),
    )


def _dashboard(row: Mapping[str, JsonValue], panel_rows: list[JsonObject]) -> DashboardEntry:
    dashboard_id = _text(row.get("id"))
    panels = [
        _panel(item, dashboard_id)
        for item in panel_rows
        if _text(item.get("dashboardId")) == dashboard_id
    ]
    return DashboardEntry(
        id=dashboard_id,
        name=_text(row.get("name")),
        note=_text(row.get("note")),
        icon=_optional_text(row.get("icon")),
        color=_optional_text(row.get("color")),
        panels=panels,
    )


def _panel(row: Mapping[str, JsonValue], dashboard_id: str) -> PanelEntry:
    position = row.get("position")
    options = row.get("options")
    query = row.get("query")
    panel_type = row.get("type")
    known: set[PanelType] = {entry.type for entry in BUILT_IN_PANEL_MANIFEST}
    canonical_type: PanelType = (
        panel_type
        if isinstance(panel_type, str) and _is_panel_type(panel_type, known)
        else "custom"
    )
    return PanelEntry(
        id=_text(row.get("id")),
        dashboard_id=dashboard_id,
        name=_text(row.get("name")),
        note=_text(row.get("note")),
        icon=_optional_text(row.get("icon")),
        color=_optional_text(row.get("color")),
        show_header=row.get("showHeader") is not False,
        type=canonical_type,
        position=PanelPosition.model_validate(
            position if isinstance(position, dict) else {"x": 0, "y": 0, "width": 4, "height": 4}
        ),
        options=dict(options) if isinstance(options, dict) else {},
        query=dict(query) if isinstance(query, dict) else {},
    )


def _revision(value: object) -> str:
    encoded = json.dumps(
        _freeze(value),
        ensure_ascii=False,
        allow_nan=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode()
    return hashlib.sha256(encoded).hexdigest()


def _freeze(value: object) -> JsonValue:
    if value is None or isinstance(value, (str, bool, int)):
        return value
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError("dashboard value contains a non-finite number")
        return value
    if isinstance(value, list):
        return [_freeze(item) for item in value]
    if isinstance(value, Mapping):
        if not all(isinstance(key, str) for key in value):
            raise ValueError("dashboard JSON object keys must be strings")
        return {str(key): _freeze(item) for key, item in value.items()}
    raise ValueError("dashboard value is not JSON-compatible")


def _text(value: object, fallback: str = "") -> str:
    return value if isinstance(value, str) else fallback


def _optional_text(value: object) -> str | None:
    return value if isinstance(value, str) and value else None


def _string_list(value: object) -> list[str]:
    if not isinstance(value, list) or not all(isinstance(item, str) and item for item in value):
        return []
    return list(value)


def _receipt_revision(receipt: Mapping[str, JsonValue]) -> str:
    item = receipt.get("item")
    if not isinstance(item, Mapping):
        return ""
    return _text(item.get("revision"))


def _receipt_trace(receipt: Mapping[str, JsonValue]) -> JsonObject:
    return _json_object(
        {
            "status": _text(receipt.get("status")),
            "changeSetId": _text(receipt.get("changeSetId")),
            "emittedEvents": _string_list(receipt.get("emittedEvents")),
        },
        "metadata receipt",
    )


def _json_object(value: object, label: str) -> JsonObject:
    frozen = _freeze(value)
    if not isinstance(frozen, dict):
        raise ValueError(f"{label} is not a JSON object")
    return frozen


def _model_object(value: BaseModel) -> JsonObject:
    return _json_object(value.model_dump(by_alias=True, mode="json"), "dashboard model")


__all__ = [
    "BUILT_IN_PANEL_MANIFEST",
    "DASHBOARD_QUERY_LIMITS",
    "PANEL_MANIFEST_VERSION",
    "InsightsError",
    "InsightsService",
    "compile_dashboard_query",
    "merge_global_filter",
    "normalize_panel_type",
    "parse_panel_type",
    "resolve_selection_targets",
]
