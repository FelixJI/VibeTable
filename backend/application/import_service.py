"""File import service: container parsing, preview and atomic apply.

Python preserves raw CSV/XLSX cell values, supplied state, mappings and source
coordinates. The Go import preview service and FieldValueKernel exclusively own
target-field conversion, defaults, blank semantics and constraints.

Process
----
* :meth:`preview` reads the granted file (streaming for large workbooks),
  auto-maps or applies the explicit column mapping, resolves explicit relation
  lookups, delegates raw cells to the authoritative Go preview, and stores the
  final plan with the Go import plan owner, which returns a single-use token.
* :meth:`apply` claims the stored plan from the Go owner, submits every valid
  planned row in one frozen mutation request and settles the claim with the
  authoritative outcome. Cancellation is checked before submission; a rejected
  request therefore leaves zero rows committed.

The Qt/controller ``confirm_cb`` callback is gone: preview is zero-write and
returns the full plan; the host shows it and the user confirms via apply.
"""

from __future__ import annotations

import asyncio
import csv
import hashlib
import logging
import time
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from datetime import date, datetime, timedelta
from datetime import time as datetime_time
from pathlib import Path
from typing import Any, BinaryIO, Protocol

from backend.application.host_files import HostFiles, text_stream
from backend.application.paste_service import PasteMutationPort
from backend.contracts.data_io import (
    MAX_ATOMIC_IMPORT_ROWS,
    ApplyImportParams,
    ApplyImportResult,
    ImportCellDiagnostic,
    ImportChunkResult,
    ImportColumnMapping,
    ImportPlan,
    ImportPlanRow,
    ImportPreviewToken,
    ImportRelationResolution,
    ImportSummary,
    PreviewImportParams,
)
from backend.contracts.data_profile import CollectionProfile
from backend.contracts.paste import PastePlanRow

#: How long an import preview token remains valid (seconds). The Go import
#: plan owner enforces this frozen TTL; the value stays here as the public
#: compatibility contract documentation and must not drift or be extended.
IMPORT_TOKEN_TTL_SECONDS: float = 10 * 60.0

#: Compatibility default retained by the public contract. Apply is atomic.
DEFAULT_CHUNK_SIZE: int = 500


logger = logging.getLogger(__name__)


class ImportFlowError(Exception):
    """An import error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str, data: dict[str, Any] | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.data = data

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        exposed: dict[str, Any] = {"code": self.code}
        if self.data:
            exposed.update(self.data)
        return exposed


@dataclass(frozen=True)
class RelationImportTarget:
    """Live-schema proof for one explicit relation import mapping."""

    relation_id: str
    target_field: str
    target_collection: str
    target_primary_key: str
    match_field: str


@dataclass(frozen=True)
class RelationImportBatchResult:
    """Server-confirmed result of one atomic relation-aware import chunk."""

    created_row_keys: list[str]
    updated_row_keys: list[str]
    request_id: str = ""


class RelationImportProvider(Protocol):
    """Permission-scoped adapter for relation resolution and atomic apply.

    The implementation uses the current product session. ``inspect_mapping``
    must reject non-PK/non-unique match
    fields. ``apply_chunk`` owns one transaction containing any requested target
    creation and the source-row mutation, and deduplicates by ``idempotency_key``.
    """

    async def inspect_mapping(
        self,
        *,
        collection: str,
        target_field: str,
        relation_id: str,
        match_field: str,
    ) -> RelationImportTarget: ...

    async def find_exact(self, target: RelationImportTarget, value: Any) -> list[Any]: ...

    async def apply_chunk(
        self,
        *,
        collection: str,
        profile: CollectionProfile,
        rows: list[ImportPlanRow],
        mode: str,
        upsert_key: str | None,
        idempotency_key: str,
    ) -> RelationImportBatchResult: ...


class ImportMutationPort(PasteMutationPort, Protocol):
    """Mutation port with Go-owned raw-cell normalization for import preview.

    The plan lifecycle ports store the normalized plan with the Go import plan
    owner, which mints the single-use token and owns consumption, concurrent
    staging and the idempotency-prefix binding. Python only forwards calls.
    """

    async def preview_import(
        self,
        *,
        collection: str,
        schema_revision: str,
        rows: list[dict[str, Any]],
        row_modes: list[str] | None = None,
    ) -> dict[str, Any]: ...

    async def mint_import_plan(
        self,
        *,
        collection: str,
        grant_id: str,
        schema_revision: str,
        capability_hash: str,
        source_hash: str,
        rows: list[dict[str, Any]],
        mode: str,
        upsert_key: str | None,
    ) -> dict[str, Any]: ...

    async def stage_import_plan(
        self,
        *,
        token: str,
        grant_id: str,
        collection: str,
        mode: str,
        capability_hash: str,
    ) -> dict[str, Any]: ...

    async def bind_import_plan(
        self, *, token: str, idempotency_prefix: str, attempt: int
    ) -> dict[str, Any]: ...

    async def settle_import_plan(
        self, *, token: str, outcome: str, attempt: int
    ) -> dict[str, Any]: ...


# ---------------------------------------------------------------------------
# File reading (streaming, cancellation-aware)
# ---------------------------------------------------------------------------


class SourceFile:
    """A granted import source file, read lazily.

    Excel workbooks are read sheet-by-sheet; CSV files are read row-by-row. The
    reader never loads the entire file into memory at once.
    """

    def __init__(self, stream: BinaryIO, display_name: str) -> None:
        self._stream = stream
        self._display_name = display_name

    def read_header_and_rows(
        self,
        *,
        max_rows: int = MAX_ATOMIC_IMPORT_ROWS,
        sheet: str | None = None,
    ) -> tuple[list[str], list[list[Any]], str]:
        """Read the header row + data rows.

        Returns ``(header, rows, source_hash)``. For XLSX the first sheet (or the
        named ``sheet``) is used; for CSV the single stream is used. Files with
        more than ``max_rows`` data rows are rejected instead of truncated,
        because apply is one atomic mutation with the same fixed row limit.
        ``source_hash`` is the SHA-256 of the file bytes (binds the preview).
        """
        digest = hashlib.sha256()
        while block := self._stream.read(256 * 1024):
            digest.update(block)
        source_hash = digest.hexdigest()
        self._stream.seek(0)
        suffix = Path(self._display_name).suffix.lower()
        if suffix in (".xlsx", ".xlsm"):
            header, rows = self._read_xlsx(max_rows=max_rows, sheet=sheet)
            return header, rows, source_hash
        if suffix == ".csv":
            header, rows = self._read_csv(max_rows=max_rows)
            return header, rows, source_hash
        raise ImportFlowError(
            f"unsupported file type {suffix!r}",
            code="import_unsupported_format",
        )

    def _read_xlsx(self, *, max_rows: int, sheet: str | None) -> tuple[list[str], list[list[Any]]]:
        from openpyxl import load_workbook
        from openpyxl.styles.numbers import is_datetime
        from openpyxl.utils.datetime import CALENDAR_WINDOWS_1900

        wb = load_workbook(self._stream, read_only=True, data_only=True)
        try:
            ws = wb[sheet] if sheet and sheet in wb.sheetnames else wb.active
            if ws is None:
                raise ImportFlowError(
                    "workbook has no readable worksheet",
                    code="import_empty_workbook",
                )
            rows_iter = ws.iter_rows()
            try:
                header = [str(c.value) if c.value is not None else "" for c in next(rows_iter)]
            except StopIteration:
                return [], []
            try:
                data: list[list[Any]] = []
                for row in rows_iter:
                    if len(data) >= max_rows:
                        raise _import_row_limit(max_rows)
                    values: list[Any] = []
                    for cell in row:
                        value = cell.value
                        if isinstance(value, (datetime_time, timedelta)):
                            raise ImportFlowError(
                                "native Excel time/duration is unsupported; use ISO text",
                                code="import_unsupported_excel_time",
                                data={"sheet": ws.title, "row": cell.row, "column": cell.column},
                            )
                        if (
                            isinstance(value, date)
                            and wb.epoch == CALENDAR_WINDOWS_1900
                            and (value.date() if isinstance(value, datetime) else value)
                            == date(1900, 2, 28)
                        ):
                            # OpenPyXL maps both serial 59 and 60 to this date.
                            raise ImportFlowError(
                                "ambiguous native date in Excel's 1900 system; use ISO text",
                                code="import_ambiguous_excel_date",
                                data={"sheet": ws.title, "row": cell.row, "column": cell.column},
                            )
                        if isinstance(value, datetime):
                            if (
                                value.tzinfo is None
                                and value.time() == datetime_time()
                                and is_datetime(cell.number_format.lower()) == "date"
                            ):
                                value = value.date().isoformat()
                            else:
                                value = value.isoformat(sep=" " if value.tzinfo is None else "T")
                        elif isinstance(value, date):
                            value = value.isoformat()
                        values.append(value)
                    data.append(values)
                return header, data
            finally:
                rows_iter.close()
        finally:
            wb.close()

    def _read_csv(self, *, max_rows: int) -> tuple[list[str], list[list[Any]]]:
        with text_stream(self._stream) as fh:
            reader = csv.reader(fh)
            try:
                header = [str(c) for c in next(reader)]
            except StopIteration:
                return [], []
            data: list[list[Any]] = []
            for row in reader:
                if len(data) >= max_rows:
                    raise _import_row_limit(max_rows)
                data.append(list(row))
        return header, data


def _import_row_limit(max_rows: int) -> ImportFlowError:
    return ImportFlowError(
        f"import contains more than {max_rows} data rows",
        code="import_row_limit",
        data={"maxRows": max_rows},
    )


# ---------------------------------------------------------------------------
# Column mapping
# ---------------------------------------------------------------------------


def auto_map_columns(
    header: list[str],
    profile: CollectionProfile,
    explicit: list[ImportColumnMapping],
) -> tuple[dict[int, str], list[str]]:
    """Map source column indices → collection field names.

    Explicit mappings take precedence; remaining columns are auto-matched by
    field key (case-insensitive). Returns ``(mapping, unmatched_columns)``.
    """
    explicit_by_source = {m.source_column: m.target_field for m in explicit}
    create_fields = set(profile.create_fields)
    mapping: dict[int, str] = {}
    matched_sources: set[str] = set()
    for index, name in enumerate(header):
        clean = name.strip()
        if clean in explicit_by_source:
            target = explicit_by_source[clean]
            if target in create_fields:
                mapping[index] = target
                matched_sources.add(clean)
            continue
        lowered = clean.lower().replace(" ", "_")
        for field in profile.create_fields:
            if field.lower() == lowered:
                mapping[index] = field
                matched_sources.add(clean)
                break
    unmatched = [name for name in header if name.strip() not in matched_sources]
    return mapping, unmatched


# ---------------------------------------------------------------------------
# Import service
# ---------------------------------------------------------------------------


@dataclass
class _StagedImportPlan:
    """A plan fetched from the Go owner for one transient apply execution.

    The staged copy exists only inside :meth:`ImportService.apply`; the
    authoritative plan state (rows, bindings, consumption) stays with the Go
    import plan owner until the final settle call.
    """

    collection: str
    schema_revision: str
    mode: str
    upsert_key: str | None
    rows: list[ImportPlanRow]
    # Lease identity of the exclusive claim minted by the Go owner. Every
    # claim-scoped call (bind, settle) must echo it so a delayed request from
    # an earlier attempt cannot touch a newer claim on the same token.
    attempt: int


class ImportService:
    """Import preview and atomic apply over product-owned ports."""

    def __init__(
        self,
        *,
        client: Any,
        auth: Any,
        bulk: ImportMutationPort,
        profiles: dict[str, CollectionProfile],
        files: HostFiles,
        relation_provider: RelationImportProvider | None = None,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self._client = client
        self._auth = auth
        self._bulk = bulk
        self._profiles = profiles
        self._files = files
        self._relation_provider = relation_provider
        self._clock = clock
        # Serializes apply executions inside this worker process only. Plan
        # consumption, concurrency and idempotency binding are owned by the Go
        # import plan owner via the staged/settled lifecycle ports.
        self._apply_lock = asyncio.Lock()

    async def preview(self, params: PreviewImportParams) -> ImportPlan:
        profile = self._profile(params.collection)
        if params.schema_revision != profile.capability_hash:
            raise ImportFlowError(
                "schema changed since the grid was rendered",
                code="schema_mismatch",
                data={
                    "currentSchemaRevision": profile.capability_hash,
                    "expectedSchemaRevision": params.schema_revision,
                },
            )
        async with self._files.read(params.grant_id) as source:
            header, rows, source_hash = SourceFile(
                source.stream, source.display_name
            ).read_header_and_rows()
        mapping, unmatched = auto_map_columns(header, profile, params.column_mapping)
        relations = {r.field: r for r in profile.relations}
        explicit_by_source = {item.source_column.strip(): item for item in params.column_mapping}
        relation_targets: dict[int, RelationImportTarget] = {}
        relation_mapping_errors: dict[int, tuple[str, str]] = {}
        for col_index, field in mapping.items():
            relation = relations.get(field)
            explicit = explicit_by_source.get(header[col_index].strip())
            if relation is None:
                if explicit and (explicit.relation_id or explicit.match_field):
                    relation_mapping_errors[col_index] = (
                        "relation_mapping_not_relation",
                        f"field {field!r} is not a relation",
                    )
                continue
            if explicit is None or explicit.relation_id is None or explicit.match_field is None:
                relation_mapping_errors[col_index] = (
                    "relation_mapping_required",
                    "relation columns require explicit relationId and matchField",
                )
                continue
            if relation.relation_id is not None and relation.relation_id != explicit.relation_id:
                relation_mapping_errors[col_index] = (
                    "relation_id_mismatch",
                    "relationId does not identify the mapped target field",
                )
                continue
            if self._relation_provider is None:
                relation_mapping_errors[col_index] = (
                    "relation_provider_unavailable",
                    "relation import resolution is not configured",
                )
                continue
            try:
                relation_targets[col_index] = await self._relation_provider.inspect_mapping(
                    collection=params.collection,
                    target_field=field,
                    relation_id=explicit.relation_id,
                    match_field=explicit.match_field,
                )
            except Exception as exc:
                relation_mapping_errors[col_index] = (
                    getattr(exc, "code", "relation_mapping_invalid"),
                    str(exc),
                )
        plan_rows: list[ImportPlanRow] = []
        for row_offset, raw_row in enumerate(rows):
            source_row = row_offset + 2  # 1-based + header
            values: dict[str, Any] = {}
            diagnostics: list[ImportCellDiagnostic] = []
            relation_resolutions: list[ImportRelationResolution] = []
            for col_index, field in mapping.items():
                raw = raw_row[col_index] if col_index < len(raw_row) else None
                relation = relations.get(field)
                mapping_error = relation_mapping_errors.get(col_index)
                if mapping_error is not None and raw is not None and raw != "":
                    diagnostics.append(
                        ImportCellDiagnostic(
                            row=source_row,
                            column=col_index + 1,
                            severity="error",
                            code=mapping_error[0],
                            message=mapping_error[1],
                            original_value=str(raw),
                        )
                    )
                    continue
                if relation is not None:
                    if raw is None or raw == "":
                        values[field] = None
                        continue
                    target = relation_targets[col_index]
                    assert self._relation_provider is not None
                    try:
                        matches = await self._relation_provider.find_exact(target, raw)
                    except Exception as exc:
                        diagnostics.append(
                            ImportCellDiagnostic(
                                row=source_row,
                                column=col_index + 1,
                                severity="error",
                                code=getattr(exc, "code", "relation_lookup_failed"),
                                message=str(exc),
                                original_value=str(raw),
                            )
                        )
                        continue
                    if len(matches) == 1:
                        values[field] = matches[0]
                        relation_resolutions.append(
                            ImportRelationResolution(
                                target_field=field,
                                relation_id=target.relation_id,
                                match_field=target.match_field,
                                source_value=raw,
                                state="matched",
                                matched_primary_key=matches[0],
                            )
                        )
                    elif len(matches) > 1:
                        diagnostics.append(
                            ImportCellDiagnostic(
                                row=source_row,
                                column=col_index + 1,
                                severity="error",
                                code="relation_match_ambiguous",
                                message=(
                                    f"exact match returned {len(matches)} target records; "
                                    "matchField must resolve to one record"
                                ),
                                original_value=str(raw),
                            )
                        )
                    else:
                        diagnostics.append(
                            ImportCellDiagnostic(
                                row=source_row,
                                column=col_index + 1,
                                severity="error",
                                code="relation_match_not_found",
                                message="exact match returned no target record",
                                original_value=str(raw),
                            )
                        )
                    continue
                # Preserve the exact parsed container value and key presence.
                # Target-field conversion and validation are owned by the Go
                # import preview service and FieldValueKernel.
                values[field] = raw
            plan_rows.append(
                ImportPlanRow(
                    source_row=source_row,
                    values=values,
                    diagnostics=diagnostics,
                    relation_resolutions=relation_resolutions,
                )
            )
        authoritative = await self._bulk.preview_import(
            collection=params.collection,
            schema_revision=params.schema_revision,
            rows=[row.values for row in plan_rows],
        )
        normalized_rows = authoritative.get("rows")
        if not isinstance(normalized_rows, list) or len(normalized_rows) != len(plan_rows):
            raise ImportFlowError(
                "invalid authoritative import preview",
                code="import_preview_invalid",
            )
        source_column_by_field = {field: index for index, field in mapping.items()}
        for row_index, authoritative_row in enumerate(normalized_rows):
            if not isinstance(authoritative_row, dict):
                raise ImportFlowError(
                    "invalid authoritative import preview row",
                    code="import_preview_invalid",
                )
            normalized_values = authoritative_row.get("values")
            raw_diagnostics = authoritative_row.get("diagnostics")
            if not isinstance(normalized_values, dict) or not isinstance(raw_diagnostics, list):
                raise ImportFlowError(
                    "invalid authoritative import preview row",
                    code="import_preview_invalid",
                )
            current = plan_rows[row_index]
            current.values = normalized_values
            for diagnostic in raw_diagnostics:
                if not isinstance(diagnostic, dict):
                    raise ImportFlowError(
                        "invalid authoritative import diagnostic",
                        code="import_preview_invalid",
                    )
                raw_field = diagnostic.get("field")
                field = raw_field if isinstance(raw_field, str) else ""
                column_index = source_column_by_field.get(field, 0)
                raw_row = rows[row_index]
                original = raw_row[column_index] if column_index < len(raw_row) else None
                current.diagnostics.append(
                    ImportCellDiagnostic(
                        row=current.source_row,
                        column=column_index + 1,
                        severity="error",
                        code=str(diagnostic.get("code") or "field.value.invalid"),
                        message=str(diagnostic.get("message") or "invalid field value"),
                        original_value="" if original is None else str(original),
                    )
                )
        error_rows = sum(
            any(item.severity == "error" for item in row.diagnostics) for row in plan_rows
        )
        warning_rows = sum(
            any(item.severity == "warning" for item in row.diagnostics) for row in plan_rows
        )
        error_count = sum(item.severity == "error" for row in plan_rows for item in row.diagnostics)
        warning_count = sum(
            item.severity == "warning" for row in plan_rows for item in row.diagnostics
        )
        summary = ImportSummary(
            total_rows=len(rows),
            valid_rows=len(rows) - error_rows,
            error_rows=error_rows,
            warning_rows=warning_rows,
            error_count=error_count,
            warning_count=warning_count,
        )
        token = await self._mint_plan(
            collection=params.collection,
            grant_id=params.grant_id,
            schema_revision=params.schema_revision,
            capability_hash=profile.capability_hash,
            source_hash=source_hash,
            rows=plan_rows,
            mode=params.mode,
            upsert_key=params.upsert_key,
        )
        return ImportPlan(
            collection=params.collection,
            schema_revision=params.schema_revision,
            capability_hash=profile.capability_hash,
            source_hash=source_hash,
            summary=summary,
            rows=plan_rows,
            source_columns=header,
            unmatched_columns=unmatched,
            token=token,
        )

    async def apply(
        self,
        params: ApplyImportParams,
        *,
        progress: Callable[[int, int, str], Awaitable[None]] | None = None,
        cancelled: Callable[[], bool] | None = None,
    ) -> ApplyImportResult:
        async with self._apply_lock:
            return await self._apply_serialized(
                params,
                progress=progress,
                cancelled=cancelled,
            )

    async def _apply_serialized(
        self,
        params: ApplyImportParams,
        *,
        progress: Callable[[int, int, str], Awaitable[None]] | None = None,
        cancelled: Callable[[], bool] | None = None,
    ) -> ApplyImportResult:
        profile = self._profile(params.collection)
        staged = await self._stage_plan(params, profile)
        reserved = False
        try:
            # Construction is inside the guarded region: even a HostFiles
            # adapter that fails while building the reservation must release
            # the staged claim instead of leaving it in flight.
            reservation = self._files.reserve_import(params.grant_id, params.token)
            async with reservation as commit_grant:
                reserved = True
                return await self._apply_reserved(
                    params,
                    staged,
                    profile,
                    commit_grant,
                    progress=progress,
                    cancelled=cancelled,
                )
        finally:
            if not reserved:
                # The Host never admitted the apply; release the staged claim so
                # an admitted retry is not blocked by a dead reservation.
                await self._settle_quietly(params.token, "rejected", staged.attempt)

    async def _apply_reserved(
        self,
        params: ApplyImportParams,
        staged: _StagedImportPlan,
        profile: CollectionProfile,
        commit_grant: Callable[[], None],
        *,
        progress: Callable[[int, int, str], Awaitable[None]] | None,
        cancelled: Callable[[], bool] | None,
    ) -> ApplyImportResult:
        valid_rows = [
            r for r in staged.rows if not any(d.severity == "error" for d in r.diagnostics)
        ]
        total = len(valid_rows)
        if cancelled and cancelled():
            await self._settle_quietly(params.token, "rejected", staged.attempt)
            raise asyncio.CancelledError
        requested_prefix = params.idempotency_prefix or (
            "imp-" + hashlib.sha256(params.token.encode("utf-8")).hexdigest()[:16]
        )
        try:
            await self._bind_prefix(params.token, requested_prefix, staged.attempt)
        except (Exception, asyncio.CancelledError):
            # Bind precedes any business submission, so a transport failure or
            # hard cancellation here is a clean rejection, never an unknown
            # outcome: release the claim and re-raise unchanged.
            await self._settle_quietly(params.token, "rejected", staged.attempt)
            raise
        prefix = requested_prefix
        idempotency_key = f"{prefix}-0"
        bulk_rows = [
            PastePlanRow(
                kind="insert",
                changes={
                    field: {"before": None, "after": value} for field, value in row.values.items()
                },
            )
            for row in valid_rows
        ]
        requires_cross_table = staged.mode == "upsert" or any(
            resolution.state == "create"
            for row in valid_rows
            for resolution in row.relation_resolutions
        )
        submitted = False
        try:
            if requires_cross_table:
                if self._relation_provider is None:
                    raise ImportFlowError(
                        "atomic relation/upsert import is not configured",
                        code="relation_provider_unavailable",
                    )
                submitted = True
                relation_result = await self._relation_provider.apply_chunk(
                    collection=params.collection,
                    profile=profile,
                    rows=valid_rows,
                    mode=staged.mode,
                    upsert_key=staged.upsert_key,
                    idempotency_key=idempotency_key,
                )
                created_keys = relation_result.created_row_keys
                updated_keys = relation_result.updated_row_keys
                request_id = relation_result.request_id
            else:
                submitted = True
                result = await self._bulk.apply(
                    collection=params.collection,
                    profile=profile,
                    rows=bulk_rows,
                    row_revisions={},
                    idempotency_key=idempotency_key,
                    schema_revision=staged.schema_revision,
                )
                if result.outcome == "pending":
                    raise ImportFlowError(
                        "import outcome is pending; retry with the same token",
                        code="import_pending",
                    )
                if result.outcome != "committed":
                    raise ImportFlowError(
                        "import conflicted; preview again",
                        code="import_conflict",
                    )
                created_keys = [str(key) for key in result.created_row_keys]
                updated_keys = [str(key) for key in result.updated_row_keys]
                request_id = result.request_id
        except asyncio.CancelledError:
            # A hard cancellation after submission leaves the outcome unknown;
            # before submission the plan is cleanly reusable. Either way the
            # Go owner decides on the next staged attempt — nothing replays here.
            await self._settle_quietly(
                params.token, "unknown" if submitted else "rejected", staged.attempt
            )
            raise
        except Exception as exc:
            known_rejection = getattr(exc, "code", None) in {
                "import_conflict",
                "import_upsert_key_missing",
                "import_upsert_key_not_unique",
                "mutation.validation.failed",
            }
            # Settle before notifying: a raising progress callback must not
            # leave the claim in flight (its error still propagates unchanged,
            # matching the frozen behavior for that edge).
            if submitted and not known_rejection:
                await self._settle_quietly(params.token, "unknown", staged.attempt)
            else:
                await self._settle_quietly(params.token, "rejected", staged.attempt)
            if progress:
                safe_code = getattr(exc, "code", exc.__class__.__name__)
                safe_parts = [f"atomic import failed [{safe_code}]"]
                cause = exc.__cause__
                safe_path = getattr(cause, "path", None)
                if isinstance(safe_path, str) and safe_path:
                    safe_parts.append(f"at {safe_path}")
                    safe_message = str(cause)
                    if safe_message:
                        safe_parts.append(safe_message)
                await progress(total, total, ": ".join(safe_parts))
            if submitted and not known_rejection:
                raise ImportFlowError(
                    "Import submission outcome is unknown; verify the data before previewing again",
                    code="import_outcome_unknown",
                ) from exc
            return ApplyImportResult(
                collection=params.collection,
                created_count=0,
                updated_count=0,
                failed_rows=[row.source_row for row in valid_rows],
            )
        chunk = ImportChunkResult(
            chunk_index=0,
            created_row_keys=created_keys,
            updated_row_keys=updated_keys,
            failed_rows=[],
            idempotency_key=idempotency_key,
        )
        commit_grant()
        try:
            await self._settle_plan(params.token, "committed", staged.attempt)
        except Exception:
            # The business writes and the Host grant are already committed. The
            # plan stays claimed (fail-closed) instead of pretending nothing
            # happened; a late settle cannot change the reported result.
            logger.warning("import.committed_plan_settlement_failed")
        if progress:
            try:
                await progress(total, total, "atomic import committed")
            except Exception:
                logger.warning("import.committed_progress_notification_failed")
        return ApplyImportResult(
            collection=params.collection,
            created_count=len(created_keys),
            updated_count=len(updated_keys),
            failed_rows=[],
            chunks=[chunk],
            request_ids=[request_id] if request_id else [],
        )

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    def _profile(self, collection: str) -> CollectionProfile:
        profile = self._profiles.get(collection)
        if profile is None:
            raise ImportFlowError(
                f"collection {collection!r} is not in the product schema",
                code="schema_unknown",
            )
        return profile

    async def _mint_plan(
        self,
        *,
        collection: str,
        grant_id: str,
        schema_revision: str,
        capability_hash: str,
        source_hash: str,
        rows: list[ImportPlanRow],
        mode: str,
        upsert_key: str | None,
    ) -> ImportPreviewToken:
        """Store the plan with the Go owner, which mints the single-use token."""
        payload = await self._plan_call(
            lambda: self._bulk.mint_import_plan(
                collection=collection,
                grant_id=grant_id,
                schema_revision=schema_revision,
                capability_hash=capability_hash,
                source_hash=source_hash,
                rows=[row.model_dump(mode="json", by_alias=True) for row in rows],
                mode=mode,
                upsert_key=upsert_key,
            )
        )
        token = payload.get("token")
        expires_at = payload.get("expiresAt")
        if not isinstance(token, str) or not token:
            raise ImportFlowError("invalid import plan token reply", code="import_plan_invalid")
        if isinstance(expires_at, bool) or not isinstance(expires_at, (int, float)):
            raise ImportFlowError("invalid import plan token expiry", code="import_plan_invalid")
        return ImportPreviewToken(token=token, expires_at=float(expires_at), consumed=False)

    async def _stage_plan(
        self,
        params: ApplyImportParams,
        profile: CollectionProfile,
    ) -> _StagedImportPlan:
        """Claim the single apply of the stored plan and fetch its frozen rows."""
        payload = await self._plan_call(
            lambda: self._bulk.stage_import_plan(
                token=params.token,
                grant_id=params.grant_id,
                collection=params.collection,
                mode=params.mode,
                capability_hash=profile.capability_hash,
            )
        )
        raw_rows = payload.get("rows")
        if not isinstance(raw_rows, list):
            raise ImportFlowError("invalid staged import plan", code="import_plan_invalid")
        try:
            rows = [ImportPlanRow.model_validate(row) for row in raw_rows]
            schema_revision = payload.get("schemaRevision")
            mode = payload.get("mode")
            attempt = payload.get("attempt")
            if not isinstance(schema_revision, str) or not schema_revision:
                raise ValueError("staged schema revision is missing")
            if not isinstance(mode, str) or not mode:
                raise ValueError("staged mode is missing")
            if isinstance(attempt, bool) or not isinstance(attempt, int):
                raise ValueError("staged attempt lease is missing")
        except Exception as exc:
            raise ImportFlowError("invalid staged import plan", code="import_plan_invalid") from exc
        upsert_key = payload.get("upsertKey")
        return _StagedImportPlan(
            collection=params.collection,
            schema_revision=schema_revision,
            mode=mode,
            upsert_key=upsert_key if isinstance(upsert_key, str) else None,
            rows=rows,
            attempt=attempt,
        )

    async def _bind_prefix(self, token: str, requested_prefix: str, attempt: int) -> None:
        await self._plan_call(
            lambda: self._bulk.bind_import_plan(
                token=token, idempotency_prefix=requested_prefix, attempt=attempt
            )
        )

    async def _settle_plan(self, token: str, outcome: str, attempt: int) -> None:
        await self._plan_call(
            lambda: self._bulk.settle_import_plan(token=token, outcome=outcome, attempt=attempt)
        )

    async def _settle_quietly(self, token: str, outcome: str, attempt: int) -> None:
        try:
            await self._settle_plan(token, outcome, attempt)
        except Exception:
            # A settlement failure must not mask the original import outcome;
            # the claim stays fail-closed with the Go owner.
            logger.warning("import.plan_settlement_failed")

    async def _plan_call(self, call: Callable[[], Awaitable[dict[str, Any]]]) -> dict[str, Any]:
        try:
            return await call()
        except ImportFlowError:
            raise
        except Exception as exc:
            code = getattr(exc, "code", None)
            if isinstance(code, str) and code:
                raise ImportFlowError(
                    str(exc) or "import plan owner rejected the call",
                    code=code,
                    data=getattr(exc, "data", None),
                ) from exc
            raise


__all__ = [
    "DEFAULT_CHUNK_SIZE",
    "IMPORT_TOKEN_TTL_SECONDS",
    "MAX_ATOMIC_IMPORT_ROWS",
    "ImportFlowError",
    "ImportService",
    "RelationImportBatchResult",
    "RelationImportProvider",
    "RelationImportTarget",
    "SourceFile",
    "auto_map_columns",
]
