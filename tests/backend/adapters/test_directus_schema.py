from __future__ import annotations

import pytest

from backend.adapters.directus import DirectusSchemaError, build_directus_schema


def _fields() -> list[dict]:
    return [
        {
            "collection": "contracts",
            "field": "amount",
            "type": "decimal",
            "meta": {"sort": 3, "required": False},
            "schema": {"is_nullable": True, "is_primary_key": False},
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
            "field": "internal_note",
            "type": "text",
            "meta": {"sort": 6},
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
                "fields": ["id", "name", "amount", "approved", "signed_on"],
            }
        },
    )

    assert plan.primary_key == "id"
    assert plan.visible_fields == ["id", "name", "amount", "approved", "signed_on"]
    assert [column.data_type for column in plan.columns] == [
        "integer",
        "text",
        "decimal",
        "boolean",
        "date",
    ]
    assert all(column.editable is False for column in plan.columns)
    assert plan.columns[0].nullable is False
    assert plan.columns[1].nullable is False
    assert plan.columns[1].title == "合同名称"
    assert "internal_note" not in plan.visible_fields


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
