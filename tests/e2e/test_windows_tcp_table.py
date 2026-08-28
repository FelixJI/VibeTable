from __future__ import annotations

from types import SimpleNamespace
from typing import Any

import pytest

from scripts.qa import _windows_tcp_table as tcp_table


def test_query_windows_tcp_table_propagates_budget_and_parses_five_column_rows(
    monkeypatch,
) -> None:
    calls: list[tuple[list[str], float]] = []

    def run(command: list[str], **kwargs: Any) -> SimpleNamespace:
        calls.append((command, float(kwargs["timeout"])))
        return SimpleNamespace(
            returncode=0,
            stdout=(
                "TCP 127.0.0.1:9222 0.0.0.0:0 LISTENING 42\n"
                "TCP [::1]:9222 [::]:0 LISTENING 42\n"
                "TCP malformed row\n"
                "UDP 0.0.0.0:53 *:* 7\n"
            ),
        )

    monkeypatch.setattr(tcp_table.subprocess, "run", run)

    rows = tcp_table.query_windows_tcp_table(timeout=1.25)

    assert calls == [(["netstat", "-ano", "-p", "tcp"], 1.25)]
    assert rows == (
        tcp_table.WindowsTcpRow("TCP", "127.0.0.1:9222", "0.0.0.0:0", "LISTENING", 42),
        tcp_table.WindowsTcpRow("TCP", "[::1]:9222", "[::]:0", "LISTENING", 42),
    )
    assert all(row.is_listener_on(9222) for row in rows)


def test_query_windows_tcp_table_rejects_exhausted_budget_without_running_netstat(
    monkeypatch,
) -> None:
    monkeypatch.setattr(
        tcp_table.subprocess,
        "run",
        lambda *_args, **_kwargs: pytest.fail("netstat must not run without budget"),
    )

    with pytest.raises(TimeoutError, match="deadline is exhausted"):
        tcp_table.query_windows_tcp_table(timeout=0.0)


def test_query_windows_tcp_table_sanitizes_timeout_and_command_failures(
    monkeypatch,
) -> None:
    def timeout(*_args: object, **_kwargs: object) -> None:
        raise tcp_table.subprocess.TimeoutExpired(
            ["netstat", "C:\\private\\secret"],
            1.0,
        )

    monkeypatch.setattr(tcp_table.subprocess, "run", timeout)
    with pytest.raises(TimeoutError, match="exceeded its deadline") as timed_out:
        tcp_table.query_windows_tcp_table(timeout=1.0)
    assert "private" not in str(timed_out.value)

    monkeypatch.setattr(
        tcp_table.subprocess,
        "run",
        lambda *_args, **_kwargs: SimpleNamespace(
            returncode=5,
            stdout="",
            stderr="C:\\private\\secret",
        ),
    )
    with pytest.raises(OSError, match="exit code 5") as failed:
        tcp_table.query_windows_tcp_table(timeout=1.0)
    assert "private" not in str(failed.value)


@pytest.mark.parametrize("localized_state", ["侦听", "任意本地化状态"])
def test_listener_semantics_do_not_depend_on_localized_state_text(
    localized_state: str,
) -> None:
    row = tcp_table.WindowsTcpRow(
        "TCP",
        "127.0.0.1:9222",
        "0.0.0.0:0",
        localized_state,
        42,
    )

    assert row.is_listener_on(9222) is True
