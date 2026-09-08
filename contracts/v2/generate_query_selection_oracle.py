"""Validate retained selection inputs; Python capture retired after owner migration."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "ccfbce59a811fcfb9b27baa11fb83bca25f5a8fc"
OUTPUT = Path(__file__).with_name("query-selection-python-oracle.json")
METHODS = ("query.selectionOpen",)


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue = None
    failure: str | None = None


def field_fixture() -> JsonObject:
    return json.loads(r"""{
  "contract": "vibetable.schema.v2",
  "identity": {
    "fieldId": "fld_01JABCDE",
    "physicalName": "f_01jabcde",
    "providerFieldId": "pb_01JABCDE"
  },
  "displayName": "金额",
  "help": "",
  "logicalType": "number",
  "lifecycle": {
    "state": "active",
    "retiredAt": null
  },
  "value": {
    "required": false,
    "default": {
      "enabled": false,
      "value": null,
      "source": "recommended",
      "defaultsVersion": 1
    },
    "presence": {
      "mode": "companion",
      "providerFieldId": "pb_01JPRESEN",
      "physicalName": "__vt_has_f_01jabcde"
    }
  },
  "constraints": {
    "unique": {
      "enabled": false,
      "blankPolicy": "ignoreMissing"
    },
    "range": {
      "min": null,
      "max": null
    },
    "length": {
      "min": null,
      "max": null
    },
    "pattern": {
      "enabled": false,
      "value": ""
    },
    "domains": {
      "only": [],
      "except": []
    },
    "selection": {
      "min": 0,
      "max": null
    }
  },
  "storage": {
    "kind": "pocketbase-number",
    "options": {
      "onlyInt": false,
      "maxSize": 0,
      "convertURLs": false,
      "presentable": false
    }
  },
  "display": {
    "kind": "number",
    "preset": "number",
    "displayScale": 2,
    "scaleMode": "max",
    "trimTrailingZeros": true,
    "useGrouping": true,
    "currency": "CNY",
    "percentStorage": "ratio",
    "unit": null,
    "precision": "minute",
    "timezone": "system",
    "mode": "default",
    "indent": 0,
    "trueLabel": "是",
    "falseLabel": "否"
  }
}
""")


def capability_fixture() -> JsonObject:
    return json.loads(r"""{
  "logicalType": "number",
  "generalSettings": ["displayName", "help", "required", "default", "unique"],
  "advancedSettings": ["range", "onlyInt", "displayScale"],
  "dangerSettings": ["retire", "purge"],
  "recommended": {
    "defaultsVersion": 1,
    "value": {
      "required": false,
      "default": {
        "enabled": false,
        "value": null,
        "source": "recommended",
        "defaultsVersion": 1
      },
      "presence": {"mode": "companion"}
    },
    "constraints": {
      "unique": {"enabled": false, "blankPolicy": "ignoreMissing"},
      "range": {"min": null, "max": null},
      "length": {"min": null, "max": null},
      "pattern": {"enabled": false, "value": ""},
      "domains": {"only": [], "except": []},
      "selection": {"min": 0, "max": null}
    },
    "storage": {
      "kind": "pocketbase-number",
      "options": {
        "onlyInt": false,
        "maxSize": 0,
        "convertURLs": false,
        "presentable": false
      }
    },
    "display": {
      "kind": "number",
      "preset": "number",
      "displayScale": 2,
      "scaleMode": "max",
      "trimTrailingZeros": true,
      "useGrouping": true,
      "currency": "CNY",
      "percentStorage": "ratio",
      "unit": null,
      "precision": "minute",
      "timezone": "system",
      "mode": "default",
      "indent": 0,
      "trueLabel": "是",
      "falseLabel": "否"
    }
  },
  "supportsRequired": true,
  "supportsDefault": true,
  "supportsUnique": true,
  "needsPresence": true,
  "displayPresets": ["number", "integer", "currency", "percent", "unit"],
  "conversionTargets": ["text"],
  "conversionRules": ["round", "floor", "ceil", "truncate", "block"],
  "compileStrategy": "pocketbase-number",
  "userCreatable": true,
  "filterOperators": ["eq", "ne", "isEmpty", "isNotEmpty", "gt", "gte", "lt", "lte"],
  "groupable": true,
  "summaryOperations": ["count", "countDistinct", "sum", "avg", "min", "max"],
  "relationCardinalities": [],
  "relationDeletePolicies": [],
  "lookupMaxDepth": 0,
  "formulaResultTypeInferred": false,
  "formulaRelationAggregates": []
}
""")


def cases() -> tuple[Case, ...]:
    method = METHODS[0]
    query: JsonObject = {"keyword": None, "filters": [], "sorts": [], "offset": 0, "limit": 1}
    params: JsonObject = {"tableId": "orders", "query": query}
    schema: JsonObject = {
        "contract": "vibetable.schema.v2",
        "tableId": "orders",
        "displayName": "订单",
        "kind": "base",
        "schemaRevision": "schema_1",
        "dataRevision": 0,
        "archivePolicy": {"mode": "none", "fieldId": None, "archivedValue": None},
        "fields": [field_fixture()],
        "capabilities": [capability_fixture()],
    }
    snapshot: JsonObject = {
        "snapshotId": "0123456789abcdef0123456789abcdef",
        "digest": "d" * 64,
        "databaseId": "workspace-oracle",
        "table": "orders",
        "schemaRevision": "schema_1",
        "dataRevision": 0,
        "normalizedQuery": query,
    }
    rows: list[JsonValue] = [
        {
            "id": "row-1",
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
    window: JsonObject = {
        "rows": rows,
        "nextCursor": "opaque-cursor-1",
        "hasMore": True,
        "filteredRows": 2,
        "totalRows": 2,
        "querySnapshot": snapshot,
    }
    response: JsonObject = {"schemaSnapshot": schema, "cursorWindow": window}

    def altered_schema(name: str, changes: JsonObject) -> Case:
        return Case(name, method, params, {**response, "schemaSnapshot": {**schema, **changes}})

    def altered_window(name: str, changes: JsonObject) -> Case:
        return Case(name, method, params, {**response, "cursorWindow": {**window, **changes}})

    return (
        Case("complete-schema-defaults-unicode-falsy", method, params, response),
        altered_window(
            "terminal-window",
            {
                "nextCursor": None,
                "hasMore": False,
                "filteredRows": 1,
                "totalRows": 1,
            },
        ),
        altered_window(
            "empty-records",
            {
                "rows": [],
                "nextCursor": None,
                "hasMore": False,
                "filteredRows": 0,
                "totalRows": 0,
            },
        ),
        Case(
            "empty-query-forwarded",
            method,
            {**params, "query": {}},
            {
                **response,
                "cursorWindow": {
                    **window,
                    "nextCursor": None,
                    "hasMore": False,
                    "filteredRows": 1,
                    "totalRows": 1,
                    "querySnapshot": {**snapshot, "normalizedQuery": {**query, "limit": 100}},
                },
            },
        ),
        Case("missing-query", method, {"tableId": "orders"}),
        Case("null-query", method, {**params, "query": None}),
        Case("array-query", method, {**params, "query": []}),
        Case("unknown-param", method, {**params, "extra": 0}),
        Case("empty-table", method, {**params, "tableId": ""}),
        Case("null-table", method, {**params, "tableId": None}),
        Case("product-error", method, params, failure="product"),
        Case("transport-error", method, params, failure="transport"),
        Case("missing-schema", method, params, {"cursorWindow": window}),
        altered_schema("schema-table-mismatch", {"tableId": "another"}),
        altered_window(
            "cursor-table-mismatch", {"querySnapshot": {**snapshot, "table": "another"}}
        ),
        altered_schema("schema-revision-mismatch", {"schemaRevision": "schema_2"}),
        altered_schema("data-revision-mismatch", {"dataRevision": 1}),
        altered_schema("boolean-data-revision", {"dataRevision": False}),
        altered_schema("incomplete-schema", {"fields": [{}]}),
        altered_schema("unknown-schema-field", {"extra": None}),
        altered_window(
            "incomplete-query-snapshot",
            {
                "querySnapshot": {
                    "table": "orders",
                    "schemaRevision": "schema_1",
                    "dataRevision": 0,
                },
            },
        ),
        altered_window("unknown-query-snapshot-field", {"querySnapshot": {**snapshot, "extra": 0}}),
        altered_window("more-without-cursor", {"nextCursor": None}),
        altered_window("terminal-with-cursor", {"hasMore": False}),
        altered_window("non-string-cursor", {"nextCursor": 0}),
        altered_window("non-boolean-has-more", {"hasMore": 1}),
        altered_window("malformed-row", {"rows": [None]}),
        altered_window("negative-row-count", {"totalRows": -1}),
    )


def validate_frozen_inputs() -> None:
    """Check historical inputs without calling the retired Python owner."""
    frozen: JsonObject = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen selection producer changed")
    entries = frozen.get("cases")
    expected_cases = cases()
    if not isinstance(entries, list) or len(entries) != len(expected_cases):
        raise ValueError("Frozen selection case inventory changed")
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
            raise ValueError("Invalid frozen selection entry")
        actual = {key: entry.get(key) for key in expected}
        if render(actual) != render(expected):
            raise ValueError(f"Frozen selection inputs changed: {case.name}")


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
