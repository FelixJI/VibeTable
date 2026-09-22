"""Trusted-host file picker and opaque path-grant capability for plugin Workers."""

from __future__ import annotations

import asyncio
import base64
import binascii
import mimetypes
import time
import uuid
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any, Literal

from backend.application.host_files import HostFiles
from backend.contracts.plugin import PluginEventEnvelope, PluginFileRequest
from backend.contracts.task import SessionPathGrant

MAX_PLUGIN_FILE_BYTES = 1_048_576
MAX_PENDING_FILE_REQUESTS = 16

PluginFileNotificationSink = Callable[[PluginEventEnvelope], Awaitable[None]]


@dataclass
class _PendingFileRequest:
    request: PluginFileRequest
    future: asyncio.Future[SessionPathGrant | None]


class HostFileCapabilityAdapter:
    """Coordinates a native WPF picker without exposing its path to the WebView."""

    def __init__(
        self,
        *,
        files: HostFiles,
        timeout_seconds: float = 300.0,
        clock: Callable[[], float] = time.time,
    ) -> None:
        if timeout_seconds <= 0 or timeout_seconds > 900:
            raise ValueError("file picker timeout must be between 0 and 900 seconds")
        self._files = files
        self._timeout_seconds = timeout_seconds
        self._clock = clock
        self._sink: PluginFileNotificationSink | None = None
        self._pending: dict[str, _PendingFileRequest] = {}

    @property
    def available(self) -> bool:
        return self._sink is not None

    def set_notification_sink(self, sink: PluginFileNotificationSink) -> None:
        self._sink = sink

    async def pick_read(
        self, execution: dict[str, Any], options: dict[str, Any]
    ) -> dict[str, Any] | None:
        selected = await self._request(execution, options, direction="read")
        if selected is None:
            return None
        return selected.model_dump(mode="json", by_alias=True)

    async def pick_write(
        self, execution: dict[str, Any], options: dict[str, Any]
    ) -> dict[str, Any] | None:
        selected = await self._request(execution, options, direction="write")
        if selected is None:
            return None
        return selected.model_dump(mode="json", by_alias=True)

    async def read(self, execution: dict[str, Any], grant_id: str) -> dict[str, str]:
        async with self._files.read(grant_id, run_id=str(execution["runId"])) as source:
            content = source.stream.read(MAX_PLUGIN_FILE_BYTES + 1)
            if len(content) > MAX_PLUGIN_FILE_BYTES:
                raise ValueError("selected plugin input file exceeds the host limit")
            return {"base64": base64.b64encode(content).decode("ascii")}

    async def write(self, execution: dict[str, Any], grant_id: str, encoded: str) -> None:
        try:
            content = base64.b64decode(encoded, validate=True)
        except (ValueError, binascii.Error) as exc:
            raise ValueError("plugin file content is not valid base64") from exc
        if len(content) > MAX_PLUGIN_FILE_BYTES:
            raise ValueError("plugin file output exceeds the host limit")
        async with self._files.write(grant_id, run_id=str(execution["runId"])) as target:
            target.stream.write(content)

    async def resolve(self, request_id: str, grant: SessionPathGrant | None) -> bool:
        pending = self._pending.get(request_id)
        if pending is None or pending.future.done():
            return False
        if grant is not None and grant.direction != pending.request.direction:
            raise ValueError("Host file grant direction does not match the picker request")
        pending.future.set_result(grant)
        return True

    async def _request(
        self,
        execution: dict[str, Any],
        options: dict[str, Any],
        *,
        direction: Literal["read", "write"],
    ) -> SessionPathGrant | None:
        if self._sink is None:
            raise RuntimeError("host file picker channel is unavailable")
        if len(self._pending) >= MAX_PENDING_FILE_REQUESTS:
            raise RuntimeError("host file picker request limit was reached")
        identity = {
            key: execution.get(key) for key in ("runId", "projectKey", "pluginId", "actionId")
        }
        if not all(isinstance(value, str) and value for value in identity.values()):
            raise RuntimeError("plugin file request execution identity is invalid")
        request_id = f"file-{uuid.uuid4().hex}"
        media_types = options.get("mediaTypes", [])
        if not isinstance(media_types, list) or not all(
            isinstance(value, str) and value for value in media_types
        ):
            raise ValueError("plugin file mediaTypes must be a string array")
        suggested_name = options.get("suggestedName")
        media_type = options.get("mediaType")
        if direction == "write" and (
            not isinstance(suggested_name, str)
            or not suggested_name
            or not isinstance(media_type, str)
            or not media_type
        ):
            raise ValueError("plugin write picker requires suggestedName and mediaType")
        request = PluginFileRequest(
            request_id=request_id,
            run_id=str(identity["runId"]),
            project_key=str(identity["projectKey"]),
            plugin_id=str(identity["pluginId"]),
            action_id=str(identity["actionId"]),
            direction=direction,
            media_types=media_types,
            suggested_name=suggested_name if isinstance(suggested_name, str) else None,
            media_type=(
                media_type
                if isinstance(media_type, str)
                else mimetypes.guess_type(suggested_name or "")[0]
            ),
            expires_at=self._clock() + self._timeout_seconds,
        )
        future: asyncio.Future[SessionPathGrant | None] = asyncio.get_running_loop().create_future()
        self._pending[request_id] = _PendingFileRequest(request=request, future=future)
        try:
            await self._sink(
                PluginEventEnvelope(
                    event_type="plugin.file.requested",
                    project_key=request.project_key,
                    entity_id=request_id,
                    revision=1,
                    snapshot=request.model_dump(mode="json", by_alias=True),
                )
            )
            return await asyncio.wait_for(future, timeout=self._timeout_seconds)
        finally:
            self._pending.pop(request_id, None)


__all__ = [
    "MAX_PENDING_FILE_REQUESTS",
    "MAX_PLUGIN_FILE_BYTES",
    "HostFileCapabilityAdapter",
]
