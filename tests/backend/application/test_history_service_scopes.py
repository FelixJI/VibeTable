from __future__ import annotations

import time
from typing import Any

import pytest

from backend.adapters.directus.errors import DirectusTransportError
from backend.adapters.directus.profile import CollectionProfile, RelationProfile
from backend.application.history_service import HistoryError, HistoryService
from backend.contracts.history import ApplyRestoreParams, PreviewRestoreParams, ReadChangeSetsParams


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        if not self.responses:
            raise AssertionError(f"unexpected request: {method} {path}")
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


class FakeAuth:
    async def access_token(self) -> str:
        return "token"


class FakeClient:
    def __init__(self, item: dict[str, Any], fields: list[dict[str, Any]] | None = None) -> None:
        self.item = dict(item)
        self.field_metadata = fields or _metadata()
        self.updates: list[dict[str, Any]] = []

    async def read_item(self, profile: Any, item_id: str) -> dict[str, Any]:
        return dict(self.item)

    async def fields(self, profile: Any) -> list[dict[str, Any]]:
        return list(self.field_metadata)

    async def update_item(
        self, profile: Any, item_id: str, values: dict[str, Any], **kwargs: Any
    ) -> dict[str, Any]:
        self.updates.append(dict(values))
        self.item.update(values)
        return dict(self.item)


def _profile() -> CollectionProfile:
    return CollectionProfile(
        collection="orders",
        primary_key="id",
        fields=[
            "id",
            "status",
            "title",
            "amount",
            "project",
            "links",
            "computed",
            "locked",
            "date_updated",
        ],
        relations=[
            RelationProfile(
                field="project",
                kind="m2o",
                related_collection="projects",
                display_fields=["title"],
            ),
            RelationProfile(field="links", kind="m2m", related_collection="tags"),
        ],
        create_fields=["id", "status", "title", "amount", "project", "links"],
        update_fields=["status", "title", "amount", "project", "links"],
        archive_field="status",
        allow_revision_history=True,
        allow_revision_revert=True,
    )


def _metadata() -> list[dict[str, Any]]:
    return [
        {
            "field": "id",
            "type": "uuid",
            "meta": {"readonly": True},
            "schema": {"is_primary_key": True},
        },
        {"field": "status", "type": "string", "meta": {}, "schema": {}},
        {"field": "title", "type": "string", "meta": {}, "schema": {}},
        {"field": "amount", "type": "integer", "meta": {}, "schema": {}},
        {"field": "project", "type": "uuid", "meta": {}, "schema": {}},
        {"field": "links", "type": "json", "meta": {}, "schema": {}},
        {"field": "computed", "type": "integer", "meta": {}, "schema": {"is_generated": True}},
        {"field": "locked", "type": "string", "meta": {}, "schema": {}},
        {
            "field": "date_updated",
            "type": "dateTime",
            "meta": {"readonly": True, "special": ["date-updated"]},
            "schema": {},
        },
    ]


def _service(
    responses: list[Any], *, item: dict[str, Any] | None = None, clock: Any = time.time
) -> tuple[HistoryService, FakeTransport, FakeClient]:
    profile = _profile()
    transport = FakeTransport(responses)
    client = FakeClient(item or {"id": "1", "status": "active", "title": "Now", "amount": 2})
    service = HistoryService(
        client=client,
        auth=FakeAuth(),
        profiles={profile.collection: profile},
        transport=transport,
        schema_revision="schema-1",
        clock=clock,
    )
    return service, transport, client


def _revisions() -> dict[str, Any]:
    return {
        "data": [
            {
                "id": "r3",
                "collection": "orders",
                "item": "2",
                "data": {"id": "2", "title": "Other", "amount": 5},
                "delta": {"title": "Other", "amount": 5},
                "activity": {
                    "id": "batch-1",
                    "action": "update",
                    "timestamp": "2026-07-22T11:00:00Z",
                    "user": {"id": "u1", "first_name": "Ada", "last_name": "Lovelace"},
                },
            },
            {
                "id": "r2",
                "collection": "orders",
                "item": "1",
                "data": {"id": "1", "title": "New", "amount": 2, "secret": "LEAK"},
                "delta": {"title": "New", "secret": "LEAK"},
                "activity": {
                    "id": "batch-1",
                    "action": "update",
                    "timestamp": "2026-07-22T11:00:00Z",
                    "user": {"id": "u1", "first_name": "Ada", "last_name": "Lovelace"},
                },
            },
            {
                "id": "r1",
                "collection": "orders",
                "item": "1",
                "data": {"id": "1", "title": "Old", "amount": 2},
                "delta": {"title": "Old", "amount": 2},
                "activity": {
                    "id": "create-1",
                    "action": "create",
                    "timestamp": "2026-07-22T10:00:00Z",
                    "user": {"id": "u1", "first_name": "Ada", "last_name": "Lovelace"},
                },
            },
        ]
    }


@pytest.mark.asyncio
async def test_table_history_groups_activity_and_builds_adjacent_diffs() -> None:
    service, transport, _ = _service([_revisions()])
    page = await service.read_change_sets(ReadChangeSetsParams(collection="orders", scope="table"))

    assert page.total == 2
    assert page.change_sets[0].activity_id == "batch-1"
    assert page.change_sets[0].affected_records == 2
    item_one = next(
        record for record in page.change_sets[0].record_changes if record.item_id == "1"
    )
    title = next(change for change in item_one.scalar_changes if change.field == "title")
    assert (title.before, title.after) == ("Old", "New")
    assert transport.requests[0]["query"]["limit"] == -1


@pytest.mark.asyncio
async def test_search_cannot_match_unreadable_history_values() -> None:
    service, _, _ = _service([_revisions()])
    page = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="table", search="LEAK")
    )
    assert page.total == 0
    assert page.change_sets == []


@pytest.mark.asyncio
async def test_revision_with_only_unreadable_changes_does_not_affect_result_count() -> None:
    revision = {
        "data": [
            {
                "id": "secret-only",
                "item": "1",
                "data": {"secret": "hidden"},
                "delta": {"secret": "hidden"},
                "activity": {
                    "id": "secret-activity",
                    "action": "update",
                    "timestamp": "2026-07-22T12:00:00Z",
                    "user": {"id": "u1"},
                },
            }
        ]
    }
    service, _, _ = _service([revision])
    page = await service.read_change_sets(ReadChangeSetsParams(collection="orders", scope="table"))
    assert page.total == 0


@pytest.mark.asyncio
async def test_cell_scope_crops_other_fields_and_pages_groups() -> None:
    service, _, _ = _service([_revisions()])
    page = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="cell", item_id="1", field="title", limit=1)
    )
    assert page.total == 2
    assert page.has_more is True
    assert {change.field for change in page.change_sets[0].scalar_changes} == {"title"}


@pytest.mark.asyncio
async def test_archived_scope_lists_only_current_archived_records() -> None:
    service, transport, _ = _service(
        [
            {"data": [{"id": "2", "title": "Deleted order"}]},
            {"data": [_revisions()["data"][0]]},
        ]
    )
    page = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="archived")
    )
    assert page.total == 1
    assert page.change_sets[0].item_id == "2"
    archive_query = transport.requests[0]["query"]
    assert archive_query["filter"] == {"status": {"_eq": "archived"}}


@pytest.mark.asyncio
async def test_history_relation_labels_are_resolved_without_leaking_unavailable_targets() -> None:
    revisions = {
        "data": [
            {
                "id": "r2",
                "item": "1",
                "data": {"project": "p2"},
                "delta": {"project": "p2"},
                "activity": {"id": "a2", "timestamp": "2026-07-22T11:00:00Z"},
            },
            {
                "id": "r1",
                "item": "1",
                "data": {"project": "p1"},
                "delta": {"project": "p1"},
                "activity": {"id": "a1", "timestamp": "2026-07-22T10:00:00Z"},
            },
        ]
    }
    service, _, _ = _service([revisions, {"data": {"title": "Alpha"}}, {"data": {"title": "Beta"}}])
    page = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="row", item_id="1")
    )
    change = page.change_sets[0].relation_changes[0]
    assert change.before_display_value == "Alpha"
    assert change.after_display_value == "Beta"
    assert change.target_available is True


@pytest.mark.asyncio
async def test_preview_classifies_non_writable_generated_incompatible_and_complex_fields() -> None:
    target = {
        "id": "different",
        "title": "Old title",
        "amount": "not-an-integer",
        "computed": 99,
        "locked": "old",
        "date_updated": "2025-01-01T00:00:00Z",
        "links": ["tag-1"],
    }
    service, _, _ = _service([{"data": {"id": "r1", "item": "1", "data": target}}])
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )
    assert preview.can_apply is True
    assert preview.restorable_fields == ["title"]
    codes = {diagnostic.code for diagnostic in preview.diagnostics}
    assert codes == {
        "primary_key",
        "type_incompatible",
        "field_generated",
        "field_not_updatable",
        "system_field",
        "complex_relation",
    }


@pytest.mark.asyncio
async def test_m2o_restore_requires_readable_target_and_uses_safe_display() -> None:
    service, _, _ = _service(
        [
            {"data": {"id": "r1", "item": "1", "data": {"project": "p2"}}},
            {"data": {"id": "p2", "title": "Apollo"}},
        ],
        item={"id": "1", "status": "active", "project": "p1"},
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )
    assert preview.restorable_fields == ["project"]
    assert preview.relation_changes[0].display_value == "Apollo"


@pytest.mark.asyncio
async def test_unreadable_m2o_target_is_safely_skipped() -> None:
    service, _, _ = _service(
        [
            {"data": {"id": "r1", "item": "1", "data": {"project": "p2"}}},
            DirectusTransportError("forbidden", status=403, code="FORBIDDEN"),
        ],
        item={"id": "1", "status": "active", "project": "p1"},
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )
    assert preview.can_apply is False
    assert preview.relation_changes == []
    assert preview.diagnostics[0].code == "relation_target_unavailable"


@pytest.mark.asyncio
async def test_cell_restore_ignores_unrelated_row_changes_and_patches_one_field() -> None:
    service, _, client = _service(
        [
            {"data": {"id": "r1", "item": "1", "data": {"title": "Old"}}},
            {"data": [{"id": "r9"}]},
            {"data": [{"id": "r10"}]},
        ]
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(
            collection="orders", item_id="1", target_revision="r1", scope="cell", field="title"
        )
    )
    client.item["amount"] = 999
    result = await service.apply_restore(
        ApplyRestoreParams(collection="orders", item_id="1", token=preview.token)
    )
    assert client.updates == [{"title": "Old"}]
    assert result.new_revision_id == "r10"


@pytest.mark.asyncio
async def test_row_restore_conflicts_when_a_restorable_field_changed() -> None:
    service, _, client = _service([{"data": {"id": "r1", "item": "1", "data": {"title": "Old"}}}])
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )
    client.item["title"] = "Concurrent"
    with pytest.raises(HistoryError) as error:
        await service.apply_restore(
            ApplyRestoreParams(collection="orders", item_id="1", token=preview.token)
        )
    assert error.value.code == "restore_conflict"


@pytest.mark.asyncio
async def test_archived_restore_checks_revision_and_unarchives_with_target_patch() -> None:
    service, _, client = _service(
        [
            {"data": {"id": "r4", "item": "1", "data": {"title": "Before delete"}}},
            {"data": [{"id": "r5"}]},
            {"data": [{"id": "r5"}]},
            {"data": [{"id": "r6"}]},
        ],
        item={"id": "1", "status": "archived", "title": "Deleted"},
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(
            collection="orders", item_id="1", target_revision="r4", scope="archived"
        )
    )
    assert set(preview.restorable_fields) == {"status", "title"}
    result = await service.apply_restore(
        ApplyRestoreParams(collection="orders", item_id="1", token=preview.token)
    )
    assert client.updates == [{"title": "Before delete", "status": "active"}]
    assert result.item["status"] == "active"


@pytest.mark.asyncio
async def test_archived_restore_is_disabled_without_archive_field_update_permission() -> None:
    service, _, _ = _service(
        [
            {"data": {"id": "r4", "item": "1", "data": {"title": "Before delete"}}},
            {"data": [{"id": "r5"}]},
        ],
        item={"id": "1", "status": "archived", "title": "Deleted"},
    )
    profile = service._profiles["orders"]
    service._profiles["orders"] = profile.model_copy(
        update={"update_fields": [field for field in profile.update_fields if field != "status"]}
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(
            collection="orders", item_id="1", target_revision="r4", scope="archived"
        )
    )
    assert preview.can_apply is False
    assert preview.restorable_fields == []
    assert any(diagnostic.code == "field_not_updatable" for diagnostic in preview.diagnostics)


@pytest.mark.asyncio
async def test_apply_fails_if_patch_does_not_create_revision() -> None:
    service, _, _ = _service(
        [
            {"data": {"id": "r1", "item": "1", "data": {"title": "Old"}}},
            {"data": [{"id": "r9"}]},
            {"data": [{"id": "r9"}]},
        ]
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )
    with pytest.raises(HistoryError) as error:
        await service.apply_restore(
            ApplyRestoreParams(collection="orders", item_id="1", token=preview.token)
        )
    assert error.value.code == "revision_not_created"
