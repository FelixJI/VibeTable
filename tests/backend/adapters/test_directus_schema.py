from __future__ import annotations

import pytest

from backend.adapters.directus import DirectusSchemaError, build_directus_schema
from backend.adapters.directus.schema import directus_readonly_fields


def _fields() -> list[dict]:
    return [
        {
            "collection": "contracts",
            "field": "amount",
            "type": "decimal",
            "meta": {"sort": 3, "required": False},
            "schema": {
                "is_nullable": True,
                "is_primary_key": False,
                "numeric_precision": 10,
                "numeric_scale": 2,
            },
        },
        {
            "collection": "contracts",
            "field": "id",
            "type": "integer",
            "meta": {"sort": 1, "readonly": True},
            "schema": {"is_nullable": False, "is_primary_key": True},
        },
        {
            "collection": "contracts",
            "field": "name",
            "type": "string",
            "meta": {
                "sort": 2,
                "required": True,
                "translations": [{"language": "zh-CN", "translation": "合同名称"}],
            },
            "schema": {"is_nullable": True, "is_primary_key": False},
        },
        {
            "collection": "contracts",
            "field": "approved",
            "type": "boolean",
            "meta": {"sort": 4},
            "schema": {"is_nullable": False, "is_primary_key": False},
        },
        {
            "collection": "contracts",
            "field": "signed_on",
            "type": "date",
            "meta": {"sort": 5},
            "schema": {"is_nullable": True, "is_primary_key": False},
        },
        {
            "collection": "contracts",
            "field": "occurred_at",
            "type": "dateTime",
            "meta": {"sort": 6},
            "schema": {"is_nullable": True, "is_primary_key": False},
        },
        {
            "collection": "contracts",
            "field": "recorded_at",
            "type": "timestamp",
            "meta": {"sort": 7},
            "schema": {"is_nullable": True, "is_primary_key": False},
        },
        {
            "collection": "contracts",
            "field": "starts_at",
            "type": "time",
            "meta": {"sort": 8},
            "schema": {"is_nullable": True, "is_primary_key": False},
        },
        {
            "collection": "contracts",
            "field": "internal_note",
            "type": "text",
            "meta": {"sort": 9},
            "schema": {"is_nullable": True, "is_primary_key": False},
        },
    ]


def test_builds_permission_pruned_read_only_schema() -> None:
    plan = build_directus_schema(
        collection="contracts",
        fields=_fields(),
        collection_permissions={
            "read": {
                "access": "partial",
                "fields": [
                    "id",
                    "name",
                    "amount",
                    "approved",
                    "signed_on",
                    "occurred_at",
                    "recorded_at",
                    "starts_at",
                ],
            }
        },
    )

    assert plan.primary_key == "id"
    assert plan.visible_fields == [
        "id",
        "name",
        "amount",
        "approved",
        "signed_on",
        "occurred_at",
        "recorded_at",
        "starts_at",
    ]
    assert plan.readonly_fields == ["id"]
    assert [column.data_type for column in plan.columns] == [
        "integer",
        "text",
        "decimal",
        "boolean",
        "date",
        "datetime",
        "datetime",
        "time",
    ]
    assert all(column.editable is False for column in plan.columns)
    assert plan.columns[0].nullable is False
    assert plan.columns[1].nullable is False
    assert plan.columns[1].title == "合同名称"
    assert "internal_note" not in plan.visible_fields


def test_readonly_policy_includes_studio_and_generated_fields() -> None:
    assert directus_readonly_fields(
        [
            {"field": "studio_locked", "meta": {"readonly": True}},
            {"field": "computed", "schema": {"is_generated": True}},
            {"field": "normal", "meta": {"readonly": False}},
        ]
    ) == {"studio_locked", "computed"}


def test_schema_revision_is_stable_across_api_field_order() -> None:
    permissions = {"read": {"access": "full", "fields": ["*"]}}

    first = build_directus_schema(
        collection="contracts",
        fields=_fields(),
        collection_permissions=permissions,
    )
    second = build_directus_schema(
        collection="contracts",
        fields=list(reversed(_fields())),
        collection_permissions=permissions,
    )

    assert first.schema_revision == second.schema_revision
    assert first.visible_fields == second.visible_fields


def test_schema_revision_changes_when_visible_shape_changes() -> None:
    permissions = {"read": {"access": "full", "fields": ["*"]}}
    changed = _fields()
    changed[0]["type"] = "integer"

    first = build_directus_schema(
        collection="contracts", fields=_fields(), collection_permissions=permissions
    )
    second = build_directus_schema(
        collection="contracts", fields=changed, collection_permissions=permissions
    )

    assert first.schema_revision != second.schema_revision


@pytest.mark.parametrize(
    ("permissions", "fields", "message"),
    [
        ({"read": {"access": "none", "fields": []}}, _fields(), "not readable"),
        ({"read": {"access": "full", "fields": ["name"]}}, _fields(), "primary key"),
        (
            {"read": {"access": "full", "fields": ["*"]}},
            [
                *_fields(),
                {"field": "other_id", "type": "integer", "schema": {"is_primary_key": True}},
            ],
            "primary key",
        ),
    ],
)
def test_rejects_unreadable_or_unstable_collection(
    permissions: dict,
    fields: list[dict],
    message: str,
) -> None:
    with pytest.raises(DirectusSchemaError, match=message):
        build_directus_schema(
            collection="contracts",
            fields=fields,
            collection_permissions=permissions,
        )


def test_numeric_scale_and_precision_flow_through_to_columns() -> None:
    permissions = {"read": {"access": "full", "fields": ["*"]}}
    plan = build_directus_schema(
        collection="contracts",
        fields=_fields(),
        collection_permissions=permissions,
    )
    by_name = {column.name: column for column in plan.columns}
    # The decimal `amount` field carries Directus numeric metadata.
    assert by_name["amount"].scale == 2
    assert by_name["amount"].precision == 10
    # Non-numeric fields report None (no numeric_scale/precision in the payload).
    assert by_name["name"].scale is None
    assert by_name["name"].precision is None
    # Integer fields report their own scale when Directus declares one.
    assert by_name["id"].scale is None


def test_schema_revision_changes_when_only_numeric_scale_changes() -> None:
    permissions = {"read": {"access": "full", "fields": ["*"]}}
    first = build_directus_schema(
        collection="contracts",
        fields=_fields(),
        collection_permissions=permissions,
    )
    # Mutate ONLY the numeric scale of the decimal field (type unchanged) and
    # confirm the revision hash changes — scale must be part of the hash input.
    changed = _fields()
    for field in changed:
        if field["field"] == "amount":
            field["schema"]["numeric_scale"] = 4
    second = build_directus_schema(
        collection="contracts",
        fields=changed,
        collection_permissions=permissions,
    )
    assert first.schema_revision != second.schema_revision


def test_malformed_numeric_metadata_degrades_to_none() -> None:
    # A non-integer numeric_scale must not crash schema build; it degrades to
    # None rather than surfacing a decode error.
    permissions = {"read": {"access": "full", "fields": ["*"]}}
    fields = _fields()
    for field in fields:
        if field["field"] == "amount":
            field["schema"]["numeric_scale"] = "not-a-number"
    plan = build_directus_schema(
        collection="contracts",
        fields=fields,
        collection_permissions=permissions,
    )
    by_name = {column.name: column for column in plan.columns}
    assert by_name["amount"].scale is None
