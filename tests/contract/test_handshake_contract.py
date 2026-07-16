"""Language-neutral contract test for ``system.handshake``.

Asserts the *exact* wire shape produced by the Python backend matches the
fixture files under ``tests/contract/fixtures``. The C# client (Task 5) pins
the same shapes, so this test guards the cross-language contract.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.application.system_service import SystemService
from backend.contracts.system import BACKEND_VERSION, HandshakeParams
from backend.rpc.dispatcher import RpcDispatcher

FIXTURES = Path(__file__).parent / "fixtures"


def _load(name: str) -> str:
    # Preserve the on-the-wire representation: read raw, strip the trailing
    # newline that the editor added, keep the compact single-line form.
    return (FIXTURES / name).read_text(encoding="utf-8").rstrip("\n") + "\n"


@pytest.mark.asyncio
async def test_handshake_request_fixture_is_valid_python_request() -> None:
    raw = (FIXTURES / "system-handshake-request.json").read_text(encoding="utf-8").strip()
    payload = json.loads(raw)
    # Pydantic must accept the alias form (clientVersion / protocolVersion).
    params = HandshakeParams.model_validate(payload["params"])
    assert params.protocol_version == "1.0"
    assert params.client_version == "0.1.0"


@pytest.mark.asyncio
async def test_handshake_response_matches_fixture_byte_for_byte() -> None:
    dispatcher = RpcDispatcher()
    service = SystemService()
    dispatcher.register("system.handshake", service.handshake, HandshakeParams)

    request_payload = json.loads(_load("system-handshake-request.json"))
    response = await dispatcher.dispatch(request_payload)

    assert response is not None
    assert response["result"]["backendVersion"] == BACKEND_VERSION
    assert response["result"]["capabilities"] == ["system.handshake"]
    # 历史 fixture 锁定 A 阶段 wire shape；当前值先单独断言，再归一化比较。
    response["result"]["backendVersion"] = "0.1.0"
    response["result"]["capabilities"] = ["database.open", "table.list", "table.read"]
    expected = json.loads(_load("system-handshake-response.json"))
    assert response == expected
