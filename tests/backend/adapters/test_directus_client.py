from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusTransportError
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
async def test_create_uses_client_uuid_and_verifies_uncertain_result() -> None:
    transport = FakeTransport(
        [
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
    assert transport.requests[0]["headers"] == {"X-Request-ID": "request-1"}
    assert transport.requests[1]["path"].endswith("/known-id")


@pytest.mark.asyncio
async def test_update_detects_changed_date_updated_before_patch() -> None:
    transport = FakeTransport([{"data": {"id": "1", "date_updated": "new"}}])
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
    assert len(transport.requests) == 1


@pytest.mark.asyncio
async def test_delete_archives_unless_profile_allows_permanent_delete() -> None:
    transport = FakeTransport([{"data": {"id": "1", "status": "archived"}}])
    client = DirectusClient(transport, FakeAuth())

    await client.delete_item(_profile(), "1")

    assert transport.requests[0]["method"] == "PATCH"
    assert transport.requests[0]["json_body"] == {"status": "archived"}


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
