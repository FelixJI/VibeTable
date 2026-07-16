"""Tests for ``backend.rpc.server.RpcServer``.

The server wraps the framing primitives + dispatcher and adds the
notification channel (JSON-RPC 2.0 object *without* an ``id``).

Behaviors pinned here:

* ``notify()`` serializes a notification with no ``id`` key
* ``notify()`` enforces the 4 MiB frame cap (``FrameTooLargeError``)
* ``serve()`` dispatches each incoming request frame and writes exactly one
  response line per request (no stray output, no dropped frames)
* ``serve()`` and ``notify()`` can interleave safely (concurrent writes)
* ``serve()`` never writes anything to ``sys.stdout`` outside of frames
"""

from __future__ import annotations

import asyncio
import json

import pytest

from backend.application.system_service import SystemService
from backend.contracts.system import HandshakeParams
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.framing import MAX_FRAME_BYTES, FrameTooLargeError
from backend.rpc.server import RpcServer


class BufferWriter:
    """In-memory AsyncWriter used to assert on the bytes a server emits."""

    def __init__(self) -> None:
        self.data: bytearray = bytearray()
        self._drain_calls = 0

    def write(self, data: bytes) -> None:
        self.data.extend(data)

    async def drain(self) -> None:
        self._drain_calls += 1


def _make_server() -> tuple[RpcServer, asyncio.StreamReader, BufferWriter]:
    dispatcher = RpcDispatcher()
    service = SystemService()
    dispatcher.register("system.handshake", service.handshake, HandshakeParams)
    reader = asyncio.StreamReader()
    writer = BufferWriter()
    return RpcServer(reader, writer, dispatcher), reader, writer


def _frames(buf: bytearray | bytes) -> list[dict]:
    return [json.loads(line) for line in bytes(buf).splitlines() if line]


@pytest.mark.asyncio
async def test_notify_writes_object_without_id() -> None:
    server, _reader, writer = _make_server()
    await server.notify("system.handshake", {"clientVersion": "0.1.0", "protocolVersion": "1.0"})

    frames = _frames(writer.data)
    assert len(frames) == 1
    payload = frames[0]
    # A JSON-RPC notification must carry jsonrpc + method + params but NO id.
    assert payload["jsonrpc"] == "2.0"
    assert payload["method"] == "system.handshake"
    assert payload["params"] == {"clientVersion": "0.1.0", "protocolVersion": "1.0"}
    assert "id" not in payload


@pytest.mark.asyncio
async def test_notify_enforces_4mib_cap() -> None:
    server, _reader, _writer = _make_server()
    # A payload whose encoded size exceeds the 4 MiB cap must be rejected.
    oversized = {"x": "a" * (MAX_FRAME_BYTES + 16)}
    with pytest.raises(FrameTooLargeError):
        await server.notify("huge", oversized)


@pytest.mark.asyncio
async def test_serve_dispatches_requests_and_writes_responses() -> None:
    server, reader, writer = _make_server()
    request_line = (
        b'{"jsonrpc":"2.0","id":"h1","method":"system.handshake",'
        b'"params":{"clientVersion":"0.1.0","protocolVersion":"1.0"}}\n'
    )
    reader.feed_data(request_line)
    reader.feed_eof()

    await server.serve()

    frames = _frames(writer.data)
    assert len(frames) == 1
    assert frames[0]["id"] == "h1"
    assert frames[0]["result"]["protocolVersion"] == "1.0"
    assert frames[0]["result"]["capabilities"] == ["system.handshake"]
    assert "error" not in frames[0]


@pytest.mark.asyncio
async def test_serve_skips_notifications() -> None:
    """A notification (no id) is dispatched but produces no response frame."""
    server, reader, writer = _make_server()
    notification_line = (
        b'{"jsonrpc":"2.0","method":"system.handshake",'
        b'"params":{"clientVersion":"0.1.0","protocolVersion":"1.0"}}\n'
    )
    reader.feed_data(notification_line)
    reader.feed_eof()

    await server.serve()

    assert bytes(writer.data) == b""


@pytest.mark.asyncio
async def test_serve_can_run_concurrently_with_notify(capfd: pytest.CaptureFixture) -> None:
    """Concurrent response + notification writes must not corrupt the stream.

    Also asserts ``serve()`` never writes anything to ``sys.stdout``/``stderr``
    outside of the framed protocol output on the writer.
    """
    server, reader, writer = _make_server()
    request_line = (
        b'{"jsonrpc":"2.0","id":"h1","method":"system.handshake",'
        b'"params":{"clientVersion":"0.1.0","protocolVersion":"1.0"}}\n'
    )
    reader.feed_data(request_line)
    reader.feed_eof()

    notify_task = asyncio.create_task(server.notify("progress", {"done": 1, "total": 2}))

    await server.serve()
    await notify_task

    frames = _frames(writer.data)
    # Two frames total: one response (id=h1) and one notification (no id),
    # in a valid order, each on its own line.
    assert len(frames) == 2
    response = next(f for f in frames if "id" in f)
    notification = next(f for f in frames if "id" not in f)
    assert response["id"] == "h1"
    assert notification["method"] == "progress"
    assert notification["params"] == {"done": 1, "total": 2}

    # serve()/notify() must never leak diagnostics to stdout — only the framed
    # protocol output on the writer. Capfd catches any stray print().
    captured = capfd.readouterr()
    assert captured.out == ""
