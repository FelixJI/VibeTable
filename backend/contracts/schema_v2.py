"""Schema V2 semantic validators over JSON-Schema-generated wire DTOs."""

from __future__ import annotations

from typing import Self

from pydantic import model_validator

from backend.contracts import generated_schema_v2 as wire

SchemaV2Model = wire.SchemaV2WireModel
LogicalType = wire.LogicalType
FieldIdentityV2 = wire.FieldIdentity
LifecycleV2 = wire.Lifecycle
DefaultSpecV2 = wire.DefaultSpec
PresenceSpecV2 = wire.PresenceSpec
ValueSpecV2 = wire.ValueSpec
UniqueSpecV2 = wire.UniqueSpec
RangeSpecV2 = wire.RangeSpec
LengthSpecV2 = wire.LengthSpec
PatternSpecV2 = wire.PatternSpec
DomainSpecV2 = wire.DomainSpec
SelectionSpecV2 = wire.SelectionSpec
ConstraintSpecV2 = wire.ConstraintSpec
StorageOptionsV2 = wire.StorageOptions
StorageSpecV2 = wire.StorageSpec
DisplaySpecV2 = wire.DisplaySpec
SelectOptionV2 = wire.SelectOption
SelectSpecV2 = wire.SelectSpec
SelectOptionDraftV2 = wire.SelectOptionDraft
SelectDraftSpecV2 = wire.SelectDraftSpec
RelationSpecV2 = wire.RelationSpec
RelationPairDraftV2 = wire.RelationPairDraft
FileSpecV2 = wire.FileSpec
JsonSpecV2 = wire.JSONSpec
AutoDateSpecV2 = wire.AutoDateSpec
FormulaSpecV2 = wire.FormulaSpec
FormulaDraftSpecV2 = wire.FormulaDraftSpec
LookupPathStepV2 = wire.LookupPathStep
LookupSpecV2 = wire.LookupSpec
RecommendedValuesV2 = wire.RecommendedValues
CapabilityV2 = wire.Capability
SchemaSnapshotV2 = wire.SchemaSnapshot
FormulaValidateRequestV2 = wire.FormulaValidateRequest
FormulaPreviewRequestV2 = wire.FormulaPreviewRequest
TableCreateIntentV2 = wire.TableCreateIntent
TableCreateReceiptV2 = wire.TableCreateReceipt
ArchivePolicyV2 = wire.ArchivePolicy
TableSettingsIntentV2 = wire.TableSettingsIntent
TableSettingsReceiptV2 = wire.TableSettingsReceipt
ActorV2 = wire.Actor
RelatedFieldChangeV2 = wire.RelatedFieldChange
DiagnosticV2 = wire.Diagnostic
FailureSampleV2 = wire.FailureSample
DependencyRefV2 = wire.DependencyRef
ImpactV2 = wire.Impact
PlanStepV2 = wire.PlanStep
RelatedApplyReceiptV2 = wire.RelatedApplyReceipt
ApplyRequestV2 = wire.ApplyRequest
MigrationStatusV2 = wire.MigrationStatus


def _validate_field_settings(value: wire.FieldDefinition | wire.FieldDraft) -> None:
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
    relation = value.relation
    if relation is not None and bool(relation.pair_id) != bool(relation.reciprocal_field_id):
        raise ValueError("pairId and reciprocalFieldId must be configured together")


def _validate_intent(value: wire.FieldChangeIntent) -> None:
    if value.draft is not None:
        _validate_field_settings(value.draft)


def _validate_definition(value: wire.FieldDefinition | None) -> None:
    if value is not None:
        _validate_field_settings(value)


class FieldDefinitionV2(wire.FieldDefinition):
    @model_validator(mode="after")
    def validate_semantics(self) -> Self:
        _validate_field_settings(self)
        return self


class FieldDraftV2(wire.FieldDraft):
    @model_validator(mode="after")
    def validate_semantics(self) -> Self:
        _validate_field_settings(self)
        return self


class FieldChangeIntentV2(wire.FieldChangeIntent):
    @model_validator(mode="after")
    def validate_semantics(self) -> Self:
        _validate_intent(self)
        return self


class FieldChangePlanV2(wire.FieldChangePlan):
    @model_validator(mode="after")
    def validate_semantics(self) -> Self:
        _validate_intent(self.intent)
        _validate_definition(self.before)
        _validate_definition(self.after)
        for related in self.related_changes or ():
            _validate_definition(related.before)
            _validate_definition(related.after)
        return self


class ApplyReceiptV2(wire.ApplyReceipt):
    @model_validator(mode="after")
    def validate_semantics(self) -> Self:
        _validate_definition(self.definition)
        for related in self.related or ():
            _validate_definition(related.definition)
        return self


class FieldSettingsDescribeResultV2(wire.FieldSettingsDescribeResult):
    @model_validator(mode="after")
    def validate_semantics(self) -> Self:
        _validate_definition(self.definition)
        return self


class FieldRecycleBinResultV2(wire.FieldRecycleBinResult):
    @model_validator(mode="after")
    def validate_semantics(self) -> Self:
        for definition in self.fields:
            _validate_definition(definition)
        return self
