from __future__ import annotations

import pytest

from backend.__main__ import _register_pocketbase_product_methods
from backend.adapters.pocketbase.client import PocketBaseProductError
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.contracts.generated_product_rpc_capabilities import current_owner_methods
from backend.contracts.product_rpc import (
    PRODUCT_RPC_REGISTRY,
    PYTHON_PRODUCT_RPC_REGISTRY,
    WORKSPACE_CATALOG_METHODS,
    ProductParams,
    _current_python_registry,
)
from backend.rpc.dispatcher import CODE_INVALID_PARAMS, CODE_METHOD_NOT_FOUND, RpcDispatcher
from backend.rpc.error_registry import CODE_PRODUCT_DATA

RETIRED_PROVIDER = "".join(["di", "rectus"])


class FakeProductService:
    def __init__(self) -> None:
        self.calls: list[tuple[str, ProductParams]] = []

    async def invoke(self, method: str, params: ProductParams) -> dict[str, object]:
        self.calls.append((method, params))
        return {}


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
        ("schema.list", {}),
        ("schema.list", {"extra": True}),
        ("schema.getTable", {"tableId": "orders"}),
        ("file.list", {"tableId": "t", "recordId": "r", "fieldId": "f"}),
    ],
)
async def test_go_owned_product_methods_have_no_python_registration_or_fallback(
    method: str,
    params: dict[str, object],
) -> None:
    dispatcher = RpcDispatcher()
    service = FakeProductService()
    _register_pocketbase_product_methods(dispatcher, service)

    response = await dispatcher.dispatch(
        {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
    )

    assert response is not None
    assert response["error"]["code"] == CODE_METHOD_NOT_FOUND
    assert service.calls == []


def test_product_rpc_registration_is_closed_and_provider_neutral() -> None:
    dispatcher = RpcDispatcher()

    _register_pocketbase_product_methods(dispatcher, FakeProductService())

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
    assert set(PRODUCT_RPC_REGISTRY) == expected_methods
    assert set(dispatcher.registered_methods) == expected_methods - {
        "file.list",
        "events.reconcile",
        "schema.getTable",
        "schema.list",
    }
    assert set(PYTHON_PRODUCT_RPC_REGISTRY) == set(dispatcher.registered_methods)
    assert set(dispatcher.registered_methods) >= WORKSPACE_CATALOG_METHODS
    assert set(PRODUCT_RPC_REGISTRY) - set(current_owner_methods("pythonBff")) == (
        WORKSPACE_CATALOG_METHODS
        | {"events.reconcile", "file.list", "schema.getTable", "schema.list"}
    )
    assert not any(
        method.startswith(f"{RETIRED_PROVIDER}.") for method in dispatcher.registered_methods
    )


@pytest.mark.asyncio
async def test_product_rpc_registration_delegates_through_single_invoke_seam() -> None:
    dispatcher = RpcDispatcher()
    service = FakeProductService()
    _register_pocketbase_product_methods(dispatcher, service)

    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "schema.describe",
            "params": {
                "collection": "orders",
                "requestGeneration": 1,
                "accepts": [
                    "vibetable.relation-capabilities.v1",
                    "vibetable.lookup-query.v1",
                ],
            },
        }
    )

    assert response == {"jsonrpc": "2.0", "id": 1, "result": {}}
    assert len(service.calls) == 1
    method, params = service.calls[0]
    assert method == "schema.describe"
    assert isinstance(params, PRODUCT_RPC_REGISTRY[method])


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("method", "params"),
    [
        (
            "schema.describe",
            {"collection": "orders", "requestGeneration": 1, "accepts": [], "extra": True},
        ),
        ("schema.describe", {}),
        ("schema.describe", {"collection": 7, "requestGeneration": 1, "accepts": []}),
        (
            "schema.describe",
            {
                "collection": "orders",
                "tableId": "orders",
                "requestGeneration": 1,
                "accepts": [
                    "vibetable.relation-capabilities.v1",
                    "vibetable.lookup-query.v1",
                ],
            },
        ),
    ],
)
async def test_product_rpc_rejects_extra_missing_wrong_type_and_alias_conflict(
    method: str,
    params: dict[str, object],
) -> None:
    dispatcher = RpcDispatcher()
    _register_pocketbase_product_methods(dispatcher, FakeProductService())

    response = await dispatcher.dispatch(
        {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
    )

    assert response is not None
    assert response["error"]["code"] == CODE_INVALID_PARAMS


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
            PocketBaseTransportError(
                "sidecar unavailable",
                code="sidecar.unavailable",
            ),
            "sidecar.unavailable",
        ),
    ],
)
async def test_product_rpc_preserves_sanitized_structured_errors(
    failure: Exception,
    expected_code: str,
) -> None:
    class ErrorService(FakeProductService):
        async def invoke(self, method: str, params: ProductParams) -> dict[str, object]:
            del method, params
            raise failure

    dispatcher = RpcDispatcher()
    _register_pocketbase_product_methods(dispatcher, ErrorService())

    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": 2,
            "method": "schema.describe",
            "params": {
                "collection": "orders",
                "requestGeneration": 1,
                "accepts": [
                    "vibetable.relation-capabilities.v1",
                    "vibetable.lookup-query.v1",
                ],
            },
        }
    )

    assert response is not None
    assert response["error"]["code"] == CODE_PRODUCT_DATA
    assert response["error"]["data"]["code"] == expected_code
    assert response["error"]["data"]["message"] == str(failure)


def test_current_python_registry_rejects_unknown_or_missing_workspace_contracts() -> None:
    models = dict(PRODUCT_RPC_REGISTRY)
    models["undeclared.read"] = ProductParams
    with pytest.raises(RuntimeError, match="undeclared non-Product methods"):
        _current_python_registry(models)
    models.pop("undeclared.read")
    models.pop(next(iter(WORKSPACE_CATALOG_METHODS)))
    with pytest.raises(RuntimeError, match="undeclared non-Product methods"):
        _current_python_registry(models)
