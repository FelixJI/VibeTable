from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest

from backend.adapters.directus.bootstrap import (
    DirectusProjectBootstrapper,
    build_collection_payload,
    build_permission_payloads,
    load_blueprint,
)
from backend.adapters.directus.errors import DirectusSchemaError

ROOT = Path(__file__).resolve().parents[3]
BLUEPRINT = ROOT / "directus" / "blueprints" / "vibetable-empty.json"


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        return self.responses.pop(0)


def test_collection_payload_has_uuid_pk_archive_and_system_fields() -> None:
    blueprint = load_blueprint(BLUEPRINT)

    payload = build_collection_payload(
        "vibetable_documents", blueprint["collections"]["vibetable_documents"]
    )

    fields = {field["field"]: field for field in payload["fields"]}
    assert fields["id"]["type"] == "uuid"
    assert fields["id"]["schema"]["is_primary_key"] is True
    assert fields["date_created"]["meta"]["special"] == ["date-created"]
    assert fields["user_updated"]["meta"]["special"] == ["user-updated"]
    assert payload["meta"]["archive_field"] == "status"
    # The empty VibeTable manifest disables Content Versions on every collection.
    assert payload["meta"]["versioning"] is False


@pytest.mark.asyncio
async def test_greenfield_apply_creates_collections_before_relations() -> None:
    blueprint = load_blueprint(BLUEPRINT)
    relation_count = sum(
        1
        for collection in blueprint["collections"].values()
        for field in collection["fields"].values()
        if field.get("relation")
    )
    transport = FakeTransport(
        [
            {"data": []},
            *({"data": {}} for _ in blueprint["collections"]),
            *({"data": {}} for _ in range(relation_count)),
            *(
                response
                for index in range(len(blueprint["policies"]))
                for response in (
                    {"data": {"id": f"policy-{index}"}},
                    {"data": {"id": f"role-{index}"}},
                    {"data": []},
                )
            ),
        ]
    )
    bootstrapper = DirectusProjectBootstrapper(transport, "admin-secret")

    actions = await bootstrapper.apply_empty(blueprint)

    assert all(action.state == "created" for action in actions)
    collection_requests = [r for r in transport.requests if r["path"] == "/collections"]
    relation_requests = [r for r in transport.requests if r["path"] == "/relations"]
    assert len(collection_requests) == 1 + len(blueprint["collections"])
    assert len(relation_requests) == relation_count
    assert len([r for r in transport.requests if r["path"] == "/policies"]) == 3
    assert len([r for r in transport.requests if r["path"] == "/roles"]) == 3
    permission_requests = [r for r in transport.requests if r["path"] == "/permissions"]
    assert len(permission_requests) == 3
    assert all(request["json_body"] for request in permission_requests)
    assert all(r["access_token"] == "admin-secret" for r in transport.requests)


def test_permission_matrix_is_explicit_allowlist_with_default_deny() -> None:
    blueprint = load_blueprint(BLUEPRINT)

    viewer = build_permission_payloads(
        "viewer-policy", blueprint["policies"]["viewer"], blueprint["collections"]
    )

    assert {permission["action"] for permission in viewer} == {"read"}
    assert {permission["collection"] for permission in viewer} == set(blueprint["collections"])
    assert all(permission["fields"] for permission in viewer)
    assert all(permission["policy"] == "viewer-policy" for permission in viewer)


@pytest.mark.asyncio
async def test_greenfield_apply_refuses_existing_business_collection() -> None:
    blueprint = load_blueprint(BLUEPRINT)
    transport = FakeTransport([{"data": [{"collection": "vibetable_documents"}]}])
    bootstrapper = DirectusProjectBootstrapper(transport, "admin-secret")

    with pytest.raises(DirectusSchemaError, match="greenfield-only"):
        await bootstrapper.apply_empty(blueprint)

    assert len(transport.requests) == 1
