from __future__ import annotations

import pytest

from backend.adapters.directus import DirectusQueryError, compile_directus_query
from backend.contracts.query import FilterCondition, SortCondition, TableQuery

APPROVED = {"id": "id", "name": "name", "amount": "amount", "status": "status"}


@pytest.mark.parametrize(
    ("operator", "value", "expected_operator", "expected_value"),
    [
        ("contains", "alpha", "_contains", "alpha"),
        ("eq", "open", "_eq", "open"),
        ("ne", "closed", "_neq", "closed"),
        ("starts_with", "A", "_starts_with", "A"),
        ("ends_with", "Z", "_ends_with", "Z"),
        ("gt", 10, "_gt", 10),
        ("lt", 20, "_lt", 20),
        ("gte", 10, "_gte", 10),
        ("lte", 20, "_lte", 20),
        ("between", [10, 20], "_between", [10, 20]),
        ("in", ["open", "draft"], "_in", ["open", "draft"]),
        ("is_null", None, "_null", True),
        ("is_not_null", None, "_nnull", True),
    ],
)
def test_maps_supported_filter_operators(
    operator: str,
    value: object,
    expected_operator: str,
    expected_value: object,
) -> None:
    plan = compile_directus_query(
        TableQuery(filters=[FilterCondition(field="status", operator=operator, value=value)]),
        approved_fields=APPROVED,
        primary_key="id",
    )

    assert plan.params["filter"] == {"status": {expected_operator: expected_value}}


def test_mixed_logic_is_folded_left_to_right() -> None:
    query = TableQuery(
        filters=[
            FilterCondition(field="name", operator="contains", value="a"),
            FilterCondition(field="status", operator="eq", value="open", logic="OR"),
            FilterCondition(field="amount", operator="gte", value=100, logic="AND"),
        ]
    )

    plan = compile_directus_query(query, approved_fields=APPROVED, primary_key="id")

    assert plan.params["filter"] == {
        "_and": [
            {
                "_or": [
                    {"name": {"_contains": "a"}},
                    {"status": {"_eq": "open"}},
                ]
            },
            {"amount": {"_gte": 100}},
        ]
    }


def test_maps_search_paging_sort_and_primary_key_tie_breaker() -> None:
    query = TableQuery(
        keyword="  contract  ",
        offset=200,
        limit=50,
        sorts=[
            SortCondition(field="amount", direction="desc"),
            SortCondition(field="amount", direction="asc"),
            SortCondition(field="name", direction="asc"),
        ],
    )

    plan = compile_directus_query(query, approved_fields=APPROVED, primary_key="id")

    assert plan.params == {
        "search": "contract",
        "limit": 50,
        "offset": 200,
        "sort": ["-amount", "name", "id"],
    }
    assert plan.referenced_fields == ["amount", "name"]
    assert plan.semantic_gaps == ["explicit_null_ordering"]


def test_does_not_duplicate_primary_key_sort() -> None:
    plan = compile_directus_query(
        TableQuery(sorts=[SortCondition(field="id", direction="desc")]),
        approved_fields=APPROVED,
        primary_key="id",
    )

    assert plan.params["sort"] == ["-id"]


@pytest.mark.parametrize(
    "condition",
    [
        FilterCondition(field="name", operator="regex", value="^a"),
        FilterCondition(field="name", operator="contains", value=123),
        FilterCondition(field="status", operator="in", value=[]),
        FilterCondition(field="amount", operator="between", value=[1]),
        FilterCondition(field="status", operator="is_null", value=True),
    ],
)
def test_rejects_unrepresentable_or_invalid_filters(condition: FilterCondition) -> None:
    with pytest.raises(DirectusQueryError):
        compile_directus_query(
            TableQuery(filters=[condition]),
            approved_fields=APPROVED,
            primary_key="id",
        )


def test_rejects_unapproved_field_and_primary_key() -> None:
    with pytest.raises(DirectusQueryError, match="unknown field"):
        compile_directus_query(
            TableQuery(sorts=[SortCondition(field="secret")]),
            approved_fields=APPROVED,
            primary_key="id",
        )

    with pytest.raises(DirectusQueryError, match="primary key"):
        compile_directus_query(
            TableQuery(),
            approved_fields={"name": "name"},
            primary_key="id",
        )
