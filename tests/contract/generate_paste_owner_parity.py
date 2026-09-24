"""Freeze representative paste DTO acceptance before replacing the Python owner."""

from __future__ import annotations

import copy
import json
from pathlib import Path

from pydantic import ValidationError

from backend.contracts.paste import ApplyPasteParams, PreviewPasteParams
from backend.contracts.product_rpc import JsonObject, JsonValue

OUTPUT = Path(__file__).with_name("fixtures") / "paste-owner-params-parity.json"


def child(value: JsonValue, key: str | int) -> JsonValue:
    if isinstance(value, list):
        assert isinstance(key, int)
        return value[key]
    assert isinstance(value, dict)
    assert isinstance(key, str)
    return value[key]


def cases() -> list[tuple[str, str, JsonObject]]:
    preview: JsonObject = {
        "collection": "orders",
        "schemaRevision": "schema_1",
        "selection": {"rowKeys": []},
        "startCell": {"rowKey": None, "column": "name"},
        "cells": [[{"rowIndex": 0, "columnIndex": 0, "rawValue": "value"}]],
    }
    result = [("canonical", "table.previewPaste", preview)]
    changes: list[tuple[str, tuple[str | int, ...], JsonValue, bool]] = [
        ("missing-row-index", ("cells", 0, 0, "rowIndex"), None, True),
        ("missing-column-index", ("cells", 0, 0, "columnIndex"), None, True),
        ("missing-raw-value", ("cells", 0, 0, "rawValue"), None, True),
        ("null-raw-value", ("cells", 0, 0, "rawValue"), None, False),
        ("numeric-raw-value", ("cells", 0, 0, "rawValue"), 12, False),
        ("string-index", ("cells", 0, 0, "rowIndex"), "2", False),
        ("integral-index", ("cells", 0, 0, "columnIndex"), 2.0, False),
        ("fractional-index", ("cells", 0, 0, "columnIndex"), 2.5, False),
        ("null-selection", ("selection",), None, False),
        ("array-selection", ("selection",), [], False),
        ("missing-selection", ("selection",), None, True),
        ("object-row-key", ("startCell", "rowKey"), {}, False),
        ("unicode-column-limit", ("startCell", "column"), "列" * 128, False),
        ("unicode-column-over", ("startCell", "column"), "列" * 129, False),
        ("unknown-cell", ("cells", 0, 0, "unknown"), True, False),
        ("empty-row", ("cells",), [[]], False),
    ]
    for name, path, value, remove in changes:
        changed = copy.deepcopy(preview)
        target: JsonValue = changed
        for key in path[:-1]:
            target = child(target, key)
        key = path[-1]
        assert isinstance(target, dict)
        assert isinstance(key, str)
        if remove:
            del target[key]
        else:
            target[key] = value
        result.append((name, "table.previewPaste", changed))
    result.append(
        (
            "snake-aliases",
            "table.previewPaste",
            {
                "collection": "orders",
                "schema_revision": "schema_1",
                "selection": None,
                "start_cell": {"row_key": None, "column": "name"},
                "cells": [
                    [{"row_index": 0, "column_index": 0, "raw_value": "x", "parsed_value": False}]
                ],
            },
        )
    )
    result.append(
        (
            "apply-snake-alias",
            "table.applyPaste",
            {
                "collection": "orders",
                "token": "opaque",
                "idempotency_key": "first-key",
            },
        )
    )
    return result


def main() -> None:
    output: list[dict[str, object]] = []
    for name, method, params in cases():
        model = PreviewPasteParams if method == "table.previewPaste" else ApplyPasteParams
        item: dict[str, object] = {"name": name, "method": method, "params": params}
        try:
            item["normalized"] = model.model_validate(params).model_dump(mode="json", by_alias=True)
            item["accepted"] = True
        except ValidationError:
            item["accepted"] = False
        output.append(item)
    OUTPUT.write_text(json.dumps(output, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
