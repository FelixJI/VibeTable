"""Retain the original query read wire after both methods migrate to Go."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "b55f878641bf74c0b49b04222a217c48abf544a7"
OUTPUT = Path(__file__).with_name("query-read-python-oracle.json")
METHODS = ("query.readRows", "query.validateSnapshot")
REPLAY_METHODS: tuple[str, ...] = ()


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue = None
    failure: str | None = None


def cases() -> tuple[Case, ...]:
    read, validate = METHODS
    ids: JsonObject = {"tableId": "orders", "rowIds": ["r1"]}
    snapshot: JsonObject = {"snapshot": {"schemaRevision": "schema_1", "dataRevision": "data_0"}}
    return (
        Case(
            "read-unicode-falsy",
            read,
            ids,
            {
                "rows": [
                    {
                        "id": "r1",
                        "text": "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي",
                        "blank": "",
                        "zero": 0,
                        "negativeZero": -0.0,
                        "false": False,
                        "null": None,
                        "array": [],
                        "object": {},
                    }
                ]
            },
        ),
        Case("read-empty-ids", read, {**ids, "rowIds": []}, {"rows": []}),
        Case(
            "read-duplicate-whitespace-ids",
            read,
            {**ids, "rowIds": ["r1", "r1", " "]},
            {"rows": []},
        ),
        Case("read-table-not-path-segment", read, {**ids, "tableId": "中文/表"}, {"rows": []}),
        Case("read-missing-ids", read, {"tableId": "orders"}),
        Case("read-ids-wrong-type", read, {**ids, "rowIds": "r1"}),
        Case("read-empty-id", read, {**ids, "rowIds": [""]}),
        Case("read-zero-id", read, {**ids, "rowIds": [0]}),
        Case("read-empty-table", read, {**ids, "tableId": ""}),
        Case("read-unknown-field", read, {**ids, "limit": 0}),
        Case("read-malformed-rows", read, ids, {"rows": [None]}),
        Case("read-product-error", read, ids, failure="product"),
        Case("read-transport-error", read, ids, failure="transport"),
        Case(
            "validate-query-unicode-falsy",
            validate,
            {
                **snapshot,
                "currentQuery": {
                    "filters": [{"value": "中文\u200fعربي"}],
                    "sorts": [],
                    "offset": 0,
                    "limit": 0,
                    "blank": "",
                    "enabled": False,
                },
            },
            {"valid": True, "reason": "", "revision": 0},
        ),
        Case(
            "validate-empty-snapshot",
            validate,
            {"snapshot": {}},
            {"valid": False, "reason": "snapshot_stale"},
        ),
        Case(
            "validate-empty-current-query",
            validate,
            {**snapshot, "currentQuery": {}},
            {"valid": True},
        ),
        Case("validate-missing-snapshot", validate, {}),
        Case("validate-null-snapshot", validate, {"snapshot": None}),
        Case("validate-null-current-query", validate, {**snapshot, "currentQuery": None}),
        Case("validate-unknown-field", validate, {**snapshot, "tableId": "orders"}),
        Case("validate-malformed-response", validate, snapshot, []),
        Case("validate-product-error", validate, snapshot, failure="product"),
        Case("validate-transport-error", validate, snapshot, failure="transport"),
    )


async def capture_case(case: Case) -> JsonObject:
    raise ValueError("Migrated query methods must consume the frozen oracle in Go")


async def capture() -> JsonObject:
    raise ValueError("Migrated query methods must consume the frozen oracle in Go")


def validate_frozen_inputs() -> None:
    frozen = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen query read producer changed")
    entries = frozen.get("cases")
    if not isinstance(entries, list) or len(entries) != len(cases()):
        raise ValueError("Frozen query read case inventory changed")
    for entry, case in zip(entries, cases(), strict=True):
        retained = {
            "name": entry["name"],
            "request": entry["request"],
            "authorityFixture": entry["authorityFixture"],
        }
        expected = {
            "name": case.name,
            "request": {
                "jsonrpc": "2.0",
                "id": case.name,
                "method": case.method,
                "params": case.params,
            },
            "authorityFixture": {"response": case.response, "failure": case.failure},
        }
        if render(retained) != render(expected):
            raise ValueError("Frozen query read inputs changed; do not regenerate")


def render(value: JsonObject) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--write", action="store_true", help="Retired after owner migration; always rejected"
    )
    parser.add_argument(
        "--check", action="store_true", help="Validate historical inputs without replay (default)"
    )
    args = parser.parse_args()
    if args.write and args.check:
        parser.error("Choose either --write or --check")
    if args.write:
        parser.error("Oracle creation is retired after owner migration; preserve the frozen file")
    try:
        validate_frozen_inputs()
    except (ValueError, OSError) as error:
        parser.error(str(error))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
