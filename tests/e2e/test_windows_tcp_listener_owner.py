from __future__ import annotations

from collections.abc import Iterable
from typing import Any

import pytest

from tests.e2e.windows_tcp_listener_owner import (
    TcpListenerRow,
    _capture_with_adapter,
)


class _ManualTime:
    def __init__(self) -> None:
        self.value = 0.0

    def monotonic(self) -> float:
        return self.value

    def advance(self, seconds: float) -> None:
        self.value += seconds


class _FakeOwnerHandle:
    pid = 42
    name = "msedgewebview2.exe"

    def __init__(
        self,
        *,
        exited: bool | BaseException | Iterable[bool | BaseException],
        name: str = "msedgewebview2.exe",
        manual_time: _ManualTime | None = None,
        wait_elapsed: float = 0.0,
        close_error: BaseException | None = None,
    ) -> None:
        self._exited = exited if isinstance(exited, (bool, BaseException)) else iter(exited)
        self.name = name
        self._manual_time = manual_time
        self._wait_elapsed = wait_elapsed
        self._close_error = close_error
        self.wait_timeouts: list[float] = []
        self.closed = False

    def wait(self, timeout: float) -> bool:
        self.wait_timeouts.append(timeout)
        if timeout > 0 and self._manual_time is not None:
            self._manual_time.advance(min(timeout, self._wait_elapsed))
        result = (
            self._exited if isinstance(self._exited, (bool, BaseException)) else next(self._exited)
        )
        if isinstance(result, BaseException):
            raise result
        return result

    def close(self) -> None:
        self.closed = True
        if self._close_error is not None:
            raise self._close_error


class _FakeListenerAdapter:
    def __init__(
        self,
        *,
        captures: tuple[tuple[TcpListenerRow, ...] | BaseException, ...],
        owner: _FakeOwnerHandle,
        manual_time: _ManualTime | None = None,
        query_elapsed: tuple[float, ...] = (),
    ) -> None:
        self._captures = iter(captures)
        self._query_elapsed = iter(query_elapsed)
        self.owner = owner
        self._manual_time = manual_time
        self.opened_pids: list[int] = []
        self.query_timeouts: list[float] = []

    def query_listeners(self, port: int, *, timeout: float) -> tuple[TcpListenerRow, ...]:
        assert port == 9222
        self.query_timeouts.append(timeout)
        if self._manual_time is not None:
            self._manual_time.advance(next(self._query_elapsed, 0.0))
        result = next(self._captures)
        if isinstance(result, BaseException):
            raise result
        return result

    def open_owner(self, pid: int) -> Any:
        self.opened_pids.append(pid)
        return self.owner


def _listener(pid: int) -> TcpListenerRow:
    return TcpListenerRow(
        protocol="TCP",
        local="127.0.0.1:9222",
        remote="0.0.0.0:0",
        state="LISTENING",
        pid=pid,
    )


def _ipv6_listener(pid: int) -> TcpListenerRow:
    return TcpListenerRow(
        protocol="TCP",
        local="[::1]:9222",
        remote="[::]:0",
        state="LISTENING",
        pid=pid,
    )


def test_exited_captured_owner_does_not_claim_a_reused_numeric_port() -> None:
    owner = _FakeOwnerHandle(exited=(False, True))
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),), (_listener(99),)),
        owner=owner,
    )

    with _capture_with_adapter(9222, adapter) as lease:
        report = lease.observe_release(timeout=4.5)

    assert report.released is True
    assert report.as_artifact() == {
        "owner": {
            "pid": 42,
            "processName": "msedgewebview2.exe",
            "identityVerified": True,
            "exited": True,
        },
        "captureRows": [
            {
                "protocol": "TCP",
                "local": "127.0.0.1:9222",
                "remote": "0.0.0.0:0",
                "state": "LISTENING",
                "pid": 42,
                "ownership": "captured-owner",
            }
        ],
        "releaseRows": [
            {
                "protocol": "TCP",
                "local": "127.0.0.1:9222",
                "remote": "0.0.0.0:0",
                "state": "LISTENING",
                "pid": 99,
                "ownership": "unowned",
            }
        ],
        "decision": "captured-owner-exited-port-reused-unowned",
        "released": True,
        "errors": [],
    }
    assert adapter.opened_pids == [42]
    assert owner.wait_timeouts == [0.0, 0.0]
    assert owner.closed is True


def test_signaled_owner_handle_defeats_same_numeric_pid_reuse() -> None:
    owner = _FakeOwnerHandle(exited=(False, True))
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),), (_listener(42),)),
        owner=owner,
    )

    with _capture_with_adapter(9222, adapter) as lease:
        report = lease.observe_release(timeout=1.0)

    artifact = report.as_artifact()
    release_rows = artifact["releaseRows"]
    assert isinstance(release_rows, list)
    assert release_rows
    release_row = release_rows[0]
    assert isinstance(release_row, dict)
    assert report.released is True
    assert report.decision == "captured-owner-exited-port-reused-unowned"
    assert release_row["ownership"] == "unowned"


def test_live_captured_owner_that_still_listens_fails_release() -> None:
    owner = _FakeOwnerHandle(exited=False)
    adapter = _FakeListenerAdapter(
        captures=(
            (_listener(42),),
            (_listener(42),),
            (_listener(42),),
            (_listener(42),),
        ),
        owner=owner,
    )

    with _capture_with_adapter(9222, adapter) as lease:
        report = lease.observe_release(timeout=3.0)

    assert report.released is False
    assert report.decision == "captured-owner-still-listening"
    assert report.errors == ()
    assert owner.wait_timeouts == [0.0, 0.0, 3.0]


def test_live_captured_owner_with_no_current_listener_releases_without_waiting() -> None:
    owner = _FakeOwnerHandle(exited=False)
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),), ()),
        owner=owner,
    )

    with _capture_with_adapter(9222, adapter) as lease:
        report = lease.observe_release(timeout=3.0)

    assert report.released is True
    assert report.decision == "listener-released"
    assert report.owner_exited is False
    assert owner.wait_timeouts == [0.0, 0.0]
    assert len(adapter.query_timeouts) == 3


def test_owned_listener_that_remains_alive_until_the_deadline_fails_closed() -> None:
    manual_time = _ManualTime()
    owner = _FakeOwnerHandle(
        exited=False,
        manual_time=manual_time,
        wait_elapsed=3.0,
    )
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),), (_listener(42),)),
        owner=owner,
        manual_time=manual_time,
    )

    with _capture_with_adapter(9222, adapter, monotonic=manual_time.monotonic) as lease:
        report = lease.observe_release(timeout=3.0)

    assert owner.wait_timeouts == [0.0, 0.0, 3.0]
    assert len(adapter.query_timeouts) == 3
    assert manual_time.value == 3.0
    assert report.released is False
    assert report.decision == "release-observation-budget-exhausted"


def test_release_query_error_fails_closed_in_the_report() -> None:
    owner = _FakeOwnerHandle(exited=(False, True))
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),), OSError("query denied")),
        owner=owner,
    )

    with _capture_with_adapter(9222, adapter) as lease:
        report = lease.observe_release(timeout=2.0)

    assert report.released is False
    assert report.decision == "release-listener-query-failed"
    assert report.release_rows == ()
    assert report.errors == ("unable to query the CDP listener at release (OSError)",)


def test_unknown_owner_handle_state_fails_closed_even_when_the_port_table_is_empty() -> None:
    owner = _FakeOwnerHandle(
        exited=(False, OSError("C:\\private\\owner-handle denied")),
        name="C:\\private\\msedgewebview2.exe",
    )
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),), ()),
        owner=owner,
    )

    with _capture_with_adapter(9222, adapter) as lease:
        report = lease.observe_release(timeout=1.0)

    assert report.released is False
    assert report.decision == "captured-owner-state-unknown"
    assert report.release_rows == ()
    assert report.errors == ("unable to observe the captured CDP owner (OSError)",)
    artifact = report.as_artifact()
    assert artifact["owner"] == {
        "pid": 42,
        "processName": "msedgewebview2.exe",
        "identityVerified": True,
        "exited": None,
    }
    assert "private" not in str(artifact)


def test_capture_accepts_multiple_listener_rows_owned_by_one_pid() -> None:
    owner = _FakeOwnerHandle(exited=(False, True))
    dual_stack = (_listener(42), _ipv6_listener(42))
    adapter = _FakeListenerAdapter(captures=(dual_stack, dual_stack, ()), owner=owner)

    with _capture_with_adapter(9222, adapter) as lease:
        report = lease.observe_release(timeout=1.0)

    assert report.released is True
    assert report.capture_rows == dual_stack
    assert adapter.opened_pids == [42]


def test_capture_rejects_an_owner_handle_that_signaled_during_identity_capture() -> None:
    owner = _FakeOwnerHandle(exited=True)
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),)),
        owner=owner,
    )

    with pytest.raises(RuntimeError, match="exited while its identity was captured"):
        _capture_with_adapter(9222, adapter)

    assert owner.wait_timeouts == [0.0]
    assert owner.closed is True


@pytest.mark.parametrize(
    "capture_rows",
    [
        (),
        (_listener(42), _listener(99)),
    ],
)
def test_capture_requires_exactly_one_listener_owner_before_opening_a_handle(
    capture_rows: tuple[TcpListenerRow, ...],
) -> None:
    owner = _FakeOwnerHandle(exited=False)
    adapter = _FakeListenerAdapter(captures=(capture_rows,), owner=owner)

    with pytest.raises(RuntimeError, match="expected one TCP listener owner"):
        _capture_with_adapter(9222, adapter)

    assert adapter.opened_pids == []
    assert owner.closed is False


def test_capture_closes_the_handle_when_the_second_query_has_a_different_owner() -> None:
    owner = _FakeOwnerHandle(exited=False)
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(99),)),
        owner=owner,
    )

    with pytest.raises(RuntimeError, match="owner changed"):
        _capture_with_adapter(9222, adapter)

    assert adapter.opened_pids == [42]
    assert owner.wait_timeouts == []
    assert owner.closed is True


def test_owner_wait_and_release_query_share_one_deadline_and_fail_closed_if_query_overruns() -> (
    None
):
    manual_time = _ManualTime()
    owner = _FakeOwnerHandle(
        exited=(False, False, True),
        manual_time=manual_time,
        wait_elapsed=1.5,
    )
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),), (_listener(42),), ()),
        owner=owner,
        manual_time=manual_time,
        query_elapsed=(0.0, 0.0, 0.0, 2.0),
    )

    with _capture_with_adapter(9222, adapter, monotonic=manual_time.monotonic) as lease:
        report = lease.observe_release(timeout=3.0)

    assert owner.wait_timeouts == [0.0, 0.0, 3.0]
    assert adapter.query_timeouts[-1] == 1.5
    assert manual_time.value == 3.5
    assert report.released is False
    assert report.decision == "release-observation-budget-exhausted"
    assert report.errors == ("CDP listener release observation exceeded its deadline",)


def test_exhausted_release_budget_fails_closed_without_waiting_or_querying() -> None:
    owner = _FakeOwnerHandle(exited=(False, True))
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),)),
        owner=owner,
    )

    with _capture_with_adapter(9222, adapter) as lease:
        report = lease.observe_release(timeout=0.0)

    assert owner.wait_timeouts == [0.0]
    assert len(adapter.query_timeouts) == 2
    assert report.released is False
    assert report.owner_exited is None
    assert report.decision == "release-observation-budget-exhausted"


def test_lease_cleanup_failure_does_not_replace_the_primary_error() -> None:
    owner = _FakeOwnerHandle(
        exited=False,
        close_error=OSError("C:\\private\\handle close denied"),
    )
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),)),
        owner=owner,
    )

    with (
        pytest.raises(ValueError, match="primary failure") as raised,
        _capture_with_adapter(9222, adapter),
    ):
        raise ValueError("primary failure")

    assert owner.closed is True
    assert raised.value.__notes__ == ["unable to close captured CDP owner handle (OSError)"]
    assert "private" not in str(raised.value.__notes__)


def test_lease_close_failure_is_cached_as_a_sanitized_report() -> None:
    owner = _FakeOwnerHandle(
        exited=False,
        close_error=OSError("C:\\private\\handle close denied"),
    )
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),)),
        owner=owner,
    )

    lease = _capture_with_adapter(9222, adapter)
    cleanup = lease.close()

    assert cleanup.as_artifact() == {
        "stableHandleClosed": False,
        "errors": ["unable to close captured CDP owner handle (OSError)"],
        "status": "failed",
    }
    assert "private" not in str(cleanup.as_artifact())


def test_unconsumed_context_manager_close_failure_fails_closed() -> None:
    owner = _FakeOwnerHandle(
        exited=False,
        close_error=OSError("C:\\private\\handle close denied"),
    )
    adapter = _FakeListenerAdapter(
        captures=((_listener(42),), (_listener(42),)),
        owner=owner,
    )

    with (
        pytest.raises(
            RuntimeError,
            match="unable to close captured CDP owner handle",
        ) as raised,
        _capture_with_adapter(9222, adapter),
    ):
        pass

    assert "private" not in str(raised.value)
