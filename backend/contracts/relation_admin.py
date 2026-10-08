"""Contracts for normalized relation discovery, schema changes and data edits."""

from __future__ import annotations

from typing import Annotated, Literal

from pydantic import Field

from backend.contracts.data_profile import RelationDeletePolicy
from backend.contracts.generated_schema_v2 import DisplaySpec, SelectOption
from backend.contracts.table import CamelModel, ColumnSchema


class RelationDiagnostic(CamelModel):
    code: str = Field(min_length=1, max_length=128)
    message: str = Field(min_length=1, max_length=1024)
    severity: Literal["warning", "error"] = "error"


class RelationDisplayFieldInfo(CamelModel):
    """Render contract of one relation label source field (#447).

    Labels format through the canonical DisplaySpec (and select options) of
    the field that actually produced the value (configured display field or
    global primary display fallback); never a second display authority.
    """

    field_id: str = Field(min_length=1, max_length=128)
    data_type: Literal[
        "text",
        "integer",
        "decimal",
        "boolean",
        "date",
        "datetime",
        "time",
        "json",
    ]
    display: DisplaySpec | None = None
    enum_options: list[SelectOption] | None = None


class NormalizedRelationDescriptor(CamelModel):
    relation_id: str = Field(min_length=1, max_length=128)
    field_ref: str = Field(min_length=1, max_length=128)
    source_collection: str = Field(min_length=1, max_length=128)
    kind: Literal["m2o", "o2m", "m2m"]
    related_collection: str | None = Field(default=None, min_length=1, max_length=128)
    many_field: str | None = Field(default=None, min_length=1, max_length=128)
    one_field: str | None = Field(default=None, min_length=1, max_length=128)
    unique: bool = False
    nullable: bool = True
    on_delete: RelationDeletePolicy = "nullify"
    preset: Literal["standard", "file", "files", "translations"] = "standard"
    self_relation: bool = False
    managed: bool = False
    pair_id: str = Field(default="", exclude_if=lambda value: not value)
    reciprocal_field_id: str = Field(default="", exclude_if=lambda value: not value)
    quick_create_eligible: bool = False
    quick_create_reason: str = ""
    state: Literal["valid", "readonly", "invalid"] = "valid"
    display_template: str | None = None
    diagnostics: list[RelationDiagnostic] = Field(default_factory=list)
    # Relation label display projection (#447): the relation's own display
    # field in the target table plus both render contracts.
    display_field_id: str | None = None
    display_field_info: RelationDisplayFieldInfo | None = None
    fallback_display_field_info: RelationDisplayFieldInfo | None = None


class SchemaSnapshot(CamelModel):
    collection: str
    primary_key: str
    primary_display_field_id: str = Field(default="", exclude_if=lambda value: not value)
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
    lookup_max_depth: int = Field(default=8, ge=1, le=32)
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
    secondary_label: str | None = None
    # Raw typed scalars behind label/secondary_label (display-only; 0 and
    # false are valid values). Clients render them through the shared field
    # display contract instead of re-parsing the label text; they never join
    # mutation business values.
    display_value: str | int | float | bool | None = None
    secondary_value: str | int | float | bool | None = None


class RelationSearchParams(CamelModel):
    relation_id: str
    query: str = Field(default="", max_length=256)
    offset: int = Field(default=0, ge=0)
    limit: int = Field(default=50, ge=1, le=200)
    # Direct batched refresh for already-selected targets (#447): resolves the
    # same label projection by stable record IDs, independent of the keyword.
    target_item_ids: list[Annotated[str, Field(min_length=1)]] = Field(
        default_factory=list, min_length=1, max_length=100, exclude_if=lambda value: not value
    )


class RelationSearchResult(CamelModel):
    items: list[RelationTargetRef]
    total: int = Field(ge=0)


class RelationCreateTargetResult(CamelModel):
    outcome: Literal["committed"]
    target: RelationTargetRef
    request_id: str


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


class RelationRemove(CamelModel):
    target: RelationTargetRef


class RelationDelta(CamelModel):
    relation_id: str
    source_item_id: str
    expected_schema_revision: str
    expected_date_updated: str | None = None
    adds: list[RelationAdd] = Field(default_factory=list)
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
    "RelationCreateTargetResult",
    "RelationDelta",
    "RelationDeltaPreview",
    "RelationDeltaResult",
    "RelationDiagnostic",
    "RelationDiscoveryResult",
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
