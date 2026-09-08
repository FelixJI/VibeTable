from __future__ import annotations

import inspect
import json
from pathlib import Path
from typing import Any

import pytest
from pydantic import ValidationError

from backend.adapters.pocketbase import product_rpc
from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.product_query_schema_rpc import (
    ProductQuerySchemaRpc,
)
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, ProductParams


def _formula_v2_field() -> dict[str, Any]:
    path = Path(__file__).parents[3] / "contracts/schema-v2/fixtures/field-definition.json"
    field = json.loads(path.read_text(encoding="utf-8"))
    field["logicalType"] = "formula"
    field["storage"]["kind"] = "computed"
    field["value"]["presence"] = {"mode": "computed"}
    field["display"]["kind"] = "readonly"
    field["formula"] = {"language": "cel-v1", "source": "1 + 1", "resultType": "number"}
    return field


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        return self.responses.pop(0)

    async def request_multipart(self, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": "MULTIPART", "path": path, **kwargs})
        return self.responses.pop(0)

    async def download_to_file(self, path: str, **kwargs: Any) -> int:
        self.requests.append({"method": "DOWNLOAD", "path": path, **kwargs})
        return 12


def _service(responses: list[Any]) -> tuple[PocketBaseProductRpc, FakeTransport]:
    transport = FakeTransport(responses)
    client = PocketBaseClient(transport=transport, session_secret="a" * 64)
    return (
        PocketBaseProductRpc(
            client=client,
            transport=transport,
            session_secret="a" * 64,
        ),
        transport,
    )


def test_params_reject_credentials_recursively() -> None:
    with pytest.raises(ValidationError):
        ProductParams.model_validate({"nested": {"sessionSecret": "secret"}})


def test_root_adapter_exposes_only_the_closed_invoke_interface() -> None:
    public_async_methods = {
        name
        for name, member in inspect.getmembers(PocketBaseProductRpc, inspect.iscoroutinefunction)
        if not name.startswith("_")
    }

    assert public_async_methods == {"invoke"}


def test_adapter_rejects_a_missing_current_python_route(monkeypatch: pytest.MonkeyPatch) -> None:
    class MissingRouteModule(ProductQuerySchemaRpc):
        def __init__(self, context: product_rpc.PocketBaseProductContext) -> None:
            super().__init__(context)
            self.methods = self.methods - {"field.recycleBin.list"}

    monkeypatch.setattr(product_rpc, "ProductQuerySchemaRpc", MissingRouteModule)
    with pytest.raises(RuntimeError, match="routes do not match the contract registry"):
        _service([])


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("method", "params"),
    [
        (
            "events.reconcile",
            {
                "tableId": "orders",
                "schemaRevision": "schema_0001",
                "dataRevision": "data_0001",
            },
        ),
        ("file.list", {"tableId": "t", "recordId": "r", "fieldId": "f"}),
        (
            "lookup.query",
            {
                "contract": "vibetable.lookup-query.v1",
                "collection": "orders",
                "fieldRefs": ["customer_name"],
                "query": {},
                "requestGeneration": 7,
                "schemaRevision": "schema-1",
                "permissionRevision": "schema-1",
                "lookupRevision": "lookup-1",
            },
        ),
        ("relation.searchTargets", {"relationId": "orders.customer"}),
        (
            "lookup.valuePage",
            {
                "collection": "records",
                "fieldRef": "owner.name",
                "sourceRecordId": "record-1",
                "schemaRevision": "s1",
                "permissionRevision": "p1",
                "lookupRevision": "l1",
                "offset": 0,
                "limit": 10,
            },
        ),
        (
            "relation.previewDelta",
            {
                "relationId": "orders.related",
                "sourceItemId": "source-1",
                "expectedSchemaRevision": "schema-1",
                "adds": [],
                "removes": [],
                "idempotencyKey": "preview-1",
            },
        ),
        ("schema.getTable", {"tableId": "orders"}),
        ("schema.list", {}),
        ("query.page", {"tableId": "orders", "query": {}}),
        ("query.selectionOpen", {"tableId": "orders", "query": {}}),
        ("query.cursorOpen", {"tableId": "orders", "query": {}}),
        ("query.cursorFetch", {"cursor": "opaque"}),
        ("schema.describe", {"collection": "orders", "requestGeneration": 1, "accepts": []}),
    ],
)
async def test_go_owned_methods_are_retired_from_python_adapter_without_transport(
    method: str, params: dict[str, object]
) -> None:
    service, transport = _service([{"tables": []}])
    validated = PRODUCT_RPC_REGISTRY[method].model_validate(params)

    with pytest.raises(ValueError, match=rf"unknown product RPC method: {method}"):
        await service.invoke(method, validated)

    assert transport.requests == []


def test_schema_v2_plan_params_defer_domain_validation_but_keep_transport_closed() -> None:
    accepted = PRODUCT_RPC_REGISTRY["field.change.plan"].model_validate(
        {
            "action": "create",
            "tableId": "orders",
            "fieldId": "",
            "expectedSchemaRevision": "schema_0001",
            "expectedDataRevision": None,
            "draft": {},
            "actor": {"id": "local-user", "kind": "user"},
            "conversionRule": "",
            "confirmation": "",
            "backupReceipt": "",
            "relationPair": {
                "reciprocalDisplayName": "订单",
                "reciprocalCardinality": "many",
                "sourceDisplayFieldId": "fld_order_number",
            },
        }
    )
    assert accepted.root["draft"] == {}
    assert accepted.root["relationPair"]["sourceDisplayFieldId"] == "fld_order_number"

    with pytest.raises(ValidationError):
        PRODUCT_RPC_REGISTRY["field.change.plan"].model_validate(
            {
                "action": "create",
                "tableId": "orders",
                "fieldId": "",
                "expectedSchemaRevision": "schema_0001",
                "expectedDataRevision": None,
                "draft": {},
                "actor": {"id": "local-user", "kind": "user"},
                "conversionRule": "",
                "confirmation": "",
                "backupReceipt": "",
                "providerUrl": "http://127.0.0.1:8090",
            }
        )


def test_schema_v2_apply_params_reject_open_nested_objects() -> None:
    with pytest.raises(ValidationError):
        PRODUCT_RPC_REGISTRY["field.change.apply"].model_validate(
            {
                "planId": "plan_1",
                "planHash": "a" * 64,
                "operationId": "op_1",
                "actor": {"id": "local-user", "kind": "user", "admin": True},
                "confirmations": [],
            }
        )


@pytest.mark.asyncio
async def test_field_settings_methods_use_only_frozen_v2_routes() -> None:
    service, transport = _service(
        [
            {"contract": "vibetable.schema.v2", "definition": None},
            {"contract": "vibetable.schema.v2", "planId": "plan_1"},
            {"contract": "vibetable.schema.v2", "operationId": "op_1"},
            {"contract": "vibetable.schema.v2", "fields": []},
        ]
    )
    describe = PRODUCT_RPC_REGISTRY["field.settings.describe"].model_validate(
        {"tableId": "orders", "fieldId": "fld_12345678"}
    )
    plan = PRODUCT_RPC_REGISTRY["field.change.plan"].model_validate(
        {
            "action": "retire",
            "tableId": "orders",
            "fieldId": "fld_12345678",
            "expectedSchemaRevision": "schema_0001",
            "expectedDataRevision": None,
            "draft": None,
            "actor": {"id": "local-user", "kind": "user"},
            "conversionRule": "",
            "confirmation": "",
            "backupReceipt": "",
            "relationPair": {
                "reciprocalDisplayName": "订单",
                "reciprocalCardinality": "many",
                "sourceDisplayFieldId": "fld_order_number",
            },
        }
    )
    apply = PRODUCT_RPC_REGISTRY["field.change.apply"].model_validate(
        {
            "planId": "plan_1",
            "planHash": "a" * 64,
            "operationId": "op_1",
            "actor": {"id": "local-user", "kind": "user"},
            "confirmations": [],
        }
    )
    recycle = PRODUCT_RPC_REGISTRY["field.recycleBin.list"].model_validate({"tableId": "orders"})

    await service.invoke("field.settings.describe", describe)
    await service.invoke("field.change.plan", plan)
    await service.invoke("field.change.apply", apply)
    await service.invoke("field.recycleBin.list", recycle)

    assert [request["path"] for request in transport.requests] == [
        "/api/vibetable/v2/field-settings/orders",
        "/api/vibetable/v2/field-change/plan",
        "/api/vibetable/v2/field-change/apply",
        "/api/vibetable/v2/field-recycle-bin/orders",
    ]
    assert transport.requests[1]["json_body"]["relationPair"] == {
        "reciprocalDisplayName": "订单",
        "reciprocalCardinality": "many",
        "sourceDisplayFieldId": "fld_order_number",
    }


@pytest.mark.asyncio
async def test_schema_formula_and_file_use_only_fixed_routes() -> None:
    service, transport = _service(
        [
            {
                "contract": "vibetable.schema.v2",
                "operationId": "operation-create-table-12345678",
                "tableId": "tbl_orders",
                "displayName": "订单",
                "schemaRevision": "schema_0001",
            },
            {
                "canonicalSource": 'relationSum(f_lines, "f_amount")',
                "resultType": "number",
                "dependencies": [],
                "relationAggregatePaths": ["f_lines.f_amount"],
            },
            {"values": {"subtotal": 12}},
            {"contractVersion": "2.0", "downloadCapability": "cap"},
        ]
    )

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
    inspected = await service.invoke(
        "formula.draft.validate",
        PRODUCT_RPC_REGISTRY["formula.draft.validate"].model_validate(
            {"tableId": "orders", "displaySource": "SUM({明细}.{金额})"}
        ),
    )
    await service.invoke(
        "formula.preview",
        ProductParams.model_validate(
            {"tableId": "orders", "field": _formula_v2_field(), "row": {}, "changedFieldIds": []}
        ),
    )
    token = await service.invoke(
        "file.token",
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice.pdf",
            }
        ),
    )

    assert token["downloadCapability"] == "cap"
    assert inspected["resultType"] == "number"
    assert [request["path"] for request in transport.requests] == [
        "/api/vibetable/v2/schema/tables",
        "/api/vibetable/v1/formulas/draft/validate",
        "/api/vibetable/v1/formulas/preview",
        "/api/vibetable/v1/files/token",
    ]
    assert all(
        request["headers"] == {"X-VibeTable-Session": "a" * 64} for request in transport.requests
    )


@pytest.mark.asyncio
async def test_schema_delete_uses_fixed_route_and_revision_guard() -> None:
    service, transport = _service([{"deleted": True, "tableId": "orders"}])

    result = await service.invoke(
        "schema.delete",
        ProductParams.model_validate({"tableId": "orders", "expectedRevision": "schema_0002"}),
    )

    assert result == {"deleted": True, "tableId": "orders"}
    assert transport.requests[0]["method"] == "POST"
    assert transport.requests[0]["path"] == "/api/vibetable/v1/schema/delete"
    assert transport.requests[0]["json_body"] == {
        "tableId": "orders",
        "expectedRevision": "schema_0002",
    }


@pytest.mark.asyncio
async def test_reconcile_has_no_python_transport_fallback() -> None:
    service, transport = _service([])
    params = ProductParams.model_validate(
        {
            "tableId": "orders",
            "schemaRevision": "schema_0001",
            "dataRevision": "data_0001",
        }
    )

    with pytest.raises(ValueError, match=r"unknown product RPC method: events\.reconcile"):
        await service.invoke("events.reconcile", params)

    assert transport.requests == []


@pytest.mark.asyncio
async def test_history_restore_uses_closed_product_routes() -> None:
    service, transport = _service(
        [
            {"token": "restore-token", "canApply": True},
            {"restoredToRevision": "rev-1"},
        ]
    )

    await service.invoke(
        "history.previewRestore",
        ProductParams.model_validate(
            {
                "collection": "orders",
                "itemId": "order-1",
                "targetRevision": "rev-1",
                "scope": "row",
            }
        ),
    )
    await service.invoke(
        "history.applyRestore",
        ProductParams.model_validate(
            {"collection": "orders", "itemId": "order-1", "token": "restore-token"}
        ),
    )
    assert transport.requests[0]["path"] == "/api/vibetable/v1/history/restore-preview"
    assert transport.requests[1]["path"] == "/api/vibetable/v1/history/restore-apply"


@pytest.mark.asyncio
async def test_trusted_host_attachment_upload_uses_one_guarded_multipart_mutation() -> None:
    service, transport = _service(
        [
            {
                "contractVersion": "2.0",
                "status": "applied",
                "changeSetId": "change-1",
            }
        ]
    )

    result = await service.invoke(
        "file.applyHostChange",
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "schemaRevision": "schema_7",
                "expectedDigest": "sha256:" + "a" * 64,
                "hostPaths": [r"C:\host-selected\invoice.pdf"],
                "removeStoredNames": [],
            }
        ),
    )

    request = transport.requests[0]
    assert result["status"] == "applied"
    assert request["method"] == "MULTIPART"
    assert request["path"] == "/api/vibetable/v1/mutations/apply"
    assert request["uploads"] == [("upload_0", r"C:\host-selected\invoice.pdf")]
    assert request["json_body"]["expectedDigest"] == "sha256:" + "a" * 64
    assert request["json_body"]["operations"] == [
        {
            "kind": "setAttachments",
            "recordId": "row-1",
            "fieldId": "invoice",
            "uploadHandles": ["upload_0"],
            "removeStoredNames": [],
        }
    ]


@pytest.mark.asyncio
async def test_trusted_host_attachment_download_keeps_capability_and_path_native() -> None:
    service, transport = _service(
        [
            {
                "contractVersion": "2.0",
                "downloadCapability": "opaque-capability",
            }
        ]
    )

    result = await service.invoke(
        "file.saveHostFile",
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice_abcd.pdf",
                "outputPath": r"C:\host-selected\invoice.pdf",
            }
        ),
    )

    assert result == {
        "contractVersion": "2.0",
        "saved": True,
        "bytes": 12,
    }
    assert transport.requests[0]["path"] == "/api/vibetable/v1/files/token"
    assert transport.requests[1] == {
        "method": "DOWNLOAD",
        "path": "/api/vibetable/v1/attachments/download",
        "query": {"capability": "opaque-capability"},
        "target_path": r"C:\host-selected\invoice.pdf",
        "headers": {"X-VibeTable-Session": "a" * 64},
        "expected_status": (200,),
    }


@pytest.mark.asyncio
async def test_snapshot_has_no_python_route_or_transport_fallback() -> None:
    service, transport = _service(
        [
            {
                "valid": True,
                "currentDataRevision": 2,
                "currentSchemaRevision": "schema_2",
            },
        ]
    )

    with pytest.raises(ValueError, match=r"unknown product RPC method: query\.validateSnapshot"):
        await service.invoke(
            "query.validateSnapshot",
            ProductParams.model_validate(
                {
                    "snapshot": {
                        "snapshotId": "snap",
                        "digest": "a" * 64,
                        "databaseId": "local",
                        "table": "orders",
                        "schemaRevision": "schema_1",
                        "dataRevision": 1,
                        "normalizedQuery": {
                            "keyword": "",
                            "filters": [],
                            "sorts": [],
                            "offset": 0,
                            "limit": 100,
                        },
                    }
                }
            ),
        )

    assert transport.requests == []
