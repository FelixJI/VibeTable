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
from pathlib import Path
from typing import Any, Literal

from backend.contracts.plugin import PluginEventEnvelope, PluginFileRequest

MAX_PLUGIN_FILE_BYTES = 1_048_576
MAX_PENDING_FILE_REQUESTS = 16

PluginFileNotificationSink = Callable[[PluginEventEnvelope], Awaitable[None]]


@dataclass
class _PendingFileRequest:
    request: PluginFileRequest
    future: asyncio.Future[str | None]


class HostFileCapabilityAdapter:
    """Coordinates a native WPF picker without exposing its path to the WebView."""

    def __init__(
        self,
        *,
        task_service: Any,
        timeout_seconds: float = 300.0,
        clock: Callable[[], float] = time.time,
    ) -> None:
        if timeout_seconds <= 0 or timeout_seconds > 900:
            raise ValueError("file picker timeout must be between 0 and 900 seconds")
        self._task_service = task_service
        self._timeout_seconds = timeout_seconds
        self._clock = clock
        self._sink: PluginFileNotificationSink | None = None
        self._pending: dict[str, _PendingFileRequest] = {}
        self._grants: dict[str, tuple[str, Literal["read", "write"]]] = {}

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
        path = Path(selected)
        if not path.is_file():
            raise ValueError("selected plugin input file is unavailable")
        if path.stat().st_size > MAX_PLUGIN_FILE_BYTES:
            raise ValueError("selected plugin input file exceeds the host limit")
        descriptor = self._task_service.issue_import_source(
            str(path), size_bytes=path.stat().st_size
        )
        self._grants[descriptor.grant_id] = (str(execution["runId"]), "read")
        return descriptor.model_dump(mode="json", by_alias=True)

    async def pick_write(
        self, execution: dict[str, Any], options: dict[str, Any]
    ) -> dict[str, Any] | None:
        selected = await self._request(execution, options, direction="write")
        if selected is None:
            return None
        descriptor = self._task_service.issue_export_target(selected)
        self._grants[descriptor.grant_id] = (str(execution["runId"]), "write")
        return descriptor.model_dump(mode="json", by_alias=True)

    def read(self, execution: dict[str, Any], grant_id: str) -> dict[str, str]:
        self._require_grant(execution, grant_id, "read")
        path = Path(
            self._task_service.resolve_path(grant_id, purpose="import_source", direction="read")
        )
        content = path.read_bytes()
        if len(content) > MAX_PLUGIN_FILE_BYTES:
            raise ValueError("selected plugin input file exceeds the host limit")
        self._task_service.consume_grant(grant_id)
        self._grants.pop(grant_id, None)
        return {"base64": base64.b64encode(content).decode("ascii")}

    def write(self, execution: dict[str, Any], grant_id: str, encoded: str) -> None:
        self._require_grant(execution, grant_id, "write")
        try:
            content = base64.b64decode(encoded, validate=True)
        except (ValueError, binascii.Error) as exc:
            raise ValueError("plugin file content is not valid base64") from exc
        if len(content) > MAX_PLUGIN_FILE_BYTES:
            raise ValueError("plugin file output exceeds the host limit")
        path = Path(
            self._task_service.resolve_path(grant_id, purpose="export_target", direction="write")
        )
        path.write_bytes(content)
        self._task_service.consume_grant(grant_id)
        self._grants.pop(grant_id, None)

    def _require_grant(
        self,
        execution: dict[str, Any],
        grant_id: str,
        direction: Literal["read", "write"],
    ) -> None:
        run_id = execution.get("runId")
        if not isinstance(run_id, str) or self._grants.get(grant_id) != (run_id, direction):
            raise ValueError("plugin file grant does not belong to this run and direction")

    async def resolve(self, request_id: str, selected_path: str | None) -> bool:
        pending = self._pending.get(request_id)
        if pending is None or pending.future.done():
            return False
        pending.future.set_result(selected_path)
        return True

    async def _request(
        self,
        execution: dict[str, Any],
        options: dict[str, Any],
        *,
        direction: Literal["read", "write"],
    ) -> str | None:
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
        future: asyncio.Future[str | None] = asyncio.get_running_loop().create_future()
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
