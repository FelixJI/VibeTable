"""Stdio JSON-RPC 2.0 server.

``RpcServer`` wires the framing primitives (Task 2) to the dispatcher
(Task 3). It is transport-agnostic: it takes any ``StreamReader``-like reader
and any ``AsyncWriter``-like writer, so tests can feed synthetic streams and
the real process can plug in asyncio pipe transports built on
``sys.stdin.buffer`` / ``sys.stdout.buffer``.

Two notable guarantees:

* **stdout is protocol-only.** No log line, prompt, or diagnostic ever lands
  on the response stream — diagnostics go to stderr (configured by the caller
  in ``__main__.py``). ``serve()`` only emits framed JSON-RPC responses.
* **``notify()`` emits a JSON-RPC notification**: an object with ``jsonrpc``,
  ``method``, and ``params`` but *no* ``id`` field.
"""

from __future__ import annotations

import asyncio
import logging
import uuid
from typing import Any

from pydantic import BaseModel

from backend.application.path_grant import PathGrantError
from backend.contracts.product_rpc import JsonObject
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.framing import AsyncWriter, read_frame, write_frame

logger = logging.getLogger(__name__)


class RpcServer:
    """Serves JSON-RPC requests from a reader and writes responses to a writer."""

    def __init__(
        self,
        reader: asyncio.StreamReader,
        writer: AsyncWriter,
        dispatcher: RpcDispatcher,
    ) -> None:
        self._reader = reader
        self._writer = writer
        self._dispatcher = dispatcher
        # Serialize all writes (responses + notifications) so concurrent
        # notify() calls cannot interleave half-frames on the wire.
        self._write_lock = asyncio.Lock()
        self._host_pending: dict[str, asyncio.Future[JsonObject]] = {}
        self._closed = False

    async def call_host_file(self, action: str, params: JsonObject) -> JsonObject:
        """Call only the fixed file broker on this process's owning Host."""
        if action not in {
            "describe",
            "openRead",
            "read",
            "closeRead",
            "reserveImport",
            "settleImport",
            "openWrite",
            "write",
            "finishWrite",
        }:
            raise ValueError("unknown Host file operation")
        if self._closed or len(self._host_pending) >= 32:
            raise RuntimeError("Host file channel is unavailable")
        request_id = f"host-file:{uuid.uuid4().hex}"
        future: asyncio.Future[JsonObject] = asyncio.get_running_loop().create_future()
        self._host_pending[request_id] = future
        try:
            async with self._write_lock:
                await write_frame(
                    self._writer,
                    {
                        "jsonrpc": "2.0",
                        "id": request_id,
                        "method": f"host.file.{action}",
                        "params": params,
                    },
                )
            if action == "finishWrite" and params.get("commit") is True:
                # Submission can outlive the ordinary callback deadline. Only
                # the native receipt or channel retirement settles this commit;
                # a local timeout cannot prove that the target was not replaced.
                return await future
            return await asyncio.wait_for(future, timeout=30)
        finally:
            self._host_pending.pop(request_id, None)

    def _receive_host_file(self, payload: dict[str, Any]) -> bool:
        request_id = payload.get("id")
        if (
            "method" in payload
            or not isinstance(request_id, str)
            or not request_id.startswith("host-file:")
        ):
            return False
        future = self._host_pending.pop(request_id, None)
        if future is not None and not future.done():
            result = payload.get("result")
            if (
                payload.get("jsonrpc") == "2.0"
                and "error" not in payload
                and isinstance(result, dict)
            ):
                future.set_result(result)
            else:
                error = payload.get("error")
                data = error.get("data") if isinstance(error, dict) else None
                if (
                    payload.get("jsonrpc") == "2.0"
                    and "result" not in payload
                    and isinstance(error, dict)
                    and error.get("code") == -32050
                    and isinstance(data, dict)
                    and data.get("kind") == "path_grant_error"
                ):
                    # Preserve the public category, never forward peer messages or paths.
                    future.set_exception(
                        PathGrantError(
                            "File grant is unavailable for this operation.",
                            code="grant_unavailable",
                        )
                    )
                else:
                    future.set_exception(RuntimeError("Host file operation was rejected"))
        return True

    async def serve(self) -> None:
        """Read broker responses without reordering ordinary RPC execution."""
        queue: asyncio.Queue[JsonObject | None] = asyncio.Queue(maxsize=64)

        async def dispatch_serially() -> None:
            while (payload := await queue.get()) is not None:
                try:
                    response = await self._dispatcher.dispatch(payload)
                except Exception:
                    logger.exception("rpc: dispatcher raised unexpectedly")
                    continue
                if response is not None:
                    async with self._write_lock:
                        await write_frame(self._writer, response)

        async def read_requests() -> None:
            try:
                while True:
                    payload = await read_frame(self._reader)
                    if payload is None:
                        break
                    if not self._receive_host_file(payload):
                        # Backpressure here would hide the callback reply that
                        # the active handler needs. Terminate on overload.
                        queue.put_nowait(payload)
            finally:
                self._closed = True
                for future in self._host_pending.values():
                    if not future.done():
                        future.set_exception(RuntimeError("Host file channel closed"))
                self._host_pending.clear()
            await queue.put(None)

        consumer = asyncio.create_task(dispatch_serially())
        reader = asyncio.create_task(read_requests())
        try:
            done, _ = await asyncio.wait((reader, consumer), return_when=asyncio.FIRST_COMPLETED)
            if consumer in done:
                await consumer
            await reader
            await consumer
        finally:
            reader.cancel()
            consumer.cancel()
            await asyncio.gather(reader, consumer, return_exceptions=True)

    async def notify(self, method: str, params: BaseModel | dict[str, Any]) -> None:
        """Send a JSON-RPC notification (no ``id``) to the client.

        Raises ``FrameTooLargeError`` if the encoded frame would exceed the
        4 MiB cap.
        """
        if isinstance(params, BaseModel):
            serialized_params = params.model_dump(by_alias=True, mode="json")
        else:
            serialized_params = params
        frame: dict[str, Any] = {
            "jsonrpc": "2.0",
            "method": method,
            "params": serialized_params,
        }
        # No "id" key by design.
        async with self._write_lock:
            await write_frame(self._writer, frame)
