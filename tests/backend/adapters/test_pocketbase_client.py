from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.pocketbase.client import (
    PocketBaseClient,
    PocketBaseProductError,
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
                "contractVersion": "1.0",
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
        "contractVersion": "1.0",
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


def test_product_error_keeps_safe_structured_details() -> None:
    error = PocketBaseProductError(
        status=409,
        payload={
            "contractVersion": "1.0",
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
