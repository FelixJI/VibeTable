"""Strict Python boundary models for the isolated Schema v2 domain."""

from __future__ import annotations

from typing import Any, Literal, Self

from pydantic import BaseModel, ConfigDict, Field, model_validator


def _to_camel(value: str) -> str:
    head, *tail = value.split("_")
    return head + "".join(part.capitalize() for part in tail)


class SchemaV2Model(BaseModel):
    """Closed model with contract wire aliases and strict scalar decoding."""

    model_config = ConfigDict(
        alias_generator=_to_camel,
        extra="forbid",
        populate_by_name=True,
        strict=True,
    )


LogicalType = Literal[
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


class FieldIdentityV2(SchemaV2Model):
    field_id: str
    physical_name: str
    provider_field_id: str


class LifecycleV2(SchemaV2Model):
    state: Literal["active", "retired"]
    retired_at: str | None


class DefaultSpecV2(SchemaV2Model):
    enabled: bool
    value: Any
    source: Literal["recommended", "user"]
    defaults_version: int = Field(ge=1)


class PresenceSpecV2(SchemaV2Model):
    mode: Literal["companion", "native", "computed"]
    provider_field_id: str = ""
    physical_name: str = ""


class ValueSpecV2(SchemaV2Model):
    required: bool
    default: DefaultSpecV2
    presence: PresenceSpecV2


class UniqueSpecV2(SchemaV2Model):
    enabled: bool
    blank_policy: Literal["ignoreMissing"]


class RangeSpecV2(SchemaV2Model):
    min: float | str | None
    max: float | str | None


class LengthSpecV2(SchemaV2Model):
    min: int | None = Field(ge=0)
    max: int | None = Field(ge=0)


class PatternSpecV2(SchemaV2Model):
    enabled: bool
    value: str


class DomainSpecV2(SchemaV2Model):
    only: list[str]
    except_: list[str] = Field(alias="except")


class SelectionSpecV2(SchemaV2Model):
    min: int = Field(ge=0)
    max: int | None = Field(ge=0)


class ConstraintSpecV2(SchemaV2Model):
    unique: UniqueSpecV2
    range: RangeSpecV2
    length: LengthSpecV2
    pattern: PatternSpecV2
    domains: DomainSpecV2
    selection: SelectionSpecV2


StorageKind = Literal[
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


class StorageOptionsV2(SchemaV2Model):
    only_int: bool
    max_size: int = Field(ge=0)
    convert_urls: bool = Field(alias="convertURLs")
    presentable: bool


class StorageSpecV2(SchemaV2Model):
    kind: StorageKind
    options: StorageOptionsV2


DisplayKind = Literal[
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


class DisplaySpecV2(SchemaV2Model):
    kind: DisplayKind
    preset: str
    display_scale: int = Field(ge=0, le=15)
    scale_mode: Literal["max", "fixed"]
    trim_trailing_zeros: bool
    use_grouping: bool
    currency: str
    percent_storage: Literal["ratio", "percent"]
    unit: str | None
    precision: Literal["exact", "day", "minute", "second", "millisecond"]
    timezone: str
    mode: str
    indent: Literal[0, 2, 4] = 0
    true_label: str
    false_label: str


class SelectOptionV2(SchemaV2Model):
    option_id: str
    label: str
    color: str
    order: int
    state: Literal["active", "retired"]


class SelectSpecV2(SchemaV2Model):
    options: list[SelectOptionV2]


class RelationSpecV2(SchemaV2Model):
    target_table_id: str
    cardinality: Literal["one", "many"]
    delete_policy: Literal["setNull", "restrict", "cascade"]
    display_field_id: str


class FileSpecV2(SchemaV2Model):
    max_files: int = Field(ge=1)
    max_bytes_per_file: int = Field(ge=1)
    allowed_mime_types: list[str] = Field(alias="allowedMimeTypes")
    thumbs: list[str]
    protected: bool


class JsonSpecV2(SchemaV2Model):
    root_type: Literal["any", "object", "array", "string", "number", "boolean", "null"]
    max_size: int = Field(ge=1)
    schema_: dict[str, Any] = Field(alias="schema")


class AutoDateSpecV2(SchemaV2Model):
    role: Literal["createdAt", "updatedAt"]


class FormulaSpecV2(SchemaV2Model):
    language: Literal["cel-v1"]
    source: str
    result_type: LogicalType


class LookupSpecV2(SchemaV2Model):
    relation_field_id: str
    target_field_id: str
    aggregate: Literal["none", "first", "distinct", "count", "sum", "avg", "min", "max"]
    result_type: LogicalType


class FieldDefinitionV2(SchemaV2Model):
    contract: Literal["vibetable.schema.v2"]
    identity: FieldIdentityV2
    display_name: str
    help: str
    logical_type: LogicalType
    lifecycle: LifecycleV2
    value: ValueSpecV2
    constraints: ConstraintSpecV2
    storage: StorageSpecV2
    display: DisplaySpecV2
    select: SelectSpecV2 | None = None
    relation: RelationSpecV2 | None = None
    file: FileSpecV2 | None = None
    json_: JsonSpecV2 | None = Field(default=None, alias="json")
    auto_date: AutoDateSpecV2 | None = None
    formula: FormulaSpecV2 | None = None
    lookup: LookupSpecV2 | None = None

    @model_validator(mode="after")
    def validate_discriminated_settings(self) -> Self:
        _validate_type_settings(self)
        return self


class FieldDraftV2(SchemaV2Model):
    display_name: str
    help: str
    logical_type: LogicalType
    value: ValueSpecV2
    constraints: ConstraintSpecV2
    storage: StorageSpecV2
    display: DisplaySpecV2
    select: SelectSpecV2 | None = None
    relation: RelationSpecV2 | None = None
    file: FileSpecV2 | None = None
    json_: JsonSpecV2 | None = Field(default=None, alias="json")
    auto_date: AutoDateSpecV2 | None = None
    formula: FormulaSpecV2 | None = None
    lookup: LookupSpecV2 | None = None

    @model_validator(mode="after")
    def validate_discriminated_settings(self) -> Self:
        _validate_type_settings(self)
        return self


def _validate_type_settings(value: FieldDefinitionV2 | FieldDraftV2) -> None:
    expected = {
        "select": "select",
        "multiSelect": "select",
        "relation": "relation",
        "file": "file",
        "json": "json_",
        "autoDate": "auto_date",
        "formula": "formula",
        "lookup": "lookup",
    }.get(value.logical_type)
    settings = {
        "select": value.select,
        "relation": value.relation,
        "file": value.file,
        "json_": value.json_,
        "auto_date": value.auto_date,
        "formula": value.formula,
        "lookup": value.lookup,
    }
    if expected is not None and settings[expected] is None:
        raise ValueError(f"{expected} settings are required for {value.logical_type}")
    unexpected = [
        name for name, configured in settings.items() if configured is not None and name != expected
    ]
    if unexpected:
        raise ValueError(f"{unexpected[0]} settings are not allowed for {value.logical_type}")


class RecommendedValuesV2(SchemaV2Model):
    defaults_version: int = Field(ge=1)
    value: ValueSpecV2
    constraints: ConstraintSpecV2
    storage: StorageSpecV2
    display: DisplaySpecV2
    file: FileSpecV2 | None = None
    json_: JsonSpecV2 | None = Field(default=None, alias="json")


class CapabilityV2(SchemaV2Model):
    logical_type: LogicalType
    general_settings: list[str]
    advanced_settings: list[str]
    danger_settings: list[str]
    recommended: RecommendedValuesV2
    supports_required: bool
    supports_default: bool
    supports_unique: bool
    needs_presence: bool
    display_presets: list[str]
    conversion_targets: list[LogicalType]
    conversion_rules: list[str]
    compile_strategy: str
    user_creatable: bool


ChangeAction = Literal["create", "update", "retire", "restore", "purge", "convert", "backfill"]


class ActorV2(SchemaV2Model):
    id: str
    kind: str


class FieldChangeIntentV2(SchemaV2Model):
    action: ChangeAction
    table_id: str
    field_id: str
    expected_schema_revision: str
    expected_data_revision: int | None = Field(ge=0)
    draft: FieldDraftV2 | None
    actor: ActorV2
    conversion_rule: str
    confirmation: str
    backup_receipt: str


class DiagnosticV2(SchemaV2Model):
    code: str
    path: str
    message: str
    details: dict[str, Any]


class FailureSampleV2(SchemaV2Model):
    record_id: str
    reason: str


class DependencyRefV2(SchemaV2Model):
    kind: str
    id: str
    name: str


class ImpactV2(SchemaV2Model):
    records: int = Field(ge=0)
    missing: int = Field(ge=0)
    ambiguous: int = Field(ge=0)
    failures: list[FailureSampleV2]
    dependencies: list[DependencyRefV2]


class PlanStepV2(SchemaV2Model):
    kind: str
    details: dict[str, Any]


class FieldChangePlanV2(SchemaV2Model):
    contract: Literal["vibetable.schema.v2"]
    plan_id: str
    plan_hash: str
    expires_at: str
    intent: FieldChangeIntentV2
    before: FieldDefinitionV2 | None
    after: FieldDefinitionV2 | None
    classes: list[Literal["display", "metadata", "constraint", "schema", "migration", "danger"]]
    expected_schema_revision: str
    expected_data_revision: int | None = Field(ge=0)
    impact: ImpactV2
    steps: list[PlanStepV2]
    warnings: list[DiagnosticV2]
    errors: list[DiagnosticV2]
    confirmations: list[str]
    creates_migration: bool
    can_apply: bool


class ApplyReceiptV2(SchemaV2Model):
    contract: Literal["vibetable.schema.v2"]
    operation_id: str
    plan_id: str
    action: ChangeAction
    table_id: str
    field_id: str
    schema_revision: str
    definition: FieldDefinitionV2 | None
    migration_job_id: str


class ApplyRequestV2(SchemaV2Model):
    plan_id: str
    plan_hash: str
    operation_id: str
    actor: ActorV2
    confirmations: list[str]
    protection_snapshot_id: str = ""


class MigrationStatusV2(SchemaV2Model):
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
    processed: int = Field(ge=0)
    total: int = Field(ge=0)
    can_cancel: bool
    error: DiagnosticV2 | None
    updated_at: str


class FieldSettingsDescribeResultV2(SchemaV2Model):
    contract: Literal["vibetable.schema.v2"]
    table_id: str
    field_id: str
    schema_revision: str
    data_revision: int = Field(ge=0)
    definition: FieldDefinitionV2 | None
    capabilities: list[CapabilityV2]
    recommended_defaults_version: int = Field(ge=1)


class FieldRecycleBinResultV2(SchemaV2Model):
    contract: Literal["vibetable.schema.v2"]
    fields: list[FieldDefinitionV2]
