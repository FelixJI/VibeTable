"""Small, closed JSON Schema subset used at plugin trust boundaries."""

from __future__ import annotations

import json
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


def validate_plugin_json(value: Any, schema: dict[str, Any], *, label: str) -> None:
    validate_plugin_schema_document(schema, label=label)
    try:
        encoded = json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    except (TypeError, ValueError) as exc:
        raise PluginSchemaError(f"{label} is not JSON serializable") from exc
    if len(encoded) > 1024 * 1024:
        raise PluginSchemaError(f"{label} exceeds the 1 MiB payload limit")
    _validate(value, schema, path="$", depth=0, label=label)


def _validate(value: Any, schema: dict[str, Any], *, path: str, depth: int, label: str) -> None:
    if depth > 32:
        raise PluginSchemaError(f"{label} exceeds the maximum nesting depth at {path}")
    if "enum" in schema and value not in schema["enum"]:
        raise PluginSchemaError(f"{label} does not match enum at {path}")
    declared = schema.get("type")
    allowed = [declared] if isinstance(declared, str) else declared
    if (
        isinstance(allowed, list)
        and allowed
        and not any(_matches_type(value, item) for item in allowed)
    ):
        raise PluginSchemaError(f"{label} has the wrong type at {path}")
    if isinstance(value, dict):
        required = schema.get("required", [])
        if isinstance(required, list):
            missing = [key for key in required if key not in value]
            if missing:
                raise PluginSchemaError(f"{label} is missing {missing[0]!r} at {path}")
        properties = schema.get("properties", {})
        if not isinstance(properties, dict):
            properties = {}
        if schema.get("additionalProperties") is False:
            extras = set(value) - set(properties)
            if extras:
                raise PluginSchemaError(
                    f"{label} contains unsupported property {sorted(extras)[0]!r} at {path}"
                )
        for key, child in value.items():
            child_schema = properties.get(key)
            if isinstance(child_schema, dict):
                _validate(
                    child,
                    child_schema,
                    path=f"{path}.{key}",
                    depth=depth + 1,
                    label=label,
                )
    elif isinstance(value, list):
        if isinstance(schema.get("minItems"), int) and len(value) < schema["minItems"]:
            raise PluginSchemaError(f"{label} has too few items at {path}")
        if isinstance(schema.get("maxItems"), int) and len(value) > schema["maxItems"]:
            raise PluginSchemaError(f"{label} has too many items at {path}")
        item_schema = schema.get("items")
        if isinstance(item_schema, dict):
            for index, child in enumerate(value):
                _validate(
                    child,
                    item_schema,
                    path=f"{path}[{index}]",
                    depth=depth + 1,
                    label=label,
                )
    elif isinstance(value, str):
        if isinstance(schema.get("minLength"), int) and len(value) < schema["minLength"]:
            raise PluginSchemaError(f"{label} is too short at {path}")
        if isinstance(schema.get("maxLength"), int) and len(value) > schema["maxLength"]:
            raise PluginSchemaError(f"{label} is too long at {path}")
        pattern = schema.get("pattern")
        if isinstance(pattern, str) and re.search(pattern, value) is None:
            raise PluginSchemaError(f"{label} does not match the pattern at {path}")
    elif isinstance(value, (int, float)) and not isinstance(value, bool):
        if isinstance(schema.get("minimum"), (int, float)) and value < schema["minimum"]:
            raise PluginSchemaError(f"{label} is below minimum at {path}")
        if isinstance(schema.get("maximum"), (int, float)) and value > schema["maximum"]:
            raise PluginSchemaError(f"{label} is above maximum at {path}")


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


def _matches_type(value: Any, declared: Any) -> bool:
    return {
        "null": value is None,
        "boolean": isinstance(value, bool),
        "integer": isinstance(value, int) and not isinstance(value, bool),
        "number": isinstance(value, (int, float)) and not isinstance(value, bool),
        "string": isinstance(value, str),
        "array": isinstance(value, list),
        "object": isinstance(value, dict),
    }.get(declared, False)


__all__ = [
    "PluginSchemaError",
    "validate_plugin_json",
    "validate_plugin_schema_document",
]
