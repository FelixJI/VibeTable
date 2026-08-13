"""Bounded PocketBase product-SSE client with cursor resume and de-duplication."""

from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator, Awaitable, Callable, Mapping
from contextlib import AbstractAsyncContextManager, suppress
from dataclasses import dataclass
from typing import Any, Literal, Protocol, cast

import httpx
from httpx_sse import EventSource, ServerSentEvent, SSEError, aconnect_sse

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
    async def receive(self) -> ServerSentEvent: ...

    async def close(self) -> None: ...


class SSEConnector(Protocol):
    async def connect(self, after_event_id: str | None) -> SSEConnection: ...


class StdlibSSEConnector:
    """HTTPX-SSE connector kept under the stable adapter class name."""

    def __init__(
        self,
        config: PocketBaseConfig,
        *,
        http_transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self._base_url = config.base_url.rstrip("/")
        self._secret = config.session_secret
        self._timeout = config.timeout_seconds
        self._http_transport = http_transport

    async def connect(self, after_event_id: str | None) -> SSEConnection:
        headers = {
            "Cache-Control": "no-cache",
            "User-Agent": "VibeTable-Next/pocketbase.realtime.v1",
            SESSION_HEADER: self._secret,
        }
        if after_event_id:
            headers["Last-Event-ID"] = after_event_id
        client = httpx.AsyncClient(
            base_url=self._base_url,
            timeout=self._timeout,
            transport=self._http_transport,
            trust_env=False,
        )
        manager = aconnect_sse(
            client,
            "GET",
            EVENTS_PATH,
            params={"after": after_event_id} if after_event_id else None,
            headers=headers,
        )
        entered = False
        try:
            event_source = await manager.__aenter__()
            entered = True
            response = event_source.response
            if response.status_code not in range(200, 300):
                raw = await _read_error_body(response)
                code = _http_code(raw)
                raise PocketBaseTransportError(
                    "PocketBase realtime subscription failed",
                    code=code,
                )
            content_type = response.headers.get("Content-Type", "").partition(";")[0]
            if content_type != "text/event-stream":
                raise PocketBaseTransportError(
                    "PocketBase returned an invalid realtime response",
                    code="realtime.invalid_response",
                )
            if not isinstance(response.stream, httpx.AsyncByteStream):
                raise PocketBaseTransportError(
                    "PocketBase returned an invalid realtime response",
                    code="realtime.invalid_response",
                )
            response.stream = _BoundedSSEStream(response.stream)
            return _HttpxSSEConnection(client, manager, event_source.aiter_sse())
        except PocketBaseTransportError:
            if entered:
                await manager.__aexit__(None, None, None)
            await client.aclose()
            raise
        except (httpx.TransportError, SSEError):
            if entered:
                await manager.__aexit__(None, None, None)
            await client.aclose()
            raise PocketBaseTransportError("PocketBase sidecar is unavailable") from None


class _BoundedSSEStream(httpx.AsyncByteStream):
    """Enforces byte budgets before the SSE decoder buffers a complete event."""

    def __init__(self, source: httpx.AsyncByteStream) -> None:
        self._source = source
        self._line_size = 0
        self._event_size = 0
        self._previous_cr = False

    async def __aiter__(self) -> AsyncIterator[bytes]:
        async for chunk in self._source:
            self._validate(chunk)
            yield chunk

    async def aclose(self) -> None:
        await self._source.aclose()

    def _validate(self, chunk: bytes) -> None:
        for value in chunk:
            if value == 10:  # LF, including the LF half of CRLF.
                if self._previous_cr:
                    self._previous_cr = False
                    continue
                self._finish_line()
                continue
            if value == 13:  # CR is also a complete SSE line ending.
                self._finish_line()
                self._previous_cr = True
                continue
            self._previous_cr = False
            self._line_size += 1
            self._event_size += 1
            if self._line_size > _MAX_LINE_BYTES or self._event_size > _MAX_EVENT_BYTES:
                raise PocketBaseTransportError(
                    "PocketBase realtime event exceeded the safe size limit",
                    code="realtime.event_too_large",
                )

    def _finish_line(self) -> None:
        if self._line_size == 0:
            self._event_size = 0
        self._line_size = 0


class _HttpxSSEConnection:
    def __init__(
        self,
        client: httpx.AsyncClient,
        manager: AbstractAsyncContextManager[EventSource],
        events: AsyncIterator[ServerSentEvent],
    ) -> None:
        self._client = client
        self._manager = manager
        self._events = events
        self._closed = False

    async def receive(self) -> ServerSentEvent:
        try:
            return await anext(self._events)
        except StopAsyncIteration:
            raise PocketBaseTransportError(
                "PocketBase realtime stream closed",
                code="realtime.disconnected",
            ) from None
        except SSEError:
            raise PocketBaseTransportError(
                "PocketBase returned an invalid realtime response",
                code="realtime.invalid_response",
            ) from None
        except httpx.TransportError:
            raise PocketBaseTransportError(
                "PocketBase realtime stream disconnected",
                code="realtime.disconnected",
            ) from None

    async def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        try:
            await self._manager.__aexit__(None, None, None)
        finally:
            await self._client.aclose()


class PocketBaseRealtimeSession:
    def __init__(self, connection: SSEConnection) -> None:
        self._connection = connection

    async def receive(self) -> ProductEvent | None:
        try:
            event = await self._connection.receive()
        except PocketBaseTransportError:
            raise
        except (OSError, TimeoutError):
            raise PocketBaseTransportError(
                "PocketBase realtime stream disconnected",
                code="realtime.disconnected",
            ) from None
        if not event.data:
            return None
        encoded_fields = (event.id.encode(), event.event.encode())
        data_lines = event.data.split("\n")
        if (
            any(len(value) > _MAX_LINE_BYTES for value in encoded_fields)
            or any(len(line.encode()) > _MAX_LINE_BYTES for line in data_lines)
            or len(event.data.encode()) > _MAX_EVENT_BYTES
        ):
            raise PocketBaseTransportError(
                "PocketBase realtime event exceeded the safe size limit",
                code="realtime.event_too_large",
            )
        return _decode_event(event.id, event.event, event.data.encode())

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
                    # A sidecar restart can reset the SSE socket while the
                    # supervisor is already reconnecting or shutting down.
                    # Cleanup failure must not terminate the backend.
                    with suppress(OSError, TimeoutError, PocketBaseTransportError):
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
        payload.get("contractVersion") != "2.0"
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
            any(
                not isinstance(payload.get(name), str) or not payload[name]
                for name in required_text
            )
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


def _invalid_event() -> PocketBaseTransportError:
    return PocketBaseTransportError(
        "PocketBase returned an invalid realtime event",
        code="realtime.invalid_event",
    )


def _http_code(raw: bytes) -> str:
    try:
        payload = json.loads(raw[: 1 << 20])
    except (UnicodeDecodeError, json.JSONDecodeError):
        return "realtime.subscribe_failed"
    if isinstance(payload, dict) and isinstance(payload.get("code"), str):
        return payload["code"]
    return "realtime.subscribe_failed"


async def _read_error_body(response: httpx.Response) -> bytes:
    body = bytearray()
    async for chunk in response.aiter_bytes():
        body.extend(chunk)
        if len(body) > 1 << 20:
            return b""
    return bytes(body)


__all__ = [
    "EVENTS_PATH",
    "PocketBaseRealtimeSession",
    "PocketBaseRealtimeSupervisor",
    "ProductEvent",
    "SSEConnection",
    "SSEConnector",
    "StdlibSSEConnector",
]
