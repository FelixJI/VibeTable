"""Generated from contracts/schema-v2/schema.schema.json; do not edit."""

from __future__ import annotations

from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field, JsonValue


def _to_camel(value: str) -> str:
    head, *tail = value.split("_")
    return head + "".join(part.capitalize() for part in tail)


class SchemaV2WireModel(BaseModel):
    model_config = ConfigDict(
        alias_generator=_to_camel,
        extra="forbid",
        populate_by_name=True,
        strict=True,
    )


type LogicalType = Literal[
    "text",
    "editor",
    "number",
    "bool",
    "date",
    "dateTime",
    "time",
    "autoDate",
    "email",
    "url",
    "select",
    "multiSelect",
    "relation",
    "file",
    "geoPoint",
    "json",
    "formula",
    "lookup",
]


class FieldIdentity(SchemaV2WireModel):
    field_id: Annotated[str, Field(pattern="^fld_[A-Za-z0-9_-]{8,}$")]
    physical_name: Annotated[str, Field(pattern="^f_[a-z0-9_]{8,}$")]
    provider_field_id: Annotated[str, Field(pattern="^pb_[A-Za-z0-9_-]{8,}$")]


class Lifecycle(SchemaV2WireModel):
    state: Literal["active", "retired"]
    retired_at: str | None


class DefaultSpec(SchemaV2WireModel):
    enabled: bool
    value: JsonValue
    source: Literal["recommended", "user"]
    defaults_version: Annotated[int, Field(ge=1)]


class PresenceSpec(SchemaV2WireModel):
    mode: Literal["companion", "native", "computed"]
    provider_field_id: str | None = None
    physical_name: str | None = None


class ValueSpec(SchemaV2WireModel):
    required: bool
    default: DefaultSpec
    presence: PresenceSpec


class UniqueSpec(SchemaV2WireModel):
    enabled: bool
    blank_policy: Literal["ignoreMissing"]


class RangeSpec(SchemaV2WireModel):
    min: float | str | None
    max: float | str | None


class LengthSpec(SchemaV2WireModel):
    min: Annotated[int | None, Field(ge=0)]
    max: Annotated[int | None, Field(ge=0)]


class PatternSpec(SchemaV2WireModel):
    enabled: bool
    value: str


class DomainSpec(SchemaV2WireModel):
    only: list[str]
    except_: list[str] = Field(alias="except")


class SelectionSpec(SchemaV2WireModel):
    min: Annotated[int, Field(ge=0)]
    max: Annotated[int | None, Field(ge=0)]


class ConstraintSpec(SchemaV2WireModel):
    unique: UniqueSpec
    range: RangeSpec
    length: LengthSpec
    pattern: PatternSpec
    domains: DomainSpec
    selection: SelectionSpec


class StorageOptions(SchemaV2WireModel):
    only_int: bool
    max_size: Annotated[int, Field(ge=0)]
    convert_u_r_ls: bool
    presentable: bool


class StorageSpec(SchemaV2WireModel):
    kind: Literal[
        "pocketbase-text",
        "pocketbase-editor",
        "pocketbase-number",
        "pocketbase-bool",
        "pocketbase-date",
        "pocketbase-autodate",
        "pocketbase-email",
        "pocketbase-url",
        "pocketbase-select",
        "pocketbase-relation",
        "pocketbase-file",
        "pocketbase-geo-point",
        "pocketbase-json",
        "computed",
    ]
    options: StorageOptions


class DisplaySpec(SchemaV2WireModel):
    kind: Literal[
        "text",
        "editor",
        "number",
        "bool",
        "date",
        "dateTime",
        "time",
        "email",
        "url",
        "select",
        "relation",
        "file",
        "geoPoint",
        "json",
        "readonly",
    ]
    preset: str
    display_scale: Annotated[int, Field(ge=0, le=15)]
    scale_mode: Literal["max", "fixed"]
    trim_trailing_zeros: bool
    use_grouping: bool
    currency: str
    percent_storage: Literal["ratio", "percent"]
    unit: str | None
    precision: Literal["exact", "day", "minute", "second", "millisecond"]
    timezone: str
    mode: str
    indent: Literal[0, 2, 4] | None = None
    true_label: str
    false_label: str


class SelectOption(SchemaV2WireModel):
    option_id: Annotated[str, Field(pattern="^opt_[A-Za-z0-9_-]{8,}$")]
    label: Annotated[str, Field(min_length=1)]
    color: str
    order: int
    state: Literal["active", "retired"]


class SelectSpec(SchemaV2WireModel):
    options: Annotated[list[SelectOption], Field(min_length=1)]


class SelectOptionDraft(SchemaV2WireModel):
    option_id: str
    label: Annotated[str, Field(min_length=1)]
    color: str
    order: int
    state: Literal["active", "retired"]


class SelectDraftSpec(SchemaV2WireModel):
    options: Annotated[list[SelectOptionDraft], Field(min_length=1)]


class RelationSpec(SchemaV2WireModel):
    target_table_id: Annotated[str, Field(min_length=1)]
    cardinality: Literal["one", "many"]
    delete_policy: Literal["setNull", "restrict", "cascade"]
    display_field_id: str
    pair_id: Annotated[str, Field(min_length=1)] | None = None
    reciprocal_field_id: Annotated[str, Field(min_length=1)] | None = None


class FileSpec(SchemaV2WireModel):
    max_files: Annotated[int, Field(ge=1)]
    max_bytes_per_file: Annotated[int, Field(ge=1)]
    allowed_mime_types: list[str]
    thumbs: list[str]
    protected: bool


class JSONSpec(SchemaV2WireModel):
    root_type: Literal["any", "object", "array", "string", "number", "boolean", "null"]
    max_size: Annotated[int, Field(ge=1)]
    schema_: dict[str, JsonValue] = Field(alias="schema")


class AutoDateSpec(SchemaV2WireModel):
    role: Literal["createdAt", "updatedAt"]


class FormulaSpec(SchemaV2WireModel):
    language: Literal["cel-v1"]
    source: Annotated[str, Field(min_length=1)]
    result_type: LogicalType


class FormulaDraftSpec(SchemaV2WireModel):
    language: Literal["cel-v1"]
    source: Annotated[str, Field(min_length=1)]


class LookupSpec(SchemaV2WireModel):
    path: Annotated[list[LookupPathStep], Field(min_length=1, max_length=8)]
    target_field_id: Annotated[str, Field(min_length=1)]


class LookupPathStep(SchemaV2WireModel):
    relation_field_id: Annotated[str, Field(min_length=1)]


class FieldDefinition(SchemaV2WireModel):
    contract: Literal["vibetable.schema.v2"]
    identity: FieldIdentity
    display_name: Annotated[str, Field(min_length=1)]
    help: str
    logical_type: LogicalType
    lifecycle: Lifecycle
    value: ValueSpec
    constraints: ConstraintSpec
    storage: StorageSpec
    display: DisplaySpec
    select: SelectSpec | None = None
    relation: RelationSpec | None = None
    file: FileSpec | None = None
    json_: JSONSpec | None = Field(None, alias="json")
    auto_date: AutoDateSpec | None = None
    formula: FormulaSpec | None = None
    lookup: LookupSpec | None = None


class FieldDraft(SchemaV2WireModel):
    display_name: Annotated[str, Field(min_length=1)]
    help: str
    logical_type: LogicalType
    value: ValueSpec
    constraints: ConstraintSpec
    storage: StorageSpec
    display: DisplaySpec
    select: SelectDraftSpec | None = None
    relation: RelationSpec | None = None
    file: FileSpec | None = None
    json_: JSONSpec | None = Field(None, alias="json")
    auto_date: AutoDateSpec | None = None
    formula: FormulaDraftSpec | None = None
    lookup: LookupSpec | None = None


class RecommendedValues(SchemaV2WireModel):
    defaults_version: Annotated[int, Field(ge=1)]
    value: ValueSpec
    constraints: ConstraintSpec
    storage: StorageSpec
    display: DisplaySpec
    file: FileSpec | None = None
    json_: JSONSpec | None = Field(None, alias="json")


class Capability(SchemaV2WireModel):
    logical_type: LogicalType
    general_settings: list[str]
    advanced_settings: list[str]
    danger_settings: list[str]
    recommended: RecommendedValues
    supports_required: bool
    supports_default: bool
    supports_unique: bool
    needs_presence: bool
    display_presets: list[str]
    conversion_targets: list[LogicalType]
    conversion_rules: list[str]
    compile_strategy: str
    user_creatable: bool
    filter_operators: list[str]
    groupable: bool
    summary_operations: list[str]
    relation_cardinalities: list[Literal["one", "many"]]
    relation_delete_policies: list[Literal["setNull", "restrict"]]
    lookup_max_depth: Annotated[int, Field(ge=0, le=8)]
    formula_result_type_inferred: bool
    formula_relation_aggregates: list[Literal["SUM", "COUNT", "AVG", "MIN", "MAX", "ANY"]]


class SchemaSnapshot(SchemaV2WireModel):
    contract: Literal["vibetable.schema.v2"]
    table_id: Annotated[str, Field(min_length=1)]
    display_name: Annotated[str, Field(min_length=1)]
    kind: Literal["base", "view"]
    schema_revision: Annotated[str, Field(min_length=1)]
    data_revision: Annotated[int, Field(ge=0)]
    archive_policy: ArchivePolicy
    fields: list[FieldDefinition]
    capabilities: list[Capability]


class FormulaValidateRequest(SchemaV2WireModel):
    table_id: Annotated[str, Field(min_length=1)]
    field: FieldDefinition


class FormulaPreviewRequest(SchemaV2WireModel):
    table_id: Annotated[str, Field(min_length=1)]
    field: FieldDefinition
    row: dict[str, JsonValue]
    changed_field_ids: list[Annotated[str, Field(min_length=1)]]


class TableCreateIntent(SchemaV2WireModel):
    display_name: Annotated[str, Field(min_length=1, max_length=128)]
    operation_id: Annotated[str, Field(min_length=1)]
    actor: Actor


class TableCreateReceipt(SchemaV2WireModel):
    contract: Literal["vibetable.schema.v2"]
    operation_id: Annotated[str, Field(min_length=1)]
    table_id: Annotated[str, Field(min_length=1)]
    display_name: Annotated[str, Field(min_length=1)]
    schema_revision: Annotated[str, Field(min_length=1)]


class ArchivePolicy(SchemaV2WireModel):
    mode: Literal["none", "status", "deletedAt"]
    field_id: str | None
    archived_value: JsonValue


class TableSettingsIntent(SchemaV2WireModel):
    table_id: Annotated[str, Field(min_length=1)]
    expected_schema_revision: Annotated[str, Field(min_length=1)]
    archive_policy: ArchivePolicy
    operation_id: Annotated[str, Field(min_length=1)]
    actor: Actor


class TableSettingsReceipt(SchemaV2WireModel):
    contract: Literal["vibetable.schema.v2"]
    operation_id: Annotated[str, Field(min_length=1)]
    table_id: Annotated[str, Field(min_length=1)]
    schema_revision: Annotated[str, Field(min_length=1)]
    archive_policy: ArchivePolicy


class Actor(SchemaV2WireModel):
    id: Annotated[str, Field(min_length=1)]
    kind: Annotated[str, Field(min_length=1)]


class RelationPairDraft(SchemaV2WireModel):
    reciprocal_display_name: Annotated[str, Field(min_length=1)]
    reciprocal_cardinality: Literal["one", "many"]
    source_display_field_id: Annotated[str, Field(min_length=1)]


class FieldChangeIntent(SchemaV2WireModel):
    action: Literal["create", "update", "retire", "restore", "purge", "convert", "backfill"]
    table_id: Annotated[str, Field(min_length=1)]
    field_id: str
    expected_schema_revision: Annotated[str, Field(min_length=1)]
    expected_data_revision: Annotated[int | None, Field(ge=0)]
    draft: FieldDraft | None
    actor: Actor
    conversion_rule: str
    confirmation: str
    backup_receipt: str
    relation_pair: RelationPairDraft | None = None


class Diagnostic(SchemaV2WireModel):
    code: Annotated[str, Field(min_length=1)]
    path: str
    message: Annotated[str, Field(min_length=1)]
    details: dict[str, JsonValue]


class FailureSample(SchemaV2WireModel):
    record_id: str
    reason: str


class DependencyRef(SchemaV2WireModel):
    kind: str
    id: str
    name: str


class Impact(SchemaV2WireModel):
    records: Annotated[int, Field(ge=0)]
    missing: Annotated[int, Field(ge=0)]
    ambiguous: Annotated[int, Field(ge=0)]
    failures: list[FailureSample]
    dependencies: list[DependencyRef]


class PlanStep(SchemaV2WireModel):
    kind: str
    details: dict[str, JsonValue]


class RelatedFieldChange(SchemaV2WireModel):
    table_id: Annotated[str, Field(min_length=1)]
    field_id: Annotated[str, Field(min_length=1)]
    before: FieldDefinition | None
    after: FieldDefinition | None
    expected_schema_revision: Annotated[str, Field(min_length=1)]


class FieldChangePlan(SchemaV2WireModel):
    contract: Literal["vibetable.schema.v2"]
    plan_id: Annotated[str, Field(min_length=1)]
    plan_hash: Annotated[str, Field(min_length=1)]
    expires_at: Annotated[str, Field(min_length=1)]
    intent: FieldChangeIntent
    before: FieldDefinition | None
    after: FieldDefinition | None
    classes: list[Literal["display", "metadata", "constraint", "schema", "migration", "danger"]]
    expected_schema_revision: str
    expected_data_revision: Annotated[int | None, Field(ge=0)]
    impact: Impact
    steps: list[PlanStep]
    warnings: list[Diagnostic]
    errors: list[Diagnostic]
    confirmations: list[str]
    creates_migration: bool
    can_apply: bool
    related_changes: list[RelatedFieldChange] | None = None


class RelatedApplyReceipt(SchemaV2WireModel):
    table_id: Annotated[str, Field(min_length=1)]
    field_id: Annotated[str, Field(min_length=1)]
    schema_revision: Annotated[str, Field(min_length=1)]
    definition: FieldDefinition | None


class ApplyReceipt(SchemaV2WireModel):
    contract: Literal["vibetable.schema.v2"]
    operation_id: str
    plan_id: str
    action: Literal["create", "update", "retire", "restore", "purge", "convert", "backfill"]
    table_id: str
    field_id: str
    schema_revision: str
    definition: FieldDefinition | None
    migration_job_id: str
    related: list[RelatedApplyReceipt] | None = None


class ApplyRequest(SchemaV2WireModel):
    plan_id: Annotated[str, Field(min_length=1)]
    plan_hash: Annotated[str, Field(min_length=1)]
    operation_id: Annotated[str, Field(min_length=1)]
    actor: Actor
    confirmations: list[str]
    protection_snapshot_id: str | None = None


class MigrationStatus(SchemaV2WireModel):
    contract: Literal["vibetable.schema.v2"]
    job_id: str
    plan_id: str
    phase: Literal[
        "planned",
        "validating",
        "ready",
        "copying",
        "verifying",
        "switching",
        "completed",
        "cancelled",
        "failed",
        "cleaning",
        "rolled_back",
    ]
    processed: Annotated[int, Field(ge=0)]
    total: Annotated[int, Field(ge=0)]
    can_cancel: bool
    error: Diagnostic | None
    updated_at: str


class FieldSettingsDescribeResult(SchemaV2WireModel):
    contract: Literal["vibetable.schema.v2"]
    table_id: Annotated[str, Field(min_length=1)]
    field_id: str
    schema_revision: Annotated[str, Field(min_length=1)]
    data_revision: Annotated[int, Field(ge=0)]
    definition: FieldDefinition | None
    capabilities: list[Capability]
    recommended_defaults_version: Annotated[int, Field(ge=1)]


class FieldRecycleBinResult(SchemaV2WireModel):
    contract: Literal["vibetable.schema.v2"]
    fields: list[FieldDefinition]


class FieldValueCorpusOption(SchemaV2WireModel):
    option_id: Annotated[str, Field(min_length=1)]
    label: str


class FieldValueCorpusCase(SchemaV2WireModel):
    id: Annotated[str, Field(min_length=1)]
    field: Annotated[str, Field(min_length=1)]
    logical_type: LogicalType
    raw_value: str
    product_value: JsonValue
    select_options: Annotated[list[FieldValueCorpusOption], Field(min_length=1)] | None = None


class FieldValueEntryCorpus(SchemaV2WireModel):
    schema_: Literal["vibetable.field-value-entry-corpus.v1"] = Field(alias="$schema")
    description: Annotated[str, Field(min_length=1)]
    cases: Annotated[list[FieldValueCorpusCase], Field(min_length=1)]


type SchemaV2Document = (
    FieldDefinition
    | Capability
    | FieldChangeIntent
    | FieldChangePlan
    | ApplyRequest
    | ApplyReceipt
    | SchemaSnapshot
    | TableCreateIntent
    | TableCreateReceipt
    | TableSettingsIntent
    | TableSettingsReceipt
    | FormulaValidateRequest
    | FormulaPreviewRequest
    | MigrationStatus
    | FieldSettingsDescribeResult
    | FieldRecycleBinResult
    | FieldValueEntryCorpus
)
