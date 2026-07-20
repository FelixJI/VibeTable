"""Opt-in lifecycle test against the repository-pinned Directus 12.1.1 runtime."""

from __future__ import annotations

import asyncio
import contextlib
import os
import uuid
from typing import Any

import pytest

from backend.adapters.directus.contracts import DirectusSourceConfig
from backend.adapters.directus.transport import StdlibDirectusTransport
from backend.infrastructure.directus_flow import DirectusFlowAdapter
from backend.infrastructure.directus_interaction import DirectusInteractionAdapter


class _StaticTokenAuth:
    def __init__(self, token: str) -> None:
        self._token = token

    async def access_token(self) -> str:
        return self._token


def _integration_environment() -> tuple[str, str]:
    url = os.environ.get("VIBETABLE_DIRECTUS_INTEGRATION_URL")
    token = os.environ.get("VIBETABLE_DIRECTUS_INTEGRATION_TOKEN")
    if not url or not token:
        pytest.skip("Directus integration URL/token are not configured")
    return url, token


@pytest.mark.integration
@pytest.mark.asyncio
async def test_directus_12_managed_manual_flow_lifecycle_and_wire_shape() -> None:
    url, token = _integration_environment()
    transport = StdlibDirectusTransport(
        DirectusSourceConfig(
            url=url,
            project="plugin-integration",
            token_ref="environment",
        )
    )
    info = await transport.request("GET", "/server/info", access_token=token)
    assert isinstance(info, dict)
    assert info.get("data", {}).get("version") == "12.1.1"
    current_user = await transport.request("GET", "/users/me", access_token=token)
    user_id = current_user.get("data", {}).get("id") if isinstance(current_user, dict) else None
    assert isinstance(user_id, str)

    adapter = DirectusFlowAdapter(transport=transport, auth=_StaticTokenAuth(token))
    interactions = DirectusInteractionAdapter(transport=transport, auth=_StaticTokenAuth(token))
    flow_uuid: str | None = None
    run_registered = False
    try:
        flow_uuid = await adapter.create_inactive_flow(
            trigger="manual",
            definition={
                "name": f"VibeTable plugin integration {uuid.uuid4().hex[:8]}",
                "options": {"collections": ["directus_users"]},
            },
        )
        await adapter.create_operations(
            flow_uuid,
            [
                {
                    "key": "confirm",
                    "type": "vibetable.confirm@1",
                    "options": {
                        "contract": "vibetable.confirm.v1",
                        "runId": "integration-run",
                        "risk": "write",
                        "title": "Confirm Directus integration",
                        "preview": {
                            "summary": [],
                            "sampleRows": [],
                            "affectedCount": 1,
                            "warnings": [],
                        },
                        "timeoutMs": 30_000,
                    },
                },
                {
                    "key": "progress",
                    "type": "vibetable.progress@1",
                    "options": {
                        "contract": "vibetable.progress.v1",
                        "runId": "integration-run",
                        "current": 1,
                        "total": 1,
                        "message": "Directus integration progress",
                        "cancellable": True,
                    },
                },
                {
                    "key": "record-input",
                    "type": "transform",
                    "options": {
                        "json": {
                            "contract": "vibetable.plugin-result.v1",
                            "status": "success",
                            "summary": "Directus integration Flow completed",
                            "warnings": [],
                        }
                    },
                },
            ],
        )
        await adapter.activate_flow(flow_uuid)

        observed = await adapter.read_flow(flow_uuid)
        assert observed is not None
        assert observed.status == "active"
        assert observed.operation_keys == ("confirm", "progress", "record-input")

        await interactions.register_run(
            run_id="integration-run",
            plugin_id="com.vibetable.integration",
            action_id="round-trip",
        )
        run_registered = True
        trigger = asyncio.create_task(
            adapter.trigger_manual(
                flow_uuid,
                {
                    "collection": "directus_users",
                    "keys": [user_id],
                    "payload": {
                        "contract": "vibetable.plugin-action-input.v1",
                        "runId": "integration-run",
                        "pluginId": "com.vibetable.integration",
                        "actionId": "round-trip",
                        "input": {},
                        "context": {},
                    },
                },
            )
        )
        pending = None
        for _ in range(100):
            snapshot = await interactions.get("integration-run")
            pending = snapshot.pending_confirmation
            if pending is not None:
                break
            await asyncio.sleep(0.05)
        assert pending is not None
        decision = await interactions.resolve("integration-run", pending.interaction_id, "approved")
        assert decision.decision == "approved"
        result: dict[str, Any] = await trigger
        assert result == {
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": "Directus integration Flow completed",
            "warnings": [],
        }
        completed = await interactions.get("integration-run")
        assert completed.progress is not None
        assert completed.progress.current == completed.progress.total == 1
        await interactions.complete_run("integration-run", "succeeded")
        run_registered = False
    finally:
        if run_registered:
            with contextlib.suppress(Exception):
                await interactions.complete_run("integration-run", "failed")
        if flow_uuid is not None:
            await adapter.deactivate_flow(flow_uuid)
            await adapter.delete_flow(flow_uuid)
            remaining = await adapter.list_flows()
            assert all(flow.flow_uuid != flow_uuid for flow in remaining)
