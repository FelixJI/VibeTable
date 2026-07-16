from __future__ import annotations

import pytest
from pydantic import BaseModel

from backend.adapters.directus.errors import DirectusTransportError
from backend.rpc.dispatcher import RpcDispatcher, register_directus_errors


class EmptyParams(BaseModel):
    pass


@pytest.mark.asyncio
async def test_dispatcher_maps_directus_error_without_secret_echo() -> None:
    async def fail(params: EmptyParams) -> BaseModel:
        raise DirectusTransportError(
            "permission denied",
            status=403,
            code="FORBIDDEN",
            field_errors={"amount": "permission denied"},
        )

    register_directus_errors()
    dispatcher = RpcDispatcher()
    dispatcher.register("directus.fail", fail, EmptyParams)

    response = await dispatcher.dispatch(
        {"jsonrpc": "2.0", "id": 1, "method": "directus.fail", "params": {}}
    )

    assert response is not None
    assert response["error"]["code"] == -32031
    assert response["error"]["data"] == {
        "kind": "directus_api",
        "message": "permission denied",
        "status": 403,
        "code": "FORBIDDEN",
        "fieldErrors": {"amount": "permission denied"},
    }
