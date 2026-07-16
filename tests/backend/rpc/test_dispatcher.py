"""Tests for ``backend.rpc.dispatcher.RpcDispatcher``.

Covers the contract spelled out in Task 3 of the SDD brief:

* unknown method -> JSON-RPC ``-32601``
* ``system.handshake`` with a mismatched protocol version -> ``-32001``
  with ``data.kind == "protocol_mismatch"``
* malformed request payload -> ``-32600``
* params failing the registered Pydantic model -> ``-32602``
* successful handshake serializes the result by alias and echoes the id
* notifications (no ``id``) produce no response
"""

from __future__ import annotations

import pytest

from backend.application.system_service import SystemService
from backend.contracts.system import BACKEND_VERSION, HandshakeParams
from backend.rpc.dispatcher import RpcDispatcher


@pytest.fixture
def dispatcher() -> RpcDispatcher:
    disp = RpcDispatcher()
    service = SystemService()
    disp.register("system.handshake", service.handshake, HandshakeParams)
    return disp


@pytest.mark.asyncio
async def test_unknown_method_returns_method_not_found(dispatcher: RpcDispatcher) -> None:
    response = await dispatcher.dispatch(
        {"jsonrpc": "2.0", "id": "1", "method": "missing", "params": {}}
    )
    assert response is not None
    assert response["error"]["code"] == -32601
    # Success channel must be absent on error responses.
    assert "result" not in response
    assert response["id"] == "1"


@pytest.mark.asyncio
async def test_handshake_rejects_protocol_mismatch(dispatcher: RpcDispatcher) -> None:
    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": "1",
            "method": "system.handshake",
            "params": {"clientVersion": "0.1.0", "protocolVersion": "2.0"},
        }
    )
    assert response is not None
    assert response["error"]["code"] == -32001
    assert response["error"]["data"]["kind"] == "protocol_mismatch"


@pytest.mark.asyncio
async def test_invalid_request_returns_invalid_request(dispatcher: RpcDispatcher) -> None:
    # Wrong jsonrpc version -> RpcRequest validation fails -> -32600.
    response = await dispatcher.dispatch(
        {"jsonrpc": "1.0", "id": "9", "method": "system.handshake", "params": {}}
    )
    assert response is not None
    assert response["error"]["code"] == -32600
    assert response["id"] == "9"


@pytest.mark.asyncio
async def test_invalid_params_returns_invalid_params(dispatcher: RpcDispatcher) -> None:
    # Missing required protocolVersion -> HandshakeParams validation fails -> -32602.
    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": "7",
            "method": "system.handshake",
            "params": {"clientVersion": "0.1.0"},
        }
    )
    assert response is not None
    assert response["error"]["code"] == -32602


@pytest.mark.asyncio
async def test_successful_handshake_serializes_by_alias(dispatcher: RpcDispatcher) -> None:
    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": "handshake-1",
            "method": "system.handshake",
            "params": {"clientVersion": "0.1.0", "protocolVersion": "1.0"},
        }
    )
    assert response is not None
    assert response["id"] == "handshake-1"
    assert response["result"] == {
        "backendVersion": BACKEND_VERSION,
        "protocolVersion": "1.0",
        "capabilities": ["system.handshake"],
    }
    assert "error" not in response


def test_registered_methods_are_sorted_and_current(dispatcher: RpcDispatcher) -> None:
    assert dispatcher.registered_methods == ("system.handshake",)


@pytest.mark.asyncio
async def test_notification_without_id_returns_none(dispatcher: RpcDispatcher) -> None:
    # A request without an id is a notification: no response is returned.
    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": None,
            "method": "system.handshake",
            "params": {"clientVersion": "0.1.0", "protocolVersion": "1.0"},
        }
    )
    assert response is None


@pytest.mark.asyncio
async def test_generic_handler_exception_maps_to_internal_error() -> None:
    """Any handler exception not in the typed error map must surface as
    ``-32603`` internal error (the dispatcher must never raise)."""
    from pydantic import BaseModel

    from backend.rpc.dispatcher import RpcDispatcher

    class _Params(BaseModel):
        model_config = {"extra": "forbid"}

    class _BoomError(Exception):
        pass

    async def _handler(_params: _Params) -> None:
        raise _BoomError("unexpected")

    disp = RpcDispatcher()
    disp.register("test.boom", _handler, _Params)
    response = await disp.dispatch(
        {"jsonrpc": "2.0", "id": "x", "method": "test.boom", "params": {}}
    )
    assert response is not None
    assert response["error"]["code"] == -32603
    assert "data" not in response["error"]  # internal errors carry no data


@pytest.mark.asyncio
async def test_sync_handler_receives_unpacked_validated_fields() -> None:
    """Registration adapters may expose synchronous services with DTO fields."""
    from pydantic import BaseModel

    class _Params(BaseModel):
        value: int

    def handler(value: int) -> dict[str, int]:
        return {"doubled": value * 2}

    disp = RpcDispatcher()
    disp.register("test.sync", handler, _Params)

    response = await disp.dispatch(
        {"jsonrpc": "2.0", "id": "sync-1", "method": "test.sync", "params": {"value": 4}}
    )

    assert response is not None
    assert response["result"] == {"doubled": 8}
