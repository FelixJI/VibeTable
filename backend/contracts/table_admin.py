from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field
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


class FieldDefinition(CamelModel):
    """A single field in a new table."""

    key: str = Field(min_length=1, max_length=64, description="Field name (column key).")
    type: FieldType = Field(description="Field data type.")


class CreateTableParams(CamelModel):
    """Parameters for creating a new table.

    Wire form::

        {"name": "customers", "fields": [{"key": "name", "type": "string"}]}
    """

    name: str = Field(min_length=1, max_length=64, description="Collection name.")
    fields: list[FieldDefinition] = Field(min_length=0, max_length=64)


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
