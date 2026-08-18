"""Content-free JSON logging shared by the packaged Python process."""

from __future__ import annotations

import json
import logging
import math
from datetime import UTC, datetime
from typing import TextIO

type LogValue = str | int | float | None

_STANDARD = {
    "timestamp",
    "level",
    "module",
    "event",
    "errorCode",
    "requestId",
    "operationId",
    "workspaceId",
    "sessionEpoch",
    "jobId",
    "durationMs",
}


class DiagnosticJsonFormatter(logging.Formatter):
    """Emit only the closed support schema; message arguments never escape."""

    def format(self, record: logging.LogRecord) -> str:
        event = record.msg if isinstance(record.msg, str) else "python.log"
        payload: dict[str, LogValue] = {
            "timestamp": datetime.fromtimestamp(record.created, UTC).isoformat(),
            "level": record.levelname.lower(),
            "module": record.name,
            "event": event[:160],
            "errorCode": _safe(record, "errorCode"),
            "requestId": _safe(record, "requestId"),
            "operationId": _safe(record, "operationId"),
            "workspaceId": _safe(record, "workspaceId"),
            "sessionEpoch": _integer(record, "sessionEpoch"),
            "jobId": _safe(record, "jobId"),
            "durationMs": _number(record, "durationMs"),
        }
        return json.dumps(
            payload,
            ensure_ascii=False,
            separators=(",", ":"),
            allow_nan=False,
        )


def configure_diagnostic_logging(stream: TextIO) -> None:
    handler = logging.StreamHandler(stream)
    handler.setFormatter(DiagnosticJsonFormatter())
    root = logging.getLogger()
    root.handlers[:] = [handler]
    root.setLevel(logging.INFO)


def _safe(record: logging.LogRecord, name: str) -> str | None:
    value: object = record.__dict__.get(name)
    if not isinstance(value, str) or len(value) > 160:
        return None
    return value


def _integer(record: logging.LogRecord, name: str) -> int | None:
    value: object = record.__dict__.get(name)
    return value if isinstance(value, int) and not isinstance(value, bool) else None


def _number(record: logging.LogRecord, name: str) -> int | float | None:
    value: object = record.__dict__.get(name)
    if not isinstance(value, (int, float)) or isinstance(value, bool):
        return None
    return value if math.isfinite(value) else None


def schema_fields() -> frozenset[str]:
    return frozenset(_STANDARD)
