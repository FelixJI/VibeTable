"""Map Directus fields and current-user permissions to VibeTable read schema."""

from __future__ import annotations

import hashlib
import json
from collections.abc import Mapping, Sequence
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict

from backend.adapters.directus.errors import DirectusSchemaError
from backend.contracts.table import ColumnSchema

_INTEGER_TYPES = {"integer", "bigInteger"}
_DECIMAL_TYPES = {"float", "decimal"}
_BOOLEAN_TYPES = {"boolean"}
_DATE_TYPES = {"date", "dateTime", "timestamp", "time"}


class DirectusSchemaPlan(BaseModel):
    """Permission-pruned, read-only schema for a Directus collection."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    collection: str
    primary_key: str
    columns: list[ColumnSchema]
    visible_fields: list[str]
    schema_revision: str


def build_directus_schema(
    *,
    collection: str,
    fields: Sequence[Mapping[str, Any]],
    collection_permissions: Mapping[str, Any],
) -> DirectusSchemaPlan:
    """Build the Phase-0 read schema from Directus metadata.

    The adapter intentionally emits read-only columns.  Write permissions are
    ignored until the revision-aware mutation endpoint from the migration spec
    exists.
    """

    read_permission = collection_permissions.get("read")
    if not isinstance(read_permission, Mapping) or read_permission.get("access") == "none":
        raise DirectusSchemaError(f"collection {collection!r} is not readable")

    allowed = _permission_fields(read_permission.get("fields"))
    normalized: list[dict[str, Any]] = []
    primary_keys: list[str] = []
    for raw in fields:
        name = raw.get("field")
        if not isinstance(name, str) or not name:
            raise DirectusSchemaError("Directus field metadata is missing 'field'")
        if allowed is not None and name not in allowed:
            continue

        schema_value = raw.get("schema")
        meta_value = raw.get("meta")
        schema: Mapping[str, Any] = schema_value if isinstance(schema_value, Mapping) else {}
        meta: Mapping[str, Any] = meta_value if isinstance(meta_value, Mapping) else {}
        is_primary_key = schema.get("is_primary_key") is True
        if is_primary_key:
            primary_keys.append(name)
        nullable = _is_nullable(schema=schema, meta=meta)
        data_type = _map_data_type(str(raw.get("type") or schema.get("data_type") or "string"))
        sort_value = meta.get("sort")
        normalized.append(
            {
                "name": name,
                "title": _field_title(name, meta),
                "data_type": data_type,
                "nullable": nullable,
                "primary_key": is_primary_key,
                "readonly": bool(meta.get("readonly")) or bool(schema.get("is_generated")),
                "sort": sort_value if isinstance(sort_value, int) else 2**31,
            }
        )

    if len(primary_keys) != 1:
        raise DirectusSchemaError(
            f"collection {collection!r} must expose exactly one primary key; "
            f"found {len(primary_keys)}"
        )

    normalized.sort(key=lambda item: (item["sort"], item["name"]))
    columns = [
        ColumnSchema(
            name=item["name"],
            title=item["title"],
            data_type=item["data_type"],
            editable=False,
            nullable=item["nullable"],
        )
        for item in normalized
    ]
    revision_payload = {
        "contract": "directus-read-schema-v1",
        "collection": collection,
        "fields": [
            {key: item[key] for key in sorted(item) if key != "sort"}
            for item in sorted(normalized, key=lambda item: item["name"])
        ],
    }
    encoded = json.dumps(
        revision_payload,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return DirectusSchemaPlan(
        collection=collection,
        primary_key=primary_keys[0],
        columns=columns,
        visible_fields=[item["name"] for item in normalized],
        schema_revision=hashlib.sha256(encoded).hexdigest(),
    )


def _permission_fields(value: Any) -> set[str] | None:
    """Return ``None`` for wildcard access, otherwise the allowed field set."""
    if value is None:
        return None
    if not isinstance(value, list):
        raise DirectusSchemaError("read permission fields must be a list or null")
    if "*" in value:
        return None
    return {item for item in value if isinstance(item, str)}


def _is_nullable(*, schema: Mapping[str, Any], meta: Mapping[str, Any]) -> bool:
    if meta.get("required") is True:
        return False
    nullable = schema.get("is_nullable")
    return bool(nullable) if isinstance(nullable, bool) else True


def _map_data_type(
    directus_type: str,
) -> Literal["text", "integer", "decimal", "boolean", "date"]:
    if directus_type in _INTEGER_TYPES:
        return "integer"
    if directus_type in _DECIMAL_TYPES:
        return "decimal"
    if directus_type in _BOOLEAN_TYPES:
        return "boolean"
    if directus_type in _DATE_TYPES:
        return "date"
    return "text"


def _field_title(name: str, meta: Mapping[str, Any]) -> str:
    translations = meta.get("translations")
    if isinstance(translations, list):
        for item in translations:
            if not isinstance(item, Mapping):
                continue
            translated = item.get("translation")
            if isinstance(translated, str) and translated.strip():
                return translated.strip()
    note = meta.get("note")
    if isinstance(note, str) and note.strip():
        return note.strip()
    return name
