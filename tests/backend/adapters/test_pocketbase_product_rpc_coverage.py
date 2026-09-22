from __future__ import annotations

from typing import Any

import pytest
from pydantic import ValidationError

from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, ProductParams
from tests.backend.product_composition_fixture import ProductBackend
from tests.backend.product_composition_fixture import product_backend as product_backend


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


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "method", ["relation.createTarget", "relation.updateSingle", "relation.applyDelta"]
)
async def test_migrated_relation_writes_have_no_python_fallback(
    method: str, product_backend: ProductBackend
) -> None:
    await product_backend.assert_retired(method, {})


@pytest.mark.asyncio
async def test_migrated_selection_open_cannot_reenter_python_transport(
    product_backend: ProductBackend,
) -> None:
    await product_backend.assert_retired(
        "query.selectionOpen", {"tableId": "orders", "query": {"limit": 10}}
    )


@pytest.mark.parametrize("non_finite", [float("nan"), float("inf"), float("-inf")])
def test_product_dto_rejects_non_finite_numbers(non_finite: float) -> None:
    with pytest.raises(ValidationError, match="JSON values"):
        ProductParams.model_validate({"value": non_finite})


def test_relation_search_rejects_provider_alias() -> None:
    with pytest.raises(ValidationError, match="unknown fields: collection"):
        PRODUCT_RPC_REGISTRY["relation.searchTargets"].model_validate(
            {"relationId": "orders.customer", "collection": "customers"}
        )


# Fixed-route token issuance, guarded multipart, remove-only, download variants,
# invalid attachment changes and non-finite responses now execute at their real
# owners in attachment_product_rpc_test.go and ProductSidecarHttpGateway.HostFiles.Tests.cs.
@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("method", "params"),
    [
        ("query.readRows", {"tableId": "orders", "rowIds": [""]}),
        ("schema.list", {"unexpected": 1}),
        ("query.validateSnapshot", {"snapshot": {"digest": "x"}, "currentQuery": {"limit": 10}}),
        (
            "history.previewRestore",
            {
                "collection": "orders",
                "itemId": "row-1",
                "targetRevision": "rev-1",
                "scope": "field",
                "field": "",
            },
        ),
        (
            "file.token",
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice.pdf",
                "variant": "thumb",
            },
        ),
        (
            "file.token",
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice.pdf",
                "variant": 1,
            },
        ),
        (
            "file.saveHostFile",
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice.pdf",
                "outputPath": "invoice.pdf",
                "variant": "",
            },
        ),
        (
            "file.applyHostChange",
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "schemaRevision": "schema_3",
                "expectedDigest": "sha256:" + "a" * 64,
                "hostPaths": [],
                "removeStoredNames": ["old.pdf"],
            },
        ),
    ],
)
async def test_native_and_go_requests_cannot_reenter_python(
    product_backend: ProductBackend, method: str, params: dict[str, object]
) -> None:
    await product_backend.assert_retired(method, params)
