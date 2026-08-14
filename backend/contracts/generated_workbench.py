"""Generated from contracts/workbench/workbench.schema.json; do not edit."""

from __future__ import annotations

from typing import Annotated, Literal

from pydantic import Field

from backend.contracts.workspace_v2 import V2Model


class ViewFilter(V2Model):
    field_id: Annotated[str, Field(min_length=1)]
    operator: Literal[
        "eq",
        "ne",
        "contains",
        "startsWith",
        "gt",
        "gte",
        "lt",
        "lte",
        "isNull",
        "isNotNull",
    ]
    value: str | float | bool | None


class ViewSort(V2Model):
    field_id: Annotated[str, Field(min_length=1)]
    direction: Literal["asc", "desc"]


class ViewQuery(V2Model):
    contract_version: Literal["1.0"]
    table_id: Annotated[str, Field(min_length=1)]
    fields: Annotated[list[Annotated[str, Field(min_length=1)]], Field(min_length=1)]
    filters: list[ViewFilter]
    sorts: list[ViewSort]
    cursor: str | None
    page_size: Annotated[int, Field(ge=1, le=500)]


class BindingVariable(V2Model):
    variable_id: Annotated[str, Field(min_length=1)]
    target_field_id: Annotated[str, Field(min_length=1)]
    operator: Literal[
        "eq",
        "ne",
        "contains",
        "startsWith",
        "gt",
        "gte",
        "lt",
        "lte",
        "isNull",
        "isNotNull",
    ]
    source: Literal["literal", "selectedRecordField"]
    source_binding_id: str | None
    source_field_id: str | None
    value: str | float | bool | None


class DataBinding(V2Model):
    binding_id: Annotated[str, Field(min_length=1)]
    query: ViewQuery
    variables: Annotated[list[BindingVariable], Field(max_length=32)]


class InterfaceAction(V2Model):
    action_id: Annotated[str, Field(min_length=1)]
    kind: Literal["record.create", "record.update", "binding.refresh", "navigate", "plugin"]
    binding_id: str | None
    target_page_id: str | None
    plugin_id: str | None
    plugin_action_id: str | None
    requires_confirmation: bool


class InterfaceElement(V2Model):
    element_id: Annotated[str, Field(min_length=1)]
    kind: Literal[
        "section",
        "columns",
        "tabs",
        "text",
        "metric",
        "chart",
        "record-list",
        "record-detail",
        "form",
        "button",
        "navigation",
    ]
    binding_id: str | None
    action_id: str | None
    text: str | None
    width: Literal["full", "half", "third"]
    children: list[InterfaceElement]


class InterfacePage(V2Model):
    page_id: Annotated[str, Field(min_length=1)]
    title: Annotated[str, Field(min_length=1)]
    elements: list[InterfaceElement]


class InterfaceDefinition(V2Model):
    contract_version: Literal["1.0"]
    interface_id: Annotated[str, Field(min_length=1)]
    name: Annotated[str, Field(min_length=1)]
    bindings: list[DataBinding]
    actions: list[InterfaceAction]
    pages: Annotated[list[InterfacePage], Field(min_length=1)]


class InterfaceSnapshot(V2Model):
    definition: InterfaceDefinition
    revision: Annotated[str, Field(min_length=1)]


class InterfaceCommitRequest(V2Model):
    definition: InterfaceDefinition
    expected_revision: str | None
    idempotency_key: Annotated[str, Field(min_length=1)]


class InterfaceListRequest(V2Model):
    pass


class InterfaceListEntry(V2Model):
    interface_id: Annotated[str, Field(min_length=1)]
    name: Annotated[str, Field(min_length=1)]
    revision: Annotated[str, Field(min_length=1)]


class InterfaceListResult(V2Model):
    items: list[InterfaceListEntry]


class InterfaceLoadRequest(V2Model):
    interface_id: Annotated[str, Field(min_length=1)]


class InterfaceCancelRequest(V2Model):
    target_request_id: Annotated[str, Field(min_length=1)]


class InterfaceDeleteRequest(V2Model):
    interface_id: Annotated[str, Field(min_length=1)]
    expected_revision: Annotated[str, Field(min_length=1)]
    idempotency_key: Annotated[str, Field(min_length=1)]


class InterfaceDeleteResult(V2Model):
    interface_id: Annotated[str, Field(min_length=1)]


class ContentProfile(V2Model):
    contract_version: Literal["1.0"]
    table_id: Annotated[str, Field(min_length=1)]
    title_field_id: Annotated[str, Field(min_length=1)]
    body_field_id: Annotated[str, Field(min_length=1)]
    summary_field_id: str | None
    searchable_field_ids: Annotated[
        list[Annotated[str, Field(min_length=1)]], Field(min_length=1, max_length=32)
    ]


class ContentProfileSnapshot(V2Model):
    profile: ContentProfile
    revision: Annotated[str, Field(min_length=1)]


class ContentProfileLoadRequest(V2Model):
    table_id: Annotated[str, Field(min_length=1)]


class ContentProfileCommitRequest(V2Model):
    profile: ContentProfile
    expected_revision: str | None
    idempotency_key: Annotated[str, Field(min_length=1)]


class ContentProfileDeleteRequest(V2Model):
    table_id: Annotated[str, Field(min_length=1)]
    expected_revision: Annotated[str, Field(min_length=1)]
    idempotency_key: Annotated[str, Field(min_length=1)]


class ContentProfileDeleteResult(V2Model):
    table_id: Annotated[str, Field(min_length=1)]


class RecordDocumentLink(V2Model):
    contract_version: Literal["1.0"]
    link_id: Annotated[str, Field(min_length=1)]
    table_id: Annotated[str, Field(min_length=1)]
    record_id: Annotated[str, Field(min_length=1)]
    document_id: Annotated[
        str,
        Field(pattern="^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"),
    ]
    role: Literal["source", "reference", "supporting", "output"]
    order: Annotated[int, Field(ge=0, le=10000)]


class RecordDocumentLinkSnapshot(V2Model):
    link: RecordDocumentLink
    revision: Annotated[str, Field(min_length=1)]


class RecordDocumentLinkListRequest(V2Model):
    table_id: Annotated[str, Field(min_length=1)]
    record_id: Annotated[str, Field(min_length=1)]


class RecordDocumentLinkListResult(V2Model):
    items: list[RecordDocumentLinkSnapshot]


class RecordDocumentLinkCommitRequest(V2Model):
    link: RecordDocumentLink
    expected_revision: str | None
    idempotency_key: Annotated[str, Field(min_length=1)]


class RecordDocumentLinkRepairRequest(V2Model):
    link_id: Annotated[str, Field(min_length=1)]
    document_id: Annotated[
        str,
        Field(pattern="^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"),
    ]
    expected_revision: Annotated[str, Field(min_length=1)]
    idempotency_key: Annotated[str, Field(min_length=1)]


class RecordDocumentLinkDeleteRequest(V2Model):
    link_id: Annotated[str, Field(min_length=1)]
    expected_revision: Annotated[str, Field(min_length=1)]
    idempotency_key: Annotated[str, Field(min_length=1)]


class RecordDocumentLinkDeleteResult(V2Model):
    link_id: Annotated[str, Field(min_length=1)]


class SearchFilter(V2Model):
    field: Literal[
        "kind",
        "tableId",
        "fieldId",
        "mimeType",
        "extension",
        "sizeBytes",
        "revisionTime",
        "status",
    ]
    operator: Literal["eq", "ne", "contains", "gt", "gte", "lt", "lte", "before", "after"]
    value: str | float | bool | None


class SearchSort(V2Model):
    field: Literal["score", "revisionTime", "title", "sizeBytes"]
    direction: Literal["asc", "desc"]


class SearchOpenTarget(V2Model):
    kind: Literal["record", "attachment", "file"]
    table_id: str | None
    record_id: str | None
    field_id: str | None
    document_id: str | None


class SearchMetadataItem(V2Model):
    key: Annotated[str, Field(min_length=1)]
    value: str | float | bool | None


class SearchRequest(V2Model):
    contract_version: Literal["1.0"]
    query: Annotated[str, Field(min_length=1)]
    logic: Literal["and", "or"]
    filters: Annotated[list[SearchFilter], Field(max_length=20)]
    sorts: Annotated[list[SearchSort], Field(max_length=3)]
    scope: Literal["current", "history"]
    cursor: str | None
    limit: Annotated[int, Field(ge=1, le=200)]


class SearchHit(V2Model):
    contract_version: Literal["1.0"]
    hit_id: Annotated[str, Field(min_length=1)]
    kind: Literal["record", "attachment", "file"]
    canonical_id: Annotated[str, Field(min_length=1)]
    title: Annotated[str, Field(min_length=1)]
    snippet: str | None
    highlights: Annotated[list[str], Field(max_length=20)]
    source_revision: Annotated[str, Field(min_length=1)]
    score: float
    revision_time: str
    metadata: list[SearchMetadataItem]
    open_target: SearchOpenTarget


class SearchResolveRequest(V2Model):
    contract_version: Literal["1.0"]
    scope: Literal["current", "history"]
    hit: SearchHit


class SearchResolveResult(V2Model):
    status: Literal["current", "stale"]
    hit: SearchHit


class SearchStatus(V2Model):
    state: Literal["idle", "building", "ready", "degraded", "failed"]
    generation: Annotated[int, Field(ge=0)]
    checkpoint: str | None
    processed: Annotated[int, Field(ge=0)]
    total: Annotated[int | None, Field(ge=0)]
    error_code: str | None


class ComputedCellEnvelope(V2Model):
    state: Literal["ready", "pending", "stale", "error"]
    value: str | float | bool | None
    definition_version: Annotated[int, Field(ge=1)]
    source_data_revision: Annotated[int, Field(ge=0)]
    dependency_watermark: Annotated[int, Field(ge=0)]
    diagnostic: str | None


class SchemaAuditEvent(V2Model):
    event_id: Annotated[str, Field(min_length=1)]
    workspace_id: str
    table_id: Annotated[str, Field(min_length=1)]
    field_id: str | None
    operation: Literal["field.create", "field.update", "field.delete", "table.update"]
    schema_revision: Annotated[int, Field(ge=1)]
    occurred_at: str
    actor_id: Annotated[str, Field(min_length=1)]
