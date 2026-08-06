from __future__ import annotations

import pytest
from pydantic import ValidationError

from backend.contracts.query import QueryViewResult


def _view_result() -> dict[str, object]:
    return {
        "page": {
            "rows": [{"id": "row-1"}],
            "offset": 0,
            "limit": 100,
            "filteredRows": 9,
            "totalRows": 9,
            "snapshot": {
                "snapshotId": "snapshot-1",
                "digest": "a" * 64,
                "databaseId": "local",
                "table": "orders",
                "schemaRevision": "schema_0001",
                "dataRevision": 1,
                "normalizedQuery": {"offset": 0, "limit": 100},
            },
        },
        "groupRows": [
            {
                "key": ["east", "open"],
                "count": 3,
                "summaries": [30],
                "parentCount": 9,
                "parentSummaries": [90],
            }
        ],
        "groupOffset": 0,
        "groupLimit": 100,
        "hasMoreGroups": False,
    }


def test_query_view_result_models_complete_parent_aggregates() -> None:
    result = QueryViewResult.model_validate(_view_result())

    assert result.group_rows[0].parent_count == 9
    assert result.model_dump(by_alias=True)["groupRows"][0]["parentSummaries"] == [90]


def test_query_view_result_rejects_partial_parent_aggregate() -> None:
    payload = _view_result()
    groups = payload["groupRows"]
    assert isinstance(groups, list)
    row = groups[0]
    assert isinstance(row, dict)
    row.pop("parentSummaries")

    with pytest.raises(ValidationError, match="provided together"):
        QueryViewResult.model_validate(payload)
