"""B3 Task 3: application service for durable per-table grid state.

``gridState.get({databaseId, table})`` and ``gridState.save({databaseId, table,
state, revision})`` are the RPC methods. The service:

* loads/saves through :class:`~backend.state.local_state_store.LocalStateStore`
  (the local user-state SQLite at ``%LOCALAPPDATA%/VibeTable/state/vibetable-state.db``);
* keeps device-local presentation state separate from Directus business data;
* leaves schema reconciliation to the Directus-aware host, which owns the
  current capability manifest.

The service is a process-wide singleton (mirrors the other B3 services).
"""

from __future__ import annotations

from backend.contracts.grid_state import (
    GridState,
    GridStateGetParams,
    GridStateResult,
    GridStateSaveParams,
)
from backend.state.local_state_store import get_local_state_store


class GridStateService:
    """Implements ``gridState.get`` / ``gridState.save``."""

    def __init__(self) -> None:
        self._store = get_local_state_store()

    async def get(self, params: GridStateGetParams) -> GridStateResult:
        """Load the saved grid state for ``(database_id, table)``.

        Returns an empty default state with a fresh revision when no state has
        been saved yet. Saved column state is returned as-is; the host applies
        the current Directus schema and defaults.
        """
        payload, revision = self._store.get_grid_state(
            database_id=params.database_id, table=params.table
        )
        if payload is None:
            # F stage: the legacy SQLite column-width import has been removed
            # (no business SQLite path remains). Default to an empty grid state.
            state = GridState()
            # Persist the imported/default state so subsequent gets are stable.
            new_rev, _conflict = self._store.save_grid_state(
                database_id=params.database_id,
                table=params.table,
                payload=state.model_dump(by_alias=True, mode="json"),
                revision=None,
            )
            return GridStateResult(state=state, revision=new_rev, conflict=False)
        state = GridState.model_validate(payload)
        return GridStateResult(state=state, revision=revision or "", conflict=False)

    async def save(self, params: GridStateSaveParams) -> GridStateResult:
        """Save grid state with optimistic-concurrency revision check.

        Returns ``conflict=True`` when ``revision`` is stale; the caller must
        re-read and retry. On success returns the new revision.
        """
        payload = params.state.model_dump(by_alias=True, mode="json")
        new_rev, conflict = self._store.save_grid_state(
            database_id=params.database_id,
            table=params.table,
            payload=payload,
            revision=params.revision,
        )
        if conflict:
            # Return the current stored state so the caller can merge.
            current_payload, current_rev = self._store.get_grid_state(
                database_id=params.database_id, table=params.table
            )
            current = (
                GridState.model_validate(current_payload)
                if current_payload is not None
                else GridState()
            )
            return GridStateResult(state=current, revision=current_rev or new_rev, conflict=True)
        return GridStateResult(state=params.state, revision=new_rev, conflict=False)


# ---------------------------------------------------------------------------
# Process-wide singleton
# ---------------------------------------------------------------------------

_SINGLETON: GridStateService | None = None


def get_grid_state_service() -> GridStateService:
    """Return the process-wide :class:`GridStateService` singleton."""
    global _SINGLETON
    if _SINGLETON is None:
        _SINGLETON = GridStateService()
    return _SINGLETON
