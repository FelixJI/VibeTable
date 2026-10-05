"""Read-only projections of Go source migrations; Python executes no migrations."""

from typing import Literal

from pydantic import Field, model_validator

from backend.contracts.task import CamelModel


class SourceImportKey(CamelModel):
    provider: str
    container_id: str
    table_id: str
    field_id: str = ""
    record_id: str = ""
    object_id: str = ""


class SourceImportWindow(CamelModel):
    started_at: str
    finished_at: str
    consistency: Literal["snapshot", "window"]


class SourceImportMapping(CamelModel):
    source: SourceImportKey
    kind: Literal["table", "field", "record"]
    local_id: str
    table_id: str
    name: str = ""
    collection: str = ""


class SourceImportBatch(CamelModel):
    job_id: str
    batch_id: str
    stage: str
    table_id: str
    mappings: list[SourceImportMapping]
    created: int = Field(ge=0)
    relation_writes: int = Field(ge=0)
    attachment_writes: int = Field(ge=0)
    schema_revisions: dict[str, str]


class SourceImportTarget(CamelModel):
    source_table_id: str
    table_id: str
    name: str
    collection: str


class SourceImportDiagnostic(CamelModel):
    code: str
    table_id: str = ""
    field_id: str = ""
    record_id: str = ""
    message: str
    blocking: bool


class SourceImportFieldSummary(CamelModel):
    source: SourceImportKey
    kind: str
    policy: Literal["native", "snapshot", "skip", "blocked"]
    definition: str


class SourceImportResult(CamelModel):
    contract: Literal["vibetable.source-import.v1"]
    job_id: str = Field(min_length=1, max_length=128)
    provider: str = Field(min_length=1)
    container_id: str = Field(min_length=1)
    source_name: str
    state: Literal[
        "queued", "running", "interrupted", "succeeded", "failed", "cancelled", "unknown", "aborted"
    ]
    stage: str
    created: int = Field(ge=0)
    total: int = Field(ge=0)
    not_submitted: int = Field(ge=0)
    unknown_records: int = Field(ge=0)
    unknown_batch: str = ""
    targets: list[SourceImportTarget]
    batches: list[SourceImportBatch]
    diagnostics: list[SourceImportDiagnostic]
    started_at: str
    finished_at: str = ""
    session_epoch: int = Field(ge=1)
    read_window: SourceImportWindow
    fields: list[SourceImportFieldSummary]

    @model_validator(mode="after")
    def validate_counts(self) -> SourceImportResult:
        if self.created + self.not_submitted + self.unknown_records != self.total:
            raise ValueError(
                "Committed, pending and unknown record counts must partition the source"
            )
        if self.unknown_records and not self.unknown_batch:
            raise ValueError("Unknown records require an unresolved batch")
        if self.state == "succeeded" and (
            self.not_submitted
            or self.unknown_records
            or self.unknown_batch
            or self.stage != "settled"
            or not self.finished_at
        ):
            raise ValueError("Migration success requires all stages to settle")
        return self
