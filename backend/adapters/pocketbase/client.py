"""Typed orchestration client for the local PocketBase product API."""

from __future__ import annotations

import math
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Protocol

from pydantic import JsonValue

from backend.contracts.schema_v2 import SchemaSnapshotV2

JsonObject = dict[str, JsonValue]

SESSION_HEADER = "X-VibeTable-Session"
MUTATION_PREVIEW_PATH = "/api/vibetable/v1/mutations/preview"
MUTATION_APPLY_PATH = "/api/vibetable/v1/mutations/apply"
IMPORT_PREVIEW_PATH = "/api/vibetable/v2/import-preview"
QUERY_PATH = "/api/vibetable/v1/query"
LOOKUP_DESCRIBE_PATH = "/api/vibetable/v1/lookups/describe"
LOOKUP_QUERY_PATH = "/api/vibetable/v1/lookups/query"
LOOKUP_VALUE_PAGE_PATH = "/api/vibetable/v1/lookups/value-page"
RELATION_DESCRIBE_PATH = "/api/vibetable/v1/relations/describe"
SCHEMA_TABLE_PATH = "/api/vibetable/v2/schema/tables"
REALTIME_RECONCILE_PATH = "/api/vibetable/v1/events/reconcile"
METADATA_PATH = "/api/vibetable/v1/metadata"
_METADATA_NAMESPACES = frozenset(
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


class PocketBaseTransport(Protocol):
    async def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, JsonValue] | None = None,
        json_body: JsonValue = None,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> JsonValue: ...

    async def request_multipart(
        self,
        path: str,
        *,
        json_body: Mapping[str, JsonValue],
        uploads: Sequence[tuple[str, str]],
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> JsonValue: ...

    async def download_to_file(
        self,
        path: str,
        *,
        query: Mapping[str, JsonValue],
        target_path: str,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
        maximum_bytes: int = 2 * 1024 * 1024 * 1024,
    ) -> int: ...


class PocketBaseProductError(Exception):
    """A sanitized product error returned by a sidecar endpoint."""

    def __init__(self, *, status: int, payload: Mapping[str, JsonValue]) -> None:
        message = payload.get("message")
        super().__init__(message if isinstance(message, str) else "PocketBase operation failed")
        self.status = status
        self.code = _text(payload.get("code"), "sidecar.request_failed")
        self.path = payload.get("path") if isinstance(payload.get("path"), str) else None
        details = payload.get("details")
        self.details = dict(details) if isinstance(details, Mapping) else {}
        self.retryable = payload.get("retryable") is True

    @property
    def rpc_error_data(self) -> JsonObject:
        return {
            "code": self.code,
            "path": self.path,
            "details": _freeze_json(self.details),
            "retryable": self.retryable,
        }


@dataclass(frozen=True)
class QueryPageResult:
    rows: list[dict[str, JsonValue]]
    offset: int
    limit: int
    filtered_rows: int
    total_rows: int
    snapshot: dict[str, JsonValue]


@dataclass(frozen=True)
class QueryCursorWindowResult:
    rows: list[dict[str, JsonValue]]
    next_cursor: str | None
    has_more: bool
    filtered_rows: int
    total_rows: int
    snapshot: dict[str, JsonValue]


@dataclass(frozen=True)
class SelectionProjectionResult:
    schema_snapshot: dict[str, JsonValue]
    cursor_window: QueryCursorWindowResult


@dataclass(frozen=True)
class QueryCursorOpenCommand:
    table_id: str
    query: JsonObject


@dataclass(frozen=True)
class LookupViewQueryCommand:
    table_id: str
    schema_revision: str
    query: JsonObject
    groups: list[JsonObject]
    group_limit: int


@dataclass(frozen=True)
class ViewQueryResult:
    page: QueryPageResult
    group_rows: list[dict[str, JsonValue]]
    group_offset: int
    group_limit: int
    has_more_groups: bool


class PocketBaseClient:
    """Calls only frozen product routes; it never accesses PB collections."""

    def __init__(self, *, transport: PocketBaseTransport, session_secret: str) -> None:
        if not session_secret:
            raise ValueError("PocketBase session secret is required")
        self._transport = transport
        self._headers = {SESSION_HEADER: session_secret}

    async def preview_mutation(self, request: Mapping[str, JsonValue]) -> JsonObject:
        return _object(
            await self._post(MUTATION_PREVIEW_PATH, request),
            "mutation preview",
        )

    async def apply_mutation(self, request: Mapping[str, JsonValue]) -> JsonObject:
        return _object(
            await self._post(MUTATION_APPLY_PATH, request),
            "mutation receipt",
        )

    async def preview_import(self, request: Mapping[str, JsonValue]) -> JsonObject:
        return _object(
            await self._post(IMPORT_PREVIEW_PATH, request),
            "import preview",
        )

    async def query_page(
        self,
        *,
        table_id: str,
        query: JsonObject,
    ) -> QueryPageResult:
        payload = _object(
            await self._post(
                QUERY_PATH,
                {
                    "operation": "page",
                    "tableId": table_id,
                    "query": query,
                },
            ),
            "query page",
        )
        return _query_page(payload)

    async def open_query_cursor(
        self,
        command: QueryCursorOpenCommand,
    ) -> QueryCursorWindowResult:
        payload = _object(
            await self._post(
                QUERY_PATH,
                {
                    "operation": "cursor.open",
                    "tableId": command.table_id,
                    "query": command.query,
                },
            ),
            "query cursor window",
        )
        return _query_cursor_window(payload)

    async def open_selection_projection(
        self,
        command: QueryCursorOpenCommand,
    ) -> SelectionProjectionResult:
        payload = _object(
            await self._post(
                QUERY_PATH,
                {
                    "operation": "selection.open",
                    "tableId": command.table_id,
                    "query": command.query,
                },
            ),
            "selection projection",
        )
        return _selection_projection(payload, command.table_id)

    async def fetch_query_cursor(self, *, cursor: str) -> QueryCursorWindowResult:
        payload = _object(
            await self._post(
                QUERY_PATH,
                {"operation": "cursor.fetch", "cursor": cursor},
            ),
            "query cursor window",
        )
        return _query_cursor_window(payload)

    async def execute_view(
        self,
        *,
        table_id: str,
        view: Mapping[str, JsonValue],
    ) -> ViewQueryResult:
        payload = _object(
            await self._post(
                QUERY_PATH,
                {
                    "operation": "view",
                    "tableId": table_id,
                    "view": view,
                },
            ),
            "view query result",
        )
        page = payload.get("page")
        group_rows = payload.get("groupRows")
        has_more_groups = payload.get("hasMoreGroups")
        if (
            not isinstance(page, dict)
            or not isinstance(group_rows, list)
            or not all(_valid_group_row(row) for row in group_rows)
            or not isinstance(has_more_groups, bool)
        ):
            raise ValueError("PocketBase returned an invalid view query result")
        return ViewQueryResult(
            page=_query_page(page),
            group_rows=[_object(row, "view group row") for row in group_rows],
            group_offset=_integer(payload.get("groupOffset"), "groupOffset"),
            group_limit=_integer(payload.get("groupLimit"), "groupLimit"),
            has_more_groups=has_more_groups,
        )

    async def read_rows(self, *, table_id: str, row_ids: list[str]) -> list[JsonObject]:
        payload = _object(
            await self._post(
                QUERY_PATH,
                {
                    "operation": "readRows",
                    "tableId": table_id,
                    "rowIds": row_ids,
                },
            ),
            "read rows",
        )
        rows = payload.get("rows")
        if not isinstance(rows, list) or not all(isinstance(row, dict) for row in rows):
            raise ValueError("PocketBase returned invalid rows")
        return [_object(row, "row") for row in rows]

    async def aggregate(
        self,
        *,
        table_id: str,
        query: Mapping[str, JsonValue],
    ) -> list[JsonObject]:
        payload = _object(
            await self._post(
                QUERY_PATH,
                {
                    "operation": "aggregate",
                    "tableId": table_id,
                    "aggregate": query,
                },
            ),
            "aggregate result",
        )
        rows = payload.get("rows")
        if not isinstance(rows, list) or not all(isinstance(row, dict) for row in rows):
            raise ValueError("PocketBase returned invalid aggregate rows")
        return [_object(row, "aggregate row") for row in rows]

    async def describe_lookups(self, table_id: str) -> JsonObject:
        return _object(
            await self._transport.request(
                "GET",
                LOOKUP_DESCRIBE_PATH,
                query={"tableId": table_id},
                headers=dict(self._headers),
                expected_status=(200,),
            ),
            "lookup catalog",
        )

    async def describe_relations(self, table_id: str) -> JsonObject:
        return _object(
            await self._transport.request(
                "GET",
                RELATION_DESCRIBE_PATH,
                query={"tableId": table_id},
                headers=dict(self._headers),
                expected_status=(200,),
            ),
            "relation catalog",
        )

    async def describe_table(self, table_id: str) -> JsonObject:
        if not table_id or "/" in table_id or "\\" in table_id:
            raise ValueError("PocketBase table id is invalid")
        raw = _object(
            await self._transport.request(
                "GET",
                f"{SCHEMA_TABLE_PATH}/{table_id}",
                headers=dict(self._headers),
                expected_status=(200,),
            ),
            "table schema",
        )
        return _object(
            SchemaSnapshotV2.model_validate(raw).model_dump(mode="json", by_alias=True),
            "table schema",
        )

    async def list_tables(self) -> JsonObject:
        return _object(
            await self._transport.request(
                "GET",
                SCHEMA_TABLE_PATH,
                headers=dict(self._headers),
                expected_status=(200,),
            ),
            "table catalog",
        )

    async def list_internal_metadata(self, namespace: str) -> JsonObject:
        path = _metadata_namespace_path(namespace)
        return _object(
            await self._transport.request(
                "GET",
                path,
                headers=dict(self._headers),
                expected_status=(200,),
            ),
            "internal metadata list",
        )

    async def upsert_internal_metadata(
        self,
        namespace: str,
        request: Mapping[str, JsonValue],
    ) -> JsonObject:
        return _object(
            await self._post(
                _metadata_namespace_path(namespace) + "/upsert",
                request,
            ),
            "internal metadata mutation",
        )

    async def delete_internal_metadata(
        self,
        namespace: str,
        request: Mapping[str, JsonValue],
    ) -> JsonObject:
        return _object(
            await self._post(
                _metadata_namespace_path(namespace) + "/delete",
                request,
            ),
            "internal metadata deletion",
        )

    async def commit_dashboard_metadata(
        self,
        request: Mapping[str, JsonValue],
    ) -> JsonObject:
        return _object(
            await self._post(METADATA_PATH + "/dashboards/commit", request),
            "dashboard metadata commit",
        )

    async def query_lookups(
        self,
        *,
        table_id: str,
        schema_revision: str,
        query: Mapping[str, JsonValue],
    ) -> QueryPageResult:
        payload = _object(
            await self._post(
                LOOKUP_QUERY_PATH,
                {
                    "tableId": table_id,
                    "schemaRevision": schema_revision,
                    "query": query,
                },
            ),
            "lookup query page",
        )
        return _query_page(payload)

    async def query_lookup_view(
        self,
        command: LookupViewQueryCommand,
    ) -> ViewQueryResult:
        payload = _object(
            await self._post(
                LOOKUP_QUERY_PATH,
                {
                    "tableId": command.table_id,
                    "schemaRevision": command.schema_revision,
                    "query": command.query,
                    "groups": command.groups,
                    "groupLimit": command.group_limit,
                },
            ),
            "lookup view query result",
        )
        return _flat_view_query_result(payload)

    async def lookup_value_page(
        self,
        *,
        table_id: str,
        schema_revision: str,
        source_record_id: str,
        field_id: str,
        offset: int,
        limit: int,
    ) -> JsonObject:
        return _object(
            await self._post(
                LOOKUP_VALUE_PAGE_PATH,
                {
                    "tableId": table_id,
                    "schemaRevision": schema_revision,
                    "sourceRecordId": source_record_id,
                    "fieldId": field_id,
                    "offset": offset,
                    "limit": limit,
                },
            ),
            "lookup value page",
        )

    async def reconcile_realtime(
        self,
        *,
        table_id: str,
        schema_revision: str,
        data_revision: str,
    ) -> JsonObject:
        payload = _object(
            await self._post(
                REALTIME_RECONCILE_PATH,
                {
                    "tableId": table_id,
                    "schemaRevision": schema_revision,
                    "dataRevision": data_revision,
                },
            ),
            "realtime reconciliation",
        )
        action = payload.get("action")
        if action not in {"none", "refresh-data", "reload-schema"}:
            raise ValueError("PocketBase returned an invalid reconciliation action")
        required_text = (
            "tableId",
            "clientSchemaRevision",
            "clientDataRevision",
            "currentSchemaRevision",
            "currentDataRevision",
        )
        if any(
            not isinstance(payload.get(name), str) or not payload[name] for name in required_text
        ):
            raise ValueError("PocketBase returned an invalid realtime reconciliation")
        return payload

    async def read_history(
        self,
        *,
        collection: str,
        item_id: str,
        limit: int = 50,
    ) -> JsonObject:
        return _object(
            await self._transport.request(
                "GET",
                "/api/vibetable/v1/history/change-sets",
                query={
                    "collection": collection,
                    "itemId": item_id,
                    "scope": "row",
                    "limit": limit,
                    "offset": 0,
                },
                headers=dict(self._headers),
                expected_status=(200,),
            ),
            "history page",
        )

    async def preview_history_restore(
        self,
        *,
        collection: str,
        item_id: str,
        target_revision: str,
    ) -> JsonObject:
        return _object(
            await self._post(
                "/api/vibetable/v1/history/restore-preview",
                {
                    "collection": collection,
                    "itemId": item_id,
                    "targetRevision": target_revision,
                    "scope": "row",
                },
            ),
            "history restore preview",
        )

    async def apply_history_restore(
        self,
        *,
        collection: str,
        item_id: str,
        token: str,
    ) -> JsonObject:
        return _object(
            await self._post(
                "/api/vibetable/v1/history/restore-apply",
                {
                    "collection": collection,
                    "itemId": item_id,
                    "token": token,
                },
            ),
            "history restore result",
        )

    async def _post(self, path: str, body: object) -> JsonValue:
        # Canonical JSON round-trip detaches caller-owned nested containers and
        # rejects non-wire values (NaN, Decimal, arbitrary Python objects)
        # before an HTTP request is attempted.
        frozen = _freeze_json(body)
        return await self._transport.request(
            "POST",
            path,
            json_body=frozen,
            headers=dict(self._headers),
            expected_status=(200,),
        )


def _freeze_json(value: object) -> JsonValue:
    if value is None or isinstance(value, (str, bool, int)):
        return value
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError("PocketBase request contains a non-finite number")
        return value
    if isinstance(value, list):
        return [_freeze_json(item) for item in value]
    if isinstance(value, Mapping):
        if not all(isinstance(key, str) for key in value):
            raise ValueError("PocketBase JSON object keys must be strings")
        return {str(key): _freeze_json(item) for key, item in value.items()}
    raise ValueError("PocketBase request contains a non-JSON value")


def _object(value: object, label: str) -> JsonObject:
    frozen = _freeze_json(value)
    if not isinstance(frozen, dict):
        raise ValueError(f"PocketBase returned an invalid {label}")
    return frozen


def _query_page(payload: Mapping[str, JsonValue]) -> QueryPageResult:
    rows = payload.get("rows")
    snapshot = payload.get("querySnapshot")
    if (
        not isinstance(rows, list)
        or not all(isinstance(row, dict) for row in rows)
        or not isinstance(snapshot, dict)
    ):
        raise ValueError("PocketBase returned an invalid query page")
    return QueryPageResult(
        rows=[_object(row, "query row") for row in rows],
        offset=_integer(payload.get("offset"), "offset"),
        limit=_integer(payload.get("limit"), "limit"),
        filtered_rows=_integer(payload.get("filteredRows"), "filteredRows"),
        total_rows=_integer(payload.get("totalRows"), "totalRows"),
        snapshot=_object(snapshot, "query snapshot"),
    )


def _query_cursor_window(payload: Mapping[str, JsonValue]) -> QueryCursorWindowResult:
    rows = payload.get("rows")
    snapshot = payload.get("querySnapshot")
    next_cursor = payload.get("nextCursor")
    has_more = payload.get("hasMore")
    if (
        not isinstance(rows, list)
        or not all(isinstance(row, dict) for row in rows)
        or not isinstance(snapshot, dict)
        or (next_cursor is not None and not isinstance(next_cursor, str))
        or not isinstance(has_more, bool)
        or has_more != (next_cursor is not None)
    ):
        raise ValueError("PocketBase returned an invalid query cursor window")
    return QueryCursorWindowResult(
        rows=[_object(row, "query cursor row") for row in rows],
        next_cursor=next_cursor,
        has_more=has_more,
        filtered_rows=_integer(payload.get("filteredRows"), "filteredRows"),
        total_rows=_integer(payload.get("totalRows"), "totalRows"),
        snapshot=_object(snapshot, "query snapshot"),
    )


def _selection_projection(
    payload: Mapping[str, JsonValue],
    expected_table: str,
) -> SelectionProjectionResult:
    schema_raw = payload.get("schemaSnapshot")
    window_raw = payload.get("cursorWindow")
    if not isinstance(schema_raw, dict) or not isinstance(window_raw, dict):
        raise ValueError("PocketBase returned an invalid selection projection")
    schema = _object(schema_raw, "selection schema snapshot")
    window = _query_cursor_window(window_raw)
    snapshot = window.snapshot
    if (
        schema.get("tableId") != expected_table
        or snapshot.get("table") != expected_table
        or not isinstance(schema.get("schemaRevision"), str)
        or schema.get("schemaRevision") != snapshot.get("schemaRevision")
        or not isinstance(schema.get("dataRevision"), int)
        or isinstance(schema.get("dataRevision"), bool)
        or not isinstance(snapshot.get("dataRevision"), int)
        or isinstance(snapshot.get("dataRevision"), bool)
        or schema.get("dataRevision") != snapshot.get("dataRevision")
    ):
        raise ValueError("PocketBase returned mismatched selection revisions")
    return SelectionProjectionResult(schema_snapshot=schema, cursor_window=window)


def _flat_view_query_result(payload: Mapping[str, JsonValue]) -> ViewQueryResult:
    group_rows = payload.get("groupRows")
    has_more_groups = payload.get("hasMoreGroups")
    if (
        not isinstance(group_rows, list)
        or not all(_valid_group_row(row) for row in group_rows)
        or not isinstance(has_more_groups, bool)
    ):
        raise ValueError("PocketBase returned an invalid lookup view query result")
    return ViewQueryResult(
        page=_query_page(payload),
        group_rows=[_object(row, "lookup group row") for row in group_rows],
        group_offset=_integer(payload.get("groupOffset"), "groupOffset"),
        group_limit=_integer(payload.get("groupLimit"), "groupLimit"),
        has_more_groups=has_more_groups,
    )


def _valid_group_row(value: object) -> bool:
    if not isinstance(value, dict):
        return False
    key = value.get("key")
    summaries = value.get("summaries")
    count = value.get("count")
    if (
        not isinstance(key, list)
        or not isinstance(summaries, list)
        or not isinstance(count, int)
        or isinstance(count, bool)
    ):
        return False
    has_parent_count = "parentCount" in value
    has_parent_summaries = "parentSummaries" in value
    if has_parent_count != has_parent_summaries:
        return False
    parent_count = value.get("parentCount")
    parent_summaries = value.get("parentSummaries")
    return (
        (
            parent_count is None
            or (isinstance(parent_count, int) and not isinstance(parent_count, bool))
        )
        and (parent_summaries is None or isinstance(parent_summaries, list))
        and (not has_parent_count or len(key) == 2)
    )


def _integer(value: object, name: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ValueError(f"PocketBase returned an invalid {name}")
    return value


def _metadata_namespace_path(namespace: str) -> str:
    if namespace not in _METADATA_NAMESPACES:
        raise ValueError(f"unknown internal metadata namespace {namespace!r}")
    return f"{METADATA_PATH}/{namespace}"


def _text(value: object, fallback: str) -> str:
    return value if isinstance(value, str) and value else fallback


__all__ = [
    "LOOKUP_DESCRIBE_PATH",
    "LOOKUP_QUERY_PATH",
    "LOOKUP_VALUE_PAGE_PATH",
    "METADATA_PATH",
    "MUTATION_APPLY_PATH",
    "MUTATION_PREVIEW_PATH",
    "QUERY_PATH",
    "REALTIME_RECONCILE_PATH",
    "RELATION_DESCRIBE_PATH",
    "SCHEMA_TABLE_PATH",
    "SESSION_HEADER",
    "LookupViewQueryCommand",
    "PocketBaseClient",
    "PocketBaseProductError",
    "PocketBaseTransport",
    "QueryCursorOpenCommand",
    "QueryPageResult",
    "ViewQueryResult",
]
