"""Retain original field settings inputs after retiring their Python execution path."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "2c211088a682163bfdd126eda4528b69cd8419f8"
METHOD = "field.settings.describe"
OUTPUT = Path(__file__).with_name("field-settings-describe-python-oracle.json")


@dataclass(frozen=True)
class Case:
    name: str
    params: JsonValue
    response: JsonValue
    failure: str | None = None
    typed_boundary: str | None = None


def settings_result(field_id: str = "") -> JsonObject:
    return {
        "contract": "vibetable.schema.v2",
        "tableId": "orders",
        "fieldId": field_id,
        "schemaRevision": "schema-1",
        "dataRevision": 7,
        "definition": None,
        "capabilities": [],
        "recommendedDefaultsVersion": 1,
    }


def cases() -> tuple[Case, ...]:
    result = settings_result()
    return (
        Case("table-defaults", {"tableId": "orders"}, result),
        Case("selected-field", {"tableId": "orders", "fieldId": "title"}, settings_result("title")),
        Case(
            "field-whitespace-preserved",
            {"tableId": "orders", "fieldId": " \t "},
            settings_result(" \t "),
        ),
        Case(
            "field-unicode-path-preserved",
            {"tableId": "orders", "fieldId": "../Cafe\u0301/中文 👩🏽‍💻?x=1"},
            settings_result("../Cafe\u0301/中文 👩🏽‍💻?x=1"),
        ),
        Case("table-128-characters", {"tableId": "a" * 128}, {**result, "tableId": "a" * 128}),
        Case("table-ascii-hyphen-underscore", {"tableId": "A_1-z"}, {**result, "tableId": "A_1-z"}),
        Case("missing-table", {}, result),
        Case("empty-table", {"tableId": ""}, result),
        Case("null-table", {"tableId": None}, result),
        Case("nonobject-params", [], result),
        Case("empty-field", {"tableId": "orders", "fieldId": ""}, result),
        Case("null-field", {"tableId": "orders", "fieldId": None}, result),
        Case("numeric-field", {"tableId": "orders", "fieldId": 7}, result),
        Case("unknown-param", {"tableId": "orders", "extra": False}, result),
        Case("table-whitespace", {"tableId": " orders "}, result),
        Case("table-unicode", {"tableId": "订单"}, result),
        Case("table-path-separator", {"tableId": "../orders"}, result),
        Case("table-percent-escape", {"tableId": "orders%2Ftitle"}, result),
        Case("table-leading-underscore", {"tableId": "_orders"}, result),
        Case("table-129-characters", {"tableId": "a" * 129}, result),
        Case(
            "field-type-before-table-path",
            {"tableId": "../orders", "fieldId": None},
            result,
            "domain",
        ),
        Case(
            "table-path-before-authority-error",
            {"tableId": "../orders", "fieldId": "title"},
            result,
            "domain",
        ),
        Case(
            "empty-response-object",
            {"tableId": "orders"},
            {},
            typed_boundary="FieldSettingsDescribeResult always emits its eight fields; an empty object is not representable",
        ),
        Case(
            "dynamic-response-pass-through",
            {"tableId": "orders", "fieldId": "title"},
            {
                **settings_result("title"),
                "definition": {"custom": [None, False, 0, "", "Cafe\u0301 👩🏽‍💻"]},
                "extension": {"nested": [1, "1"]},
            },
            typed_boundary="Typed FieldDefinition and fixed result cannot preserve arbitrary definition/extra root members; dynamic JSON itself is not a blanket exemption",
        ),
        Case(
            "wrong-shaped-response-object",
            {"tableId": "orders"},
            {"contract": False, "dataRevision": "seven", "capabilities": {"custom": True}},
            typed_boundary="Typed result cannot express wrong scalar/container types and omitted required wire fields; original Python passes the object through",
        ),
        Case(
            "null-response",
            {"tableId": "orders"},
            None,
            typed_boundary="Typed response struct cannot be a null root",
        ),
        Case(
            "array-response",
            {"tableId": "orders"},
            [],
            typed_boundary="Typed response struct cannot be an array root",
        ),
        Case("public-domain-error", {"tableId": "orders", "fieldId": "missing"}, result, "domain"),
        Case(
            "transport-error",
            {"tableId": "orders"},
            result,
            "transport",
            "Direct in-process service has no Python HTTP transport outage; public domain errors remain replayable",
        ),
        populated_settings_case(),
    )


def populated_settings_case() -> Case:
    """Independent valid input shaped after the schema-v2 fixtures, never an expected response."""
    definition: JsonObject = {
        "contract": "vibetable.schema.v2",
        "identity": {
            "fieldId": "fld_01JABCDE",
            "physicalName": "f_01jabcde",
            "providerFieldId": "pb_01JABCDE",
        },
        "displayName": "金额 Café 👩🏽\u200d💻",
        "help": "保留空值、默认值与精度；不改写 Unicode。",
        "logicalType": "number",
        "lifecycle": {"state": "active", "retiredAt": None},
        "value": {
            "required": False,
            "default": {
                "enabled": False,
                "value": None,
                "source": "recommended",
                "defaultsVersion": 1,
            },
            "presence": {
                "mode": "companion",
                "providerFieldId": "pb_01JPRESEN",
                "physicalName": "__vt_has_f_01jabcde",
            },
        },
        "constraints": {
            "unique": {"enabled": False, "blankPolicy": "ignoreMissing"},
            "range": {"min": None, "max": None},
            "length": {"min": None, "max": None},
            "pattern": {"enabled": False, "value": ""},
            "domains": {"only": [], "except": []},
            "selection": {"min": 0, "max": None},
        },
        "storage": {
            "kind": "pocketbase-number",
            "options": {"onlyInt": False, "maxSize": 0, "convertURLs": False, "presentable": False},
        },
        "display": {
            "kind": "number",
            "preset": "number",
            "displayScale": 2,
            "scaleMode": "max",
            "trimTrailingZeros": True,
            "useGrouping": True,
            "currency": "CNY",
            "percentStorage": "ratio",
            "unit": None,
            "precision": "minute",
            "timezone": "system",
            "mode": "default",
            "indent": 0,
            "trueLabel": "是",
            "falseLabel": "否",
        },
    }
    capability: JsonObject = {
        "logicalType": "number",
        "generalSettings": ["displayName", "help", "required", "default", "unique"],
        "advancedSettings": ["range", "onlyInt", "displayScale"],
        "dangerSettings": ["retire", "purge"],
        "recommended": {
            "defaultsVersion": 1,
            "value": {
                "required": False,
                "default": {
                    "enabled": False,
                    "value": None,
                    "source": "recommended",
                    "defaultsVersion": 1,
                },
                "presence": {"mode": "companion"},
            },
            "constraints": {
                "unique": {"enabled": False, "blankPolicy": "ignoreMissing"},
                "range": {"min": None, "max": None},
                "length": {"min": None, "max": None},
                "pattern": {"enabled": False, "value": ""},
                "domains": {"only": [], "except": []},
                "selection": {"min": 0, "max": None},
            },
            "storage": {
                "kind": "pocketbase-number",
                "options": {
                    "onlyInt": False,
                    "maxSize": 0,
                    "convertURLs": False,
                    "presentable": False,
                },
            },
            "display": {
                "kind": "number",
                "preset": "number",
                "displayScale": 2,
                "scaleMode": "max",
                "trimTrailingZeros": True,
                "useGrouping": True,
                "currency": "CNY",
                "percentStorage": "ratio",
                "unit": None,
                "precision": "minute",
                "timezone": "system",
                "mode": "default",
                "indent": 0,
                "trueLabel": "是",
                "falseLabel": "否",
            },
        },
        "supportsRequired": True,
        "supportsDefault": True,
        "supportsUnique": True,
        "needsPresence": True,
        "displayPresets": ["number", "integer", "currency", "percent", "unit"],
        "conversionTargets": ["text"],
        "conversionRules": ["round", "floor", "ceil", "truncate", "block"],
        "compileStrategy": "pocketbase-number",
        "userCreatable": True,
        "filterOperators": ["eq", "ne", "isEmpty", "isNotEmpty", "gt", "gte", "lt", "lte"],
        "groupable": True,
        "summaryOperations": ["count", "countDistinct", "sum", "avg", "min", "max"],
        "relationCardinalities": [],
        "relationDeletePolicies": [],
        "lookupMaxDepth": 0,
        "formulaResultTypeInferred": False,
        "formulaRelationAggregates": [],
    }
    return Case(
        "populated-valid-definition-and-capability",
        {"tableId": "orders", "fieldId": "fld_01JABCDE"},
        {**settings_result("fld_01JABCDE"), "definition": definition, "capabilities": [capability]},
    )


def validate_frozen_inputs() -> None:
    frozen = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen field settings producer changed")
    if (
        frozen.get("boundary")
        != "Original Workspace catalog method through Python Product DTO/adapter; scripted authority HTTP"
    ):
        raise ValueError("Frozen field settings boundary changed")
    entries = frozen.get("cases")
    expected_cases = cases()
    if not isinstance(entries, list) or len(entries) != len(expected_cases):
        raise ValueError("Frozen field settings case inventory changed")
    for entry, case in zip(entries, expected_cases, strict=True):
        expected: JsonObject = {
            "name": case.name,
            "request": {"jsonrpc": "2.0", "id": case.name, "method": METHOD, "params": case.params},
            "authorityFixture": {"response": case.response, "failure": case.failure},
            "typedGoBoundary": case.typed_boundary,
        }
        if not isinstance(entry, dict) or any(
            entry.get(key) != value for key, value in expected.items()
        ):
            raise ValueError(f"Frozen field settings inputs changed: {case.name}")


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
