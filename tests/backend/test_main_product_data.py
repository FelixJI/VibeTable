from __future__ import annotations

import pytest

from backend.__main__ import _register_pocketbase_product_methods
from backend.adapters.pocketbase.client import PocketBaseProductError
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, ProductParams
from backend.rpc.dispatcher import CODE_INVALID_PARAMS, CODE_PRODUCT_DATA, RpcDispatcher

RETIRED_PROVIDER = "".join(["di", "rectus"])


class FakeProductService:
    def __init__(self) -> None:
        self.calls: list[tuple[str, ProductParams]] = []

    async def invoke(self, method: str, params: ProductParams) -> dict[str, object]:
        self.calls.append((method, params))
        return {}


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
        "lookup.validate",
        "lookup.preview",
        "lookup.query",
        "lookup.valuePage",
        "mutation.apply",
        "mutation.preview",
        "query.page",
        "query.view",
        "query.readRows",
        "query.validateSnapshot",
        "relation.applyDelta",
        "relation.createTarget",
        "relation.previewDelta",
        "relation.searchTargets",
        "relation.updateSingle",
        "schema.apply",
        "schema.delete",
        "schema.describe",
        "schema.getTable",
        "schema.list",
        "schema.validate",
        "history.applyRestore",
        "history.previewRestore",
        "history.read",
    }
    assert set(PRODUCT_RPC_REGISTRY) == expected_methods
    assert set(dispatcher.registered_methods) == expected_methods
    assert not any(
        method.startswith(f"{RETIRED_PROVIDER}.") for method in dispatcher.registered_methods
    )


@pytest.mark.asyncio
async def test_product_rpc_registration_delegates_through_single_invoke_seam() -> None:
    dispatcher = RpcDispatcher()
    service = FakeProductService()
    _register_pocketbase_product_methods(dispatcher, service)

    response = await dispatcher.dispatch(
        {"jsonrpc": "2.0", "id": 1, "method": "schema.list", "params": {}}
    )

    assert response == {"jsonrpc": "2.0", "id": 1, "result": {}}
    assert len(service.calls) == 1
    method, params = service.calls[0]
    assert method == "schema.list"
    assert isinstance(params, PRODUCT_RPC_REGISTRY[method])


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("method", "params"),
    [
        ("schema.list", {"extra": True}),
        ("schema.getTable", {}),
        ("schema.getTable", {"tableId": 7}),
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
            "method": "schema.list",
            "params": {},
        }
    )

    assert response is not None
    assert response["error"]["code"] == CODE_PRODUCT_DATA
    assert response["error"]["data"]["code"] == expected_code
    assert response["error"]["data"]["message"] == str(failure)
