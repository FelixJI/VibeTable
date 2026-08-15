"""C1 data import/export contracts.

These contracts describe product import (preview + apply) and export
processes that turn Excel/CSV files into table rows and back, with
schema-driven column mapping, relation lookup, and one atomic write.

Import process (two-phase, like B2 paste):

* **preview** (zero writes) — the host submits a path grant (file the WPF picker
  chose), the target collection, the schema revision it rendered against, and an
  optional explicit column mapping. The service reads the file (streaming for
  large workbooks), normalizes each cell using the collection's capability
  profile (date/currency/enum/relation), and returns an :class:`ImportPlan`
  describing exactly what will be created plus a single-use token.
* **apply** (writes) — the host submits the token + an import mode (create-only
  or upsert-by-approved-key). Every accepted row is submitted in one frozen
  MutationKernel request with one idempotency key.

Export process:

* **export** — the host submits a path grant (export target) + a query. The
  service streams the full result set (not just the current page) to CSV/XLSX,
  honouring field/row permissions, and reports progress via the task runtime.
"""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator
from pydantic.alias_generators import to_camel


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    model_config = _camel_config()


# ---------------------------------------------------------------------------
# Import preview
# ---------------------------------------------------------------------------


#: The import mode. ``create_only`` always inserts new rows; ``upsert`` updates
#: existing rows matched by an approved unique key (default).
ImportMode = Literal["create_only", "upsert"]

#: Severity for an import diagnostic. ``error`` blocks apply; ``warning``
#: requires explicit acknowledgement.
ImportDiagnosticSeverity = Literal["error", "warning"]

#: MutationKernel accepts at most 1,000 operations in one atomic request.  The
#: preview contract uses the same ceiling so every successful plan is
#: guaranteed to fit the authoritative apply boundary.
MAX_ATOMIC_IMPORT_ROWS: int = 1_000


class ImportColumnMapping(CamelModel):
    """An explicit user mapping from a source column to a collection field.

    ``source_column`` is the header in the file; ``target_field`` is the
    collection field name. Relation columns never guess a display field: callers
    must provide both the stable product ``relation_id`` and a ``match_field``
    whose uniqueness has been established from the live schema.

    """

    source_column: str = Field(min_length=1, max_length=128)
    target_field: str = Field(min_length=1, max_length=128)
    relation_id: str | None = Field(default=None, min_length=1, max_length=128)
    match_field: str | None = Field(default=None, min_length=1, max_length=128)

    @model_validator(mode="after")
    def validate_relation_mapping_pair(self) -> ImportColumnMapping:
        if (self.relation_id is None) != (self.match_field is None):
            raise ValueError("relationId and matchField must be provided together")
        return self


class PreviewImportParams(CamelModel):
    """Parameters for ``data.previewImport``.

    Wire form::

        {"grantId": "grant-1", "collection": "vibetable_demo",
         "schemaRevision": "...", "mode": "create_only",
         "columnMapping": [{"sourceColumn": "合同编号", "targetField": "number"}]}

    ``grant_id`` is the import-source path grant the WPF picker issued.
    ``column_mapping`` is optional; when omitted the service auto-matches by
    field key and display name.
    """

    grant_id: str = Field(min_length=1, max_length=128)
    collection: str = Field(min_length=1, max_length=128)
    schema_revision: str = Field(min_length=1)
    mode: ImportMode = "create_only"
    upsert_key: str | None = Field(default=None, max_length=128)
    column_mapping: list[ImportColumnMapping] = Field(default_factory=list, max_length=256)


ImportRelationResolutionState = Literal["matched"]


class ImportRelationResolution(CamelModel):
    """Previewed exact-match relation resolution retained for atomic apply.

    The first PocketBase release deliberately supports linking existing target
    records only. Creating records in another table is a separate product
    mutation and is not advertised by the import contract.
    """

    target_field: str = Field(min_length=1, max_length=128)
    relation_id: str = Field(min_length=1, max_length=128)
    match_field: str = Field(min_length=1, max_length=128)
    source_value: Any
    state: ImportRelationResolutionState
    matched_primary_key: Any = None


class ImportCellDiagnostic(CamelModel):
    """A localized diagnostic for one import cell.

    ``sheet``/``row``/``column`` locate the source cell (row/column are 1-based
    in the file; sheet is the workbook sheet name or ``""`` for CSV).
    """

    sheet: str = Field(default="", max_length=128)
    row: int = Field(ge=1)
    column: int = Field(ge=1)
    severity: ImportDiagnosticSeverity
    code: str = Field(min_length=1, max_length=64)
    message: str = Field(min_length=1, max_length=512)
    original_value: str = Field(default="")


class ImportPlanRow(CamelModel):
    """One planned row from the import preview.

    ``values`` are the normalized field→value pairs ready to write.
    ``diagnostics`` carry any localized errors/warnings for this row.
    """

    source_row: int = Field(ge=1)
    values: dict[str, Any] = Field(default_factory=dict)
    diagnostics: list[ImportCellDiagnostic] = Field(default_factory=list)
    relation_resolutions: list[ImportRelationResolution] = Field(default_factory=list)


class ImportSummary(CamelModel):
    """Aggregated counts for an :class:`ImportPlan`."""

    total_rows: int = Field(ge=0)
    valid_rows: int = Field(ge=0)
    error_rows: int = Field(ge=0)
    warning_rows: int = Field(ge=0)
    error_count: int = Field(ge=0)
    warning_count: int = Field(ge=0)


class ImportPreviewToken(CamelModel):
    """An opaque, single-use token authorizing one import apply."""

    token: str = Field(min_length=1, max_length=2048)
    expires_at: float
    consumed: bool = False


class ImportPlan(CamelModel):
    """Result of ``data.previewImport``.

    ``source_hash`` binds the plan to the exact file bytes; ``capability_hash``
    binds it to the schema/permission version. The host must re-preview if
    either changes. ``unmatched_columns`` lists source columns that could not be
    auto-mapped (the user must map them explicitly or ignore them).
    """

    collection: str = Field(min_length=1, max_length=128)
    schema_revision: str = Field(min_length=1)
    capability_hash: str = Field(min_length=1)
    source_hash: str = Field(min_length=1)
    summary: ImportSummary
    rows: list[ImportPlanRow] = Field(
        default_factory=list,
        max_length=MAX_ATOMIC_IMPORT_ROWS,
    )
    unmatched_columns: list[str] = Field(default_factory=list, max_length=256)
    diagnostics: list[ImportCellDiagnostic] = Field(default_factory=list)
    token: ImportPreviewToken


class ApplyImportParams(CamelModel):
    """Parameters for ``data.applyImport``.

    Wire form::

        {"grantId": "grant-1", "collection": "...", "token": "...",
         "mode": "create_only", "idempotencyPrefix": "imp-uuid"}

    ``idempotency_prefix`` binds the single atomic request so retries are
    deduplicated.
    """

    grant_id: str = Field(min_length=1, max_length=128)
    collection: str = Field(min_length=1, max_length=128)
    token: str = Field(min_length=1, max_length=2048)
    mode: ImportMode = "create_only"
    idempotency_prefix: str = Field(default="", max_length=128)


class ImportChunkResult(CamelModel):
    """The result of one import chunk (reported via task progress)."""

    chunk_index: int = Field(ge=0)
    created_row_keys: list[str] = Field(default_factory=list)
    updated_row_keys: list[str] = Field(default_factory=list)
    failed_rows: list[int] = Field(default_factory=list)
    idempotency_key: str = Field(default="", max_length=128)


class ApplyImportResult(CamelModel):
    """Final result of ``data.applyImport``.

    ``committed`` counts are server-confirmed for the single whole-file
    transaction. ``failed_rows`` lists source row numbers rejected before or
    during that transaction; a failed apply commits zero rows.
    """

    collection: str = Field(min_length=1, max_length=128)
    created_count: int = Field(ge=0)
    updated_count: int = Field(ge=0)
    failed_rows: list[int] = Field(default_factory=list)
    chunks: list[ImportChunkResult] = Field(default_factory=list)
    request_ids: list[str] = Field(default_factory=list)


# ---------------------------------------------------------------------------
# Export
# ---------------------------------------------------------------------------


ExportFormat = Literal["csv", "xlsx"]


class ExportParams(CamelModel):
    """Parameters for ``data.export``.

    Wire form::

        {"grantId": "grant-1", "collection": "vibetable_demo",
         "query": {...}, "format": "csv", "includeRelations": true}

    ``grant_id`` is the export-target path grant (write direction). ``query`` is
    the full B4 query AST; the export covers ALL matching rows (not just the
    current page) via stable paging. ``include_relations`` adds declared
    relation display fields as extra columns.
    """

    grant_id: str = Field(min_length=1, max_length=128)
    collection: str = Field(min_length=1, max_length=128)
    query: dict[str, Any] = Field(default_factory=dict)
    format: ExportFormat = "csv"
    include_relations: bool = False
    lookup_ids: list[str] = Field(default_factory=list, max_length=256)
    lookup_revision: str | None = Field(default=None, min_length=1, max_length=128)

    @model_validator(mode="after")
    def require_lookup_revision(self) -> ExportParams:
        if self.lookup_ids and self.lookup_revision is None:
            raise ValueError("lookupRevision is required when lookupIds are requested")
        if len(self.lookup_ids) != len(set(self.lookup_ids)):
            raise ValueError("lookupIds must be unique")
        return self


class ExportResult(CamelModel):
    """Result of ``data.export``.

    ``rows_written`` is the confirmed count. ``schema_revision`` /
    ``capability_hash`` record what was exported so the user can detect drift.
    """

    collection: str = Field(min_length=1, max_length=128)
    format: ExportFormat
    rows_written: int = Field(ge=0)
    schema_revision: str = Field(default="", max_length=128)
    capability_hash: str = Field(default="", max_length=128)
    output_display_name: str = Field(default="", max_length=256)


# ---------------------------------------------------------------------------
# Template generation
# ---------------------------------------------------------------------------


class GenerateTemplateParams(CamelModel):
    """Parameters for ``data.generateTemplate``.

    Wire form::

        {"collection": "vibetable_demo", "format": "xlsx"}

    Produces a download-ready template with column names, required hints, enum
    options and relation notes derived from the current product schema.
    """

    collection: str = Field(min_length=1, max_length=128)
    grant_id: str = Field(min_length=1, max_length=128)
    format: ExportFormat = "xlsx"


class TemplateResult(CamelModel):
    """Result of ``data.generateTemplate``.

    The template is written to a host-picked export target grant; the result
    carries the display name for the UI.
    """

    collection: str = Field(min_length=1, max_length=128)
    grant_id: str = Field(min_length=1, max_length=128)
    display_name: str = Field(default="", max_length=256)


__all__ = [
    "MAX_ATOMIC_IMPORT_ROWS",
    "ApplyImportParams",
    "ApplyImportResult",
    "CamelModel",
    "ExportFormat",
    "ExportParams",
    "ExportResult",
    "GenerateTemplateParams",
    "ImportCellDiagnostic",
    "ImportChunkResult",
    "ImportColumnMapping",
    "ImportDiagnosticSeverity",
    "ImportMode",
    "ImportPlan",
    "ImportPlanRow",
    "ImportPreviewToken",
    "ImportRelationResolution",
    "ImportRelationResolutionState",
    "ImportSummary",
    "PreviewImportParams",
    "TemplateResult",
]
