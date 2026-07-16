"""B2 paste contract fixture tests.

Validates the versioned ``table-b2-paste-contracts.json`` fixture against the
Pydantic models in :mod:`backend.contracts.paste`, so the on-the-wire shape
stays pinned across the Python / C# / TypeScript layers.

This mirrors the B3 / B4 contract-test pattern: load the fixture, validate it
through the model constructors, and assert the key field semantics.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

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

FIXTURES = Path(__file__).parent / "fixtures"


def _load(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


def test_fixture_contract_header() -> None:
    fixture = _load("table-b2-paste-contracts.json")
    assert fixture["contract"] == "table.paste.fixtures.v1"
    assert fixture["schemaVersion"] == "vibetable-1.0"


def test_preview_params_round_trip() -> None:
    fixture = _load("table-b2-paste-contracts.json")
    params = PreviewPasteParams.model_validate(fixture["preview"]["params"])
    assert params.collection == "vibetable_demo"
    assert params.start_cell.column == "number"
    assert len(params.cells) == 2
    first_cell = params.cells[0][0]
    assert isinstance(first_cell, PasteCell)
    assert first_cell.raw_value == "B-1"
    assert first_cell.parsed_value == "B-1"


def test_preview_plan_round_trip() -> None:
    fixture = _load("table-b2-paste-contracts.json")
    plan = PastePlan.model_validate(fixture["preview"]["plan"])
    assert plan.capability_hash == "fixture-capability-hash"
    assert plan.summary.update_rows == 2
    assert plan.rows[0].kind == "update"
    assert plan.rows[0].target_row_key == "contract-1"
    assert plan.rows[0].expected_date_updated == "2026-07-14T00:00:00Z"
    assert plan.rows[0].changes["number"]["after"] == "B-1"
    assert isinstance(plan.token, PasteToken)
    assert plan.overflow is False


def test_validation_errors_carry_localized_diagnostics() -> None:
    fixture = _load("table-b2-paste-contracts.json")
    errors_block = fixture["validationErrors"]
    summary = PasteSummary.model_validate(errors_block["summary"])
    assert summary.error_count == 2
    row = PastePlanRow.model_validate(errors_block["rows"][0])
    diagnostic = row.diagnostics[0]
    assert isinstance(diagnostic, PasteCellDiagnostic)
    assert diagnostic.severity == "error"
    assert diagnostic.code == "column_readonly"


def test_overflow_marker_carries_cap_and_count() -> None:
    fixture = _load("table-b2-paste-contracts.json")
    overflow = fixture["overflow"]
    assert overflow["cellCount"] == 10001
    assert overflow["error"]["code"] == "paste_overflow"
    assert overflow["error"]["data"]["maxCells"] == 10000


def test_apply_committed_round_trip() -> None:
    fixture = _load("table-b2-paste-contracts.json")
    block = fixture["applyCommitted"]
    params = ApplyPasteParams.model_validate(block["params"])
    assert params.idempotency_key == "idem-1"
    result = ApplyPasteResult.model_validate(block["result"])
    assert result.outcome == "committed"
    assert result.updated_row_keys == ["contract-1", "contract-2"]
    assert result.conflicts == []


def test_apply_conflict_round_trip() -> None:
    fixture = _load("table-b2-paste-contracts.json")
    result = ApplyPasteResult.model_validate(fixture["applyConflict"]["result"])
    assert result.outcome == "conflict"
    assert len(result.conflicts) == 1
    conflict = result.conflicts[0]
    assert isinstance(conflict, ApplyPasteConflict)
    assert conflict.row_key == "contract-1"
    assert conflict.current_value["number"] == "A-9"


def test_apply_pending_round_trip() -> None:
    fixture = _load("table-b2-paste-contracts.json")
    result = ApplyPasteResult.model_validate(fixture["applyPending"]["result"])
    assert result.outcome == "pending"
    assert result.request_id == "idem-3"


def test_start_cell_anchor_is_optional() -> None:
    # A null rowKey signals an append (paste past the last row).
    start = PasteStartCell.model_validate({"rowKey": None, "column": "number"})
    assert start.row_key is None


if __name__ == "__main__":
    pytest.main([__file__, "-q"])
