from __future__ import annotations

import unicodedata
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


def _display_name(value: str) -> str:
    """Normalize a user-facing name without turning it into an identifier."""
    # Preserve the user's visible typography (for example full-width Chinese
    # punctuation). NFKC/case-folding belongs only to the conflict key stored
    # in the identifier registry.
    normalized = value.strip()
    if not normalized:
        raise ValueError("display name must not be blank")
    if any(unicodedata.category(char) in {"Cc", "Cs"} for char in normalized):
        raise ValueError("display name must not contain control characters")
    return normalized


class FieldDefinition(CamelModel):
    """A user-facing field definition in a new table.

    ``key`` is retained for wire compatibility, but represents the display
    name. The service allocates a stable ASCII physical field key.
    """

    key: str = Field(min_length=1, max_length=128, description="Field display name.")
    type: FieldType = Field(description="Field data type.")

    @field_validator("key")
    @classmethod
    def _validate_key_display_name(cls, value: str) -> str:
        return _display_name(value)


class CreateTableParams(CamelModel):
    """Parameters for creating a new table.

    Wire form::

        {"name": "客户清单", "fields": [{"key": "姓名", "type": "string"}]}
    """

    name: str = Field(min_length=1, max_length=128, description="Collection display name.")
    fields: list[FieldDefinition] = Field(min_length=0, max_length=64)

    @field_validator("name")
    @classmethod
    def _validate_name_display_name(cls, value: str) -> str:
        return _display_name(value)


class CreateTableResult(CamelModel):
    """Result of creating a table."""

    collection: str
    display_name: str | None = None
    primary_key: str = "id"
    fields: list[str]
    field_display_names: dict[str, str] = Field(default_factory=dict)


class DeleteTableParams(CamelModel):
    """Parameters for deleting a table.

    Wire form::

        {"name": "customers"}
    """

    name: str = Field(min_length=1, max_length=128, description="Physical collection name.")


class DeleteTableResult(CamelModel):
    """Result of deleting a table."""

    collection: str
    deleted: bool


class IdentifierMappingEntry(CamelModel):
    """Safe, user-facing projection of one registry row."""

    id: str
    entity_kind: Literal["collection", "field"]
    parent_physical_name: str | None = None
    physical_name: str
    display_name: str
    locale: str = "zh-CN"
    aliases: list[str] = Field(default_factory=list)
    origin: Literal["vibetable", "directus", "import"]
    status: Literal["pending", "active", "orphaned", "deleted"]


class IdentifierMappingsResult(CamelModel):
    mappings: list[IdentifierMappingEntry] = Field(default_factory=list)


class ListIdentifierMappingsParams(CamelModel):
    search: str | None = Field(default=None, max_length=128)


class UpdateIdentifierAliasesParams(CamelModel):
    mapping_id: str = Field(min_length=1, max_length=64)
    aliases: list[str] = Field(default_factory=list, max_length=32)


class ImportIdentifierMappingItem(CamelModel):
    """Portable mapping data. Physical identity is matched, never changed."""

    entity_kind: Literal["collection", "field"]
    parent_physical_name: str | None = None
    physical_name: str = Field(min_length=1, max_length=128)
    display_name: str = Field(min_length=1, max_length=128)
    aliases: list[str] = Field(default_factory=list, max_length=32)


class ImportIdentifierMappingsParams(CamelModel):
    mappings: list[ImportIdentifierMappingItem] = Field(default_factory=list, max_length=4096)


class ReconcileIdentifierMappingsParams(CamelModel):
    pass


__all__ = [
    "CreateTableParams",
    "CreateTableResult",
    "DeleteTableParams",
    "DeleteTableResult",
    "FieldDefinition",
    "FieldType",
    "IdentifierMappingEntry",
    "IdentifierMappingsResult",
    "ImportIdentifierMappingItem",
    "ImportIdentifierMappingsParams",
    "ListIdentifierMappingsParams",
    "ReconcileIdentifierMappingsParams",
    "UpdateIdentifierAliasesParams",
]
