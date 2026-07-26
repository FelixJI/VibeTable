"""Typed orchestration client for the local PocketBase product API."""

from __future__ import annotations

import json
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Any, Protocol

SESSION_HEADER = "X-VibeTable-Session"
MUTATION_PREVIEW_PATH = "/api/vibetable/v1/mutations/preview"
MUTATION_APPLY_PATH = "/api/vibetable/v1/mutations/apply"
QUERY_PATH = "/api/vibetable/v1/query"
LOOKUP_DESCRIBE_PATH = "/api/vibetable/v1/lookups/describe"
LOOKUP_QUERY_PATH = "/api/vibetable/v1/lookups/query"
RELATION_DESCRIBE_PATH = "/api/vibetable/v1/relations/describe"
SCHEMA_TABLE_PATH = "/api/vibetable/v1/schema/tables"
REALTIME_RECONCILE_PATH = "/api/vibetable/v1/events/reconcile"
METADATA_PATH = "/api/vibetable/v1/metadata"
BACKUP_PATH = "/api/vibetable/v1/backups"
BACKUP_RESTORE_PATH = "/api/vibetable/v1/backups/restore"
_METADATA_NAMESPACES = frozenset(
    {
        "shared_settings",
        "dashboards",
        "panels",
        "presets",
        "identifier_mappings",
        "content_versions",
    }
)


class PocketBaseTransport(Protocol):
    async def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = None,
        json_body: Any | None = None,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> Any: ...

    async def request_multipart(
        self,
        path: str,
        *,
        json_body: Mapping[str, Any],
        uploads: Sequence[tuple[str, str]],
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> Any: ...

    async def download_to_file(
        self,
        path: str,
        *,
        query: Mapping[str, Any],
        target_path: str,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
        maximum_bytes: int = 2 * 1024 * 1024 * 1024,
    ) -> int: ...


class PocketBaseProductError(Exception):
    """A sanitized product error returned by a sidecar endpoint."""

    def __init__(self, *, status: int, payload: Mapping[str, Any]) -> None:
        message = payload.get("message")
        super().__init__(message if isinstance(message, str) else "PocketBase operation failed")
        self.status = status
        self.code = _text(payload.get("code"), "sidecar.request_failed")
        self.path = payload.get("path") if isinstance(payload.get("path"), str) else None
        details = payload.get("details")
        self.details = dict(details) if isinstance(details, Mapping) else {}
        self.retryable = payload.get("retryable") is True

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {
            "code": self.code,
            "path": self.path,
            "details": _freeze_json(self.details),
            "retryable": self.retryable,
        }


@dataclass(frozen=True)
class QueryPageResult:
    rows: list[dict[str, Any]]
    offset: int
    limit: int
    filtered_rows: int
    total_rows: int
    snapshot: dict[str, Any]


class PocketBaseClient:
    """Calls only frozen product routes; it never accesses PB collections."""

    def __init__(self, *, transport: PocketBaseTransport, session_secret: str) -> None:
        if not session_secret:
            raise ValueError("PocketBase session secret is required")
        self._transport = transport
        self._headers = {SESSION_HEADER: session_secret}

    async def preview_mutation(self, request: Mapping[str, Any]) -> dict[str, Any]:
        return _object(
            await self._post(MUTATION_PREVIEW_PATH, request),
            "mutation preview",
        )

    async def apply_mutation(self, request: Mapping[str, Any]) -> dict[str, Any]:
        return _object(
            await self._post(MUTATION_APPLY_PATH, request),
            "mutation receipt",
        )

    async def query_page(
        self,
        *,
        table_id: str,
        query: dict[str, Any],
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

    async def read_rows(self, *, table_id: str, row_ids: list[str]) -> list[dict[str, Any]]:
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
        return [_freeze_json(row) for row in rows]

    async def aggregate(
        self,
        *,
        table_id: str,
        query: Mapping[str, Any],
    ) -> list[dict[str, Any]]:
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
        return [_freeze_json(row) for row in rows]

    async def describe_lookups(self, table_id: str) -> dict[str, Any]:
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

    async def describe_relations(self, table_id: str) -> dict[str, Any]:
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

    async def describe_table(self, table_id: str) -> dict[str, Any]:
        if not table_id or "/" in table_id or "\\" in table_id:
            raise ValueError("PocketBase table id is invalid")
        return _object(
            await self._transport.request(
                "GET",
                f"{SCHEMA_TABLE_PATH}/{table_id}",
                headers=dict(self._headers),
                expected_status=(200,),
            ),
            "table schema",
        )

    async def list_tables(self) -> dict[str, Any]:
        return _object(
            await self._transport.request(
                "GET",
                SCHEMA_TABLE_PATH,
                headers=dict(self._headers),
                expected_status=(200,),
            ),
            "table catalog",
        )

    async def list_backups(self) -> dict[str, Any]:
        return _object(
            await self._transport.request(
                "GET",
                BACKUP_PATH,
                headers=dict(self._headers),
                expected_status=(200,),
            ),
            "backup list",
        )

    async def create_backup(self, name: str) -> dict[str, Any]:
        return _object(
            await self._transport.request(
                "POST",
                BACKUP_PATH,
                json_body={"name": name},
                headers=dict(self._headers),
                expected_status=(201,),
            ),
            "backup create result",
        )

    async def delete_backup(self, name: str) -> dict[str, Any]:
        return _object(
            await self._transport.request(
                "DELETE",
                BACKUP_PATH,
                json_body={"name": name},
                headers=dict(self._headers),
                expected_status=(200,),
            ),
            "backup delete result",
        )

    async def restore_backup(self, name: str) -> dict[str, Any]:
        return _object(
            await self._transport.request(
                "POST",
                BACKUP_RESTORE_PATH,
                json_body={"name": name},
                headers=dict(self._headers),
                expected_status=(202,),
            ),
            "backup restore result",
        )

    async def list_internal_metadata(self, namespace: str) -> dict[str, Any]:
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
        request: Mapping[str, Any],
    ) -> dict[str, Any]:
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
        request: Mapping[str, Any],
    ) -> dict[str, Any]:
        return _object(
            await self._post(
                _metadata_namespace_path(namespace) + "/delete",
                request,
            ),
            "internal metadata deletion",
        )

    async def commit_dashboard_metadata(
        self,
        request: Mapping[str, Any],
    ) -> dict[str, Any]:
        return _object(
            await self._post(METADATA_PATH + "/dashboards/commit", request),
            "dashboard metadata commit",
        )

    async def query_lookups(
        self,
        *,
        table_id: str,
        schema_revision: str,
        query: Mapping[str, Any],
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

    async def reconcile_realtime(
        self,
        *,
        table_id: str,
        schema_revision: str,
        data_revision: str,
    ) -> dict[str, Any]:
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
    ) -> dict[str, Any]:
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
    ) -> dict[str, Any]:
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
    ) -> dict[str, Any]:
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

    async def _post(self, path: str, body: Mapping[str, Any]) -> Any:
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


def _freeze_json(value: Any) -> Any:
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    )
    return json.loads(encoded)


def _object(value: Any, label: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ValueError(f"PocketBase returned an invalid {label}")
    return _freeze_json(value)


def _query_page(payload: Mapping[str, Any]) -> QueryPageResult:
    rows = payload.get("rows")
    snapshot = payload.get("querySnapshot")
    if (
        not isinstance(rows, list)
        or not all(isinstance(row, dict) for row in rows)
        or not isinstance(snapshot, dict)
    ):
        raise ValueError("PocketBase returned an invalid query page")
    return QueryPageResult(
        rows=[_freeze_json(row) for row in rows],
        offset=_integer(payload.get("offset"), "offset"),
        limit=_integer(payload.get("limit"), "limit"),
        filtered_rows=_integer(payload.get("filteredRows"), "filteredRows"),
        total_rows=_integer(payload.get("totalRows"), "totalRows"),
        snapshot=_freeze_json(snapshot),
    )


def _integer(value: Any, name: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ValueError(f"PocketBase returned an invalid {name}")
    return value


def _metadata_namespace_path(namespace: str) -> str:
    if namespace not in _METADATA_NAMESPACES:
        raise ValueError(f"unknown internal metadata namespace {namespace!r}")
    return f"{METADATA_PATH}/{namespace}"


def _text(value: Any, fallback: str) -> str:
    return value if isinstance(value, str) and value else fallback


__all__ = [
    "BACKUP_PATH",
    "BACKUP_RESTORE_PATH",
    "LOOKUP_DESCRIBE_PATH",
    "LOOKUP_QUERY_PATH",
    "METADATA_PATH",
    "MUTATION_APPLY_PATH",
    "MUTATION_PREVIEW_PATH",
    "QUERY_PATH",
    "REALTIME_RECONCILE_PATH",
    "RELATION_DESCRIBE_PATH",
    "SCHEMA_TABLE_PATH",
    "SESSION_HEADER",
    "PocketBaseClient",
    "PocketBaseProductError",
    "PocketBaseTransport",
    "QueryPageResult",
]
