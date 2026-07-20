from __future__ import annotations

from typing import Any

import pytest

from backend.infrastructure.directus_flow import DirectusFlowAdapter


class _Auth:
    async def access_token(self) -> str:
        return "token-1"


class _Transport:
    def __init__(self) -> None:
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> dict[str, Any]:
        self.requests.append({"method": method, "path": path, **kwargs})
        if method == "POST" and path == "/flows":
            return {"data": {"id": "flow-1"}}
        if method == "POST" and path == "/operations":
            return {
                "data": {
                    "id": f"op-{sum(1 for item in self.requests if item['path'] == '/operations' and item['method'] == 'POST')}"
                }
            }
        if method == "GET" and path == "/flows/flow-1":
            return {
                "data": {
                    "id": "flow-1",
                    "trigger": "manual",
                    "status": "active",
                    "name": "Managed",
                    "operation": "op-1",
                }
            }
        if method == "GET" and path == "/operations":
            return {
                "data": [
                    {
                        "id": "op-1",
                        "key": "confirm",
                        "type": "vibetable.confirm@1",
                        "options": {"title": "确认"},
                        "resolve": "op-2",
                    },
                    {
                        "id": "op-2",
                        "key": "write",
                        "type": "items.update",
                        "options": {"collection": "articles"},
                    },
                ]
            }
        if method == "POST" and path == "/flows/trigger/flow-1":
            return {"ok": True}
        return {"data": {}}


@pytest.mark.asyncio
async def test_directus_flow_adapter_hides_crud_and_manual_trigger_wire_shapes() -> None:
    transport = _Transport()
    adapter = DirectusFlowAdapter(transport=transport, auth=_Auth())

    flow_uuid = await adapter.create_inactive_flow(trigger="manual", definition={"name": "Managed"})
    await adapter.create_operations(
        flow_uuid,
        [
            {"key": "confirm", "type": "vibetable.confirm@1", "options": {}},
            {"key": "write", "type": "items.update", "options": {}},
        ],
    )
    await adapter.activate_flow(flow_uuid)
    observed = await adapter.read_flow(flow_uuid)
    result = await adapter.trigger_manual(
        flow_uuid,
        {
            "collection": "articles",
            "keys": ["a-1"],
            "payload": {"contract": "vibetable.plugin-action-input.v1"},
        },
    )

    assert observed is not None
    assert observed.status == "active"
    assert observed.operation_keys == ("confirm", "write")
    assert observed.definition["operation"] == "confirm"
    assert observed.definition["operations"][0]["resolve"] == "write"
    assert observed.definition["operations"][1]["options"] == {"collection": "articles"}
    assert result == {"ok": True}
    assert transport.requests[0] == {
        "method": "POST",
        "path": "/flows",
        "access_token": "token-1",
        "json_body": {"name": "Managed", "trigger": "manual", "status": "inactive"},
    }
    creates = [
        request
        for request in transport.requests
        if request["method"] == "POST" and request["path"] == "/operations"
    ]
    assert [request["json_body"]["key"] for request in creates] == ["confirm", "write"]
    assert all(request["json_body"]["flow"] == "flow-1" for request in creates)
    patches = [
        request
        for request in transport.requests
        if request["method"] == "PATCH" and request["path"].startswith("/operations/")
    ]
    assert patches[0]["json_body"] == {"resolve": "op-2"}
    root = next(
        request
        for request in transport.requests
        if request["method"] == "PATCH"
        and request["path"] == "/flows/flow-1"
        and "operation" in request["json_body"]
    )
    assert root["json_body"] == {"operation": "op-1"}
    assert any(request.get("json_body") == {"status": "active"} for request in transport.requests)
    assert transport.requests[-1]["path"] == "/flows/trigger/flow-1"
