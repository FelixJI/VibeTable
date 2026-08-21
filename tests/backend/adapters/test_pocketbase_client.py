from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.pocketbase.client import (
    LookupViewQueryCommand,
    PocketBaseClient,
    PocketBaseProductError,
    QueryCursorOpenCommand,
)


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(
        self,
        method: str,
        path: str,
        *,
        query: dict[str, Any] | None = None,
        json_body: Any | None = None,
        headers: dict[str, str] | None = None,
        expected_status: tuple[int, ...] = (200,),
    ) -> Any:
        request = {
            "method": method,
            "path": path,
            "json_body": json_body,
            "headers": headers,
            "expected_status": expected_status,
        }
        if query is not None:
            request["query"] = query
        self.requests.append(request)
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


@pytest.mark.asyncio
async def test_mutation_apply_sends_frozen_product_request_with_session_header() -> None:
    transport = FakeTransport(
        [
            {
                "contractVersion": "2.0",
                "status": "applied",
                "changeSetId": "change-1",
                "affectedRows": [],
                "computedFields": {},
                "newRevision": "2",
                "emittedEvents": [],
                "warnings": [],
            }
        ]
    )
    client = PocketBaseClient(transport=transport, session_secret="a" * 64)
    request = {
        "contractVersion": "2.0",
        "requestId": "paste-1",
        "idempotencyKey": "paste-1",
        "tableId": "orders",
        "schemaRevision": "schema-1",
        "operations": [
            {
                "kind": "insert",
                "recordId": None,
                "values": {
                    "payload": {"nested": [1, True, None, {"name": "原样"}]},
                },
            }
        ],
        "actor": {"type": "user", "id": "local-user", "displayName": None},
        "expectedRevision": None,
        "expectedDigest": None,
    }

    receipt = await client.apply_mutation(request)

    assert receipt["status"] == "applied"
    assert transport.requests == [
        {
            "method": "POST",
            "path": "/api/vibetable/v1/mutations/apply",
            "json_body": request,
            "headers": {"X-VibeTable-Session": "a" * 64},
            "expected_status": (200,),
        }
    ]


@pytest.mark.asyncio
async def test_query_page_uses_query_port_and_preserves_json_values() -> None:
    rows = [{"id": "1", "payload": {"tags": ["a", "b"], "enabled": True}}]
    transport = FakeTransport(
        [
            {
                "rows": rows,
                "offset": 0,
                "limit": 100,
                "filteredRows": 1,
                "totalRows": 1,
                "querySnapshot": {
                    "snapshotId": "0" * 32,
                    "databaseId": "db",
                    "table": "orders",
                    "schemaRevision": "schema-1",
                    "dataRevision": 2,
                    "normalizedQuery": {
                        "filters": [],
                        "sorts": [],
                        "offset": 0,
                        "limit": 100,
                    },
                    "digest": "d" * 64,
                },
            }
        ]
    )
    client = PocketBaseClient(transport=transport, session_secret="b" * 64)

    page = await client.query_page(
        table_id="orders",
        query={"filters": [], "sorts": [], "offset": 0, "limit": 100},
    )

    assert page.rows == rows
    assert page.filtered_rows == 1
    assert transport.requests[0]["path"] == "/api/vibetable/v1/query"
    assert transport.requests[0]["json_body"] == {
        "operation": "page",
        "tableId": "orders",
        "query": {"filters": [], "sorts": [], "offset": 0, "limit": 100},
    }


@pytest.mark.asyncio
async def test_query_cursor_open_and_fetch_forward_opaque_ast_and_token() -> None:
    snapshot = {
        "snapshotId": "0" * 32,
        "databaseId": "db",
        "table": "orders",
        "schemaRevision": "schema-1",
        "dataRevision": 2,
        "normalizedQuery": {"filters": [], "sorts": [], "offset": 0, "limit": 1},
        "digest": "d" * 64,
    }
    transport = FakeTransport(
        [
            {
                "rows": [{"id": "1"}],
                "nextCursor": "opaque-next",
                "hasMore": True,
                "filteredRows": 2,
                "totalRows": 2,
                "querySnapshot": snapshot,
            },
            {
                "rows": [{"id": "2"}],
                "nextCursor": None,
                "hasMore": False,
                "filteredRows": 2,
                "totalRows": 2,
                "querySnapshot": snapshot,
            },
        ]
    )
    client = PocketBaseClient(transport=transport, session_secret="b" * 64)
    ast = {"filters": [{"field": "payload", "operator": "eq", "value": {"x": 1}}], "limit": 1}

    first = await client.open_query_cursor(QueryCursorOpenCommand(table_id="orders", query=ast))
    second = await client.fetch_query_cursor(cursor=first.next_cursor or "")

    assert [row["id"] for row in [*first.rows, *second.rows]] == ["1", "2"]
    assert transport.requests[0]["json_body"] == {
        "operation": "cursor.open",
        "tableId": "orders",
        "query": ast,
    }
    assert transport.requests[1]["json_body"] == {
        "operation": "cursor.fetch",
        "cursor": "opaque-next",
    }


@pytest.mark.asyncio
async def test_selection_projection_forwards_one_atomic_sidecar_read() -> None:
    snapshot = {
        "snapshotId": "0" * 32,
        "databaseId": "db",
        "table": "orders",
        "schemaRevision": "schema_0001",
        "dataRevision": 2,
        "normalizedQuery": {"offset": 0, "limit": 1},
        "digest": "d" * 64,
    }
    transport = FakeTransport(
        [
            {
                "schemaSnapshot": {
                    "contract": "vibetable.schema.v2",
                    "tableId": "orders",
                    "displayName": "Orders",
                    "kind": "base",
                    "schemaRevision": "schema_0001",
                    "dataRevision": 2,
                    "archivePolicy": {
                        "mode": "none",
                        "fieldId": None,
                        "archivedValue": None,
                    },
                    "fields": [],
                    "capabilities": [],
                },
                "cursorWindow": {
                    "rows": [{"id": "1"}],
                    "nextCursor": None,
                    "hasMore": False,
                    "filteredRows": 1,
                    "totalRows": 1,
                    "querySnapshot": snapshot,
                },
            }
        ]
    )
    client = PocketBaseClient(transport=transport, session_secret="b" * 64)

    projection = await client.open_selection_projection(
        QueryCursorOpenCommand(table_id="orders", query={"limit": 1})
    )

    assert projection.schema_snapshot["schemaRevision"] == "schema_0001"
    assert projection.cursor_window.rows == [{"id": "1"}]
    assert transport.requests[0]["json_body"] == {
        "operation": "selection.open",
        "tableId": "orders",
        "query": {"limit": 1},
    }


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("schema_revision", "data_revision", "table_id", "snapshot_data_revision"),
    [
        ("schema_0002", 2, "orders", 2),
        ("schema_0001", 3, "orders", 2),
        ("schema_0001", 2, "customers", 2),
        ("schema_0001", 1, "orders", True),
    ],
)
async def test_selection_projection_rejects_mismatched_revisions_without_retry(
    schema_revision: str,
    data_revision: int,
    table_id: str,
    snapshot_data_revision: object,
) -> None:
    transport = FakeTransport(
        [
            {
                "schemaSnapshot": {
                    "tableId": table_id,
                    "schemaRevision": schema_revision,
                    "dataRevision": data_revision,
                },
                "cursorWindow": {
                    "rows": [],
                    "nextCursor": None,
                    "hasMore": False,
                    "filteredRows": 0,
                    "totalRows": 0,
                    "querySnapshot": {
                        "table": "orders",
                        "schemaRevision": "schema_0001",
                        "dataRevision": snapshot_data_revision,
                    },
                },
            }
        ]
    )
    client = PocketBaseClient(transport=transport, session_secret="b" * 64)

    with pytest.raises(ValueError, match="mismatched selection revisions"):
        await client.open_selection_projection(
            QueryCursorOpenCommand(table_id="orders", query={"limit": 1})
        )

    assert len(transport.requests) == 1


@pytest.mark.asyncio
async def test_aggregate_uses_frozen_query_port_operation() -> None:
    transport = FakeTransport([{"rows": [{"region": "east", "total": 12}]}])
    client = PocketBaseClient(transport=transport, session_secret="d" * 64)
    query = {
        "filters": [],
        "groupBy": ["region"],
        "metrics": [{"function": "sum", "field": "amount", "alias": "total"}],
        "limit": 100,
    }

    rows = await client.aggregate(table_id="orders", query=query)

    assert rows == [{"region": "east", "total": 12}]
    assert transport.requests[0]["json_body"] == {
        "operation": "aggregate",
        "tableId": "orders",
        "aggregate": query,
    }


@pytest.mark.asyncio
async def test_view_query_returns_independently_paged_full_result_groups() -> None:
    page_payload = {
        "rows": [{"id": "order-1"}],
        "offset": 0,
        "limit": 1,
        "filteredRows": 12_500,
        "totalRows": 25_000,
        "querySnapshot": {
            "snapshotId": "0" * 32,
            "databaseId": "db",
            "table": "orders",
            "schemaRevision": "schema-1",
            "dataRevision": 2,
            "normalizedQuery": {"filters": [], "sorts": [], "offset": 0, "limit": 1},
            "digest": "d" * 64,
        },
    }
    transport = FakeTransport(
        [
            {
                "page": page_payload,
                "groupRows": [
                    {
                        "key": ["east", "open"],
                        "count": 3000,
                        "summaries": [5000],
                        "parentCount": 7000,
                        "parentSummaries": [12345],
                    }
                ],
                "groupOffset": 0,
                "groupLimit": 100,
                "hasMoreGroups": False,
            }
        ]
    )
    client = PocketBaseClient(transport=transport, session_secret="e" * 64)
    view = {
        "query": {"filters": [], "sorts": [], "offset": 0, "limit": 1},
        "groups": [{"field": "region"}],
        "summaries": [{"field": "amount", "function": "sum"}],
        "groupOffset": 0,
        "groupLimit": 100,
    }

    result = await client.execute_view(table_id="orders", view=view)

    assert result.page.filtered_rows == 12_500
    assert result.group_rows[0]["count"] == 3000
    assert result.group_rows[0]["parentCount"] == 7000
    assert result.group_rows[0]["parentSummaries"] == [12345]
    assert transport.requests[0]["json_body"] == {
        "operation": "view",
        "tableId": "orders",
        "view": view,
    }


@pytest.mark.asyncio
async def test_lookup_view_uses_one_typed_bounded_query_command() -> None:
    transport = FakeTransport(
        [
            {
                "rows": [],
                "offset": 0,
                "limit": 1,
                "filteredRows": 0,
                "totalRows": 0,
                "querySnapshot": {},
                "groupRows": [],
                "groupOffset": 0,
                "groupLimit": 50,
                "hasMoreGroups": False,
            }
        ]
    )
    client = PocketBaseClient(transport=transport, session_secret="e" * 64)

    await client.query_lookup_view(
        LookupViewQueryCommand(
            table_id="orders",
            schema_revision="schema-1",
            query={"filters": [], "limit": 1},
            groups=[{"field": "region", "direction": "asc"}],
            group_limit=50,
        )
    )

    assert transport.requests[0]["json_body"] == {
        "tableId": "orders",
        "schemaRevision": "schema-1",
        "query": {"filters": [], "limit": 1},
        "groups": [{"field": "region", "direction": "asc"}],
        "groupLimit": 50,
    }


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "group_row",
    [
        {"key": ["east", "open"], "count": 3, "summaries": [], "parentCount": 9},
        {
            "key": ["east"],
            "count": 3,
            "summaries": [],
            "parentCount": 9,
            "parentSummaries": [],
        },
    ],
)
async def test_view_query_rejects_malformed_parent_group_aggregates(
    group_row: dict[str, object],
) -> None:
    transport = FakeTransport(
        [
            {
                "page": {
                    "rows": [],
                    "offset": 0,
                    "limit": 1,
                    "filteredRows": 0,
                    "totalRows": 0,
                    "querySnapshot": {
                        "snapshotId": "0" * 32,
                        "databaseId": "db",
                        "table": "orders",
                        "schemaRevision": "schema-1",
                        "dataRevision": 2,
                        "normalizedQuery": {"offset": 0, "limit": 1},
                        "digest": "d" * 64,
                    },
                },
                "groupRows": [group_row],
                "groupOffset": 0,
                "groupLimit": 100,
                "hasMoreGroups": False,
            }
        ]
    )
    client = PocketBaseClient(transport=transport, session_secret="e" * 64)

    with pytest.raises(ValueError, match="invalid view query result"):
        await client.execute_view(table_id="orders", view={})


def test_product_error_keeps_safe_structured_details() -> None:
    error = PocketBaseProductError(
        status=409,
        payload={
            "contractVersion": "2.0",
            "code": "mutation.digest_conflict",
            "path": "expectedDigest",
            "message": "record changed",
            "details": {"recordId": "row-1"},
            "retryable": False,
        },
    )

    assert error.code == "mutation.digest_conflict"
    assert error.rpc_error_data == {
        "code": "mutation.digest_conflict",
        "path": "expectedDigest",
        "details": {"recordId": "row-1"},
        "retryable": False,
    }
    assert "a" * 64 not in str(error)


@pytest.mark.asyncio
async def test_lookup_export_calls_describe_and_lookup_product_routes() -> None:
    transport = FakeTransport(
        [
            {
                "tableId": "orders",
                "schemaRevision": "schema_7",
                "lookups": [
                    {
                        "lookupId": "lookup-price",
                        "fieldId": "field-price",
                        "physicalName": "contract_price",
                    }
                ],
            },
            {
                "rows": [{"id": "1", "contract_price": 12.5}],
                "offset": 0,
                "limit": 100,
                "filteredRows": 1,
                "totalRows": 1,
                "querySnapshot": {},
            },
        ]
    )
    client = PocketBaseClient(transport=transport, session_secret="c" * 64)

    catalog = await client.describe_lookups("orders")
    page = await client.query_lookups(
        table_id="orders",
        schema_revision="schema_7",
        query={"filters": [], "sorts": [], "offset": 0, "limit": 100},
    )

    assert catalog["lookups"][0]["physicalName"] == "contract_price"
    assert page.rows[0]["contract_price"] == 12.5
    assert transport.requests[0]["method"] == "GET"
    assert transport.requests[0]["path"] == "/api/vibetable/v1/lookups/describe"
    assert transport.requests[0]["query"] == {"tableId": "orders"}
    assert transport.requests[1]["path"] == "/api/vibetable/v1/lookups/query"


@pytest.mark.asyncio
async def test_realtime_reconcile_uses_product_route_and_validates_action() -> None:
    transport = FakeTransport(
        [
            {
                "tableId": "orders",
                "clientSchemaRevision": "schema_0001",
                "clientDataRevision": "data_0001",
                "currentSchemaRevision": "schema_0001",
                "currentDataRevision": "data_0002",
                "action": "refresh-data",
            }
        ]
    )
    client = PocketBaseClient(transport=transport, session_secret="e" * 64)

    result = await client.reconcile_realtime(
        table_id="orders",
        schema_revision="schema_0001",
        data_revision="data_0001",
    )

    assert result["action"] == "refresh-data"
    assert transport.requests[0]["path"] == "/api/vibetable/v1/events/reconcile"
