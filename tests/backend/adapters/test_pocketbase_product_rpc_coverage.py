from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from pydantic import ValidationError

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, ProductParams


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
    service, _transport = service_with([{"fields": [non_finite]}])

    with pytest.raises(ValueError, match="non-finite JSON number"):
        await service.invoke(
            "field.recycleBin.list", ProductParams.model_validate({"tableId": "orders"})
        )


@pytest.mark.asyncio
async def test_closed_routes_cover_schema_formula_file_and_remove_only_attachment() -> None:
    service, transport = service_with(
        [
            {
                "contract": "vibetable.schema.v2",
                "operationId": "operation-create-table-12345678",
                "tableId": "tbl_orders",
                "displayName": "订单",
                "schemaRevision": "schema_0001",
            },
            {"valid": True, "diagnostics": []},
            {"downloadCapability": "opaque", "contractVersion": "2.0"},
            {"status": "applied"},
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
    with pytest.raises(ValueError, match=r"unknown product RPC method: query\.readRows"):
        await service.invoke(
            "query.readRows",
            ProductParams.model_validate({"tableId": "orders", "rowIds": [""]}),
        )
    with pytest.raises(ValueError, match=r"unknown product RPC method: schema\.list"):
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
    service, transport = service_with([])
    with pytest.raises(ValueError, match=r"unknown product RPC method: query\.validateSnapshot"):
        await service.invoke(
            "query.validateSnapshot",
            ProductParams.model_validate(
                {"snapshot": {"digest": "x"}, "currentQuery": {"limit": 10}}
            ),
        )

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
    assert transport.requests == []


@pytest.mark.asyncio
async def test_migrated_selection_open_cannot_reenter_python_transport() -> None:
    service, transport = service_with([])
    with pytest.raises(ValueError, match=r"unknown product RPC method: query\.selectionOpen"):
        await service.invoke(
            "query.selectionOpen",
            ProductParams.model_validate({"tableId": "orders", "query": {"limit": 10}}),
        )
    assert transport.requests == []
