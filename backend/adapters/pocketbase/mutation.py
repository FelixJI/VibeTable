"""Paste/import adapter that compiles rows into the frozen MutationKernel wire."""

from __future__ import annotations

import secrets as pysecrets
from typing import Any, Protocol

from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseProductError
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.application.paste_service import (
    ApplyPasteConflict,
    ApplyPasteResult,
    PasteError,
    PastePlanRow,
)
from backend.application.revisioned_metadata_port import json_object
from backend.contracts.data_profile import CollectionProfile


class _CurrentUserProvider(Protocol):
    async def current_user(self) -> Any: ...


class PocketBaseBulkMutationClient:
    """Compatibility port for PasteService backed only by MutationKernel."""

    def __init__(self, *, client: PocketBaseClient, auth: _CurrentUserProvider) -> None:
        self._client = client
        self._auth = auth

    async def preview_import(
        self,
        *,
        collection: str,
        schema_revision: str,
        rows: list[dict[str, Any]],
        row_modes: list[str] | None = None,
    ) -> dict[str, Any]:
        modes = row_modes or ["insert"] * len(rows)
        if len(modes) != len(rows):
            raise ValueError("row_modes must align with rows")
        return await self._client.preview_import(
            {
                "contract": "vibetable.import-preview.v1",
                "tableId": collection,
                "schemaRevision": schema_revision,
                "rows": [
                    {"values": row, "mode": mode} for row, mode in zip(rows, modes, strict=True)
                ],
            }
        )

    async def preview_paste(
        self,
        *,
        collection: str,
        profile: CollectionProfile,
        rows: list[PastePlanRow],
        raw_rows: list[dict[str, Any]],
        row_revisions: dict[str | int, str],
        schema_revision: str,
    ) -> None:
        operations = _operations(rows, row_revisions, raw_rows=raw_rows)
        if not operations:
            return
        user = await self._auth.current_user()
        request_id = "paste-preview-" + pysecrets.token_hex(12)
        request = json_object(
            {
                "contractVersion": "2.0",
                "requestId": request_id,
                "idempotencyKey": request_id,
                "tableId": collection,
                "schemaRevision": schema_revision or profile.capability_hash,
                "operations": operations,
                "actor": {
                    "type": "user",
                    "id": str(getattr(user, "id", "") or "local-user"),
                    "displayName": _display_name(user),
                },
                "expectedRevision": None,
                "expectedDigest": None,
            }
        )
        try:
            await self._client.preview_mutation(request)
        except PocketBaseProductError as exc:
            raise PasteError(
                str(exc),
                code=exc.code,
                data=exc.rpc_error_data,
            ) from exc

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
    ) -> ApplyPasteResult:
        operations = _operations(rows, row_revisions, raw_rows=raw_rows)
        if not operations:
            return ApplyPasteResult(
                collection=collection,
                outcome="committed",
                skipped_row_keys=_skipped_row_keys(rows),
                request_id=idempotency_key,
            )
        user = await self._auth.current_user()
        request = json_object(
            {
                "contractVersion": "2.0",
                "requestId": idempotency_key,
                "idempotencyKey": idempotency_key,
                "tableId": collection,
                "schemaRevision": schema_revision or profile.capability_hash,
                "operations": operations,
                "actor": {
                    "type": "user",
                    "id": str(getattr(user, "id", "") or "local-user"),
                    "displayName": _display_name(user),
                },
                "expectedRevision": None,
                "expectedDigest": None,
            }
        )
        try:
            receipt = await self._client.apply_mutation(request)
        except PocketBaseProductError as exc:
            if exc.code in {
                "mutation.digest_conflict",
                "mutation.revision_conflict",
                "mutation.schema_revision_conflict",
                "mutation.record.not_found",
            }:
                row_key = _single_update_key(rows) or ""
                return ApplyPasteResult(
                    collection=collection,
                    outcome="conflict",
                    conflicts=[
                        ApplyPasteConflict(
                            row_key=row_key,
                            current_value=dict(exc.details),
                            expected_date_updated=_single_row_guard(
                                rows,
                                row_revisions,
                            ),
                        )
                    ],
                    request_id=idempotency_key,
                )
            raise PasteError(
                "PocketBase rejected the mutation",
                code=exc.code,
                data=exc.rpc_error_data,
            ) from exc
        except PocketBaseTransportError:
            # The request may already be committed. The caller retries the same
            # idempotency key and receives the original MutationKernel receipt.
            return ApplyPasteResult(
                collection=collection,
                outcome="pending",
                request_id=idempotency_key,
            )
        status = receipt.get("status")
        if status == "pending":
            return ApplyPasteResult(
                collection=collection,
                outcome="pending",
                request_id=idempotency_key,
            )
        if status not in {"applied", "replayed"}:
            raise PasteError(
                "PocketBase returned an invalid mutation receipt",
                code="mutation_invalid_response",
            )
        created: list[str | int] = []
        updated: list[str | int] = []
        affected = receipt.get("affectedRows")
        if not isinstance(affected, list):
            raise PasteError(
                "PocketBase returned an invalid mutation receipt",
                code="mutation_invalid_response",
            )
        for raw in affected:
            if not isinstance(raw, dict):
                raise PasteError(
                    "PocketBase returned an invalid mutation receipt",
                    code="mutation_invalid_response",
                )
            record_id = raw.get("recordId")
            if not isinstance(record_id, str):
                raise PasteError(
                    "PocketBase returned an invalid mutation receipt",
                    code="mutation_invalid_response",
                )
            if raw.get("operation") == "insert":
                created.append(record_id)
            elif raw.get("operation") == "update":
                updated.append(record_id)
        return ApplyPasteResult(
            collection=collection,
            outcome="committed",
            created_row_keys=created,
            updated_row_keys=updated,
            skipped_row_keys=_skipped_row_keys(rows),
            request_id=idempotency_key,
        )


def _operations(
    rows: list[PastePlanRow],
    revisions: dict[str | int, str],
    *,
    raw_rows: list[dict[str, Any]] | None = None,
) -> list[dict[str, Any]]:
    operations: list[dict[str, Any]] = []
    for index, row in enumerate(rows):
        values = {
            name: change.get("after")
            for name, change in row.changes.items()
            if isinstance(change, dict) and "after" in change
        }
        raw_values = raw_rows[index] if raw_rows is not None else None
        if row.kind == "update" and not (raw_values if raw_values is not None else values):
            continue
        if row.kind == "update" and row.target_row_key is not None:
            operation: dict[str, Any] = {
                "kind": "update",
                "recordId": str(row.target_row_key),
                "values": {} if raw_values is not None else values,
            }
            if raw_values is not None:
                operation["rawValues"] = raw_values
            guard = revisions.get(row.target_row_key)
            if isinstance(guard, str) and guard.startswith("row_"):
                operation["expectedRevision"] = guard
            elif isinstance(guard, str) and guard.startswith("sha256:"):
                operation["expectedDigest"] = guard
            operations.append(operation)
        elif row.kind == "insert":
            operations.append(
                {
                    "kind": "insert",
                    "recordId": None,
                    "values": {} if raw_values is not None else values,
                    **({"rawValues": raw_values} if raw_values is not None else {}),
                }
            )
    return operations


def _single_row_guard(
    rows: list[PastePlanRow],
    revisions: dict[str | int, str],
) -> str | None:
    writable = [
        row for row in rows if row.kind == "insert" or (row.kind == "update" and bool(row.changes))
    ]
    if len(writable) != 1:
        return None
    row = writable[0]
    if row.kind != "update" or row.target_row_key is None:
        return None
    value = revisions.get(row.target_row_key)
    return (
        value
        if isinstance(value, str) and (value.startswith("row_") or value.startswith("sha256:"))
        else None
    )


def _skipped_row_keys(rows: list[PastePlanRow]) -> list[str | int]:
    return [
        row.target_row_key
        for row in rows
        if row.target_row_key is not None
        and (row.kind == "skip" or (row.kind == "update" and not row.changes))
    ]


def _single_update_key(rows: list[PastePlanRow]) -> str | int | None:
    updates = [
        row.target_row_key
        for row in rows
        if row.kind == "update" and row.changes and row.target_row_key is not None
    ]
    return updates[0] if len(updates) == 1 else None


def _display_name(user: Any) -> str | None:
    parts = [
        str(value).strip()
        for value in (getattr(user, "first_name", None), getattr(user, "last_name", None))
        if value
    ]
    return " ".join(parts) or None


__all__ = ["PocketBaseBulkMutationClient"]
