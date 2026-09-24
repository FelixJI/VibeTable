"""Shared paste-shaped mutation port and errors still used by file import.

Production paste preview plans and single-use tokens are owned by Go. The old
Python implementation exists only in tests/backend/application/paste_oracle.py.
"""

from __future__ import annotations

from typing import Any, Protocol

from backend.contracts.data_profile import CollectionProfile
from backend.contracts.paste import ApplyPasteConflict, ApplyPasteResult, PastePlanRow


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


__all__ = [
    "ApplyPasteConflict",
    "ApplyPasteResult",
    "PasteError",
    "PasteMutationPort",
    "PastePlanRow",
]
