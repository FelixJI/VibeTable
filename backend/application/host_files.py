"""Byte-only access to the native file broker of this worker's owning Host."""

from __future__ import annotations

import asyncio
import base64
import logging
from collections.abc import AsyncIterator, Awaitable, Buffer, Callable, Iterator
from contextlib import asynccontextmanager, contextmanager, suppress
from dataclasses import dataclass
from io import RawIOBase, TextIOWrapper
from tempfile import SpooledTemporaryFile
from typing import BinaryIO, cast

from backend.application.file_work import Progress
from backend.contracts.product_rpc import JsonObject
from backend.contracts.task import SessionPathGrant

HostFileCall = Callable[[str, JsonObject], Awaitable[JsonObject]]
logger = logging.getLogger(__name__)
BLOCK_BYTES = 256 * 1024


class FileCommitUnknownError(RuntimeError):
    code = "export_outcome_unknown"


@dataclass
class FileBuffer:
    stream: BinaryIO
    display_name: str


@contextmanager
def text_stream(stream: BinaryIO) -> Iterator[TextIOWrapper]:
    wrapper = TextIOWrapper(stream, encoding="utf-8-sig", newline="")
    try:
        yield wrapper
    finally:
        wrapper.detach()


class HostFiles:
    def __init__(self, call: HostFileCall, maximum_bytes: int | None = None) -> None:
        self._call = call
        self._maximum_bytes = maximum_bytes

    def bound_run(self, run_id: str, maximum_bytes: int = 32 * 1024 * 1024) -> HostFiles:
        """Host-selected plugin DataIO run; never convert it to a product grant."""

        async def call(action: str, params: JsonObject) -> JsonObject:
            if action in {"describe", "openRead", "openWrite"}:
                params = {**params, "runId": run_id}
            return await self._call(action, params)

        return HostFiles(call, maximum_bytes)

    async def describe(self, grant_id: str) -> SessionPathGrant:
        return SessionPathGrant.model_validate(await self._call("describe", {"grantId": grant_id}))

    @asynccontextmanager
    async def read(
        self,
        grant_id: str,
        *,
        run_id: str | None = None,
        cancelled: Callable[[], bool] | None = None,
        progress: Progress | None = None,
    ) -> AsyncIterator[FileBuffer]:
        opened = await self._call("openRead", self._grant_params(grant_id, run_id))
        transfer_id = _string(opened, "transferId")
        with SpooledTemporaryFile(max_size=8 * 1024 * 1024, mode="w+b") as spool:
            try:
                read_bytes = 0
                while True:
                    if cancelled and cancelled():
                        raise asyncio.CancelledError
                    block = await self._call(
                        "read", {"transferId": transfer_id, "maxBytes": BLOCK_BYTES}
                    )
                    content = base64.b64decode(_string(block, "base64"), validate=True)
                    if not isinstance(block.get("eof"), bool):
                        raise RuntimeError("Invalid Host file EOF acknowledgement")
                    if len(content) > BLOCK_BYTES or (not content and not block["eof"]):
                        raise RuntimeError("Invalid Host file block")
                    spool.write(content)
                    read_bytes += len(content)
                    if self._maximum_bytes is not None and read_bytes > self._maximum_bytes:
                        raise RuntimeError("Plugin DataIO source exceeds its byte budget")
                    if progress:
                        await progress(0, 0, f"读取来源文件：{read_bytes} 字节")
                    if block["eof"]:
                        break
                spool.seek(0)
                yield FileBuffer(cast(BinaryIO, spool), _string(opened, "displayName"))
            finally:
                await self._call("closeRead", {"transferId": transfer_id})

    @asynccontextmanager
    async def write(
        self,
        grant_id: str,
        *,
        run_id: str | None = None,
        cancelled: Callable[[], bool] | None = None,
    ) -> AsyncIterator[FileBuffer]:
        opened = await self._call("openWrite", self._grant_params(grant_id, run_id))
        transfer_id = _string(opened, "transferId")
        finishing = False
        with SpooledTemporaryFile(max_size=8 * 1024 * 1024, mode="w+b") as spool:
            try:
                output = (
                    _BoundedWriter(cast(BinaryIO, spool), self._maximum_bytes)
                    if self._maximum_bytes is not None
                    else spool
                )
                yield FileBuffer(cast(BinaryIO, output), _string(opened, "displayName"))
                spool.seek(0)
                offset = 0
                while content := spool.read(BLOCK_BYTES):
                    if cancelled and cancelled():
                        raise asyncio.CancelledError
                    receipt = await self._call(
                        "write",
                        {
                            "transferId": transfer_id,
                            "offset": offset,
                            "base64": base64.b64encode(content).decode("ascii"),
                        },
                    )
                    offset += len(content)
                    if receipt.get("offset") != offset:
                        raise RuntimeError("Host file write acknowledgement mismatch")
                if cancelled and cancelled():
                    raise asyncio.CancelledError
                # Once submitted, cancellation must observe the final native
                # outcome. A committed file must not become a cancelled task.
                finishing = True
                commit = asyncio.ensure_future(
                    self._call("finishWrite", {"transferId": transfer_id, "commit": True})
                )
                try:
                    while not commit.done():
                        with suppress(asyncio.CancelledError):
                            await asyncio.shield(commit)
                    receipt = commit.result()
                    if receipt.get("committed") is not True or receipt.get("bytes") != offset:
                        raise RuntimeError("Host file commit was not confirmed")
                except Exception as exc:
                    raise FileCommitUnknownError(
                        "Export submission outcome is unknown; verify the target file."
                    ) from exc
            finally:
                if not finishing:
                    await self._call("finishWrite", {"transferId": transfer_id, "commit": False})

    @asynccontextmanager
    async def reserve_import(
        self, grant_id: str, plan_token: str
    ) -> AsyncIterator[Callable[[], None]]:
        reservation = await self._call("reserveImport", {"grantId": grant_id, "token": plan_token})
        committed = False

        def mark_committed() -> None:
            nonlocal committed
            committed = True

        try:
            yield mark_committed
        finally:
            # Mark synchronously after Go's commit, before any await. A missing
            # settlement reply cannot release an already applied import grant.
            try:
                await self._call(
                    "settleImport",
                    {
                        "reservationId": _string(reservation, "reservationId"),
                        "outcome": "consumed" if committed else "released",
                    },
                )
            except Exception, asyncio.CancelledError:
                if not committed:
                    raise
                # The authority already committed. The Host reservation remains
                # fail-closed until settled or its owning epoch retires; cleanup
                # failure cannot change the business result into a failed import.
                logger.warning("import.committed_grant_settlement_failed")

    @staticmethod
    def _grant_params(grant_id: str, run_id: str | None) -> JsonObject:
        params: JsonObject = {"grantId": grant_id}
        if run_id is not None:
            params["runId"] = run_id
        return params


def _string(reply: JsonObject, key: str) -> str:
    value = reply.get(key)
    if not isinstance(value, str):
        raise RuntimeError(f"Invalid Host file {key} acknowledgement")
    return value


class _BoundedWriter(RawIOBase):
    """Bound formatter output before a spooled file can grow beyond the plugin budget."""

    def __init__(self, stream: BinaryIO, maximum: int) -> None:
        self._stream = stream
        self._maximum = maximum

    def writable(self) -> bool:
        return True

    def readable(self) -> bool:
        return True

    def seekable(self) -> bool:
        return True

    def tell(self) -> int:
        return self._stream.tell()

    def seek(self, offset: int, whence: int = 0) -> int:
        return self._stream.seek(offset, whence)

    def write(self, content: Buffer) -> int:
        if self.tell() + memoryview(content).nbytes > self._maximum:
            raise RuntimeError("Plugin DataIO output exceeds its byte budget")
        return self._stream.write(content)

    def read(self, size: int = -1) -> bytes:
        return self._stream.read(size)

    def flush(self) -> None:
        if not self._stream.closed:
            self._stream.flush()
