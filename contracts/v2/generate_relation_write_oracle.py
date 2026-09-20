"""Freeze original Relation write Product wire; HTTP responses are scripted, not domain proof."""

from __future__ import annotations

import argparse
import json
from copy import deepcopy
from pathlib import Path

from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "55e0bfa41bbbf6d2f4129227d29a085e6c599a87"
OUTPUT = Path(__file__).with_name("relation-write-python-oracle.json")
METHODS = ("relation.createTarget", "relation.updateSingle", "relation.applyDelta")
BOUNDARY = "Original Python dispatcher/closed DTO/adapter/error projection; scripted HTTP authority, not Go execution"


def cases() -> list[JsonObject]:
    target: JsonObject = {"tableId": "authors", "recordId": "a-new", "label": "中文 👩🏽‍💻"}
    catalog: JsonObject = {
        "relations": [
            {
                "relationId": "orders.author",
                "sourceTableId": "orders",
                "physicalName": "author_id",
                "targetTableId": "authors",
                "cardinality": "one",
            }
        ]
    }
    rows: JsonObject = {"rows": [{"id": "o-1", "author_id": "a-old"}]}
    receipt: JsonObject = {"receipt": {"status": "applied", "changeSetId": "change-1"}}
    renderer: JsonObject = {"collection": "authors", "itemId": "a-new", "label": "中文 👩🏽‍💻"}
    delta: JsonObject = {
        "relationId": "orders.author",
        "sourceItemId": "o-1",
        "expectedSchemaRevision": "schema-1",
        "idempotencyKey": "write-1",
    }
    descriptors = catalog["relations"]
    assert isinstance(descriptors, list)
    descriptor = descriptors[0]
    assert isinstance(descriptor, dict)
    requests: list[JsonObject] = [
        {"relationId": "orders.author", "idempotencyKey": "create-1"},
        {**delta, "target": renderer},
        {**delta, "adds": [renderer], "removes": [{"collection": "authors", "itemId": "a-old"}]},
    ]
    responses: list[list[JsonValue]] = [
        [{"target": target}],
        [catalog, rows, receipt],
        [{"current": [target]}],
    ]
    result: list[JsonObject] = []

    def add(
        index: int,
        name: str,
        params: JsonValue,
        bodies: list[JsonValue] | None = None,
        status: int = 200,
    ) -> None:
        result.append(
            {
                "name": METHODS[index] + ":" + name,
                "method": METHODS[index],
                "params": deepcopy(params),
                "bodies": deepcopy(responses[index] if bodies is None else bodies),
                "status": status,
            }
        )

    for index, params in enumerate(requests):
        add(index, "success", params)
        add(index, "unknown-param", {**params, "extra": True})
        add(index, "internal-alias-not-public", {**params, "expectedDigest": "not-public"})
        add(
            index,
            "missing-key",
            {key: value for key, value in params.items() if key != "idempotencyKey"},
        )
        add(index, "empty-key", {**params, "idempotencyKey": ""})
        add(index, "wrong-relation-type", {**params, "relationId": False})
        add(index, "credential", {**params, "sessionSecret": "not-a-secret"})
        add(index, "nonobject", [])
        add(
            index,
            "authority-error",
            params,
            [
                {
                    "code": "relation.target_invalid",
                    "message": "wrong target",
                    "path": None,
                    "details": None,
                    "retryable": False,
                }
            ],
            422,
        )
    add(0, "label", {**requests[0], "label": "中文"})
    add(
        0,
        "values",
        {
            **requests[0],
            "values": {"title": "new", "count": 9007199254740993, "empty": None, "zero": 0},
        },
    )
    add(0, "empty-label", {**requests[0], "label": ""})
    add(0, "null-values", {**requests[0], "values": None})
    add(0, "wrong-values", {**requests[0], "values": []})
    add(0, "missing-target-result", requests[0], [{}])
    add(1, "clear", {**requests[1], "target": None})
    add(
        1,
        "unchanged",
        requests[1],
        [catalog, {"rows": [{"id": "o-1", "author_id": "a-new"}]}, receipt],
    )
    add(
        1,
        "source-empty",
        requests[1],
        [catalog, {"rows": [{"id": "o-1", "author_id": None}]}, receipt],
    )
    add(
        1,
        "wrong-cardinality",
        requests[1],
        [{"relations": [{**descriptor, "cardinality": "many"}]}],
    )
    add(1, "missing-source", requests[1], [catalog, {"rows": []}])
    add(1, "wrong-table", {**requests[1], "target": {**renderer, "collection": "elsewhere"}})
    add(1, "wrong-target-type", {**requests[1], "target": []})
    add(
        1,
        "source-invalid-value",
        requests[1],
        [catalog, {"rows": [{"id": "o-1", "author_id": False}]}],
    )
    for index in (1, 2):
        add(index, "date-null", {**requests[index], "expectedDateUpdated": None})
        add(index, "date-text", {**requests[index], "expectedDateUpdated": "stale"})
        add(index, "date-object", {**requests[index], "expectedDateUpdated": {"anything": False}})
        variants: list[tuple[str, JsonValue]] = [
            ("nested-target", {"target": renderer, "note": 0}),
            ("alias-priority", {**renderer, "tableId": "authors", "recordId": "a-priority"}),
            ("alias-fallback", {**renderer, "tableId": False, "recordId": ""}),
            ("empty-nested-target", {"target": {}, **renderer}),
            ("null-label", {**renderer, "label": None}),
            ("missing-label", {key: value for key, value in renderer.items() if key != "label"}),
        ]
        for name, value in variants:
            field = {"target": value} if index == 1 else {"adds": [value]}
            add(index, name, {**requests[index], **field})
    add(2, "empty-delta", {**requests[2], "adds": [], "removes": []}, [{"current": []}])
    add(2, "wrong-adds", {**requests[2], "adds": {}})
    add(2, "wrong-target", {**requests[2], "adds": [False]})
    add(2, "missing-current-result", requests[2], [{}])
    return result


async def capture_case(case: JsonObject) -> JsonObject:
    raise RuntimeError("Relation write Python capture is retired; preserve the original producer")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.parse_args()
    frozen = json.loads(OUTPUT.read_text(encoding="utf-8"))
    assert frozen["producerCommit"] == PRODUCER_COMMIT
    assert frozen["boundary"] == BOUNDARY
    expected = cases()
    assert len(frozen["cases"]) == len(expected)
    for entry, case in zip(frozen["cases"], expected, strict=True):
        assert entry["name"] == case["name"]
        assert entry["request"] == {
            "jsonrpc": "2.0",
            "id": case["name"],
            "method": case["method"],
            "params": case["params"],
        }
        assert entry["authorityFixture"] == case["bodies"]
        assert entry["authorityStatus"] == case["status"]
        assert entry["response"]["id"] == case["name"]
    print(f"Retained Relation write Python oracle: {len(expected)} cases")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
