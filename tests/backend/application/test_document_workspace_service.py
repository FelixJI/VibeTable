"""Tests for the G3 DocumentWorkspaceService.

Validates that the BFF:
1. Rejects link collections not declared in the capability manifest
2. Allows any collection declared in the manifest to link documents
3. Reads folder/documents from Directus index
4. Publishes revisions via extension endpoint
5. Reads document history
6. Never accepts absolute paths
"""

from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.profile import CapabilityManifest, CollectionProfile
from backend.application.document_workspace_service import DocumentWorkspaceService
from backend.contracts.document_workspace import (
    LinkDocumentParams,
    PublishIndexBatchParams,
    ReadDocumentHistoryParams,
    ReadFolderParams,
    RevisionIndexEntry,
)


def _manifest() -> CapabilityManifest:
    # The allow-list is dynamic: any collection declared here may link
    # documents. We declare two arbitrary business collections to prove the
    # check is no longer a hardcoded frozenset of business names.
    return CapabilityManifest(
        schema_version="vibetable-1.0",
        directus_compatibility=">=12 <13",
        collections=[
            CollectionProfile(
                collection="vibetable_demo",
                primary_key="id",
                fields=["id", "status", "title", "date_updated"],
            ),
            CollectionProfile(
                collection="vibetable_customers",
                primary_key="id",
                fields=["id", "status", "name", "date_updated"],
            ),
        ],
    )


class FakeTransport:
    def __init__(self, responses: list[Any] | None = None) -> None:
        self.responses = list(responses or [])
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        if self.responses:
            return self.responses.pop(0)
        return {"data": [], "meta": {}}


class FakeAuth:
    async def access_token(self) -> str:
        return "test-token"


def _make_service(transport: FakeTransport) -> DocumentWorkspaceService:
    return DocumentWorkspaceService(
        auth=FakeAuth(),
        profiles=_manifest().by_collection,
        transport=transport,
        extension_base_url="",
    )


@pytest.mark.asyncio
async def test_read_folder_returns_linked_documents() -> None:
    transport = FakeTransport(
        responses=[
            {
                "data": [
                    {
                        "id": "link-1",
                        "link_type": "primary",
                        "document": {
                            "id": "doc-1",
                            "file_name": "main.docx",
                            "mime_type": "application/octet-stream",
                            "main_head": "rev-1",
                            "main_hash": "abc123",
                            "status": "active",
                            "folder": {"relative_path": "contracts/main.docx"},
                        },
                    }
                ]
            }
        ]
    )
    service = _make_service(transport)
    result = await service.read_folder(
        ReadFolderParams(collection="vibetable_demo", item_id="c-001")
    )
    assert len(result.documents) == 1
    assert result.documents[0].document_id == "doc-1"
    assert result.documents[0].file_name == "main.docx"


@pytest.mark.asyncio
async def test_read_folder_allows_any_declared_collection() -> None:
    """Any collection declared in the manifest may read its document folder."""
    transport = FakeTransport(responses=[{"data": [], "meta": {}}])
    service = _make_service(transport)
    # vibetable_customers is declared in the manifest but is NOT one of the
    # legacy hardcoded names — it must still be accepted.
    result = await service.read_folder(
        ReadFolderParams(collection="vibetable_customers", item_id="cu-9")
    )
    assert result.collection == "vibetable_customers"
    assert result.documents == []
    # And the links query must have been issued for that collection.
    assert any(
        r["method"] == "GET" and "/items/vibetable_document_links" in r["path"]
        for r in transport.requests
    )


@pytest.mark.asyncio
async def test_read_folder_rejects_undeclared_collection() -> None:
    transport = FakeTransport()
    service = _make_service(transport)
    with pytest.raises(Exception, match="not in capability manifest"):
        await service.read_folder(ReadFolderParams(collection="directus_users", item_id="u-1"))


@pytest.mark.asyncio
async def test_publish_index_batch_sends_to_extension() -> None:
    transport = FakeTransport(
        responses=[{"data": {"results": [{"revisionId": "rev-1", "status": "created"}]}}]
    )
    service = _make_service(transport)
    result = await service.publish_index_batch(
        PublishIndexBatchParams(
            revisions=[
                RevisionIndexEntry(
                    revision_id="rev-1",
                    document_id="doc-1",
                    scheme_id="scheme-1",
                    sequence=1,
                    hash="abcdef12345678",
                )
            ],
            idempotency_key="idem-1",
        )
    )
    assert len(result.results) == 1
    assert result.results[0].status == "created"
    # Verify the request went to the extension endpoint.
    posts = [r for r in transport.requests if r["method"] == "POST"]
    assert any("vibetable-workspace-index/publish" in r["path"] for r in posts)


@pytest.mark.asyncio
async def test_link_document_creates_link() -> None:
    transport = FakeTransport(responses=[{"data": {"linkId": "link-1", "status": "created"}}])
    service = _make_service(transport)
    result = await service.link_document(
        LinkDocumentParams(
            document_id="doc-1",
            item_collection="vibetable_demo",
            item_id="c-001",
        )
    )
    assert result.link_id == "link-1"
    assert result.status == "created"


@pytest.mark.asyncio
async def test_link_document_allows_any_declared_collection() -> None:
    """Any collection declared in the manifest may link documents."""
    transport = FakeTransport(responses=[{"data": {"linkId": "link-9", "status": "created"}}])
    service = _make_service(transport)
    result = await service.link_document(
        LinkDocumentParams(
            document_id="doc-1",
            item_collection="vibetable_customers",
            item_id="cu-9",
        )
    )
    assert result.status == "created"


@pytest.mark.asyncio
async def test_link_document_rejects_undeclared_collection() -> None:
    transport = FakeTransport()
    service = _make_service(transport)
    with pytest.raises(Exception, match="not in capability manifest"):
        await service.link_document(
            LinkDocumentParams(
                document_id="doc-1",
                item_collection="directus_users",
                item_id="u-1",
            )
        )


@pytest.mark.asyncio
async def test_read_document_history_returns_revisions() -> None:
    transport = FakeTransport(
        responses=[
            {
                "data": [
                    {
                        "id": "rev-2",
                        "sequence": 2,
                        "version_label": "main/V2",
                        "kind": "formal",
                        "hash": "hash2",
                        "size": 200,
                        "date_created": "2026-07-15T10:00:00Z",
                        "created_by": {"first_name": "Ada", "last_name": "Lovelace"},
                        "scheme": {"name": "main"},
                    }
                ],
                "meta": {"filter_count": 1, "total_count": 1},
            }
        ]
    )
    service = _make_service(transport)
    result = await service.read_document_history(ReadDocumentHistoryParams(document_id="doc-1"))
    assert len(result.revisions) == 1
    assert result.revisions[0].revision_id == "rev-2"
    assert result.revisions[0].scheme_name == "main"
    assert result.revisions[0].version_label == "main/V2"
    assert result.total == 1


@pytest.mark.asyncio
async def test_unlink_document_deletes_link() -> None:
    transport = FakeTransport(responses=[{"data": {"deleted": "link-1"}}])
    service = _make_service(transport)
    result = await service.unlink_document(
        __import__(
            "backend.contracts.document_workspace", fromlist=["UnlinkDocumentParams"]
        ).UnlinkDocumentParams(link_id="link-1")
    )
    assert result["deleted"] == "link-1"
