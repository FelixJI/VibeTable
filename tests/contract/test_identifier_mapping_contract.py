"""Backward-compatible wire contracts for physical/display name separation."""

from backend.contracts.directus import DirectusCollectionListResult
from backend.contracts.table_admin import CreateTableResult


def test_legacy_collection_list_without_display_names_still_deserializes() -> None:
    result = DirectusCollectionListResult.model_validate(
        {
            "collections": ["customers"],
            "capabilityHashes": {"customers": "hash"},
        }
    )
    assert result.collections == ["customers"]
    assert result.display_names == {}


def test_collection_list_keeps_physical_array_and_adds_display_names_map() -> None:
    result = DirectusCollectionListResult(
        collections=["vt_t_01K0000000000000"],
        capability_hashes={"vt_t_01K0000000000000": "hash"},
        display_names={"vt_t_01K0000000000000": "客户清单"},
    )
    wire = result.model_dump(by_alias=True, mode="json")
    assert wire["collections"] == ["vt_t_01K0000000000000"]
    assert wire["displayNames"] == {"vt_t_01K0000000000000": "客户清单"}


def test_legacy_create_result_and_extended_result_are_both_valid() -> None:
    legacy = CreateTableResult.model_validate(
        {"collection": "customers", "primaryKey": "id", "fields": ["id", "name"]}
    )
    assert legacy.display_name is None
    assert legacy.field_display_names == {}

    extended = CreateTableResult(
        collection="vt_t_01K0000000000000",
        display_name="客户清单",
        primary_key="id",
        fields=["id", "f_01K0000000000000"],
        field_display_names={"f_01K0000000000000": "姓名"},
    ).model_dump(by_alias=True, mode="json")
    assert extended["displayName"] == "客户清单"
    assert extended["fieldDisplayNames"] == {"f_01K0000000000000": "姓名"}
