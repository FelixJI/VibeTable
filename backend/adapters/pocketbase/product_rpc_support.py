"""Shared internal seams for PocketBase product RPC modules."""

from __future__ import annotations

import hashlib
import json
import math
import re
from collections.abc import Awaitable, Callable, Mapping
from dataclasses import dataclass
from typing import Protocol

from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseTransport
from backend.contracts.product_rpc import JsonObject, JsonValue, ProductParams

_PATH_SEGMENT = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$")

ProductRpcHandler = Callable[[ProductParams], Awaitable[JsonObject]]


class ProductRpcModule(Protocol):
    """One cohesive family behind the root adapter's closed invoke seam."""

    methods: frozenset[str]

    async def invoke(self, method: str, params: ProductParams) -> JsonObject: ...


@dataclass(frozen=True, slots=True)
class PocketBaseProductContext:
    """Authenticated sidecar dependencies shared by product RPC modules."""

    client: PocketBaseClient
    transport: PocketBaseTransport
    headers: Mapping[str, str]

    async def post(self, path: str, body: JsonObject) -> JsonObject:
        return _result_object(
            await self.transport.request(
                "POST",
                path,
                json_body=body,
                headers=dict(self.headers),
                expected_status=(200,),
            )
        )


def _result_object(value: object) -> JsonObject:
    if not isinstance(value, dict):
        raise ValueError("PocketBase returned an invalid product response")
    if not all(isinstance(key, str) for key in value):
        raise ValueError("PocketBase returned an invalid product response")
    return {str(key): _json_value(item) for key, item in value.items()}


def _json_value(value: object) -> JsonValue:
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError("PocketBase returned a non-finite JSON number")
        return value
    if value is None or isinstance(value, (str, bool, int)):
        return value
    if isinstance(value, list):
        return [_json_value(item) for item in value]
    if isinstance(value, dict):
        return _result_object(value)
    raise ValueError("PocketBase returned a non-JSON product value")


def _object(value: JsonObject, name: str) -> JsonObject:
    result = value.get(name)
    if not isinstance(result, dict):
        raise ValueError(f"{name} must be an object")
    return _result_object(result)


def _array(value: JsonObject, name: str) -> list[JsonValue]:
    result = value.get(name, [])
    if not isinstance(result, list):
        raise ValueError(f"{name} must be an array")
    return [_json_value(item) for item in result]


def _optional_string_list(value: JsonValue) -> list[str]:
    if value is None:
        return []
    if not isinstance(value, list) or not all(isinstance(item, str) and item for item in value):
        raise ValueError("PocketBase returned an invalid string capability list")
    return [item for item in value if isinstance(item, str)]


def _text(value: JsonObject, name: str) -> str:
    result = value.get(name)
    if not isinstance(result, str) or not result:
        raise ValueError(f"{name} must be a non-empty string")
    return result


def _text_any(value: JsonObject, *names: str) -> str:
    for name in names:
        result = value.get(name)
        if isinstance(result, str) and result:
            return result
    raise ValueError(f"{'/'.join(names)} must be a non-empty string")


def _optional_text(value: JsonObject, name: str) -> str:
    result = value.get(name, "")
    if not isinstance(result, str):
        raise ValueError(f"{name} must be a string")
    return result


def _integer(value: JsonObject, name: str, default: int | None = None) -> int:
    result = value.get(name, default)
    if isinstance(result, bool) or not isinstance(result, int):
        raise ValueError(f"{name} must be an integer")
    return result


def _path_segment(value: str) -> str:
    if not _PATH_SEGMENT.fullmatch(value):
        raise ValueError("table id is invalid")
    return value


def _stable_hash(value: JsonValue) -> str:
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode()
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def _lookup_revision(schema_revision: str, lookups: list[JsonValue]) -> str:
    return _stable_hash({"schemaRevision": schema_revision, "lookups": lookups})


def _renderer_data_type(value: str) -> str:
    result = {
        "text": "text",
        "shortText": "text",
        "longText": "text",
        "richText": "text",
        "editor": "text",
        "email": "text",
        "url": "text",
        "uuid": "text",
        "select": "text",
        "multiSelect": "text",
        "list": "text",
        "hash": "text",
        "secret": "text",
        "integer": "integer",
        "number": "decimal",
        "float": "decimal",
        "decimal": "decimal",
        "boolean": "boolean",
        "bool": "boolean",
        "date": "date",
        "dateTime": "datetime",
        "datetime": "datetime",
        "autoDate": "datetime",
        "autodate": "datetime",
        "time": "time",
        "json": "json",
        "geoPoint": "json",
        "geoJson": "json",
        "relation": "text",
        "file": "text",
        "formula": "text",
        "lookup": "text",
    }.get(value)
    if result is None:
        raise ValueError("PocketBase returned an unknown data type")
    return result


__all__ = ["PocketBaseProductContext", "ProductRpcHandler", "ProductRpcModule"]
