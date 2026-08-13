from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator

import httpx
import pytest
from httpx_sse import ServerSentEvent

from backend.adapters.pocketbase.realtime import (
    PocketBaseRealtimeSession,
    PocketBaseRealtimeSupervisor,
    ProductEvent,
    SSEConnection,
    StdlibSSEConnector,
)
from backend.adapters.pocketbase.transport import (
    PocketBaseConfig,
    PocketBaseTransportError,
)

SECRET = "a" * 64


class AsyncContent(httpx.AsyncByteStream):
    def __init__(self, content: bytes) -> None:
        self._content = content

    async def __aiter__(self) -> AsyncIterator[bytes]:
        yield self._content


def _payload(event_id: str, sequence: int = 1) -> dict[str, object]:
    return {
        "contractVersion": "2.0",
        "topic": "data.changed",
        "eventId": event_id,
        "sequence": sequence,
        "occurredAt": "2026-07-24T00:00:00Z",
        "schemaRevision": "schema_0001",
        "dataRevision": "data_0001",
        "changeSetId": "change-1",
        "tableId": "orders",
        "recordIds": ["row-1"],
        "operation": "update",
    }


def _event(event_id: str, sequence: int = 1) -> ServerSentEvent:
    payload = _payload(event_id, sequence)
    return ServerSentEvent(
        id=event_id,
        event="data.changed",
        data=json.dumps(payload, separators=(",", ":")),
    )


def _event_bytes(event_id: str, sequence: int = 1) -> bytes:
    payload = {
        **_payload(event_id, sequence),
    }
    return (
        f"id: {event_id}\n"
        "event: data.changed\n"
        f"data: {json.dumps(payload, separators=(',', ':'))}\n\n"
    ).encode()


def _durable_event(cursor: int, event_id: str, sequence: int = 1) -> ServerSentEvent:
    return ServerSentEvent(
        id=f"rt:{cursor}",
        event="data.changed",
        data=json.dumps(_payload(event_id, sequence), separators=(",", ":")),
    )


class FakeConnection:
    def __init__(self, *events: ServerSentEvent) -> None:
        self._events = iter(events)
        self.closed = False

    async def receive(self) -> ServerSentEvent:
        try:
            return next(self._events)
        except StopIteration:
            raise PocketBaseTransportError(
                "closed",
                code="realtime.disconnected",
            ) from None

    async def close(self) -> None:
        self.closed = True


class FakeConnector:
    def __init__(self, outcomes: list[SSEConnection | Exception]) -> None:
        self._outcomes = list(outcomes)
        self.cursors: list[str | None] = []

    async def connect(self, after_event_id: str | None) -> SSEConnection:
        self.cursors.append(after_event_id)
        outcome = self._outcomes.pop(0)
        if isinstance(outcome, Exception):
            raise outcome
        return outcome


class ResetConnection:
    async def receive(self) -> ServerSentEvent:
        raise ConnectionResetError("sidecar restarted")

    async def close(self) -> None:
        raise ConnectionResetError("socket already reset")


@pytest.mark.asyncio
async def test_session_decodes_product_event_and_ignores_heartbeat() -> None:
    connection = FakeConnection(ServerSentEvent(), _event("evt-1"))
    session = PocketBaseRealtimeSession(connection)

    assert await session.receive() is None
    event = await session.receive()

    assert event is not None
    assert event == ProductEvent(
        event_id="evt-1",
        topic="data.changed",
        sequence=1,
        payload=event.payload,
    )
    assert event.payload["tableId"] == "orders"


@pytest.mark.asyncio
async def test_session_accepts_cr_only_sse_line_endings() -> None:
    captured: httpx.Request | None = None

    async def handler(request: httpx.Request) -> httpx.Response:
        nonlocal captured
        captured = request
        raw = _event_bytes("evt-cr").replace(b"\n", b"\r")
        return httpx.Response(
            200,
            headers={"Content-Type": "text/event-stream; charset=utf-8"},
            content=raw,
        )

    connector = StdlibSSEConnector(
        PocketBaseConfig("http://127.0.0.1:8090", SECRET),
        http_transport=httpx.MockTransport(handler),
    )
    connection = await connector.connect("rt:41")
    try:
        event = await PocketBaseRealtimeSession(connection).receive()
    finally:
        await connection.close()

    assert event is not None
    assert event.event_id == "evt-cr"
    assert captured is not None
    assert captured.url.params["after"] == "rt:41"
    assert captured.headers["Last-Event-ID"] == "rt:41"


@pytest.mark.asyncio
async def test_connector_maps_product_and_invalid_stream_responses() -> None:
    async def product_error(_: httpx.Request) -> httpx.Response:
        return httpx.Response(409, json={"code": "realtime.cursor_expired"})

    connector = StdlibSSEConnector(
        PocketBaseConfig("http://127.0.0.1:8090", SECRET),
        http_transport=httpx.MockTransport(product_error),
    )
    with pytest.raises(PocketBaseTransportError) as product:
        await connector.connect("rt:1")
    assert product.value.code == "realtime.cursor_expired"

    async def invalid_content_type(_: httpx.Request) -> httpx.Response:
        return httpx.Response(200, headers={"Content-Type": "application/json"})

    connector = StdlibSSEConnector(
        PocketBaseConfig("http://127.0.0.1:8090", SECRET),
        http_transport=httpx.MockTransport(invalid_content_type),
    )
    with pytest.raises(PocketBaseTransportError) as invalid:
        await connector.connect(None)
    assert invalid.value.code == "realtime.invalid_response"


@pytest.mark.asyncio
async def test_connector_maps_network_failure_before_stream_opens() -> None:
    async def unavailable(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("offline", request=request)

    connector = StdlibSSEConnector(
        PocketBaseConfig("http://127.0.0.1:8090", SECRET),
        http_transport=httpx.MockTransport(unavailable),
    )
    with pytest.raises(PocketBaseTransportError) as caught:
        await connector.connect(None)
    assert caught.value.code == "sidecar.unavailable"


@pytest.mark.asyncio
async def test_connector_bounds_lines_before_sse_decoder_buffers_them(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr("backend.adapters.pocketbase.realtime._MAX_LINE_BYTES", 8)

    async def oversized(_: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            headers={"Content-Type": "text/event-stream"},
            stream=AsyncContent(b"data: 123456789\n\n"),
        )

    connector = StdlibSSEConnector(
        PocketBaseConfig("http://127.0.0.1:8090", SECRET),
        http_transport=httpx.MockTransport(oversized),
    )
    connection = await connector.connect(None)
    try:
        with pytest.raises(PocketBaseTransportError) as caught:
            await connection.receive()
    finally:
        await connection.close()
    assert caught.value.code == "realtime.event_too_large"


@pytest.mark.asyncio
async def test_session_rejects_mismatched_event_identity() -> None:
    raw = _event("evt-1")
    raw = ServerSentEvent(
        id=raw.id,
        event=raw.event,
        data=raw.data.replace('"eventId":"evt-1"', '"eventId":"evt-other"'),
    )
    session = PocketBaseRealtimeSession(FakeConnection(raw))

    with pytest.raises(PocketBaseTransportError, match="invalid realtime event") as error:
        await session.receive()

    assert error.value.code == "realtime.invalid_event"


@pytest.mark.asyncio
async def test_session_keeps_durable_cursor_separate_from_product_event_id() -> None:
    session = PocketBaseRealtimeSession(FakeConnection(_durable_event(42, "evt-product")))

    event = await session.receive()

    assert event is not None
    assert event.event_id == "evt-product"
    assert event.cursor == "rt:42"


@pytest.mark.asyncio
async def test_supervisor_resumes_cursor_and_deduplicates_replay() -> None:
    stop = asyncio.Event()
    connector = FakeConnector(
        [
            FakeConnection(_event("evt-1")),
            FakeConnection(_event("evt-1"), _event("evt-2", 2)),
        ]
    )
    emitted: list[str] = []

    async def emit(event: ProductEvent) -> None:
        emitted.append(event.event_id)
        if event.event_id == "evt-2":
            stop.set()

    async def no_wait(_: float) -> None:
        return None

    supervisor = PocketBaseRealtimeSupervisor(connector, sleep=no_wait)
    await supervisor.run(emit, stop)

    assert emitted == ["evt-1", "evt-2"]
    assert connector.cursors == [None, "evt-1"]
    assert supervisor.last_event_id == "evt-2"


@pytest.mark.asyncio
async def test_supervisor_reconnects_with_durable_rowid_cursor() -> None:
    stop = asyncio.Event()
    connector = FakeConnector(
        [
            FakeConnection(_durable_event(41, "evt-1")),
            FakeConnection(
                _durable_event(41, "evt-1"),
                _durable_event(42, "evt-2", 2),
            ),
        ]
    )
    emitted: list[str] = []

    async def emit(event: ProductEvent) -> None:
        emitted.append(event.event_id)
        if event.event_id == "evt-2":
            stop.set()

    async def no_wait(_: float) -> None:
        return None

    supervisor = PocketBaseRealtimeSupervisor(connector, sleep=no_wait)
    await supervisor.run(emit, stop)

    assert emitted == ["evt-1", "evt-2"]
    assert connector.cursors == [None, "rt:41"]
    assert supervisor.last_event_id == "rt:42"


@pytest.mark.asyncio
async def test_supervisor_reconnects_when_sidecar_resets_stream_and_close() -> None:
    stop = asyncio.Event()
    connector = FakeConnector(
        [
            ResetConnection(),
            FakeConnection(_event("evt-after-restart")),
        ]
    )
    emitted: list[str] = []

    async def emit(event: ProductEvent) -> None:
        emitted.append(event.event_id)
        stop.set()

    async def no_wait(_: float) -> None:
        return None

    supervisor = PocketBaseRealtimeSupervisor(connector, sleep=no_wait)
    await supervisor.run(emit, stop)

    assert emitted == ["evt-after-restart"]
    assert connector.cursors == [None, None]


@pytest.mark.asyncio
async def test_supervisor_reconciles_expired_cursor_before_fresh_subscription() -> None:
    stop = asyncio.Event()
    connector = FakeConnector(
        [
            PocketBaseTransportError(
                "expired",
                code="realtime.cursor_expired",
            ),
            FakeConnection(_event("evt-fresh")),
        ]
    )
    reconciled = 0

    async def reconcile() -> None:
        nonlocal reconciled
        reconciled += 1

    async def emit(_: ProductEvent) -> None:
        stop.set()

    async def no_wait(_: float) -> None:
        return None

    supervisor = PocketBaseRealtimeSupervisor(
        connector,
        sleep=no_wait,
        reconcile_cursor_gap=reconcile,
    )
    supervisor._last_event_id = "evt-old"
    await supervisor.run(emit, stop)

    assert reconciled == 1
    assert connector.cursors == ["evt-old", None]


@pytest.mark.asyncio
async def test_supervisor_reconciles_legacy_catchup_limit_before_fresh_subscription() -> None:
    connector = FakeConnector(
        [
            PocketBaseTransportError(
                "legacy cursor is outside retained history",
                code="realtime.catchup_limit",
            ),
            FakeConnection(_event("evt-new")),
        ]
    )
    reconciled = 0

    async def reconcile() -> None:
        nonlocal reconciled
        reconciled += 1

    emitted: list[str] = []
    stop = asyncio.Event()

    async def emit(event: ProductEvent) -> None:
        emitted.append(event.event_id)
        stop.set()

    async def no_wait(_: float) -> None:
        return None

    supervisor = PocketBaseRealtimeSupervisor(
        connector,
        reconcile_cursor_gap=reconcile,
        sleep=no_wait,
    )
    supervisor._last_event_id = "legacy-event-id"
    await supervisor.run(emit, stop)

    assert reconciled == 1
    assert connector.cursors == ["legacy-event-id", None]
    assert emitted == ["evt-new"]


@pytest.mark.parametrize(
    "event",
    [
        ServerSentEvent(id="evt-1", event="unknown", data="{}"),
        ServerSentEvent(id="evt-1", event="data.changed", data="not-json"),
        ServerSentEvent(id="evt-1", event="data.changed", data="{}"),
    ],
)
@pytest.mark.asyncio
async def test_session_rejects_invalid_event_shapes(event: ServerSentEvent) -> None:
    with pytest.raises(PocketBaseTransportError):
        await PocketBaseRealtimeSession(FakeConnection(event)).receive()
