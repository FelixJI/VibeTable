from __future__ import annotations

import pytest
from pydantic import ValidationError

from backend.adapters.pocketbase.client import PocketBaseProductError
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.contracts.generated_product_rpc_capabilities import current_owner_methods
from backend.contracts.product_rpc import (
    PRODUCT_RPC_REGISTRY,
    PYTHON_PRODUCT_RPC_REGISTRY,
    ProductParams,
    _current_python_registry,
)
from backend.rpc.error_registry import CODE_PRODUCT_DATA
from tests.backend.product_composition_fixture import ProductBackend
from tests.backend.product_composition_fixture import product_backend as product_backend

RETIRED_PROVIDER = "".join(["di", "rectus"])


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("method", "params"),
    [
        (
            "events.reconcile",
            {"tableId": "orders", "schemaRevision": "schema_0001", "dataRevision": "data_0001"},
        ),
        ("field.settings.describe", {"tableId": "orders"}),
        ("field.settings.describe", {"extra": True}),
        ("mutation.apply", {}),
        ("mutation.preview", {}),
        ("schema.list", {}),
        ("table.previewPaste", {}),
        ("table.applyPaste", {}),
        ("query.page", {"tableId": "orders", "query": {}}),
        ("query.page", {"extra": True}),
        ("query.cursorOpen", {"tableId": "orders", "query": {}}),
        ("query.cursorFetch", {"cursor": "opaque"}),
        ("query.cursorOpen", {"extra": True}),
        ("query.cursorFetch", {"extra": True}),
        ("query.view", {"tableId": "orders", "view": {}}),
        ("query.view", {"extra": True}),
        ("query.validateSnapshot", {"snapshot": {}}),
        ("query.validateSnapshot", {"snapshot": {}, "extra": True}),
        ("lookup.valuePage", {"extra": True}),
        ("relation.inspectPair", {"tableId": "orders", "fieldId": "fld_link"}),
        ("relation.previewDelta", {"extra": True}),
        ("lookup.query", {"extra": True}),
        ("schema.getTable", {"tableId": "orders"}),
        ("file.list", {"tableId": "t", "recordId": "r", "fieldId": "f"}),
        ("field.change.status", {"jobId": "job_01JMIGRATE"}),
        ("field.change.cancel", {"jobId": "job_01JMIGRATE"}),
        ("field.recycleBin.list", {"tableId": "orders"}),
        ("formula.validate", {"tableId": "orders", "field": {"contract": "vibetable.schema.v2"}}),
        ("formula.draft.validate", {"tableId": "orders", "displaySource": "1 + 1"}),
        (
            "formula.preview",
            {
                "tableId": "orders",
                "field": {"contract": "vibetable.schema.v2"},
                "row": {},
                "changedFieldIds": [],
            },
        ),
        (
            "schema.table.create",
            {
                "displayName": "订单",
                "operationId": "operation-create-table-12345678",
                "actor": {"id": "desktop-host", "kind": "host"},
            },
        ),
        ("schema.delete", {"tableId": "orders", "expectedRevision": "schema_0002"}),
    ],
)
async def test_go_owned_product_methods_have_no_python_registration_or_fallback(
    method: str, params: dict[str, object], product_backend: ProductBackend
) -> None:
    await product_backend.assert_retired(method, params)


@pytest.mark.asyncio
async def test_product_rpc_registration_is_closed_and_provider_neutral(
    product_backend: ProductBackend,
) -> None:
    expected_methods = {
        "field.settings.describe",
        "field.change.plan",
        "field.change.apply",
        "field.change.status",
        "field.change.cancel",
        "field.recycleBin.list",
        "events.reconcile",
        "file.list",
        "file.token",
        "file.applyHostChange",
        "file.saveHostFile",
        "formula.preview",
        "formula.draft.validate",
        "formula.validate",
        "lookup.list",
        "lookup.query",
        "lookup.valuePage",
        "mutation.apply",
        "mutation.preview",
        "query.page",
        "query.cursorOpen",
        "query.selectionOpen",
        "query.cursorFetch",
        "query.view",
        "query.readRows",
        "query.validateSnapshot",
        "relation.applyDelta",
        "relation.createTarget",
        "relation.inspectPair",
        "relation.previewDelta",
        "relation.searchTargets",
        "relation.updateSingle",
        "schema.table.create",
        "schema.delete",
        "schema.describe",
        "schema.getTable",
        "schema.list",
        "history.applyRestore",
        "history.previewRestore",
        "history.read",
    }
    assert set(product_backend.server._dispatcher.registered_methods) == {
        "system.handshake",
        "task.startExecution",
        "task.cancelExecution",
        "task.settleExport",
        "data.previewImport",
        "data.applyImport",
        "data.export",
        "data.generateTemplate",
        "plugin.inspectInstall",
        "plugin.commitInstall",
        "plugin.cancelInstall",
        "plugin.upgrade",
        "plugin.rollback",
        "plugin.uninstall",
        "plugin.describeAction",
        "plugin.startAction",
        "plugin.resolveInteraction",
        "plugin.resolveFile",
        "plugin.cancelTask",
    }
    assert set(PRODUCT_RPC_REGISTRY) == expected_methods
    assert not PYTHON_PRODUCT_RPC_REGISTRY
    assert expected_methods.isdisjoint(product_backend.server._dispatcher.registered_methods)
    assert expected_methods.isdisjoint(current_owner_methods("pythonBff"))
    assert not any(
        method.startswith(f"{RETIRED_PROVIDER}.")
        for method in product_backend.server._dispatcher.registered_methods
    )
    assert product_backend.transport.requests == []


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("method", "params"),
    [
        ("file.token", {"tableId": "orders", "extra": True}),
        ("file.token", {}),
        (
            "file.token",
            {"tableId": 7, "recordId": "row-1", "fieldId": "invoice", "storedName": "s"},
        ),
        (
            "file.token",
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "s",
                "collection": "orders",
            },
        ),
    ],
)
async def test_product_rpc_rejects_extra_missing_wrong_type_and_alias_conflict(
    method: str, params: dict[str, object], product_backend: ProductBackend
) -> None:
    with pytest.raises(ValidationError):
        PRODUCT_RPC_REGISTRY[method].model_validate(params)
    await product_backend.assert_retired(method, params)


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("failure", "expected_code"),
    [
        (
            PocketBaseProductError(
                status=409,
                payload={
                    "code": "mutation.digest_conflict",
                    "message": "record changed",
                    "path": None,
                    "details": {"recordId": "row-1"},
                    "retryable": False,
                },
            ),
            "mutation.digest_conflict",
        ),
        (
            PocketBaseTransportError("sidecar unavailable", code="sidecar.unavailable"),
            "sidecar.unavailable",
        ),
    ],
)
async def test_product_rpc_preserves_sanitized_structured_errors(
    failure: Exception, expected_code: str, product_backend: ProductBackend
) -> None:
    product_backend.transport.failure = failure
    response = await product_backend.server._dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": 2,
            "method": "data.generateTemplate",
            "params": {"collection": "orders", "grantId": "not-opened"},
        }
    )
    assert response is not None
    assert response["error"]["code"] == CODE_PRODUCT_DATA
    assert response["error"]["data"]["code"] == expected_code
    assert response["error"]["data"]["message"] == str(failure)
    if isinstance(failure, PocketBaseProductError):
        assert response["error"]["data"]["details"] == failure.details
        assert response["error"]["data"]["path"] == failure.path
        assert response["error"]["data"]["retryable"] == failure.retryable
    assert product_backend.transport.requests == [("GET", "/api/vibetable/v2/schema/tables/orders")]


def test_current_python_registry_rejects_unknown_and_retires_go_owned_routes() -> None:
    models = dict(PRODUCT_RPC_REGISTRY)
    models["undeclared.read"] = ProductParams
    with pytest.raises(RuntimeError, match="undeclared non-Product methods"):
        _current_python_registry(models)
    models.pop("undeclared.read")
    registry = _current_python_registry(models)
    assert set(registry) == set(current_owner_methods("pythonBff")) & set(models)
    assert not (
        set(registry)
        & {
            "field.change.plan",
            "field.change.apply",
            "field.change.status",
            "field.change.cancel",
            "field.recycleBin.list",
            "schema.table.create",
            "schema.delete",
            "formula.validate",
            "formula.draft.validate",
            "formula.preview",
        }
    )
