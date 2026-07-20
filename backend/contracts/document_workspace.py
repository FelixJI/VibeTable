"""G3 document workspace contracts: workspace metadata RPC.

The BFF handles metadata only — it never accepts local absolute paths or
binary payloads. The C# publisher reads from ``.backup/outbox/{eventId}.json``
and posts to the vibetable-workspace-index extension; the BFF writes a receipt
and deletes the outbox event on success.

RPCs:
* ``workspace.readFolder`` — list folders/documents in a workspace folder
* ``workspace.publishIndexBatch`` — publish revision metadata batch
* ``workspace.linkDocument`` — link a document to a business item
* ``workspace.unlinkDocument`` — remove a link
* ``workspace.readDocumentHistory`` — read document version history
"""

from __future__ import annotations

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class WorkspaceModel(BaseModel):
    model_config = _camel_config()


# ---------------------------------------------------------------------------
# readFolder
# ---------------------------------------------------------------------------


class ReadFolderParams(WorkspaceModel):
    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)


class DocumentSummary(WorkspaceModel):
    link_id: str | None = Field(default=None, max_length=128)
    document_id: str = Field(min_length=1, max_length=128)
    workspace_id: str = Field(min_length=1, max_length=128)
    file_name: str = Field(min_length=1, max_length=255)
    mime_type: str | None = Field(default=None, max_length=128)
    main_head: str | None = Field(default=None, max_length=128)
    main_hash: str | None = Field(default=None, max_length=64)
    status: str = Field(default="active", max_length=32)
    link_type: str | None = Field(default=None, max_length=64)
    folder_relative_path: str | None = Field(default=None, max_length=512)
    is_missing: bool = False


class FolderResult(WorkspaceModel):
    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    folder_id: str | None = Field(default=None, max_length=128)
    documents: list[DocumentSummary] = Field(default_factory=list)


class ReadDocumentsParams(WorkspaceModel):
    limit: int = Field(default=200, ge=1, le=500)
    offset: int = Field(default=0, ge=0)


class DocumentListResult(WorkspaceModel):
    documents: list[DocumentSummary] = Field(default_factory=list)
    total: int = Field(default=0, ge=0)


# ---------------------------------------------------------------------------
# publishIndexBatch
# ---------------------------------------------------------------------------


class RevisionIndexEntry(WorkspaceModel):
    revision_id: str = Field(min_length=1, max_length=128)
    document_id: str = Field(min_length=1, max_length=128)
    scheme_id: str = Field(min_length=1, max_length=128)
    parent_revision_id: str | None = Field(default=None, max_length=128)
    sequence: int = Field(ge=1)
    version_label: str = Field(default="", max_length=128)
    kind: str = Field(default="formal", max_length=32)
    hash: str = Field(min_length=8, max_length=64)
    size: int = Field(default=0, ge=0)
    mime_type: str = Field(default="", max_length=128)
    created_by: str | None = Field(default=None, max_length=128)
    device_id: str | None = Field(default=None, max_length=128)
    comment: str | None = Field(default=None)


class PublishIndexBatchParams(WorkspaceModel):
    revisions: list[RevisionIndexEntry] = Field(min_length=1, max_length=100)
    idempotency_key: str = Field(min_length=1, max_length=128)


class PublishResult(WorkspaceModel):
    revision_id: str = Field(min_length=1, max_length=128)
    status: str = Field(min_length=1, max_length=32)


class PublishIndexBatchResult(WorkspaceModel):
    results: list[PublishResult] = Field(default_factory=list)
    conflicts: list[str] = Field(default_factory=list)


# ---------------------------------------------------------------------------
# linkDocument / unlinkDocument
# ---------------------------------------------------------------------------


class LinkDocumentParams(WorkspaceModel):
    document_id: str = Field(min_length=1, max_length=128)
    item_collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    link_type: str = Field(default="primary", max_length=64)


class LinkResult(WorkspaceModel):
    link_id: str = Field(min_length=1, max_length=128)
    status: str = Field(min_length=1, max_length=32)


class UnlinkDocumentParams(WorkspaceModel):
    link_id: str = Field(min_length=1, max_length=128)


# ---------------------------------------------------------------------------
# readDocumentHistory
# ---------------------------------------------------------------------------


class ReadDocumentHistoryParams(WorkspaceModel):
    document_id: str = Field(min_length=1, max_length=128)
    limit: int = Field(default=50, ge=1, le=100)
    offset: int = Field(default=0, ge=0)


class DocumentRevisionEntry(WorkspaceModel):
    revision_id: str = Field(min_length=1, max_length=128)
    scheme_name: str | None = Field(default=None, max_length=128)
    sequence: int = Field(ge=1)
    version_label: str = Field(default="", max_length=128)
    kind: str = Field(default="formal", max_length=32)
    hash: str = Field(default="", max_length=64)
    size: int = Field(default=0, ge=0)
    created_at: str = Field(default="", max_length=64)
    created_by: str | None = Field(default=None, max_length=128)


class DocumentHistoryResult(WorkspaceModel):
    document_id: str = Field(min_length=1, max_length=128)
    revisions: list[DocumentRevisionEntry] = Field(default_factory=list)
    total: int = Field(default=0, ge=0)
