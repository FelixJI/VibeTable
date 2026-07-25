"""Tests for the provider-neutral document workspace metadata boundary."""

from __future__ import annotations

import inspect
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from threading import Barrier

import pytest
from pydantic import ValidationError

from backend.application.document_workspace_service import (
    DocumentWorkspaceError,
    DocumentWorkspaceService,
)
from backend.application.workspace_index import (
    SqliteWorkspaceIndex,
    WorkspaceIndexError,
)
from backend.contracts.document_workspace import (
    LinkDocumentParams,
    PublishHeadAdvance,
    PublishIndexBatchParams,
    ReadDocumentHistoryParams,
    ReadDocumentsParams,
    ReadFolderParams,
    RegisterDocumentParams,
    RevisionIndexEntry,
    UnlinkDocumentParams,
)

ALLOWED_COLLECTIONS = {"vibetable_demo", "vibetable_customers"}


async def _record_exists(_collection: str, _item_id: str) -> bool:
    return True


def _registration(
    *,
    document_id: str = "doc-1",
    item_collection: str | None = None,
    item_id: str | None = None,
) -> RegisterDocumentParams:
    return RegisterDocumentParams(
        workspace_id="workspace-1",
        workspace_name="Workspace",
        document_id=document_id,
        file_name="预算.xlsx",
        mime_type="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        scheme_id="scheme-1",
        revision_id=f"{document_id}-revision-1",
        hash="a" * 64,
        size=123,
        created_at="2026-07-24T08:00:00Z",
        item_collection=item_collection,
        item_id=item_id,
    )


@pytest.fixture
def service(tmp_path: Path) -> DocumentWorkspaceService:
    instance = DocumentWorkspaceService(
        index=SqliteWorkspaceIndex(tmp_path / "state" / "workspace-index.db"),
        allowed_collections=ALLOWED_COLLECTIONS,
        record_exists=_record_exists,
    )
    yield instance
    instance.close()


@pytest.mark.asyncio
async def test_register_list_and_read_folder_use_metadata_only(
    service: DocumentWorkspaceService,
) -> None:
    result = await service.register_document(
        _registration(item_collection="vibetable_demo", item_id="c-001")
    )

    assert result.status == "created"
    assert result.link_id is not None

    global_result = await service.read_documents(ReadDocumentsParams())
    assert global_result.total == 1
    assert global_result.documents[0].document_id == "doc-1"
    assert global_result.documents[0].workspace_id == "workspace-1"
    assert global_result.documents[0].link_id is None

    folder = await service.read_folder(
        ReadFolderParams(collection="vibetable_demo", item_id="c-001")
    )
    assert [entry.document_id for entry in folder.documents] == ["doc-1"]
    assert folder.documents[0].link_id == result.link_id

    wire = _registration().model_dump(by_alias=True)
    assert all("path" not in key.casefold() for key in wire)
    assert all("content" not in key.casefold() for key in wire)


@pytest.mark.asyncio
async def test_register_document_is_durable_across_index_reopen(tmp_path: Path) -> None:
    db_path = tmp_path / "state" / "workspace-index.db"
    first = DocumentWorkspaceService(
        index=SqliteWorkspaceIndex(db_path),
        allowed_collections=ALLOWED_COLLECTIONS,
    )
    await first.register_document(_registration())
    first.close()

    second = DocumentWorkspaceService(
        index=SqliteWorkspaceIndex(db_path),
        allowed_collections=ALLOWED_COLLECTIONS,
    )
    try:
        documents = await second.read_documents(ReadDocumentsParams())
        history = await second.read_document_history(ReadDocumentHistoryParams(document_id="doc-1"))
        assert [entry.document_id for entry in documents.documents] == ["doc-1"]
        assert [entry.revision_id for entry in history.revisions] == ["doc-1-revision-1"]
    finally:
        second.close()


@pytest.mark.asyncio
async def test_record_scope_is_dynamic_but_allow_listed(
    service: DocumentWorkspaceService,
) -> None:
    with pytest.raises(DocumentWorkspaceError, match="supplied together"):
        await service.register_document(_registration(item_collection="vibetable_demo"))

    for collection in ("system_users", "undeclared_records"):
        with pytest.raises(DocumentWorkspaceError, match="not in capability manifest"):
            await service.register_document(
                _registration(item_collection=collection, item_id="item-1")
            )
        with pytest.raises(DocumentWorkspaceError, match="not in capability manifest"):
            await service.read_folder(ReadFolderParams(collection=collection, item_id="item-1"))

    await service.register_document(_registration())
    linked = await service.link_document(
        LinkDocumentParams(
            document_id="doc-1",
            item_collection="vibetable_customers",
            item_id="cu-9",
        )
    )
    assert linked.status == "created"


@pytest.mark.asyncio
async def test_collection_policy_requires_a_live_catalog_entry(
    tmp_path: Path,
) -> None:
    catalog: dict[str, object] = {}
    instance = DocumentWorkspaceService(
        index=SqliteWorkspaceIndex(tmp_path / "state" / "workspace-index.db"),
        collection_catalog=catalog,
        record_exists=_record_exists,
    )
    try:
        await instance.register_document(_registration())
        with pytest.raises(DocumentWorkspaceError) as error:
            await instance.link_document(
                LinkDocumentParams(
                    document_id="doc-1",
                    item_collection="new_orders_2026",
                    item_id="order-1",
                )
            )
        assert error.value.code == "link_collection_not_allowed"

        catalog["new_orders_2026"] = object()
        linked = await instance.link_document(
            LinkDocumentParams(
                document_id="doc-1",
                item_collection="new_orders_2026",
                item_id="order-1",
            )
        )
        assert linked.status == "created"
    finally:
        instance.close()


@pytest.mark.asyncio
async def test_record_link_operations_require_a_live_target_record(tmp_path: Path) -> None:
    live_records = {
        ("vibetable_demo", "register-live"),
        ("vibetable_demo", "link-live"),
        ("vibetable_demo", "folder-live"),
    }

    async def record_exists(collection: str, item_id: str) -> bool:
        return (collection, item_id) in live_records

    instance = DocumentWorkspaceService(
        index=SqliteWorkspaceIndex(tmp_path / "state" / "workspace-index.db"),
        collection_catalog={"vibetable_demo": object()},
        record_exists=record_exists,
    )
    try:
        await instance.register_document(
            _registration(item_collection="vibetable_demo", item_id="register-live")
        )
        await instance.link_document(
            LinkDocumentParams(
                document_id="doc-1",
                item_collection="vibetable_demo",
                item_id="link-live",
            )
        )
        await instance.read_folder(
            ReadFolderParams(collection="vibetable_demo", item_id="folder-live")
        )

        operations = (
            lambda: instance.register_document(
                _registration(
                    document_id="missing-register",
                    item_collection="vibetable_demo",
                    item_id="missing",
                )
            ),
            lambda: instance.link_document(
                LinkDocumentParams(
                    document_id="doc-1",
                    item_collection="vibetable_demo",
                    item_id="missing",
                )
            ),
            lambda: instance.read_folder(
                ReadFolderParams(collection="vibetable_demo", item_id="missing")
            ),
        )
        for operation in operations:
            with pytest.raises(DocumentWorkspaceError) as error:
                await operation()
            assert error.value.code == "link_record_not_found"

        live_records.remove(("vibetable_demo", "link-live"))
        with pytest.raises(DocumentWorkspaceError) as deleted_error:
            await instance.read_folder(
                ReadFolderParams(collection="vibetable_demo", item_id="link-live")
            )
        assert deleted_error.value.code == "link_record_not_found"
    finally:
        instance.close()


@pytest.mark.asyncio
async def test_record_link_operations_fail_closed_without_record_authority(
    tmp_path: Path,
) -> None:
    instance = DocumentWorkspaceService(
        index=SqliteWorkspaceIndex(tmp_path / "state" / "workspace-index.db"),
        collection_catalog={"vibetable_demo": object()},
    )
    try:
        with pytest.raises(DocumentWorkspaceError) as error:
            await instance.read_folder(
                ReadFolderParams(collection="vibetable_demo", item_id="item-1")
            )
        assert error.value.code == "link_record_authority_unavailable"
    finally:
        instance.close()


@pytest.mark.asyncio
async def test_publish_is_idempotent_and_preserves_immutable_conflicts(
    service: DocumentWorkspaceService,
) -> None:
    await service.register_document(_registration())
    params = PublishIndexBatchParams(
        revisions=[
            RevisionIndexEntry(
                revision_id="rev-2",
                document_id="doc-1",
                scheme_id="scheme-1",
                parent_revision_id="doc-1-revision-1",
                sequence=2,
                version_label="main/V2",
                hash="b" * 64,
                size=200,
                mime_type="application/octet-stream",
                created_at="2026-07-24T09:00:00Z",
                created_by="local-user",
            )
        ],
        head_advance=PublishHeadAdvance(
            document_id="doc-1",
            scheme_id="scheme-1",
            expected_head_revision_id="doc-1-revision-1",
            new_head_revision_id="rev-2",
        ),
        idempotency_key="idem-1",
    )

    first = await service.publish_index_batch(params)
    replay = await service.publish_index_batch(params)
    assert [(entry.revision_id, entry.status) for entry in first.results] == [("rev-2", "created")]
    assert replay == first

    conflicting = params.model_copy(
        update={"revisions": [params.revisions[0].model_copy(update={"hash": "c" * 64})]}
    )
    with pytest.raises(DocumentWorkspaceError) as error:
        await service.publish_index_batch(conflicting)
    assert error.value.code == "workspace.idempotency_conflict"

    immutable_conflict = conflicting.model_copy(
        update={"head_advance": None, "idempotency_key": "idem-2"}
    )
    result = await service.publish_index_batch(immutable_conflict)
    assert result.conflicts == ["rev-2"]
    assert result.results[0].status == "conflict"

    history = await service.read_document_history(ReadDocumentHistoryParams(document_id="doc-1"))
    assert [entry.revision_id for entry in history.revisions] == [
        "rev-2",
        "doc-1-revision-1",
    ]
    assert history.revisions[0].hash == "b" * 64
    assert history.revisions[0].created_at == "2026-07-24T09:00:00Z"


@pytest.mark.asyncio
async def test_metadata_only_publish_never_advances_head_and_head_cas_requires_ancestry(
    service: DocumentWorkspaceService,
) -> None:
    await service.register_document(_registration())
    initial_head = "doc-1-revision-1"
    orphan = RevisionIndexEntry(
        revision_id="orphan-revision",
        document_id="doc-1",
        scheme_id="scheme-1",
        parent_revision_id=initial_head,
        sequence=2,
        hash="d" * 64,
        created_at="2026-07-24T09:05:00Z",
    )

    await service.publish_index_batch(
        PublishIndexBatchParams(
            revisions=[orphan],
            head_advance=None,
            idempotency_key="orphan-metadata",
        )
    )
    documents = await service.read_documents(ReadDocumentsParams())
    assert documents.documents[0].main_head == initial_head

    next_head = orphan.model_copy(
        update={
            "revision_id": "main-revision-2",
            "created_at": "2026-07-24T09:10:00Z",
        }
    )
    await service.publish_index_batch(
        PublishIndexBatchParams(
            revisions=[next_head],
            head_advance=PublishHeadAdvance(
                document_id="doc-1",
                scheme_id="scheme-1",
                expected_head_revision_id=initial_head,
                new_head_revision_id=next_head.revision_id,
            ),
            idempotency_key="advance-main",
        )
    )
    documents = await service.read_documents(ReadDocumentsParams())
    assert documents.documents[0].main_head == next_head.revision_id

    fork = orphan.model_copy(
        update={
            "revision_id": "fork-revision",
            "created_at": "2026-07-24T09:15:00Z",
        }
    )
    with pytest.raises(DocumentWorkspaceError) as error:
        await service.publish_index_batch(
            PublishIndexBatchParams(
                revisions=[fork],
                head_advance=PublishHeadAdvance(
                    document_id="doc-1",
                    scheme_id="scheme-1",
                    expected_head_revision_id=next_head.revision_id,
                    new_head_revision_id=fork.revision_id,
                ),
                idempotency_key="reject-fork",
            )
        )
    assert error.value.code == "workspace.head_not_descendant"

    history = await service.read_document_history(ReadDocumentHistoryParams(document_id="doc-1"))
    assert "fork-revision" not in {revision.revision_id for revision in history.revisions}


@pytest.mark.asyncio
async def test_revision_id_cannot_be_reused_across_documents(
    service: DocumentWorkspaceService,
) -> None:
    await service.register_document(_registration(document_id="doc-a"))
    with pytest.raises(DocumentWorkspaceError) as error:
        await service.register_document(
            RegisterDocumentParams(
                **{
                    **_registration(document_id="doc-b").model_dump(),
                    "revision_id": "doc-a-revision-1",
                }
            )
        )
    assert error.value.code == "revision_immutable_conflict"

    documents = await service.read_documents(ReadDocumentsParams())
    assert [document.document_id for document in documents.documents] == ["doc-a"]


@pytest.mark.asyncio
async def test_unlink_only_retires_link_not_document(
    service: DocumentWorkspaceService,
) -> None:
    registration = await service.register_document(
        _registration(item_collection="vibetable_demo", item_id="c-001")
    )
    assert registration.link_id is not None

    result = await service.unlink_document(UnlinkDocumentParams(link_id=registration.link_id))
    assert result == {"deleted": registration.link_id}
    folder = await service.read_folder(
        ReadFolderParams(collection="vibetable_demo", item_id="c-001")
    )
    documents = await service.read_documents(ReadDocumentsParams())
    assert folder.documents == []
    assert documents.total == 1


def test_contract_rejects_paths_and_binary_payload_fields() -> None:
    payload = _registration().model_dump(by_alias=True)
    with pytest.raises(ValidationError):
        RegisterDocumentParams.model_validate({**payload, "localPath": "C:/private/report.docx"})
    with pytest.raises(ValidationError):
        RegisterDocumentParams.model_validate({**payload, "contentBase64": "AA=="})


def test_publish_contract_carries_timestamp_and_explicit_head_advance() -> None:
    params = PublishIndexBatchParams(
        revisions=[
            RevisionIndexEntry(
                revision_id="rev-2",
                document_id="doc-1",
                scheme_id="scheme-1",
                parent_revision_id="doc-1-revision-1",
                sequence=2,
                hash="b" * 64,
                created_at="2026-07-24T09:30:00Z",
            )
        ],
        head_advance=PublishHeadAdvance(
            document_id="doc-1",
            scheme_id="scheme-1",
            expected_head_revision_id="doc-1-revision-1",
            new_head_revision_id="rev-2",
        ),
        idempotency_key="idem-explicit-head",
    )

    wire = params.model_dump(by_alias=True)
    assert wire["revisions"][0]["createdAt"] == "2026-07-24T09:30:00Z"
    assert wire["headAdvance"] == {
        "documentId": "doc-1",
        "schemeId": "scheme-1",
        "expectedHeadRevisionId": "doc-1-revision-1",
        "newHeadRevisionId": "rev-2",
    }


@pytest.mark.parametrize(
    ("model", "payload"),
    [
        (
            RegisterDocumentParams,
            {**_registration().model_dump(), "created_at": "2026-07-24 09:30:00Z"},
        ),
        (
            RevisionIndexEntry,
            {
                "revision_id": "rev-invalid-time",
                "document_id": "doc-1",
                "scheme_id": "scheme-1",
                "sequence": 2,
                "hash": "b" * 64,
                "created_at": "2026-07-24T09:30:00+08:00",
            },
        ),
    ],
)
def test_workspace_created_at_requires_rfc3339_utc(
    model: type[RegisterDocumentParams] | type[RevisionIndexEntry],
    payload: dict[str, object],
) -> None:
    with pytest.raises(ValidationError):
        model.model_validate(payload)


def test_workspace_created_at_is_canonicalized_to_utc_z() -> None:
    registration = RegisterDocumentParams.model_validate(
        {
            **_registration().model_dump(),
            "created_at": "2026-07-24T09:30:00.120000+00:00",
        }
    )
    revision = RevisionIndexEntry(
        revision_id="rev-canonical-time",
        document_id="doc-1",
        scheme_id="scheme-1",
        sequence=2,
        hash="b" * 64,
        created_at="2026-07-24T09:30:00+00:00",
    )

    assert registration.created_at == "2026-07-24T09:30:00.12Z"
    assert revision.created_at == "2026-07-24T09:30:00Z"


def test_workspace_index_refuses_pb_data_and_leaves_it_untouched(
    tmp_path: Path,
) -> None:
    pb_data = tmp_path / "pb_data"
    pb_data.mkdir()
    sentinel = pb_data / "do-not-touch.bin"
    sentinel.write_bytes(b"owned-by-data-backend")

    with pytest.raises(ValueError, match="must not be stored"):
        SqliteWorkspaceIndex(pb_data / "workspace-index.db")

    assert sentinel.read_bytes() == b"owned-by-data-backend"
    assert sorted(path.name for path in pb_data.iterdir()) == ["do-not-touch.bin"]


def test_workspace_index_rejects_unknown_schema_version_before_writing(
    tmp_path: Path,
) -> None:
    import sqlite3

    db_path = tmp_path / "state" / "workspace-index.db"
    db_path.parent.mkdir(parents=True)
    connection = sqlite3.connect(db_path)
    connection.executescript(
        """
        CREATE TABLE workspace_schema_meta (
            key TEXT PRIMARY KEY,
            value TEXT NOT NULL
        );
        INSERT INTO workspace_schema_meta(key, value)
        VALUES ('schema_version', '999');
        CREATE TABLE sentinel(value TEXT NOT NULL);
        INSERT INTO sentinel(value) VALUES ('untouched');
        """
    )
    connection.commit()
    connection.close()

    with pytest.raises(WorkspaceIndexError) as error:
        SqliteWorkspaceIndex(db_path)
    assert getattr(error.value, "code", None) == "workspace.schema_version_unsupported"

    connection = sqlite3.connect(db_path)
    try:
        assert connection.execute("SELECT value FROM sentinel").fetchone() == ("untouched",)
        assert (
            connection.execute(
                """
                SELECT COUNT(*) FROM sqlite_master
                WHERE type = 'table' AND name = 'workspace_documents'
                """
            ).fetchone()[0]
            == 0
        )
    finally:
        connection.close()


def test_workspace_index_rejects_partial_unversioned_schema_without_stamping_it(
    tmp_path: Path,
) -> None:
    import sqlite3

    db_path = tmp_path / "state" / "workspace-index.db"
    db_path.parent.mkdir(parents=True)
    connection = sqlite3.connect(db_path)
    connection.execute("CREATE TABLE workspace_documents(document_id TEXT PRIMARY KEY)")
    connection.commit()
    connection.close()

    with pytest.raises(WorkspaceIndexError) as error:
        SqliteWorkspaceIndex(db_path)
    assert getattr(error.value, "code", None) == "workspace.schema_incomplete"

    connection = sqlite3.connect(db_path)
    try:
        assert (
            connection.execute(
                """
                SELECT COUNT(*) FROM sqlite_master
                WHERE type = 'table' AND name = 'workspace_schema_meta'
                """
            ).fetchone()[0]
            == 0
        )
    finally:
        connection.close()


def test_workspace_index_rejects_versioned_schema_with_missing_columns(
    tmp_path: Path,
) -> None:
    import sqlite3

    db_path = tmp_path / "state" / "workspace-index.db"
    db_path.parent.mkdir(parents=True)
    connection = sqlite3.connect(db_path)
    connection.executescript(
        """
        CREATE TABLE workspace_schema_meta (
            key TEXT PRIMARY KEY,
            value TEXT NOT NULL
        );
        INSERT INTO workspace_schema_meta(key, value)
        VALUES ('schema_version', '1');
        CREATE TABLE workspace_documents(document_id TEXT PRIMARY KEY);
        """
    )
    connection.commit()
    connection.close()

    with pytest.raises(WorkspaceIndexError) as error:
        SqliteWorkspaceIndex(db_path)
    assert getattr(error.value, "code", None) == "workspace.schema_invalid"

    connection = sqlite3.connect(db_path)
    try:
        assert (
            connection.execute(
                """
                SELECT COUNT(*) FROM sqlite_master
                WHERE type = 'table' AND name = 'workspace_revisions'
                """
            ).fetchone()[0]
            == 0
        )
    finally:
        connection.close()


def test_workspace_index_rejects_same_named_index_with_wrong_columns(
    tmp_path: Path,
) -> None:
    import sqlite3

    db_path = tmp_path / "state" / "workspace-index.db"
    SqliteWorkspaceIndex(db_path).close()
    connection = sqlite3.connect(db_path)
    connection.executescript(
        """
        DROP INDEX ix_workspace_revisions_document_sequence;
        CREATE INDEX ix_workspace_revisions_document_sequence
            ON workspace_revisions(sequence, document_id);
        """
    )
    connection.commit()
    connection.close()

    with pytest.raises(WorkspaceIndexError) as error:
        SqliteWorkspaceIndex(db_path)
    assert error.value.code == "workspace.schema_invalid"


def test_workspace_index_rejects_missing_link_uniqueness_constraint(
    tmp_path: Path,
) -> None:
    import sqlite3

    db_path = tmp_path / "state" / "workspace-index.db"
    SqliteWorkspaceIndex(db_path).close()
    connection = sqlite3.connect(db_path)
    connection.executescript(
        """
        DROP INDEX ix_workspace_links_item;
        ALTER TABLE workspace_links RENAME TO workspace_links_old;
        CREATE TABLE workspace_links (
            link_id TEXT PRIMARY KEY,
            document_id TEXT NOT NULL,
            item_collection TEXT NOT NULL,
            item_id TEXT NOT NULL,
            link_type TEXT NOT NULL,
            status TEXT NOT NULL
        );
        CREATE INDEX ix_workspace_links_item
            ON workspace_links(item_collection, item_id, status);
        DROP TABLE workspace_links_old;
        """
    )
    connection.commit()
    connection.close()

    with pytest.raises(WorkspaceIndexError) as error:
        SqliteWorkspaceIndex(db_path)
    assert error.value.code == "workspace.schema_invalid"


def test_workspace_index_rejects_wrong_created_at_default(tmp_path: Path) -> None:
    import sqlite3

    db_path = tmp_path / "state" / "workspace-index.db"
    SqliteWorkspaceIndex(db_path).close()
    connection = sqlite3.connect(db_path)
    connection.executescript(
        """
        DROP INDEX ix_workspace_revisions_document_sequence;
        ALTER TABLE workspace_revisions RENAME TO workspace_revisions_old;
        CREATE TABLE workspace_revisions (
            revision_id TEXT PRIMARY KEY,
            document_id TEXT NOT NULL,
            scheme_id TEXT NOT NULL,
            parent_revision_id TEXT,
            sequence INTEGER NOT NULL,
            version_label TEXT NOT NULL,
            kind TEXT NOT NULL,
            hash TEXT NOT NULL,
            size INTEGER NOT NULL,
            mime_type TEXT NOT NULL,
            created_by TEXT,
            device_id TEXT,
            comment TEXT,
            created_at TEXT NOT NULL DEFAULT 'wrong'
        );
        CREATE INDEX ix_workspace_revisions_document_sequence
            ON workspace_revisions(document_id, sequence DESC);
        DROP TABLE workspace_revisions_old;
        """
    )
    connection.commit()
    connection.close()

    with pytest.raises(WorkspaceIndexError) as error:
        SqliteWorkspaceIndex(db_path)
    assert error.value.code == "workspace.schema_invalid"


def test_workspace_head_advance_is_an_atomic_sql_compare_and_swap(tmp_path: Path) -> None:
    db_path = tmp_path / "state" / "workspace-index.db"
    first = SqliteWorkspaceIndex(db_path)
    second = SqliteWorkspaceIndex(db_path)
    try:
        first.register_document(_registration())
        root = "doc-1-revision-1"

        def branch(prefix: str) -> list[RevisionIndexEntry]:
            revisions: list[RevisionIndexEntry] = []
            parent = root
            for sequence in range(2, 101):
                revision = RevisionIndexEntry(
                    revision_id=f"{prefix}-{sequence}",
                    document_id="doc-1",
                    scheme_id="scheme-1",
                    parent_revision_id=parent,
                    sequence=sequence,
                    hash=(prefix * 64)[:64],
                    created_at=f"2026-07-24T09:{sequence % 60:02d}:00Z",
                )
                revisions.append(revision)
                parent = revision.revision_id
            return revisions

        left = branch("b")
        right = branch("c")
        first.publish_revisions(
            PublishIndexBatchParams(
                revisions=left,
                idempotency_key="metadata-left",
            )
        )
        first.publish_revisions(
            PublishIndexBatchParams(
                revisions=right,
                idempotency_key="metadata-right",
            )
        )

        start = Barrier(2)

        def advance(
            index: SqliteWorkspaceIndex,
            revision: RevisionIndexEntry,
            idempotency_key: str,
        ) -> str:
            start.wait()
            try:
                index.publish_revisions(
                    PublishIndexBatchParams(
                        revisions=[revision],
                        head_advance=PublishHeadAdvance(
                            document_id="doc-1",
                            scheme_id="scheme-1",
                            expected_head_revision_id=root,
                            new_head_revision_id=revision.revision_id,
                        ),
                        idempotency_key=idempotency_key,
                    )
                )
            except WorkspaceIndexError as exc:
                return exc.code
            return "ok"

        with ThreadPoolExecutor(max_workers=2) as executor:
            left_result = executor.submit(advance, first, left[-1], "advance-left")
            right_result = executor.submit(advance, second, right[-1], "advance-right")
            outcomes = sorted((left_result.result(), right_result.result()))

        assert outcomes == ["ok", "workspace.head_conflict"]
    finally:
        first.close()
        second.close()


def test_production_workspace_service_has_no_remote_provider_dependency() -> None:
    import backend.application.document_workspace_service as service_module
    import backend.application.workspace_index as index_module

    source = (inspect.getsource(service_module) + inspect.getsource(index_module)).casefold()
    assert "backend.adapters." + "dire" "ctus" not in source
    assert "access_token" not in source
    assert "extension_base_url" not in source
    assert "/items/" not in source
    assert "http://" not in source
    assert "https://" not in source


class MemoryWorkspaceIndex:
    """Minimal registration-only port for composition tests."""

    def close(self) -> None:
        return None
