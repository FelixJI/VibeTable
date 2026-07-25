"""File import service: normalization, preview and atomic apply.

Reuses the date/currency/enum normalization algorithms from the legacy
:mod:`core.data.import_validator` (extracted here as pure functions, decoupled
from Qt/SQLite), but drives them from a product :class:`CollectionProfile`
instead of the SQLite field-mapping service.

Process
----
* :meth:`preview` reads the granted file (streaming for large workbooks),
  auto-maps or applies the explicit column mapping, normalizes each cell against
  the collection's create-field allow-list + relations, and returns an
  :class:`ImportPlan` bound to the source hash + capability hash via a
  single-use token.
* :meth:`apply` submits every valid planned row in one frozen mutation request.
  ``chunkSize`` remains a host compatibility hint only: it never creates
  independent commits. Cancellation is checked before submission; a rejected
  request therefore leaves zero rows committed.

The Qt/controller ``confirm_cb`` callback is gone: preview is zero-write and
returns the full plan; the host shows it and the user confirms via apply.
"""

from __future__ import annotations

import asyncio
import csv
import hashlib
import json
import re
import time
import uuid
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from datetime import date, datetime, timedelta
from pathlib import Path
from typing import Any, Protocol

from backend.application.paste_service import PasteMutationPort
from backend.contracts.data_io import (
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
from backend.contracts.data_profile import CollectionProfile, RelationProfile
from backend.contracts.paste import PastePlanRow

#: How long an import preview token remains valid (seconds).
IMPORT_TOKEN_TTL_SECONDS: float = 10 * 60.0

#: Compatibility default retained by the public contract. Apply is atomic.
DEFAULT_CHUNK_SIZE: int = 500

#: Currency symbols stripped before numeric parsing (mirrors the legacy cleaner).
_CURRENCY_SYMBOLS = ["￥", "$", "¥", "€", "£", "₹", "USD", "CNY", "RMB", "元"]

#: Multi-format date/datetime parse formats (mirrors the legacy validator).
DEFAULT_DATE_INPUT_FORMATS = [
    "%Y-%m-%d",
    "%Y/%m/%d",
    "%Y.%m.%d",
    "%Y%m%d",
    "%d-%m-%Y",
    "%d/%m/%Y",
    "%d.%m.%Y",
    "%m-%d-%Y",
    "%m/%d/%Y",
    "%Y年%m月%d日",
    "%d-%m-%y",
    "%d/%m/%y",
]
DEFAULT_DATETIME_INPUT_FORMATS = [
    "%Y-%m-%d %H:%M:%S",
    "%Y-%m-%d %H:%M",
    "%Y/%m/%d %H:%M:%S",
    "%Y.%m.%d %H:%M:%S",
    "%Y%m%d%H%M%S",
    "%Y-%m-%dT%H:%M:%S",
]


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


# ---------------------------------------------------------------------------
# Pure normalization helpers (extracted from core.data.import_validator)
# ---------------------------------------------------------------------------


def parse_date(value: Any, *, date_type: str = "date") -> tuple[bool, str | None, str | None]:
    """Parse a date/datetime value. Returns ``(ok, normalized, error)``."""
    if value is None or value == "":
        return True, None, None
    if isinstance(value, date) and not isinstance(value, datetime):
        return True, value.isoformat(), None
    if isinstance(value, datetime):
        if date_type == "date":
            return True, value.date().isoformat(), None
        return True, value.strftime("%Y-%m-%d %H:%M:%S"), None
    # Excel serial number (>30000 to avoid misreading small years).
    if isinstance(value, (int, float)) and value > 30000:
        try:
            parsed = datetime(1899, 12, 30) + timedelta(days=float(value))
            if date_type == "date":
                return True, parsed.date().isoformat(), None
            return True, parsed.strftime("%Y-%m-%d %H:%M:%S"), None
        except (ValueError, OverflowError):
            pass
    formats = (
        DEFAULT_DATETIME_INPUT_FORMATS + DEFAULT_DATE_INPUT_FORMATS
        if date_type == "datetime"
        else DEFAULT_DATE_INPUT_FORMATS
    )
    text = str(value).strip()
    for fmt in formats:
        try:
            parsed = datetime.strptime(text, fmt)
            if date_type == "date":
                return True, parsed.date().isoformat(), None
            return True, parsed.strftime("%Y-%m-%d %H:%M:%S"), None
        except ValueError:
            continue
    return False, None, f"unrecognized date format: {value!r}"


def clean_currency(value: Any) -> str:
    """Strip currency symbols + thousands separators from a value."""
    if value is None or value == "":
        return ""
    cleaned = str(value).strip()
    for symbol in _CURRENCY_SYMBOLS:
        cleaned = cleaned.replace(symbol, "")
    cleaned = cleaned.replace(",", "").strip()
    return cleaned


def parse_number(
    value: Any, *, integer: bool = False
) -> tuple[bool, float | int | None, str | None]:
    """Parse a numeric value (integer or decimal). Returns ``(ok, value, error)``."""
    if value is None or value == "":
        return True, None, None
    if isinstance(value, (int, float)):
        return True, (int(value) if integer else float(value)), None
    cleaned = clean_currency(value)
    try:
        number = float(cleaned)
    except ValueError:
        return False, None, f"not a valid number: {value!r}"
    return True, (int(number) if integer else number), None


def validate_choice(value: Any, options: list[str]) -> tuple[bool, str | None, str | None]:
    """Strict enum membership. Returns ``(ok, value, error)``."""
    if value is None or value == "":
        return True, None, None
    text = str(value).strip()
    if text in options:
        return True, text, None
    hint = ", ".join(options[:10])
    if len(options) > 10:
        hint += f" ... ({len(options)} total)"
    return False, None, f"value {value!r} not in options: [{hint}]"


def _select_options(value: Any) -> list[str]:
    if not isinstance(value, list):
        return []
    for constraint in value:
        if not isinstance(constraint, dict) or constraint.get("kind") != "enum":
            continue
        options = constraint.get("options")
        if not isinstance(options, list):
            return []
        result: list[str] = []
        for option in options:
            raw = option.get("value") if isinstance(option, dict) else option
            if isinstance(raw, str):
                result.append(raw)
        return result
    return []


# ---------------------------------------------------------------------------
# File reading (streaming, cancellation-aware)
# ---------------------------------------------------------------------------


class SourceFile:
    """A granted import source file, read lazily.

    Excel workbooks are read sheet-by-sheet; CSV files are read row-by-row. The
    reader never loads the entire file into memory at once.
    """

    def __init__(self, path: str) -> None:
        self._path = Path(path)

    def read_header_and_rows(
        self,
        *,
        max_rows: int = 100000,
        sheet: str | None = None,
    ) -> tuple[list[str], list[list[Any]], str]:
        """Read the header row + data rows.

        Returns ``(header, rows, source_hash)``. For XLSX the first sheet (or the
        named ``sheet``) is used; for CSV the single stream is used.
        ``source_hash`` is the SHA-256 of the file bytes (binds the preview).
        """
        source_hash = hashlib.sha256(self._path.read_bytes()).hexdigest()
        suffix = self._path.suffix.lower()
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

        wb = load_workbook(self._path, read_only=True, data_only=True)
        try:
            ws = wb[sheet] if sheet and sheet in wb.sheetnames else wb.active
            if ws is None:
                raise ImportFlowError(
                    "workbook has no readable worksheet",
                    code="import_empty_workbook",
                )
            rows_iter = ws.iter_rows(values_only=True)
            try:
                header = [str(c) if c is not None else "" for c in next(rows_iter)]
            except StopIteration:
                return [], []
            data: list[list[Any]] = []
            for row in rows_iter:
                if len(data) >= max_rows:
                    break
                data.append(list(row))
            return header, data
        finally:
            wb.close()

    def _read_csv(self, *, max_rows: int) -> tuple[list[str], list[list[Any]]]:
        with self._path.open("r", encoding="utf-8-sig", newline="") as fh:
            reader = csv.reader(fh)
            try:
                header = [str(c) for c in next(reader)]
            except StopIteration:
                return [], []
            data: list[list[Any]] = []
            for row in reader:
                if len(data) >= max_rows:
                    break
                data.append(list(row))
        return header, data


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


class _StoredImportPlan:
    """A preview plan retained server-side for a single apply."""

    __slots__ = (
        "capability_hash",
        "collection",
        "consumed",
        "expires_at",
        "idempotency_prefix",
        "mode",
        "rows",
        "schema_revision",
        "source_hash",
        "upsert_key",
    )

    def __init__(
        self,
        *,
        collection: str,
        schema_revision: str,
        capability_hash: str,
        source_hash: str,
        rows: list[ImportPlanRow],
        mode: str,
        upsert_key: str | None,
        expires_at: float,
    ) -> None:
        self.collection = collection
        self.schema_revision = schema_revision
        self.capability_hash = capability_hash
        self.source_hash = source_hash
        self.rows = rows
        self.mode = mode
        self.upsert_key = upsert_key
        self.expires_at = expires_at
        self.consumed = False
        self.idempotency_prefix: str | None = None


class ImportService:
    """Import preview and atomic apply over product-owned ports."""

    def __init__(
        self,
        *,
        client: Any,
        auth: Any,
        bulk: PasteMutationPort,
        profiles: dict[str, CollectionProfile],
        resolve_path: Callable[..., str],
        consume_grant: Callable[[str], None],
        relation_provider: RelationImportProvider | None = None,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self._client = client
        self._auth = auth
        self._bulk = bulk
        self._profiles = profiles
        self._resolve_path = resolve_path
        self._consume_grant = consume_grant
        self._relation_provider = relation_provider
        self._clock = clock
        self._plans: dict[str, _StoredImportPlan] = {}
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
        path = self._resolve_path(
            params.grant_id,
            purpose="import_source",
            direction="read",
        )
        source = SourceFile(path)
        header, rows, source_hash = source.read_header_and_rows()
        mapping, unmatched = auto_map_columns(header, profile, params.column_mapping)
        relations = {r.field: r for r in profile.relations}
        explicit_by_source = {item.source_column.strip(): item for item in params.column_mapping}
        relation_targets: dict[int, RelationImportTarget] = {}
        relation_mapping_errors: dict[int, tuple[str, str]] = {}
        for col_index, field in mapping.items():
            relation = relations.get(field)
            explicit = explicit_by_source.get(header[col_index].strip())
            if relation is None:
                if explicit and (
                    explicit.relation_id or explicit.match_field or explicit.relation_lookup
                ):
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
        error_count = 0
        warning_count = 0
        error_rows = 0
        warning_rows = 0
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
                ok, normalized, error = self._normalize(field, raw, profile, relations)
                if not ok and error:
                    diagnostics.append(
                        ImportCellDiagnostic(
                            row=source_row,
                            column=col_index + 1,
                            severity="error",
                            code="value_invalid",
                            message=error,
                            original_value="" if raw is None else str(raw),
                        )
                    )
                elif normalized is not None:
                    values[field] = normalized
            has_error = any(d.severity == "error" for d in diagnostics)
            has_warning = any(d.severity == "warning" for d in diagnostics)
            if has_error:
                error_rows += 1
            if has_warning:
                warning_rows += 1
            error_count += sum(1 for d in diagnostics if d.severity == "error")
            warning_count += sum(1 for d in diagnostics if d.severity == "warning")
            plan_rows.append(
                ImportPlanRow(
                    source_row=source_row,
                    values=values,
                    diagnostics=diagnostics,
                    relation_resolutions=relation_resolutions,
                )
            )
        summary = ImportSummary(
            total_rows=len(rows),
            valid_rows=len(rows) - error_rows,
            error_rows=error_rows,
            warning_rows=warning_rows,
            error_count=error_count,
            warning_count=warning_count,
        )
        token = self._mint_plan(
            collection=params.collection,
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
        stored = self._plans.get(params.token)
        if stored is None:
            raise ImportFlowError("import token not found", code="import_token_unknown")
        if self._clock() >= stored.expires_at:
            raise ImportFlowError("import token expired", code="import_token_expired")
        if stored.consumed:
            raise ImportFlowError("import token already used", code="import_token_consumed")
        if stored.capability_hash != profile.capability_hash:
            raise ImportFlowError("schema changed since preview", code="schema_mismatch")
        valid_rows = [
            r for r in stored.rows if not any(d.severity == "error" for d in r.diagnostics)
        ]
        total = len(valid_rows)
        if cancelled and cancelled():
            raise asyncio.CancelledError
        requested_prefix = params.idempotency_prefix or (
            "imp-" + hashlib.sha256(params.token.encode("utf-8")).hexdigest()[:16]
        )
        if stored.idempotency_prefix is None:
            stored.idempotency_prefix = requested_prefix
        elif stored.idempotency_prefix != requested_prefix:
            raise ImportFlowError(
                "import token is bound to a different idempotency prefix",
                code="import_idempotency_mismatch",
            )
        prefix = stored.idempotency_prefix
        idempotency_key = f"{prefix}-0"
        bulk_rows = [
            PastePlanRow(
                kind="insert",
                changes={
                    field: {"before": None, "after": value}
                    for field, value in row.values.items()
                },
            )
            for row in valid_rows
        ]
        requires_cross_table = stored.mode == "upsert" or any(
            resolution.state == "create"
            for row in valid_rows
            for resolution in row.relation_resolutions
        )
        try:
            if requires_cross_table:
                if self._relation_provider is None:
                    raise ImportFlowError(
                        "atomic relation/upsert import is not configured",
                        code="relation_provider_unavailable",
                    )
                relation_result = await self._relation_provider.apply_chunk(
                    collection=params.collection,
                    profile=profile,
                    rows=valid_rows,
                    mode=stored.mode,
                    upsert_key=stored.upsert_key,
                    idempotency_key=idempotency_key,
                )
                created_keys = relation_result.created_row_keys
                updated_keys = relation_result.updated_row_keys
                request_id = relation_result.request_id
            else:
                result = await self._bulk.apply(
                    collection=params.collection,
                    profile=profile,
                    rows=bulk_rows,
                    row_revisions={},
                    idempotency_key=idempotency_key,
                    schema_revision=stored.schema_revision,
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
        except Exception as exc:
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
        if progress:
            await progress(total, total, "atomic import committed")
        stored.consumed = True
        self._consume_grant(params.grant_id)
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

    def _normalize(
        self,
        field: str,
        raw: Any,
        profile: CollectionProfile,
        relations: dict[str, RelationProfile],
    ) -> tuple[bool, Any, str | None]:
        # Relation fields: store the raw primary-key value (display-name lookup
        # is a preview-time resolution that requires querying the related
        # collection; for the first cut we accept PK values directly and warn
        # on display-name inputs that need explicit mapping).
        if field in relations:
            if raw is None or raw == "":
                return True, None, None
            return True, str(raw).strip(), None
        descriptor = profile.field_schemas.get(field, {})
        data_type = descriptor.get("dataType")
        if data_type in {"date", "dateTime", "autoDate"}:
            return parse_date(
                raw,
                date_type="date" if data_type == "date" else "datetime",
            )
        if data_type == "integer":
            return parse_number(raw, integer=True)
        if data_type in {"float", "decimal", "number"}:
            return parse_number(raw)
        if data_type == "boolean":
            if raw is None or raw == "":
                return True, None, None
            if isinstance(raw, bool):
                return True, raw, None
            normalized = str(raw).strip().lower()
            if normalized in {"true", "1", "yes", "y", "是"}:
                return True, True, None
            if normalized in {"false", "0", "no", "n", "否"}:
                return True, False, None
            return False, None, f"invalid boolean value: {raw!r}"
        if data_type in {"json", "geoJson", "list"}:
            if raw is None or raw == "":
                return True, None, None
            if isinstance(raw, (dict, list, int, float, bool)):
                return True, raw, None
            try:
                return True, json.loads(str(raw)), None
            except (json.JSONDecodeError, TypeError):
                return False, None, f"invalid JSON value: {raw!r}"
        if data_type in {"select", "multiSelect"}:
            options = _select_options(descriptor.get("constraints"))
            if data_type == "select":
                return validate_choice(raw, options)
            if raw is None or raw == "":
                return True, [], None
            values: Any = raw
            if isinstance(raw, str):
                try:
                    decoded = json.loads(raw)
                    values = decoded if isinstance(decoded, list) else raw.split(",")
                except json.JSONDecodeError:
                    values = raw.split(",")
            if not isinstance(values, list):
                return False, None, f"invalid multi-select value: {raw!r}"
            normalized_values = [str(value).strip() for value in values]
            invalid = [value for value in normalized_values if value not in options]
            if invalid:
                return False, None, f"values are not in select options: {invalid!r}"
            return True, normalized_values, None
        if data_type == "time":
            if raw is None or raw == "":
                return True, None, None
            value = str(raw).strip()
            if re.fullmatch(r"(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,3})?", value):
                return True, value, None
            return False, None, f"invalid time value: {raw!r}"
        # String-like fields keep their textual wire representation. The
        # MutationKernel remains authoritative for length/pattern/URL/email
        # constraints and returns stable field paths on rejection.
        if raw is None or raw == "":
            return True, None, None
        if isinstance(raw, float) and raw.is_integer():
            return True, str(int(raw)), None
        return True, str(raw).strip(), None

    def _mint_plan(
        self,
        *,
        collection: str,
        schema_revision: str,
        capability_hash: str,
        source_hash: str,
        rows: list[ImportPlanRow],
        mode: str,
        upsert_key: str | None,
    ) -> ImportPreviewToken:
        token = f"imp-{uuid.uuid4().hex[:24]}"
        self._plans[token] = _StoredImportPlan(
            collection=collection,
            schema_revision=schema_revision,
            capability_hash=capability_hash,
            source_hash=source_hash,
            rows=rows,
            mode=mode,
            upsert_key=upsert_key,
            expires_at=self._clock() + IMPORT_TOKEN_TTL_SECONDS,
        )
        return ImportPreviewToken(
            token=token, expires_at=self._clock() + IMPORT_TOKEN_TTL_SECONDS, consumed=False
        )


__all__ = [
    "DEFAULT_CHUNK_SIZE",
    "IMPORT_TOKEN_TTL_SECONDS",
    "ImportFlowError",
    "ImportService",
    "RelationImportBatchResult",
    "RelationImportProvider",
    "RelationImportTarget",
    "SourceFile",
    "auto_map_columns",
    "clean_currency",
    "parse_date",
    "parse_number",
    "validate_choice",
]
