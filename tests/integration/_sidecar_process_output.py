"""Bounded, content-free process-output capture for packaged sidecar tests."""

from __future__ import annotations

import json
import re
import threading
from collections import deque
from dataclasses import dataclass
from typing import TextIO

MAX_READINESS_CHARS = 16 * 1024
MAX_STDERR_LINE_CHARS = 4 * 1024
MAX_STDERR_TAIL_CHARS = 64 * 1024
_READ_CHUNK_CHARS = 8 * 1024
_SAFE_TEXT = re.compile(r"^[A-Za-z0-9_.:-]+$")
_SAFE_TIMESTAMP = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$")
_EXPECTED_LOG_FIELDS = {
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
_SAFE_LEVELS = {"debug", "info", "warn", "error"}


@dataclass(frozen=True)
class ProcessOutputSnapshot:
    readiness_phase: str
    stderr_events: tuple[str, ...]
    stdout_discarded_chars: int
    stderr_rejected: int
    stderr_overlong: int
    stderr_dropped: int
    reader_errors: int

    def diagnostic_text(self) -> str:
        tail = "[" + ",".join(self.stderr_events) + "]"
        return (
            f"readiness={self.readiness_phase}; "
            f"stdoutDiscardedChars={self.stdout_discarded_chars}; "
            f"stderrEvents={tail}; stderrRejected={self.stderr_rejected}; "
            f"stderrOverlong={self.stderr_overlong}; stderrDropped={self.stderr_dropped}; "
            f"readerErrors={self.reader_errors}"
        )


EMPTY_PROCESS_OUTPUT = ProcessOutputSnapshot(
    readiness_phase="not-started",
    stderr_events=(),
    stdout_discarded_chars=0,
    stderr_rejected=0,
    stderr_overlong=0,
    stderr_dropped=0,
    reader_errors=0,
)


class SidecarProcessOutput:
    """Own both child pipes and expose only bounded, safe diagnostic evidence."""

    def __init__(self, stdout: TextIO, stderr: TextIO) -> None:
        self._stdout = stdout
        self._stderr = stderr
        self._lock = threading.Lock()
        self._finish_lock = threading.Lock()
        self._readiness_available = threading.Event()
        self._readiness_line = ""
        self._readiness_error = ""
        self._readiness_phase = "waiting"
        self._stderr_events: deque[str] = deque()
        self._stderr_tail_chars = 2
        self._stdout_discarded_chars = 0
        self._stderr_rejected = 0
        self._stderr_overlong = 0
        self._stderr_dropped = 0
        self._reader_errors = 0
        self._finished = False
        self._stdout_thread = threading.Thread(
            target=self._pump_stdout,
            name="packaged-sidecar-stdout",
        )
        self._stderr_thread = threading.Thread(
            target=self._pump_stderr,
            name="packaged-sidecar-stderr",
        )

    @classmethod
    def attach(cls, stdout: TextIO, stderr: TextIO) -> SidecarProcessOutput:
        owner = cls(stdout, stderr)
        owner._stdout_thread.start()
        owner._stderr_thread.start()
        return owner

    def wait_readiness(self, timeout: float) -> str:
        if not self._readiness_available.wait(timeout):
            with self._lock:
                self._readiness_phase = "timed-out"
            raise TimeoutError("sidecar readiness timed out")
        with self._lock:
            if self._readiness_error:
                raise ValueError(self._readiness_error)
            return self._readiness_line

    def snapshot(self) -> ProcessOutputSnapshot:
        with self._lock:
            return ProcessOutputSnapshot(
                readiness_phase=self._readiness_phase,
                stderr_events=tuple(self._stderr_events),
                stdout_discarded_chars=self._stdout_discarded_chars,
                stderr_rejected=self._stderr_rejected,
                stderr_overlong=self._stderr_overlong,
                stderr_dropped=self._stderr_dropped,
                reader_errors=self._reader_errors,
            )

    @property
    def readers_alive(self) -> tuple[str, ...]:
        return tuple(
            thread.name
            for thread in (self._stdout_thread, self._stderr_thread)
            if thread.is_alive()
        )

    def finish_after_process_exit(self) -> None:
        with self._finish_lock:
            if self._finished:
                return
            # Popen keeps child descriptors closed by default. Once its owner has
            # observed process exit, both pumps must drain to EOF before we close.
            for thread in (self._stdout_thread, self._stderr_thread):
                thread.join()
            self._finished = True
            self._stdout.close()
            self._stderr.close()

    def _pump_stdout(self) -> None:
        try:
            line = self._stdout.readline(MAX_READINESS_CHARS + 1)
            with self._lock:
                if not line:
                    self._readiness_error = "readiness stream reached EOF"
                    self._readiness_phase = "eof"
                elif len(line) > MAX_READINESS_CHARS:
                    self._readiness_error = "readiness line exceeded limit"
                    self._readiness_phase = "overlong"
                else:
                    self._readiness_line = line
                    self._readiness_phase = "received"
            self._readiness_available.set()
            while chunk := self._stdout.read(_READ_CHUNK_CHARS):
                with self._lock:
                    self._stdout_discarded_chars += len(chunk)
        except (OSError, ValueError):
            with self._lock:
                self._reader_errors += 1
                if not self._readiness_available.is_set():
                    self._readiness_error = "readiness reader failed"
                    self._readiness_phase = "reader-error"
            self._readiness_available.set()

    def _pump_stderr(self) -> None:
        try:
            while line := self._stderr.readline(MAX_STDERR_LINE_CHARS + 1):
                if len(line) > MAX_STDERR_LINE_CHARS:
                    if not line.endswith("\n"):
                        self._discard_overlong_stderr_line()
                    else:
                        with self._lock:
                            self._stderr_overlong += 1
                    continue
                self._capture_stderr_line(line)
        except (OSError, ValueError):
            with self._lock:
                self._reader_errors += 1

    def _discard_overlong_stderr_line(self) -> None:
        while chunk := self._stderr.readline(MAX_STDERR_LINE_CHARS + 1):
            if chunk.endswith("\n") or len(chunk) <= MAX_STDERR_LINE_CHARS:
                break
        with self._lock:
            self._stderr_overlong += 1

    def _capture_stderr_line(self, line: str) -> None:
        projected = _project_safe_event(line)
        if projected is None:
            with self._lock:
                self._stderr_rejected += 1
            return
        with self._lock:
            while (
                self._stderr_events
                and self._stderr_tail_chars + len(projected) + (1 if self._stderr_events else 0)
                > MAX_STDERR_TAIL_CHARS
            ):
                removed = self._stderr_events.popleft()
                self._stderr_tail_chars -= len(removed) + (1 if self._stderr_events else 0)
                self._stderr_dropped += 1
            if self._stderr_events:
                self._stderr_tail_chars += 1
            self._stderr_events.append(projected)
            self._stderr_tail_chars += len(projected)


def _project_safe_event(line: str) -> str | None:
    try:
        payload = json.loads(line)
    except (json.JSONDecodeError, TypeError):
        return None
    if not isinstance(payload, dict) or set(payload) != _EXPECTED_LOG_FIELDS:
        return None
    timestamp = payload["timestamp"]
    level = payload["level"]
    module = payload["module"]
    event = payload["event"]
    error_code = payload["errorCode"]
    duration_ms = payload["durationMs"]
    if not isinstance(timestamp, str) or _SAFE_TIMESTAMP.fullmatch(timestamp) is None:
        return None
    if not isinstance(level, str) or level not in _SAFE_LEVELS:
        return None
    if not _is_safe_text(module) or not _is_safe_text(event):
        return None
    if error_code is not None and not _is_safe_text(error_code):
        return None
    if duration_ms is not None and (
        not isinstance(duration_ms, (int, float)) or isinstance(duration_ms, bool)
    ):
        return None
    projected = {
        "timestamp": timestamp,
        "level": level,
        "module": module,
        "event": event,
        "errorCode": error_code,
        "durationMs": duration_ms,
    }
    return json.dumps(projected, ensure_ascii=True, separators=(",", ":"))


def _is_safe_text(value: object) -> bool:
    return (
        isinstance(value, str) and 0 < len(value) <= 160 and _SAFE_TEXT.fullmatch(value) is not None
    )
