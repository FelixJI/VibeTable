"""Unicode display-name contracts for runtime table administration."""

from __future__ import annotations

import pytest
from pydantic import ValidationError

from backend.contracts.table_admin import CreateTableParams, FieldDefinition


@pytest.mark.parametrize(
    "key", ["姓名", "1月金额", "联系电话（备用）", "备注/说明", "状态 ✅", "name"]
)
def test_field_definition_accepts_unicode_display_name(key: str) -> None:
    assert FieldDefinition(key=key, type="string").key == key


def test_display_names_preserve_typography_and_are_trimmed() -> None:
    assert FieldDefinition(key="  ＡＢＣ  ", type="string").key == "ＡＢＣ"
    assert CreateTableParams(name="  客户清单  ", fields=[]).name == "客户清单"


@pytest.mark.parametrize("value", ["", "   ", "客户\n清单", "字段\x00名"])
def test_display_names_reject_blank_or_control_characters(value: str) -> None:
    with pytest.raises(ValidationError):
        FieldDefinition(key=value, type="string")


def test_display_names_reject_overlength() -> None:
    with pytest.raises(ValidationError):
        CreateTableParams(name="表" * 129, fields=[])


def test_create_table_params_preserves_legacy_wire_keys() -> None:
    params = CreateTableParams.model_validate(
        {
            "name": "2026年客户清单 ✅",
            "fields": [{"key": "联系电话（备用）", "type": "string"}],
        }
    )
    assert params.model_dump(by_alias=True)["name"] == "2026年客户清单 ✅"
    assert params.model_dump(by_alias=True)["fields"][0]["key"] == "联系电话（备用）"


@pytest.mark.parametrize(
    "field_type",
    [
        "string",
        "text",
        "integer",
        "bigInteger",
        "float",
        "decimal",
        "boolean",
        "date",
        "dateTime",
        "timestamp",
        "time",
        "json",
        "csv",
        "uuid",
        "hash",
        "binary",
    ],
)
def test_field_definition_accepts_all_standalone_directus_types(field_type: str) -> None:
    assert FieldDefinition(key="字段", type=field_type).type == field_type  # type: ignore[arg-type]


def test_field_definition_rejects_relation_alias_without_configuration() -> None:
    with pytest.raises(ValidationError):
        FieldDefinition(key="关联", type="alias")  # type: ignore[arg-type]
