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
from backend.contracts.data_io import PreviewImportParams
from backend.contracts.task import CreateTaskParams, TaskIdParams
from backend.rpc.dispatcher import RpcDispatcher
from tests.backend.host_files_fixture import file_task_fixture as build_task_service

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

    assert _product_runtime() is None


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
    monkeypatch.setattr(backend_main, "_product_runtime", lambda: client)
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
    server, plugin_service = await backend_main._build_server()
    try:
        dispatcher = server._dispatcher
        assert {
            "insights.dashboardQueryLimits",
            "insights.deleteDashboardWorkspace",
            "insights.executeDashboardQuery",
            "insights.listDashboards",
            "insights.panelManifest",
            "insights.readDashboardWorkspace",
            "insights.saveDashboardDraft",
            "preset.delete",
            "preset.list",
            "preset.save",
        }.isdisjoint(dispatcher.registered_methods)
        from tests.backend.host_files_fixture import LocalFilePeer

        peer = LocalFilePeer()
        descriptor = peer.grants.issue(path=str(source), purpose="import_source", direction="read")
        monkeypatch.setattr(server, "call_host_file", peer.call)
        grant_id = descriptor.grant_id
        assert all(not method.startswith("path.") for method in dispatcher.registered_methods)
        assert {"file.token", "file.applyHostChange", "file.saveHostFile"}.isdisjoint(
            dispatcher.registered_methods
        )
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
    finally:
        if plugin_service is not None:
            await plugin_service.close()


def test_recovery_preview_probe_refreshes_schema_without_files_or_plans() -> None:
    class ReadOnlyTransport(_ImportTransport, _Transport):
        def __init__(self) -> None:
            self.calls: list[tuple[str, str]] = []

        async def request(self, method: str, path: str, **kwargs: Any) -> Any:
            assert method == "GET", (method, path)
            self.calls.append((method, path))
            return await super().request(method, path, **kwargs)

    async def run() -> None:
        transport = ReadOnlyTransport()
        client = PocketBaseClient(transport=transport, session_secret="test-only")
        dispatcher = RpcDispatcher()
        tasks = build_task_service()
        runtime = _configure_pocketbase_data_io(dispatcher, client=client, task_service=tasks)
        response = await dispatcher.dispatch(
            {
                "jsonrpc": "2.0",
                "id": "e2e-recovery-owned",
                "method": "data.previewImport",
                "params": {
                    "grantId": "e2e-python-recovery-probe",
                    "collection": "orders",
                    "schemaRevision": "e2e-python-recovery-probe",
                },
            }
        )
        assert response is not None
        assert response["id"] == "e2e-recovery-owned"
        assert response["error"]["code"] == -32060
        assert response["error"]["data"]["message"] == "schema changed since the grid was rendered"
        assert response["error"]["data"]["code"] == "schema_mismatch"
        assert transport.calls == [("GET", "/api/vibetable/v2/schema/tables/orders")]
        assert not runtime._import._plans
        assert not tasks.grants._grants

    asyncio.run(run())


@pytest.mark.asyncio
async def test_build_server_preserves_worker_product_errors(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    from backend.adapters.pocketbase.client import PocketBaseProductError
    from backend.rpc import dispatcher, error_registry

    class RejectingTransport(_Transport):
        async def request(self, method: str, path: str, **kwargs: Any) -> Any:
            assert (method, path) == ("GET", "/api/vibetable/v2/schema/tables/orders")
            raise PocketBaseProductError(
                status=409,
                payload={
                    "code": "schema.revision_conflict",
                    "message": "Schema changed",
                    "path": "schemaRevision",
                    "details": {"current": "schema_2"},
                    "retryable": False,
                },
            )

    registry = error_registry.RpcErrorRegistry()
    monkeypatch.setattr(error_registry, "application_error_registry", registry)
    monkeypatch.setattr(dispatcher, "application_error_registry", registry)
    client = PocketBaseClient(transport=RejectingTransport(), session_secret="test-only")
    monkeypatch.setattr(backend_main, "_product_runtime", lambda: client)
    monkeypatch.setattr(backend_main.sys, "stdin", SimpleNamespace(buffer=io.BytesIO()))
    monkeypatch.setattr(backend_main.sys, "stdout", SimpleNamespace(buffer=io.BytesIO()))
    monkeypatch.setenv("VIBETABLE_STATE_DIR", str(tmp_path / "state"))
    server, plugins = await backend_main._build_server()
    try:
        response = await server._dispatcher.dispatch(
            {
                "jsonrpc": "2.0",
                "id": "worker-error",
                "method": "data.generateTemplate",
                "params": {"collection": "orders", "grantId": "not-opened"},
            }
        )
        assert response == {
            "jsonrpc": "2.0",
            "id": "worker-error",
            "error": {
                "code": -32150,
                "message": "Product data error",
                "data": {
                    "kind": "product_data_error",
                    "message": "Schema changed",
                    "code": "schema.revision_conflict",
                    "path": "schemaRevision",
                    "details": {"current": "schema_2"},
                    "retryable": False,
                },
            },
        }
    finally:
        if plugins is not None:
            await plugins.close()
