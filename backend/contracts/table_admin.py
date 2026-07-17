from __future__ import annotations

import re
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator
from pydantic.alias_generators import to_camel


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    model_config = _camel_config()


FieldType = Literal["string", "integer", "decimal", "date", "boolean", "text"]

#: A Directus/SQL identifier: ASCII letter first, then 0-63 letters/digits/
#: underscores (total length 1-64, matching the Field min/max_length). Mirrors
#: ``backend.adapters.directus.profile._IDENTIFIER`` (CollectionProfile enforces
#: the same rule when the runtime profile is built), but is duplicated here so
#: the contract layer rejects bad names at the RPC boundary (-32602 Invalid
#: params) instead of letting them reach the service and surface as an opaque
#: "Internal error" from the dispatcher's untyped-exception fallback. Keep the
#: two definitions in sync if either changes.
_IDENTIFIER = re.compile(r"^[A-Za-z][A-Za-z0-9_]{0,63}$")

_IDENTIFIER_HINT = (
    "must start with an ASCII letter and contain only letters, digits, or underscores"
)


class FieldDefinition(CamelModel):
    """A single field in a new table."""

    key: str = Field(min_length=1, max_length=64, description="Field name (column key).")
    type: FieldType = Field(description="Field data type.")

    @field_validator("key")
    @classmethod
    def _validate_key_identifier(cls, value: str) -> str:
        if not _IDENTIFIER.fullmatch(value):
            raise ValueError(f"field name {value!r} {_IDENTIFIER_HINT}")
        return value


class CreateTableParams(CamelModel):
    """Parameters for creating a new table.

    Wire form::

        {"name": "customers", "fields": [{"key": "name", "type": "string"}]}
    """

    name: str = Field(min_length=1, max_length=64, description="Collection name.")
    fields: list[FieldDefinition] = Field(min_length=0, max_length=64)

    @field_validator("name")
    @classmethod
    def _validate_name_identifier(cls, value: str) -> str:
        if not _IDENTIFIER.fullmatch(value):
            raise ValueError(f"collection name {value!r} {_IDENTIFIER_HINT}")
        return value


class CreateTableResult(CamelModel):
    """Result of creating a table."""

    collection: str
    primary_key: str = "id"
    fields: list[str]


class DeleteTableParams(CamelModel):
    """Parameters for deleting a table.

    Wire form::

        {"name": "customers"}
    """

    name: str = Field(min_length=1, max_length=64, description="Collection name.")


class DeleteTableResult(CamelModel):
    """Result of deleting a table."""

    collection: str
    deleted: bool


__all__ = [
    "CreateTableParams",
    "CreateTableResult",
    "DeleteTableParams",
    "DeleteTableResult",
    "FieldDefinition",
    "FieldType",
]
