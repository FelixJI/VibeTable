"""Versioned contracts for realtime, read-only Lookup fields."""

from __future__ import annotations

from typing import Annotated, Literal

from pydantic import Field, JsonValue, model_validator

from backend.contracts.query import FilterCondition, FilterExpression, SortCondition
from backend.contracts.selection import QuerySnapshot
from backend.contracts.table import CamelModel

LookupOutputType = Literal[
    "text",
    "integer",
    "decimal",
    "boolean",
    "date",
    "datetime",
    "time",
    "json",
]
LookupState = Literal["valid", "restricted", "invalid"]


class LookupPathStep(CamelModel):
    relation_id: str = Field(min_length=1, max_length=128)


class TargetFieldSource(CamelModel):
    kind: Literal["target_field"] = "target_field"
    field_ref: str = Field(min_length=1, max_length=128)


class LookupReferenceSource(CamelModel):
    kind: Literal["lookup"] = "lookup"
    lookup_id: str = Field(min_length=1, max_length=128)


LookupSource = Annotated[
    TargetFieldSource | LookupReferenceSource,
    Field(discriminator="kind"),
]


class LookupDiagnostic(CamelModel):
    code: str = Field(min_length=1, max_length=128)
    message: str = Field(min_length=1, max_length=1024)
    path_index: int | None = Field(default=None, ge=0)


class LookupDefinition(CamelModel):
    lookup_id: str = Field(min_length=1, max_length=128)
    collection: str = Field(min_length=1, max_length=128)
    field_key: str = Field(min_length=1, max_length=128)
    display_name: str = Field(min_length=1, max_length=128)
    # Logical depth is intentionally unbounded. Execution budgets, not an
    # arbitrary saved hop count, protect the runtime.
    path: list[LookupPathStep] = Field(min_length=1)
    source: LookupSource
    output_type: LookupOutputType
    output_scale: int | None = Field(default=None, ge=0, le=30)
    revision: int = Field(default=1, ge=1)
    state: LookupState = "valid"
    diagnostics: list[LookupDiagnostic] = Field(default_factory=list)
    dependencies: list[str] = Field(default_factory=list)

    @model_validator(mode="after")
    def validate_definition(self) -> LookupDefinition:
        if self.output_type != "decimal" and self.output_scale is not None:
            raise ValueError("outputScale is only valid for decimal Lookups")
        if isinstance(self.source, LookupReferenceSource):
            deps = set(self.dependencies)
            if self.source.lookup_id not in deps:
                raise ValueError("lookup sources must be declared as dependencies")
        return self


class LookupCollectionParams(CamelModel):
    collection: str = Field(min_length=1, max_length=128)


class LookupIdentityParams(LookupCollectionParams):
    lookup_id: str = Field(min_length=1, max_length=128)


class LookupListResult(CamelModel):
    collection: str
    definitions: list[LookupDefinition] = Field(default_factory=list)
    lookup_revision: str


class LookupValueProvenance(CamelModel):
    collection: str = Field(min_length=1, max_length=128)
    collection_label: str = Field(min_length=1, max_length=256)
    item_id: str = Field(min_length=1, max_length=256)
    record_label: str = Field(min_length=1, max_length=512)
    field_id: str = Field(min_length=1, max_length=128)
    field_label: str = Field(min_length=1, max_length=256)
    value: JsonValue = None


class LookupCellValue(CamelModel):
    state: Literal["ok", "restricted", "invalid", "too_expensive"] = "ok"
    value: JsonValue = None
    provenance: list[LookupValueProvenance] = Field(default_factory=list)
    provenance_total: int = Field(default=0, ge=0)
    provenance_total_known: bool = True
    provenance_offset: int = Field(default=0, ge=0)
    provenance_limit: int = Field(default=100, ge=1)
    provenance_has_more: bool = False
    diagnostic: LookupDiagnostic | None = None


class LookupGroup(CamelModel):
    field_ref: str = Field(min_length=1, max_length=128)
    direction: Literal["asc", "desc"] = "asc"


class LookupQuery(CamelModel):
    filters: list[FilterExpression] = Field(default_factory=list, max_length=50)
    sorts: list[SortCondition] = Field(default_factory=list, max_length=16)
    groups: list[LookupGroup] = Field(default_factory=list, max_length=2)
    offset: int = Field(default=0, ge=0)
    limit: int = Field(default=100, ge=1, le=500)

    @model_validator(mode="after")
    def validate_filter_tree(self) -> LookupQuery:
        def walk(expression: FilterExpression, parent_depth: int) -> tuple[int, int]:
            if isinstance(expression, FilterCondition):
                return parent_depth, 1
            depth = parent_depth + 1
            child_depth = depth
            count = 0
            for child in expression.filters:
                nested_depth, nested_count = walk(child, depth)
                child_depth = max(child_depth, nested_depth)
                count += nested_count
            return child_depth, count

        max_depth = 0
        condition_count = 0
        for expression in self.filters:
            depth, count = walk(expression, 0)
            max_depth = max(max_depth, depth)
            condition_count += count
        if max_depth > 3:
            raise ValueError("filter group nesting cannot exceed 3 levels")
        if condition_count > 50:
            raise ValueError("filter tree cannot contain more than 50 conditions")
        return self


class LookupQueryParams(CamelModel):
    contract: Literal["vibetable.lookup-query.v1"] = "vibetable.lookup-query.v1"
    collection: str = Field(min_length=1, max_length=128)
    field_refs: list[str] = Field(min_length=1, max_length=256)
    query: LookupQuery = Field(default_factory=LookupQuery)
    request_generation: int = Field(default=0, ge=0)
    schema_revision: str = Field(min_length=1, max_length=128)
    permission_revision: str = Field(min_length=1, max_length=128)
    lookup_revision: str = Field(min_length=1, max_length=128)


class LookupValuePageParams(CamelModel):
    collection: str = Field(min_length=1, max_length=128)
    field_ref: str = Field(min_length=1, max_length=128)
    source_record_id: str = Field(min_length=1, max_length=256)
    offset: int = Field(default=0, ge=0)
    limit: int = Field(default=100, ge=1, le=500)
    schema_revision: str = Field(min_length=1, max_length=128)
    permission_revision: str = Field(min_length=1, max_length=128)
    lookup_revision: str = Field(min_length=1, max_length=128)


class LookupColumnResult(CamelModel):
    field_ref: str
    title: str
    output_type: LookupOutputType
    nullable: bool = True
    scale: int | None = None
    state: LookupState = "valid"


class LookupGroupNode(CamelModel):
    path: list[JsonValue] = Field(default_factory=list)
    key: JsonValue = None
    count: int = Field(ge=0)
    aggregates: dict[str, JsonValue] = Field(default_factory=dict)
    child_cursor: str | None = None


class LookupQueryResult(CamelModel):
    contract: Literal["vibetable.lookup-query.v1"] = "vibetable.lookup-query.v1"
    collection: str
    request_generation: int = Field(ge=0)
    schema_revision: str
    permission_revision: str
    lookup_revision: str
    columns: list[LookupColumnResult]
    rows: list[dict[str, JsonValue]]
    groups: list[LookupGroupNode] = Field(default_factory=list)
    offset: int = Field(ge=0)
    limit: int = Field(ge=1)
    filtered_rows: int = Field(ge=0)
    total_rows: int = Field(ge=0)
    snapshot: QuerySnapshot


def validate_lookup_dependency_graph(definitions: list[LookupDefinition]) -> None:
    """Raise when saved Lookup definitions contain a dependency cycle."""

    graph = {definition.lookup_id: set(definition.dependencies) for definition in definitions}
    if len(graph) != len(definitions):
        raise ValueError("Lookup IDs must be globally unique")
    known = set(graph)
    for lookup_id, dependencies in graph.items():
        unknown = dependencies - known
        if unknown:
            raise ValueError(
                f"Lookup {lookup_id!r} references unknown dependencies: "
                + ", ".join(sorted(unknown))
            )

    visiting: set[str] = set()
    visited: set[str] = set()

    def visit(lookup_id: str) -> None:
        if lookup_id in visiting:
            raise ValueError(f"Lookup dependency cycle detected at {lookup_id!r}")
        if lookup_id in visited:
            return
        visiting.add(lookup_id)
        for dependency in graph[lookup_id]:
            visit(dependency)
        visiting.remove(lookup_id)
        visited.add(lookup_id)

    for lookup_id in graph:
        visit(lookup_id)


__all__ = [
    "LookupCellValue",
    "LookupCollectionParams",
    "LookupColumnResult",
    "LookupDefinition",
    "LookupDiagnostic",
    "LookupGroup",
    "LookupGroupNode",
    "LookupIdentityParams",
    "LookupListResult",
    "LookupOutputType",
    "LookupPathStep",
    "LookupQuery",
    "LookupQueryParams",
    "LookupQueryResult",
    "LookupReferenceSource",
    "LookupSource",
    "LookupState",
    "LookupValueProvenance",
    "TargetFieldSource",
    "validate_lookup_dependency_graph",
]
