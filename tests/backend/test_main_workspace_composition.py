from __future__ import annotations

import io
from collections.abc import Mapping, Sequence
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest

import backend.__main__ as backend_main
from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.transport import PocketBaseConfig
from backend.application.document_workspace_service import DocumentWorkspaceError
from backend.application.product_data_service import PocketBaseProductDataService
from backend.contracts.document_workspace import (
    LinkDocumentParams,
    ReadFolderParams,
    RegisterDocumentParams,
)


class _ProductTransport:
    def __init__(self) -> None:
        self.requests: list[tuple[str, str, Any]] = []

    async def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = None,
        json_body: Any | None = None,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> Any:
        del query, headers, expected_status
        self.requests.append((method, path, json_body))
        if method == "GET" and path == "/api/vibetable/v1/schema/tables":
            return {"tables": [{"tableId": "orders", "schemaRevision": "schema_1"}]}
        if method == "POST" and path == "/api/vibetable/v1/query":
            assert isinstance(json_body, dict)
            assert json_body["operation"] == "readRows"
            assert json_body["tableId"] == "orders"
            return {
                "rows": [{"id": row_id} for row_id in json_body["rowIds"] if row_id == "order-1"]
            }
        raise AssertionError(f"unexpected product request: {method} {path}")


def _registration(
    document_id: str,
    *,
    item_collection: str | None = None,
    item_id: str | None = None,
) -> RegisterDocumentParams:
    return RegisterDocumentParams(
        workspace_id="workspace-1",
        workspace_name="Workspace",
        document_id=document_id,
        file_name=f"{document_id}.docx",
        scheme_id="main",
        revision_id=f"{document_id}-revision-1",
        hash="a" * 64,
        created_at="2026-07-24T09:00:00Z",
        item_collection=item_collection,
        item_id=item_id,
    )


@pytest.mark.asyncio
async def test_build_server_wires_workspace_to_pocketbase_record_authority(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    transport = _ProductTransport()
    secret = "a" * 64
    client = PocketBaseClient(transport=transport, session_secret=secret)
    product_service = PocketBaseProductDataService(
        client=client,
        transport=transport,
        session_secret=secret,
    )
    config = PocketBaseConfig(
        base_url="http://127.0.0.1:8090",
        session_secret=secret,
    )
    monkeypatch.setattr(
        backend_main,
        "_product_runtime",
        lambda: (product_service, client, config),
    )
    monkeypatch.setattr(backend_main, "_start_realtime", lambda *_args: None)
    monkeypatch.setattr(
        backend_main.sys,
        "stdin",
        SimpleNamespace(buffer=io.BytesIO()),
    )
    monkeypatch.setattr(
        backend_main.sys,
        "stdout",
        SimpleNamespace(buffer=io.BytesIO()),
    )
    monkeypatch.setenv("LOCALAPPDATA", str(tmp_path / "local-app-data"))
    monkeypatch.setenv("VIBETABLE_STATE_DIR", str(tmp_path / "state"))

    _server, workspace, plugin_service, realtime = await backend_main._build_server()
    try:
        registered = await workspace.register_document(
            _registration(
                "doc-scoped",
                item_collection="orders",
                item_id="order-1",
            )
        )
        assert registered.link_id is not None

        folder = await workspace.read_folder(
            ReadFolderParams(collection="orders", item_id="order-1")
        )
        assert [document.document_id for document in folder.documents] == ["doc-scoped"]

        await workspace.register_document(_registration("doc-linked"))
        linked = await workspace.link_document(
            LinkDocumentParams(
                document_id="doc-linked",
                item_collection="orders",
                item_id="order-1",
            )
        )
        assert linked.status == "created"

        with pytest.raises(DocumentWorkspaceError) as missing:
            await workspace.link_document(
                LinkDocumentParams(
                    document_id="doc-linked",
                    item_collection="orders",
                    item_id="missing-order",
                )
            )
        assert missing.value.code == "link_record_not_found"
        assert realtime is None
    finally:
        workspace.close()
        if plugin_service is not None:
            await plugin_service.close()
