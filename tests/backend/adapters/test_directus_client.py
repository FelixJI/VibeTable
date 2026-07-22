from __future__ import annotations

import asyncio
from typing import Any

import pytest

from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusSchemaError, DirectusTransportError
from backend.adapters.directus.profile import CollectionProfile, RelationProfile
from backend.contracts.query import FilterCondition, TableQuery


class FakeAuth:
    async def access_token(self) -> str:
        return "user-access"


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


def _profile() -> CollectionProfile:
    return CollectionProfile(
        collection="contracts",
        fields=["id", "number", "status", "owner", "date_updated"],
        create_fields=["id", "number", "status", "owner"],
        update_fields=["number", "status", "owner"],
        relations=[
            RelationProfile(
                field="owner",
                kind="m2o",
                related_collection="directus_users",
                display_fields=["first_name", "last_name"],
            )
        ],
    )


def _fields_response(*, readonly: set[str] | None = None) -> dict[str, Any]:
    readonly = readonly or set()
    return {
        "data": [
            {
                "field": name,
                "meta": {"readonly": name in readonly},
                "schema": {"is_generated": False},
            }
            for name in _profile().fields
        ]
    }


@pytest.mark.asyncio
async def test_read_items_applies_profile_fields_archive_filter_and_user_token() -> None:
    transport = FakeTransport(
        [{"data": [{"id": "1", "status": "active"}], "meta": {"filter_count": 1}}]
    )
    client = DirectusClient(transport, FakeAuth())

    rows, meta, plan = await client.read_items(
        _profile(),
        TableQuery(filters=[FilterCondition(field="number", operator="contains", value="A")]),
    )

    assert rows[0]["id"] == "1"
    assert meta["filter_count"] == 1
    assert plan.referenced_fields == ["number"]
    request = transport.requests[0]
    assert request["access_token"] == "user-access"
    assert request["query"]["fields"] == _profile().fields
    assert request["query"]["filter"] == {
        "_and": [
            {"number": {"_contains": "A"}},
            {"status": {"_neq": "archived"}},
        ]
    }


@pytest.mark.asyncio
async def test_read_item_accepts_a_live_permission_cropped_field_set() -> None:
    transport = FakeTransport([{"data": {"id": "1", "number": "A-1"}}])
    client = DirectusClient(transport, FakeAuth())

    await client.read_item(_profile(), "1", fields=["id", "number"])

    assert transport.requests[0]["query"]["fields"] == ["id", "number"]


@pytest.mark.asyncio
async def test_create_uses_client_uuid_and_verifies_uncertain_result() -> None:
    transport = FakeTransport(
        [
            _fields_response(),
            DirectusTransportError("offline", code="SERVICE_UNAVAILABLE"),
            {"data": {"id": "known-id", "number": "A-1"}},
        ]
    )
    client = DirectusClient(transport, FakeAuth())

    created = await client.create_item(
        _profile(),
        {"id": "known-id", "number": "A-1", "status": "active"},
        request_id="request-1",
    )

    assert created["id"] == "known-id"
    assert transport.requests[1]["headers"] == {"X-Request-ID": "request-1"}
    assert transport.requests[2]["path"].endswith("/known-id")


def test_create_rejects_directus_readonly_field_before_post() -> None:
    transport = FakeTransport(
        [
            _fields_response(),
            _fields_response(readonly={"owner"}),
        ]
    )
    client = DirectusClient(transport, FakeAuth())

    async def create_after_studio_policy_change() -> None:
        await client.fields(_profile())
        await client.create_item(_profile(), {"number": "A-1", "owner": "user-2"})

    with pytest.raises(DirectusSchemaError, match=r"read-only.*owner"):
        asyncio.run(create_after_studio_policy_change())

    assert [request["method"] for request in transport.requests] == ["GET", "GET"]
    assert {request["path"] for request in transport.requests} == {"/fields/contracts"}


@pytest.mark.asyncio
async def test_update_detects_changed_date_updated_before_patch() -> None:
    transport = FakeTransport([_fields_response(), {"data": {"id": "1", "date_updated": "new"}}])
    client = DirectusClient(transport, FakeAuth())

    with pytest.raises(DirectusTransportError) as captured:
        await client.update_item(
            _profile(),
            "1",
            {"number": "A-2"},
            expected_date_updated="old",
        )

    assert captured.value.status == 409
    assert captured.value.code == "EDIT_CONFLICT"
    assert [request["method"] for request in transport.requests] == ["GET", "GET"]
    assert all(request["method"] != "PATCH" for request in transport.requests)


@pytest.mark.asyncio
async def test_conditional_update_compares_only_selected_fields_before_patch() -> None:
    transport = FakeTransport(
        [
            _fields_response(),
            {"data": {"id": "1", "number": "A-2", "status": "changed-elsewhere"}},
        ]
    )
    client = DirectusClient(transport, FakeAuth())

    updated = await client.update_item_if_unchanged(
        _profile(),
        "1",
        {"number": "A-2"},
        expected_values={"number": "A-1"},
        request_id="restore-1",
        operation="restore",
        authorization_token="authorization-42",
    )

    assert updated["number"] == "A-2"
    assert transport.requests[-1]["method"] == "POST"
    assert transport.requests[-1]["path"] == "/vibetable-bulk-mutation/restore"
    assert transport.requests[-1]["headers"] == {
        "Idempotency-Key": "restore-1",
    }
    assert transport.requests[-1]["json_body"] == {
        "contract": "vibetable-history-restore.v1",
        "authorizationToken": "authorization-42",
    }


@pytest.mark.asyncio
async def test_conditional_update_rejects_changed_selected_field_without_patch() -> None:
    transport = FakeTransport([_fields_response(), {"data": []}])
    client = DirectusClient(transport, FakeAuth())

    with pytest.raises(DirectusTransportError) as captured:
        await client.update_item_if_unchanged(
            _profile(),
            "1",
            {"number": "A-2"},
            expected_values={"number": "A-1"},
        )

    assert captured.value.code == "EDIT_CONFLICT"
    assert transport.requests[-1]["method"] == "PATCH"


def test_update_rejects_directus_readonly_field_before_patch() -> None:
    transport = FakeTransport(
        [
            _fields_response(),
            _fields_response(readonly={"owner"}),
        ]
    )
    client = DirectusClient(transport, FakeAuth())

    async def update_after_studio_policy_change() -> None:
        # Prime the cache with an editable schema, then simulate Studio making
        # the same field read-only before the user commits their edit.
        await client.fields(_profile())
        await client.update_item(_profile(), "1", {"owner": "user-2"})

    with pytest.raises(DirectusSchemaError, match=r"read-only.*owner"):
        asyncio.run(update_after_studio_policy_change())

    assert [request["method"] for request in transport.requests] == ["GET", "GET"]
    assert {request["path"] for request in transport.requests} == {"/fields/contracts"}


@pytest.mark.asyncio
async def test_delete_archives_unless_profile_allows_permanent_delete() -> None:
    transport = FakeTransport([_fields_response(), {"data": {"id": "1", "status": "archived"}}])
    client = DirectusClient(transport, FakeAuth())

    await client.delete_item(_profile(), "1")

    assert transport.requests[1]["method"] == "PATCH"
    assert transport.requests[1]["json_body"] == {"status": "archived"}


@pytest.mark.asyncio
async def test_relations_are_pruned_to_profile_allowlist() -> None:
    transport = FakeTransport(
        [
            {
                "data": [
                    {"meta": {"many_field": "owner"}},
                    {"meta": {"many_field": "internal_secret"}},
                ]
            }
        ]
    )
    client = DirectusClient(transport, FakeAuth())

    relations = await client.relations(_profile())

    assert relations == [{"meta": {"many_field": "owner"}}]
