"""C1 relation workspace contracts edge-case coverage.

Validates the camelCase wire shape, ``max_depth`` / ``relations`` / length
boundaries, the required-field matrix on ``RelationColumn`` and the
``extra="forbid"`` posture declared on the relation projection contracts.
Uses inline payloads (no JSON fixtures exist for relation yet).
"""

from __future__ import annotations

import pytest
from pydantic import ValidationError

from backend.contracts.query import TableQuery
from backend.contracts.relation import (
    RelationColumn,
    RelationProjectionParams,
    RelationProjectionResult,
)

# ---------------------------------------------------------------------------
# Happy-path construction + camelCase round-trip
# ---------------------------------------------------------------------------


def test_relation_projection_params_round_trips_with_defaults() -> None:
    params = RelationProjectionParams(collection="orders")
    dumped = params.model_dump(mode="json", by_alias=True)
    assert dumped == {
        "collection": "orders",
        "query": {
            "keyword": None,
            "filters": [],
            "sorts": [],
            "offset": 0,
            "limit": 100,
        },
        "relations": [],
        "maxDepth": 1,
    }
    assert params.max_depth == 1
    assert params.relations == []
    assert isinstance(params.query, TableQuery)


def test_relation_projection_params_round_trips_full_payload() -> None:
    params = RelationProjectionParams(
        collection="orders",
        query=TableQuery(limit=50, offset=10),
        relations=["project", "owner"],
        max_depth=2,
    )
    dumped = params.model_dump(mode="json", by_alias=True)
    assert dumped["query"]["limit"] == 50
    assert dumped["query"]["offset"] == 10
    assert dumped["relations"] == ["project", "owner"]
    assert dumped["maxDepth"] == 2


def test_relation_column_round_trips() -> None:
    column = RelationColumn(
        relation="project",
        field="code",
        related_collection="projects",
        display_path="projects.code",
    )
    dumped = column.model_dump(mode="json", by_alias=True)
    assert dumped == {
        "relation": "project",
        "field": "code",
        "relatedCollection": "projects",
        "displayPath": "projects.code",
    }


def test_relation_projection_result_round_trips() -> None:
    result = RelationProjectionResult(
        collection="orders",
        rows=[{"id": "row-1", "project": {"code": "P1"}}],
        relation_columns=[
            RelationColumn(
                relation="project",
                field="code",
                related_collection="projects",
                display_path="projects.code",
            )
        ],
        restricted_relations=["secret"],
        capability_hash="hash-1",
    )
    dumped = result.model_dump(mode="json", by_alias=True)
    assert dumped["rows"] == [{"id": "row-1", "project": {"code": "P1"}}]
    assert dumped["relationColumns"][0]["relatedCollection"] == "projects"
    assert dumped["restrictedRelations"] == ["secret"]
    assert dumped["capabilityHash"] == "hash-1"


def test_relation_projection_result_accepts_empty_rows() -> None:
    result = RelationProjectionResult(collection="orders", rows=[], capability_hash="h")
    assert result.rows == []
    assert result.relation_columns == []
    assert result.restricted_relations == []


# ---------------------------------------------------------------------------
# max_depth boundaries (ge=1, le=3)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("max_depth", [1, 2, 3])
def test_max_depth_accepts_valid_range(max_depth: int) -> None:
    params = RelationProjectionParams(collection="orders", max_depth=max_depth)
    assert params.max_depth == max_depth


@pytest.mark.parametrize("max_depth", [0, 4, -1])
def test_max_depth_rejects_out_of_range(max_depth: int) -> None:
    with pytest.raises(ValidationError):
        RelationProjectionParams(collection="orders", max_depth=max_depth)


# ---------------------------------------------------------------------------
# relations max_length=16
# ---------------------------------------------------------------------------


def test_relations_accepts_sixteen_entries() -> None:
    relations = [f"rel{i}" for i in range(16)]
    params = RelationProjectionParams(collection="orders", relations=relations)
    assert params.relations == relations


def test_relations_rejects_seventeen_entries() -> None:
    with pytest.raises(ValidationError):
        RelationProjectionParams(collection="orders", relations=[f"rel{i}" for i in range(17)])


# ---------------------------------------------------------------------------
# collection length boundaries (min_length=1, max_length=128)
# ---------------------------------------------------------------------------


def test_collection_rejects_empty_and_overlength() -> None:
    with pytest.raises(ValidationError):
        RelationProjectionParams(collection="")
    with pytest.raises(ValidationError):
        RelationProjectionParams(collection="x" * 129)


# ---------------------------------------------------------------------------
# RelationColumn required-field matrix
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "missing",
    ["relation", "field", "related_collection", "display_path"],
)
def test_relation_column_requires_all_four_fields(missing: str) -> None:
    kwargs: dict[str, str] = {
        "relation": "project",
        "field": "code",
        "related_collection": "projects",
        "display_path": "projects.code",
    }
    kwargs.pop(missing)
    with pytest.raises(ValidationError):
        RelationColumn(**kwargs)  # type: ignore[arg-type]


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("relation", ""),
        ("field", ""),
        ("related_collection", ""),
        ("display_path", ""),
    ],
)
def test_relation_column_rejects_empty_strings(field: str, value: str) -> None:
    kwargs: dict[str, str] = {
        "relation": "project",
        "field": "code",
        "related_collection": "projects",
        "display_path": "projects.code",
    }
    kwargs[field] = value
    with pytest.raises(ValidationError):
        RelationColumn(**kwargs)


def test_relation_column_rejects_overlength_related_collection() -> None:
    with pytest.raises(ValidationError):
        RelationColumn(
            relation="project",
            field="code",
            related_collection="x" * 129,
            display_path="p.code",
        )


def test_relation_column_rejects_overlength_display_path() -> None:
    with pytest.raises(ValidationError):
        RelationColumn(
            relation="project",
            field="code",
            related_collection="projects",
            display_path="x" * 257,
        )


# ---------------------------------------------------------------------------
# RelationProjectionResult.capability_hash min_length=1, rows required
# ---------------------------------------------------------------------------


def test_result_requires_rows_field() -> None:
    with pytest.raises(ValidationError):
        RelationProjectionResult(collection="orders", capability_hash="h")  # type: ignore[call-arg]


def test_result_requires_non_empty_capability_hash() -> None:
    with pytest.raises(ValidationError):
        RelationProjectionResult(collection="orders", rows=[], capability_hash="")


# ---------------------------------------------------------------------------
# extra="forbid" rejection
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("model", "payload"),
    [
        (
            RelationProjectionParams,
            {"collection": "orders", "unexpected": True},
        ),
        (
            RelationColumn,
            {
                "relation": "project",
                "field": "code",
                "relatedCollection": "projects",
                "displayPath": "p.code",
                "unexpected": True,
            },
        ),
        (
            RelationProjectionResult,
            {"collection": "orders", "rows": [], "capabilityHash": "h", "x": 1},
        ),
    ],
)
def test_relation_models_reject_unknown_keys(model: type, payload: dict[str, object]) -> None:
    with pytest.raises(ValidationError):
        model.model_validate(payload)
