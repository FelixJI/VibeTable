from __future__ import annotations

import json
import logging
from io import StringIO

from backend.infrastructure.diagnostic_logging import (
    DiagnosticJsonFormatter,
    configure_diagnostic_logging,
    schema_fields,
)


def test_diagnostic_formatter_emits_closed_schema_without_message_values() -> None:
    record = logging.LogRecord(
        "backend.rpc",
        logging.ERROR,
        __file__,
        1,
        "rpc.dispatch_failed",
        ("private search text",),
        None,
    )
    for key, value in {
        "errorCode": "rpc.internal",
        "operationId": "operation-1",
        "businessValue": "must-not-escape",
    }.items():
        setattr(record, key, value)

    payload = json.loads(DiagnosticJsonFormatter().format(record))

    assert set(payload) == schema_fields()
    assert payload["event"] == "rpc.dispatch_failed"
    assert payload["errorCode"] == "rpc.internal"
    assert payload["operationId"] == "operation-1"
    serialized = json.dumps(payload)
    assert "private search text" not in serialized
    assert "must-not-escape" not in serialized


def test_diagnostic_logging_rejects_non_json_numbers_through_text_stream_interface() -> None:
    stream = StringIO()
    root = logging.getLogger()
    previous_handlers = list(root.handlers)
    previous_level = root.level
    try:
        configure_diagnostic_logging(stream)
        logger = logging.getLogger("backend.rpc")

        logger.info("rpc.completed", extra={"durationMs": float("inf")})
    finally:
        root.handlers[:] = previous_handlers
        root.setLevel(previous_level)

    payload = json.loads(stream.getvalue())
    assert payload["event"] == "rpc.completed"
    assert payload["durationMs"] is None
