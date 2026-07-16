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
from typing import Any

from pydantic import BaseModel

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

    async def serve(self) -> None:
        """Read request frames until EOF, dispatch each, and write responses.

        Notifications (requests without an ``id``) produce no response frame.
        Any exception other than framing errors is logged to stderr (via the
        ``backend.rpc.server`` logger) and the loop continues; a framing error
        (e.g. oversized frame) terminates the loop.
        """
        while True:
            try:
                payload = await read_frame(self._reader)
            except asyncio.IncompleteReadError:
                break
            except Exception:
                # Bad frame (e.g. oversized, malformed JSON) — log to stderr
                # and stop serving. The framing layer raises FrameTooLargeError
                # and ValueError variants here.
                logger.exception("rpc: framing error while reading request frame")
                raise
            if payload is None:
                # Clean EOF.
                break

            try:
                response = await self._dispatcher.dispatch(payload)
            except Exception:
                # dispatch() is expected to convert handler errors to JSON-RPC
                # error objects and never raise; if it does, log and continue.
                logger.exception("rpc: dispatcher raised unexpectedly")
                continue
            if response is None:
                # Notification: no response on the wire.
                continue

            async with self._write_lock:
                await write_frame(self._writer, response)

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
