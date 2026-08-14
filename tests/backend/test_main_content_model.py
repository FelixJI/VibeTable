from __future__ import annotations

import pytest

from backend.__main__ import _register_content_model_methods
from backend.application.content_model_service import ContentModelError, ContentModelService
from backend.contracts.generated_workbench import (
    ContentProfileCommitRequest,
    ContentProfileDeleteResult,
    ContentProfileSnapshot,
    RecordDocumentLinkCommitRequest,
    RecordDocumentLinkDeleteResult,
    RecordDocumentLinkListResult,
    RecordDocumentLinkRepairRequest,
    RecordDocumentLinkSnapshot,
)
from backend.rpc.dispatcher import CODE_INVALID_PARAMS, RpcDispatcher
from backend.rpc.error_registry import CODE_CONTENT_MODEL


class _Service(ContentModelService):
    def __init__(self) -> None:
        pass

    async def load_profile(self, table_id: str) -> ContentProfileSnapshot:
        raise ContentModelError(f"Profile {table_id} not found.", code="content_profile.not_found")

    async def commit_profile(self, request: ContentProfileCommitRequest) -> ContentProfileSnapshot:
        return ContentProfileSnapshot(profile=request.profile, revision="revision-1")

    async def delete_profile(
        self, table_id: str, expected_revision: str, idempotency_key: str
    ) -> ContentProfileDeleteResult:
        del expected_revision, idempotency_key
        return ContentProfileDeleteResult(table_id=table_id)

    async def list_links(self, table_id: str, record_id: str) -> RecordDocumentLinkListResult:
        del table_id, record_id
        return RecordDocumentLinkListResult(items=[])

    async def commit_link(
        self, request: RecordDocumentLinkCommitRequest
    ) -> RecordDocumentLinkSnapshot:
        del request
        raise AssertionError("not used")

    async def repair_link(
        self, request: RecordDocumentLinkRepairRequest
    ) -> RecordDocumentLinkSnapshot:
        del request
        raise AssertionError("not used")

    async def delete_link(
        self, link_id: str, expected_revision: str, idempotency_key: str
    ) -> RecordDocumentLinkDeleteResult:
        del expected_revision, idempotency_key
        return RecordDocumentLinkDeleteResult(link_id=link_id)


def _dispatcher() -> RpcDispatcher:
    dispatcher = RpcDispatcher()
    _register_content_model_methods(dispatcher, _Service())
    return dispatcher


def test_content_model_rpc_registration_is_closed() -> None:
    assert set(_dispatcher().registered_methods) == {
        "contentProfile.load",
        "contentProfile.commit",
        "contentProfile.delete",
        "recordDocumentLink.list",
        "recordDocumentLink.commit",
        "recordDocumentLink.repair",
        "recordDocumentLink.delete",
    }


@pytest.mark.asyncio
async def test_content_model_rpc_maps_typed_error_and_rejects_unknown_fields() -> None:
    dispatcher = _dispatcher()
    missing = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "contentProfile.load",
            "params": {"tableId": "articles"},
        }
    )
    invalid = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": 2,
            "method": "recordDocumentLink.list",
            "params": {"tableId": "articles", "recordId": "1", "collection": "raw"},
        }
    )

    assert missing is not None
    assert missing["error"]["code"] == CODE_CONTENT_MODEL
    assert missing["error"]["data"]["code"] == "content_profile.not_found"
    assert invalid is not None
    assert invalid["error"]["code"] == CODE_INVALID_PARAMS
