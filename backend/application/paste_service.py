"""Two-phase paste application service over product-owned ports.

* :meth:`preview` produces a :class:`PastePlan` (no writes) and a single-use
  :class:`PasteToken` bound to the user / project / collection / schema hash /
  target rows / payload hash.
* :meth:`apply` validates the token and delegates the atomic batch to the
  product mutation endpoint, returning a confirmed :class:`ApplyPasteResult`.

The bulk endpoint runs in one server transaction (all-or-nothing) and honours
an idempotency key for safe retries. The Python layer never holds raw payload
material in the token itself — the token is an opaque signed handle that the
service resolves to its in-memory plan.

Tokens live in-process (the Python broker is the single session owner) and
expire after :data:`TOKEN_TTL_SECONDS` (5 minutes). They are consumed exactly
once; any change to the binding (user, schema, row revision, payload) invalidates
the token with a precise error.
"""

from __future__ import annotations

import asyncio
import hashlib
import hmac
import json
import re
import secrets as pysecrets
import time
from collections.abc import Callable
from typing import Any, Protocol

from backend.contracts.data_profile import CollectionProfile
from backend.contracts.paste import (
    ApplyPasteConflict,
    ApplyPasteParams,
    ApplyPasteResult,
    PasteCell,
    PasteCellDiagnostic,
    PastePlan,
    PastePlanRow,
    PasteStartCell,
    PasteSummary,
    PasteToken,
    PreviewPasteParams,
)

#: How long a preview token remains valid (seconds).
TOKEN_TTL_SECONDS: float = 5 * 60.0

#: Hard cap on parsed clipboard cells. Larger pastes must use the C1 file import.
MAX_PASTE_CELLS: int = 10_000


class PasteError(Exception):
    """A paste error carrying an RPC-friendly ``code`` and ``data``."""

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


class _StoredPlan:
    """A preview plan retained server-side for a single apply."""

    __slots__ = (
        "capability_hash",
        "collection",
        "consumed",
        "expires_at",
        "idempotency_key",
        "payload_hash",
        "project",
        "raw_rows",
        "row_revisions",
        "rows",
        "schema_revision",
        "user_id",
    )

    def __init__(
        self,
        *,
        user_id: str,
        project: str,
        collection: str,
        schema_revision: str,
        capability_hash: str,
        payload_hash: str,
        rows: list[PastePlanRow],
        row_revisions: dict[str | int, str],
        expires_at: float,
        raw_rows: list[dict[str, Any]] | None = None,
    ) -> None:
        self.user_id = user_id
        self.project = project
        self.collection = collection
        self.schema_revision = schema_revision
        self.capability_hash = capability_hash
        self.payload_hash = payload_hash
        self.rows = rows
        self.row_revisions = row_revisions
        self.raw_rows = list(raw_rows or [{} for _ in rows])
        self.expires_at = expires_at
        self.consumed = False
        self.idempotency_key: str | None = None


class PasteTokenStore:
    """In-memory store of signed, single-use preview tokens.

    Each token is an opaque random handle plus an HMAC tag derived from a
    per-process secret, so a token minted by one process cannot be replayed
    against another. The store also retains the bound plan so apply can resolve
    the token without trusting host-supplied payload material.
    """

    def __init__(
        self,
        *,
        secret: bytes | None = None,
        clock: Callable[[], float] = time.time,
        ttl_seconds: float = TOKEN_TTL_SECONDS,
    ) -> None:
        self._secret = secret or pysecrets.token_bytes(32)
        self._clock = clock
        self._ttl_seconds = ttl_seconds
        self._plans: dict[str, _StoredPlan] = {}

    def mint(self, plan: _StoredPlan) -> PasteToken:
        raw = pysecrets.token_urlsafe(24)
        tag = hmac.new(self._secret, raw.encode("ascii"), hashlib.sha256).hexdigest()
        token = f"{raw}.{tag}"
        self._plans[token] = plan
        return PasteToken(token=token, expires_at=plan.expires_at, consumed=False)

    @property
    def ttl_seconds(self) -> float:
        return self._ttl_seconds

    @property
    def now(self) -> float:
        return self._clock()

    def resolve(self, token: str) -> _StoredPlan:
        if "." not in token:
            raise PasteError("invalid paste token", code="paste_token_invalid")
        raw, _, tag = token.rpartition(".")
        expected = hmac.new(self._secret, raw.encode("ascii"), hashlib.sha256).hexdigest()
        if not hmac.compare_digest(expected, tag):
            raise PasteError("invalid paste token", code="paste_token_invalid")
        plan = self._plans.get(token)
        if plan is None:
            raise PasteError("paste token not found", code="paste_token_unknown")
        if self._clock() >= plan.expires_at:
            raise PasteError("paste token expired", code="paste_token_expired")
        return plan

    def consume(self, plan: _StoredPlan) -> None:
        plan.consumed = True

    def __contains__(self, token: str) -> bool:
        return token in self._plans


class PasteMutationPort(Protocol):
    async def preview_import(
        self,
        *,
        collection: str,
        schema_revision: str,
        rows: list[dict[str, Any]],
        row_modes: list[str] | None = None,
    ) -> dict[str, Any]: ...

    async def preview_paste(
        self,
        *,
        collection: str,
        profile: CollectionProfile,
        rows: list[PastePlanRow],
        raw_rows: list[dict[str, Any]],
        row_revisions: dict[str | int, str],
        schema_revision: str,
    ) -> None: ...

    async def apply(
        self,
        *,
        collection: str,
        profile: CollectionProfile,
        rows: list[PastePlanRow],
        row_revisions: dict[str | int, str],
        idempotency_key: str,
        schema_revision: str | None = None,
        raw_rows: list[dict[str, Any]] | None = None,
    ) -> ApplyPasteResult: ...


class _CurrentActor(Protocol):
    id: str


class PasteAuthPort(Protocol):
    async def current_user(self) -> _CurrentActor: ...


class PasteReadPort(Protocol):
    async def readonly_fields(
        self,
        profile: CollectionProfile,
        *,
        refresh: bool,
    ) -> set[str]: ...

    async def require_write_fields(
        self,
        profile: CollectionProfile,
        fields: set[str],
        *,
        operation: str,
        refresh: bool,
    ) -> None: ...

    async def read_item(
        self,
        profile: CollectionProfile,
        item_id: str,
    ) -> dict[str, Any]: ...


class PasteService:
    """B2 application service for ``table.previewPaste`` / ``table.applyPaste``."""

    def __init__(
        self,
        *,
        client: PasteReadPort,
        auth: PasteAuthPort,
        bulk: PasteMutationPort,
        profiles: dict[str, CollectionProfile],
        project: str,
        token_store: PasteTokenStore | None = None,
    ) -> None:
        self._client = client
        self._auth = auth
        self._bulk = bulk
        self._profiles = profiles
        self._project = project
        self._tokens = token_store or PasteTokenStore()
        self._apply_lock = asyncio.Lock()

    async def preview(self, params: PreviewPasteParams) -> PastePlan:
        profile = self._profile(params.collection)
        if params.schema_revision != profile.capability_hash:
            raise PasteError(
                "schema changed since the grid was rendered",
                code="schema_mismatch",
                data={
                    "currentSchemaRevision": profile.capability_hash,
                    "expectedSchemaRevision": params.schema_revision,
                },
            )
        total_cells = sum(len(row) for row in params.cells)
        if total_cells > MAX_PASTE_CELLS:
            raise PasteError(
                f"clipboard exceeds the {MAX_PASTE_CELLS} cell limit; use file import",
                code="paste_overflow",
                data={"maxCells": MAX_PASTE_CELLS, "cellCount": total_cells},
            )
        user = await self._auth.current_user()
        readonly = await self._client.readonly_fields(profile, refresh=False)
        editable_columns, readonly_columns = self._column_layout(profile, readonly)
        anchor_column_index = _anchor_column_index(params.start_cell, editable_columns)
        plan_rows, row_revisions, coordinates = await self._resolve_plan(
            profile=profile,
            params=params,
            editable_columns=editable_columns,
            readonly_columns=readonly_columns,
            anchor_column_index=anchor_column_index,
        )
        raw_rows = await self._apply_authoritative_preview(
            profile=profile,
            schema_revision=params.schema_revision,
            rows=plan_rows,
            coordinates=coordinates,
            row_revisions=row_revisions,
        )
        payload_hash = _payload_hash(params.cells)
        expires_at = self._tokens.now + self._tokens.ttl_seconds
        stored = _StoredPlan(
            user_id=user.id,
            project=self._project,
            collection=profile.collection,
            schema_revision=params.schema_revision,
            capability_hash=profile.capability_hash,
            payload_hash=payload_hash,
            rows=plan_rows,
            row_revisions=row_revisions,
            expires_at=expires_at,
            raw_rows=raw_rows,
        )
        token = self._tokens.mint(stored)
        summary = _summarize(plan_rows)
        return PastePlan(
            collection=profile.collection,
            schema_revision=params.schema_revision,
            capability_hash=profile.capability_hash,
            summary=summary,
            rows=plan_rows,
            token=token,
        )

    async def apply(self, params: ApplyPasteParams) -> ApplyPasteResult:
        async with self._apply_lock:
            return await self._apply_serialized(params)

    async def _apply_serialized(self, params: ApplyPasteParams) -> ApplyPasteResult:
        profile = self._profile(params.collection)
        stored = self._tokens.resolve(params.token)
        if stored.consumed:
            raise PasteError("paste token already used", code="paste_token_consumed")
        if stored.collection != params.collection:
            raise PasteError("paste token does not match collection", code="paste_token_invalid")
        if stored.schema_revision != profile.capability_hash:
            raise PasteError(
                "schema changed since the plan was prepared",
                code="schema_mismatch",
                data={
                    "currentSchemaRevision": profile.capability_hash,
                    "expectedSchemaRevision": stored.schema_revision,
                },
            )
        user = await self._auth.current_user()
        if stored.user_id != user.id:
            raise PasteError("paste token belongs to another user", code="paste_token_invalid")
        if stored.idempotency_key is None:
            stored.idempotency_key = params.idempotency_key
        elif stored.idempotency_key != params.idempotency_key:
            raise PasteError(
                "paste token is bound to a different idempotency key",
                code="paste_idempotency_mismatch",
            )
        if any(
            diagnostic.severity == "error" for row in stored.rows for diagnostic in row.diagnostics
        ):
            raise PasteError(
                "paste plan contains validation errors",
                code="paste_plan_invalid",
            )
        create_fields = {
            name for row in stored.rows if row.kind == "insert" for name in row.changes
        }
        update_fields = {
            name for row in stored.rows if row.kind == "update" for name in row.changes
        }
        try:
            # Refresh once for the first operation and reuse that same live
            # policy snapshot for the other operation kind.
            if create_fields:
                await self._client.require_write_fields(
                    profile,
                    create_fields,
                    operation="create",
                    refresh=True,
                )
            if update_fields:
                await self._client.require_write_fields(
                    profile,
                    update_fields,
                    operation="update",
                    refresh=not create_fields,
                )
        except Exception as exc:
            raise PasteError(
                "field policy changed since the plan was prepared",
                code="schema_mismatch",
            ) from exc
        result = await self._bulk.apply(
            collection=profile.collection,
            profile=profile,
            rows=stored.rows,
            row_revisions=stored.row_revisions,
            idempotency_key=params.idempotency_key,
            schema_revision=stored.schema_revision,
            raw_rows=stored.raw_rows,
        )
        if result.outcome == "committed":
            self._tokens.consume(stored)
        return result

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    def _profile(self, collection: str) -> CollectionProfile:
        profile = self._profiles.get(collection)
        if profile is None:
            raise PasteError(
                f"collection {collection!r} is not in the product schema",
                code="schema_unknown",
            )
        return profile

    def _column_layout(
        self,
        profile: CollectionProfile,
        readonly_fields: set[str],
    ) -> tuple[list[str], list[str]]:
        editable = [
            name
            for name in profile.update_fields
            if name in profile.fields and name not in readonly_fields
        ]
        readonly = [name for name in profile.fields if name not in editable]
        return editable, readonly

    async def _resolve_plan(
        self,
        *,
        profile: CollectionProfile,
        params: PreviewPasteParams,
        editable_columns: list[str],
        readonly_columns: list[str],
        anchor_column_index: int,
    ) -> tuple[
        list[PastePlanRow],
        dict[str | int, str],
        list[dict[str, tuple[int, int]]],
    ]:
        row_keys = _selection_row_keys(params.selection)
        start_row = _selection_anchor_index(row_keys, params.start_cell)
        plan_rows: list[PastePlanRow] = []
        row_revisions: dict[str | int, str] = {}
        coordinates: list[dict[str, tuple[int, int]]] = []
        for offset, cell_row in enumerate(params.cells):
            target_index = start_row + offset
            kind, target_key = _classify_row(
                row_keys=row_keys,
                target_index=target_index,
                start_cell=params.start_cell,
            )
            if kind == "skip" and target_key is None:
                plan_rows.append(
                    PastePlanRow(
                        kind="skip",
                        diagnostics=[
                            PasteCellDiagnostic(
                                row_index=offset,
                                column_index=0,
                                severity="error",
                                code="anchor_out_of_range",
                                message="paste anchor is past the last selected row and appends are not supported",
                            )
                        ],
                    )
                )
                coordinates.append({})
                continue
            if kind == "update" and target_key is not None:
                current = await self._client.read_item(profile, str(target_key))
                date_updated_field = profile.date_updated_field
                guard_value = (
                    current.get(date_updated_field) if date_updated_field else None
                ) or current.get("__vibetableDigest")
                if isinstance(guard_value, str) and guard_value:
                    row_revisions[target_key] = guard_value
                changes, diagnostics, row_coordinates = self._build_changes(
                    cell_row=cell_row,
                    editable_columns=editable_columns,
                    readonly_columns=readonly_columns,
                    anchor_column_index=anchor_column_index,
                    current_row=current,
                )
                plan_rows.append(
                    PastePlanRow(
                        kind="update",
                        target_row_key=target_key,
                        expected_date_updated=row_revisions.get(target_key),
                        changes=changes,
                        diagnostics=diagnostics,
                    )
                )
                coordinates.append(row_coordinates)
            else:
                changes, diagnostics, row_coordinates = self._build_changes(
                    cell_row=cell_row,
                    editable_columns=editable_columns,
                    readonly_columns=readonly_columns,
                    anchor_column_index=anchor_column_index,
                    current_row={},
                )
                plan_rows.append(
                    PastePlanRow(
                        kind="insert",
                        changes=changes,
                        diagnostics=diagnostics,
                    )
                )
                coordinates.append(row_coordinates)
        return plan_rows, row_revisions, coordinates

    async def _apply_authoritative_preview(
        self,
        *,
        profile: CollectionProfile,
        schema_revision: str,
        rows: list[PastePlanRow],
        coordinates: list[dict[str, tuple[int, int]]],
        row_revisions: dict[str | int, str],
    ) -> list[dict[str, Any]]:
        raw_rows = [
            {field: change["after"] for field, change in row.changes.items() if "after" in change}
            for row in rows
        ]
        writable = [
            (index, row) for index, row in enumerate(rows) if row.kind in {"insert", "update"}
        ]
        if not writable:
            return raw_rows
        authoritative = await self._bulk.preview_import(
            collection=profile.collection,
            schema_revision=schema_revision,
            rows=[raw_rows[index] for index, _ in writable],
            row_modes=[row.kind for _, row in writable],
        )
        normalized_rows = authoritative.get("rows")
        if not isinstance(normalized_rows, list) or len(normalized_rows) != len(writable):
            raise PasteError(
                "invalid authoritative paste preview",
                code="paste_preview_invalid",
            )
        for authoritative_index, raw_result in enumerate(normalized_rows):
            if not isinstance(raw_result, dict):
                raise PasteError(
                    "invalid authoritative paste preview row",
                    code="paste_preview_invalid",
                )
            normalized = raw_result.get("values")
            diagnostics = raw_result.get("diagnostics")
            if not isinstance(normalized, dict) or not isinstance(diagnostics, list):
                raise PasteError(
                    "invalid authoritative paste preview row",
                    code="paste_preview_invalid",
                )
            plan_index, row = writable[authoritative_index]
            for field, normalized_value in normalized.items():
                change = row.changes.get(field)
                if change is not None:
                    change["after"] = normalized_value
            for raw_diagnostic in diagnostics:
                if not isinstance(raw_diagnostic, dict):
                    raise PasteError(
                        "invalid authoritative paste diagnostic",
                        code="paste_preview_invalid",
                    )
                raw_field = raw_diagnostic.get("field")
                field = raw_field if isinstance(raw_field, str) else ""
                source_row, source_column = _diagnostic_coordinate(
                    profile=profile,
                    coordinates=coordinates,
                    plan_index=plan_index,
                    field=field,
                )
                row.diagnostics.append(
                    PasteCellDiagnostic(
                        row_index=source_row,
                        column_index=source_column,
                        severity="error",
                        code=str(raw_diagnostic.get("code") or "field.value.invalid"),
                        message=str(raw_diagnostic.get("message") or "invalid field value"),
                    )
                )
        for plan_index, row in writable:
            if row.kind != "update":
                continue
            unchanged = [
                field
                for field, change in row.changes.items()
                if change.get("before") == change.get("after")
            ]
            for field in unchanged:
                row.changes.pop(field, None)
                raw_rows[plan_index].pop(field, None)

        preview_targets = [
            (plan_index, row)
            for plan_index, row in writable
            if row.changes
            and not any(diagnostic.severity == "error" for diagnostic in row.diagnostics)
        ]
        if not preview_targets:
            return raw_rows
        try:
            await self._bulk.preview_paste(
                collection=profile.collection,
                profile=profile,
                rows=[row for _, row in preview_targets],
                raw_rows=[raw_rows[index] for index, _ in preview_targets],
                row_revisions=row_revisions,
                schema_revision=schema_revision,
            )
        except PasteError as error:
            path = (error.data or {}).get("path")
            match = (
                re.match(
                    r"^operations\[(\d+)\]\.rawValues\.([^.]+)",
                    path,
                )
                if isinstance(path, str)
                else None
            )
            if match is None:
                raise
            operation_index = int(match.group(1))
            if operation_index >= len(preview_targets):
                raise PasteError(
                    "invalid authoritative mutation preview path",
                    code="paste_preview_invalid",
                ) from error
            field = match.group(2)
            plan_index, row = preview_targets[operation_index]
            source_row, source_column = _diagnostic_coordinate(
                profile=profile,
                coordinates=coordinates,
                plan_index=plan_index,
                field=field,
            )
            row.diagnostics.append(
                PasteCellDiagnostic(
                    row_index=source_row,
                    column_index=source_column,
                    severity="error",
                    code=error.code,
                    message=str(error),
                )
            )
        return raw_rows

    def _build_changes(
        self,
        *,
        cell_row: list[PasteCell],
        editable_columns: list[str],
        readonly_columns: list[str],
        anchor_column_index: int,
        current_row: dict[str, Any],
    ) -> tuple[
        dict[str, dict[str, Any]],
        list[PasteCellDiagnostic],
        dict[str, tuple[int, int]],
    ]:
        changes: dict[str, dict[str, Any]] = {}
        diagnostics: list[PasteCellDiagnostic] = []
        coordinates: dict[str, tuple[int, int]] = {}
        for cell in cell_row:
            resolved_index = anchor_column_index + cell.column_index
            if cell.column is not None:
                column = cell.column
                if cell.column_index != 0:
                    diagnostics.append(
                        PasteCellDiagnostic(
                            row_index=cell.row_index,
                            column_index=cell.column_index,
                            severity="warning",
                            code="column_override_ignored",
                            message="cell column override ignored; columns are resolved from the anchor",
                        )
                    )
            elif 0 <= resolved_index < len(editable_columns):
                column = editable_columns[resolved_index]
            else:
                if 0 <= resolved_index < len(editable_columns) + len(readonly_columns):
                    diagnostics.append(
                        PasteCellDiagnostic(
                            row_index=cell.row_index,
                            column_index=cell.column_index,
                            severity="error",
                            code="column_readonly",
                            message="target column is read-only",
                        )
                    )
                else:
                    diagnostics.append(
                        PasteCellDiagnostic(
                            row_index=cell.row_index,
                            column_index=cell.column_index,
                            severity="error",
                            code="column_out_of_range",
                            message="target column is outside the editable range",
                        )
                    )
                continue
            before = current_row.get(column)
            # Clipboard/file adapters own coordinates, not field semantics.
            # Preserve the exact raw cell and let MutationKernel route it
            # through FieldValueKernel.NormalizeRawInput.
            after = cell.raw_value
            if before == after:
                continue
            changes[column] = {"before": before, "after": after}
            coordinates[column] = (cell.row_index, cell.column_index)
        return changes, diagnostics, coordinates


# ---------------------------------------------------------------------------
# Module-level helpers (pure functions, easy to unit test)
# ---------------------------------------------------------------------------


def _selection_row_keys(selection: Any) -> list[str | int]:
    """Extract the ordered row keys from a B3 selection snapshot.

    The selection may be a :class:`SelectionSnapshot` or the dict/alias form the
    host carries. Missing or malformed selections resolve to an empty list (the
    paste becomes an insert at the anchor).
    """
    if hasattr(selection, "row_keys"):
        return list(selection.row_keys)
    if isinstance(selection, dict):
        return list(selection.get("rowKeys") or selection.get("row_keys") or [])
    return []


def _selection_anchor_index(row_keys: list[str | int], start_cell: PasteStartCell) -> int:
    """Locate the anchor row index within the selection's row-key order."""
    if start_cell.row_key is None:
        return len(row_keys)
    for index, key in enumerate(row_keys):
        if str(key) == str(start_cell.row_key):
            return index
    raise PasteError(
        "paste anchor row is not part of the current selection",
        code="anchor_not_selected",
    )


def _anchor_column_index(start_cell: PasteStartCell, editable_columns: list[str]) -> int:
    """Locate the anchor column index within the editable-column order."""
    for index, name in enumerate(editable_columns):
        if name == start_cell.column:
            return index
    raise PasteError(
        f"paste anchor column {start_cell.column!r} is not editable",
        code="anchor_column_readonly",
    )


def _classify_row(
    *,
    row_keys: list[str | int],
    target_index: int,
    start_cell: PasteStartCell,
) -> tuple[str, str | int | None]:
    if target_index < len(row_keys):
        return "update", row_keys[target_index]
    if start_cell.row_key is None and target_index == len(row_keys):
        return "insert", None
    return "skip", None


def _payload_hash(cells: list[list[PasteCell]]) -> str:
    payload = [
        [{"c": cell.column_index, "col": cell.column, "v": cell.raw_value} for cell in row]
        for row in cells
    ]
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _diagnostic_coordinate(
    *,
    profile: CollectionProfile,
    coordinates: list[dict[str, tuple[int, int]]],
    plan_index: int,
    field: str,
) -> tuple[int, int]:
    """Resolve kernel field IDs back to clipboard coordinates."""
    row_coordinates = coordinates[plan_index]
    direct = row_coordinates.get(field)
    if direct is not None:
        return direct
    for physical_name, schema in profile.field_schemas.items():
        if schema.get("fieldId") == field:
            resolved = row_coordinates.get(physical_name)
            if resolved is not None:
                return resolved
            break
    if row_coordinates:
        source_row = min(row for row, _ in row_coordinates.values())
        return source_row, 0
    return plan_index, 0


def _summarize(rows: list[PastePlanRow]) -> PasteSummary:
    update_rows = sum(1 for row in rows if row.kind == "update" and row.changes)
    insert_rows = sum(1 for row in rows if row.kind == "insert")
    skip_rows = sum(
        1 for row in rows if row.kind == "skip" or (row.kind == "update" and not row.changes)
    )
    error_count = sum(len([d for d in row.diagnostics if d.severity == "error"]) for row in rows)
    warning_count = sum(
        len([d for d in row.diagnostics if d.severity == "warning"]) for row in rows
    )
    return PasteSummary(
        update_rows=update_rows,
        insert_rows=insert_rows,
        skip_rows=skip_rows,
        error_count=error_count,
        warning_count=warning_count,
    )


def _build_bulk_payload(
    *,
    collection: str,
    profile: CollectionProfile,
    rows: list[PastePlanRow],
    row_revisions: dict[str | int, str],
    idempotency_key: str,
) -> dict[str, Any]:
    operations: list[dict[str, Any]] = []
    for row in rows:
        if row.kind == "skip":
            continue
        if row.kind == "update" and row.target_row_key is not None:
            operations.append(
                {
                    "kind": "update",
                    "primaryKey": str(row.target_row_key),
                    "expectedDateUpdated": row_revisions.get(row.target_row_key),
                    "values": {name: change["after"] for name, change in row.changes.items()},
                }
            )
        else:
            operations.append(
                {
                    "kind": "create",
                    "values": {name: change["after"] for name, change in row.changes.items()},
                }
            )
    return {
        "contract": "vibetable-bulk-mutation.v1",
        "collection": collection,
        "primaryKey": profile.primary_key,
        "idempotencyKey": idempotency_key,
        "operations": operations,
    }


def _parse_bulk_response(
    collection: str,
    response: Any,
    idempotency_key: str,
) -> ApplyPasteResult:
    if not isinstance(response, dict):
        raise PasteError("bulk endpoint returned an invalid response", code="bulk_invalid_response")
    data = response.get("data", response)
    if not isinstance(data, dict):
        raise PasteError("bulk endpoint returned an invalid response", code="bulk_invalid_response")
    conflicts = [
        ApplyPasteConflict(
            row_key=conflict["primaryKey"],
            current_value=conflict.get("currentValue", {}),
            expected_date_updated=conflict.get("expectedDateUpdated"),
        )
        for conflict in data.get("conflicts", [])
    ]
    if conflicts:
        return ApplyPasteResult(
            collection=collection,
            outcome="conflict",
            conflicts=conflicts,
            request_id=idempotency_key,
        )
    return ApplyPasteResult(
        collection=collection,
        outcome="committed",
        created_row_keys=data.get("createdRowKeys", []),
        updated_row_keys=data.get("updatedRowKeys", []),
        skipped_row_keys=data.get("skippedRowKeys", []),
        request_id=idempotency_key,
    )


__all__ = [
    "MAX_PASTE_CELLS",
    "TOKEN_TTL_SECONDS",
    "PasteError",
    "PasteMutationPort",
    "PasteService",
    "PasteTokenStore",
]
