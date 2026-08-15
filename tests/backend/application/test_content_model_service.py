from __future__ import annotations

from typing import Any

import pytest

from backend.application.content_model_service import ContentModelError, ContentModelService
from backend.application.revisioned_metadata_port import (
    JsonObject,
    MetadataConflictError,
    MetadataDelete,
    MetadataQuery,
    MetadataRecord,
    MetadataWrite,
    json_object,
)
from backend.contracts.generated_workbench import (
    ContentProfileCommitRequest,
    RecordDocumentLinkCommitRequest,
    RecordDocumentLinkRepairRequest,
)
from tests.backend.schema_v2_fixtures import field_v2, snapshot_v2


class _Metadata:
    def __init__(self) -> None:
        self.fail_write = False
        self.fail_delete = False
        self.rows: dict[tuple[str, str], MetadataRecord] = {}
        self.mutations: list[tuple[str, str]] = []

    async def read(self, query: MetadataQuery) -> tuple[MetadataRecord, ...]:
        rows = [row for (stored, _), row in self.rows.items() if stored == query.namespace]
        return tuple(row for row in rows if not query.keys or row.logical_id in query.keys)

    async def write(self, command: MetadataWrite) -> MetadataRecord:
        assert command.idempotency_key
        if self.fail_write:
            raise MetadataConflictError()
        current = self.rows.get((command.namespace, command.logical_id))
        if (current.revision if current else None) != command.expected_revision:
            raise ValueError("metadata revision does not match")
        revision = f"revision-{len(self.mutations) + 1}"
        row = MetadataRecord(command.logical_id, revision, command.values)
        self.rows[(command.namespace, command.logical_id)] = row
        self.mutations.append((command.namespace, command.logical_id))
        return row

    async def delete(self, command: MetadataDelete) -> None:
        assert command.idempotency_key
        if self.fail_delete:
            raise MetadataConflictError()
        current = self.rows.get((command.namespace, command.logical_id))
        if current is None or current.revision != command.expected_revision:
            raise ValueError("metadata revision does not match")
        del self.rows[(command.namespace, command.logical_id)]
        self.mutations.append((command.namespace, command.logical_id))


class _ProductData:
    async def describe_table(self, table_id: str) -> JsonObject:
        if table_id != "articles":
            raise ValueError("table not found")
        return json_object(
            snapshot_v2(
                table_id,
                [
                    field_v2("title"),
                    field_v2("body", "editor"),
                    field_v2("summary"),
                    field_v2("secret", "json"),
                ],
                revision="schema-1",
            )
        )

    async def read_rows(self, *, table_id: str, row_ids: list[str]) -> list[JsonObject]:
        if table_id == "articles" and row_ids == ["record-1"]:
            return [{"id": "record-1"}]
        return []


def _profile(**overrides: Any) -> ContentProfileCommitRequest:
    payload = {
        "profile": {
            "contractVersion": "1.0",
            "tableId": "articles",
            "titleFieldId": "fld_title000",
            "bodyFieldId": "fld_body0000",
            "summaryFieldId": "fld_summary0",
            "searchableFieldIds": ["fld_title000", "fld_body0000", "fld_summary0"],
        },
        "expectedRevision": None,
        "idempotencyKey": "profile-create-1",
    }
    payload.update(overrides)
    return ContentProfileCommitRequest.model_validate(payload)


def _link(link_id: str = "link-1", order: int = 0) -> RecordDocumentLinkCommitRequest:
    return RecordDocumentLinkCommitRequest.model_validate(
        {
            "link": {
                "contractVersion": "1.0",
                "linkId": link_id,
                "tableId": "articles",
                "recordId": "record-1",
                "documentId": "22222222-2222-4222-8222-222222222222",
                "role": "reference",
                "order": order,
            },
            "expectedRevision": None,
            "idempotencyKey": f"create-{link_id}",
        }
    )


@pytest.mark.asyncio
async def test_content_profile_is_table_level_revisioned_settings_and_never_copies_content() -> (
    None
):
    metadata = _Metadata()
    service = ContentModelService(metadata_port=metadata, product_data=_ProductData())

    created = await service.commit_profile(_profile())
    loaded = await service.load_profile("articles")
    deleted = await service.delete_profile("articles", created.revision, "profile-delete-1")

    assert loaded == created
    assert created.profile.body_field_id == "fld_body0000"
    assert metadata.mutations == [
        ("content_profiles", "articles"),
        ("content_profiles", "articles"),
    ]
    assert deleted.table_id == "articles"


@pytest.mark.asyncio
async def test_content_model_maps_write_and_delete_races_to_stable_conflict() -> None:
    metadata = _Metadata()
    service = ContentModelService(metadata_port=metadata, product_data=_ProductData())
    created = await service.commit_profile(_profile())

    metadata.fail_write = True
    with pytest.raises(ContentModelError) as write_conflict:
        await service.commit_profile(
            _profile(
                expectedRevision=created.revision,
                idempotencyKey="profile-update-race",
            )
        )
    assert write_conflict.value.code == "content_model.edit_conflict"

    metadata.fail_write = False
    metadata.fail_delete = True
    with pytest.raises(ContentModelError) as delete_conflict:
        await service.delete_profile("articles", created.revision, "profile-delete-race")
    assert delete_conflict.value.code == "content_model.edit_conflict"


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("field", "value", "code"),
    [
        ("bodyFieldId", "fld_title000", "content_profile.body_type_invalid"),
        ("titleFieldId", "missing", "content_profile.field_missing"),
        ("searchableFieldIds", ["fld_secret00"], "content_profile.search_field_invalid"),
    ],
)
async def test_content_profile_rejects_schema_drift_before_persistence(
    field: str,
    value: Any,
    code: str,
) -> None:
    metadata = _Metadata()
    service = ContentModelService(metadata_port=metadata, product_data=_ProductData())
    raw = _profile().model_dump(by_alias=True, mode="json")
    raw["profile"][field] = value

    with pytest.raises(ContentModelError) as raised:
        await service.commit_profile(ContentProfileCommitRequest.model_validate(raw))

    assert raised.value.code == code
    assert metadata.mutations == []


@pytest.mark.asyncio
async def test_record_document_link_create_repair_list_and_delete_only_mutate_link() -> None:
    metadata = _Metadata()
    service = ContentModelService(metadata_port=metadata, product_data=_ProductData())
    second = await service.commit_link(_link("link-2", 20))
    first = await service.commit_link(_link("link-1", 10))

    listed = await service.list_links("articles", "record-1")
    repaired = await service.repair_link(
        RecordDocumentLinkRepairRequest.model_validate(
            {
                "linkId": "link-1",
                "documentId": "33333333-3333-4333-8333-333333333333",
                "expectedRevision": first.revision,
                "idempotencyKey": "repair-link-1",
            }
        )
    )
    deleted = await service.delete_link("link-2", second.revision, "delete-link-2")

    assert [item.link.link_id for item in listed.items] == ["link-1", "link-2"]
    assert repaired.link.document_id == "33333333-3333-4333-8333-333333333333"
    assert repaired.link.record_id == "record-1"
    assert deleted.link_id == "link-2"
    assert all(namespace == "record_document_links" for namespace, _ in metadata.mutations)


@pytest.mark.asyncio
async def test_record_document_link_rejects_missing_record() -> None:
    metadata = _Metadata()
    service = ContentModelService(metadata_port=metadata, product_data=_ProductData())
    raw = _link().model_dump(by_alias=True, mode="json")
    raw["link"]["recordId"] = "missing"

    with pytest.raises(ContentModelError) as raised:
        await service.commit_link(RecordDocumentLinkCommitRequest.model_validate(raw))

    assert raised.value.code == "record_document_link.record_missing"
    assert metadata.mutations == []
