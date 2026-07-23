from __future__ import annotations

import asyncio
from pathlib import Path
from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, SessionStatus
from backend.adapters.directus.profile import CapabilityManifest
from backend.adapters.directus.query import compile_directus_query
from backend.application.directus_service import DirectusService
from backend.contracts.directus import (
    DirectusCollectionParams,
    DirectusEmptyParams,
    DirectusReadParams,
    DirectusSubscribeParams,
    DirectusUnsubscribeParams,
)
from backend.contracts.lookup import LookupDefinition, LookupListResult
from backend.contracts.query import TableQuery
from backend.contracts.relation_admin import SchemaDescribeParams

ROOT = Path(__file__).resolve().parents[3]
MANIFEST = ROOT / "directus" / "capabilities" / "vibetable-empty-capabilities.json"


class FakeAuth:
    def status(self) -> SessionStatus:
        return SessionStatus(state="authenticated")

    async def current_user(self) -> CurrentUser:
        return CurrentUser(id="user-1", display_name="Test User")


class FakeClient:
    async def server_info(self) -> dict[str, Any]:
        return {
            "project": {"project_name": "VibeTable Test"},
            "version": "12.1.1",
        }

    async def fields(self, profile: Any) -> list[dict[str, Any]]:
        return [
            {
                "field": field,
                "type": "uuid" if field == "id" else "string",
                "meta": {"sort": index, "readonly": field in {"id", "date_updated"}},
                "schema": {
                    "is_primary_key": field == "id",
                    "is_nullable": field != "id",
                },
            }
            for index, field in enumerate(profile.fields)
        ]

    async def relations(self, profile: Any) -> list[dict[str, Any]]:
        return [{"meta": {"many_field": relation.field}} for relation in profile.relations]

    async def schema_fields(self) -> list[dict[str, Any]]:
        manifest = CapabilityManifest.model_validate_json(MANIFEST.read_text(encoding="utf-8"))
        return [
            {
                "collection": profile.collection,
                "field": field,
                "type": "uuid" if field == profile.primary_key else "string",
                "meta": {"sort": index, "readonly": field in {"id", "date_updated"}},
                "schema": {
                    "is_primary_key": field == profile.primary_key,
                    "is_nullable": field != profile.primary_key,
                },
            }
            for profile in manifest.collections
            for index, field in enumerate(profile.fields)
        ]

    async def schema_relations(self) -> list[dict[str, Any]]:
        return []

    async def permission_snapshot(self) -> Any:
        return []

    async def relation_lookup_capabilities(self) -> dict[str, Any]:
        return {
            "relation_read_v1": True,
            "relation_edit_v1": True,
            "lookup_query_v1": True,
            "reason": None,
        }

    async def read_items(
        self, profile: Any, query: TableQuery, *, include_archived: bool
    ) -> tuple[list[dict[str, Any]], dict[str, Any], Any]:
        plan = compile_directus_query(
            query,
            approved_fields=profile.approved_fields,
            primary_key=profile.primary_key,
        )
        return ([{"id": "item-1", "status": "active"}], {"filter_count": 1, "total_count": 2}, plan)


class FakeRealtime:
    def __init__(self) -> None:
        self.started = asyncio.Event()
        self.subscriptions: list[Any] = []

    async def run(self, subscriptions: list[Any], emit: Any, stop: Any) -> None:
        self.subscriptions = subscriptions
        self.started.set()
        await stop.wait()


def _service() -> DirectusService:
    manifest = CapabilityManifest.model_validate_json(MANIFEST.read_text(encoding="utf-8"))
    return DirectusService(manifest, FakeAuth(), FakeClient())


@pytest.mark.asyncio
async def test_schema_marks_only_manifest_update_fields_editable() -> None:
    result = await _service().schema(DirectusCollectionParams(collection="vibetable_documents"))

    columns = {column.name: column for column in result.columns}
    assert columns["id"].editable is False
    # ``file_name`` is in the document profile's update_fields allowlist.
    assert columns["file_name"].editable is True
    assert columns["date_updated"].editable is False
    assert result.capability_hash
    assert result.relations


@pytest.mark.asyncio
async def test_describe_schema_separates_live_revisions_and_adopts_relations() -> None:
    manifest = CapabilityManifest.model_validate(
        {
            "schema_version": "test-1",
            "directus_compatibility": ">=12 <13",
            "collections": [
                {
                    "collection": "orders",
                    "fields": ["id", "contract"],
                    "create_fields": ["id", "contract"],
                    "update_fields": ["contract"],
                    "archive_field": None,
                    "date_updated_field": None,
                }
            ],
        }
    )

    class LiveClient(FakeClient):
        async def schema_fields(self) -> list[dict[str, Any]]:
            return [
                {
                    "collection": "orders",
                    "field": "id",
                    "type": "uuid",
                    "meta": {},
                    "schema": {"is_primary_key": True, "is_nullable": False},
                },
                {
                    "collection": "orders",
                    "field": "contract",
                    "type": "uuid",
                    "meta": {"special": ["m2o"]},
                    "schema": {"is_primary_key": False, "is_nullable": True},
                },
                {
                    "collection": "contracts",
                    "field": "id",
                    "type": "uuid",
                    "meta": {},
                    "schema": {"is_primary_key": True, "is_nullable": False},
                },
            ]

        async def schema_relations(self) -> list[dict[str, Any]]:
            return [
                {
                    "collection": "orders",
                    "field": "contract",
                    "related_collection": "contracts",
                    "meta": {
                        "id": 7,
                        "many_collection": "orders",
                        "many_field": "contract",
                        "one_collection": "contracts",
                    },
                    "schema": {"on_delete": "SET NULL"},
                }
            ]

        async def permission_snapshot(self) -> Any:
            return [{"collection": "orders", "action": "read", "fields": ["*"]}]

    service = DirectusService(manifest, FakeAuth(), LiveClient())
    described = await service.describe_schema(
        SchemaDescribeParams(
            collection="orders",
            request_generation=3,
            accepts=[
                "vibetable.relation-capabilities.v1",
                "vibetable.lookup-query.v1",
            ],
        )
    )
    snapshot = described.schema_snapshot

    assert described.request_generation == 3
    assert described.capabilities.lookup_query_v1 is True
    assert snapshot.capability_hash == manifest.collections[0].capability_hash
    assert snapshot.schema_revision != snapshot.permission_revision
    assert snapshot.lookup_revision
    assert snapshot.normalized_relations[0].relation_id == "directus:7:m2o"
    assert snapshot.normalized_relations[0].field_ref == "orders.contract"
    assert {column.name: column.editable for column in snapshot.columns} == {
        "contract": True,
        "id": False,
    }
    columns = {column.name: column for column in snapshot.columns}
    assert columns["contract"].field_id == "orders.contract"
    assert columns["contract"].kind == "relation"
    assert columns["contract"].relation_id == "directus:7:m2o"


@pytest.mark.asyncio
async def test_directus_schema_carries_relation_and_lookup_columns() -> None:
    manifest = CapabilityManifest.model_validate(
        {
            "schema_version": "test-1",
            "directus_compatibility": ">=12 <13",
            "collections": [
                {
                    "collection": "orders",
                    "fields": ["id", "contract"],
                    "create_fields": ["id", "contract"],
                    "update_fields": ["contract"],
                    "archive_field": None,
                    "date_updated_field": None,
                }
            ],
        }
    )

    class LiveClient(FakeClient):
        async def schema_fields(self) -> list[dict[str, Any]]:
            return [
                {
                    "collection": "orders",
                    "field": "id",
                    "type": "uuid",
                    "meta": {},
                    "schema": {"is_primary_key": True, "is_nullable": False},
                },
                {
                    "collection": "orders",
                    "field": "contract",
                    "type": "uuid",
                    "meta": {"special": ["m2o"]},
                    "schema": {"is_primary_key": False, "is_nullable": True},
                },
                {
                    "collection": "contracts",
                    "field": "id",
                    "type": "uuid",
                    "meta": {},
                    "schema": {"is_primary_key": True, "is_nullable": False},
                },
            ]

        async def schema_relations(self) -> list[dict[str, Any]]:
            return [
                {
                    "collection": "orders",
                    "field": "contract",
                    "related_collection": "contracts",
                    "meta": {
                        "id": 7,
                        "many_collection": "orders",
                        "many_field": "contract",
                        "one_collection": "contracts",
                    },
                    "schema": {"on_delete": "SET NULL"},
                }
            ]

    definition = LookupDefinition.model_validate(
        {
            "lookupId": "orders.contract_price",
            "collection": "orders",
            "fieldKey": "contract_price",
            "displayName": "Contract price",
            "path": [{"relationId": "directus:7:m2o"}],
            "source": {"kind": "target_field", "fieldRef": "price"},
            "outputType": "decimal",
            "outputScale": 2,
        }
    )

    class Lookups:
        async def list(self, _params: Any) -> LookupListResult:
            return LookupListResult(
                collection="orders", definitions=[definition], lookup_revision="lookup-1"
            )

    service = DirectusService(manifest, FakeAuth(), LiveClient())
    service.lookup_service = Lookups()  # type: ignore[assignment]

    result = await service.schema(DirectusCollectionParams(collection="orders"))
    columns = {column.name: column for column in result.columns}

    assert columns["contract"].kind == "relation"
    assert columns["contract"].relation_id == "directus:7:m2o"
    assert columns["contract_price"].field_id == "orders.contract_price"
    assert columns["contract_price"].kind == "lookup"
    assert columns["contract_price"].lookup_id == "orders.contract_price"
    assert columns["contract_price"].editable is False


def test_schema_keeps_directus_readonly_update_field_non_editable() -> None:
    """Directus metadata must further restrict the profile write allowlist."""

    class ReadonlyFieldClient(FakeClient):
        async def schema_fields(self) -> list[dict[str, Any]]:
            fields = await super().schema_fields()
            for field in fields:
                if field["collection"] == "vibetable_documents" and field["field"] == "file_name":
                    field["meta"]["readonly"] = True
            return fields

    manifest = CapabilityManifest.model_validate_json(MANIFEST.read_text(encoding="utf-8"))
    service = DirectusService(manifest, FakeAuth(), ReadonlyFieldClient())

    result = asyncio.run(service.schema(DirectusCollectionParams(collection="vibetable_documents")))

    columns = {column.name: column for column in result.columns}
    # ``file_name`` is in update_fields, but Studio has marked it read-only.
    assert columns["file_name"].editable is False


@pytest.mark.asyncio
async def test_read_adds_transport_row_key_and_count_metadata() -> None:
    result = await _service().read(
        DirectusReadParams(collection="vibetable_documents", query=TableQuery(limit=25))
    )

    assert result.rows == [{"id": "item-1", "status": "active", "rowKey": "item-1"}]
    assert result.filtered_rows == 1
    assert result.total_rows == 2
    assert result.limit == 25


@pytest.mark.asyncio
async def test_collection_list_and_current_user_are_safe_dtos() -> None:
    service = _service()

    collections = await service.list_collections(DirectusEmptyParams())
    user = await service.current_user(DirectusEmptyParams())

    # Every built-in vibetable_* workspace collection is marked hidden in the
    # manifest, so none surface in the user-facing list.
    assert collections.collections == []
    assert collections.capability_hashes == {}
    assert user.model_dump(by_alias=True)["displayName"] == "Test User"


@pytest.mark.asyncio
async def test_list_collections_excludes_hidden_but_keeps_visible() -> None:
    # A custom manifest mixing one hidden and one visible collection proves the
    # filter keys off the hidden flag (not the empty-list boundary the real
    # manifest happens to hit when all six built-ins are hidden).
    manifest = CapabilityManifest.model_validate(
        {
            "contract": "directus.project.v1",
            "schema_version": "test-1.0",
            "directus_compatibility": ">=12 <13",
            "collections": [
                {
                    "collection": "internal_log",
                    "fields": ["id", "status", "date_updated"],
                    "create_fields": ["id", "status"],
                    "update_fields": ["status"],
                    "hidden": True,
                },
                {
                    "collection": "projects",
                    "fields": ["id", "status", "name", "date_updated"],
                    "create_fields": ["id", "status", "name"],
                    "update_fields": ["status", "name"],
                    "hidden": False,
                },
            ],
        }
    )
    service = DirectusService(manifest, FakeAuth(), FakeClient())

    class CachedTableAdmin:
        @property
        def display_names(self) -> dict[str, str]:
            return {"projects": "项目"}

        async def reconcile_identifiers(self) -> dict[str, str]:
            raise AssertionError("collection discovery must not run a full reconcile")

    service.table_admin_service = CachedTableAdmin()  # type: ignore[assignment]

    collections = await service.list_collections(DirectusEmptyParams())

    assert collections.collections == ["projects"]
    assert set(collections.capability_hashes) == {"projects"}
    assert collections.display_names == {"projects": "项目"}


@pytest.mark.asyncio
async def test_server_info_exposes_only_safe_compatibility_metadata() -> None:
    result = await _service().server_info(DirectusEmptyParams())

    assert result.project_name == "VibeTable Test"
    assert result.directus_version == "12.1.1"
    assert result.compatibility == ">=12 <13"


@pytest.mark.asyncio
async def test_subscribe_restarts_supervisor_and_unsubscribe_stops_it() -> None:
    manifest = CapabilityManifest.model_validate_json(MANIFEST.read_text(encoding="utf-8"))
    realtime = FakeRealtime()
    service = DirectusService(manifest, FakeAuth(), FakeClient(), realtime)

    result = await service.subscribe(
        DirectusSubscribeParams(
            uid="documents-main",
            collection="vibetable_documents",
            fields=["id", "status", "file_name"],
        )
    )
    await realtime.started.wait()

    assert result.active is True
    assert realtime.subscriptions[0][1].uid == "documents-main"
    stopped = await service.unsubscribe(DirectusUnsubscribeParams(uid="documents-main"))
    assert stopped.active is False
    assert stopped.collection == "vibetable_documents"


@pytest.mark.asyncio
async def test_subscribe_rejects_fields_outside_capability_profile() -> None:
    manifest = CapabilityManifest.model_validate_json(MANIFEST.read_text(encoding="utf-8"))
    service = DirectusService(manifest, FakeAuth(), FakeClient(), FakeRealtime())

    with pytest.raises(ValueError, match="allowlist"):
        await service.subscribe(
            DirectusSubscribeParams(
                uid="documents-secret",
                collection="vibetable_documents",
                fields=["internal_secret"],
            )
        )
