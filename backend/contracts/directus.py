"""B4 JSON-RPC contracts for Directus auth, schema, query and mutations."""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel

from backend.adapters.directus.auth import SessionStatus
from backend.contracts.query import TableQuery
from backend.contracts.table import ColumnSchema


class CamelModel(BaseModel):
    model_config = ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class DirectusEmptyParams(CamelModel):
    pass


class DirectusLoginParams(CamelModel):
    email: str = Field(min_length=3, max_length=320)
    password: str = Field(min_length=1, max_length=1024)
    otp: str | None = Field(default=None, min_length=1, max_length=128)


class DirectusCollectionParams(CamelModel):
    collection: str = Field(min_length=1, max_length=128)


class DirectusItemParams(DirectusCollectionParams):
    item_id: str = Field(min_length=1, max_length=128)


class DirectusReadParams(DirectusCollectionParams):
    query: TableQuery = Field(default_factory=TableQuery)
    include_archived: bool = False


class DirectusCreateParams(DirectusCollectionParams):
    values: dict[str, Any]
    request_id: str | None = Field(default=None, max_length=128)


class DirectusUpdateParams(DirectusItemParams):
    values: dict[str, Any]
    expected_date_updated: str | None = None
    request_id: str | None = Field(default=None, max_length=128)


class DirectusSubscribeParams(DirectusCollectionParams):
    uid: str = Field(min_length=1, max_length=128)
    fields: list[str] = Field(min_length=1, max_length=64)


class DirectusUnsubscribeParams(CamelModel):
    uid: str = Field(min_length=1, max_length=128)


class DirectusCollectionListResult(CamelModel):
    collections: list[str]
    capability_hashes: dict[str, str]
    display_names: dict[str, str] = Field(default_factory=dict)


class DirectusServerInfoResult(CamelModel):
    project_name: str | None = None
    directus_version: str | None = None
    compatibility: str


class DirectusSchemaResult(CamelModel):
    collection: str
    primary_key: str
    columns: list[ColumnSchema]
    relations: list[dict[str, Any]]
    schema_revision: str
    capability_hash: str


class DirectusPageResult(CamelModel):
    collection: str
    rows: list[dict[str, Any]]
    offset: int
    limit: int
    filtered_rows: int | None = None
    total_rows: int | None = None
    semantic_gaps: list[str] = Field(default_factory=list)
    capability_hash: str


class DirectusItemResult(CamelModel):
    collection: str
    item: dict[str, Any]


class DirectusSubscriptionResult(CamelModel):
    uid: str
    collection: str | None = None
    active: bool


__all__ = [
    "DirectusCollectionListResult",
    "DirectusCollectionParams",
    "DirectusCreateParams",
    "DirectusEmptyParams",
    "DirectusItemParams",
    "DirectusItemResult",
    "DirectusLoginParams",
    "DirectusPageResult",
    "DirectusReadParams",
    "DirectusSchemaResult",
    "DirectusServerInfoResult",
    "DirectusSubscribeParams",
    "DirectusSubscriptionResult",
    "DirectusUnsubscribeParams",
    "DirectusUpdateParams",
    "SessionStatus",
]
