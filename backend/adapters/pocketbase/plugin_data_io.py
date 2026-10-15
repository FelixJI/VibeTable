"""Host-only DataIO composition over the existing surface authority and services."""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any, cast

from pydantic import JsonValue

from backend.adapters.pocketbase.client import PocketBaseClient, QueryPageResult
from backend.adapters.pocketbase.data_io import PocketBaseLocalAuth
from backend.adapters.pocketbase.mutation import PocketBaseBulkMutationClient
from backend.adapters.pocketbase.relation_io import PocketBaseRelationImportProvider
from backend.application.export_service import ExportError, ExportService, QueryPage
from backend.application.host_files import HostFiles
from backend.application.import_service import ImportService
from backend.contracts.data_io import ExportParams, ExportResult, ImportPlan, PreviewImportParams
from backend.contracts.data_profile import CollectionProfile
from backend.contracts.plugin_data_io import PluginIoScope
from backend.contracts.product_rpc import JsonObject


@dataclass
class PluginExportPage:
    rows: list[dict[str, Any]]
    offset: int
    limit: int
    filtered_rows: int
    total_rows: int
    snapshot: dict[str, Any]
    computed_pending: bool = False


class ScopedImportPreview(PocketBaseBulkMutationClient):
    """Only ImportService.preview is composed with this port; apply stays in Host."""

    def __init__(
        self,
        client: PocketBaseClient,
        scope: PluginIoScope,
        mode: str,
        upsert_key: str | None = None,
    ) -> None:
        super().__init__(client=client, auth=PocketBaseLocalAuth())
        self._scope = scope
        self._mode = mode
        self._upsert_key = upsert_key

    async def preview_import(
        self,
        *,
        collection: str,
        schema_revision: str,
        rows: list[dict[str, Any]],
        row_modes: list[str] | None = None,
    ) -> dict[str, Any]:
        return await self._client.plugin_surface(
            self._scope.principal,
            "import.preview",
            {
                "collection": collection,
                "schemaRevision": schema_revision,
                "rows": cast(list[JsonValue], rows),
                "mode": self._mode,
                # The authoritative preview matches rows by the same approved
                # upsert key as import.mint, so unmatched rows are normalized
                # as inserts and missing required fields surface as row
                # diagnostics instead of a later request-level failure.
                "upsertKey": self._upsert_key,
            },
        )

    async def mint_import_plan(
        self,
        *,
        collection: str,
        grant_id: str,
        schema_revision: str,
        capability_hash: str,
        source_hash: str,
        rows: list[dict[str, Any]],
        mode: str,
        upsert_key: str | None,
    ) -> dict[str, Any]:
        return await self._client.plugin_surface(
            self._scope.principal,
            "import.mint",
            {
                "contract": self.IMPORT_PLAN_CONTRACT,
                "collection": collection,
                "grantId": grant_id,
                "schemaRevision": schema_revision,
                "capabilityHash": capability_hash,
                "sourceHash": source_hash,
                "rows": cast(list[JsonValue], rows),
                "mode": mode,
                "upsertKey": upsert_key,
            },
        )


class ScopedExportQuery:
    def __init__(
        self, client: PocketBaseClient, scope: PluginIoScope, profile: CollectionProfile
    ) -> None:
        self._client = client
        self._scope = scope
        self._computed_fields = {
            field
            for field, schema in profile.field_schemas.items()
            if schema.get("dataType") in {"formula", "lookup"}
        }
        self._offset = 0
        self._cursor: str | None = None
        self._dependencies: JsonObject | None = None
        self._done = False
        self._last: PluginExportPage | None = None

    async def query_page(self, *, table_id: str, query: dict[str, object]) -> QueryPage:
        offset = query.get("offset", 0)
        if (
            not isinstance(offset, int)
            or isinstance(offset, bool)
            or offset != self._offset
            or query.get("limit") != 100
        ):
            raise ExportError("export paging identity changed", code="export_snapshot_changed")
        if self._done:
            assert self._last is not None
            # Recheck the principal and columns even for an empty final page.
            await self._client.plugin_surface(
                self._scope.principal,
                "data.ioProfile",
                {"collection": table_id, "operation": "read", "fields": list(self._scope.fields)},
            )
            return PluginExportPage(
                rows=[],
                offset=self._offset,
                limit=100,
                filtered_rows=self._last.filtered_rows,
                total_rows=self._last.total_rows,
                snapshot=self._last.snapshot,
            )
        request: JsonObject = {
            **self._scope.query,
            "contract": "vibetable.plugin-query.v2",
            "collection": table_id,
            "fields": list(self._scope.fields),
            "pageSize": 100,
        }
        result = await self._client.plugin_surface(
            self._scope.principal,
            "data.exportQuery",
            request,
            cursor=self._cursor,
            dependencies=self._dependencies,
        )
        raw_rows = result.get("items")
        if not isinstance(raw_rows, list) or any(not isinstance(row, dict) for row in raw_rows):
            raise ExportError("invalid scoped export page", code="export_snapshot_invalid")
        rows: list[dict[str, JsonValue]] = []
        for raw in raw_rows:
            assert isinstance(raw, dict)
            row: dict[str, JsonValue] = {}
            for field in self._scope.fields:
                value = raw.get(field)
                if field in self._computed_fields:
                    if (
                        not isinstance(value, dict)
                        or set(value) != {"state", "value", "fresh"}
                        or value["fresh"] is not True
                        or value["state"] != "ready"
                    ):
                        raise ExportError(
                            "computed value is stale", code="export_computed_cell_not_ready"
                        )
                    value = value["value"]
                row[field] = value
            rows.append(row)
        cursor = result.get("nextCursor")
        self._cursor = cursor if isinstance(cursor, str) and cursor else None
        self._done = self._cursor is None
        self._offset += len(rows)
        dependencies = result.get("dependencies")
        self._dependencies = dependencies if isinstance(dependencies, dict) else None
        snapshot = {
            key: result.get(key) for key in ("schemaRevision", "dataRevision", "clockPeriod")
        }
        self._last = PluginExportPage(
            rows=rows,
            offset=offset,
            limit=100,
            filtered_rows=cast(int, result["filteredRows"]),
            total_rows=cast(int, result["totalRows"]),
            snapshot=snapshot,
            computed_pending=result.get("complete") is not True,
        )
        return self._last


class ScopedRelationRead:
    def __init__(self, client: PocketBaseClient, scope: PluginIoScope) -> None:
        self._client = client
        self._scope = scope

    async def describe_relations(self, table_id: str) -> dict[str, Any]:
        return await self._client.plugin_surface(
            self._scope.principal, "data.ioRelations", {"collection": table_id}
        )

    async def describe_table(self, table_id: str) -> dict[str, Any]:
        return await self._client.plugin_surface(
            self._scope.principal, "data.ioTargetSchema", {"collection": table_id}
        )

    async def query_page(self, *, table_id: str, query: dict[str, Any]) -> QueryPageResult:
        page = await self._client.plugin_surface(
            self._scope.principal,
            "data.query",
            {
                "contract": "vibetable.plugin-query.v2",
                "collection": table_id,
                "fields": ["id"],
                "filters": query["filters"],
                "pageSize": 2,
            },
        )
        return QueryPageResult(
            rows=cast(list[dict[str, JsonValue]], page["items"]),
            offset=0,
            limit=2,
            filtered_rows=cast(int, page["filteredRows"]),
            total_rows=cast(int, page["totalRows"]),
            snapshot={},
        )


async def preview_plugin_import(
    client: PocketBaseClient,
    files: HostFiles,
    raw: JsonObject,
    progress: Callable[[int, int, str], Awaitable[None]],
    cancelled: Callable[[], bool],
) -> ImportPlan:
    copied = dict(raw)
    scope = PluginIoScope.model_validate(copied.pop("pluginScope"))
    params = PreviewImportParams.model_validate(copied)
    profile = CollectionProfile.model_validate(
        await client.plugin_surface(
            scope.principal,
            "data.ioProfile",
            {
                "collection": params.collection,
                "operation": "create",
                "fields": [],
                "mode": params.mode,
            },
        )
    )
    bulk = ScopedImportPreview(client, scope, params.mode, params.upsert_key)
    service = ImportService(
        bulk=bulk,
        maximum_source_expansion_bytes=128 * 1024 * 1024,
        profiles={params.collection: profile},
        files=files.bound_run(scope.run_id),
        relation_provider=PocketBaseRelationImportProvider(
            client=ScopedRelationRead(client, scope), bulk=bulk
        ),
    )
    return await service.preview(params, progress=progress, cancelled=cancelled)


async def export_plugin_data(
    client: PocketBaseClient,
    files: HostFiles,
    raw: JsonObject,
    progress: Callable[[int, int, str], Awaitable[None]],
    cancelled: Callable[[], bool],
) -> ExportResult:
    copied = dict(raw)
    scope = PluginIoScope.model_validate(copied.pop("pluginScope"))
    params = ExportParams.model_validate(copied)
    if params.include_relations or params.lookup_ids:
        raise ExportError(
            "relation labels require an authorized projection", code="plugin_read_denied"
        )
    profile = CollectionProfile.model_validate(
        await client.plugin_surface(
            scope.principal,
            "data.ioProfile",
            {"collection": params.collection, "operation": "read", "fields": list(scope.fields)},
        )
    )
    return await ExportService(
        query_port=ScopedExportQuery(client, scope, profile),
        profiles={params.collection: profile},
        files=files.bound_run(scope.run_id),
        output_columns=scope.fields,
    ).export(params, progress=progress, cancelled=cancelled)
