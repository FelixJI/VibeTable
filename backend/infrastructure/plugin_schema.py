"""Small, closed JSON Schema subset used at plugin trust boundaries."""

from __future__ import annotations

import re
from typing import Any


class PluginSchemaError(ValueError):
    @property
    def rpc_error_data(self) -> dict[str, str]:
        return {"code": "plugin_schema_invalid", "recoverability": "reconfigure"}


_ALLOWED_SCHEMA_KEYWORDS = {
    "$schema",
    "additionalProperties",
    "default",
    "description",
    "enum",
    "examples",
    "items",
    "maxItems",
    "maxLength",
    "maximum",
    "minItems",
    "minLength",
    "minimum",
    "pattern",
    "properties",
    "required",
    "title",
    "type",
}
_JSON_TYPES = {"array", "boolean", "integer", "null", "number", "object", "string"}


def validate_plugin_schema_document(schema: dict[str, Any], *, label: str) -> None:
    """Validate the deliberately small, non-recursive-reference schema dialect."""

    _validate_schema_node(schema, path="$", depth=0, label=label)


def _validate_schema_node(schema: dict[str, Any], *, path: str, depth: int, label: str) -> None:
    if depth > 32:
        raise PluginSchemaError(f"{label} schema exceeds the maximum nesting depth at {path}")
    unknown = set(schema) - _ALLOWED_SCHEMA_KEYWORDS
    if unknown:
        raise PluginSchemaError(
            f"{label} schema contains unsupported keyword {sorted(unknown)[0]!r} at {path}"
        )
    declared = schema.get("type")
    declared_types = [declared] if isinstance(declared, str) else declared
    if declared_types is not None and (
        not isinstance(declared_types, list)
        or not declared_types
        or not all(isinstance(item, str) and item in _JSON_TYPES for item in declared_types)
    ):
        raise PluginSchemaError(f"{label} schema has an invalid type at {path}")
    required = schema.get("required")
    if required is not None and (
        not isinstance(required, list)
        or len(required) != len(set(required))
        or not all(isinstance(item, str) and item for item in required)
    ):
        raise PluginSchemaError(f"{label} schema has invalid required fields at {path}")
    properties = schema.get("properties")
    if properties is not None:
        if not isinstance(properties, dict) or not all(
            isinstance(key, str) and isinstance(value, dict) for key, value in properties.items()
        ):
            raise PluginSchemaError(f"{label} schema has invalid properties at {path}")
        for key, child in properties.items():
            _validate_schema_node(
                child,
                path=f"{path}.properties.{key}",
                depth=depth + 1,
                label=label,
            )
    items = schema.get("items")
    if items is not None:
        if not isinstance(items, dict):
            raise PluginSchemaError(f"{label} schema has invalid items at {path}")
        _validate_schema_node(items, path=f"{path}.items", depth=depth + 1, label=label)
    additional = schema.get("additionalProperties")
    if additional is not None and not isinstance(additional, bool):
        raise PluginSchemaError(
            f"{label} schema only supports boolean additionalProperties at {path}"
        )
    enum = schema.get("enum")
    if enum is not None and (not isinstance(enum, list) or not enum):
        raise PluginSchemaError(f"{label} schema has an invalid enum at {path}")
    for keyword in ("minItems", "maxItems", "minLength", "maxLength"):
        value = schema.get(keyword)
        if value is not None and (
            not isinstance(value, int) or isinstance(value, bool) or value < 0
        ):
            raise PluginSchemaError(f"{label} schema has invalid {keyword} at {path}")
    for keyword in ("minimum", "maximum"):
        value = schema.get(keyword)
        if value is not None and (not isinstance(value, (int, float)) or isinstance(value, bool)):
            raise PluginSchemaError(f"{label} schema has invalid {keyword} at {path}")
    pattern = schema.get("pattern")
    if pattern is not None:
        if not isinstance(pattern, str):
            raise PluginSchemaError(f"{label} schema has invalid pattern at {path}")
        try:
            re.compile(pattern)
        except re.error as exc:
            raise PluginSchemaError(f"{label} schema has invalid pattern at {path}") from exc


__all__ = [
    "PluginSchemaError",
    "validate_plugin_schema_document",
]
