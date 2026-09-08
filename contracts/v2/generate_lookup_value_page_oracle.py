"""Validate retained lookup.valuePage inputs after the original Python owner retired."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from backend.adapters.pocketbase.product_rpc_support import _lookup_revision
from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "a19ccd5366d62be6338f628d06b6c5a37484f20f"
OUTPUT = Path(__file__).with_name("lookup-value-page-python-oracle.json")
METHOD = "lookup.valuePage"

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


async def capture_case(case: Case) -> JsonObject:
    raise RuntimeError("Python lookup value page capture is retired; preserve the frozen producer")


async def capture() -> JsonObject:
    raise RuntimeError("Python lookup value page capture is retired; preserve the frozen producer")


def validate_frozen_inputs() -> None:
    """Validate retained independent inputs without invoking the retired owner."""
    frozen: JsonObject = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen lookup value page producer changed")
    if frozen.get("typedGoBoundaries") != TYPED_GO_BOUNDARIES:
        raise ValueError("Frozen lookup value page typed boundaries changed")
    entries = frozen.get("cases")
    expected_cases = cases()
    if not isinstance(entries, list) or len(entries) != len(expected_cases):
        raise ValueError("Frozen lookup value page case inventory changed")
    for entry, case in zip(entries, expected_cases, strict=True):
        expected: JsonObject = {
            "name": case.name,
            "request": {"jsonrpc": "2.0", "id": case.name, "method": METHOD, "params": case.params},
            "authorityFixture": {
                "catalog": case.catalog,
                "page": case.page,
                "failure": case.failure,
            },
        }
        if not isinstance(entry, dict):
            raise ValueError("Invalid frozen lookup value page entry")
        if render({key: entry.get(key) for key in expected}) != render(expected):
            raise ValueError(f"Frozen lookup value page inputs changed: {case.name}")


def render(value: JsonObject) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true", help="Retired; always rejected")
    parser.add_argument("--check", action="store_true", help="Validate retained inputs (default)")
    args = parser.parse_args()
    if args.write:
        parser.error("Python capture is retired; preserve the original producer and frozen file")
    try:
        validate_frozen_inputs()
    except (ValueError, OSError) as error:
        parser.error(str(error))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
