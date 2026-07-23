"""D1 file-tools service tests.

Covers the operation journal lifecycle, Directus Files read/upload/unlink/delete,
and Asset Preset preview approval.
"""

from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.profile import CapabilityManifest, JunctionProfile
from backend.application.file_tools_service import (
    APPROVED_ASSET_PRESETS,
    FileToolsError,
    FileToolsService,
    OperationJournal,
)
from backend.contracts.file_tools import (
    DeleteFileParams,
    JournalIdParams,
    JournalStep,
    ListJournalParams,
    PresetPreviewParams,
    ReadFilesParams,
    ResolveJournalParams,
    UnlinkFileParams,
    UploadFileParams,
)


class FakeDirectusAuth(DirectusAuthBroker):
    def __init__(self) -> None:
        self._user = CurrentUser(id="u1", display_name="T", role_id="r1")

    async def access_token(self) -> str:
        return "tok"


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


def _manifest() -> CapabilityManifest:
    return CapabilityManifest.model_validate(
        {
            "contract": "directus.project.v1",
            "schema_version": "vibetable-1.0",
            "directus_compatibility": ">=12 <13",
            "collections": [
                {
                    "collection": "vibetable_demo",
                    "primary_key": "id",
                    "fields": ["id", "number", "document", "status", "date_updated"],
                    "create_fields": ["number", "document"],
                    "update_fields": ["number", "document"],
                    "archive_field": "status",
                    "archive_value": "archived",
                    "restore_value": "active",
                    "date_updated_field": "date_updated",
                    "relations": [
                        {
                            "field": "document",
                            "kind": "file",
                            "related_collection": "directus_files",
                            "display_fields": ["filename_download", "type"],
                        }
                    ],
                }
            ],
        }
    )


def _service(
    transport: FakeTransport,
    manifest: CapabilityManifest,
    path_for_grant: str,
) -> FileToolsService:
    return FileToolsService(
        client=DirectusClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        transport=transport,
        resolve_path=lambda _g, *, purpose, direction: path_for_grant,
        consume_grant=lambda _g: None,
    )


# ---------------------------------------------------------------------------
# Operation journal
# ---------------------------------------------------------------------------


def test_journal_plan_begin_commit_lifecycle() -> None:
    journal = OperationJournal()
    jid = journal.plan("rename", [JournalStep(kind="rename", source="a", target="b")])
    assert journal.get(jid).state == "planned"
    journal.begin(jid)
    assert journal.get(jid).state == "running"
    journal.commit(jid)
    assert journal.get(jid).state == "committed"
    assert journal.get(jid) not in journal.pending()


def test_journal_pending_lists_uncommitted() -> None:
    journal = OperationJournal()
    jid = journal.plan("replace", [])
    assert journal.get(jid) in journal.pending()
    journal.commit(jid)
    assert journal.get(jid) not in journal.pending()


def test_journal_rollback_restores_backup(tmp_path: Any) -> None:
    backup = tmp_path / "backup.txt"
    backup.write_text("original")
    target = tmp_path / "target.txt"
    target.write_text("modified")
    journal = OperationJournal()
    jid = journal.plan(
        "replace",
        [
            JournalStep(
                kind="replace", source=str(target), target=str(target), backup_path=str(backup)
            )
        ],
    )
    journal.mark_rollback_required(jid, "crash")
    journal.resolve(jid, "rollback")
    assert journal.get(jid).state == "rolled-back"
    assert target.read_text() == "original"


def test_journal_unknown_raises() -> None:
    journal = OperationJournal()
    with pytest.raises(FileToolsError, match="not found") as captured:
        journal.get("bogus")
    assert captured.value.rpc_error_data == {"code": "journal_unknown"}


def test_journal_failed_keep_and_discard_lifecycle() -> None:
    journal = OperationJournal()
    failed = journal.plan("delete", [])
    journal.mark_failed(failed, "permission denied")
    assert journal.get(failed).state == "failed"
    assert journal.get(failed).error == "permission denied"

    kept = journal.plan("replace", [])
    assert journal.resolve(kept, "keep").state == "committed"
    journal.discard(kept)
    with pytest.raises(FileToolsError, match="not found"):
        journal.get(kept)


# ---------------------------------------------------------------------------
# Directus Files
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_read_files_returns_file_metadata() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            {
                "data": {
                    "id": "1",
                    "document": {
                        "id": "f1",
                        "filename_download": "contract.pdf",
                        "type": "application/pdf",
                        "filesize": 1024,
                    },
                }
            }
        ]
    )
    service = _service(transport, manifest, "")
    result = await service.read_files(
        ReadFilesParams(collection="vibetable_demo", item_id="1", relation_field="document")
    )
    assert len(result.files) == 1
    assert result.files[0].filename == "contract.pdf"


@pytest.mark.asyncio
async def test_read_files_filters_malformed_list_entries() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            {
                "data": {
                    "id": "1",
                    "document": [
                        None,
                        {"id": "f1", "filename": "safe.txt", "file_size": 7},
                        "not-a-file",
                    ],
                }
            }
        ]
    )
    result = await _service(transport, manifest, "").read_files(
        ReadFilesParams(collection="vibetable_demo", item_id="1", relation_field="document")
    )
    assert [(file.id, file.filename, file.file_size) for file in result.files] == [
        ("f1", "safe.txt", 7)
    ]


@pytest.mark.asyncio
async def test_upload_file_updates_relation_then_consumes_grant(tmp_path: Any) -> None:
    path = tmp_path / "contract.pdf"
    path.write_bytes(b"safe-content")
    consumed: list[str] = []
    transport = FakeTransport(
        [
            {
                "data": {
                    "id": "f1",
                    "filename_download": "contract.pdf",
                    "type": "application/pdf",
                    "filesize": 12,
                }
            },
            {"data": [{"field": "document", "meta": {"readonly": False}}]},
            {"data": {"id": "1", "document": "f1"}},
        ]
    )
    manifest = _manifest()
    service = FileToolsService(
        client=DirectusClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        transport=transport,
        resolve_path=lambda _g, *, purpose, direction: str(path),
        consume_grant=consumed.append,
    )

    uploaded = await service.upload_file(
        UploadFileParams(
            collection="vibetable_demo",
            item_id="1",
            relation_field="document",
            grant_id="grant-1",
        )
    )

    assert uploaded.id == "f1"
    assert [request["method"] for request in transport.requests] == ["POST", "GET", "PATCH"]
    assert transport.requests[2]["json_body"] == {"document": "f1"}
    assert consumed == ["grant-1"]


@pytest.mark.asyncio
async def test_upload_file_rejects_non_updatable_relation_before_reading_grant() -> None:
    manifest = _manifest()
    raw = manifest.model_dump(mode="json")
    raw["collections"][0]["update_fields"].remove("document")
    restricted = CapabilityManifest.model_validate(raw)
    service = _service(FakeTransport([]), restricted, "")

    with pytest.raises(FileToolsError, match="not updatable") as captured:
        await service.upload_file(
            UploadFileParams(
                collection="vibetable_demo",
                item_id="1",
                relation_field="document",
                grant_id="grant-1",
            )
        )
    assert captured.value.rpc_error_data == {"code": "field_not_updatable"}


@pytest.mark.asyncio
async def test_read_files_rejects_non_file_relation() -> None:
    manifest = _manifest()
    transport = FakeTransport([])
    service = _service(transport, manifest, "")
    with pytest.raises(FileToolsError, match="not a declared file relation"):
        await service.read_files(
            ReadFilesParams(collection="vibetable_demo", item_id="1", relation_field="number")
        )


@pytest.mark.asyncio
async def test_unlink_sets_relation_null() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            {"data": [{"field": "document", "meta": {"readonly": False}}]},
            {"data": {"id": "1", "document": None}},
        ]
    )
    service = _service(transport, manifest, "")
    result = await service.unlink_file(
        UnlinkFileParams(
            collection="vibetable_demo",
            item_id="1",
            relation_field="document",
            file_id="f1",
        )
    )
    assert result["deleted"] is False


@pytest.mark.asyncio
async def test_delete_file_calls_directus_delete() -> None:
    manifest = _manifest()
    transport = FakeTransport([{"data": []}, None])
    service = _service(transport, manifest, "")
    result = await service.delete_file(DeleteFileParams(file_id="f1"))
    assert result == {"deleted": "f1"}
    assert [request["method"] for request in transport.requests] == ["GET", "DELETE"]


@pytest.mark.asyncio
async def test_delete_file_refuses_when_business_item_still_references_file() -> None:
    transport = FakeTransport([{"data": [{"id": "item-1"}]}])
    service = _service(transport, _manifest(), "")
    with pytest.raises(FileToolsError, match="still referenced") as captured:
        await service.delete_file(DeleteFileParams(file_id="f1"))
    assert captured.value.rpc_error_data == {"code": "file_in_use"}
    assert [request["method"] for request in transport.requests] == ["GET"]


@pytest.mark.asyncio
async def test_delete_file_fails_closed_when_reference_query_fails() -> None:
    transport = FakeTransport([RuntimeError("Directus unavailable")])
    service = _service(transport, _manifest(), "")
    with pytest.raises(FileToolsError, match="refusing permanent deletion") as captured:
        await service.delete_file(DeleteFileParams(file_id="f1"))
    assert captured.value.rpc_error_data == {"code": "file_reference_check_failed"}
    assert [request["method"] for request in transport.requests] == ["GET"]


@pytest.mark.asyncio
async def test_delete_file_fails_closed_on_malformed_reference_response() -> None:
    transport = FakeTransport([{"data": {}}])
    service = _service(transport, _manifest(), "")
    with pytest.raises(FileToolsError, match="invalid file reference") as captured:
        await service.delete_file(DeleteFileParams(file_id="f1"))
    assert captured.value.rpc_error_data == {"code": "file_reference_check_failed"}
    assert [request["method"] for request in transport.requests] == ["GET"]


@pytest.mark.asyncio
async def test_delete_file_checks_multi_file_relation_and_ignores_standard_relation() -> None:
    manifest = _manifest()
    profile = manifest.by_collection["vibetable_demo"]
    relation = profile.relations[0]
    profile = profile.model_copy(
        update={
            "relations": [
                relation.model_copy(update={"preset": "standard"}),
                relation.model_copy(
                    update={
                        "kind": "m2m",
                        "preset": "files",
                        "junction": JunctionProfile(
                            collection="vibetable_demo_files",
                            source_field="demo_id",
                            target_field="asset_id",
                        ),
                    }
                ),
            ]
        }
    )
    transport = FakeTransport([{"data": []}, None])
    service = FileToolsService(
        client=DirectusClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        profiles={profile.collection: profile},
        transport=transport,
        resolve_path=lambda _g, *, purpose, direction: "",
        consume_grant=lambda _g: None,
    )
    assert await service.delete_file(DeleteFileParams(file_id="f1")) == {"deleted": "f1"}
    assert transport.requests[0]["query"]["filter"] == {"document": {"asset_id": {"_eq": "f1"}}}


@pytest.mark.asyncio
async def test_delete_file_fails_closed_for_unverified_multi_file_junction() -> None:
    manifest = _manifest()
    profile = manifest.by_collection["vibetable_demo"]
    relation = profile.relations[0].model_copy(update={"preset": "files", "junction": None})
    profile = profile.model_copy(update={"relations": [relation]})
    service = FileToolsService(
        client=DirectusClient(FakeTransport([]), FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        profiles={profile.collection: profile},
        transport=FakeTransport([]),
        resolve_path=lambda _g, *, purpose, direction: "",
        consume_grant=lambda _g: None,
    )
    with pytest.raises(FileToolsError, match="no verified junction") as captured:
        await service.delete_file(DeleteFileParams(file_id="f1"))
    assert captured.value.rpc_error_data == {"code": "file_reference_check_failed"}


@pytest.mark.asyncio
async def test_unlink_rejects_non_updatable_relation() -> None:
    manifest = _manifest()
    raw = manifest.model_dump(mode="json")
    raw["collections"][0]["update_fields"].remove("document")
    service = _service(FakeTransport([]), CapabilityManifest.model_validate(raw), "")
    with pytest.raises(FileToolsError, match="not updatable"):
        await service.unlink_file(
            UnlinkFileParams(
                collection="vibetable_demo",
                item_id="1",
                relation_field="document",
                file_id="f1",
            )
        )


@pytest.mark.asyncio
async def test_preset_preview_rejects_unapproved_key() -> None:
    manifest = _manifest()
    transport = FakeTransport([])
    service = _service(transport, manifest, "")
    with pytest.raises(FileToolsError, match="not in the approved set"):
        await service.preset_preview(
            PresetPreviewParams(file_id="f1", preset_key="evil-custom-size")
        )


@pytest.mark.asyncio
async def test_preset_preview_accepts_approved_key() -> None:
    manifest = _manifest()
    transport = FakeTransport([{"data": {"id": "f1", "type": "image/png"}}])
    service = _service(transport, manifest, "")
    key = next(iter(APPROVED_ASSET_PRESETS))
    result = await service.preset_preview(PresetPreviewParams(file_id="f1", preset_key=key))
    assert result.preset_key == key
    assert f"key={key}" in result.url


def test_service_journal_wrappers_and_unknown_collection() -> None:
    service = _service(FakeTransport([]), _manifest(), "")
    journal_id = service._journal.plan("replace", [])
    assert service.list_journal(ListJournalParams()).pending[0].journal_id == journal_id
    assert (
        service.resolve_journal(ResolveJournalParams(journal_id=journal_id, action="keep")).state
        == "committed"
    )
    assert service.discard_journal(JournalIdParams(journal_id=journal_id)) == {
        "discarded": journal_id
    }
    with pytest.raises(Exception, match="not in capability manifest"):
        service._profile("missing")
