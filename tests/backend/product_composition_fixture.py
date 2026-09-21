"""Real Python composition with a recording authority boundary for owner tests."""

from __future__ import annotations

import io
from collections.abc import AsyncIterator
from dataclasses import dataclass
from pathlib import Path
from types import SimpleNamespace

import pytest
import pytest_asyncio

import backend.__main__ as backend_main
from backend.adapters.pocketbase.client import PocketBaseClient
from backend.contracts.product_rpc import JsonValue
from backend.rpc import dispatcher, error_registry
from backend.rpc.dispatcher import CODE_METHOD_NOT_FOUND
from backend.rpc.server import RpcServer


class RecordingAuthority:
    def __init__(self) -> None:
        self.requests: list[tuple[str, str]] = []
        self.failure: Exception | None = None

    async def request(self, method: str, path: str, **kwargs: object) -> JsonValue:
        self.requests.append((method, path))
        if self.failure is not None:
            raise self.failure
        raise AssertionError(f"Unexpected authority request: {method} {path}")

    async def request_multipart(self, path: str, **kwargs: object) -> JsonValue:
        self.requests.append(("MULTIPART", path))
        raise AssertionError("Python must not upload native files")

    async def download_to_file(self, path: str, **kwargs: object) -> int:
        self.requests.append(("DOWNLOAD", path))
        raise AssertionError("Python must not download native files")


@dataclass
class ProductBackend:
    server: RpcServer
    transport: RecordingAuthority

    async def assert_retired(self, method: str, params: object) -> None:
        response = await self.server._dispatcher.dispatch(
            {
                "jsonrpc": "2.0",
                "id": "retired-owner",
                "method": method,
                "params": params,
            }
        )
        assert response is not None
        assert response["id"] == "retired-owner"
        assert response["error"]["code"] == CODE_METHOD_NOT_FOUND
        assert self.transport.requests == []


@pytest_asyncio.fixture
async def product_backend(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> AsyncIterator[ProductBackend]:
    transport = RecordingAuthority()
    client = PocketBaseClient(transport=transport, session_secret="test-only")
    monkeypatch.setattr(backend_main, "_product_runtime", lambda: client)
    monkeypatch.setattr(backend_main.sys, "stdin", SimpleNamespace(buffer=io.BytesIO()))
    monkeypatch.setattr(backend_main.sys, "stdout", SimpleNamespace(buffer=io.BytesIO()))
    monkeypatch.setenv("VIBETABLE_STATE_DIR", str(tmp_path / "state"))
    registry = error_registry.RpcErrorRegistry()
    monkeypatch.setattr(error_registry, "application_error_registry", registry)
    monkeypatch.setattr(dispatcher, "application_error_registry", registry)
    server, plugins = await backend_main._build_server()
    try:
        yield ProductBackend(server, transport)
    finally:
        if plugins is not None:
            await plugins.close()
