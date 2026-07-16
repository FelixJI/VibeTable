"""C1 data-IO contract fixture tests.

Validates the versioned ``table-c1-data-io-contracts.json`` fixture against the
Pydantic models in :mod:`backend.contracts.task`, :mod:`backend.contracts.data_io`
and :mod:`backend.contracts.relation`, pinning the cross-language wire shape.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.data_io import (
    ApplyImportParams,
    ApplyImportResult,
    ExportParams,
    ExportResult,
    ImportColumnMapping,
    ImportPlan,
    PreviewImportParams,
)
from backend.contracts.relation import (
    RelationColumn,
    RelationProjectionParams,
    RelationProjectionResult,
)
from backend.contracts.task import (
    CreateTaskParams,
    SessionPathGrant,
    TaskStatus,
)

FIXTURES = Path(__file__).parent / "fixtures"


def _load(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


def test_fixture_contract_header() -> None:
    fixture = _load("table-c1-data-io-contracts.json")
    assert fixture["contract"] == "table.c1.data-io.fixtures.v1"


def test_task_runtime_round_trip() -> None:
    fixture = _load("table-c1-data-io-contracts.json")
    block = fixture["taskRuntime"]
    params = CreateTaskParams.model_validate(block["createParams"])
    assert params.kind == "data.import"
    status = TaskStatus.model_validate(block["status"])
    assert status.state == "running"
    assert status.progress.done == 42
    assert status.progress.total == 100


def test_path_grant_descriptor_round_trip() -> None:
    fixture = _load("table-c1-data-io-contracts.json")
    grant = SessionPathGrant.model_validate(fixture["pathGrant"]["descriptor"])
    assert grant.purpose == "import_source"
    assert grant.direction == "read"
    assert grant.size_bytes == 18432
    # The descriptor must not leak a raw filesystem path.
    assert "/" not in grant.display_name


def test_import_plan_round_trip() -> None:
    fixture = _load("table-c1-data-io-contracts.json")
    block = fixture["importPlan"]
    params = PreviewImportParams.model_validate(block["params"])
    assert params.mode == "create_only"
    assert isinstance(params.column_mapping[0], ImportColumnMapping)
    plan = ImportPlan.model_validate(block["plan"])
    assert plan.summary.total_rows == 3
    assert plan.summary.error_rows == 1
    assert plan.rows[2].diagnostics[0].severity == "error"
    assert plan.unmatched_columns == ["备注"]
    assert plan.token.token == "imp-fixture-token"


def test_apply_import_round_trip() -> None:
    fixture = _load("table-c1-data-io-contracts.json")
    block = fixture["applyImport"]
    params = ApplyImportParams.model_validate(block["params"])
    assert params.chunk_size == 500
    result = ApplyImportResult.model_validate(block["result"])
    assert result.created_count == 2
    assert result.failed_rows == [4]
    assert len(result.chunks) == 1
    assert result.chunks[0].created_row_keys == ["c1", "c2"]


def test_export_round_trip() -> None:
    fixture = _load("table-c1-data-io-contracts.json")
    block = fixture["export"]
    params = ExportParams.model_validate(block["params"])
    assert params.format == "csv"
    assert params.include_relations is True
    result = ExportResult.model_validate(block["result"])
    assert result.rows_written == 150
    assert result.output_display_name == "contracts-export.csv"


def test_relation_projection_round_trip() -> None:
    fixture = _load("table-c1-data-io-contracts.json")
    block = fixture["relationProjection"]
    params = RelationProjectionParams.model_validate(block["params"])
    assert params.relations == ["project"]
    assert params.max_depth == 1
    result = RelationProjectionResult.model_validate(block["result"])
    assert result.rows[0]["project"]["code"] == "P1"
    paths = [c.display_path for c in result.relation_columns]
    assert "project.code" in paths
    assert "project.name" in paths
    assert isinstance(result.relation_columns[0], RelationColumn)


if __name__ == "__main__":
    pytest.main([__file__, "-q"])
