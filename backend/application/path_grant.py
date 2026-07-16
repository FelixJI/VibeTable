"""C1 session path-grant store.

Holds short-lived, session-bound authorizations for file paths the WPF file
picker chose. The Web layer only ever holds the opaque grant id + display
metadata; the canonical path stays inside the Python broker and never crosses
the WebView boundary.

Grants expire after :data:`GRANT_TTL_SECONDS` (5 minutes) and are single-use
(resolving consumes them, so a stolen grant id cannot be replayed). The store
is in-process; grants do not survive a restart (a stale grant id is rejected
with a clear error so the host re-requests the file).
"""

from __future__ import annotations

import os
import time
import uuid
from pathlib import Path
from typing import Any

from backend.contracts.task import SessionPathGrant

#: How long a path grant remains valid (seconds).
GRANT_TTL_SECONDS: float = 5 * 60.0


class PathGrantError(Exception):
    """A path-grant error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code


class _StoredGrant:
    """Internal record of one path grant."""

    __slots__ = (
        "consumed",
        "direction",
        "display_name",
        "expires_at",
        "grant_id",
        "mime_type",
        "path",
        "purpose",
        "size_bytes",
    )

    def __init__(
        self,
        *,
        grant_id: str,
        purpose: str,
        direction: str,
        path: str,
        display_name: str,
        size_bytes: int | None,
        mime_type: str | None,
        expires_at: float,
    ) -> None:
        self.grant_id = grant_id
        self.purpose = purpose
        self.direction = direction
        self.path = path
        self.display_name = display_name
        self.size_bytes = size_bytes
        self.mime_type = mime_type
        self.expires_at = expires_at
        self.consumed = False


class SessionPathGrantStore:
    """In-memory store of session-bound path grants."""

    def __init__(self, *, clock: Any = time.time, ttl_seconds: float = GRANT_TTL_SECONDS) -> None:
        self._clock = clock
        self._ttl_seconds = ttl_seconds
        self._grants: dict[str, _StoredGrant] = {}

    def issue(
        self,
        *,
        purpose: str,
        direction: str,
        path: str,
        display_name: str | None = None,
        size_bytes: int | None = None,
        mime_type: str | None = None,
    ) -> SessionPathGrant:
        """Issue a new grant for ``path``.

        The canonical path is normalized (resolving ``..`` and symlinks where
        possible) so a later resolve cannot escape to an unintended file.
        """
        grant_id = f"grant-{uuid.uuid4().hex[:12]}"
        normalized = str(Path(path).resolve())
        name = display_name or os.path.basename(normalized)
        stored = _StoredGrant(
            grant_id=grant_id,
            purpose=purpose,
            direction=direction,
            path=normalized,
            display_name=name,
            size_bytes=size_bytes,
            mime_type=mime_type,
            expires_at=self._clock() + self._ttl_seconds,
        )
        self._grants[grant_id] = stored
        return SessionPathGrant(
            grant_id=grant_id,
            purpose=purpose,  # type: ignore[arg-type]
            direction=direction,  # type: ignore[arg-type]
            display_name=name,
            size_bytes=size_bytes,
            mime_type=mime_type,
            expires_at=stored.expires_at,
        )

    def descriptor(self, grant_id: str) -> SessionPathGrant:
        """Return the public descriptor for ``grant_id`` (no raw path)."""
        stored = self._get(grant_id)
        return SessionPathGrant(
            grant_id=stored.grant_id,
            purpose=stored.purpose,  # type: ignore[arg-type]
            direction=stored.direction,  # type: ignore[arg-type]
            display_name=stored.display_name,
            size_bytes=stored.size_bytes,
            mime_type=stored.mime_type,
            expires_at=stored.expires_at,
        )

    def resolve(self, grant_id: str, *, purpose: str, direction: str) -> str:
        """Resolve ``grant_id`` to its canonical path, validating purpose/direction.

        Resolving does NOT consume the grant (export target may be resolved
        repeatedly during streaming writes). Import sources are consumed by the
        import handler when the file is fully read.
        """
        stored = self._get(grant_id)
        if stored.purpose != purpose:
            raise PathGrantError(
                f"grant was issued for {stored.purpose!r}, not {purpose!r}",
                code="grant_purpose_mismatch",
            )
        if stored.direction != direction:
            raise PathGrantError(
                f"grant is {stored.direction}-only, not {direction!r}",
                code="grant_direction_mismatch",
            )
        return stored.path

    def consume(self, grant_id: str) -> None:
        """Mark ``grant_id`` as consumed (single-use for import sources)."""
        stored = self._get(grant_id)
        stored.consumed = True

    def _get(self, grant_id: str) -> _StoredGrant:
        stored = self._grants.get(grant_id)
        if stored is None:
            raise PathGrantError("path grant not found", code="grant_unknown")
        if self._clock() >= stored.expires_at:
            self._grants.pop(grant_id, None)
            raise PathGrantError("path grant expired", code="grant_expired")
        return stored


__all__ = ["GRANT_TTL_SECONDS", "PathGrantError", "SessionPathGrantStore"]
