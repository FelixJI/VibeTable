from __future__ import annotations

import asyncio
import io
import json
from collections.abc import Mapping, Sequence
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest

import backend.__main__ as backend_main
from backend.__main__ import (
    _configure_pocketbase_data_io,
    _product_runtime,
)
from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.data_io import ProductDataIoRuntime
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.adapters.pocketbase.transport import PocketBaseConfig
from backend.application.task_service import build_task_service
from backend.contracts.data_io import PreviewImportParams
from backend.contracts.task import CreateTaskParams, TaskIdParams
from backend.rpc.dispatcher import RpcDispatcher

RETIRED_PROVIDER = "".join(["di", "rectus"])


class _Transport:
    async def request(self, *_args: Any, **_kwargs: Any) -> Any:
        raise AssertionError("composition must not perform network I/O")

    async def request_multipart(self, *_args: Any, **_kwargs: Any) -> Any:
        raise AssertionError("composition must not perform network I/O")

    async def download_to_file(self, *_args: Any, **_kwargs: Any) -> int:
        raise AssertionError("composition must not perform network I/O")


def test_pocketbase_data_io_composition_registers_only_product_paths() -> None:
    client = PocketBaseClient(transport=_Transport(), session_secret="a" * 64)
    dispatcher = RpcDispatcher()
    tasks = build_task_service()

    runtime = _configure_pocketbase_data_io(
        dispatcher,
        client=client,
        task_service=tasks,
    )

    assert isinstance(runtime, ProductDataIoRuntime)
    assert {
        "table.previewPaste",
        "table.applyPaste",
        "data.previewImport",
        "data.applyImport",
        "data.export",
        "data.generateTemplate",
    } <= set(dispatcher.registered_methods)
    assert {"data.import", "data.export"} <= set(tasks.runtime._handlers)
    assert not any(
        RETIRED_PROVIDER in method.casefold() for method in dispatcher.registered_methods
    )


def test_product_runtime_fails_closed_without_sidecar_session(monkeypatch: Any) -> None:
    monkeypatch.delenv("VIBETABLE_SIDECAR_URL", raising=False)
    monkeypatch.delenv("VIBETABLE_SIDECAR_SESSION_SECRET", raising=False)

    service, client, config = _product_runtime()

    assert service is None
    assert client is None
    assert config is None


class _ImportTransport:
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
        if method == "GET" and path == "/api/vibetable/v2/schema/tables":
            return {
                "tables": [
                    {
                        "tableId": "orders",
                        "schemaRevision": "schema_1",
                    }
                ]
            }
        if method == "GET" and path == "/api/vibetable/v2/schema/tables/orders":
            field = json.loads(
                (
                    Path(__file__).parents[2] / "contracts/schema-v2/fixtures/field-definition.json"
                ).read_text(encoding="utf-8")
            )
            field["identity"] = {
                "fieldId": "fld_payload01",
                "physicalName": "f_payload01",
                "providerFieldId": "pb_payload01",
            }
            field["displayName"] = "Payload"
            field["logicalType"] = "json"
            field["storage"]["kind"] = "pocketbase-json"
            field["display"]["kind"] = "json"
            return {
                "contract": "vibetable.schema.v2",
                "tableId": "orders",
                "displayName": "Orders",
                "kind": "base",
                "schemaRevision": "schema_1",
                "dataRevision": 1,
                "archivePolicy": {
                    "mode": "none",
                    "fieldId": None,
                    "archivedValue": None,
                },
                "fields": [field],
                "capabilities": [],
            }
        if method == "POST" and path == "/api/vibetable/v2/import-preview":
            assert isinstance(json_body, dict)
            raw_payload = json_body["rows"][0]["values"]["f_payload01"]
            assert isinstance(raw_payload, str)
            return {
                "contract": "vibetable.import-preview.v1",
                "rows": [
                    {
                        "values": {"f_payload01": json.loads(raw_payload)},
                        "diagnostics": [],
                    }
                ],
            }
        if method == "POST" and path == "/api/vibetable/v1/mutations/apply":
            assert isinstance(json_body, dict)
            assert json_body["operations"][0]["values"]["f_payload01"] == {
                "nested": {"value": 7},
                "items": [1, 2, 3],
            }
            return {
                "contractVersion": "2.0",
                "status": "applied",
                "changeSetId": "change_1",
                "affectedRows": [
                    {
                        "recordId": "row000000000001",
                        "operation": "insert",
                        "revision": "row_0001",
                        "digest": "sha256:" + "a" * 64,
                    }
                ],
                "computedFields": {},
                "newRevision": "row_0001",
                "emittedEvents": [],
                "warnings": [],
            }
        raise AssertionError(f"unexpected product request: {method} {path}")


@pytest.mark.asyncio
async def test_product_import_task_round_trips_typed_json_through_real_task_runtime(
    tmp_path: Path,
) -> None:
    source = tmp_path / "orders.csv"
    source.write_text(
        'f_payload01\n"{""nested"":{""value"":7},""items"":[1,2,3]}"\n',
        encoding="utf-8",
    )
    client = PocketBaseClient(
        transport=_ImportTransport(),  # type: ignore[arg-type]
        session_secret="a" * 64,
    )
    dispatcher = RpcDispatcher()
    tasks = build_task_service()
    runtime = _configure_pocketbase_data_io(
        dispatcher,
        client=client,
        task_service=tasks,
    )
    grant = tasks.issue_import_source(str(source), size_bytes=source.stat().st_size)
    plan = await runtime.preview_import(
        PreviewImportParams(
            grant_id=grant.grant_id,
            collection="orders",
            schema_revision="schema_1",
            mode="create_only",
            column_mapping=[],
        )
    )
    queued = await tasks.create_task(
        CreateTaskParams(
            kind="data.import",
            params={
                "grantId": grant.grant_id,
                "collection": "orders",
                "token": plan.token.token,
                "mode": "create_only",
                "idempotencyPrefix": "import-json-task",
            },
        )
    )
    status = await tasks.status_task(TaskIdParams(task_id=queued.task_id))
    for _ in range(100):
        if status.state not in {"queued", "running"}:
            break
        await asyncio.sleep(0)
        status = await tasks.status_task(TaskIdParams(task_id=queued.task_id))
    assert status.state == "succeeded", status.error
    assert status.model_dump(by_alias=True, mode="json")["result"] == {
        "collection": "orders",
        "createdCount": 1,
        "updatedCount": 0,
        "failedRows": [],
        "chunks": [
            {
                "chunkIndex": 0,
                "createdRowKeys": ["row000000000001"],
                "updatedRowKeys": [],
                "failedRows": [],
                "idempotencyKey": "import-json-task-0",
            }
        ],
        "requestIds": ["import-json-task-0"],
    }


@pytest.mark.asyncio
async def test_build_server_dispatches_import_task_without_internal_error(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    source = tmp_path / "orders.csv"
    source.write_text(
        'f_payload01\n"{""nested"":{""value"":7},""items"":[1,2,3]}"\n',
        encoding="utf-8",
    )
    transport = _ImportTransport()
    secret = "a" * 64
    client = PocketBaseClient(
        transport=transport,  # type: ignore[arg-type]
        session_secret=secret,
    )
    product_service = PocketBaseProductRpc(
        client=client,
        transport=transport,  # type: ignore[arg-type]
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
    output = io.BytesIO()
    monkeypatch.setattr(
        backend_main.sys,
        "stdin",
        SimpleNamespace(buffer=io.BytesIO()),
    )
    monkeypatch.setattr(
        backend_main.sys,
        "stdout",
        SimpleNamespace(buffer=output),
    )
    monkeypatch.setenv("LOCALAPPDATA", str(tmp_path / "local-app-data"))
    monkeypatch.setenv("VIBETABLE_STATE_DIR", str(tmp_path / "state"))
    server, plugin_service, realtime = await backend_main._build_server()
    try:
        dispatcher = server._dispatcher
        registered = await dispatcher.dispatch(
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "path.registerImportSource",
                "params": {
                    "path": str(source),
                    "sizeBytes": source.stat().st_size,
                    "mimeType": "text/csv",
                },
            }
        )
        assert registered is not None
        assert "error" not in registered
        grant_id = registered["result"]["grantId"]
        previewed = await dispatcher.dispatch(
            {
                "jsonrpc": "2.0",
                "id": 2,
                "method": "data.previewImport",
                "params": {
                    "grantId": grant_id,
                    "collection": "orders",
                    "schemaRevision": "schema_1",
                    "mode": "create_only",
                    "columnMapping": [],
                },
            }
        )
        assert previewed is not None
        assert "error" not in previewed
        token = previewed["result"]["token"]["token"]
        created = await dispatcher.dispatch(
            {
                "jsonrpc": "2.0",
                "id": 3,
                "method": "task.create",
                "params": {
                    "kind": "data.import",
                    "params": {
                        "grantId": grant_id,
                        "collection": "orders",
                        "token": token,
                        "mode": "create_only",
                        "idempotencyPrefix": "import-json-build-server",
                    },
                },
            }
        )
        assert created is not None
        assert "error" not in created
        task_id = created["result"]["taskId"]
        status: dict[str, Any] | None = None
        for request_id in range(4, 104):
            status = await dispatcher.dispatch(
                {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "method": "task.status",
                    "params": {"taskId": task_id},
                }
            )
            assert status is not None
            assert "error" not in status
            if status["result"]["state"] not in {"queued", "running"}:
                break
            await asyncio.sleep(0)
        assert status is not None
        assert status["result"]["state"] == "succeeded"
        assert status["result"]["result"]["createdCount"] == 1
        assert realtime is None
    finally:
        if plugin_service is not None:
            await plugin_service.close()
