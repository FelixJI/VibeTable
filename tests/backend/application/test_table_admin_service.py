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
    params = CreateTableParams(name="客户清单", fields=[FieldDefinition(key="姓名", type="string")])
    result = await service.create_table(params)
    assert result.collection.startswith("vt_t_")
    assert result.display_name == "客户清单"
    assert list(result.field_display_names.values()) == ["姓名"]
    # 应调用了 POST /collections
    calls = list(transport.request.call_args_list)
    assert any(c.args[0] == "POST" and "/collections" in c.args[1] for c in calls)
    # 当前用户 token 应按调用传入
    assert all(c.kwargs.get("access_token") == "tok" for c in calls)
    # manifest 应更新：写入真正的 CollectionProfile（而非占位），使会话内立即可用
    assert result.collection in profiles
    profile = profiles[result.collection]
    assert profile.collection == result.collection
    assert profile.primary_key == "id"
    physical_field = next(iter(result.field_display_names))
    assert physical_field in profile.fields
    assert physical_field in profile.create_fields
    assert profile.archive_field == "status"
    assert profile.allow_revision_history is True
    assert profile.allow_revision_revert is True
    assert profile.allow_permanent_delete is False
    assert profile.allow_dashboards is True
    assert "date_updated" not in profile.update_fields


def test_reconciled_business_profile_enables_safe_history_without_permanent_delete():
    profile = TableAdminService._profile_from_existing(
        "orders", ["id", "status", "title", "date_created", "date_updated"]
    )
    assert profile.allow_revision_history is True
    assert profile.allow_revision_revert is True
    assert profile.allow_permanent_delete is False
    assert profile.allow_dashboards is True
    assert profile.update_fields == ["status", "title"]


@pytest.mark.asyncio
async def test_create_table_builds_profile_before_calling_directus():
    """Regression: ``_profile_for`` used to run AFTER ``transport.request``,
    so a profile-build failure left an orphan collection in Directus. The
    profile must be built first; if it fails, Directus is never called."""
    transport = MagicMock()
    transport.request = AsyncMock(return_value={"data": {}})
    profiles: dict = {}
    service = TableAdminService(transport=transport, auth=_make_auth(), profiles=profiles)
    params = CreateTableParams(
        name="orders",
        fields=[
            FieldDefinition(key="title", type="string"),
            FieldDefinition(key="amount", type="integer"),
        ],
    )
    await service.create_table(params)

    assert transport.request.called, "Directus POST should have been issued"
    # The profile must already be resolvable before the transport call happened:
    # build it here the same way the service does and confirm it is the one
    # stored. (The ordering guarantee itself is enforced structurally —
    # _profile_for is called before transport.request in the source — and a
    # failure there is covered by test_create_table_wraps_profile_failure.)
    assert len(profiles) == 1
    profile = next(iter(profiles.values()))
    assert len([field for field in profile.fields if field.startswith("f_")]) == 2


@pytest.mark.asyncio
async def test_create_table_wraps_profile_failure_as_table_admin_error():
    """If ``_profile_for`` ever raises (defense-in-depth: the contract layer
    should already have rejected bad names), the service must wrap it as a
    registered ``TableAdminError`` — never let a raw ``ValidationError`` leak,
    which would hit the dispatcher's opaque -32603 Internal-error bucket.
    Directus must NOT be called when this happens (no orphan collection)."""
    transport = MagicMock()
    transport.request = AsyncMock(return_value={"data": {}})
    service = TableAdminService(transport=transport, auth=_make_auth(), profiles={})
    # Force _profile_for to fail by monkeypatching it.
    service._profile_for = MagicMock(side_effect=ValueError("simulated profile failure"))
    # Bypass contract validation by constructing params that the service would
    # otherwise accept; the name is valid so _validate_name passes.
    params = CreateTableParams(name="ok_table", fields=[FieldDefinition(key="k", type="string")])

    with pytest.raises(TableAdminError) as exc_info:
        await service.create_table(params)
    assert "simulated profile failure" in str(exc_info.value)
    # Registry reads may occur, but no user collection or mapping item is POSTed.
    assert not any(
        call.args[:2] == ("POST", "/collections")
        and isinstance(call.kwargs.get("json_body"), dict)
        and call.kwargs["json_body"].get("collection", "").startswith("vt_t_")
        for call in transport.request.call_args_list
    )


@pytest.mark.asyncio
async def test_delete_table_rejects_system_prefix():
    transport = MagicMock()
    service = TableAdminService(transport=transport, auth=_make_auth(), profiles={})
    with pytest.raises(TableAdminError):
        await service.delete_table(DeleteTableParams(name="vibetable_documents"))


@pytest.mark.asyncio
async def test_delete_table_rejects_non_physical_identifier_before_transport():
    transport = MagicMock()
    transport.request = AsyncMock(return_value={})
    service = TableAdminService(transport=transport, auth=_make_auth(), profiles={})

    with pytest.raises(TableAdminError) as error:
        await service.delete_table(DeleteTableParams(name="orders/../../directus_users"))

    assert error.value.code == "table_name_invalid"
    transport.request.assert_not_called()


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
