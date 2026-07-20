"""G3 document workspace service: metadata RPC over the workspace index.

The service reads/writes Directus workspace index collections under the
current user's token. It handles metadata only — never accepts local
absolute paths or binary payloads. The C# publisher handles outbox events;
this BFF writes receipts and deletes outbox events on success.

All workspace index collections are read-only for ordinary users; the
publish/link/unlink operations go through the vibetable-workspace-index extension
endpoint which enforces current-user accountability and idempotency.
"""

from __future__ import annotations

from typing import Any

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.profile import CollectionProfile
from backend.contracts.document_workspace import (
    DocumentHistoryResult,
    DocumentListResult,
    DocumentRevisionEntry,
    DocumentSummary,
    FolderResult,
    LinkDocumentParams,
    LinkResult,
    PublishIndexBatchParams,
    PublishIndexBatchResult,
    PublishResult,
    ReadDocumentHistoryParams,
    ReadDocumentsParams,
    ReadFolderParams,
    UnlinkDocumentParams,
)


class DocumentWorkspaceError(Exception):
    """A document-workspace-flow error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


class DocumentWorkspaceService:
    """Document workspace metadata service."""

    def __init__(
        self,
        *,
        auth: DirectusAuthBroker,
        profiles: dict[str, CollectionProfile],
        transport: Any,
        extension_base_url: str = "",
    ) -> None:
        self._auth = auth
        self._profiles = profiles
        self._transport = transport
        self._extension_base_url = extension_base_url

    # ------------------------------------------------------------------
    # readFolder
    # ------------------------------------------------------------------

    async def read_folder(self, params: ReadFolderParams) -> FolderResult:
        """Read documents linked to a business item.

        Queries vibetable_document_links for the item, then resolves each
        document's summary. If a document's working file is missing on
        the local side (determined by the C# host, not this BFF), the
        ``is_missing`` flag is set by the host after receiving this result.
        """
        if params.collection not in self._profiles:
            raise DocumentWorkspaceError(
                f"collection {params.collection!r} is not in capability manifest",
                code="link_collection_not_allowed",
            )
        token = await self._auth.access_token()

        # Query links for this business item.
        links_payload = await self._transport.request(
            "GET",
            "/items/vibetable_document_links",
            access_token=token,
            query={
                "filter": {
                    "item_collection": {"_eq": params.collection},
                    "item_id": {"_eq": params.item_id},
                    "status": {"_eq": "active"},
                },
                "fields": [
                    "id",
                    "document.id",
                    "document.workspace.workspace_id",
                    "document.file_name",
                    "document.mime_type",
                    "document.main_head",
                    "document.main_hash",
                    "document.status",
                    "document.folder.relative_path",
                    "document.folder.display_name",
                    "link_type",
                ],
                "limit": 100,
            },
        )
        raw_links = _response_list(links_payload)

        documents = [
            _document_summary(
                raw.get("document") or {},
                link_id=str(raw.get("id", "")) or None,
                link_type=str(raw.get("link_type", "primary")),
            )
            for raw in raw_links
        ]

        return FolderResult(
            collection=params.collection,
            item_id=params.item_id,
            folder_id=None,
            documents=documents,
        )

    async def read_documents(self, params: ReadDocumentsParams) -> DocumentListResult:
        """Read the global workspace-document index without local paths."""
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            "/items/vibetable_documents",
            access_token=token,
            query={
                "filter": {"status": {"_eq": "active"}},
                "fields": [
                    "id",
                    "workspace.workspace_id",
                    "file_name",
                    "mime_type",
                    "main_head",
                    "main_hash",
                    "status",
                    "folder.relative_path",
                    "folder.display_name",
                ],
                "limit": params.limit,
                "offset": params.offset,
                "meta": "filter_count,total_count",
                "sort": "-date_updated",
            },
        )
        raw_documents = _response_list(payload)
        meta = _response_meta(payload)
        documents = [_document_summary(raw) for raw in raw_documents]
        return DocumentListResult(
            documents=documents,
            total=_safe_int(meta.get("filter_count"))
            or _safe_int(meta.get("total_count"))
            or len(documents),
        )

    # ------------------------------------------------------------------
    # publishIndexBatch
    # ------------------------------------------------------------------

    async def publish_index_batch(self, params: PublishIndexBatchParams) -> PublishIndexBatchResult:
        """Publish a batch of revision metadata to the workspace index.

        Delegates to the vibetable-workspace-index extension endpoint. The
        extension handles idempotency (same revisionId + hash = no-op)
        and immutable conflict detection (same revisionId, different hash).
        """
        token = await self._auth.access_token()

        revisions_payload = [
            {
                "documentId": rev.document_id,
                "schemeId": rev.scheme_id,
                "revisionId": rev.revision_id,
                "parentRevisionId": rev.parent_revision_id,
                "sequence": rev.sequence,
                "versionLabel": rev.version_label,
                "kind": rev.kind,
                "hash": rev.hash,
                "size": rev.size,
                "mimeType": rev.mime_type,
                "createdBy": rev.created_by,
                "deviceId": rev.device_id,
                "comment": rev.comment,
            }
            for rev in params.revisions
        ]

        response = await self._transport.request(
            "POST",
            f"{self._extension_base_url}/vibetable-workspace-index/publish",
            access_token=token,
            json_body={
                "revisions": revisions_payload,
                "idempotencyKey": params.idempotency_key,
            },
        )

        raw_results = _response_data_list(response)
        results = [
            PublishResult(
                revision_id=str(r.get("revisionId", "")),
                status=str(r.get("status", "unknown")),
            )
            for r in raw_results
        ]
        conflicts = [
            str(r.get("revisionId", "")) for r in raw_results if r.get("status") == "conflict"
        ]
        return PublishIndexBatchResult(results=results, conflicts=conflicts)

    # ------------------------------------------------------------------
    # linkDocument / unlinkDocument
    # ------------------------------------------------------------------

    async def link_document(self, params: LinkDocumentParams) -> LinkResult:
        """Link a document to a business item."""
        if params.item_collection not in self._profiles:
            raise DocumentWorkspaceError(
                f"collection {params.item_collection!r} is not in capability manifest",
                code="link_collection_not_allowed",
            )
        token = await self._auth.access_token()
        response = await self._transport.request(
            "POST",
            f"{self._extension_base_url}/vibetable-workspace-index/link",
            access_token=token,
            json_body={
                "documentId": params.document_id,
                "itemCollection": params.item_collection,
                "itemId": params.item_id,
                "linkType": params.link_type,
            },
        )
        data = _response_data(response)
        return LinkResult(
            link_id=str(data.get("linkId", "")),
            status=str(data.get("status", "created")),
        )

    async def unlink_document(self, params: UnlinkDocumentParams) -> dict[str, Any]:
        """Remove a document link (only the link, not the file)."""
        token = await self._auth.access_token()
        await self._transport.request(
            "POST",
            f"{self._extension_base_url}/vibetable-workspace-index/unlink",
            access_token=token,
            json_body={"linkId": params.link_id},
        )
        return {"deleted": params.link_id}

    # ------------------------------------------------------------------
    # readDocumentHistory
    # ------------------------------------------------------------------

    async def read_document_history(
        self, params: ReadDocumentHistoryParams
    ) -> DocumentHistoryResult:
        """Read the version history of a document from the index."""
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            "/items/vibetable_document_revisions",
            access_token=token,
            query={
                "filter": {
                    "document": {"_eq": params.document_id},
                    "status": {"_eq": "active"},
                },
                "fields": [
                    "id",
                    "sequence",
                    "version_label",
                    "kind",
                    "hash",
                    "size",
                    "date_created",
                    "created_by.first_name",
                    "created_by.last_name",
                    "scheme.name",
                ],
                "limit": params.limit,
                "offset": params.offset,
                "meta": "filter_count,total_count",
                "sort": "-sequence",
            },
        )
        raw_revisions = _response_list(payload)
        meta = _response_meta(payload)

        entries = [
            DocumentRevisionEntry(
                revision_id=str(r.get("id", "")),
                scheme_name=str((r.get("scheme") or {}).get("name", "")) or None,
                sequence=int(r.get("sequence", 0)),
                version_label=str(r.get("version_label", "")),
                kind=str(r.get("kind", "formal")),
                hash=str(r.get("hash", "")),
                size=int(r.get("size", 0)),
                created_at=str(r.get("date_created", "")),
                created_by=_display_name(r.get("created_by") or {}),
            )
            for r in raw_revisions
        ]

        return DocumentHistoryResult(
            document_id=params.document_id,
            revisions=entries,
            total=_safe_int(meta.get("filter_count"))
            or _safe_int(meta.get("total_count"))
            or len(entries),
        )


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _response_list(payload: Any) -> list[dict[str, Any]]:
    if isinstance(payload, dict) and isinstance(payload.get("data"), list):
        return [item for item in payload["data"] if isinstance(item, dict)]
    return []


def _document_summary(
    doc: dict[str, Any],
    *,
    link_id: str | None = None,
    link_type: str | None = None,
) -> DocumentSummary:
    return DocumentSummary(
        link_id=link_id,
        document_id=str(doc.get("id", "")),
        workspace_id=str((doc.get("workspace") or {}).get("workspace_id", "")),
        file_name=str(doc.get("file_name", "")),
        mime_type=str(doc.get("mime_type", "")) or None,
        main_head=str(doc.get("main_head", "")) or None,
        main_hash=str(doc.get("main_hash", "")) or None,
        status=str(doc.get("status", "active")),
        link_type=link_type,
        folder_relative_path=str((doc.get("folder") or {}).get("relative_path", "")) or None,
    )


def _response_data(payload: Any) -> dict[str, Any]:
    if isinstance(payload, dict):
        data = payload.get("data")
        if isinstance(data, dict):
            return data
    return {}


def _response_data_list(payload: Any) -> list[dict[str, Any]]:
    if not isinstance(payload, dict):
        return []
    data = payload.get("data")
    if isinstance(data, list):
        return [r for r in data if isinstance(r, dict)]
    if isinstance(data, dict) and isinstance(data.get("results"), list):
        return [r for r in data["results"] if isinstance(r, dict)]
    return []


def _response_meta(payload: Any) -> dict[str, Any]:
    if isinstance(payload, dict) and isinstance(payload.get("meta"), dict):
        return payload["meta"]
    return {}


def _safe_int(value: Any) -> int | None:
    return value if isinstance(value, int) else None


def _display_name(user: dict[str, Any]) -> str | None:
    first = user.get("first_name")
    last = user.get("last_name")
    if first or last:
        return " ".join(p for p in [first, last] if p).strip() or None
    return None


__all__ = [
    "DocumentWorkspaceError",
    "DocumentWorkspaceService",
]
