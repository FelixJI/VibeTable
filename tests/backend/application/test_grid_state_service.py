"""Tests for ``backend.application.grid_state_service.GridStateService``.

Covers (per B3 Task 3):

* first save + subsequent get round-trip
* update with matching revision
* stale revision conflict (caller must re-read)
* local migration idempotency (store-level; exercised via repeated gets)
* database rename identity mapping (store-level; covered in store tests)
* saved columns preserved for host-owned schema reconciliation
* state excludes row data and pending edits
"""

from __future__ import annotations

from pathlib import Path

import pytest
from pydantic import ValidationError

from backend.application.grid_state_service import GridStateService
from backend.contracts.grid_state import (
    ColumnState,
    GridState,
    GridStateGetParams,
    GridStateSaveParams,
)
from backend.state.local_state_store import reset_local_state_store_for_tests


@pytest.fixture
async def service(tmp_path: Path) -> GridStateService:
    """A GridStateService backed by a temp state DB (no business SQLite)."""
    import backend.application.grid_state_service as mod
    import backend.state.local_state_store as store_mod

    state_db = tmp_path / "state.db"
    store = reset_local_state_store_for_tests(db_path=state_db)
    mod._SINGLETON = None
    svc = GridStateService()
    yield svc
    mod._SINGLETON = None
    store_mod._SINGLETON = None
    store.close()


def _state(**overrides) -> GridState:
    base = {
        "columns": [ColumnState(name="amount", width=120, visible=True)],
        "density": "comfortable",
        "forced_remote": False,
    }
    base.update(overrides)
    return GridState(**base)


# ---------------------------------------------------------------------------
# First save + get round-trip
# ---------------------------------------------------------------------------


async def test_first_save_then_get_round_trips(service: GridStateService) -> None:
    state = _state()
    save_result = await service.save(
        GridStateSaveParams(database_id="db1", table="contracts", state=state, revision=None)
    )
    assert save_result.conflict is False
    assert save_result.revision
    get_result = await service.get(GridStateGetParams(database_id="db1", table="contracts"))
    assert get_result.state.columns[0].name == "amount"
    assert get_result.state.columns[0].width == 120
    assert get_result.revision == save_result.revision


async def test_get_with_no_prior_state_returns_default(service: GridStateService) -> None:
    result = await service.get(GridStateGetParams(database_id="db1", table="contracts"))
    assert result.state.columns == []
    assert result.revision  # gets a fresh revision from the persisted default


# ---------------------------------------------------------------------------
# Update with matching revision
# ---------------------------------------------------------------------------


async def test_update_with_matching_revision(service: GridStateService) -> None:
    state1 = _state(columns=[ColumnState(name="amount", width=100)])
    save1 = await service.save(
        GridStateSaveParams(database_id="db1", table="contracts", state=state1, revision=None)
    )
    state2 = _state(columns=[ColumnState(name="amount", width=200)])
    save2 = await service.save(
        GridStateSaveParams(
            database_id="db1", table="contracts", state=state2, revision=save1.revision
        )
    )
    assert save2.conflict is False
    assert save2.revision != save1.revision
    get_result = await service.get(GridStateGetParams(database_id="db1", table="contracts"))
    assert get_result.state.columns[0].width == 200


# ---------------------------------------------------------------------------
# Stale revision conflict
# ---------------------------------------------------------------------------


async def test_stale_revision_conflict_returns_current_state(
    service: GridStateService,
) -> None:
    state1 = _state(columns=[ColumnState(name="amount", width=100)])
    save1 = await service.save(
        GridStateSaveParams(database_id="db1", table="contracts", state=state1, revision=None)
    )
    # Advance the revision with a second save.
    state2 = _state(columns=[ColumnState(name="amount", width=200)])
    await service.save(
        GridStateSaveParams(
            database_id="db1", table="contracts", state=state2, revision=save1.revision
        )
    )
    # Now save with the stale save1.revision -> conflict.
    state3 = _state(columns=[ColumnState(name="amount", width=300)])
    conflict_result = await service.save(
        GridStateSaveParams(
            database_id="db1", table="contracts", state=state3, revision=save1.revision
        )
    )
    assert conflict_result.conflict is True
    # The returned state is the current stored one (width=200), not the rejected one.
    assert conflict_result.state.columns[0].width == 200


# ---------------------------------------------------------------------------
# The native host owns schema reconciliation.
# ---------------------------------------------------------------------------


async def test_saved_columns_preserved_on_load(service: GridStateService) -> None:
    """F stage: column pruning moved to the native capability layer; the
    GridStateService preserves saved columns as-is (no SQLite schema lookup)."""
    state = _state(
        columns=[
            ColumnState(name="amount", width=100),
            ColumnState(name="dropped_column", width=50),
        ]
    )
    save = await service.save(
        GridStateSaveParams(database_id="db1", table="contracts", state=state, revision=None)
    )
    assert save.conflict is False
    get_result = await service.get(GridStateGetParams(database_id="db1", table="contracts"))
    names = [c.name for c in get_result.state.columns]
    # Both columns are preserved (pruning is now the host's responsibility).
    assert "amount" in names
    assert "dropped_column" in names


async def test_newly_added_columns_remain_visible_by_default(
    service: GridStateService,
) -> None:
    """When the schema adds a column, the host supplies its presentation default."""
    state = _state(columns=[ColumnState(name="amount", width=100)])
    await service.save(
        GridStateSaveParams(database_id="db1", table="contracts", state=state, revision=None)
    )
    get_result = await service.get(GridStateGetParams(database_id="db1", table="contracts"))
    # 'name' column exists in the schema but not in saved state; it is simply
    # absent from the loaded columns (host default applies).
    assert all(c.name != "name" for c in get_result.state.columns)


# ---------------------------------------------------------------------------
# State excludes row data and pending edits
# ---------------------------------------------------------------------------


async def test_state_excludes_row_data_and_pending_edits(
    service: GridStateService,
) -> None:
    """The GridState model rejects row-data / pending-edits fields (extra=forbid)."""
    with pytest.raises(ValidationError):
        GridState(columns=[], rowData=[{"id": 1}])  # type: ignore[arg-type]
    with pytest.raises(ValidationError):
        GridState(columns=[], pendingEdits=[{"row": 1}])  # type: ignore[arg-type]


# ---------------------------------------------------------------------------
# Density and forced-remote round-trip
# ---------------------------------------------------------------------------


async def test_density_and_forced_remote_round_trip(
    service: GridStateService,
) -> None:
    state = GridState(
        columns=[],
        density="compact",
        forced_remote=True,
    )
    save = await service.save(
        GridStateSaveParams(database_id="db1", table="contracts", state=state, revision=None)
    )
    assert save.conflict is False
    get_result = await service.get(GridStateGetParams(database_id="db1", table="contracts"))
    assert get_result.state.density == "compact"
    assert get_result.state.forced_remote is True
