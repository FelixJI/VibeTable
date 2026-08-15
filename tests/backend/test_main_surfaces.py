from __future__ import annotations

import pytest

from backend.__main__ import _register_surface_methods
from backend.application.surface_service import SurfaceError, SurfaceService
from backend.contracts.generated_workbench import (
    InterfaceCommitRequest,
    InterfaceDeleteResult,
    InterfaceListResult,
    InterfaceSnapshot,
)
from backend.rpc.dispatcher import CODE_INVALID_PARAMS, RpcDispatcher
from backend.rpc.error_registry import CODE_SURFACE


class _Service(SurfaceService):
    def __init__(self) -> None:
        self.calls: list[tuple[str, object]] = []

    async def list(self) -> InterfaceListResult:
        self.calls.append(("list", None))
        return InterfaceListResult(items=[])

    async def load(self, interface_id: str) -> InterfaceSnapshot:
        self.calls.append(("load", interface_id))
        raise SurfaceError("Interface not found.", code="surface.not_found")

    async def commit(self, request: InterfaceCommitRequest) -> InterfaceSnapshot:
        self.calls.append(("commit", request))
        raise AssertionError("not used")

    async def delete(
        self, interface_id: str, expected_revision: str, idempotency_key: str
    ) -> InterfaceDeleteResult:
        self.calls.append(("delete", (interface_id, expected_revision, idempotency_key)))
        return InterfaceDeleteResult(interface_id=interface_id)


def _dispatcher() -> tuple[RpcDispatcher, _Service]:
    dispatcher = RpcDispatcher()
    service = _Service()
    _register_surface_methods(dispatcher, service)
    return dispatcher, service


def test_surface_rpc_registration_is_closed() -> None:
    dispatcher, _service = _dispatcher()
    assert set(dispatcher.registered_methods) == {
        "interface.list",
        "interface.load",
        "interface.commit",
        "interface.delete",
    }


@pytest.mark.asyncio
async def test_surface_rpc_serializes_list_and_maps_typed_errors() -> None:
    dispatcher, service = _dispatcher()
    listed = await dispatcher.dispatch(
        {"jsonrpc": "2.0", "id": 1, "method": "interface.list", "params": {}}
    )
    missing = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": 2,
            "method": "interface.load",
            "params": {"interfaceId": "interface-1"},
        }
    )

    assert listed == {"jsonrpc": "2.0", "id": 1, "result": {"items": []}}
    assert missing is not None
    assert missing["error"]["code"] == CODE_SURFACE
    assert missing["error"]["data"] == {
        "kind": "surface_error",
        "message": "Interface not found.",
        "code": "surface.not_found",
    }
    assert service.calls == [("list", None), ("load", "interface-1")]


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("method", "params"),
    [
        ("interface.list", {"extra": True}),
        ("interface.load", {}),
        ("interface.load", {"interfaceId": ""}),
        (
            "interface.delete",
            {"interfaceId": "interface-1", "expectedRevision": "rev"},
        ),
        (
            "interface.commit",
            {
                "definition": {},
                "expectedRevision": None,
                "idempotencyKey": "operation-1",
                "provider": "raw-rpc",
            },
        ),
    ],
)
async def test_surface_rpc_rejects_unknown_missing_and_invalid_params(
    method: str, params: dict[str, object]
) -> None:
    dispatcher, _service = _dispatcher()
    response = await dispatcher.dispatch(
        {"jsonrpc": "2.0", "id": 3, "method": method, "params": params}
    )
    assert response is not None
    assert response["error"]["code"] == CODE_INVALID_PARAMS
