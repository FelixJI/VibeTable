"""Tests for the G1 HistoryService (ChangeSet aggregation + safe restore).

Validates the G1.3 gate:
* readChangeSets crops to readable fields and splits scalar/relation changes.
* previewRestore binds token to current_hash + schema_revision.
* applyRestore rejects token expiry, hash conflict and schema drift.
* applyRestore verifies the restore produced a new Revision.
* No permission field leaks into the response.
"""

from __future__ import annotations

import time
from typing import Any

import pytest

from backend.adapters.directus.profile import (
    CapabilityManifest,
    CollectionProfile,
    RelationProfile,
)
from backend.application.history_service import HistoryError, HistoryService

# ---------------------------------------------------------------------------
# Synthetic manifest
# ---------------------------------------------------------------------------


def _profile() -> CollectionProfile:
    return CollectionProfile(
        collection="vibetable_demo",
        primary_key="id",
        fields=[
            "id",
            "status",
            "title",
            "amount",
            "project",
            "date_updated",
        ],
        relations=[
            RelationProfile(field="project", kind="m2o", related_collection="vibetable_related")
        ],
        create_fields=["id", "status", "title", "amount", "project"],
        update_fields=["status", "title", "amount", "project"],
        allow_revision_history=True,
        allow_revision_revert=True,
    )


def _manifest() -> CapabilityManifest:
    return CapabilityManifest(
        schema_version="vibetable-1.0",
        directus_compatibility=">=12 <13",
        collections=[_profile()],
    )


# ---------------------------------------------------------------------------
# Fake transport
# ---------------------------------------------------------------------------


class FakeTransport:
    def __init__(self, responses: list[Any] | None = None) -> None:
        self.responses = list(responses or [])
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        if self.responses:
            return self.responses.pop(0)
        return {"data": [], "meta": {"filter_count": 0, "total_count": 0}}


class FakeClient:
    def __init__(self, item: dict[str, Any]) -> None:
        self._item = item
        self.updates: list[dict[str, Any]] = []

    async def read_item(self, profile: Any, item_id: str) -> dict[str, Any]:
        return dict(self._item)

    async def update_item(
        self, profile: Any, item_id: str, values: dict[str, Any], **kwargs: Any
    ) -> dict[str, Any]:
        self.updates.append(dict(values))
        self._item.update(values)
        return dict(self._item)


class FakeAuth:
    def __init__(self) -> None:
        self._token = "test-access-token"

    async def access_token(self) -> str:
        return self._token


def _make_service(
    *,
    transport: FakeTransport,
    item: dict[str, Any],
    schema_revision: str = "vibetable-1.0",
    clock: Any = None,
) -> HistoryService:
    manifest = _manifest()
    return HistoryService(
        client=FakeClient(item),
        auth=FakeAuth(),
        profiles=manifest.by_collection,
        transport=transport,
        schema_revision=schema_revision,
        clock=clock or time.time,
    )


# ---------------------------------------------------------------------------
# readChangeSets
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_read_change_sets_returns_scalar_and_relation_changes() -> None:
    revisions = {
        "data": [
            {
                "id": "rev-10",
                "delta": {"title": "New Title", "amount": 15000.0, "project": "p-002"},
                "activity": {
                    "id": "act-10",
                    "action": "update",
                    "user": {"id": "u-1", "first_name": "Ada", "last_name": "Lovelace"},
                    "timestamp": "2026-07-15T10:00:00Z",
                },
            }
        ],
        "meta": {"filter_count": 1, "total_count": 1},
    }
    transport = FakeTransport(responses=[revisions])
    service = _make_service(transport=transport, item={"title": "Old"})
    result = await service.read_change_sets(_params(collection="vibetable_demo", item_id="c-001"))
    assert len(result.change_sets) == 1
    cs = result.change_sets[0]
    assert cs.action == "update"
    assert cs.actor.display_name == "Ada Lovelace"
    # title and amount are scalar; project is a relation.
    assert len(cs.scalar_changes) == 2
    assert len(cs.relation_changes) == 1
    assert cs.relation_changes[0].field == "project"
    assert cs.relation_changes[0].related_collection == "vibetable_related"


@pytest.mark.asyncio
async def test_read_change_sets_crops_unreadable_fields() -> None:
    """Fields not in the profile must not appear in the response."""
    revisions = {
        "data": [
            {
                "id": "rev-1",
                "delta": {"title": "Visible", "secret_field": "LEAKED"},
                "activity": {
                    "id": "act-1",
                    "action": "update",
                    "user": {"id": "u-1", "first_name": "A", "last_name": "B"},
                    "timestamp": "2026-07-15T10:00:00Z",
                },
            }
        ],
        "meta": {"filter_count": 1, "total_count": 1},
    }
    transport = FakeTransport(responses=[revisions])
    service = _make_service(transport=transport, item={})
    result = await service.read_change_sets(_params(collection="vibetable_demo", item_id="c-001"))
    cs = result.change_sets[0]
    fields = {sc.field for sc in cs.scalar_changes}
    assert "secret_field" not in fields
    assert "title" in fields


@pytest.mark.asyncio
async def test_read_change_sets_rejects_collection_without_history() -> None:
    transport = FakeTransport()
    service = _make_service(transport=transport, item={})
    # Sabotage: disable revision history.
    service._profiles["vibetable_demo"] = service._profiles["vibetable_demo"].model_copy(
        update={"allow_revision_history": False}
    )
    with pytest.raises(HistoryError, match="does not allow revision history"):
        await service.read_change_sets(_params(collection="vibetable_demo", item_id="c-001"))


# ---------------------------------------------------------------------------
# previewRestore
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_preview_restore_returns_token_and_changes() -> None:
    current_item = {"title": "Current", "amount": 100.0, "project": "p-001"}
    revision_data = {"data": {"data": {"title": "Target", "amount": 200.0, "project": "p-002"}}}
    transport = FakeTransport(
        responses=[revision_data, {"data": {"id": "p-002", "title": "Project Two"}}]
    )
    service = _make_service(transport=transport, item=current_item)
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="vibetable_demo", item_id="c-001", target_revision="rev-9")
    )
    assert preview.token.startswith("rst-")
    assert preview.schema_revision == "vibetable-1.0"
    assert len(preview.scalar_changes) == 2  # title + amount
    assert len(preview.relation_changes) == 1  # project
    assert preview.diagnostics == []


@pytest.mark.asyncio
async def test_preview_restore_generates_diagnostics_for_retired_fields() -> None:
    current_item = {"title": "Current"}
    # revision_data includes a field NOT in the readable profile.
    revision_data = {"data": {"data": {"title": "Target", "deleted_field": "old"}}}
    transport = FakeTransport(responses=[revision_data])
    service = _make_service(transport=transport, item=current_item)
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="vibetable_demo", item_id="c-001", target_revision="rev-8")
    )
    assert len(preview.diagnostics) == 1
    assert preview.diagnostics[0].field == "deleted_field"
    assert preview.diagnostics[0].classification == "schema_retired"


# ---------------------------------------------------------------------------
# applyRestore
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_apply_restore_rejects_unknown_token() -> None:
    transport = FakeTransport()
    service = _make_service(transport=transport, item={})
    with pytest.raises(HistoryError, match="token not found"):
        await service.apply_restore(
            ApplyRestoreParams(collection="vibetable_demo", item_id="c-001", token="bogus")
        )


@pytest.mark.asyncio
async def test_apply_restore_rejects_expired_token() -> None:
    current_item = {"title": "Current", "amount": 100.0, "project": "p-001"}
    revision_data = {"data": {"data": {"title": "Target", "amount": 200.0}}}
    transport = FakeTransport(responses=[revision_data])

    fixed_time = 1000.0
    service = _make_service(transport=transport, item=current_item, clock=lambda: fixed_time)
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="vibetable_demo", item_id="c-001", target_revision="rev-9")
    )
    # Advance clock past TTL.
    service._clock = lambda: fixed_time + 999
    with pytest.raises(HistoryError, match="expired"):
        await service.apply_restore(
            ApplyRestoreParams(collection="vibetable_demo", item_id="c-001", token=preview.token)
        )


@pytest.mark.asyncio
async def test_apply_restore_rejects_hash_conflict() -> None:
    """If the item changed since preview, apply must fail."""
    current_item = {"title": "Current", "amount": 100.0, "project": "p-001"}
    revision_data = {"data": {"data": {"title": "Target", "amount": 200.0}}}
    transport = FakeTransport(responses=[revision_data])
    service = _make_service(transport=transport, item=current_item)
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="vibetable_demo", item_id="c-001", target_revision="rev-9")
    )
    # Simulate the item changing after preview by using a different item.
    service._client = FakeClient({"title": "CHANGED", "amount": 999.0, "project": "p-001"})
    with pytest.raises(HistoryError, match="item changed"):
        await service.apply_restore(
            ApplyRestoreParams(collection="vibetable_demo", item_id="c-001", token=preview.token)
        )


@pytest.mark.asyncio
async def test_apply_restore_rejects_schema_drift() -> None:
    """If schema_revision changed since preview, apply must fail."""
    current_item = {"title": "Current", "amount": 100.0, "project": "p-001"}
    revision_data = {"data": {"data": {"title": "Target"}}}
    transport = FakeTransport(responses=[revision_data])
    service = _make_service(transport=transport, item=current_item, schema_revision="vibetable-1.0")
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="vibetable_demo", item_id="c-001", target_revision="rev-9")
    )
    # Simulate schema change.
    service._schema_revision = "vibetable-1.1"
    with pytest.raises(HistoryError, match="schema changed"):
        await service.apply_restore(
            ApplyRestoreParams(collection="vibetable_demo", item_id="c-001", token=preview.token)
        )


@pytest.mark.asyncio
async def test_apply_restore_succeeds_and_records_new_revision() -> None:
    current_item = {"title": "Current", "amount": 100.0, "project": "p-001"}
    revision_data = {"data": {"data": {"title": "Target", "amount": 200.0}}}
    # Responses: revision_data for preview, then latest before/after the field PATCH.
    transport = FakeTransport(
        responses=[
            revision_data,  # preview reads revision
            {"data": [{"id": "rev-9"}]},  # pre-revert latest
            {"data": [{"id": "rev-11"}]},  # post-revert latest (new id)
        ]
    )
    service = _make_service(transport=transport, item=current_item)
    # Override client to return refreshed item after revert.
    service._client = AsyncMockItemClient(current_item)

    preview = await service.preview_restore(
        PreviewRestoreParams(collection="vibetable_demo", item_id="c-001", target_revision="rev-9")
    )
    result = await service.apply_restore(
        ApplyRestoreParams(collection="vibetable_demo", item_id="c-001", token=preview.token)
    )
    assert result.restored_to_revision == "rev-9"
    assert result.new_revision_id == "rev-11"


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


class AsyncMockItemClient:
    """Returns the same item for every read_item call (for apply tests)."""

    def __init__(self, item: dict[str, Any]) -> None:
        self._item = item
        self.updates: list[dict[str, Any]] = []

    async def read_item(self, profile: Any, item_id: str) -> dict[str, Any]:
        return dict(self._item)

    async def update_item(
        self, profile: Any, item_id: str, values: dict[str, Any], **kwargs: Any
    ) -> dict[str, Any]:
        self.updates.append(dict(values))
        self._item.update(values)
        return dict(self._item)


def _params(*, collection: str, item_id: str):
    from backend.contracts.history import ReadChangeSetsParams

    return ReadChangeSetsParams(collection=collection, item_id=item_id)


# Import at bottom to avoid circulars in the test helper.
from backend.contracts.history import (  # noqa: E402
    ApplyRestoreParams,
    PreviewRestoreParams,
)
