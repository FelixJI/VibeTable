import asyncio
import json

import pytest

from backend.rpc.framing import FrameTooLargeError, read_frame, write_frame


@pytest.mark.asyncio
async def test_round_trip_frame() -> None:
    reader = asyncio.StreamReader()
    reader.feed_data(b'{"jsonrpc":"2.0","id":"1","method":"system.handshake"}\n')
    reader.feed_eof()
    assert await read_frame(reader) == {
        "jsonrpc": "2.0",
        "id": "1",
        "method": "system.handshake",
    }


@pytest.mark.asyncio
async def test_rejects_frame_over_limit() -> None:
    reader = asyncio.StreamReader()
    reader.feed_data((json.dumps({"value": "x" * 40}) + "\n").encode())
    reader.feed_eof()
    with pytest.raises(FrameTooLargeError):
        await read_frame(reader, max_bytes=32)


class BufferWriter:
    def __init__(self) -> None:
        self.data = b""

    def write(self, data: bytes) -> None:
        self.data += data

    async def drain(self) -> None:
        return None


@pytest.mark.asyncio
async def test_write_frame_is_compact_json_line() -> None:
    writer = BufferWriter()
    await write_frame(writer, {"ok": True})
    assert writer.data == b'{"ok":true}\n'
