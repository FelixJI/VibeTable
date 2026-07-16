"""Deterministic fake Python backend for the supervisor lifecycle tests.

This script deliberately uses ONLY the Python standard library (``json``,
``sys``, ``time``). It MUST NOT import any application or backend code: the
whole point is to give the .NET supervisor tests a tiny, hermetic stand-in for
the real ``backend.__main__`` so we can exercise process spawning, the
JSON-RPC handshake, Job-Object kill semantics, stderr capture, and graceful
shutdown without dragging the real backend (and its dependency tree) into the
.NET test suite.

Wire protocol
-------------
Frames are newline-delimited JSON-RPC 2.0 objects on stdin/stdout, mirroring
``backend/rpc/framing.py``: compact JSON, ``\\n`` as the sole terminator.
stderr is reserved for diagnostics (a known sentinel line is emitted on boot
so the supervisor's stderr-capture test can assert against it).

Supported methods
-----------------
``system.handshake``
    Returns the protocol handshake result and its registered fake methods.
    Honors an optional ``__forceProtocol`` suffix on the requested
    ``protocolVersion`` (e.g. ``"2.0"``) so the protocol-mismatch test can
    drive a Faulted state without a second fake binary.

``test.exit``
    Exits the process immediately with code 0. Used by the unexpected-exit
    test to break the reader mid-stream.

``test.delay``
    Sleeps for ``params.seconds`` before reading the next frame. The
    startup-timeout test uses this to defer the handshake reply past the
    supervisor's startup deadline.

Shutdown
--------
On EOF (stdin closed) the fake exits cleanly with code 0 — unless it has been
told to ignore EOF (via the ``__VIBETABLE_IGNORE_EOF=1`` environment variable) for
the forced-kill-after-timeout test, in which case it loops forever and waits
to be killed by the Job Object.
"""

from __future__ import annotations

import json
import os
import sys
import time
from typing import Any

#: Known diagnostic written to stderr at startup. The supervisor's
#: stderr-capture test asserts this substring is present in the captured log.
STDERR_SENTINEL = "vibetable-fake-backend: ready"

#: Protocol version advertised by the real backend.
PROTOCOL_VERSION = "1.0"

#: Backend version advertised in the handshake result.
BACKEND_VERSION = "0.1.0"

#: Methods implemented by this hermetic fake backend.
CAPABILITIES = ["system.handshake", "test.delay", "test.exit"]


def _write_frame(payload: dict[str, Any]) -> None:
    """Serialize ``payload`` as compact JSON + ``\\n`` and flush stdout."""
    sys.stdout.buffer.write((json.dumps(payload, separators=(",", ":")) + "\n").encode("utf-8"))
    sys.stdout.buffer.flush()


def _handshake_result(req_id: str) -> dict[str, Any]:
    return {
        "jsonrpc": "2.0",
        "id": req_id,
        "result": {
            "backendVersion": BACKEND_VERSION,
            "protocolVersion": PROTOCOL_VERSION,
            "capabilities": list(CAPABILITIES),
        },
    }


def _error(req_id: str, code: int, message: str, *, kind: str | None = None) -> dict[str, Any]:
    data: dict[str, Any] = {} if kind is None else {"kind": kind}
    return {
        "jsonrpc": "2.0",
        "id": req_id,
        "error": {"code": code, "message": message, "data": data},
    }


def _force_protocol_mismatch(req_id: str, requested: str) -> dict[str, Any]:
    """Return a handshake response that advertises a DIFFERENT protocol
    version than the supervisor requested.

    The supervisor asks for ``"1.0"``; the fake claims to speak ``"2.0"``.
    The supervisor's handshake validator must reject this and transition to
    Faulted.
    """
    return {
        "jsonrpc": "2.0",
        "id": req_id,
        "result": {
            "backendVersion": BACKEND_VERSION,
            "protocolVersion": "2.0",
            "capabilities": list(CAPABILITIES),
        },
    }


def _handle_line(line: str) -> bool:
    """Process one inbound JSON line.

    Returns ``True`` to continue the read loop, ``False`` to terminate the
    process cleanly (e.g. after ``test.exit``).
    """
    line = line.strip()
    if not line:
        return True

    try:
        frame = json.loads(line)
    except json.JSONDecodeError:
        # Malformed input: emit a JSON-RPC parse error and keep going so the
        # supervisor can observe the response without the process dying.
        _write_frame(_error("0", -32700, "Parse error"))
        return True

    req_id = frame.get("id")
    method = frame.get("method")
    params = frame.get("params") or {}

    if req_id is None or method is None:
        # Not a request we can respond to (notification / malformed). Ignore.
        return True

    if method == "system.handshake":
        requested_protocol = params.get("protocolVersion", PROTOCOL_VERSION)
        # Optional handshake delay: the startup-timeout test sets this so the
        # fake waits past the supervisor's startup deadline before replying.
        delay_raw = os.environ.get("__VIBETABLE_HANDSHAKE_DELAY_SECONDS", "0")
        try:
            delay_seconds = float(delay_raw)
        except ValueError:
            delay_seconds = 0.0
        if delay_seconds > 0:
            time.sleep(delay_seconds)
        if os.environ.get("__VIBETABLE_FORCE_PROTOCOL_MISMATCH") == "1":
            _write_frame(_force_protocol_mismatch(req_id, requested_protocol))
        else:
            _write_frame(_handshake_result(req_id))
        return True

    if method == "test.exit":
        # Acknowledge, then exit immediately. The supervisor should observe an
        # unexpected process exit and transition to Faulted.
        _write_frame(
            {
                "jsonrpc": "2.0",
                "id": req_id,
                "result": {"exiting": True},
            }
        )
        sys.stdout.buffer.flush()
        return False

    if method == "test.delay":
        seconds = float(params.get("seconds", 0))
        time.sleep(seconds)
        _write_frame(
            {
                "jsonrpc": "2.0",
                "id": req_id,
                "result": {"slept": seconds},
            }
        )
        return True

    # Unknown method: JSON-RPC method-not-found error.
    _write_frame(_error(req_id, -32601, f"Method not found: {method}"))
    return True


def main() -> int:
    # Emit the known stderr sentinel FIRST, before any stdout output, so the
    # supervisor's stderr-capture test can rely on it.
    sys.stderr.write(STDERR_SENTINEL + "\n")
    sys.stderr.flush()

    ignore_eof = os.environ.get("__VIBETABLE_IGNORE_EOF") == "1"

    while True:
        line = sys.stdin.buffer.readline()
        if not line:
            # Clean EOF on stdin.
            if ignore_eof:
                # Park forever; the supervisor must kill us via the Job Object.
                while True:
                    time.sleep(1.0)
            return 0
        try:
            keep_going = _handle_line(line.decode("utf-8", errors="replace"))
        except SystemExit:
            raise
        except Exception as exc:  # pragma: no cover - defensive
            sys.stderr.write(f"vibetable-fake-backend: handler error: {exc}\n")
            sys.stderr.flush()
            keep_going = True
        if not keep_going:
            return 0


if __name__ == "__main__":
    sys.exit(main())
