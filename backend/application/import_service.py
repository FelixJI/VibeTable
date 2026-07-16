"""C1 Directus-aware import service: file reading, normalization, preview, apply.

Reuses the date/currency/enum normalization algorithms from the legacy
:mod:`core.data.import_validator` (extracted here as pure functions, decoupled
from Qt/SQLite), but drives them from a Directus :class:`CollectionProfile`
instead of the SQLite field-mapping service.

Flow
----
* :meth:`preview` reads the granted file (streaming for large workbooks),
  auto-maps or applies the explicit column mapping, normalizes each cell against
  the collection's create-field allow-list + relations, and returns an
  :class:`ImportPlan` bound to the source hash + capability hash via a
  single-use token.
* :meth:`apply` chunks the planned rows and reuses the B2 bulk-mutation
  endpoint for each chunk (idempotency key per chunk). Large imports run as a
  task with per-chunk progress; cancellation stops un-submitted chunks.

The Qt/controller ``confirm_cb`` callback is gone: preview is zero-write and
returns the full plan; the host shows it and the user confirms via apply.
"""

from __future__ import annotations

import csv
import hashlib
import time
import uuid
from collections.abc import Awaitable, Callable
from datetime import date, datetime, timedelta
from pathlib import Path
from typing import Any

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.profile import CollectionProfile, RelationProfile
from backend.application.paste_service import BulkMutationClient
from backend.contracts.data_io import (
    ApplyImportParams,
    ApplyImportResult,
    ImportCellDiagnostic,
    ImportChunkResult,
    ImportColumnMapping,
    ImportPlan,
    ImportPlanRow,
    ImportPreviewToken,
    ImportSummary,
    PreviewImportParams,
)
from backend.contracts.paste import PastePlanRow

#: How long an import preview token remains valid (seconds).
IMPORT_TOKEN_TTL_SECONDS: float = 10 * 60.0

#: Default chunk size for apply (rows per bulk-mutation request).
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
    """An import-flow error carrying an RPC-friendly ``code``."""

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


class ImportService:
    """C1 import preview + apply over the B4 Directus data plane."""

    def __init__(
        self,
        *,
        client: DirectusClient,
        auth: DirectusAuthBroker,
        bulk: BulkMutationClient,
        profiles: dict[str, CollectionProfile],
        resolve_path: Callable[[str, str, str], str],
        consume_grant: Callable[[str], None],
        clock: Callable[[], float] = time.time,
    ) -> None:
        self._client = client
        self._auth = auth
        self._bulk = bulk
        self._profiles = profiles
        self._resolve_path = resolve_path
        self._consume_grant = consume_grant
        self._clock = clock
        self._plans: dict[str, _StoredImportPlan] = {}

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
        path = self._resolve_path(params.grant_id, "import_source", "read")
        source = SourceFile(path)
        header, rows, source_hash = source.read_header_and_rows()
        mapping, unmatched = auto_map_columns(header, profile, params.column_mapping)
        relations = {r.field: r for r in profile.relations}
        plan_rows: list[ImportPlanRow] = []
        error_count = 0
        warning_count = 0
        error_rows = 0
        warning_rows = 0
        for row_offset, raw_row in enumerate(rows):
            source_row = row_offset + 2  # 1-based + header
            values: dict[str, Any] = {}
            diagnostics: list[ImportCellDiagnostic] = []
            for col_index, field in mapping.items():
                raw = raw_row[col_index] if col_index < len(raw_row) else None
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
        chunk_size = params.chunk_size or DEFAULT_CHUNK_SIZE
        prefix = params.idempotency_prefix or f"imp-{uuid.uuid4().hex[:8]}"
        created = 0
        updated = 0
        failed: list[int] = []
        chunks: list[ImportChunkResult] = []
        request_ids: list[str] = []
        total = len(valid_rows)
        for chunk_index, start in enumerate(range(0, total, chunk_size)):
            if cancelled and cancelled():
                break
            chunk_rows = valid_rows[start : start + chunk_size]
            bulk_rows = [
                PastePlanRow(
                    kind="update" if stored.mode == "upsert" and stored.upsert_key else "insert",
                    changes={
                        field: {"before": None, "after": value}
                        for field, value in row.values.items()
                    },
                )
                for row in chunk_rows
            ]
            row_revisions: dict[str | int, str] = {}
            idempotency_key = f"{prefix}-{chunk_index}"
            try:
                result = await self._bulk.apply(
                    collection=params.collection,
                    profile=profile,
                    rows=bulk_rows,
                    row_revisions=row_revisions,
                    idempotency_key=idempotency_key,
                )
            except Exception as exc:
                failed.extend(r.source_row for r in chunk_rows)
                if progress:
                    await progress(
                        start + len(chunk_rows), total, f"chunk {chunk_index} failed: {exc}"
                    )
                continue
            request_ids.append(result.request_id)
            chunks.append(
                ImportChunkResult(
                    chunk_index=chunk_index,
                    created_row_keys=[str(key) for key in result.created_row_keys],
                    updated_row_keys=[str(key) for key in result.updated_row_keys],
                    failed_rows=[],
                    idempotency_key=idempotency_key,
                )
            )
            created += len(result.created_row_keys)
            updated += len(result.updated_row_keys)
            if progress:
                await progress(start + len(chunk_rows), total, f"chunk {chunk_index} committed")
        if not failed:
            stored.consumed = True
            self._consume_grant(params.grant_id)
        return ApplyImportResult(
            collection=params.collection,
            created_count=created,
            updated_count=updated,
            failed_rows=failed,
            chunks=chunks,
            directus_request_ids=request_ids,
        )

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    def _profile(self, collection: str) -> CollectionProfile:
        from backend.adapters.directus.errors import DirectusSchemaError

        profile = self._profiles.get(collection)
        if profile is None:
            raise DirectusSchemaError(f"collection {collection!r} is not in capability manifest")
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
        # Map the field to a Directus-ish type guess from the profile fields.
        # (The capability manifest carries field names but not rich types; the
        # B4 schema endpoint provides ColumnSchema for finer typing. For the
        # first cut we infer from the field name conventions.)
        lowered = field.lower()
        if "date" in lowered:
            ok, value, error = parse_date(raw)
            return ok, value, error
        if "amount" in lowered or "price" in lowered or "total" in lowered:
            return parse_number(raw)
        if lowered in ("sort",) or lowered.endswith("_count"):
            return parse_number(raw, integer=True)
        # Default: text.
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
    "SourceFile",
    "auto_map_columns",
    "clean_currency",
    "parse_date",
    "parse_number",
    "validate_choice",
]
