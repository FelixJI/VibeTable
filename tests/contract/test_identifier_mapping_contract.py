"""Physical/display name separation remains provider-neutral."""

from backend.contracts.table_admin import CreateTableResult


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
