from __future__ import annotations

import asyncio
import json

import pytest

from backend.adapters.pocketbase.realtime import (
    PocketBaseRealtimeSession,
    PocketBaseRealtimeSupervisor,
    ProductEvent,
)
from backend.adapters.pocketbase.transport import PocketBaseTransportError


def _event(event_id: str, sequence: int = 1) -> bytes:
    payload = {
        "contractVersion": "1.0",
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
    return (
        f"id: {event_id}\n"
        "event: data.changed\n"
        f"data: {json.dumps(payload, separators=(',', ':'))}\n\n"
    ).encode()


def _durable_event(cursor: int, event_id: str, sequence: int = 1) -> bytes:
    return _event(event_id, sequence).replace(
        f"id: {event_id}\n".encode(),
        f"id: rt:{cursor}\n".encode(),
        1,
    )


class FakeConnection:
    def __init__(self, raw: bytes) -> None:
        self._lines = iter(raw.splitlines(keepends=True))
        self.closed = False

    async def readline(self, limit: int) -> bytes:
        try:
            return next(self._lines)
        except StopIteration:
            return b""

    async def close(self) -> None:
        self.closed = True


class FakeConnector:
    def __init__(self, outcomes: list[FakeConnection | Exception]) -> None:
        self._outcomes = list(outcomes)
        self.cursors: list[str | None] = []

    async def connect(self, after_event_id: str | None) -> FakeConnection:
        self.cursors.append(after_event_id)
        outcome = self._outcomes.pop(0)
        if isinstance(outcome, Exception):
            raise outcome
        return outcome


class ResetConnection:
    async def readline(self, limit: int) -> bytes:
        raise ConnectionResetError("sidecar restarted")

    async def close(self) -> None:
        raise ConnectionResetError("socket already reset")


@pytest.mark.asyncio
async def test_session_decodes_product_event_and_ignores_heartbeat() -> None:
    connection = FakeConnection(b": heartbeat\n\n" + _event("evt-1"))
    session = PocketBaseRealtimeSession(connection)

    assert await session.receive() is None
    event = await session.receive()

    assert event == ProductEvent(
        event_id="evt-1",
        topic="data.changed",
        sequence=1,
        payload=event.payload,
    )
    assert event.payload["tableId"] == "orders"


@pytest.mark.asyncio
async def test_session_rejects_mismatched_event_identity() -> None:
    raw = _event("evt-1").replace(b'"eventId":"evt-1"', b'"eventId":"evt-other"')
    session = PocketBaseRealtimeSession(FakeConnection(raw))

    with pytest.raises(PocketBaseTransportError, match="invalid realtime event") as error:
        await session.receive()

    assert error.value.code == "realtime.invalid_event"


@pytest.mark.asyncio
async def test_session_keeps_durable_cursor_separate_from_product_event_id() -> None:
    session = PocketBaseRealtimeSession(FakeConnection(_durable_event(42, "evt-product")))

    event = await session.receive()

    assert event.event_id == "evt-product"
    assert event.cursor == "rt:42"


@pytest.mark.asyncio
async def test_supervisor_resumes_cursor_and_deduplicates_replay() -> None:
    stop = asyncio.Event()
    connector = FakeConnector(
        [
            FakeConnection(_event("evt-1")),
            FakeConnection(_event("evt-1") + _event("evt-2", 2)),
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
            FakeConnection(_durable_event(41, "evt-1") + _durable_event(42, "evt-2", 2)),
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
    "raw",
    [
        b"id: evt-1\nevent: unknown\ndata: {}\n\n",
        b"id: evt-1\nevent: data.changed\ndata: not-json\n\n",
        b"id: evt-1\nevent: data.changed\ndata: {}\n\n",
    ],
)
@pytest.mark.asyncio
async def test_session_rejects_invalid_event_shapes(raw: bytes) -> None:
    with pytest.raises(PocketBaseTransportError):
        await PocketBaseRealtimeSession(FakeConnection(raw)).receive()
