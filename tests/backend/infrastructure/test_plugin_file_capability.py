from __future__ import annotations

import asyncio
from pathlib import Path
from typing import Any

import pytest

from backend.application.task_service import build_task_service
from backend.infrastructure.plugin_file_capability import HostFileCapabilityAdapter


def _execution() -> dict[str, Any]:
    return {
        "runId": "run-1",
        "projectKey": "project-1",
        "pluginId": "com.example.files",
        "actionId": "export",
    }


@pytest.mark.asyncio
async def test_host_file_capability_uses_native_resolution_and_opaque_grants(
    tmp_path: Path,
) -> None:
    source = tmp_path / "source.txt"
    source.write_bytes(b"safe input")
    target = tmp_path / "target.txt"
    events: list[Any] = []
    adapter = HostFileCapabilityAdapter(task_service=build_task_service())

    async def notify(event: Any) -> None:
        events.append(event)

    adapter.set_notification_sink(notify)
    read_task = asyncio.create_task(adapter.pick_read(_execution(), {"mediaTypes": ["text/plain"]}))
    await asyncio.sleep(0)
    request = events[-1].snapshot
    assert str(source) not in str(request)
    assert await adapter.resolve(request["requestId"], str(source)) is True
    read_grant = await read_task
    assert read_grant is not None
    with pytest.raises(ValueError, match="belong"):
        adapter.read({**_execution(), "runId": "run-other"}, read_grant["grantId"])
    content = adapter.read(_execution(), read_grant["grantId"])

    write_task = asyncio.create_task(
        adapter.pick_write(
            _execution(),
            {"suggestedName": "target.txt", "mediaType": "text/plain"},
        )
    )
    await asyncio.sleep(0)
    write_request = events[-1].snapshot
    assert await adapter.resolve(write_request["requestId"], str(target)) is True
    write_grant = await write_task
    assert write_grant is not None
    adapter.write(_execution(), write_grant["grantId"], content["base64"])

    assert target.read_bytes() == b"safe input"
    with pytest.raises(ValueError, match="belong"):
        adapter.write(_execution(), write_grant["grantId"], content["base64"])
