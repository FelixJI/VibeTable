"""Validate retained lookup.query inputs after the original Python owner retired."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from backend.adapters.pocketbase.product_rpc_support import _lookup_revision
from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "6e25fd033697c57a4ca113caf98c90293b892548"
OUTPUT = Path(__file__).with_name("lookup-query-python-oracle.json")
METHOD = "lookup.query"


@dataclass(frozen=True)
class Case:
    name: str
    params: JsonValue
    catalog: JsonValue
    page: JsonValue
    failure: str | None = None


def descriptor() -> JsonObject:
    return {
        "lookupId": "orders.customer_name",
        "tableId": "orders",
        "fieldId": "customer_name",
        "physicalName": "customer_name",
        "displayName": "客户姓名",
        "relationFieldId": "customer",
        "targetFieldId": "name",
        "resultCardinality": "one",
        "outputStorage": "text",
        "revision": 1,
    }


def catalog_result() -> JsonObject:
    return {
        "tableId": "orders",
        "schemaRevision": "schema-1",
        "lookupMaxDepth": 8,
        "relations": [],
        "lookups": [descriptor()],
    }


def base_params(catalog: JsonObject | None = None) -> JsonObject:
    catalog = catalog if catalog is not None else catalog_result()
    lookups = catalog["lookups"]
    schema = catalog["schemaRevision"]
    assert isinstance(lookups, list)
    assert isinstance(schema, str)
    return {
        "contract": "vibetable.lookup-query.v1",
        "collection": "orders",
        "fieldRefs": ["customer_name"],
        "query": {},
        "requestGeneration": 7,
        "schemaRevision": schema,
        "permissionRevision": schema,
        "lookupRevision": _lookup_revision(schema, lookups),
    }


def view_result() -> JsonObject:
    return {
        "rows": [
            {
                "id": "o-1",
                "customer_name": "Cafe\u0301 👩🏽‍💻",
                "nil": None,
                "dynamic": {"values": [False, 0, ""], "state": "valid"},
            }
        ],
        "offset": 0,
        "limit": 50,
        "filteredRows": 1,
        "totalRows": 3,
        "querySnapshot": {
            "snapshotId": "snap-1",
            "digest": "digest-1",
            "databaseId": "workspace-1",
            "table": "orders",
            "schemaRevision": "schema-1",
            "dataRevision": 2,
            "normalizedQuery": {},
        },
        "groupRows": [],
        "groupOffset": 0,
        "groupLimit": 5000,
        "hasMoreGroups": False,
    }


TYPED_GO_BOUNDARIES: JsonObject = {
    "catalog-nonobject": "CatalogResult cannot encode an array root",
    "catalog-lookups-null": "Malformed null catalog lookup list; typed nil slice is representable but must not be silently normalized",
    "catalog-unselected-malformed": "Typed LookupDescriptor emits fields omitted by the malformed descriptor",
    "view-nonobject": "LookupQueryResult cannot encode an array root",
    "view-missing-rows": "Typed Page always emits rows",
    "view-boolean-count": "Typed page integer cannot encode bool",
    "view-missing-groupRows": "Typed result always emits groupRows",
    "view-nonboolean-window": "Typed HasMoreGroups cannot encode integer",
    "group-missing-summaries": "Typed GroupRow always emits summaries",
    "group-boolean-count": "Typed GroupRow.Count cannot encode bool",
    "snapshot-open-object": "Typed QuerySnapshot cannot preserve unknown members or missing defined fields",
    "catalog-product-error": "First-hop public domain error, expressible via mutation.ProductError to PublicError; replay required",
    "page-product-error": "Second-hop public domain error, expressible via mutation.ProductError to PublicError; replay required",
    "catalog-transport-error": "Scripted first-hop transport outage",
    "page-transport-error": "Scripted second-hop transport outage",
}


def cases() -> tuple[Case, ...]:
    params, catalog, view = base_params(), catalog_result(), view_result()
    duplicate = {
        **catalog,
        "lookups": [
            descriptor(),
            {**descriptor(), "displayName": "后者", "outputStorage": "number"},
        ],
    }
    malformed = {**catalog, "lookups": [descriptor(), {"physicalName": "unselected"}]}
    grouped = {
        **view,
        "groupRows": [
            {
                "key": ["EU", None],
                "count": 2,
                "summaries": [99],
                "parentCount": 3,
                "parentSummaries": [123],
            },
            {
                "key": ["US", False],
                "count": 1,
                "summaries": [],
                "parentCount": 1,
                "parentSummaries": [],
            },
            {
                "key": ["EU", ""],
                "count": 1,
                "summaries": [],
                "parentCount": 999,
                "parentSummaries": [],
            },
        ],
    }
    return (
        Case("dynamic-rows", params, catalog, view),
        Case(
            "empty-selection-and-negative-generation",
            {**params, "fieldRefs": [], "requestGeneration": -1},
            catalog,
            view,
        ),
        Case(
            "contract-and-whitespace-are-not-normalized",
            {**params, "contract": "other", "collection": " 中文 "},
            catalog,
            view,
        ),
        Case(
            "group-translation-and-parent-first-occurrence",
            {
                **params,
                "query": {
                    "offset": 2,
                    "limit": 1,
                    "groups": [
                        {"fieldRef": "region", "direction": "desc"},
                        {"fieldRef": "customer_name"},
                    ],
                },
            },
            catalog,
            grouped,
        ),
        Case(
            "single-group-null-key",
            params,
            catalog,
            {**view, "groupRows": [{"key": [None], "count": -2, "summaries": []}]},
        ),
        Case("duplicate-physical-last-wins", base_params(duplicate), duplicate, view),
        Case(
            "duplicate-fieldrefs-preserved",
            {**params, "fieldRefs": ["customer_name", "customer_name"]},
            catalog,
            view,
        ),
        Case(
            "groups-type-before-catalog-error",
            {**params, "query": {"groups": None}},
            catalog,
            view,
            "catalog-product",
        ),
        Case(
            "stale-before-group-direction",
            {
                **params,
                "schemaRevision": "stale",
                "query": {"groups": [{"fieldRef": "x", "direction": "bad"}]},
            },
            catalog,
            view,
        ),
        Case("stale-permission", {**params, "permissionRevision": "stale"}, catalog, view),
        Case("stale-lookup", {**params, "lookupRevision": "stale"}, catalog, view),
        Case(
            "group-direction-before-query-error",
            {**params, "query": {"groups": [{"fieldRef": "x", "direction": "bad"}]}},
            catalog,
            view,
            "page-product",
        ),
        Case("group-nonobject", {**params, "query": {"groups": [None]}}, catalog, view),
        Case(
            "group-empty-field", {**params, "query": {"groups": [{"fieldRef": ""}]}}, catalog, view
        ),
        Case("unknown-field-after-query", {**params, "fieldRefs": ["unknown"]}, catalog, view),
        Case(
            "query-error-before-unknown-field",
            {**params, "fieldRefs": ["unknown"]},
            catalog,
            view,
            "page-product",
        ),
        Case(
            "window-before-column-projection",
            base_params(malformed),
            malformed,
            {**view, "hasMoreGroups": True},
        ),
        Case("catalog-unselected-malformed", base_params(malformed), malformed, view),
        Case("catalog-nonobject", params, [], view),
        Case("catalog-lookups-null", params, {**catalog, "lookups": None}, view),
        Case("view-nonobject", params, catalog, []),
        Case("view-missing-rows", params, catalog, {k: v for k, v in view.items() if k != "rows"}),
        Case("view-boolean-count", params, catalog, {**view, "totalRows": True}),
        Case(
            "view-missing-groupRows",
            params,
            catalog,
            {k: v for k, v in view.items() if k != "groupRows"},
        ),
        Case("view-nonboolean-window", params, catalog, {**view, "hasMoreGroups": 0}),
        Case(
            "group-missing-summaries",
            params,
            catalog,
            {**view, "groupRows": [{"key": ["x"], "count": 1}]},
        ),
        Case(
            "group-boolean-count",
            params,
            catalog,
            {**view, "groupRows": [{"key": ["x"], "count": True, "summaries": []}]},
        ),
        Case(
            "group-empty-key",
            params,
            catalog,
            {**view, "groupRows": [{"key": [], "count": 1, "summaries": []}]},
        ),
        Case(
            "group-missing-parent",
            params,
            catalog,
            {**view, "groupRows": [{"key": ["x", "y"], "count": 1, "summaries": []}]},
        ),
        Case(
            "snapshot-open-object",
            params,
            catalog,
            {**view, "querySnapshot": {"custom": [None, False, 0]}},
        ),
        Case(
            "negative-authority-counts",
            params,
            catalog,
            {**view, "offset": -1, "limit": 0, "filteredRows": -2, "totalRows": -3},
        ),
        Case("boolean-generation", {**params, "requestGeneration": True}, catalog, view),
        Case("unknown-param", {**params, "unknown": None}, catalog, view),
        Case("catalog-product-error", params, catalog, view, "catalog-product"),
        Case("page-product-error", params, catalog, view, "page-product"),
        Case("catalog-transport-error", params, catalog, view, "catalog-transport"),
        Case("page-transport-error", params, catalog, view, "page-transport"),
        *typed_shape_cases(),
    )


def typed_shape_cases() -> tuple[Case, ...]:
    """Independent authority inputs whose complete wire shape can be represented by Go DTOs."""
    catalog = {
        **catalog_result(),
        "lookups": [{**descriptor(), "path": [{"relationId": "orders.customer"}]}],
    }
    query: JsonObject = {"offset": 0, "limit": 50}
    params = {**base_params(catalog), "query": query}
    view = view_result()
    snapshot = view["querySnapshot"]
    assert isinstance(snapshot, dict)
    view = {**view, "querySnapshot": {**snapshot, "normalizedQuery": query}}
    grouped_params = {
        **params,
        "query": {
            **query,
            "groups": [
                {"fieldRef": "region", "direction": "desc"},
                {"fieldRef": "customer_name", "direction": "asc"},
            ],
        },
    }
    grouped_view = {
        **view,
        "rows": [
            {"id": "o-1", "region": "EU", "customer_name": "Cafe\u0301 👩🏽‍💻"},
            {"id": "o-2", "region": "US", "customer_name": None},
            {"id": "o-3", "region": "EU", "customer_name": ""},
        ],
        "filteredRows": 3,
        "totalRows": 3,
        "groupRows": [
            {
                "key": ["EU", "Cafe\u0301 👩🏽‍💻"],
                "count": 1,
                "summaries": [1],
                "parentCount": 2,
                "parentSummaries": [2],
            },
            {
                "key": ["US", None],
                "count": 1,
                "summaries": [1],
                "parentCount": 1,
                "parentSummaries": [1],
            },
            {
                "key": ["EU", ""],
                "count": 1,
                "summaries": [1],
                "parentCount": 2,
                "parentSummaries": [2],
            },
        ],
    }
    return (
        Case("typed-shape-dynamic-rows", params, catalog, view),
        Case("typed-shape-grouped-rows", grouped_params, catalog, grouped_view),
    )


def render(value: JsonObject) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


async def capture_case(case: Case) -> JsonObject:
    raise RuntimeError("The original Python lookup.query owner is retired; retain its wire corpus")


async def capture() -> JsonObject:
    raise RuntimeError("The original Python lookup.query owner is retired; retain its wire corpus")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true")
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if args.write:
        parser.error("The Python owner is retired; the original lookup query cannot be regenerated")
    frozen = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if frozen.get("producerCommit") != PRODUCER_COMMIT:
        parser.error("The original lookup query producer differs")
    actual = [
        {
            "name": case.name,
            "request": {"jsonrpc": "2.0", "id": case.name, "method": METHOD, "params": case.params},
            "authorityFixture": {
                "catalog": case.catalog,
                "page": case.page,
                "failure": case.failure,
            },
        }
        for case in cases()
    ]
    retained = [
        {
            "name": entry["name"],
            "request": entry["request"],
            "authorityFixture": entry["authorityFixture"],
        }
        for entry in frozen["cases"]
    ]
    if actual != retained or frozen.get("typedGoBoundaries") != TYPED_GO_BOUNDARIES:
        parser.error("Retained lookup query inputs or typed boundaries differ; do not regenerate")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
