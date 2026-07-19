from __future__ import annotations

import re
import uuid
from typing import Any

import pytest

from backend.application.identifier_mapping_service import REGISTRY_COLLECTION
from backend.application.table_admin_service import TableAdminError, TableAdminService
from backend.contracts.table_admin import (
    CreateTableParams,
    FieldDefinition,
    ImportIdentifierMappingItem,
    ImportIdentifierMappingsParams,
    ListIdentifierMappingsParams,
    UpdateIdentifierAliasesParams,
)


class _Auth:
    async def access_token(self) -> str:
        return "token"


class _StatefulTransport:
    def __init__(self) -> None:
        self.collections: dict[str, dict[str, Any]] = {}
        self.mappings: dict[str, dict[str, Any]] = {}

    async def request(self, method: str, path: str, **kwargs: Any) -> dict[str, Any]:
        body = kwargs.get("json_body")
        if method == "GET" and path == "/collections":
            return {
                "data": [
                    {"collection": name, "meta": definition.get("meta", {})}
                    for name, definition in self.collections.items()
                ]
            }
        if method == "POST" and path == "/collections":
            assert isinstance(body, dict)
            self.collections[body["collection"]] = body
            return {"data": body}
        if method == "GET" and path == f"/items/{REGISTRY_COLLECTION}":
            return {"data": list(self.mappings.values())}
        if method == "POST" and path == f"/items/{REGISTRY_COLLECTION}":
            assert isinstance(body, list)
            for item in body:
                self.mappings[item["id"]] = dict(item)
            return {"data": body}
        if method == "PATCH" and path.startswith(f"/items/{REGISTRY_COLLECTION}/"):
            mapping_id = path.rsplit("/", 1)[-1]
            self.mappings[mapping_id].update(body)
            return {"data": self.mappings[mapping_id]}
        if method == "GET" and path.startswith("/fields/"):
            collection = path.rsplit("/", 1)[-1]
            return {"data": self.collections[collection].get("fields", [])}
        if method == "DELETE" and path.startswith("/collections/"):
            self.collections.pop(path.rsplit("/", 1)[-1], None)
            return {}
        raise AssertionError(f"unexpected request {method} {path}")


def _ids() -> Any:
    value = 1
    while True:
        yield uuid.UUID(int=value)
        value += 1


@pytest.mark.asyncio
async def test_unicode_names_create_stable_ascii_keys_and_translations() -> None:
    transport = _StatefulTransport()
    ids = _ids()
    profiles: dict[str, Any] = {}
    service = TableAdminService(
        transport=transport, auth=_Auth(), profiles=profiles, id_factory=lambda: next(ids)
    )

    result = await service.create_table(
        CreateTableParams(
            name="2026年客户清单 ✅",
            fields=[
                FieldDefinition(key="联系电话（备用）", type="string"),
                FieldDefinition(key="1月金额", type="decimal"),
                FieldDefinition(key="备注/说明", type="text"),
            ],
        )
    )

    assert re.fullmatch(r"vt_t_[0-9A-HJKMNP-TV-Z]{16}", result.collection)
    assert all(re.fullmatch(r"f_[0-9A-HJKMNP-TV-Z]{16}", key) for key in result.field_display_names)
    assert list(result.field_display_names.values()) == ["联系电话（备用）", "1月金额", "备注/说明"]
    body = transport.collections[result.collection]
    assert body["meta"]["translations"] == [
        {"language": "zh-CN", "translation": "2026年客户清单 ✅"}
    ]
    user_fields = [field for field in body["fields"] if field["field"].startswith("f_")]
    assert [field["meta"]["translations"][0]["translation"] for field in user_fields] == [
        "联系电话（备用）",
        "1月金额",
        "备注/说明",
    ]
    assert profiles[result.collection].collection == result.collection
    assert all(item["status"] == "active" for item in transport.mappings.values())

    physical_before = result.collection
    labels = await service.reconcile_identifiers()
    assert labels[physical_before] == "2026年客户清单 ✅"
    assert physical_before in profiles


@pytest.mark.asyncio
async def test_duplicate_names_use_nfkc_casefold_and_do_not_allocate_second_table() -> None:
    transport = _StatefulTransport()
    ids = _ids()
    service = TableAdminService(
        transport=transport, auth=_Auth(), profiles={}, id_factory=lambda: next(ids)
    )
    await service.create_table(
        CreateTableParams(name="ＡＢＣ", fields=[FieldDefinition(key="姓名", type="string")])
    )
    with pytest.raises(TableAdminError) as error:
        await service.create_table(
            CreateTableParams(name="abc", fields=[FieldDefinition(key="电话", type="string")])
        )
    assert error.value.code == "table_name_duplicate"
    assert len([name for name in transport.collections if name.startswith("vt_t_")]) == 1


@pytest.mark.asyncio
async def test_duplicate_field_display_names_are_rejected() -> None:
    service = TableAdminService(transport=_StatefulTransport(), auth=_Auth(), profiles={})
    with pytest.raises(TableAdminError) as error:
        await service.create_table(
            CreateTableParams(
                name="客户",
                fields=[
                    FieldDefinition(key="Ａ", type="string"),
                    FieldDefinition(key="a", type="string"),
                ],
            )
        )
    assert error.value.code == "field_name_duplicate"


@pytest.mark.asyncio
async def test_reconcile_adopts_external_collection_and_field_translation() -> None:
    transport = _StatefulTransport()
    transport.collections["legacy_orders"] = {
        "collection": "legacy_orders",
        "meta": {"translations": [{"language": "zh-CN", "translation": "历史订单"}]},
        "fields": [
            {"field": "id", "type": "uuid", "schema": {"is_primary_key": True}},
            {"field": "status", "type": "string"},
            {
                "field": "amount",
                "type": "decimal",
                "meta": {"translations": [{"language": "zh-CN", "translation": "订单金额"}]},
            },
        ],
    }
    profiles: dict[str, Any] = {}
    service = TableAdminService(transport=transport, auth=_Auth(), profiles=profiles)

    labels = await service.reconcile_identifiers()

    assert labels == {"legacy_orders": "历史订单"}
    assert "legacy_orders" in profiles
    adopted = list(transport.mappings.values())
    assert {
        (item["entity_kind"], item["physical_name"], item["display_name"]) for item in adopted
    } == {
        ("collection", "legacy_orders", "历史订单"),
        ("field", "amount", "订单金额"),
    }
    assert all(item["origin"] == "directus" and item["status"] == "active" for item in adopted)

    # A later Data Studio translation edit is reconciled back into the
    # registry while preserving the previous display name as an alias.
    transport.collections["legacy_orders"]["meta"]["translations"][0]["translation"] = (
        "历史订单（已归档）"
    )
    transport.collections["legacy_orders"]["fields"][2]["meta"]["translations"][0][
        "translation"
    ] = "含税金额"
    labels = await service.reconcile_identifiers()
    assert labels["legacy_orders"] == "历史订单（已归档）"
    collection_mapping = next(
        item for item in transport.mappings.values() if item["entity_kind"] == "collection"
    )
    amount_mapping = next(
        item for item in transport.mappings.values() if item["physical_name"] == "amount"
    )
    assert collection_mapping["aliases"] == ["历史订单"]
    assert amount_mapping["display_name"] == "含税金额"


@pytest.mark.asyncio
async def test_reconcile_orphans_deleted_schema_removes_profile_and_reactivates_on_restore() -> (
    None
):
    transport = _StatefulTransport()
    transport.collections["legacy_orders"] = {
        "collection": "legacy_orders",
        "meta": {"translations": [{"language": "zh-CN", "translation": "历史订单"}]},
        "fields": [
            {"field": "id", "type": "uuid"},
            {
                "field": "amount",
                "type": "decimal",
                "meta": {"translations": [{"language": "zh-CN", "translation": "金额"}]},
            },
        ],
    }
    profiles: dict[str, Any] = {}
    service = TableAdminService(transport=transport, auth=_Auth(), profiles=profiles)

    await service.reconcile_identifiers()
    assert "legacy_orders" in profiles
    mapping_count = len(transport.mappings)

    # A field removed in Studio must stop reserving an active mapping.
    transport.collections["legacy_orders"]["fields"] = [{"field": "id", "type": "uuid"}]
    await service.reconcile_identifiers()
    amount = next(item for item in transport.mappings.values() if item["physical_name"] == "amount")
    assert amount["status"] == "orphaned"

    # A collection removed in Studio must disappear from the runtime profile
    # registry, otherwise list_collections keeps returning a ghost table.
    transport.collections.pop("legacy_orders")
    assert await service.reconcile_identifiers() == {}
    assert "legacy_orders" not in profiles
    collection = next(
        item for item in transport.mappings.values() if item["entity_kind"] == "collection"
    )
    assert collection["status"] == "orphaned"

    # Restoring the same physical schema reuses, rather than duplicates, the
    # mapping records and makes them active again.
    transport.collections["legacy_orders"] = {
        "collection": "legacy_orders",
        "meta": {"translations": [{"language": "zh-CN", "translation": "历史订单"}]},
        "fields": [
            {"field": "id", "type": "uuid"},
            {"field": "amount", "type": "decimal"},
        ],
    }
    assert await service.reconcile_identifiers() == {"legacy_orders": "历史订单"}
    assert len(transport.mappings) == mapping_count
    assert all(item["status"] == "active" for item in transport.mappings.values())


@pytest.mark.asyncio
async def test_mapping_management_lists_searches_updates_and_imports_aliases() -> None:
    transport = _StatefulTransport()
    ids = _ids()
    service = TableAdminService(
        transport=transport, auth=_Auth(), profiles={}, id_factory=lambda: next(ids)
    )
    created = await service.create_table(
        CreateTableParams(name="客户清单", fields=[FieldDefinition(key="联系电话", type="string")])
    )

    listed = await service.list_identifier_mappings(ListIdentifierMappingsParams(search="客户"))
    assert [item.display_name for item in listed.mappings] == ["客户清单"]
    table_mapping = listed.mappings[0]

    updated = await service.update_identifier_aliases(
        UpdateIdentifierAliasesParams(
            mapping_id=table_mapping.id, aliases=["客户", " 客户 ", "客户清单"]
        )
    )
    updated_table = next(item for item in updated.mappings if item.id == table_mapping.id)
    assert updated_table.aliases == ["客户"]

    imported = await service.import_identifier_mappings(
        ImportIdentifierMappingsParams(
            mappings=[
                ImportIdentifierMappingItem(
                    entity_kind="collection",
                    physical_name=created.collection,
                    display_name="旧客户表",
                    aliases=["历史客户"],
                ),
                ImportIdentifierMappingItem(
                    entity_kind="collection",
                    physical_name="unknown_table",
                    display_name="不应导入",
                ),
            ]
        )
    )
    imported_table = next(item for item in imported.mappings if item.id == table_mapping.id)
    assert imported_table.aliases == ["客户", "历史客户", "旧客户表"]
    assert all(item.physical_name != "unknown_table" for item in imported.mappings)


@pytest.mark.asyncio
async def test_mapping_alias_collision_is_rejected_within_same_scope() -> None:
    transport = _StatefulTransport()
    ids = _ids()
    service = TableAdminService(
        transport=transport, auth=_Auth(), profiles={}, id_factory=lambda: next(ids)
    )
    await service.create_table(CreateTableParams(name="客户", fields=[]))
    await service.create_table(CreateTableParams(name="订单", fields=[]))
    mappings = (await service.list_identifier_mappings(ListIdentifierMappingsParams())).mappings
    orders = next(item for item in mappings if item.display_name == "订单")

    with pytest.raises(TableAdminError) as error:
        await service.update_identifier_aliases(
            UpdateIdentifierAliasesParams(mapping_id=orders.id, aliases=["ＫＥＨＵ", "客户"])
        )
    assert error.value.code == "alias_duplicate"
