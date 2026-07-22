from __future__ import annotations

import base64
import hashlib
import json
import time
from typing import Any

import pytest

import backend.application.history_service as history_module
from backend.adapters.directus.errors import DirectusTransportError
from backend.adapters.directus.profile import CollectionProfile, RelationProfile
from backend.application.history_service import HistoryError, HistoryService
from backend.contracts.history import ApplyRestoreParams, PreviewRestoreParams, ReadChangeSetsParams


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []
        self.permissions: dict[str, Any] = {
            "orders": {
                "read": {"access": "full", "fields": ["*"]},
                "update": {"access": "full", "fields": ["*"]},
            }
        }
        self.item_update_access = True
        self.accountability = "all"
        self.activity_markers: dict[str, str] = {}

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        if path == "/collections/orders":
            return {
                "data": {
                    "collection": "orders",
                    "meta": {"accountability": self.accountability},
                }
            }
        if path == "/vibetable-bulk-mutation/history-markers":
            requested = kwargs.get("json_body", {}).get("activityIds", [])
            return {
                "data": {
                    activity_id: self.activity_markers[activity_id]
                    for activity_id in requested
                    if activity_id in self.activity_markers
                }
            }
        if path == "/vibetable-bulk-mutation/restore/authorize":
            return {
                "data": {
                    "authorizationToken": "directus-restore-authorization",
                    "expiresAt": "2026-07-22T00:05:00Z",
                }
            }
        if path == "/permissions/me":
            return {"data": self.permissions}
        if path.startswith("/permissions/me/"):
            return {"data": {"update": {"access": self.item_update_access}}}
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
        self.conditional_update_kwargs: list[dict[str, Any]] = []
        self.race_values: dict[str, Any] | None = None

    async def read_item(self, profile: Any, item_id: str, **kwargs: Any) -> dict[str, Any]:
        return dict(self.item)

    async def fields(self, profile: Any) -> list[dict[str, Any]]:
        return list(self.field_metadata)

    async def update_item(
        self, profile: Any, item_id: str, values: dict[str, Any], **kwargs: Any
    ) -> dict[str, Any]:
        self.updates.append(dict(values))
        self.item.update(values)
        return dict(self.item)

    async def update_item_if_unchanged(
        self,
        profile: Any,
        item_id: str,
        values: dict[str, Any],
        *,
        expected_values: dict[str, Any],
        **kwargs: Any,
    ) -> dict[str, Any]:
        self.conditional_update_kwargs.append(dict(kwargs))
        if self.race_values:
            self.item.update(self.race_values)
            self.race_values = None
        if any(self.item.get(field) != value for field, value in expected_values.items()):
            raise DirectusTransportError("conflict", status=409, code="EDIT_CONFLICT")
        return await self.update_item(profile, item_id, values, **kwargs)


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
        proof_secret="test-history-proof-secret",
        clock=clock,
    )
    return service, transport, client


def _revisions() -> dict[str, Any]:
    return {
        # Mirror Directus' ascending timestamp/activity/revision ordering.
        "data": list(
            reversed(
                [
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
            )
        )
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
    revisions_request = next(
        request for request in transport.requests if request["path"] == "/revisions"
    )
    assert revisions_request["query"]["limit"] == 500


@pytest.mark.asyncio
async def test_same_timestamp_activities_remain_contiguous_when_revision_ids_interleave() -> None:
    revisions = {
        "data": [
            {
                "id": "r1",
                "item": "1",
                "data": {"title": "A"},
                "delta": {"title": "A"},
                "activity": {
                    "id": "activity-a",
                    "action": "create",
                    "timestamp": "2026-07-22T10:00:00Z",
                },
            },
            {
                "id": "r3",
                "item": "3",
                "data": {"title": "C"},
                "delta": {"title": "C"},
                "activity": {
                    "id": "activity-a",
                    "action": "create",
                    "timestamp": "2026-07-22T10:00:00Z",
                },
            },
            {
                "id": "r2",
                "item": "2",
                "data": {"title": "B"},
                "delta": {"title": "B"},
                "activity": {
                    "id": "activity-b",
                    "action": "create",
                    "timestamp": "2026-07-22T10:00:00Z",
                },
            },
        ]
    }
    service, _, _ = _service([revisions])

    page = await service.read_change_sets(ReadChangeSetsParams(collection="orders", scope="table"))

    assert page.total == 2
    groups = {group.activity_id: group for group in page.change_sets}
    assert groups["activity-a"].affected_records == 2
    assert groups["activity-b"].affected_records == 1


@pytest.mark.asyncio
async def test_restore_and_soft_archive_are_classified_as_business_actions() -> None:
    revisions = {
        "data": [
            {
                "id": "r1",
                "item": "1",
                "data": {"status": "active", "title": "Before"},
                "delta": {"status": "active", "title": "Before"},
                "activity": {"id": "a1", "action": "create", "timestamp": "2026-07-22T10:00:00Z"},
            },
            {
                "id": "r2",
                "item": "1",
                "data": {"status": "archived", "title": "Before"},
                "delta": {"status": "archived"},
                "activity": {"id": "a2", "action": "update", "timestamp": "2026-07-22T11:00:00Z"},
            },
            {
                "id": "r3",
                "item": "1",
                "data": {"status": "active", "title": "Restored"},
                "delta": {"status": "active", "title": "Restored"},
                "activity": {
                    "id": "a3",
                    "action": "update",
                    "timestamp": "2026-07-22T12:00:00Z",
                },
            },
        ]
    }
    service, transport, _ = _service([revisions])
    transport.activity_markers["a3"] = "restore"

    page = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="table", actions=["restore"])
    )

    assert page.total == 1
    assert page.change_sets[0].action == "restore"
    assert page.change_sets[0].record_changes[0].action == "restore"
    assert any(
        request["path"] == "/vibetable-bulk-mutation/history-markers"
        for request in transport.requests
    )

    service, _, _ = _service([revisions])
    deleted = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="table", actions=["delete"])
    )
    assert deleted.total == 1
    assert deleted.change_sets[0].action == "delete"


@pytest.mark.asyncio
async def test_search_cannot_match_unreadable_history_values() -> None:
    service, _, _ = _service([_revisions()])
    page = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="table", search="LEAK")
    )
    assert page.total == 0


@pytest.mark.asyncio
async def test_query_crops_revision_values_to_current_read_permissions() -> None:
    service, transport, _ = _service([_revisions()])
    transport.permissions["orders"]["read"]["fields"] = ["id", "status", "amount"]

    page = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="table", search="New")
    )

    assert page.total == 0
    assert all(
        change.field != "title" for group in page.change_sets for change in group.scalar_changes
    )


@pytest.mark.asyncio
async def test_permission_summary_missing_explicit_access_fails_closed() -> None:
    service, transport, _ = _service([_revisions()])
    transport.permissions["orders"]["read"] = {"fields": ["*"]}

    with pytest.raises(HistoryError) as captured:
        await service.read_change_sets(ReadChangeSetsParams(collection="orders", scope="table"))

    assert captured.value.code == "history_not_allowed"


@pytest.mark.asyncio
async def test_runtime_revision_capability_fails_closed_when_accountability_is_disabled() -> None:
    service, transport, _ = _service([_revisions()])
    transport.accountability = "activity"

    with pytest.raises(HistoryError) as captured:
        await service.read_change_sets(ReadChangeSetsParams(collection="orders", scope="table"))

    assert captured.value.code == "history_not_allowed"


@pytest.mark.asyncio
async def test_date_filter_normalizes_offsets_before_comparison() -> None:
    service, _, _ = _service([_revisions()])

    page = await service.read_change_sets(
        ReadChangeSetsParams.model_validate(
            {
                "collection": "orders",
                "scope": "table",
                "dateFrom": "2026-07-22T18:30:00+08:00",
            }
        )
    )

    assert page.total == 1
    assert page.change_sets[0].activity_id == "batch-1"


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
            {"data": [_revisions()["data"][-1]]},
        ]
    )
    page = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="archived")
    )
    assert page.total == 1
    assert page.change_sets[0].item_id == "2"
    archive_query = next(
        request["query"] for request in transport.requests if request["path"] == "/items/orders"
    )
    assert archive_query["filter"] == {"status": {"_eq": "archived"}}
    assert set(archive_query["fields"]) <= set(_profile().fields)


@pytest.mark.asyncio
async def test_archived_default_targets_revision_before_archive_transition() -> None:
    revisions = {
        "data": list(
            reversed(
                [
                    {
                        "id": "r3",
                        "item": "2",
                        "data": {"id": "2", "status": "archived", "title": "Edited later"},
                        "delta": {"title": "Edited later"},
                        "activity": {"id": "a3", "timestamp": "2026-07-22T12:00:00Z"},
                    },
                    {
                        "id": "r2",
                        "item": "2",
                        "data": {"id": "2", "status": "archived", "title": "Before delete"},
                        "delta": {"status": "archived"},
                        "activity": {"id": "a2", "timestamp": "2026-07-22T11:00:00Z"},
                    },
                    {
                        "id": "r1",
                        "item": "2",
                        "data": {"id": "2", "status": "active", "title": "Before delete"},
                        "delta": {"status": "active", "title": "Before delete"},
                        "activity": {"id": "a1", "timestamp": "2026-07-22T10:00:00Z"},
                    },
                ]
            )
        )
    }
    service, _, _ = _service([{"data": [{"id": "2", "title": "Edited later"}]}, revisions])

    page = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="archived")
    )

    assert page.archived_default_revision_ids == {"2": "r1"}


@pytest.mark.asyncio
async def test_table_scope_does_not_return_archived_default_mappings() -> None:
    revisions = {
        "data": [
            {
                "id": "r1",
                "item": "2",
                "data": {"id": "2", "status": "active", "title": "Before delete"},
                "delta": {"status": "active", "title": "Before delete"},
                "activity": {"id": "a1", "timestamp": "2026-07-22T10:00:00Z"},
            },
            {
                "id": "r2",
                "item": "2",
                "data": {"id": "2", "status": "archived", "title": "Before delete"},
                "delta": {"status": "archived"},
                "activity": {"id": "a2", "timestamp": "2026-07-22T11:00:00Z"},
            },
        ]
    }
    service, _, _ = _service([revisions])

    page = await service.read_change_sets(ReadChangeSetsParams(collection="orders", scope="table"))

    assert page.archived_default_revision_ids == {}


@pytest.mark.asyncio
async def test_deep_offset_uses_two_pass_bounded_window(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(history_module, "_HISTORY_WINDOW_LIMIT", 2)
    revisions = {
        "data": [
            {
                "id": index + 1,
                "item": "1",
                "data": {"title": f"Title {index}"},
                "delta": {"title": f"Title {index}"},
                "activity": {
                    "id": f"a{index}",
                    "action": "update",
                    "timestamp": f"2026-07-22T1{index}:00:00Z",
                },
            }
            for index in range(3)
        ]
    }
    service, transport, _ = _service([revisions, revisions])

    page = await service.read_change_sets(
        ReadChangeSetsParams(collection="orders", scope="table", offset=2, limit=1)
    )

    assert page.total == 3
    assert [group.activity_id for group in page.change_sets] == ["a0"]
    revision_requests = [
        request for request in transport.requests if request["path"] == "/revisions"
    ]
    assert len(revision_requests) == 2
    assert revision_requests[1]["query"]["filter"] == {
        "_and": [
            {"collection": {"_eq": "orders"}},
            {"id": {"_lte": "3"}},
        ]
    }


@pytest.mark.asyncio
async def test_history_stream_reads_all_bounded_pages_without_a_total_scan_cap(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(history_module, "_HISTORY_PAGE_SIZE", 3)
    service, transport, _ = _service(
        [
            {"data": [{}, {}], "meta": {"filter_count": 3}},
            {"data": [{}], "meta": {"filter_count": 3}},
        ]
    )

    page = await service.read_change_sets(ReadChangeSetsParams(collection="orders", scope="table"))

    assert page.total == 0
    revision_requests = [
        request for request in transport.requests if request["path"] == "/revisions"
    ]
    assert [request["query"]["limit"] for request in revision_requests] == [3, 3]


@pytest.mark.asyncio
async def test_history_relation_labels_are_resolved_without_leaking_unavailable_targets() -> None:
    revisions = {
        "data": list(
            reversed(
                [
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
            )
        )
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
    links_change = next(change for change in preview.relation_changes if change.field == "links")
    assert links_change.target_available is False
    assert links_change.before_item_id is None
    assert links_change.after_item_id is None


@pytest.mark.asyncio
async def test_partial_item_update_permission_disables_restore_confirmation() -> None:
    service, transport, _ = _service(
        [{"data": {"id": "r1", "item": "1", "data": {"title": "Old"}}}]
    )
    transport.permissions["orders"]["update"]["access"] = "partial"
    transport.item_update_access = False

    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )

    assert preview.can_apply is False
    assert preview.restorable_fields == []
    assert [diagnostic.code for diagnostic in preview.diagnostics] == ["field_not_updatable"]


@pytest.mark.asyncio
async def test_restore_preview_fails_closed_without_local_proof_secret() -> None:
    service, transport, _ = _service(
        [{"data": {"id": "r1", "item": "1", "data": {"title": "Old"}}}]
    )
    service._proof_secret = None

    with pytest.raises(HistoryError) as error:
        await service.preview_restore(
            PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
        )

    assert error.value.code == "restore_not_allowed"
    assert all(
        request["path"] != "/vibetable-bulk-mutation/restore/authorize"
        for request in transport.requests
    )


@pytest.mark.asyncio
async def test_m2o_restore_requires_readable_target_and_uses_safe_display() -> None:
    service, _, _ = _service(
        [
            {"data": {"id": "r1", "item": "1", "data": {"project": "p2"}}},
            {"data": {"id": "p1", "title": "Before"}},
            {"data": {"id": "p2", "title": "Apollo"}},
        ],
        item={"id": "1", "status": "active", "project": "p1"},
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )
    assert preview.restorable_fields == ["project"]
    assert preview.relation_changes[0].display_value == "Apollo"
    assert preview.relation_changes[0].before_display_value == "Before"


@pytest.mark.asyncio
async def test_unreadable_current_m2o_target_does_not_block_readable_historical_target() -> None:
    service, _, _ = _service(
        [
            {"data": {"id": "r1", "item": "1", "data": {"project": "p2"}}},
            DirectusTransportError("forbidden", status=403, code="FORBIDDEN"),
            {"data": {"id": "p2", "title": "Apollo"}},
        ],
        item={"id": "1", "status": "active", "project": "p1"},
    )

    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )

    assert preview.can_apply is True
    assert preview.restorable_fields == ["project"]
    change = preview.relation_changes[0]
    assert change.before_display_value is None
    assert change.display_value == "Apollo"
    assert change.target_available is True


@pytest.mark.asyncio
async def test_unreadable_m2o_target_is_safely_skipped() -> None:
    service, _, _ = _service(
        [
            {"data": {"id": "r1", "item": "1", "data": {"project": "p2"}}},
            {"data": {"id": "p1", "title": "Before"}},
            DirectusTransportError("forbidden", status=403, code="FORBIDDEN"),
        ],
        item={"id": "1", "status": "active", "project": "p1"},
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )
    assert preview.can_apply is False
    assert len(preview.relation_changes) == 1
    assert preview.relation_changes[0].target_available is False
    assert preview.diagnostics[0].code == "relation_target_unavailable"


@pytest.mark.asyncio
async def test_cell_restore_ignores_unrelated_row_changes_and_patches_one_field() -> None:
    service, transport, client = _service(
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
    authorization_request = next(
        request
        for request in transport.requests
        if request["path"] == "/vibetable-bulk-mutation/restore/authorize"
    )
    proof_body = authorization_request["json_body"]
    assert proof_body["contract"] == "vibetable-history-preview-proof.v1"
    restore_request = json.loads(base64.urlsafe_b64decode(proof_body["payload"]).decode("utf-8"))
    assert restore_request == {
        "contract": "vibetable-history-restore.v1",
        "collection": "orders",
        "itemId": "1",
        "targetRevision": "r1",
        "scope": "cell",
        "field": "title",
        "schemaRevision": "schema-1",
        "values": {"title": "Old"},
        "expectedValues": {"title": "Now"},
    }
    assert proof_body["subject"] == hashlib.sha256(b"token").hexdigest()
    assert len(proof_body["signature"]) == 64
    assert client.conditional_update_kwargs[0]["authorization_token"] == (
        "directus-restore-authorization"
    )
    assert result.new_revision_id == "r10"
    with pytest.raises(HistoryError) as reused:
        await service.apply_restore(
            ApplyRestoreParams(collection="orders", item_id="1", token=preview.token)
        )
    assert reused.value.code == "restore_token_unknown"


@pytest.mark.asyncio
async def test_restore_detects_selected_field_race_at_conditional_update() -> None:
    service, _, client = _service(
        [
            {"data": {"id": "r1", "item": "1", "data": {"title": "Old"}}},
            {"data": [{"id": "r9"}]},
        ]
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(
            collection="orders", item_id="1", target_revision="r1", scope="cell", field="title"
        )
    )
    client.race_values = {"title": "Raced"}

    with pytest.raises(HistoryError) as error:
        await service.apply_restore(
            ApplyRestoreParams(collection="orders", item_id="1", token=preview.token)
        )

    assert error.value.code == "restore_conflict"
    assert client.updates == []


@pytest.mark.asyncio
async def test_restore_rejects_permission_change_after_preview() -> None:
    service, transport, _ = _service(
        [{"data": {"id": "r1", "item": "1", "data": {"title": "Old"}}}]
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )
    transport.permissions["orders"]["update"]["fields"] = []

    with pytest.raises(HistoryError) as error:
        await service.apply_restore(
            ApplyRestoreParams(collection="orders", item_id="1", token=preview.token)
        )

    assert error.value.code == "schema_drift"


@pytest.mark.asyncio
async def test_restore_rejects_item_permission_change_after_preview() -> None:
    service, transport, _ = _service(
        [{"data": {"id": "r1", "item": "1", "data": {"title": "Old"}}}]
    )
    preview = await service.preview_restore(
        PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
    )
    transport.item_update_access = False

    with pytest.raises(HistoryError) as error:
        await service.apply_restore(
            ApplyRestoreParams(collection="orders", item_id="1", token=preview.token)
        )

    assert error.value.code == "schema_drift"


@pytest.mark.asyncio
async def test_restore_token_store_is_bounded(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(history_module, "_RESTORE_TOKEN_LIMIT", 2)
    revision = {"data": {"id": "r1", "item": "1", "data": {"title": "Old"}}}
    service, _, _ = _service([revision, revision, revision])

    previews = [
        await service.preview_restore(
            PreviewRestoreParams(collection="orders", item_id="1", target_revision="r1")
        )
        for _ in range(3)
    ]

    assert len(service._restore_tokens) == 2
    with pytest.raises(HistoryError) as error:
        await service.apply_restore(
            ApplyRestoreParams(collection="orders", item_id="1", token=previews[0].token)
        )
    assert error.value.code == "restore_token_unknown"


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
