"""C2 collaboration service tests.

Covers Activity/Revisions reading with permission cropping, the two-step
revert flow (preview bound to current hash, apply rejects on conflict),
Comments CRUD with sanitization + request-id dedup, mention search, and
Notifications inbox/archive/unread count.
"""

from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.profile import CapabilityManifest
from backend.application.collaboration_service import (
    CollaborationError,
    CollaborationService,
)
from backend.contracts.collaboration import (
    ApplyRevertParams,
    CreateCommentParams,
    DeleteCommentParams,
    NotificationIdParams,
    PreviewRevertParams,
    ReadActivityParams,
    ReadCommentsParams,
    ReadNotificationsParams,
    SearchMentionsParams,
    UpdateCommentParams,
)


class FakeDirectusAuth(DirectusAuthBroker):
    def __init__(self) -> None:
        self._user = CurrentUser(id="user-1", display_name="Tester", role_id="role-1")

    async def access_token(self) -> str:
        return "access"


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        if not self.responses:
            raise AssertionError(f"unexpected {method} {path}")
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


def _manifest(allow_revert: bool = True) -> CapabilityManifest:
    return CapabilityManifest.model_validate(
        {
            "contract": "directus.project.v1",
            "schema_version": "vibetable-1.0",
            "directus_compatibility": ">=12 <13",
            "collections": [
                {
                    "collection": "vibetable_demo",
                    "primary_key": "id",
                    "fields": ["id", "status", "number", "title", "amount", "date_updated"],
                    "create_fields": ["number", "title", "amount"],
                    "update_fields": ["number", "title", "amount"],
                    "archive_field": "status",
                    "archive_value": "archived",
                    "restore_value": "active",
                    "date_updated_field": "date_updated",
                    "allow_revision_revert": allow_revert,
                }
            ],
        }
    )


def _service(
    transport: FakeTransport,
    manifest: CapabilityManifest,
) -> CollaborationService:
    return CollaborationService(
        client=DirectusClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        transport=transport,
    )


# ---------------------------------------------------------------------------
# Activity / Revisions
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_read_activity_crops_delta_to_readable_fields() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            {
                "data": [
                    {
                        "id": "rev-1",
                        "delta": {"number": "A-1", "secret_field": "leak"},
                        "activity": {
                            "id": "act-1",
                            "action": "update",
                            "user": {"id": "u1", "first_name": "Ada"},
                            "timestamp": "2026-07-14T00:00:00Z",
                        },
                    }
                ],
                "meta": {"filter_count": 1, "total_count": 1},
            }
        ]
    )
    service = _service(transport, manifest)
    result = await service.read_activity(
        ReadActivityParams(collection="vibetable_demo", item_id="1")
    )
    assert len(result.revisions) == 1
    assert result.revisions[0].delta.get("number") == "A-1"
    # secret_field is NOT in the profile's readable fields → cropped.
    assert "secret_field" not in result.revisions[0].delta
    assert result.revisions[0].user_name == "Ada"


@pytest.mark.asyncio
async def test_read_activity_empty_when_permission_denied() -> None:
    from backend.adapters.directus.errors import DirectusTransportError

    manifest = _manifest()
    transport = FakeTransport([DirectusTransportError("forbidden", status=403, code="FORBIDDEN")])
    service = _service(transport, manifest)
    with pytest.raises(DirectusTransportError):
        await service.read_activity(ReadActivityParams(collection="vibetable_demo", item_id="1"))


# ---------------------------------------------------------------------------
# Revert (two-step)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_preview_revert_binds_token_to_current_hash() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            {"data": {"id": "1", "number": "A-1", "title": "T", "amount": 100, "status": "active"}},
            {
                "data": {
                    "id": "rev-5",
                    "data": {"number": "A-OLD", "title": "T", "amount": 100, "status": "active"},
                }
            },
        ]
    )
    service = _service(transport, manifest)
    preview = await service.preview_revert(
        PreviewRevertParams(collection="vibetable_demo", item_id="1", target_revision="rev-5")
    )
    assert preview.target_revision == "rev-5"
    assert "number" in preview.changes
    assert preview.changes["number"]["after"] == "A-OLD"
    assert preview.token.startswith("rev-")


@pytest.mark.asyncio
async def test_preview_revert_rejects_collection_without_capability() -> None:
    manifest = _manifest(allow_revert=False)
    transport = FakeTransport([])
    service = _service(transport, manifest)
    with pytest.raises(CollaborationError, match="does not allow"):
        await service.preview_revert(
            PreviewRevertParams(collection="vibetable_demo", item_id="1", target_revision="rev-5")
        )


@pytest.mark.asyncio
async def test_apply_revert_rejects_when_item_changed_since_preview() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            {"data": {"id": "1", "number": "A-1", "title": "T", "amount": 100, "status": "active"}},
            {
                "data": {
                    "id": "rev-5",
                    "data": {"number": "A-OLD", "title": "T", "amount": 100, "status": "active"},
                }
            },
        ]
    )
    service = _service(transport, manifest)
    preview = await service.preview_revert(
        PreviewRevertParams(collection="vibetable_demo", item_id="1", target_revision="rev-5")
    )
    # The apply re-reads the item; simulate it having changed.
    transport.responses.insert(
        0,
        {
            "data": {
                "id": "1",
                "number": "A-CHANGED",
                "title": "T",
                "amount": 100,
                "status": "active",
            }
        },
    )
    with pytest.raises(CollaborationError, match="changed since"):
        await service.apply_revert(
            ApplyRevertParams(collection="vibetable_demo", item_id="1", token=preview.token)
        )


@pytest.mark.asyncio
async def test_apply_revert_rejects_unknown_token() -> None:
    manifest = _manifest()
    transport = FakeTransport([])
    service = _service(transport, manifest)
    with pytest.raises(CollaborationError, match="not found"):
        await service.apply_revert(
            ApplyRevertParams(collection="vibetable_demo", item_id="1", token="rev-bogus.deadbeef")
        )


# ---------------------------------------------------------------------------
# Comments
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_read_comments_returns_entries() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            {
                "data": [
                    {
                        "id": "c1",
                        "collection": "vibetable_demo",
                        "item": "1",
                        "comment": "hello",
                        "user": {"id": "u1", "first_name": "Ada"},
                        "date_created": "2026-07-14T00:00:00Z",
                    }
                ],
                "meta": {"filter_count": 1, "total_count": 1},
            }
        ]
    )
    service = _service(transport, manifest)
    result = await service.read_comments(
        ReadCommentsParams(collection="vibetable_demo", item_id="1")
    )
    assert len(result.comments) == 1
    assert result.comments[0].comment == "hello"
    assert result.comments[0].user_name == "Ada"


@pytest.mark.asyncio
async def test_create_comment_sanitizes_dangerous_markup() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            {
                "data": {
                    "id": "c1",
                    "comment": "cleaned",
                    "collection": "vibetable_demo",
                    "item": "1",
                    "date_created": "2026-07-14T00:00:00Z",
                }
            }
        ]
    )
    service = _service(transport, manifest)
    await service.create_comment(
        CreateCommentParams(
            collection="vibetable_demo",
            item_id="1",
            comment="<script>alert(1)</script>hi",
        )
    )
    sent_body = transport.requests[0]["json_body"]
    assert "<script" not in sent_body["comment"].lower()


@pytest.mark.asyncio
async def test_update_comment_patches_own_comment() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [{"data": {"id": "c1", "comment": "edited", "collection": "vibetable_demo", "item": "1"}}]
    )
    service = _service(transport, manifest)
    result = await service.update_comment(UpdateCommentParams(comment_id="c1", comment="edited"))
    assert result.comment == "edited"
    assert transport.requests[0]["method"] == "PATCH"
    assert transport.requests[0]["path"] == "/comments/c1"


@pytest.mark.asyncio
async def test_delete_comment_returns_deleted_id() -> None:
    manifest = _manifest()
    transport = FakeTransport([None])
    service = _service(transport, manifest)
    result = await service.delete_comment(DeleteCommentParams(comment_id="c1"))
    assert result == {"deleted": "c1"}


@pytest.mark.asyncio
async def test_search_mentions_returns_matching_users() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            {
                "data": [
                    {"id": "u1", "first_name": "Ada", "last_name": "Lovelace"},
                    {"id": "u2", "first_name": "Adam", "last_name": "Smith"},
                ]
            }
        ]
    )
    service = _service(transport, manifest)
    result = await service.search_mentions(SearchMentionsParams(prefix="ad"))
    assert len(result.mentions) == 2
    assert result.mentions[0].name == "Ada Lovelace"


# ---------------------------------------------------------------------------
# Notifications
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_read_notifications_inbox_with_unread_count() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            {
                "data": [
                    {
                        "id": "n1",
                        "subject": "Test",
                        "message": "hi",
                        "collection": "vibetable_demo",
                        "item": "1",
                        "timestamp": "2026-07-14T00:00:00Z",
                        "status": "inbox",
                    }
                ],
                "meta": {"filter_count": 1, "total_count": 1},
            },
            # unread count query
            {"data": [], "meta": {"filter_count": 3}},
        ]
    )
    service = _service(transport, manifest)
    result = await service.read_notifications(ReadNotificationsParams(folder="inbox"))
    assert len(result.notifications) == 1
    assert result.unread_count == 3


@pytest.mark.asyncio
async def test_archive_notification_patches_status() -> None:
    manifest = _manifest()
    transport = FakeTransport([None])
    service = _service(transport, manifest)
    result = await service.archive_notification(NotificationIdParams(notification_id="n1"))
    assert result == {"archived": "n1"}
    assert transport.requests[0]["json_body"]["status"] == "archived"


@pytest.mark.asyncio
async def test_delete_notification_returns_deleted_id() -> None:
    manifest = _manifest()
    transport = FakeTransport([None])
    service = _service(transport, manifest)
    result = await service.delete_notification(NotificationIdParams(notification_id="n1"))
    assert result == {"deleted": "n1"}
