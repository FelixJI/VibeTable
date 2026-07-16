from unittest.mock import AsyncMock, MagicMock

import pytest

from backend.application.table_admin_service import TableAdminError, TableAdminService
from backend.contracts.table_admin import CreateTableParams, DeleteTableParams, FieldDefinition


def _make_auth(token: str = "tok") -> MagicMock:
    """Mock auth broker exposing ``async access_token() -> str``."""
    auth = MagicMock()
    auth.access_token = AsyncMock(return_value=token)
    return auth


@pytest.mark.asyncio
async def test_create_table_posts_collection_and_fields():
    transport = MagicMock()
    transport.request = AsyncMock(return_value={"data": {}})
    profiles: dict = {}
    service = TableAdminService(transport=transport, auth=_make_auth(), profiles=profiles)
    params = CreateTableParams(name="customers", fields=[FieldDefinition(key="name", type="string")])
    result = await service.create_table(params)
    assert result.collection == "customers"
    # 应调用了 POST /collections
    calls = list(transport.request.call_args_list)
    assert any(c.args[0] == "POST" and "/collections" in c.args[1] for c in calls)
    # 当前用户 token 应按调用传入
    assert all(c.kwargs.get("access_token") == "tok" for c in calls)
    # manifest 应更新：写入真正的 CollectionProfile（而非占位），使会话内立即可用
    assert "customers" in profiles
    profile = profiles["customers"]
    assert profile.collection == "customers"
    assert profile.primary_key == "id"
    assert "name" in profile.fields
    assert "name" in profile.create_fields
    assert profile.archive_field == "status"


@pytest.mark.asyncio
async def test_create_table_rejects_system_prefix():
    transport = MagicMock()
    service = TableAdminService(transport=transport, auth=_make_auth(), profiles={})
    params = CreateTableParams(name="directus_users", fields=[])
    with pytest.raises(TableAdminError):
        await service.create_table(params)


@pytest.mark.asyncio
async def test_delete_table_rejects_system_prefix():
    transport = MagicMock()
    service = TableAdminService(transport=transport, auth=_make_auth(), profiles={})
    with pytest.raises(TableAdminError):
        await service.delete_table(DeleteTableParams(name="vibetable_documents"))


@pytest.mark.asyncio
async def test_delete_table_calls_delete():
    transport = MagicMock()
    transport.request = AsyncMock(return_value={})
    from backend.adapters.directus.profile import CollectionProfile

    fields = ["id", "status", "name", "sort", "date_updated"]
    profiles = {
        "customers": CollectionProfile(
            collection="customers",
            fields=fields,
            create_fields=fields,
            update_fields=fields,
        )
    }
    service = TableAdminService(transport=transport, auth=_make_auth(), profiles=profiles)
    await service.delete_table(DeleteTableParams(name="customers"))
    calls = list(transport.request.call_args_list)
    assert any(c.args[0] == "DELETE" and "customers" in c.args[1] for c in calls)
    assert all(c.kwargs.get("access_token") == "tok" for c in calls)
    assert "customers" not in profiles
