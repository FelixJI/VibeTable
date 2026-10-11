"""Bounded file work: cooperative cancellation and join before resource release."""

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Buffer, Callable
from contextlib import suppress
from io import BufferedIOBase
from threading import Event
from typing import BinaryIO

Check = Callable[[], None]
Report = Callable[[int, int, str], None]
Progress = Callable[[int, int, str], Awaitable[None]]


async def run_file_work[T](
    work: Callable[[Check, Report], T],
    *,
    cancelled: Callable[[], bool] | None = None,
    progress: Progress | None = None,
) -> T:
    stop = Event()
    loop = asyncio.get_running_loop()

    def check() -> None:
        if stop.is_set() or (cancelled and cancelled()):
            raise asyncio.CancelledError

    async def emit(done: int, total: int, message: str) -> None:
        if progress is not None:
            await progress(done, total, message)

    def report(done: int, total: int, message: str) -> None:
        check()
        if progress is not None:
            # Backpressure keeps reports bounded; the loop stays free for controls.
            asyncio.run_coroutine_threadsafe(emit(done, total, message), loop).result()

    worker = asyncio.create_task(asyncio.to_thread(work, check, report))
    try:
        return await asyncio.shield(worker)
    except asyncio.CancelledError:
        stop.set()
        # Repeated cancellation must not close a stream under its running thread.
        while not worker.done():
            with suppress(asyncio.CancelledError):
                await asyncio.shield(worker)
        if not worker.cancelled():
            worker.result()
        raise


class CheckedStream(BufferedIOBase):
    """Non-owning stream guard, including openpyxl's ZIP read/write boundaries."""

    def __init__(self, stream: BinaryIO, check: Check) -> None:
        self._stream = stream
        self._check = check

    def readable(self) -> bool:
        return self._stream.readable()

    def writable(self) -> bool:
        return self._stream.writable()

    def seekable(self) -> bool:
        return self._stream.seekable()

    def read(self, size: int | None = -1) -> bytes:
        self._check()
        return self._stream.read(-1 if size is None else size)

    def read1(self, size: int = -1) -> bytes:
        return self.read(size)

    def write(self, buffer: Buffer) -> int:
        self._check()
        return self._stream.write(buffer)

    def seek(self, offset: int, whence: int = 0) -> int:
        self._check()
        return self._stream.seek(offset, whence)

    def tell(self) -> int:
        return self._stream.tell()

    def flush(self) -> None:
        if not self._stream.closed:
            self._stream.flush()
