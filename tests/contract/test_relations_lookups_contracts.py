from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.lookup import (
    LookupDefinition,
    LookupQueryParams,
    LookupQueryResult,
    LookupValuePageParams,
)
from backend.contracts.relation_admin import (
    RelationDisplayFieldInfo,
    RelationSearchParams,
    RelationSingleUpdateResult,
    SchemaSnapshot,
)

FIXTURE = Path(__file__).parent / "fixtures" / "table-relations-lookups-contracts.json"


def _load() -> dict:
    return json.loads(FIXTURE.read_text(encoding="utf-8"))


def test_relations_lookups_fixture_round_trip() -> None:
    fixture = _load()

    assert fixture["contract"] == "table.relations-lookups.fixtures.v1"
    snapshot = SchemaSnapshot.model_validate(fixture["schemaSnapshot"])
    definition = LookupDefinition.model_validate(fixture["lookupDefinition"])
    single_update = RelationSingleUpdateResult.model_validate(fixture["singleUpdateResult"])
    params = LookupQueryParams.model_validate(fixture["query"]["params"])
    result = LookupQueryResult.model_validate(fixture["query"]["result"])

    assert snapshot.normalized_relations[0].relation_id == "orders.contract"
    assert definition.output_type == "decimal"
    assert single_update.current is not None
    assert single_update.current.item_id == "contract-7"
    assert params.request_generation == result.request_generation == 7
    assert result.rows[0]["orders.contract_price"] == "1250.50"


def test_lookup_query_accepts_the_same_nested_filter_ast_as_table_query() -> None:
    params = LookupQueryParams.model_validate(
        {
            "collection": "orders",
            "fieldRefs": ["orders.customer_name"],
            "query": {
                "filters": [
                    {
                        "groupLogic": "OR",
                        "filters": [
                            {"field": "orders.status", "operator": "eq", "value": "open"},
                            {"field": "orders.priority", "operator": "eq", "value": "urgent"},
                        ],
                    }
                ]
            },
            "schemaRevision": "schema:1",
            "permissionRevision": "permission:1",
            "lookupRevision": "lookup:1",
        }
    )

    assert params.query.model_dump(by_alias=True, exclude_none=True)["filters"] == [
        {
            "groupLogic": "OR",
            "filters": [
                {"field": "orders.status", "operator": "eq", "value": "open", "logic": "AND"},
                {"field": "orders.priority", "operator": "eq", "value": "urgent", "logic": "AND"},
            ],
        }
    ]


def test_lookup_source_pages_are_revision_bound_and_bounded() -> None:
    params = LookupValuePageParams.model_validate(
        {
            "collection": "orders",
            "fieldRef": "line_skus",
            "sourceRecordId": "order-1",
            "offset": 10_000,
            "limit": 100,
            "schemaRevision": "schema_7",
            "permissionRevision": "permission_7",
            "lookupRevision": "lookup_7",
        }
    )
    assert params.offset == 10_000
    with pytest.raises(ValueError, match="less than or equal to 500"):
        LookupValuePageParams.model_validate(
            {
                **params.model_dump(by_alias=True),
                "limit": 501,
            }
        )


def test_lookup_query_rejects_more_than_two_server_side_groups() -> None:
    with pytest.raises(ValueError, match="at most 2 items"):
        LookupQueryParams.model_validate(
            {
                "collection": "orders",
                "fieldRefs": ["orders.customer_name"],
                "query": {
                    "groups": [
                        {"fieldRef": "orders.region"},
                        {"fieldRef": "orders.customer_name"},
                        {"fieldRef": "orders.status"},
                    ]
                },
                "schemaRevision": "schema:1",
                "permissionRevision": "permission:1",
                "lookupRevision": "lookup:1",
            }
        )


def test_lookup_query_accepts_empty_projection_for_relation_display_labels() -> None:
    params = LookupQueryParams.model_validate(
        {
            "collection": "orders",
            "fieldRefs": [],
            "query": {
                "filters": [{"field": "id", "operator": "in", "value": ["order-1"]}],
                "offset": 0,
                "limit": 1,
            },
            "schemaRevision": "schema:1",
            "permissionRevision": "permission:1",
            "lookupRevision": "lookup:1",
        }
    )

    dumped = params.model_dump(by_alias=True, mode="json")
    assert dumped["fieldRefs"] == []
    assert dumped["query"]["filters"][0]["value"] == ["order-1"]
    assert dumped["query"]["limit"] == 1


def test_relation_display_field_info_carries_canonical_enum_options() -> None:
    """#447×#459：select 标签来源字段带 canonical enumOptions，不建第二套选项模型。"""
    info = RelationDisplayFieldInfo.model_validate(
        {
            "fieldId": "fld_status01",
            "dataType": "text",
            "display": {
                "kind": "select",
                "preset": "",
                "displayScale": 2,
                "scaleMode": "fixed",
                "trimTrailingZeros": False,
                "useGrouping": True,
                "currency": "",
                "percentStorage": "ratio",
                "unit": None,
                "precision": "exact",
                "timezone": "system",
                "mode": "default",
                "trueLabel": "是",
                "falseLabel": "否",
            },
            "enumOptions": [
                {
                    "optionId": "opt_status01",
                    "label": "进行中",
                    "color": "#ffaa00",
                    "order": 0,
                    "state": "active",
                },
                {
                    "optionId": "opt_status02",
                    "label": "旧状态",
                    "color": "",
                    "order": 1,
                    "state": "retired",
                },
            ],
        }
    )
    dumped = info.model_dump(by_alias=True, exclude_none=True)
    assert [option["label"] for option in dumped["enumOptions"]] == ["进行中", "旧状态"]
    assert dumped["enumOptions"][1]["state"] == "retired"
    omitted = RelationDisplayFieldInfo.model_validate(
        {"fieldId": "fld_amount01", "dataType": "decimal"}
    )
    assert "enumOptions" not in omitted.model_dump(by_alias=True, exclude_none=True)


def test_relation_selected_id_refresh_budget_matches_go_boundary() -> None:
    omitted = RelationSearchParams(relation_id="source.link")
    assert "targetItemIds" not in omitted.model_dump(by_alias=True)
    for ids in ([], [""], ["record"] * 101):
        with pytest.raises(ValueError, match="target"):
            RelationSearchParams(relation_id="source.link", target_item_ids=ids)
    assert RelationSearchParams(
        relation_id="source.link", target_item_ids=["first", "second"]
    ).target_item_ids == ["first", "second"]
