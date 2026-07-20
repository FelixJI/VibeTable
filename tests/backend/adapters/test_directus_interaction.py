from __future__ import annotations

from typing import Any

import pytest

from backend.infrastructure.directus_interaction import DirectusInteractionAdapter


class _Auth:
    async def access_token(self) -> str:
        return "token-1"


class _Transport:
    def __init__(self) -> None:
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> dict[str, Any]:
        self.requests.append({"method": method, "path": path, **kwargs})
        if path.endswith("/confirm/confirm-1"):
            return {"data": {"status": "decided", "decision": "approved"}}
        if path.endswith("/cancel"):
            return {"data": {"status": "cancel-requested"}}
        if method == "GET":
            return {
                "data": {
                    "runId": "run-1",
                    "pluginId": "com.example.write",
                    "actionId": "write",
                    "caller": {"userId": "user-1", "projectId": "local:default"},
                    "cancelRequested": False,
                    "progress": {
                        "current": 2,
                        "total": 10,
                        "message": "处理中",
                        "cancellable": True,
                    },
                    "pendingConfirmation": {
                        "interactionId": "confirm-1",
                        "risk": "write",
                        "title": "确认写入",
                        "preview": {"affectedCount": 2},
                        "expiresAt": 1234,
                    },
                }
            }
        return {"data": {"status": "ok"}}


@pytest.mark.asyncio
async def test_directus_interaction_adapter_maps_authenticated_bridge_contract() -> None:
    transport = _Transport()
    adapter = DirectusInteractionAdapter(transport=transport, auth=_Auth())

    await adapter.register_run(run_id="run-1", plugin_id="com.example.write", action_id="write")
    snapshot = await adapter.get("run-1")
    resolved = await adapter.resolve("run-1", "confirm-1", "approved")
    cancel = await adapter.request_cancel("run-1")
    await adapter.complete_run("run-1", "succeeded")

    assert snapshot.project_key == "local:default"
    assert snapshot.caller == "user-1"
    assert snapshot.progress is not None
    assert snapshot.progress.current == 2
    assert snapshot.pending_confirmation is not None
    assert snapshot.pending_confirmation.preview.affected_count == 2
    assert resolved.status == "resolved"
    assert resolved.decision == "approved"
    assert cancel.cancel_requested is True
    assert transport.requests[0]["expected_status"] == (201,)
    assert transport.requests[0]["json_body"]["contract"] == "vibetable.plugin-run.v1"
    assert transport.requests[2]["json_body"] == {"decision": "approve"}
    assert transport.requests[-1]["json_body"] == {"terminalHint": "succeeded"}


@pytest.mark.asyncio
async def test_directus_interaction_preserves_expired_tombstone_without_fabricated_approval() -> (
    None
):
    class _ExpiredTransport(_Transport):
        async def request(self, method: str, path: str, **kwargs: Any) -> dict[str, Any]:
            if "/confirm/" in path:
                return {"data": {"status": "already-decided", "decision": "expired"}}
            return await super().request(method, path, **kwargs)

    adapter = DirectusInteractionAdapter(transport=_ExpiredTransport(), auth=_Auth())

    result = await adapter.resolve("run-1", "confirm-1", "approved")

    assert result.status == "expired"
    assert result.decision is None
