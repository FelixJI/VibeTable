"""Stable ownership evidence for one Windows TCP listener generation."""

from __future__ import annotations

import sys
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import PureWindowsPath
from typing import Protocol

from tests.e2e._windows_tcp_table import WindowsTcpRow

TcpListenerRow = WindowsTcpRow

__all__ = [
    "OwnerLeaseCleanupReport",
    "PortReleaseReport",
    "TcpListenerRow",
    "WindowsTcpListenerOwnerLease",
]


@dataclass(frozen=True)
class OwnerLeaseCleanupReport:
    stable_handle_closed: bool
    errors: tuple[str, ...] = ()

    def as_artifact(self) -> dict[str, object]:
        return {
            "stableHandleClosed": self.stable_handle_closed,
            "errors": list(self.errors),
            "status": "passed" if self.stable_handle_closed and not self.errors else "failed",
        }


@dataclass(frozen=True)
class PortReleaseReport:
    owner_pid: int
    owner_name: str
    capture_rows: tuple[TcpListenerRow, ...]
    release_rows: tuple[TcpListenerRow, ...]
    decision: str
    released: bool
    owner_exited: bool | None = None
    errors: tuple[str, ...] = ()

    def as_artifact(self) -> dict[str, object]:
        return {
            "owner": {
                "pid": self.owner_pid,
                "processName": self.owner_name,
                "identityVerified": True,
                "exited": self.owner_exited,
            },
            "captureRows": [
                row.as_artifact() | {"ownership": "captured-owner"} for row in self.capture_rows
            ],
            "releaseRows": [
                row.as_artifact()
                | {
                    "ownership": (
                        "captured-owner"
                        if self.owner_exited is not True and row.pid == self.owner_pid
                        else "unowned"
                    )
                }
                for row in self.release_rows
            ],
            "decision": self.decision,
            "released": self.released,
            "errors": list(self.errors),
        }


class _OwnerHandle(Protocol):
    pid: int
    name: str

    def wait(self, timeout: float) -> bool: ...

    def close(self) -> None: ...


class _ListenerOwnerAdapter(Protocol):
    def query_listeners(self, port: int, *, timeout: float) -> tuple[TcpListenerRow, ...]: ...

    def open_owner(self, pid: int) -> _OwnerHandle: ...


class WindowsTcpListenerOwnerLease:
    """Owns a stable process identity for the listener observed at readiness."""

    def __init__(
        self,
        *,
        port: int,
        owner: _OwnerHandle,
        capture_rows: tuple[TcpListenerRow, ...],
        adapter: _ListenerOwnerAdapter,
        monotonic: Callable[[], float],
    ) -> None:
        self._port = port
        self._owner = owner
        self._owner_name = PureWindowsPath(owner.name).name
        self._capture_rows = capture_rows
        self._adapter = adapter
        self._monotonic = monotonic
        self._closed = False
        self._cleanup_report: OwnerLeaseCleanupReport | None = None

    def observe_release(self, *, timeout: float) -> PortReleaseReport:
        if self._closed:
            raise RuntimeError("TCP listener owner lease is closed")
        if timeout < 0:
            raise ValueError("timeout must be non-negative")
        deadline = self._monotonic() + timeout

        def remaining() -> float:
            return max(0.0, deadline - self._monotonic())

        query_budget = remaining()
        if query_budget <= 0:
            return self._budget_exhausted_report(owner_exited=None)
        try:
            current_rows = self._adapter.query_listeners(self._port, timeout=query_budget)
        except TimeoutError:
            return self._budget_exhausted_report(owner_exited=None)
        except (OSError, RuntimeError) as exc:
            return PortReleaseReport(
                owner_pid=self._owner.pid,
                owner_name=self._owner_name,
                capture_rows=self._capture_rows,
                release_rows=(),
                decision="release-listener-query-failed",
                released=False,
                owner_exited=None,
                errors=(f"unable to query the CDP listener at release ({type(exc).__name__})",),
            )
        if remaining() <= 0:
            return self._budget_exhausted_report(owner_exited=None)
        try:
            owner_exited = self._owner.wait(0.0)
        except (OSError, RuntimeError) as exc:
            return PortReleaseReport(
                owner_pid=self._owner.pid,
                owner_name=self._owner_name,
                capture_rows=self._capture_rows,
                release_rows=current_rows,
                decision="captured-owner-state-unknown",
                released=False,
                owner_exited=None,
                errors=(f"unable to observe the captured CDP owner ({type(exc).__name__})",),
            )
        if owner_exited:
            decision = (
                "captured-owner-exited-port-reused-unowned"
                if current_rows
                else "captured-owner-exited"
            )
            return self._release_report(
                release_rows=current_rows,
                decision=decision,
                owner_exited=True,
            )
        if not any(row.pid == self._owner.pid for row in current_rows):
            return self._release_report(
                release_rows=current_rows,
                decision="listener-released",
                owner_exited=False,
            )

        owner_budget = remaining()
        if owner_budget <= 0:
            return self._budget_exhausted_report(owner_exited=False)
        owner_error: str | None = None
        try:
            owner_exited = self._owner.wait(owner_budget)
        except (OSError, RuntimeError) as exc:
            owner_exited = False
            owner_error = f"unable to observe the captured CDP owner ({type(exc).__name__})"
        query_budget = remaining()
        if query_budget <= 0:
            return self._budget_exhausted_report(owner_exited=None if owner_error else owner_exited)
        try:
            release_rows = self._adapter.query_listeners(self._port, timeout=query_budget)
        except TimeoutError:
            return self._budget_exhausted_report(owner_exited=None if owner_error else owner_exited)
        except (OSError, RuntimeError) as exc:
            errors = [] if owner_error is None else [owner_error]
            errors.append(f"unable to query the CDP listener at release ({type(exc).__name__})")
            return PortReleaseReport(
                owner_pid=self._owner.pid,
                owner_name=self._owner_name,
                capture_rows=self._capture_rows,
                release_rows=(),
                decision="release-listener-query-failed",
                released=False,
                owner_exited=(None if owner_error is not None else owner_exited),
                errors=tuple(errors),
            )
        if remaining() <= 0:
            return self._budget_exhausted_report(owner_exited=None if owner_error else owner_exited)
        if owner_error is not None:
            return PortReleaseReport(
                owner_pid=self._owner.pid,
                owner_name=self._owner_name,
                capture_rows=self._capture_rows,
                release_rows=release_rows,
                decision="captured-owner-state-unknown",
                released=False,
                owner_exited=None,
                errors=(owner_error,),
            )
        if owner_exited:
            return self._release_report(
                release_rows=release_rows,
                decision=(
                    "captured-owner-exited-port-reused-unowned"
                    if release_rows
                    else "captured-owner-exited"
                ),
                owner_exited=True,
            )
        owner_listening = any(row.pid == self._owner.pid for row in release_rows)
        if not owner_listening:
            return self._release_report(
                release_rows=release_rows,
                decision="listener-released",
                owner_exited=False,
            )
        return PortReleaseReport(
            owner_pid=self._owner.pid,
            owner_name=self._owner_name,
            capture_rows=self._capture_rows,
            release_rows=release_rows,
            decision="captured-owner-still-listening",
            released=False,
            owner_exited=False,
        )

    def _release_report(
        self,
        *,
        release_rows: tuple[TcpListenerRow, ...],
        decision: str,
        owner_exited: bool,
    ) -> PortReleaseReport:
        return PortReleaseReport(
            owner_pid=self._owner.pid,
            owner_name=self._owner_name,
            capture_rows=self._capture_rows,
            release_rows=release_rows,
            decision=decision,
            released=True,
            owner_exited=owner_exited,
        )

    def _budget_exhausted_report(self, *, owner_exited: bool | None) -> PortReleaseReport:
        return PortReleaseReport(
            owner_pid=self._owner.pid,
            owner_name=self._owner_name,
            capture_rows=self._capture_rows,
            release_rows=(),
            decision="release-observation-budget-exhausted",
            released=False,
            owner_exited=owner_exited,
            errors=("CDP listener release observation exceeded its deadline",),
        )

    def close(self) -> OwnerLeaseCleanupReport:
        if self._cleanup_report is not None:
            return self._cleanup_report
        self._closed = True
        try:
            self._owner.close()
        except (OSError, RuntimeError) as close_error:
            self._cleanup_report = OwnerLeaseCleanupReport(
                stable_handle_closed=False,
                errors=(
                    f"unable to close captured CDP owner handle ({type(close_error).__name__})",
                ),
            )
        else:
            self._cleanup_report = OwnerLeaseCleanupReport(stable_handle_closed=True)
        return self._cleanup_report

    def __enter__(self) -> WindowsTcpListenerOwnerLease:
        return self

    def __exit__(
        self,
        _exc_type: object,
        exc: BaseException | None,
        _traceback: object,
    ) -> None:
        cleanup = self.close()
        if not cleanup.errors:
            return
        if exc is None:
            raise RuntimeError("; ".join(cleanup.errors))
        for error in cleanup.errors:
            exc.add_note(error)

    @classmethod
    def capture(cls, port: int) -> WindowsTcpListenerOwnerLease:
        if sys.platform != "win32":
            raise RuntimeError("TCP listener owner leases require Windows")
        if not 1 <= port <= 65535:
            raise ValueError("port must be between 1 and 65535")
        from tests.e2e._windows_process_scope_win32 import _Win32TcpListenerOwnerAdapter

        return _capture_with_adapter(port, _Win32TcpListenerOwnerAdapter())


def _capture_with_adapter(
    port: int,
    adapter: _ListenerOwnerAdapter,
    *,
    monotonic: Callable[[], float] = time.monotonic,
    timeout: float = 10.0,
) -> WindowsTcpListenerOwnerLease:
    if timeout <= 0:
        raise TimeoutError("CDP listener owner capture requires a positive timeout")
    deadline = monotonic() + timeout

    def remaining() -> float:
        return max(0.0, deadline - monotonic())

    before = adapter.query_listeners(port, timeout=remaining())
    if remaining() <= 0:
        raise TimeoutError("CDP listener owner capture exceeded its deadline")
    owner_pids = {row.pid for row in before}
    if len(owner_pids) != 1:
        raise RuntimeError(
            f"expected one TCP listener owner on the CDP port; observed {len(owner_pids)}"
        )
    owner = adapter.open_owner(next(iter(owner_pids)))
    try:
        query_budget = remaining()
        if query_budget <= 0:
            raise TimeoutError("CDP listener owner capture exceeded its deadline")
        after = adapter.query_listeners(port, timeout=query_budget)
        if remaining() <= 0:
            raise TimeoutError("CDP listener owner capture exceeded its deadline")
        if {row.pid for row in after} != {owner.pid}:
            raise RuntimeError("the CDP listener owner changed while its identity was captured")
        if owner.wait(0.0):
            raise RuntimeError("the CDP listener owner exited while its identity was captured")
        return WindowsTcpListenerOwnerLease(
            port=port,
            owner=owner,
            capture_rows=after,
            adapter=adapter,
            monotonic=monotonic,
        )
    except BaseException as exc:
        try:
            owner.close()
        except (OSError, RuntimeError) as close_error:
            exc.add_note(
                f"CDP listener owner handle cleanup also failed ({type(close_error).__name__})"
            )
        raise
