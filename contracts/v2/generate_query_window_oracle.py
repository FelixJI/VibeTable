"""Validate historical inputs after the Python query window owners have migrated."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "c97c83336e4aa1bdf993fc46a7de57040219fb03"
OUTPUT = Path(__file__).with_name("query-window-python-oracle.json")
METHODS = ("query.page", "query.cursorOpen", "query.cursorFetch")
PYTHON_REPLAY_METHODS: tuple[str, ...] = ()


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue = None
    failure: str | None = None


def cases() -> tuple[Case, ...]:
    page, open_cursor, fetch = METHODS
    params: JsonObject = {"tableId": "中文/表", "query": {"offset": 0, "limit": 0}}
    snapshot: JsonObject = {"schemaRevision": "schema_1", "dataRevision": 0}
    rows: list[JsonValue] = [
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
    page_result: JsonObject = {
        "rows": rows,
        "offset": 3,
        "limit": 5,
        "filteredRows": 1,
        "totalRows": 7,
        "querySnapshot": snapshot,
        "ignored": "not projected",
    }
    window: JsonObject = {
        "rows": rows,
        "nextCursor": "游标 /+",
        "hasMore": True,
        "filteredRows": 1,
        "totalRows": 7,
        "querySnapshot": snapshot,
        "ignored": "not projected",
    }
    terminal: JsonObject = {**window, "rows": [], "nextCursor": None, "hasMore": False}
    cursor: JsonObject = {"cursor": "游标 /+"}
    return (
        Case("page-unicode-falsy-offset-limit", page, params, page_result),
        Case(
            "page-empty-query",
            page,
            {**params, "query": {}},
            {**page_result, "rows": [], "offset": 0, "limit": 0},
        ),
        Case(
            "page-nested-query-forwarded",
            page,
            {
                **params,
                "query": {"filters": [{"value": False}], "sorts": [], "offset": -1, "limit": 0},
            },
            page_result,
        ),
        Case("page-missing-query", page, {"tableId": "orders"}),
        Case("page-null-query", page, {**params, "query": None}),
        Case("page-unknown-field", page, {**params, "extra": 0}),
        Case("page-malformed-rows", page, params, {**page_result, "rows": [None]}),
        Case("page-malformed-offset", page, params, {**page_result, "offset": False}),
        Case("page-product-error", page, params, failure="product"),
        Case("page-transport-error", page, params, failure="transport"),
        Case("open-unicode-falsy", open_cursor, params, window),
        Case("open-terminal-window", open_cursor, params, terminal),
        Case("open-empty-table", open_cursor, {**params, "tableId": ""}),
        Case("open-unknown-field", open_cursor, {**params, "cursor": "x"}),
        Case("open-malformed-snapshot", open_cursor, params, {**window, "querySnapshot": None}),
        Case("open-inconsistent-null-cursor", open_cursor, params, {**window, "nextCursor": None}),
        Case("open-product-error", open_cursor, params, failure="product"),
        Case("open-transport-error", open_cursor, params, failure="transport"),
        Case("fetch-unicode-cursor", fetch, cursor, window),
        Case("fetch-terminal-window", fetch, cursor, terminal),
        Case("fetch-empty-next-cursor", fetch, cursor, {**window, "nextCursor": ""}),
        Case("fetch-empty-cursor", fetch, {"cursor": ""}),
        Case("fetch-null-cursor", fetch, {"cursor": None}),
        Case("fetch-unknown-field", fetch, {**cursor, "limit": 1}),
        Case("fetch-malformed-next-cursor", fetch, cursor, {**window, "nextCursor": 0}),
        Case("fetch-product-error", fetch, cursor, failure="product"),
        Case("fetch-transport-error", fetch, cursor, failure="transport"),
    )


def replay_cases() -> tuple[Case, ...]:
    return tuple(case for case in cases() if case.method in PYTHON_REPLAY_METHODS)


async def capture_case(case: Case) -> JsonObject:
    # All three query methods retired before this boundary migration. Keep the
    # existing refusal contract without importing their removed Python adapter.
    raise ValueError(f"Python oracle replay is retired for {case.method}")


async def capture() -> JsonObject:
    return {
        "producerCommit": PRODUCER_COMMIT,
        "boundary": "Python Product dispatcher + adapter; scripted authority transport",
        "cases": [await capture_case(case) for case in replay_cases()],
    }


def render(value: JsonObject) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


def validate_frozen_inputs() -> None:
    frozen = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen query window producer changed")
    entries = frozen.get("cases")
    expected_cases = cases()
    if not isinstance(entries, list) or len(entries) != len(expected_cases):
        raise ValueError("Frozen query window case inventory changed")
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
            raise ValueError("Invalid frozen query window entry")
        if render({key: entry.get(key) for key in expected}) != render(expected):
            raise ValueError(f"Frozen query window inputs changed: {case.name}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--write",
        action="store_true",
        help="Retired: the frozen producer oracle cannot be regenerated",
    )
    parser.add_argument(
        "--check", action="store_true", help="Compare without changing the frozen oracle (default)"
    )
    args = parser.parse_args()
    if args.write:
        parser.error("The frozen producer oracle cannot be regenerated after owner migration")
    try:
        validate_frozen_inputs()
    except (ValueError, OSError) as error:
        parser.error(str(error))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
