"""Contracts for normalized relation discovery, schema changes and data edits."""

from __future__ import annotations

from typing import Any, Literal

from pydantic import Field

from backend.contracts.data_profile import JunctionProfile, RelationDeletePolicy
from backend.contracts.table import CamelModel, ColumnSchema


class RelationDiagnostic(CamelModel):
    code: str = Field(min_length=1, max_length=128)
    message: str = Field(min_length=1, max_length=1024)
    severity: Literal["warning", "error"] = "error"


class NormalizedRelationDescriptor(CamelModel):
    relation_id: str = Field(min_length=1, max_length=128)
    field_ref: str = Field(min_length=1, max_length=128)
    source_collection: str = Field(min_length=1, max_length=128)
    kind: Literal["m2o", "o2m", "m2m", "m2a"]
    related_collection: str | None = Field(default=None, min_length=1, max_length=128)
    allowed_collections: list[str] = Field(default_factory=list)
    many_field: str | None = Field(default=None, min_length=1, max_length=128)
    one_field: str | None = Field(default=None, min_length=1, max_length=128)
    junction: JunctionProfile | None = None
    unique: bool = False
    nullable: bool = True
    on_delete: RelationDeletePolicy = "nullify"
    preset: Literal["standard", "file", "files", "translations"] = "standard"
    self_relation: bool = False
    managed: bool = False
    state: Literal["valid", "readonly", "invalid"] = "valid"
    display_template: str | None = None
    diagnostics: list[RelationDiagnostic] = Field(default_factory=list)


class SchemaSnapshot(CamelModel):
    collection: str
    primary_key: str
    columns: list[ColumnSchema]
    normalized_relations: list[NormalizedRelationDescriptor] = Field(default_factory=list)
    schema_revision: str
    permission_revision: str
    capability_hash: str
    lookup_revision: str


class RelationLookupCapabilities(CamelModel):
    contract: Literal["vibetable.relation-capabilities.v1"] = "vibetable.relation-capabilities.v1"
    relation_read_v1: bool = True
    relation_edit_v1: bool = False
    relation_import_v1: bool = False
    lookup_query_v1: bool = False
    reason: Literal["extension_missing", "incompatible", "permission_denied"] | None = None


class SchemaDescribeParams(CamelModel):
    collection: str = Field(min_length=1, max_length=128)
    request_generation: int = Field(default=0, ge=0)
    accepts: list[str] = Field(default_factory=list, max_length=16)


class SchemaDescribeResult(CamelModel):
    contract: Literal["vibetable.schema-describe.v1"] = "vibetable.schema-describe.v1"
    collection: str
    request_generation: int = Field(ge=0)
    schema_snapshot: SchemaSnapshot = Field(alias="schema")
    capabilities: RelationLookupCapabilities


class RelationDiscoveryResult(CamelModel):
    relations: list[NormalizedRelationDescriptor]
    schema_revision: str
    diagnostics: list[RelationDiagnostic] = Field(default_factory=list)


class RelationTargetRef(CamelModel):
    collection: str
    item_id: str
    label: str
    junction_id: str | None = None
    junction_revision: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")
    junction_values: dict[str, Any] = Field(default_factory=dict)


class RelationSearchParams(CamelModel):
    relation_id: str
    query: str = Field(default="", max_length=256)
    collection: str | None = None
    offset: int = Field(default=0, ge=0)
    limit: int = Field(default=50, ge=1, le=200)


class RelationSearchResult(CamelModel):
    items: list[RelationTargetRef]
    total: int = Field(ge=0)


class RelationSingleUpdateParams(CamelModel):
    relation_id: str = Field(min_length=1, max_length=128)
    source_item_id: str = Field(min_length=1, max_length=256)
    target: RelationTargetRef | None = None
    expected_schema_revision: str = Field(min_length=1, max_length=128)
    expected_date_updated: str | None = None
    idempotency_key: str = Field(min_length=1, max_length=128)


class RelationSingleUpdateResult(CamelModel):
    outcome: Literal["committed", "conflict"]
    current: RelationTargetRef | None = None
    schema_revision: str
    request_id: str


class RelationAdd(CamelModel):
    target: RelationTargetRef


class RelationJunctionPatch(CamelModel):
    junction_id: str
    values: dict[str, Any]
    expected_revision: str | None = None


class RelationRemove(CamelModel):
    target: RelationTargetRef
    expected_revision: str | None = None


class RelationDelta(CamelModel):
    relation_id: str
    source_item_id: str
    expected_schema_revision: str
    expected_date_updated: str | None = None
    adds: list[RelationAdd] = Field(default_factory=list)
    updates: list[RelationJunctionPatch] = Field(default_factory=list)
    removes: list[RelationRemove] = Field(default_factory=list)
    idempotency_key: str = Field(min_length=1, max_length=128)


class RelationDeltaResult(CamelModel):
    outcome: Literal["committed", "conflict"]
    current: list[RelationTargetRef]
    schema_revision: str
    request_id: str


class RelationDeltaPreview(CamelModel):
    delta: RelationDelta
    relation_id: str
    source_item_id: str
    adds: int = Field(ge=0)
    updates: int = Field(ge=0)
    removes: int = Field(ge=0)
    current: list[RelationTargetRef] = Field(default_factory=list)
    can_apply: bool
    schema_revision: str
    diagnostics: list[RelationDiagnostic] = Field(default_factory=list)


__all__ = [
    "NormalizedRelationDescriptor",
    "RelationAdd",
    "RelationDelta",
    "RelationDeltaPreview",
    "RelationDeltaResult",
    "RelationDiagnostic",
    "RelationDiscoveryResult",
    "RelationJunctionPatch",
    "RelationLookupCapabilities",
    "RelationRemove",
    "RelationSearchParams",
    "RelationSearchResult",
    "RelationSingleUpdateParams",
    "RelationSingleUpdateResult",
    "RelationTargetRef",
    "SchemaDescribeParams",
    "SchemaDescribeResult",
    "SchemaSnapshot",
]
