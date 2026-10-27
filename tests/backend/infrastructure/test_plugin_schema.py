from __future__ import annotations

import pytest

from backend.infrastructure.plugin_schema import (
    PluginSchemaError,
    validate_plugin_schema_document,
)


def test_schema_error_exposes_stable_rpc_metadata() -> None:
    error = PluginSchemaError("bad")
    assert error.rpc_error_data == {
        "code": "plugin_schema_invalid",
        "recoverability": "reconfigure",
    }


def test_valid_nested_schema_covers_supported_keywords() -> None:
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
