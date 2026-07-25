from __future__ import annotations

import pytest

from backend.infrastructure import plugin_schema as subject
from backend.infrastructure.plugin_schema import (
    PluginSchemaError,
    _matches_type,
    validate_plugin_json,
    validate_plugin_schema_document,
)


def test_schema_error_exposes_stable_rpc_metadata() -> None:
    error = PluginSchemaError("bad")
    assert error.rpc_error_data == {
        "code": "plugin_schema_invalid",
        "recoverability": "reconfigure",
    }


def test_valid_nested_schema_and_json_cover_all_constraints() -> None:
    schema = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "title": "Config",
        "description": "closed",
        "type": "object",
        "required": ["name", "items"],
        "additionalProperties": False,
        "properties": {
            "name": {
                "type": "string",
                "minLength": 2,
                "maxLength": 8,
                "pattern": "^[a-z]+$",
                "default": "ok",
                "examples": ["ok"],
            },
            "items": {
                "type": "array",
                "minItems": 1,
                "maxItems": 2,
                "items": {
                    "type": ["integer", "null"],
                    "minimum": 1,
                    "maximum": 9,
                    "enum": [1, 2, None],
                },
            },
            "enabled": {"type": "boolean"},
        },
    }
    validate_plugin_schema_document(schema, label="settings")
    validate_plugin_json(
        {"name": "alpha", "items": [1, None], "enabled": True},
        schema,
        label="settings",
    )


@pytest.mark.parametrize(
    ("schema", "message"),
    [
        ({"$ref": "#"}, "unsupported keyword"),
        ({"type": "wat"}, "invalid type"),
        ({"type": []}, "invalid type"),
        ({"type": [1]}, "invalid type"),
        ({"required": "x"}, "invalid required"),
        ({"required": ["x", "x"]}, "invalid required"),
        ({"required": [""]}, "invalid required"),
        ({"properties": []}, "invalid properties"),
        ({"properties": {"x": "bad"}}, "invalid properties"),
        ({"items": []}, "invalid items"),
        ({"additionalProperties": {}}, "boolean additionalProperties"),
        ({"enum": []}, "invalid enum"),
        ({"minItems": -1}, "invalid minItems"),
        ({"maxItems": True}, "invalid maxItems"),
        ({"minLength": 1.5}, "invalid minLength"),
        ({"maxLength": -2}, "invalid maxLength"),
        ({"minimum": True}, "invalid minimum"),
        ({"maximum": "9"}, "invalid maximum"),
        ({"pattern": 1}, "invalid pattern"),
        ({"pattern": "["}, "invalid pattern"),
    ],
)
def test_schema_document_rejects_malformed_keywords(schema: dict, message: str) -> None:
    with pytest.raises(PluginSchemaError, match=message):
        validate_plugin_schema_document(schema, label="config")


def test_schema_document_rejects_excessive_depth() -> None:
    schema: dict = {"type": "string"}
    for _ in range(34):
        schema = {"type": "array", "items": schema}
    with pytest.raises(PluginSchemaError, match="nesting depth"):
        validate_plugin_schema_document(schema, label="deep")


@pytest.mark.parametrize(
    ("value", "schema", "message"),
    [
        ("x", {"enum": ["y"]}, "does not match enum"),
        (True, {"type": "integer"}, "wrong type"),
        (1, {"type": "boolean"}, "wrong type"),
        ({}, {"type": "array"}, "wrong type"),
        ({}, {"required": ["id"]}, "missing 'id'"),
        ({"x": 1}, {"properties": {}, "additionalProperties": False}, "unsupported property"),
        ([], {"minItems": 1}, "too few items"),
        ([1, 2], {"maxItems": 1}, "too many items"),
        ("a", {"minLength": 2}, "too short"),
        ("abc", {"maxLength": 2}, "too long"),
        ("abc", {"pattern": "^z"}, "pattern"),
        (0, {"minimum": 1}, "below minimum"),
        (10, {"maximum": 9}, "above maximum"),
    ],
)
def test_json_validation_rejects_constraint_violations(
    value: object, schema: dict, message: str
) -> None:
    with pytest.raises(PluginSchemaError, match=message):
        validate_plugin_json(value, schema, label="payload")


@pytest.mark.parametrize("value", [{1, 2}, object()])
def test_json_validation_rejects_non_wire_values(value: object) -> None:
    with pytest.raises(PluginSchemaError, match="not JSON serializable"):
        validate_plugin_json(value, {}, label="payload")


def test_json_validation_rejects_payload_over_one_mib() -> None:
    with pytest.raises(PluginSchemaError, match="1 MiB"):
        validate_plugin_json("x" * (1024 * 1024 + 1), {}, label="payload")


def test_json_validation_rejects_excessive_value_depth() -> None:
    schema: dict = {}
    value: object = "leaf"
    for _ in range(34):
        schema = {"type": "array", "items": schema}
        value = [value]
    with pytest.raises(PluginSchemaError, match="nesting depth"):
        validate_plugin_json(value, schema, label="payload")


@pytest.mark.parametrize(
    ("value", "declared", "expected"),
    [
        (None, "null", True),
        (True, "boolean", True),
        (True, "integer", False),
        (1, "integer", True),
        (1, "number", True),
        (1.5, "number", True),
        ("x", "string", True),
        ([], "array", True),
        ({}, "object", True),
        ("x", "future", False),
    ],
)
def test_matches_type_is_closed(value: object, declared: str, expected: bool) -> None:
    assert _matches_type(value, declared) is expected


def test_unconstrained_properties_and_items_are_tolerated() -> None:
    subject._validate(
        {"free": {"nested": object()}},
        {"type": "object", "properties": []},
        path="$",
        depth=0,
        label="payload",
    )
    subject._validate(
        [object()],
        {"type": "array", "items": []},
        path="$",
        depth=0,
        label="payload",
    )
