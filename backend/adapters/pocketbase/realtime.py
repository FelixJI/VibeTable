"""Bounded PocketBase product-SSE client with cursor resume and de-duplication."""

from __future__ import annotations

import asyncio
import json
from collections.abc import Awaitable, Callable, Mapping
from dataclasses import dataclass
from typing import Any, Literal, Protocol, cast
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen

from backend.adapters.pocketbase.client import SESSION_HEADER
from backend.adapters.pocketbase.transport import PocketBaseConfig, PocketBaseTransportError

EVENTS_PATH = "/api/vibetable/v1/events"
_MAX_LINE_BYTES = 1 << 20
_MAX_EVENT_BYTES = 2 << 20
_MAX_SEEN_EVENT_IDS = 10_000
_OPERATIONS = {"insert", "update", "archive", "restore", "delete", "schema"}
_TASK_STATES = {"pending", "running", "succeeded", "failed", "cancelled"}


@dataclass(frozen=True)
class ProductEvent:
    event_id: str
    topic: Literal["data.changed", "task.changed"]
    sequence: int
    payload: dict[str, Any]
    cursor: str | None = None


class SSEConnection(Protocol):
    async def readline(self, limit: int) -> bytes: ...

    async def close(self) -> None: ...


class SSEConnector(Protocol):
    async def connect(self, after_event_id: str | None) -> SSEConnection: ...


class StdlibSSEConnector:
    """Opens only the validated private loopback origin from ``PocketBaseConfig``."""

    def __init__(self, config: PocketBaseConfig) -> None:
        self._base_url = config.base_url.rstrip("/")
        self._secret = config.session_secret
        self._timeout = config.timeout_seconds

    async def connect(self, after_event_id: str | None) -> SSEConnection:
        return await asyncio.to_thread(self._connect_sync, after_event_id)

    def _connect_sync(self, after_event_id: str | None) -> SSEConnection:
        query = "?" + urlencode({"after": after_event_id}) if after_event_id else ""
        headers = {
            "Accept": "text/event-stream",
            "Cache-Control": "no-cache",
            "User-Agent": "VibeTable-Next/pocketbase.realtime.v1",
            SESSION_HEADER: self._secret,
        }
        if after_event_id:
            headers["Last-Event-ID"] = after_event_id
        request = Request(self._base_url + EVENTS_PATH + query, headers=headers, method="GET")
        try:
            response = urlopen(request, timeout=self._timeout)
        except HTTPError as exc:
            code = _http_code(exc)
            raise PocketBaseTransportError(
                "PocketBase realtime subscription failed",
                code=code,
            ) from None
        except (URLError, TimeoutError, OSError):
            raise PocketBaseTransportError("PocketBase sidecar is unavailable") from None
        content_type = response.headers.get_content_type()
        if content_type != "text/event-stream":
            response.close()
            raise PocketBaseTransportError(
                "PocketBase returned an invalid realtime response",
                code="realtime.invalid_response",
            )
        return _UrlopenSSEConnection(response)


class _UrlopenSSEConnection:
    def __init__(self, response: Any) -> None:
        self._response = response

    async def readline(self, limit: int) -> bytes:
        return await asyncio.to_thread(self._response.readline, limit)

    async def close(self) -> None:
        await asyncio.to_thread(self._response.close)


class PocketBaseRealtimeSession:
    def __init__(self, connection: SSEConnection) -> None:
        self._connection = connection

    async def receive(self) -> ProductEvent | None:
        event_id = ""
        topic = ""
        data_parts: list[bytes] = []
        size = 0
        while True:
            raw = await self._connection.readline(_MAX_LINE_BYTES + 1)
            if not raw:
                raise PocketBaseTransportError(
                    "PocketBase realtime stream closed",
                    code="realtime.disconnected",
                )
            if len(raw) > _MAX_LINE_BYTES:
                raise PocketBaseTransportError(
                    "PocketBase realtime line exceeded the safe size limit",
                    code="realtime.event_too_large",
                )
            if raw in {b"\n", b"\r\n"}:
                if not data_parts:
                    return None
                break
            if raw.startswith(b":"):
                continue
            field, separator, value = raw.rstrip(b"\r\n").partition(b":")
            if not separator:
                continue
            if value.startswith(b" "):
                value = value[1:]
            if field == b"id":
                event_id = _decode_line(value)
            elif field == b"event":
                topic = _decode_line(value)
            elif field == b"data":
                size += len(value)
                if size > _MAX_EVENT_BYTES:
                    raise PocketBaseTransportError(
                        "PocketBase realtime event exceeded the safe size limit",
                        code="realtime.event_too_large",
                    )
                data_parts.append(value)
        return _decode_event(event_id, topic, b"\n".join(data_parts))

    async def close(self) -> None:
        await self._connection.close()


class PocketBaseRealtimeSupervisor:
    """Reconnects with ``Last-Event-ID`` and emits each product event once."""

    def __init__(
        self,
        connector: SSEConnector,
        *,
        sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
        reconcile_cursor_gap: Callable[[], Awaitable[None]] | None = None,
    ) -> None:
        self._connector = connector
        self._sleep = sleep
        self._reconcile_cursor_gap = reconcile_cursor_gap
        self._last_event_id: str | None = None
        self._seen: dict[str, None] = {}

    @property
    def last_event_id(self) -> str | None:
        return self._last_event_id

    async def run(
        self,
        emit: Callable[[ProductEvent], Awaitable[None]],
        stop: asyncio.Event,
    ) -> None:
        attempt = 0
        while not stop.is_set():
            session: PocketBaseRealtimeSession | None = None
            try:
                session = PocketBaseRealtimeSession(
                    await self._connector.connect(self._last_event_id)
                )
                attempt = 0
                while not stop.is_set():
                    event = await session.receive()
                    if event is None or event.event_id in self._seen:
                        continue
                    await emit(event)
                    self._remember(event)
            except PocketBaseTransportError as exc:
                if stop.is_set():
                    break
                if exc.code in {
                    "realtime.cursor_unknown",
                    "realtime.cursor_expired",
                    "realtime.catchup_limit",
                }:
                    if self._reconcile_cursor_gap is None:
                        raise
                    await self._reconcile_cursor_gap()
                    self._last_event_id = None
                attempt += 1
                await self._sleep(min(30.0, 0.5 * (2 ** min(attempt, 6))))
            finally:
                if session is not None:
                    await session.close()

    def _remember(self, event: ProductEvent) -> None:
        self._seen[event.event_id] = None
        self._last_event_id = event.cursor or event.event_id
        while len(self._seen) > _MAX_SEEN_EVENT_IDS:
            self._seen.pop(next(iter(self._seen)))


def _decode_event(event_id: str, topic: str, raw: bytes) -> ProductEvent:
    if not event_id or "\x00" in event_id or "\n" in event_id:
        raise _invalid_event()
    if topic not in {"data.changed", "task.changed"}:
        raise _invalid_event()
    try:
        payload = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise _invalid_event() from None
    if not isinstance(payload, dict):
        raise _invalid_event()
    payload_event_id = payload.get("eventId")
    if not isinstance(payload_event_id, str) or not payload_event_id:
        raise _invalid_event()
    durable_cursor = event_id != payload_event_id
    if durable_cursor and (
        not event_id.startswith("rt:")
        or not event_id.removeprefix("rt:").isdigit()
        or int(event_id.removeprefix("rt:")) < 1
    ):
        raise _invalid_event()
    _validate_envelope(payload, payload_event_id, topic)
    return ProductEvent(
        event_id=payload_event_id,
        topic=cast(Literal["data.changed", "task.changed"], topic),
        sequence=payload["sequence"],
        payload=payload,
        cursor=event_id if durable_cursor else None,
    )


def _validate_envelope(payload: Mapping[str, Any], event_id: str, topic: str) -> None:
    if (
        payload.get("contractVersion") != "1.0"
        or payload.get("eventId") != event_id
        or payload.get("topic") != topic
        or isinstance(payload.get("sequence"), bool)
        or not isinstance(payload.get("sequence"), int)
        or payload["sequence"] < 1
        or not isinstance(payload.get("occurredAt"), str)
        or not payload["occurredAt"]
    ):
        raise _invalid_event()
    if topic == "data.changed":
        required_text = ("schemaRevision", "dataRevision", "tableId")
        record_ids = payload.get("recordIds")
        if (
            any(not isinstance(payload.get(name), str) or not payload[name] for name in required_text)
            or not isinstance(record_ids, list)
            or not all(isinstance(value, str) and value for value in record_ids)
            or payload.get("operation") not in _OPERATIONS
        ):
            raise _invalid_event()
    else:
        progress = payload.get("progress")
        if (
            not isinstance(payload.get("taskId"), str)
            or not payload["taskId"]
            or not isinstance(payload.get("taskType"), str)
            or not payload["taskType"]
            or payload.get("state") not in _TASK_STATES
            or isinstance(progress, bool)
            or not isinstance(progress, (int, float))
            or not 0 <= progress <= 1
        ):
            raise _invalid_event()


def _decode_line(value: bytes) -> str:
    try:
        return value.decode("utf-8")
    except UnicodeDecodeError:
        raise _invalid_event() from None


def _invalid_event() -> PocketBaseTransportError:
    return PocketBaseTransportError(
        "PocketBase returned an invalid realtime event",
        code="realtime.invalid_event",
    )


def _http_code(exc: HTTPError) -> str:
    try:
        payload = json.loads(exc.read(1 << 20))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return "realtime.subscribe_failed"
    if isinstance(payload, dict) and isinstance(payload.get("code"), str):
        return payload["code"]
    return "realtime.subscribe_failed"


__all__ = [
    "EVENTS_PATH",
    "PocketBaseRealtimeSession",
    "PocketBaseRealtimeSupervisor",
    "ProductEvent",
    "SSEConnection",
    "SSEConnector",
    "StdlibSSEConnector",
]
