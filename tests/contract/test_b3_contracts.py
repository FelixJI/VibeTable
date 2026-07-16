"""Language-neutral contract tests for the B3 query/state fixtures.

Asserts the Python Pydantic models accept the exact wire shape stored in the
fixture files under ``tests/contract/fixtures``. The C# client
(``B3ContractsFixtureTests``) and the TS contracts pin the same shapes, so
these tests guard the cross-language contract.
"""

from __future__ import annotations

import json
from pathlib import Path

from backend.contracts.grid_state import GridState, GridStateResult
from backend.contracts.query import TableQuery
from backend.contracts.selection import QuerySnapshot, SelectionSnapshot

FIXTURES = Path(__file__).parent / "fixtures"


def _load(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


def test_table_query_fixture_is_valid_python_model() -> None:
    payload = _load("table-query.json")
    query = TableQuery.model_validate(payload)
    assert query.limit == 100
    assert len(query.filters) == 5
    assert len(query.sorts) == 1
    assert query.sorts[0].direction == "desc"


def test_table_query_fixture_serializes_camel_case() -> None:
    payload = _load("table-query.json")
    query = TableQuery.model_validate(payload)
    dumped = query.model_dump(by_alias=True, mode="json")
    # camelCase wire aliases present.
    assert "nullsLast" in json.dumps(dumped["sorts"][0])
    assert "filters" in dumped


def test_query_snapshot_fixture_is_valid_python_model() -> None:
    payload = _load("query-snapshot.json")
    snap = QuerySnapshot.model_validate(payload)
    assert snap.table == "contracts"
    assert snap.data_revision == 42
    assert len(snap.digest) == 16
    assert snap.normalized_query is not None


def test_selection_snapshot_fixture_is_valid_python_model() -> None:
    payload = _load("selection-snapshot.json")
    sel = SelectionSnapshot.model_validate(payload)
    assert sel.data_revision == 42
    assert sel.row_keys == [17, 23, 41]
    assert sel.query_snapshot.table == "contracts"


def test_grid_state_fixture_is_valid_python_model() -> None:
    payload = _load("grid-state.json")
    state = GridState.model_validate(payload)
    assert len(state.columns) == 2
    assert state.density == "comfortable"
    assert state.forced_remote is False
    assert len(state.sorts) == 1


def test_grid_state_result_round_trips() -> None:
    state = GridState(
        columns=[],
        density="compact",
        forced_remote=True,
    )
    result = GridStateResult(state=state, revision="rev-1", conflict=False)
    dumped = result.model_dump(by_alias=True, mode="json")
    assert dumped["revision"] == "rev-1"
    assert dumped["conflict"] is False
    # Round-trip back.
    restored = GridStateResult.model_validate(dumped)
    assert restored.state.density == "compact"
    assert restored.state.forced_remote is True
