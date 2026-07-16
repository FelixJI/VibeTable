import pytest
from pydantic import ValidationError

from backend.rpc.messages import RpcRequest


def test_request_requires_jsonrpc_2() -> None:
    with pytest.raises(ValidationError):
        RpcRequest.model_validate({"jsonrpc": "1.0", "id": "1", "method": "system.handshake"})


def test_request_accepts_object_params() -> None:
    request = RpcRequest.model_validate(
        {"jsonrpc": "2.0", "id": "1", "method": "system.handshake", "params": {}}
    )
    assert request.method == "system.handshake"
