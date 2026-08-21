from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from pydantic import ValidationError

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, ProductParams
from tests.backend.schema_v2_fixtures import field_v2, snapshot_v2


class ScriptedTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        assert self.responses, f"unexpected request: {method} {path}"
        return self.responses.pop(0)

    async def request_multipart(self, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": "MULTIPART", "path": path, **kwargs})
        assert self.responses, f"unexpected multipart request: {path}"
        return self.responses.pop(0)

    async def download_to_file(self, path: str, **kwargs: Any) -> int:
        self.requests.append({"method": "DOWNLOAD", "path": path, **kwargs})
        return 17


def service_with(
    responses: list[Any],
) -> tuple[PocketBaseProductRpc, ScriptedTransport]:
    transport = ScriptedTransport(responses)
    client = PocketBaseClient(transport=transport, session_secret="s" * 64)
    return (
        PocketBaseProductRpc(
            client=client,
            transport=transport,
            session_secret="s" * 64,
        ),
        transport,
    )


def scalar_field(
    field_id: str = "name",
    *,
    physical_name: str | None = None,
) -> dict[str, Any]:
    return {
        "fieldId": field_id,
        "physicalName": physical_name or field_id,
        "displayName": field_id.title(),
        "kind": "scalar",
        "dataType": "shortText",
        "storageType": "text",
        "nullable": True,
        "defaultValue": None,
        "constraints": [],
        "editor": {"kind": "text", "config": {}},
        "readOnly": False,
        "formula": None,
        "relation": None,
        "lookup": None,
        "attachmentPolicy": None,
    }


def formula_v2_field() -> dict[str, Any]:
    path = Path(__file__).parents[3] / "contracts/schema-v2/fixtures/field-definition.json"
    field = json.loads(path.read_text(encoding="utf-8"))
    field["logicalType"] = "formula"
    field["storage"]["kind"] = "computed"
    field["value"]["presence"] = {"mode": "computed"}
    field["display"]["kind"] = "readonly"
    field["formula"] = {
        "language": "cel-v1",
        "source": "price * quantity",
        "resultType": "number",
    }
    return field


def relation_field(
    field_id: str = "customer",
    *,
    target: str = "customers",
) -> dict[str, Any]:
    return {
        **scalar_field(field_id),
        "kind": "relation",
        "dataType": "relation",
        "storageType": "relation",
        "relation": {
            "targetTableId": target,
            "cardinality": "one",
            "deletePolicy": "setNull",
        },
    }


def table_schema(
    table_id: str,
    fields: list[dict[str, Any]],
    revision: str = "schema_3",
) -> dict[str, Any]:
    return {
        "contractVersion": "2.0",
        "tableId": table_id,
        "physicalName": table_id,
        "displayName": table_id.title(),
        "kind": "base",
        "schemaRevision": revision,
        "archivePolicy": {"mode": "none", "fieldId": None, "archivedValue": None},
        "fields": fields,
        "indexes": [],
    }


def lookup_renderer(
    *,
    revision: int = 1,
    output_type: str = "text",
) -> dict[str, Any]:
    return {
        "lookupId": "orders.customer_name",
        "collection": "orders",
        "fieldKey": "customer_name",
        "displayName": "Customer name",
        "path": [{"relationId": "orders.customer"}],
        "source": {"kind": "target_field", "fieldRef": "name"},
        "outputType": output_type,
        "revision": revision,
        "state": "valid",
        "diagnostics": [],
        "dependencies": [],
    }


def lookup_descriptor(revision: int = 1) -> dict[str, Any]:
    return {
        "lookupId": "orders.customer_name",
        "tableId": "orders",
        "fieldId": "customer_name",
        "physicalName": "customer_name",
        "displayName": "Customer name",
        "relationFieldId": "customer",
        "targetFieldId": "name",
        "resultCardinality": "one",
        "outputStorage": "text",
        "revision": revision,
    }


def page(rows: list[dict[str, Any]], *, offset: int = 0, limit: int = 50) -> dict[str, Any]:
    return {
        "rows": rows,
        "offset": offset,
        "limit": limit,
        "filteredRows": len(rows),
        "totalRows": len(rows),
        "querySnapshot": {"digest": "snapshot"},
    }


def test_product_params_enforce_json_size_depth_and_closed_shapes() -> None:
    with pytest.raises(ValidationError, match="must be an object"):
        ProductParams.model_validate([])
    with pytest.raises(ValidationError, match="forbidden field"):
        ProductParams.model_validate({1: "not-a-string-key"})
    with pytest.raises(ValidationError, match="JSON values"):
        ProductParams.model_validate({"value": float("nan")})
    with pytest.raises(ValidationError, match="safe size limit"):
        ProductParams.model_validate({"value": "x" * (1 << 20)})

    nested: dict[str, Any] = {}
    cursor = nested
    for _ in range(34):
        cursor["next"] = {}
        cursor = cursor["next"]
    with pytest.raises(ValidationError, match="too deeply nested"):
        ProductParams.model_validate(nested)

    schema_list = PRODUCT_RPC_REGISTRY["schema.list"]
    assert schema_list.model_validate({}).root == {}
    with pytest.raises(ValidationError, match="unknown fields"):
        schema_list.model_validate({"extra": True})

    query_model = PRODUCT_RPC_REGISTRY["query.page"]
    with pytest.raises(ValidationError, match="omit required fields"):
        query_model.model_validate({"tableId": "orders"})
    with pytest.raises(ValidationError, match="wrong type"):
        query_model.model_validate({"tableId": "orders", "query": []})
    with pytest.raises(ValidationError, match="must not be empty"):
        query_model.model_validate({"tableId": "", "query": {}})

    cursor_open = PRODUCT_RPC_REGISTRY["query.cursorOpen"]
    assert cursor_open.model_validate({"tableId": "orders", "query": {"limit": 50}}).root[
        "query"
    ] == {"limit": 50}
    selection_open = PRODUCT_RPC_REGISTRY["query.selectionOpen"]
    assert selection_open.model_validate({"tableId": "orders", "query": {"limit": 50}}).root[
        "query"
    ] == {"limit": 50}
    cursor_fetch = PRODUCT_RPC_REGISTRY["query.cursorFetch"]
    assert cursor_fetch.model_validate({"cursor": "opaque"}).root == {"cursor": "opaque"}
    with pytest.raises(ValidationError, match="unknown fields"):
        cursor_fetch.model_validate({"cursor": "opaque", "tableId": "orders"})


@pytest.mark.parametrize(
    "non_finite",
    [float("nan"), float("inf"), float("-inf")],
    ids=["nan", "positive-infinity", "negative-infinity"],
)
@pytest.mark.asyncio
async def test_public_invoke_rejects_non_finite_product_response(non_finite: float) -> None:
    service, _transport = service_with([{"tables": [non_finite]}])

    with pytest.raises(ValueError, match="non-finite JSON number"):
        await service.invoke("schema.list", ProductParams.model_validate({}))


@pytest.mark.asyncio
async def test_selection_open_returns_one_strict_revision_matched_projection() -> None:
    schema = snapshot_v2("orders", [field_v2("name")], revision="schema_0001")
    snapshot = {
        "snapshotId": "0" * 32,
        "digest": "d" * 64,
        "databaseId": "db",
        "table": "orders",
        "schemaRevision": "schema_0001",
        "dataRevision": 1,
        "normalizedQuery": {"offset": 0, "limit": 10},
    }
    service, transport = service_with(
        [
            {
                "schemaSnapshot": schema,
                "cursorWindow": {
                    "rows": [{"id": "row-1"}],
                    "nextCursor": None,
                    "hasMore": False,
                    "filteredRows": 1,
                    "totalRows": 1,
                    "querySnapshot": snapshot,
                },
            }
        ]
    )

    result = await service.invoke(
        "query.selectionOpen",
        PRODUCT_RPC_REGISTRY["query.selectionOpen"].model_validate(
            {"tableId": "orders", "query": {"limit": 10}}
        ),
    )

    assert result["schemaSnapshot"]["schemaRevision"] == "schema_0001"
    assert result["cursorWindow"]["querySnapshot"]["dataRevision"] == 1
    assert transport.requests[0]["json_body"]["operation"] == "selection.open"
    assert transport.responses == []


@pytest.mark.asyncio
async def test_selection_open_rejects_malformed_schema_without_retry() -> None:
    service, transport = service_with(
        [
            {
                "schemaSnapshot": {
                    "tableId": "orders",
                    "schemaRevision": "schema_0001",
                    "dataRevision": 1,
                },
                "cursorWindow": {
                    "rows": [],
                    "nextCursor": None,
                    "hasMore": False,
                    "filteredRows": 0,
                    "totalRows": 0,
                    "querySnapshot": {
                        "snapshotId": "0" * 32,
                        "digest": "d" * 64,
                        "databaseId": "db",
                        "table": "orders",
                        "schemaRevision": "schema_0001",
                        "dataRevision": 1,
                        "normalizedQuery": {"offset": 0, "limit": 10},
                    },
                },
            }
        ]
    )

    with pytest.raises(ValidationError):
        await service.invoke(
            "query.selectionOpen",
            ProductParams.model_validate({"tableId": "orders", "query": {"limit": 10}}),
        )

    assert len(transport.requests) == 1


@pytest.mark.asyncio
async def test_closed_routes_cover_query_mutation_formula_file_and_remove_only_attachment() -> None:
    service, transport = service_with(
        [
            {
                "contract": "vibetable.schema.v2",
                "operationId": "operation-create-table-12345678",
                "tableId": "tbl_orders",
                "displayName": "订单",
                "schemaRevision": "schema_0001",
            },
            page([{"id": "row-1"}], limit=1),
            {"canApply": True, "operations": []},
            {"status": "applied", "receipt": {"id": "change-1"}},
            {"valid": True, "diagnostics": []},
            {"downloadCapability": "opaque", "contractVersion": "2.0"},
            {"status": "applied"},
            {"items": [{"tableId": "customers", "recordId": "c-1", "label": "Ada"}], "total": 1},
            {
                "target": {
                    "tableId": "customers",
                    "recordId": "c-2",
                    "label": "Grace",
                }
            },
            {"current": [{"tableId": "customers", "recordId": "c-1", "label": "Ada"}]},
        ]
    )

    assert (
        await service.invoke(
            "schema.table.create",
            PRODUCT_RPC_REGISTRY["schema.table.create"].model_validate(
                {
                    "displayName": "订单",
                    "operationId": "operation-create-table-12345678",
                    "actor": {"id": "desktop-host", "kind": "host"},
                }
            ),
        )
    )["tableId"] == "tbl_orders"
    queried = await service.invoke(
        "query.page", ProductParams.model_validate({"tableId": "orders", "query": {"limit": 1}})
    )
    assert queried["rows"] == [{"id": "row-1"}]
    assert queried["snapshot"] == {"digest": "snapshot"}
    assert (
        await service.invoke("mutation.preview", ProductParams.model_validate({"operations": []}))
    )["canApply"] is True
    assert (
        await service.invoke("mutation.apply", ProductParams.model_validate({"operations": []}))
    )["status"] == "applied"
    assert (
        await service.invoke(
            "formula.validate",
            ProductParams.model_validate({"tableId": "orders", "field": formula_v2_field()}),
        )
    )["valid"] is True
    await service.invoke(
        "file.token",
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice.pdf",
                "variant": "thumb",
            }
        ),
    )
    removed = await service.invoke(
        "file.applyHostChange",
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "schemaRevision": "schema_3",
                "expectedDigest": "sha256:" + "a" * 64,
                "hostPaths": [],
                "removeStoredNames": ["old.pdf"],
            }
        ),
    )
    assert removed["status"] == "applied"

    searched = await service.invoke(
        "relation.searchTargets",
        ProductParams.model_validate(
            {
                "relationId": "orders.customer",
                "query": "ad",
                "offset": 0,
                "limit": 10,
            }
        ),
    )
    assert searched["items"][0]["collection"] == "customers"
    created = await service.invoke(
        "relation.createTarget",
        PRODUCT_RPC_REGISTRY["relation.createTarget"].model_validate(
            {
                "relationId": "orders.customer",
                "label": "Grace",
                "idempotencyKey": "create-customer-1",
            }
        ),
    )
    assert created == {
        "outcome": "committed",
        "target": {
            "collection": "customers",
            "itemId": "c-2",
            "label": "Grace",
            "secondaryLabel": None,
        },
        "requestId": "create-customer-1",
    }
    assert transport.requests[-1]["path"] == "/api/vibetable/v1/relations/create-target"
    assert "targetTableId" not in transport.requests[-1]["json_body"]
    applied = await service.invoke(
        "relation.applyDelta",
        ProductParams.model_validate(
            {
                "relationId": "orders.customer",
                "sourceItemId": "row-1",
                "expectedSchemaRevision": "schema_3",
                "adds": [{"collection": "customers", "itemId": "c-1"}],
                "removes": [],
                "updates": [],
                "idempotencyKey": "relation-op-1",
            }
        ),
    )
    assert applied["outcome"] == "committed"
    assert applied["current"][0]["itemId"] == "c-1"
    assert transport.responses == []


@pytest.mark.asyncio
async def test_route_validation_rejects_bad_rows_attachments_files_and_history() -> None:
    service, transport = service_with([])
    with pytest.raises(ValueError, match="rowIds"):
        await service.invoke(
            "query.readRows",
            ProductParams.model_validate({"tableId": "orders", "rowIds": [""]}),
        )
    with pytest.raises(ValueError, match="does not accept"):
        await service.invoke("schema.list", ProductParams.model_validate({"unexpected": 1}))
    with pytest.raises(ValueError, match="variant must be a string"):
        await service.invoke(
            "file.token",
            ProductParams.model_validate(
                {
                    "tableId": "orders",
                    "recordId": "row-1",
                    "fieldId": "invoice",
                    "storedName": "invoice.pdf",
                    "variant": 1,
                }
            ),
        )
    for change in (
        {"hostPaths": [], "removeStoredNames": []},
        {"hostPaths": [""] * 33, "removeStoredNames": []},
    ):
        with pytest.raises(ValueError, match="attachment change"):
            await service.invoke(
                "file.applyHostChange",
                ProductParams.model_validate(
                    {
                        "tableId": "orders",
                        "recordId": "row-1",
                        "fieldId": "invoice",
                        "schemaRevision": "schema_3",
                        "expectedDigest": "sha256:" + "a" * 64,
                        **change,
                    }
                ),
            )
    with pytest.raises(ValueError, match="expectedDigest"):
        await service.invoke(
            "file.applyHostChange",
            ProductParams.model_validate(
                {
                    "tableId": "orders",
                    "recordId": "row-1",
                    "fieldId": "invoice",
                    "schemaRevision": "schema_3",
                    "expectedDigest": "bad",
                    "hostPaths": [],
                    "removeStoredNames": ["old.pdf"],
                }
            ),
        )
    with pytest.raises(ValueError, match="actions"):
        await service.invoke(
            "history.read", ProductParams.model_validate({"collection": "orders", "actions": [""]})
        )
    with pytest.raises(ValueError, match="field"):
        await service.invoke(
            "history.previewRestore",
            ProductParams.model_validate(
                {
                    "collection": "orders",
                    "itemId": "row-1",
                    "targetRevision": "rev-1",
                    "scope": "field",
                    "field": "",
                }
            ),
        )
    with pytest.raises(ValidationError, match="unknown fields: collection"):
        PRODUCT_RPC_REGISTRY["relation.searchTargets"].model_validate(
            {"relationId": "orders.customer", "collection": "customers"}
        )
    assert transport.requests == []


@pytest.mark.asyncio
async def test_lookup_grouped_query_returns_renderer_envelope() -> None:
    descriptor = lookup_descriptor()
    revision = "sha256:86864bd289da0d7c8dc42fb321de98f66b1e69fc7ac41f853f2843b044b9700b"
    query_rows = [
        {"id": "o-1", "customer_name": "Ada", "region": "EU"},
        {"id": "o-2", "customer_name": "Bob", "region": "US"},
        {"id": "o-3", "customer_name": "Ana", "region": "EU"},
    ]
    service, transport = service_with(
        [
            {
                "tableId": "orders",
                "schemaRevision": "schema_3",
                "relations": [],
                "lookups": [descriptor],
            },
            {
                **page(query_rows[:2], limit=2),
                "groupRows": [
                    {
                        "key": ["US", "Bob"],
                        "count": 1,
                        "summaries": [],
                        "parentCount": 1,
                        "parentSummaries": [],
                    },
                    {
                        "key": ["EU", "Ada"],
                        "count": 1,
                        "summaries": [],
                        "parentCount": 2,
                        "parentSummaries": [],
                    },
                    {
                        "key": ["EU", "Ana"],
                        "count": 1,
                        "summaries": [],
                        "parentCount": 2,
                        "parentSummaries": [],
                    },
                ],
                "groupOffset": 0,
                "groupLimit": 5000,
                "hasMoreGroups": False,
            },
        ]
    )

    result = await service.invoke(
        "lookup.query",
        ProductParams.model_validate(
            {
                "collection": "orders",
                "fieldRefs": ["customer_name"],
                "query": {
                    "offset": 0,
                    "limit": 2,
                    "groups": [
                        {"fieldRef": "region", "direction": "desc"},
                        {"fieldRef": "customer_name", "direction": "asc"},
                    ],
                },
                "requestGeneration": 8,
                "schemaRevision": "schema_3",
                "permissionRevision": "schema_3",
                "lookupRevision": revision,
            }
        ),
    )
    assert result["columns"][0]["outputType"] == "text"
    assert result["groups"][0]["key"] == "US"
    assert any(group["path"] == ["EU"] for group in result["groups"])
    lookup_request = transport.requests[1]["json_body"]
    assert lookup_request["groups"] == [
        {"field": "region", "direction": "desc"},
        {"field": "customer_name", "direction": "asc"},
    ]
    assert lookup_request["groupLimit"] == 5000
    assert transport.responses == []


@pytest.mark.asyncio
async def test_lookup_grouped_query_fails_closed_when_group_window_is_exhausted() -> None:
    descriptor = lookup_descriptor()
    revision = "sha256:86864bd289da0d7c8dc42fb321de98f66b1e69fc7ac41f853f2843b044b9700b"
    service, transport = service_with(
        [
            {
                "tableId": "orders",
                "schemaRevision": "schema_3",
                "relations": [],
                "lookups": [descriptor],
            },
            {
                **page([], limit=1),
                "groupRows": [],
                "groupOffset": 0,
                "groupLimit": 5000,
                "hasMoreGroups": True,
            },
        ]
    )

    with pytest.raises(ValueError, match="bounded window"):
        await service.invoke(
            "lookup.query",
            ProductParams.model_validate(
                {
                    "collection": "orders",
                    "fieldRefs": ["customer_name"],
                    "query": {
                        "limit": 1,
                        "groups": [{"fieldRef": "customer_name", "direction": "asc"}],
                    },
                    "requestGeneration": 9,
                    "schemaRevision": "schema_3",
                    "permissionRevision": "schema_3",
                    "lookupRevision": revision,
                }
            ),
        )

    assert len(transport.requests) == 2
    assert transport.responses == []


@pytest.mark.asyncio
async def test_single_relation_update_translates_current_and_desired_targets() -> None:
    descriptor = {
        "relationId": "orders.customer",
        "sourceTableId": "orders",
        "sourceFieldId": "customer",
        "physicalName": "customer_id",
        "targetTableId": "customers",
        "cardinality": "one",
        "deletePolicy": "setNull",
    }
    service, transport = service_with(
        [
            {
                "tableId": "orders",
                "schemaRevision": "schema_3",
                "relations": [descriptor],
                "lookups": [],
            },
            {"rows": [{"id": "o-1", "customer_id": "c-old"}]},
            {"receipt": {"changeSetId": "change-1"}},
        ]
    )

    result = await service.invoke(
        "relation.updateSingle",
        ProductParams.model_validate(
            {
                "relationId": "orders.customer",
                "sourceItemId": "o-1",
                "target": {
                    "collection": "customers",
                    "itemId": "c-new",
                    "label": "New customer",
                },
                "expectedSchemaRevision": "schema_3",
                "idempotencyKey": "single-relation-1",
                "expectedDigest": "sha256:" + "b" * 64,
            }
        ),
    )

    assert result["outcome"] == "committed"
    assert result["current"]["itemId"] == "c-new"
    mutation = transport.requests[2]["json_body"]
    assert mutation["adds"] == [
        {"tableId": "customers", "recordId": "c-new", "label": "New customer"}
    ]
    assert mutation["removes"] == [{"tableId": "customers", "recordId": "c-old", "label": "c-old"}]
    assert mutation["actor"]["id"] == "local-user"
    assert transport.responses == []


@pytest.mark.asyncio
async def test_small_service_boundaries_cover_optional_and_invalid_catalog_paths() -> None:
    service, transport = service_with(
        [
            snapshot_v2("orders", [field_v2("name")], revision="schema_3"),
            {"valid": True},
            {
                "tableId": "orders",
                "schemaRevision": "schema_3",
                "relations": [],
                "lookups": [],
            },
        ]
    )
    assert (
        await service.invoke(
            "schema.getTable",
            ProductParams.model_validate({"tableId": "orders"}),
        )
    )["tableId"] == "orders"
    await service.invoke(
        "query.validateSnapshot",
        ProductParams.model_validate({"snapshot": {"digest": "x"}, "currentQuery": {"limit": 10}}),
    )
    assert transport.requests[1]["json_body"]["currentQuery"] == {"limit": 10}

    with pytest.raises(ValueError, match="variant"):
        await service.invoke(
            "file.saveHostFile",
            ProductParams.model_validate(
                {
                    "tableId": "orders",
                    "recordId": "row-1",
                    "fieldId": "invoice",
                    "storedName": "invoice.pdf",
                    "outputPath": "invoice.pdf",
                    "variant": "",
                }
            ),
        )
    with pytest.raises(ValueError, match=r"query\.groups"):
        await service.invoke(
            "lookup.query",
            ProductParams.model_validate(
                {
                    "collection": "orders",
                    "fieldRefs": [],
                    "query": {"groups": "bad"},
                    "requestGeneration": 1,
                    "schemaRevision": "schema_3",
                    "permissionRevision": "schema_3",
                    "lookupRevision": "stale",
                }
            ),
        )
    with pytest.raises(ValueError, match="revisions are stale"):
        await service.invoke(
            "lookup.query",
            ProductParams.model_validate(
                {
                    "collection": "orders",
                    "fieldRefs": [],
                    "query": {},
                    "requestGeneration": 1,
                    "schemaRevision": "schema_2",
                    "permissionRevision": "schema_2",
                    "lookupRevision": "stale",
                }
            ),
        )
