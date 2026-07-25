"""Provider-neutral application service for workspace document metadata.

Document bytes, immutable revisions, restore operations, and the registration
outbox remain owned by the native workspace module. This service handles only
portable metadata and delegates persistence to :class:`WorkspaceIndexPort`.
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable, Collection, Mapping
from pathlib import Path
from typing import Any, TypeVar

from backend.application.workspace_index import (
    SqliteWorkspaceIndex,
    WorkspaceIndexError,
    WorkspaceIndexPort,
)
from backend.contracts.document_workspace import (
    DocumentHistoryResult,
    DocumentListResult,
    FolderResult,
    LinkDocumentParams,
    LinkResult,
    PublishIndexBatchParams,
    PublishIndexBatchResult,
    ReadDocumentHistoryParams,
    ReadDocumentsParams,
    ReadFolderParams,
    RegisterDocumentParams,
    RegisterDocumentResult,
    UnlinkDocumentParams,
)

T = TypeVar("T")


class DocumentWorkspaceError(Exception):
    """A document-workspace error carrying a stable product error code."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


class DocumentWorkspaceService:
    """Validate product scope and orchestrate a workspace metadata index."""

    def __init__(
        self,
        *,
        index: WorkspaceIndexPort | None = None,
        index_path: Path | None = None,
        allowed_collections: Collection[str] | None = None,
        collection_catalog: Mapping[str, Any] | None = None,
        collection_catalog_loader: (
            Callable[[], Awaitable[Mapping[str, Any]]] | None
        ) = None,
        record_exists: Callable[[str, str], Awaitable[bool]] | None = None,
        **composition_compatibility: Any,
    ) -> None:
        # Older composition roots pass a mapping named ``profiles``. Reading
        # only its keys preserves the product capability allow-list while the
        # remaining obsolete provider objects are deliberately ignored.
        profile_mapping = composition_compatibility.get("profiles")
        if collection_catalog is None and isinstance(profile_mapping, Mapping):
            collection_catalog = profile_mapping
        if collection_catalog is not None:
            # Keep the live mapping: runtime table creation mutates the shared
            # catalog and must become visible without reopening the broker.
            self._collection_catalog = collection_catalog
        elif allowed_collections is not None:
            self._collection_catalog = {str(collection): True for collection in allowed_collections}
        else:
            # No authoritative catalog means no record-link capability.
            self._collection_catalog = {}
        self._collection_catalog_loader = collection_catalog_loader
        self._record_exists = record_exists
        self._index = index or SqliteWorkspaceIndex(index_path)

    async def read_folder(self, params: ReadFolderParams) -> FolderResult:
        await self._require_record(params.collection, params.item_id)
        return self._translate(lambda: self._index.read_folder(params.collection, params.item_id))

    async def read_documents(self, params: ReadDocumentsParams) -> DocumentListResult:
        return self._translate(
            lambda: self._index.read_documents(limit=params.limit, offset=params.offset)
        )

    async def register_document(self, params: RegisterDocumentParams) -> RegisterDocumentResult:
        if (params.item_collection is None) != (params.item_id is None):
            raise DocumentWorkspaceError(
                "itemCollection and itemId must be supplied together",
                code="document_scope_invalid",
            )
        if params.item_collection is not None:
            assert params.item_id is not None
            await self._require_record(params.item_collection, params.item_id)
        return self._translate(lambda: self._index.register_document(params))

    async def publish_index_batch(self, params: PublishIndexBatchParams) -> PublishIndexBatchResult:
        return self._translate(lambda: self._index.publish_revisions(params))

    async def link_document(self, params: LinkDocumentParams) -> LinkResult:
        await self._require_record(params.item_collection, params.item_id)
        return self._translate(lambda: self._index.link_document(params))

    async def unlink_document(self, params: UnlinkDocumentParams) -> dict[str, Any]:
        self._translate(lambda: self._index.unlink_document(params.link_id))
        return {"deleted": params.link_id}

    async def read_document_history(
        self, params: ReadDocumentHistoryParams
    ) -> DocumentHistoryResult:
        return self._translate(
            lambda: self._index.read_history(
                params.document_id,
                limit=params.limit,
                offset=params.offset,
            )
        )

    def close(self) -> None:
        self._index.close()

    async def _require_collection(self, collection: str) -> None:
        if collection not in self._collection_catalog and self._collection_catalog_loader is not None:
            try:
                catalog = await self._collection_catalog_loader()
            except DocumentWorkspaceError:
                raise
            except Exception as exc:
                raise DocumentWorkspaceError(
                    "record-link authority is unavailable",
                    code="link_record_authority_unavailable",
                ) from exc
            if not isinstance(catalog, Mapping):
                raise DocumentWorkspaceError(
                    "record-link authority is unavailable",
                    code="link_record_authority_unavailable",
                )
            self._collection_catalog = catalog
        if collection not in self._collection_catalog:
            raise DocumentWorkspaceError(
                f"collection {collection!r} is not in capability manifest",
                code="link_collection_not_allowed",
            )

    async def _require_record(self, collection: str, item_id: str) -> None:
        await self._require_collection(collection)
        if self._record_exists is None:
            raise DocumentWorkspaceError(
                "record-link authority is unavailable",
                code="link_record_authority_unavailable",
            )
        try:
            exists = await self._record_exists(collection, item_id)
        except DocumentWorkspaceError:
            raise
        except Exception as exc:
            raise DocumentWorkspaceError(
                "record-link authority is unavailable",
                code="link_record_authority_unavailable",
            ) from exc
        if not exists:
            raise DocumentWorkspaceError(
                f"record {collection!r}/{item_id!r} does not exist",
                code="link_record_not_found",
            )

    @staticmethod
    def _translate(operation: Callable[[], T]) -> T:
        try:
            return operation()
        except WorkspaceIndexError as exc:
            raise DocumentWorkspaceError(str(exc), code=exc.code) from exc


__all__ = [
    "DocumentWorkspaceError",
    "DocumentWorkspaceService",
]
