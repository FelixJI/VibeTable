"""Paste/import adapter that compiles rows into the frozen MutationKernel wire."""

from __future__ import annotations

from typing import Any, Protocol

from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseProductError
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.application.paste_service import (
    ApplyPasteConflict,
    ApplyPasteResult,
    PasteError,
    PastePlanRow,
)
from backend.contracts.data_profile import CollectionProfile


class _CurrentUserProvider(Protocol):
    async def current_user(self) -> Any: ...


class PocketBaseBulkMutationClient:
    """Compatibility port for PasteService backed only by MutationKernel."""

    def __init__(self, *, client: PocketBaseClient, auth: _CurrentUserProvider) -> None:
        self._client = client
        self._auth = auth

    async def apply(
        self,
        *,
        collection: str,
        profile: CollectionProfile,
        rows: list[PastePlanRow],
        row_revisions: dict[str | int, str],
        idempotency_key: str,
        schema_revision: str | None = None,
    ) -> ApplyPasteResult:
        operations = _operations(rows, row_revisions)
        if not operations:
            return ApplyPasteResult(
                collection=collection,
                outcome="committed",
                skipped_row_keys=[
                    row.target_row_key
                    for row in rows
                    if row.kind == "skip" and row.target_row_key is not None
                ],
                request_id=idempotency_key,
            )
        user = await self._auth.current_user()
        request = {
            "contractVersion": "1.0",
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
            if not isinstance(raw, dict) or not isinstance(raw.get("recordId"), str):
                raise PasteError(
                    "PocketBase returned an invalid mutation receipt",
                    code="mutation_invalid_response",
                )
            if raw.get("operation") == "insert":
                created.append(raw["recordId"])
            elif raw.get("operation") == "update":
                updated.append(raw["recordId"])
        return ApplyPasteResult(
            collection=collection,
            outcome="committed",
            created_row_keys=created,
            updated_row_keys=updated,
            skipped_row_keys=[
                row.target_row_key
                for row in rows
                if row.kind == "skip" and row.target_row_key is not None
            ],
            request_id=idempotency_key,
        )


def _operations(
    rows: list[PastePlanRow],
    revisions: dict[str | int, str],
) -> list[dict[str, Any]]:
    operations: list[dict[str, Any]] = []
    for row in rows:
        values = {
            name: change.get("after")
            for name, change in row.changes.items()
            if isinstance(change, dict) and "after" in change
        }
        if row.kind == "update" and row.target_row_key is not None:
            operation: dict[str, Any] = {
                "kind": "update",
                "recordId": str(row.target_row_key),
                "values": values,
            }
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
                    "values": values,
                }
            )
    return operations


def _single_row_guard(
    rows: list[PastePlanRow],
    revisions: dict[str | int, str],
) -> str | None:
    writable = [row for row in rows if row.kind != "skip"]
    if len(writable) != 1:
        return None
    row = writable[0]
    if row.kind != "update" or row.target_row_key is None:
        return None
    value = revisions.get(row.target_row_key)
    return (
        value
        if isinstance(value, str)
        and (value.startswith("row_") or value.startswith("sha256:"))
        else None
    )


def _single_update_key(rows: list[PastePlanRow]) -> str | int | None:
    updates = [
        row.target_row_key
        for row in rows
        if row.kind == "update" and row.target_row_key is not None
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
