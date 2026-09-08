"""Capture lookup.valuePage through the original Python dispatcher, adapter and client."""

from __future__ import annotations

import argparse
import asyncio
import inspect
import json
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from functools import partial
from pathlib import Path

from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseProductError
from backend.adapters.pocketbase.product_relation_lookup_file_rpc import (
    ProductRelationLookupFileRpc,
)
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.adapters.pocketbase.product_rpc_support import _lookup_revision
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, JsonObject, JsonValue
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.product_errors import register_product_rpc_errors

PRODUCER_COMMIT = "a19ccd5366d62be6338f628d06b6c5a37484f20f"
OUTPUT = Path(__file__).with_name("lookup-value-page-python-oracle.json")
METHOD = "lookup.valuePage"
CAPTURE_ROOT = Path(__file__).resolve().parents[2]

TYPED_GO_BOUNDARIES: JsonObject = {
    "empty-object-result": "CellValue emits mandatory fields; it cannot encode an empty object.",
    "extra-result-fields": "CellValue cannot retain undeclared top-level fields.",
    "non-object-result": "CellValue is a struct; Python rejects an array root.",
    "wrong-result-field-types": "CellValue state/count/provenance have fixed Go types.",
    "catalog-non-object": "CatalogResult is a struct, not an array.",
    "catalog-missing-lookups": "CatalogResult emits lookups; absence differs from null.",
    "catalog-wrong-lookups": "CatalogResult.Lookups is a slice, not an object.",
    "catalog-missing-schema": "CatalogResult emits schemaRevision; absence differs from empty text.",
    "catalog-nontext-schema": "CatalogResult.SchemaRevision is string, not bool.",
    "catalog-mixed-members": "[]LookupDescriptor cannot contain non-object members.",
    "first-matching-field-missing-id": "LookupDescriptor emits fieldId; absence is a historical shape.",
    "catalog-transport-error": "An in-process catalog call has no Python HTTP transport boundary.",
    "page-transport-error": "An in-process value page call has no Python HTTP transport boundary.",
}


@dataclass(frozen=True)
class Case:
    name: str
    params: JsonValue
    catalog: JsonValue
    page: JsonValue
    failure: str | None = None


def descriptor() -> JsonObject:
    return {
        "lookupId": "orders.lookup_skus",
        "tableId": "orders",
        "fieldId": "lookup_skus",
        "physicalName": "line_skus",
        "displayName": "商品",
        "relationFieldId": "lines",
        "path": [{"relationId": "orders.lines"}],
        "targetFieldId": "sku",
        "resultCardinality": "many",
        "outputStorage": "json",
        "revision": 1,
    }


def catalog_result(lookups: list[JsonValue] | None = None) -> JsonObject:
    return {
        "tableId": "orders",
        "schemaRevision": "schema-1",
        "lookupMaxDepth": 8,
        "relations": [],
        "lookups": [descriptor()] if lookups is None else lookups,
    }


def base_params(catalog: JsonObject | None = None) -> JsonObject:
    source = catalog_result() if catalog is None else catalog
    lookups = source["lookups"]
    schema = source["schemaRevision"]
    assert isinstance(lookups, list)
    assert isinstance(schema, str)
    return {
        "collection": "orders",
        "fieldRef": "line_skus",
        "sourceRecordId": "order-1",
        "offset": 0,
        "limit": 1,
        "schemaRevision": schema,
        "permissionRevision": schema,
        "lookupRevision": _lookup_revision(schema, lookups),
    }


def cell_result() -> JsonObject:
    return {
        "state": "ok",
        "value": [False, 0, "", None, [], {}, "中文 Cafe\u0301 👩🏽‍💻"],
        "provenance": [
            {
                "collection": "lines",
                "collectionLabel": "明细",
                "itemId": "line-1",
                "recordLabel": "记录",
                "fieldId": "sku",
                "fieldLabel": "商品",
                "value": {"text": "中文", "zero": 0, "false": False},
            }
        ],
        "provenanceTotal": 3,
        "provenanceTotalKnown": True,
        "provenanceOffset": 0,
        "provenanceLimit": 1,
        "provenanceHasMore": True,
    }


def cases() -> tuple[Case, ...]:
    params, catalog, page = base_params(), catalog_result(), cell_result()
    duplicate = catalog_result([descriptor(), {**descriptor(), "fieldId": "later-field"}])
    missing_id = descriptor()
    del missing_id["fieldId"]
    missing_first = catalog_result([missing_id, descriptor()])
    mixed = catalog_result([None, 7, "ignored", descriptor()])
    return (
        Case("baseline-first-page", params, catalog, page),
        Case(
            "null-value-and-provenance",
            params,
            catalog,
            {**page, "value": None, "provenance": None},
        ),
        Case(
            "empty-provenance-terminal",
            params,
            catalog,
            {
                **page,
                "value": [],
                "provenance": [],
                "provenanceTotal": 0,
                "provenanceHasMore": False,
            },
        ),
        Case(
            "diagnostic-state",
            params,
            catalog,
            {
                **page,
                "state": "error",
                "value": None,
                "diagnostic": {
                    "code": "lookup.target_missing",
                    "message": "目标不存在",
                    "pathIndex": 0,
                },
            },
        ),
        Case(
            "maximum-limit-500", {**params, "limit": 500}, catalog, {**page, "provenanceLimit": 500}
        ),
        Case("offset-one", {**params, "offset": 1}, catalog, {**page, "provenanceOffset": 1}),
        Case(
            "negative-result-counters-pass-through",
            params,
            catalog,
            {**page, "provenanceTotal": -1, "provenanceOffset": -1, "provenanceLimit": 0},
        ),
        Case("empty-object-result", params, catalog, {}),
        Case("extra-result-fields", params, catalog, {**page, "future": {"unchanged": False}}),
        Case("non-object-result", params, catalog, []),
        Case(
            "wrong-result-field-types",
            params,
            catalog,
            {**page, "state": None, "provenance": {}, "provenanceTotal": True},
        ),
        Case("catalog-non-object", params, [], page),
        Case("catalog-missing-lookups", params, {"schemaRevision": "schema-1"}, page),
        Case("catalog-wrong-lookups", params, {**catalog, "lookups": {}}, page),
        Case("catalog-missing-schema", params, {"lookups": [descriptor()]}, page),
        Case("catalog-nontext-schema", params, {**catalog, "schemaRevision": False}, page),
        Case("catalog-mixed-members", base_params(mixed), mixed, page),
        Case("first-matching-physical-name-wins", base_params(duplicate), duplicate, page),
        Case("first-matching-field-missing-id", base_params(missing_first), missing_first, page),
        Case("unknown-physical-field", {**params, "fieldRef": "unknown"}, catalog, page),
        Case(
            "stable-field-id-is-not-physical-name",
            {**params, "fieldRef": "lookup_skus"},
            catalog,
            page,
        ),
        Case("stale-schema-revision", {**params, "schemaRevision": "stale"}, catalog, page),
        Case("stale-permission-revision", {**params, "permissionRevision": "stale"}, catalog, page),
        Case("stale-lookup-revision", {**params, "lookupRevision": "stale"}, catalog, page),
        Case("negative-offset", {**params, "offset": -1}, catalog, page),
        Case("zero-limit", {**params, "limit": 0}, catalog, page),
        Case("limit-501", {**params, "limit": 501}, catalog, page),
        Case(
            "missing-offset",
            {key: value for key, value in params.items() if key != "offset"},
            catalog,
            page,
        ),
        Case("boolean-limit", {**params, "limit": True}, catalog, page),
        Case("float-offset", {**params, "offset": 0.0}, catalog, page),
        Case("empty-collection", {**params, "collection": ""}, catalog, page),
        Case("wrong-source-type", {**params, "sourceRecordId": []}, catalog, page),
        Case("unknown-param", {**params, "extra": True}, catalog, page),
        Case("non-object-params", [], catalog, page),
        Case(
            "unicode-and-whitespace-not-trimmed",
            {**params, "collection": " 中文 ", "sourceRecordId": " Cafe\u0301 👩🏽‍💻 "},
            catalog,
            page,
        ),
        Case("catalog-product-error", params, catalog, page, "catalog-product"),
        Case("page-product-error", params, catalog, page, "page-product"),
        Case("catalog-transport-error", params, catalog, page, "catalog-transport"),
        Case("page-transport-error", params, catalog, page, "page-transport"),
    )


class RecordingTransport:
    """Execute only the two actual authority endpoints, with ordered scripted responses."""

    def __init__(self, case: Case) -> None:
        self.case = case
        self.requests: list[JsonObject] = []

    async def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, JsonValue] | None = None,
        json_body: JsonValue = None,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> JsonValue:
        assert headers == {"X-VibeTable-Session": "oracle-only"}
        assert tuple(expected_status) == (200,)
        index = len(self.requests)
        if index == 0:
            assert method == "GET"
            assert path == "/api/vibetable/v1/relations/describe"
            assert query is not None
            assert set(query) == {"tableId"}
            assert json_body is None
            phase = "catalog"
        else:
            assert index == 1, "Value page must make at most two authority requests"
            assert method == "POST"
            assert path == "/api/vibetable/v1/lookups/value-page"
            assert query is None
            phase = "page"
        self.requests.append(
            {
                "method": method,
                "path": path,
                "query": dict(query) if query is not None else None,
                "body": json_body,
                "expectedStatus": list(expected_status),
            }
        )
        if self.case.failure == phase + "-product":
            raise PocketBaseProductError(
                status=409,
                payload={
                    "code": "lookup.schema_revision_conflict",
                    "message": phase + " revision conflict",
                    "path": "schemaRevision",
                    "details": {"phase": phase},
                    "retryable": False,
                },
            )
        if self.case.failure == phase + "-transport":
            raise PocketBaseTransportError(phase + " unavailable")
        return self.case.catalog if index == 0 else self.case.page

    async def request_multipart(
        self,
        path: str,
        *,
        json_body: Mapping[str, JsonValue],
        uploads: Sequence[tuple[str, str]],
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> JsonValue:
        raise AssertionError("Value page must not upload files")

    async def download_to_file(
        self,
        path: str,
        *,
        query: Mapping[str, JsonValue],
        target_path: str,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
        maximum_bytes: int = 2 * 1024 * 1024 * 1024,
    ) -> int:
        raise AssertionError("Value page must not download files")


async def capture_case(case: Case) -> JsonObject:
    for symbol in (
        PocketBaseProductRpc,
        ProductRelationLookupFileRpc,
        PocketBaseClient,
        RpcDispatcher,
        PRODUCT_RPC_REGISTRY[METHOD],
        _lookup_revision,
    ):
        if not Path(inspect.getfile(symbol)).resolve().is_relative_to(CAPTURE_ROOT / "backend"):
            raise RuntimeError(
                "Capture requires this checkout's backend; set PYTHONPATH to its root "
                "and run python -m contracts.v2.generate_lookup_value_page_oracle"
            )
    transport = RecordingTransport(case)
    service = PocketBaseProductRpc(
        client=PocketBaseClient(transport=transport, session_secret="oracle-only"),
        transport=transport,
        session_secret="oracle-only",
    )
    dispatcher = RpcDispatcher()
    register_product_rpc_errors()
    dispatcher.register(METHOD, partial(service.invoke, METHOD), PRODUCT_RPC_REGISTRY[METHOD])
    request: JsonObject = {
        "jsonrpc": "2.0",
        "id": case.name,
        "method": METHOD,
        "params": case.params,
    }
    response = await dispatcher.dispatch(request)
    return {
        "name": case.name,
        "request": request,
        "authorityFixture": {"catalog": case.catalog, "page": case.page, "failure": case.failure},
        "authorityRequests": list(transport.requests),
        "response": response,
    }


async def capture() -> JsonObject:
    return {
        "producerCommit": PRODUCER_COMMIT,
        "boundary": "Original Python Product dispatcher + adapter + client; scripted authority HTTP",
        "typedGoBoundaries": TYPED_GO_BOUNDARIES,
        "cases": [await capture_case(case) for case in cases()],
    }


def render(value: JsonObject) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true", help="Create once; reject existing output")
    parser.add_argument("--check", action="store_true", help="Compare complete capture (default)")
    args = parser.parse_args()
    if args.write and args.check:
        parser.error("Choose either --write or --check")
    if args.write:
        generated = render(asyncio.run(capture()))
        with OUTPUT.open("x", encoding="utf-8", newline="\n") as stream:
            stream.write(generated)
        return 0
    if OUTPUT.read_text(encoding="utf-8") != render(asyncio.run(capture())):
        parser.error("Original lookup value page differs; inspect the change, do not regenerate")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
