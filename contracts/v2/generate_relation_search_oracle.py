"""Retain original Python relation search inputs after retiring its runtime owner."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "8cf989c5873830d28d61067a9afb82ddb06db124"
OUTPUT = Path(__file__).with_name("relation-search-python-oracle.json")
METHODS = ("relation.searchTargets",)


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue = None
    failure: str | None = None


def cases() -> tuple[Case, ...]:
    method = METHODS[0]
    params: JsonObject = {"relationId": "rel-orders"}
    item: JsonObject = {
        "tableId": "orders",
        "recordId": "r1",
        "label": "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي",
        "secondaryLabel": "private authority detail",
        "extra": {"enabled": False},
    }
    result: JsonObject = {
        "items": [item],
        "total": 1,
        "snapshot": {"opaque": "authority-only"},
        "extra": False,
    }
    return (
        Case("omitted-query-and-paging-defaults", method, params, result),
        Case(
            "explicit-query-and-paging",
            method,
            {**params, "query": "中文 Cafe\u0301", "offset": 2, "limit": 3},
            result,
        ),
        Case("whitespace-query-preserved", method, {**params, "query": " \t "}, result),
        Case("whitespace-relation-forwarded", method, {"relationId": " "}, result),
        Case("empty-result", method, params, {"items": [], "total": 0}),
        Case(
            "non-object-items-filtered",
            method,
            params,
            {"items": [None, False, 0, "", [], item], "total": 6},
        ),
        Case(
            "all-non-object-items-filtered",
            method,
            params,
            {"items": [None, False, 0, "", []], "total": 5},
        ),
        Case("negative-total-preserved", method, params, {**result, "total": -1}),
        Case("limit-101-forwarded", method, {**params, "limit": 101}, result),
        Case(
            "negative-offset-zero-limit-forwarded",
            method,
            {**params, "offset": -1, "limit": 0},
            result,
        ),
        Case("long-query-forwarded", method, {**params, "query": "x" * 257}, result),
        Case("missing-relation", method, {}),
        Case("empty-relation", method, {"relationId": ""}),
        Case("empty-query", method, {**params, "query": ""}),
        Case("null-query", method, {**params, "query": None}),
        Case("boolean-offset", method, {**params, "offset": False}),
        Case("string-limit", method, {**params, "limit": "50"}),
        Case("unknown-parameter", method, {**params, "collection": "orders"}),
        Case("nested-credential", method, {**params, "query": {"password": "test-only"}}),
        Case("non-object-result", method, params, []),
        Case("missing-items", method, params, {"total": 1}),
        Case("null-items", method, params, {"items": None, "total": 1}),
        Case(
            "item-missing-label",
            method,
            params,
            {"items": [{"tableId": "orders", "recordId": "r1"}], "total": 1},
        ),
        Case(
            "item-empty-record-id",
            method,
            params,
            {"items": [{**item, "recordId": ""}], "total": 1},
        ),
        Case(
            "item-non-text-label", method, params, {"items": [{**item, "label": False}], "total": 1}
        ),
        Case("missing-total", method, params, {"items": []}),
        Case("boolean-total", method, params, {"items": [], "total": False}),
        Case("string-total", method, params, {"items": [], "total": "0"}),
        Case("public-error", method, params, failure="product"),
        Case("transport-error", method, params, failure="transport"),
    )


def validate_frozen_inputs() -> None:
    """Check historical inputs without calling the retired Python owner."""
    frozen: JsonObject = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen relation search producer changed")
    entries = frozen.get("cases")
    expected_cases = cases()
    if not isinstance(entries, list) or len(entries) != len(expected_cases):
        raise ValueError("Frozen relation search case inventory changed")
    for entry, case in zip(entries, expected_cases, strict=True):
        expected: JsonObject = {
            "name": case.name,
            "request": {
                "jsonrpc": "2.0",
                "id": case.name,
                "method": case.method,
                "params": case.params,
            },
            "authorityFixture": {"response": case.response, "failure": case.failure},
        }
        if not isinstance(entry, dict):
            raise ValueError("Invalid frozen relation search entry")
        actual = {key: entry.get(key) for key in expected}
        if render(actual) != render(expected):
            raise ValueError(f"Frozen relation search inputs changed: {case.name}")


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
