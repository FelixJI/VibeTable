"""D1 file-tools service tests.

Covers the operation journal lifecycle, Directus Files read/upload/unlink/delete,
and Asset Preset preview approval.
"""

from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.profile import CapabilityManifest
from backend.application.file_tools_service import (
    APPROVED_ASSET_PRESETS,
    FileToolsError,
    FileToolsService,
    OperationJournal,
)
from backend.contracts.file_tools import (
    DeleteFileParams,
    JournalStep,
    PresetPreviewParams,
    ReadFilesParams,
    UnlinkFileParams,
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
    with pytest.raises(FileToolsError, match="not found"):
        journal.get("bogus")


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
    transport = FakeTransport([{"data": {"id": "1", "document": None}}])
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
    transport = FakeTransport([None])
    service = _service(transport, manifest, "")
    result = await service.delete_file(DeleteFileParams(file_id="f1"))
    assert result == {"deleted": "f1"}
    assert transport.requests[0]["method"] == "DELETE"


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
