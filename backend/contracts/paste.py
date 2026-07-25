"""B2 multi-row paste contracts: ``table.previewPaste`` / ``table.applyPaste``.

These contracts describe the transparent preview + idempotent batch write process
that turns a clipboard TSV grid into a server-confirmed result summary.

The process is two-phase and bounded:

* **preview** (no writes) — the host submits the parsed clipboard (rows of
  cells), the target collection, the current selection snapshot and the schema
  revision it rendered against. The service resolves each cell against the
  collection capability profile (editable field allow-list, type coercion,
  permission checks) and the *current* row revisions, and returns a
  :class:`PastePlan` describing exactly what will change plus a single-use
  :class:`PasteToken` binding the plan.
* **apply** (writes) — the host submits the token; the service delegates the
  atomic batch to the product mutation kernel. The kernel applies every change
  in one server transaction (all-or-nothing),
  honours an idempotency key for safe retries, and returns a confirmed result
  summary.

Wire conventions
----------------
* Field aliases use ``camelCase``; Python attributes stay ``snake_case``.
* ``populate_by_name=True`` accepts both forms.
* ``extra="forbid"`` rejects unknown keys so a stale client cannot silently
  drop a field.

Design notes
------------
* The token is **opaque** to the host (a signed, server-bound handle). It never
  carries enough material to replay the write without the server's secret.
* ``PastePlan`` classifies every target row as ``update`` / ``insert`` /
  ``skip`` and attaches per-cell errors/warnings so the UI can show precise,
  locatable feedback before the user confirms.
* Errors and warnings are co-located per cell so the confirm UI can block
  submission when any error is present and require explicit acknowledgement of
  warnings.
"""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    """Shared base for paste-domain contracts."""

    model_config = _camel_config()


#: Why a single paste row was classified the way it was. ``skip`` covers
#: read-only columns, out-of-range columns, empty source cells and rows that do
#: not resolve to a real target (e.g. an out-of-range anchor past the last row
#: when the collection does not allow appends).
PasteRowKind = Literal["update", "insert", "skip"]


#: Severity for a paste-cell diagnostic. ``error`` blocks submission; ``warning``
#: requires explicit user acknowledgement before the plan may be applied.
PasteDiagnosticSeverity = Literal["error", "warning"]


class PasteCell(CamelModel):
    """One cell produced by clipboard parsing.

    ``row_index`` / ``column_index`` are 0-based offsets into the parsed
    clipboard rectangle (not the collection row id); the host uses them to map a
    diagnostic back onto the highlighted source rectangle. ``column`` is the
    resolved collection field name the paste targets (``null`` when the column
    could not be resolved, e.g. an out-of-range column).
    """

    row_index: int = Field(ge=0)
    column_index: int = Field(ge=0)
    column: str | None = Field(default=None, max_length=128)
    raw_value: str
    parsed_value: Any = None


class PreviewPasteParams(CamelModel):
    """Parameters for ``table.previewPaste``.

    Wire form::

        {"collection": "vibetable_demo", "schemaRevision": "...",
         "selection": {...}, "startCell": {"rowKey": "...", "column": "number"},
         "cells": [[{...}, {...}], ...]}

    ``selection`` is the B3 selection snapshot the host rendered; it locates the
    paste anchor across remote pages and carries the row keys the update rows
    map onto. ``start_cell`` is the grid anchor the rectangle's top-left corner
    aligns to. ``cells`` is a list of rows, each a list of :class:`PasteCell`.
    """

    collection: str = Field(min_length=1, max_length=128)
    schema_revision: str = Field(min_length=1)
    selection: Any
    start_cell: PasteStartCell
    cells: list[list[PasteCell]] = Field(min_length=1, max_length=10000)


class PasteStartCell(CamelModel):
    """The grid anchor the parsed rectangle's top-left corner aligns to.

    ``row_key`` is the transport key of the anchor row (``null`` when the user
    anchors the paste past the last row, signalling an append). ``column`` is
    the editable collection field the leftmost clipboard column maps onto.
    """

    row_key: str | int | None = None
    column: str = Field(min_length=1, max_length=128)


class PasteCellDiagnostic(CamelModel):
    """A localized diagnostic for one paste cell.

    ``severity`` is ``error`` (blocks apply) or ``warning`` (requires
    confirmation). ``message`` is UI-ready text. The coordinates mirror the
    source :class:`PasteCell` so the host can highlight the offending cell.
    """

    row_index: int = Field(ge=0)
    column_index: int = Field(ge=0)
    severity: PasteDiagnosticSeverity
    code: str = Field(min_length=1, max_length=64)
    message: str = Field(min_length=1, max_length=512)


class PastePlanRow(CamelModel):
    """One planned change to a target row.

    For ``update`` rows, ``target_row_key`` is the resolved row and
    ``expected_date_updated`` is the revision the server must still observe when
    the plan is applied. For ``insert`` rows, ``target_row_key`` is ``null``.
    ``changes`` maps field name -> normalized ``(before, after)`` value pairs
    (empty for ``skip`` rows). ``diagnostics`` carries any localized
    errors/warnings for this row.
    """

    kind: PasteRowKind
    target_row_key: str | int | None = None
    expected_date_updated: str | None = None
    changes: dict[str, dict[str, Any]] = Field(default_factory=dict)
    diagnostics: list[PasteCellDiagnostic] = Field(default_factory=list)


class PasteSummary(CamelModel):
    """Aggregated counts for a :class:`PastePlan`.

    The confirm UI shows these alongside the per-row diagnostics so the user
    can see the real impact size before committing.
    """

    update_rows: int = Field(ge=0)
    insert_rows: int = Field(ge=0)
    skip_rows: int = Field(ge=0)
    error_count: int = Field(ge=0)
    warning_count: int = Field(ge=0)


class PasteToken(CamelModel):
    """An opaque, single-use, server-bound handle authorizing one apply.

    Wire form::

        {"token": "opaque-string", "expiresAt": 1.6e9, "consumed": false}

    The token is bound (server-side) to the user, project, collection, schema
    hash, target row keys/revisions and the payload hash. The host must not
    interpret its contents. ``expires_at`` is a Unix timestamp (seconds).
    """

    token: str = Field(min_length=1, max_length=2048)
    expires_at: float
    consumed: bool = False


class PastePlan(CamelModel):
    """Result of ``table.previewPaste``.

    ``capability_hash`` / ``schema_revision`` mirror the values the host used so
    it can detect a stale plan if the capability changed while the user was
    reviewing it. ``summary`` is the aggregate; ``rows`` is the per-target plan;
    ``diagnostics`` carries plan-level errors that are not tied to one cell
    (e.g. schema mismatch, selection invalidated). ``overflow`` is set when the
    parsed clipboard exceeded the 10k cell hard cap; the host must redirect the
    user to the C1 file-import path instead of applying.
    """

    collection: str = Field(min_length=1, max_length=128)
    schema_revision: str = Field(min_length=1)
    capability_hash: str = Field(min_length=1)
    summary: PasteSummary
    rows: list[PastePlanRow] = Field(min_length=1, max_length=10000)
    diagnostics: list[PasteCellDiagnostic] = Field(default_factory=list)
    token: PasteToken
    overflow: bool = False


class ApplyPasteParams(CamelModel):
    """Parameters for ``table.applyPaste``.

    Wire form:: ``{"collection": "...", "token": "...", "idempotencyKey": "..."}``

    ``idempotency_key`` is a client-generated UUID the host reuses on a retry so
    the server can return the original result instead of replaying the write.
    """

    collection: str = Field(min_length=1, max_length=128)
    token: str = Field(min_length=1, max_length=2048)
    idempotency_key: str = Field(min_length=1, max_length=128)


#: Outcome of an apply. ``committed`` is the all-or-nothing success case;
#: ``conflict`` means one or more target rows changed since preview and the user
#: must re-preview; ``pending`` means the request timed out and the confirmed
#: result is unknown (the host polls by idempotency key).
ApplyOutcome = Literal["committed", "conflict", "pending"]


class ApplyPasteConflict(CamelModel):
    """One row that blocked an apply because its revision changed.

    The host shows the current value and asks the user to re-preview rather than
    silently overwriting.
    """

    row_key: str | int
    current_value: dict[str, Any]
    expected_date_updated: str | None = None


class ApplyPasteResult(CamelModel):
    """Result of ``table.applyPaste``.

    ``outcome`` is the authoritative status. ``committed`` counts are
    server-confirmed (never client-optimistic). ``conflicts`` is populated only
    for the ``conflict`` outcome. ``request_id`` lets the host correlate with
    the idempotency key when polling a ``pending`` outcome.
    """

    collection: str = Field(min_length=1, max_length=128)
    outcome: ApplyOutcome
    created_row_keys: list[str | int] = Field(default_factory=list)
    updated_row_keys: list[str | int] = Field(default_factory=list)
    skipped_row_keys: list[str | int] = Field(default_factory=list)
    conflicts: list[ApplyPasteConflict] = Field(default_factory=list)
    request_id: str = Field(default="", max_length=128)


# Resolve the forward reference for PreviewPasteParams.start_cell.
PreviewPasteParams.model_rebuild()


__all__ = [
    "ApplyOutcome",
    "ApplyPasteConflict",
    "ApplyPasteParams",
    "ApplyPasteResult",
    "CamelModel",
    "PasteCell",
    "PasteCellDiagnostic",
    "PasteDiagnosticSeverity",
    "PastePlan",
    "PastePlanRow",
    "PasteRowKind",
    "PasteStartCell",
    "PasteSummary",
    "PasteToken",
    "PreviewPasteParams",
]
