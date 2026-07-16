import asyncio
import json
from typing import Any, Protocol

MAX_FRAME_BYTES = 4 * 1024 * 1024


class AsyncWriter(Protocol):
    def write(self, data: bytes) -> None: ...
    async def drain(self) -> None: ...


class FrameTooLargeError(ValueError):
    pass


async def read_frame(reader, max_bytes: int = MAX_FRAME_BYTES) -> dict[str, Any] | None:
    try:
        raw = await reader.readuntil(b"\n")
    except asyncio.IncompleteReadError as exc:
        raw = exc.partial
    if not raw:
        return None
    if len(raw) > max_bytes:
        raise FrameTooLargeError(f"RPC frame exceeds {max_bytes} bytes")
    value = json.loads(raw.decode("utf-8"))
    if not isinstance(value, dict):
        raise ValueError("RPC frame must be a JSON object")
    return value


async def write_frame(writer: AsyncWriter, payload: dict[str, Any]) -> None:
    raw = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8") + b"\n"
    if len(raw) > MAX_FRAME_BYTES:
        raise FrameTooLargeError(f"RPC frame exceeds {MAX_FRAME_BYTES} bytes")
    writer.write(raw)
    await writer.drain()
