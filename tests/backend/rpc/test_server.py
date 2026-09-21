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
from tests.backend.product_composition_fixture import product_backend as product_backend


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


@pytest.mark.asyncio
async def test_host_file_reply_unblocks_current_handler_without_reordering_requests() -> None:
    dispatcher = RpcDispatcher()
    reader = asyncio.StreamReader()
    frames: asyncio.Queue[dict] = asyncio.Queue()
    order: list[str] = []

    class InteractiveWriter(BufferWriter):
        def write(self, data: bytes) -> None:
            super().write(data)
            frames.put_nowait(json.loads(data))

    writer = InteractiveWriter()
    server = RpcServer(reader, writer, dispatcher)

    async def first(_params: HandshakeParams) -> dict:
        order.append("first-start")
        result = await server.call_host_file("describe", {"grantId": "opaque"})
        order.append("first-end")
        return result

    async def second(_params: HandshakeParams) -> dict:
        order.append("second")
        return {"ok": True}

    dispatcher.register("first", first, HandshakeParams)
    dispatcher.register("second", second, HandshakeParams)
    serving = asyncio.create_task(server.serve())
    params = {"clientVersion": "0.1.0", "protocolVersion": "1.0"}
    try:
        for method in ("first", "second"):
            reader.feed_data(
                json.dumps(
                    {"jsonrpc": "2.0", "id": method, "method": method, "params": params}
                ).encode()
                + b"\n"
            )
        callback = await asyncio.wait_for(frames.get(), 2)
        assert callback["method"] == "host.file.describe"
        assert order == ["first-start"]
        reader.feed_data(
            json.dumps(
                {"jsonrpc": "2.0", "id": callback["id"], "result": {"grantId": "opaque"}}
            ).encode()
            + b"\n"
        )
        responses = [await asyncio.wait_for(frames.get(), 2) for _ in range(2)]
        assert [response["id"] for response in responses] == ["first", "second"]
        assert responses[0]["result"] == {"grantId": "opaque"}
        assert order == ["first-start", "first-end", "second"]
    finally:
        reader.feed_eof()
        await asyncio.wait_for(serving, 2)


@pytest.mark.asyncio
@pytest.mark.parametrize("action", ["describe", "finishWrite"])
async def test_eof_fails_pending_host_file_call(action) -> None:
    server, reader, writer = _make_server()
    serving = asyncio.create_task(server.serve())
    params = {"grantId": "opaque"} if action == "describe" else {"transferId": "t", "commit": True}
    call = asyncio.create_task(server.call_host_file(action, params))
    await asyncio.sleep(0)
    assert _frames(writer.data)[0]["method"] == f"host.file.{action}"
    reader.feed_eof()
    with pytest.raises(RuntimeError, match="channel closed"):
        await asyncio.wait_for(call, 2)
    await asyncio.wait_for(serving, 2)
    with pytest.raises(RuntimeError, match="unavailable"):
        await server.call_host_file("describe", {"grantId": "opaque"})


@pytest.mark.asyncio
async def test_host_file_channel_rejects_unknown_operation_without_writing() -> None:
    server, _reader, writer = _make_server()
    with pytest.raises(ValueError, match="unknown"):
        await server.call_host_file("executeShell", {"path": "forbidden"})
    assert writer.data == b""


@pytest.mark.asyncio
async def test_response_write_failure_stops_reader_without_waiting_for_eof() -> None:
    class BrokenWriter(BufferWriter):
        async def drain(self) -> None:
            raise OSError("closed output")

    dispatcher = RpcDispatcher()
    dispatcher.register("system.handshake", SystemService().handshake, HandshakeParams)
    reader = asyncio.StreamReader()
    server = RpcServer(reader, BrokenWriter(), dispatcher)
    reader.feed_data(
        b'{"jsonrpc":"2.0","id":"a","method":"system.handshake","params":{"clientVersion":"1","protocolVersion":"1.0"}}\n'
    )
    with pytest.raises(OSError, match="closed output"):
        await asyncio.wait_for(server.serve(), 2)
    with pytest.raises(RuntimeError, match="unavailable"):
        await server.call_host_file("describe", {"grantId": "opaque"})


@pytest.mark.asyncio
async def test_ordinary_rpc_overload_terminates_instead_of_blocking_callback_read_pump() -> None:
    server, reader, _writer = _make_server()
    frame = b'{"jsonrpc":"2.0","id":"a","method":"system.handshake","params":{"clientVersion":"1","protocolVersion":"1.0"}}\n'
    reader.feed_data(frame * 66)
    with pytest.raises(asyncio.QueueFull):
        await asyncio.wait_for(server.serve(), 2)


@pytest.mark.parametrize(
    ("native_code", "kind", "expected_code"),
    [
        (-32050, "path_grant_error", -32050),
        (-32098, "path_grant_error", -32603),
        (-32050, "unknown", -32603),
    ],
)
async def test_real_task_composition_preserves_only_closed_host_grant_errors(
    product_backend, native_code, kind, expected_code
):
    server = product_backend.server
    reader = asyncio.StreamReader()
    frames = asyncio.Queue()

    class InteractiveWriter(BufferWriter):
        def write(self, data):
            super().write(data)
            frames.put_nowait(json.loads(data))

    server._reader = reader
    server._writer = InteractiveWriter()
    serving = asyncio.create_task(server.serve())
    try:
        reader.feed_data(
            json.dumps(
                {
                    "jsonrpc": "2.0",
                    "id": "export-create",
                    "method": "task.create",
                    "params": {"kind": "data.export", "params": {"grantId": "expired"}},
                }
            ).encode()
            + b"\n"
        )
        callback = await asyncio.wait_for(frames.get(), 2)
        assert callback["method"] == "host.file.describe"
        assert callback["params"] == {"grantId": "expired"}
        reader.feed_data(
            json.dumps(
                {
                    "jsonrpc": "2.0",
                    "id": callback["id"],
                    "error": {
                        "code": native_code,
                        "message": "private-path-marker",
                        "data": {"kind": kind, "message": "private-path-marker"},
                    },
                }
            ).encode()
            + b"\n"
        )
        response = await asyncio.wait_for(frames.get(), 2)
        assert response["id"] == "export-create"
        assert response["error"]["code"] == expected_code
        if expected_code == -32050:
            assert response["error"]["message"] == "Path grant error"
            assert response["error"]["data"]["kind"] == "path_grant_error"
        assert "private-path-marker" not in json.dumps(response)
        assert product_backend.transport.requests == []
    finally:
        reader.feed_eof()
        await asyncio.wait_for(serving, 2)


async def test_native_commit_outlives_callback_deadline_without_a_false_failed_task(monkeypatch):
    import base64

    from backend.application.export_service import ExportService
    from backend.application.host_files import HostFiles
    from backend.application.task_runtime import TaskRuntime
    from backend.contracts.data_io import ExportParams
    from tests.backend.application.test_export_service import FakeQueryPort, _manifest

    reader = asyncio.StreamReader()
    committing = asyncio.Event()
    received = bytearray()
    commit_id = None
    last_method = None
    output = b"previous complete file"
    original_wait_for = asyncio.wait_for

    async def expire_callback_deadline(awaitable, timeout):
        if timeout == 30 and last_method == "host.file.finishWrite":
            # Deterministic deadline injection: the native operation has been
            # submitted but has not committed or returned its receipt yet.
            raise TimeoutError("callback deadline elapsed")
        return await original_wait_for(awaitable, timeout)

    monkeypatch.setattr(asyncio, "wait_for", expire_callback_deadline)

    class HostPeer(BufferWriter):
        def write(self, data):
            nonlocal commit_id, last_method
            frame = json.loads(data)
            last_method = frame["method"]
            if last_method == "host.file.openWrite":
                result = {"transferId": "t", "displayName": "out.csv"}
            elif last_method == "host.file.write":
                received.extend(base64.b64decode(frame["params"]["base64"]))
                result = {"offset": len(received)}
            else:
                assert last_method == "host.file.finishWrite"
                assert frame["params"]["commit"] is True
                commit_id = frame["id"]
                committing.set()
                return
            reader.feed_data(
                json.dumps(
                    {
                        "jsonrpc": "2.0",
                        "id": frame["id"],
                        "result": result,
                    }
                ).encode()
                + b"\n"
            )

    server = RpcServer(reader, HostPeer(), RpcDispatcher())
    writer = ExportService(
        query_port=FakeQueryPort([[{"title": "complete"}]]),
        profiles=_manifest(),
        files=HostFiles(server.call_host_file),
    )
    runtime = TaskRuntime()

    async def export(_task_id, _reporter, token):
        return await writer.export(
            ExportParams.model_validate(
                {
                    "collection": "vibetable_demo",
                    "query": {},
                    "format": "csv",
                    "grantId": "g",
                }
            ),
            cancelled=lambda: token.cancelled,
        )

    runtime.register("data.export", export)
    serving = asyncio.create_task(server.serve())
    created = await runtime.create("data.export", {})
    try:
        await asyncio.wait_for(committing.wait(), 2)
        # Let the worker observe an expired deadline if one was applied.
        await asyncio.sleep(0)
        assert runtime.status(created.task_id).state == "running"
        assert output == b"previous complete file"
        output = bytes(received)
        reader.feed_data(
            json.dumps(
                {
                    "jsonrpc": "2.0",
                    "id": commit_id,
                    "result": {"committed": True, "bytes": len(received)},
                }
            ).encode()
            + b"\n"
        )
        result = await asyncio.wait_for(runtime.wait(created.task_id), 2)
        assert result.state == "succeeded"
        assert b"complete" in output
    finally:
        reader.feed_eof()
        await asyncio.wait_for(serving, 2)
        await asyncio.wait_for(runtime.wait(created.task_id), 2)
