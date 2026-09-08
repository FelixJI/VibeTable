"""Check retained original Python relation preview inputs after its owner migration."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "6ed36810f3753caed5e2e8ca27a4d4ad2117d41d"
OUTPUT = Path(__file__).with_name("relation-preview-python-oracle.json")
METHODS = ("relation.previewDelta",)

# These describe authority wire shapes, not additional domain success cases.
TYPED_GO_BOUNDARIES: JsonObject = {
    "non-object-result": "DeltaPreview is a struct, not an array.",
    "non-object-current-members-filtered": "[]TargetRef cannot contain non-object members.",
    "non-array-current": "Current is []TargetRef, not an object.",
    "current-nontext-label": "TargetRef.Label is string, not bool.",
    "secondary-label-falsy-becomes-null": "TargetRef.SecondaryLabel is string, not arbitrary JSON.",
    "secondary-label-truthy-nontext-preserved": "TargetRef.SecondaryLabel cannot carry truthy non-text JSON.",
    "can-apply-missing-is-false": "DeltaPreview always emits CanApply.",
    "can-apply-one-is-false": "CanApply is bool, not integer.",
    "can-apply-string-is-false": "CanApply is bool, not string.",
    "can-apply-null-is-false": "CanApply is bool, not null.",
    "transport-error": "An in-process PreviewDelta call has no Python HTTP transport boundary.",
}


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue = None
    failure: str | None = None


def base_params() -> JsonObject:
    return {
        "relationId": "orders.related",
        "sourceItemId": "source-1",
        "expectedSchemaRevision": "schema-1",
        "adds": [],
        "removes": [],
        "idempotencyKey": "preview-1",
    }


def cases() -> tuple[Case, ...]:
    method = METHODS[0]
    params = base_params()
    target: JsonObject = {
        "tableId": "authors",
        "recordId": "author-1",
        "label": "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي",
        "secondaryLabel": "副标题",
    }
    result: JsonObject = {
        "current": [target],
        "result": [],
        "adds": 99,
        "removes": 99,
        "diagnostics": ["authority-only"],
        "canApply": True,
    }
    falsy_labels: tuple[JsonValue, ...] = (None, "", False, 0, [], {})
    truthy_labels: tuple[JsonValue, ...] = (True, 1, ["detail"], {"detail": False})
    return (
        Case("empty-delta-hydrates-current", method, params, result),
        Case("empty-current", method, params, {"current": [], "canApply": True}),
        Case(
            "nested-target-translation-and-original-echo",
            method,
            {
                **params,
                "adds": [
                    {
                        "target": {"collection": "authors", "itemId": "a2", "label": "新增 👩🏽‍💻"},
                        "note": [0, False],
                    }
                ],
                "removes": [{"collection": "authors", "itemId": "author-1"}],
            },
            result,
        ),
        Case(
            "target-alias-priority-and-fallback",
            method,
            {
                **params,
                "adds": [
                    {
                        "tableId": "preferred",
                        "collection": "ignored",
                        "recordId": "preferred-id",
                        "itemId": "ignored",
                        "label": "",
                    },
                    {
                        "tableId": False,
                        "collection": "authors",
                        "recordId": "",
                        "itemId": "fallback-id",
                    },
                ],
            },
            result,
        ),
        Case(
            "non-object-nested-target-uses-outer",
            method,
            {**params, "adds": [{"target": False, "collection": "authors", "itemId": "a2"}]},
            result,
        ),
        Case(
            "empty-nested-object-does-not-fall-back",
            method,
            {**params, "adds": [{"target": {}, "collection": "authors", "itemId": "a2"}]},
        ),
        Case(
            "whitespace-identifiers-and-label-preserved",
            method,
            {
                **params,
                "relationId": " ",
                "sourceItemId": "\t",
                "adds": [{"collection": " ", "itemId": " ", "label": "\t"}],
            },
            result,
        ),
        Case(
            "expected-date-null-echoed-not-forwarded",
            method,
            {**params, "expectedDateUpdated": None},
            result,
        ),
        Case(
            "expected-date-object-echoed-not-forwarded",
            method,
            {**params, "expectedDateUpdated": {"nested": [False, 0, "中文"]}},
            result,
        ),
        Case(
            "secondary-label-falsy-becomes-null",
            method,
            params,
            {
                "current": [{**target, "secondaryLabel": value} for value in falsy_labels],
                "canApply": True,
            },
        ),
        Case(
            "secondary-label-truthy-nontext-preserved",
            method,
            params,
            {
                "current": [{**target, "secondaryLabel": value} for value in truthy_labels],
                "canApply": True,
            },
        ),
        Case(
            "non-object-current-members-filtered",
            method,
            params,
            {"current": [None, False, 0, "", [], target], "canApply": True},
        ),
        Case("can-apply-missing-is-false", method, params, {"current": []}),
        Case("can-apply-false", method, params, {"current": [], "canApply": False}),
        Case("can-apply-one-is-false", method, params, {"current": [], "canApply": 1}),
        Case("can-apply-string-is-false", method, params, {"current": [], "canApply": "true"}),
        Case("can-apply-null-is-false", method, params, {"current": [], "canApply": None}),
        Case("non-object-result", method, params, []),
        Case("missing-current", method, params, {"canApply": True}),
        Case("null-current", method, params, {"current": None, "canApply": True}),
        Case("non-array-current", method, params, {"current": {}, "canApply": True}),
        Case(
            "current-missing-label",
            method,
            params,
            {"current": [{"tableId": "authors", "recordId": "a1"}], "canApply": True},
        ),
        Case(
            "current-empty-record-id",
            method,
            params,
            {"current": [{**target, "recordId": ""}], "canApply": True},
        ),
        Case(
            "current-nontext-label",
            method,
            params,
            {"current": [{**target, "label": False}], "canApply": True},
        ),
        Case(
            "missing-required-adds",
            method,
            {key: value for key, value in params.items() if key != "adds"},
        ),
        Case("unknown-top-level-field", method, {**params, "extra": True}),
        Case("empty-relation-is-handler-error", method, {**params, "relationId": ""}),
        Case("wrong-relation-type-is-handler-error", method, {**params, "relationId": False}),
        Case("null-adds-is-handler-error", method, {**params, "adds": None}),
        Case("non-object-add-is-handler-error", method, {**params, "adds": [False]}),
        Case(
            "null-target-label-is-handler-error",
            method,
            {**params, "adds": [{"collection": "authors", "itemId": "a2", "label": None}]},
        ),
        Case("top-level-authority-alias-rejected", method, {**params, "sourceRecordId": "other"}),
        Case(
            "nested-credential-rejected-before-handler",
            method,
            {**params, "expectedDateUpdated": {"password": "oracle-only"}},
        ),
        Case("public-error", method, params, failure="product"),
        Case("transport-error", method, params, failure="transport"),
        Case(
            "duplicate-adds-forwarded-to-authority",
            method,
            {
                **params,
                "adds": [
                    {"collection": "authors", "itemId": "a2"},
                    {"collection": "authors", "itemId": "a2"},
                ],
            },
            result,
        ),
        Case("non-object-params", method, []),
    )


async def capture_case(case: Case) -> JsonObject:
    raise RuntimeError("Python preview capture is retired; preserve the frozen producer")


async def capture() -> JsonObject:
    raise RuntimeError("Python preview capture is retired; preserve the frozen producer")


def validate_frozen_inputs() -> None:
    """Check historical inputs without calling the retired Python owner."""
    frozen: JsonObject = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen relation preview producer changed")
    if frozen.get("typedGoBoundaries") != TYPED_GO_BOUNDARIES:
        raise ValueError("Frozen preview typed boundaries changed")
    entries = frozen.get("cases")
    expected_cases = cases()
    if not isinstance(entries, list) or len(entries) != len(expected_cases):
        raise ValueError("Frozen relation preview case inventory changed")
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
            raise ValueError("Invalid frozen relation preview entry")
        actual = {key: entry.get(key) for key in expected}
        if render(actual) != render(expected):
            raise ValueError(f"Frozen relation preview inputs changed: {case.name}")


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
