"""Byte-only access to the native file broker of this worker's owning Host."""

from __future__ import annotations

import asyncio
import base64
import logging
from collections.abc import AsyncIterator, Awaitable, Callable, Iterator
from contextlib import asynccontextmanager, contextmanager
from dataclasses import dataclass
from io import TextIOWrapper
from tempfile import SpooledTemporaryFile
from typing import BinaryIO, cast

from backend.contracts.product_rpc import JsonObject
from backend.contracts.task import SessionPathGrant

HostFileCall = Callable[[str, JsonObject], Awaitable[JsonObject]]
logger = logging.getLogger(__name__)
BLOCK_BYTES = 256 * 1024


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
    def __init__(self, call: HostFileCall) -> None:
        self._call = call

    async def describe(self, grant_id: str) -> SessionPathGrant:
        return SessionPathGrant.model_validate(await self._call("describe", {"grantId": grant_id}))

    @asynccontextmanager
    async def read(self, grant_id: str, *, run_id: str | None = None) -> AsyncIterator[FileBuffer]:
        opened = await self._call("openRead", self._grant_params(grant_id, run_id))
        transfer_id = _string(opened, "transferId")
        with SpooledTemporaryFile(max_size=8 * 1024 * 1024, mode="w+b") as spool:
            try:
                while True:
                    block = await self._call(
                        "read", {"transferId": transfer_id, "maxBytes": BLOCK_BYTES}
                    )
                    content = base64.b64decode(_string(block, "base64"), validate=True)
                    if not isinstance(block.get("eof"), bool):
                        raise RuntimeError("Invalid Host file EOF acknowledgement")
                    if len(content) > BLOCK_BYTES or (not content and not block["eof"]):
                        raise RuntimeError("Invalid Host file block")
                    spool.write(content)
                    if block["eof"]:
                        break
                spool.seek(0)
                yield FileBuffer(cast(BinaryIO, spool), _string(opened, "displayName"))
            finally:
                await self._call("closeRead", {"transferId": transfer_id})

    @asynccontextmanager
    async def write(self, grant_id: str, *, run_id: str | None = None) -> AsyncIterator[FileBuffer]:
        opened = await self._call("openWrite", self._grant_params(grant_id, run_id))
        transfer_id = _string(opened, "transferId")
        finishing = False
        with SpooledTemporaryFile(max_size=8 * 1024 * 1024, mode="w+b") as spool:
            try:
                yield FileBuffer(cast(BinaryIO, spool), _string(opened, "displayName"))
                spool.seek(0)
                offset = 0
                while content := spool.read(BLOCK_BYTES):
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
                # Once submitted, cancellation must observe the final native
                # outcome. A committed file must not become a cancelled task.
                finishing = True
                commit = asyncio.ensure_future(
                    self._call("finishWrite", {"transferId": transfer_id, "commit": True})
                )
                try:
                    receipt = await asyncio.shield(commit)
                except asyncio.CancelledError:
                    receipt = await commit
                if receipt.get("committed") is not True or receipt.get("bytes") != offset:
                    raise RuntimeError("Host file commit was not confirmed")
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
            except Exception:
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
