"""Contract guards for renderer-facing preset/content-version writes."""

import pytest
from pydantic import ValidationError

from backend.contracts.presets_versions_dashboards import (
    CreateVersionParams,
    DeletePresetParams,
    DeleteVersionParams,
    PromoteVersionParams,
    SavePresetParams,
    SaveVersionParams,
    VersionIdParams,
)


@pytest.mark.parametrize(
    ("model", "payload"),
    [
        (
            SavePresetParams,
            {
                "collection": "orders",
                "name": "My view",
                "view": {},
            },
        ),
        (
            DeletePresetParams,
            {"presetId": "p1", "expectedRevision": "rev-p1"},
        ),
        (
            CreateVersionParams,
            {"collection": "orders", "itemId": "row-1"},
        ),
        (
            SaveVersionParams,
            {
                "collection": "orders",
                "itemId": "row-1",
                "versionId": "v1",
                "values": {},
            },
        ),
        (
            PromoteVersionParams,
            {
                "collection": "orders",
                "itemId": "row-1",
                "versionId": "v1",
                "mainHash": "hash-1",
            },
        ),
        (
            DeleteVersionParams,
            {
                "collection": "orders",
                "itemId": "row-1",
                "versionId": "v1",
                "expectedRevision": "rev-v1",
            },
        ),
    ],
)
def test_write_contracts_require_operation_id(model: type, payload: dict) -> None:
    with pytest.raises(ValidationError):
        model.model_validate(payload)


def test_shared_version_id_contract_keeps_compare_read_only() -> None:
    params = VersionIdParams.model_validate(
        {"collection": "orders", "itemId": "row-1", "versionId": "v1"}
    )

    assert params.operation_id is None


def test_operation_id_uses_camel_case_on_the_wire() -> None:
    params = DeletePresetParams.model_validate(
        {
            "presetId": "p1",
            "expectedRevision": "rev-p1",
            "operationId": "op-delete-preset-1",
        }
    )

    assert params.model_dump(by_alias=True) == {
        "presetId": "p1",
        "expectedRevision": "rev-p1",
        "operationId": "op-delete-preset-1",
    }


def test_preset_view_round_trips_typed_filter_groups_and_group_summaries() -> None:
    payload = {
        "collection": "orders",
        "name": "Open orders by month",
        "operationId": "op-save-preset-1",
        "view": {
            "filters": [
                {
                    "groupLogic": "AND",
                    "filters": [
                        {
                            "field": "status",
                            "operator": "ne",
                            "value": "closed",
                            "logic": "AND",
                        },
                        {
                            "groupLogic": "OR",
                            "filters": [
                                {
                                    "field": "amount",
                                    "operator": "gte",
                                    "value": 100,
                                    "logic": "AND",
                                },
                                {
                                    "field": "priority",
                                    "operator": "eq",
                                    "value": "urgent",
                                    "logic": "AND",
                                },
                            ],
                        },
                    ],
                }
            ],
            "sorts": [{"field": "created_at", "direction": "desc", "nullsLast": True}],
            "groups": [
                {"field": "warehouse", "direction": "asc", "bucket": "value"},
                {"field": "created_at", "direction": "desc", "bucket": "month"},
            ],
            "summaries": [
                {"field": "amount", "function": "sum"},
                {"field": "amount", "function": "avg"},
                {"field": "quantity", "function": "max"},
            ],
            "search": "",
            "visibleFields": ["status", "amount", "warehouse", "created_at"],
            "layout": "table",
        },
    }

    params = SavePresetParams.model_validate(payload)

    assert params.view.model_dump(
        by_alias=True,
        include={"filters", "sorts", "groups", "summaries", "visible_fields"},
    ) == {
        key: payload["view"][key]
        for key in ("filters", "sorts", "groups", "summaries", "visibleFields")
    }


def test_preset_view_rejects_filter_groups_deeper_than_three_levels() -> None:
    expression: dict = {"field": "status", "operator": "eq", "value": "open"}
    for _ in range(4):
        expression = {"groupLogic": "AND", "filters": [expression]}

    with pytest.raises(ValidationError, match="at most 3 levels"):
        SavePresetParams.model_validate(
            {
                "collection": "orders",
                "name": "Too deep",
                "operationId": "op-save-preset-too-deep",
                "view": {"filters": [expression]},
            }
        )


def test_preset_view_rejects_more_than_fifty_conditions_across_groups() -> None:
    groups = [
        {
            "groupLogic": "OR",
            "filters": [
                {"field": f"field_{group}_{index}", "operator": "eq", "value": index}
                for index in range(17)
            ],
        }
        for group in range(3)
    ]

    with pytest.raises(ValidationError, match="at most 50 conditions"):
        SavePresetParams.model_validate(
            {
                "collection": "orders",
                "name": "Too many filters",
                "operationId": "op-save-preset-too-many-filters",
                "view": {"filters": groups},
            }
        )
