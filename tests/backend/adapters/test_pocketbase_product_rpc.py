from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from pydantic import ValidationError

from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, ProductParams
from tests.backend.product_composition_fixture import ProductBackend
from tests.backend.product_composition_fixture import product_backend as product_backend


def _formula_v2_field() -> dict[str, Any]:
    path = Path(__file__).parents[3] / "contracts/schema-v2/fixtures/field-definition.json"
    field = json.loads(path.read_text(encoding="utf-8"))
    field["logicalType"] = "formula"
    field["storage"]["kind"] = "computed"
    field["value"]["presence"] = {"mode": "computed"}
    field["display"]["kind"] = "readonly"
    field["formula"] = {"language": "cel-v1", "source": "1 + 1", "resultType": "number"}
    return field


def test_params_reject_credentials_recursively() -> None:
    with pytest.raises(ValidationError):
        ProductParams.model_validate({"nested": {"sessionSecret": "secret"}})


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("method", "params"),
    [
        (
            "events.reconcile",
            {"tableId": "orders", "schemaRevision": "schema_0001", "dataRevision": "data_0001"},
        ),
        ("field.settings.describe", {"tableId": "orders"}),
        (
            "field.change.plan",
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
            },
        ),
        (
            "field.change.apply",
            {
                "planId": "plan_1",
                "planHash": "a" * 64,
                "operationId": "op_1",
                "actor": {"id": "local-user", "kind": "user"},
                "confirmations": [],
            },
        ),
        ("field.change.status", {"jobId": "job_01JMIGRATE"}),
        ("field.change.cancel", {"jobId": "job_01JMIGRATE"}),
        ("field.recycleBin.list", {"tableId": "orders"}),
        (
            "schema.table.create",
            {
                "displayName": "订单",
                "operationId": "operation-create-table-12345678",
                "actor": {"id": "desktop-host", "kind": "host"},
            },
        ),
        ("schema.delete", {"tableId": "orders", "expectedRevision": "schema_0002"}),
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
    method: str, params: dict[str, object], product_backend: ProductBackend
) -> None:
    await product_backend.assert_retired(method, params)


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

    patch_request = accepted.root | {
        "action": "update",
        "fieldId": "fld_orders_customer",
        "draft": None,
        "relationPairPatch": {"reciprocalDisplayName": "购买记录"},
    }
    patch_request.pop("relationPair")
    patch = PRODUCT_RPC_REGISTRY["field.change.plan"].model_validate(patch_request)
    assert patch.root == patch_request

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


def test_retired_schema_methods_keep_their_full_public_parameter_models() -> None:
    from backend.contracts.product_rpc import PYTHON_PRODUCT_RPC_REGISTRY

    retired = {
        "field.change.plan",
        "field.change.apply",
        "field.change.status",
        "field.change.cancel",
        "field.recycleBin.list",
        "schema.table.create",
        "schema.delete",
    }
    for method in retired:
        assert method in PRODUCT_RPC_REGISTRY
        assert method not in PYTHON_PRODUCT_RPC_REGISTRY


@pytest.mark.asyncio
async def test_go_owned_formula_routes_have_no_python_transport(
    product_backend: ProductBackend,
) -> None:
    for method, params in [
        ("formula.validate", {"tableId": "orders", "field": _formula_v2_field()}),
        ("formula.draft.validate", {"tableId": "orders", "displaySource": "SUM({明细}.{金额})"}),
        (
            "formula.preview",
            {"tableId": "orders", "field": _formula_v2_field(), "row": {}, "changedFieldIds": []},
        ),
    ]:
        await product_backend.assert_retired(method, params)


@pytest.mark.asyncio
async def test_schema_delete_has_no_python_transport(product_backend: ProductBackend) -> None:
    await product_backend.assert_retired(
        "schema.delete", {"tableId": "orders", "expectedRevision": "schema_0002"}
    )


@pytest.mark.asyncio
async def test_reconcile_has_no_python_transport_fallback(product_backend: ProductBackend) -> None:
    params = ProductParams.model_validate(
        {"tableId": "orders", "schemaRevision": "schema_0001", "dataRevision": "data_0001"}
    )
    await product_backend.assert_retired("events.reconcile", params.root)


@pytest.mark.asyncio
@pytest.mark.parametrize("method", ["history.previewRestore", "history.applyRestore"])
async def test_history_restore_has_no_python_transport_fallback(
    method: str, product_backend: ProductBackend
) -> None:
    await product_backend.assert_retired(method, {})


@pytest.mark.asyncio
async def test_snapshot_has_no_python_route_or_transport_fallback(
    product_backend: ProductBackend,
) -> None:
    await product_backend.assert_retired(
        "query.validateSnapshot",
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
        },
    )
