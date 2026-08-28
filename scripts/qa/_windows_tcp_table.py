"""One sanitized Windows TCP-table query and row model for QA evidence."""

from __future__ import annotations

import ipaddress
import subprocess
from dataclasses import dataclass

__all__ = ["WindowsTcpRow", "query_windows_tcp_table"]


def _endpoint_parts(endpoint: str) -> tuple[str, int] | None:
    separator = endpoint.rfind(":")
    if separator < 0:
        return None
    host = endpoint[:separator]
    if host.startswith("[") and host.endswith("]"):
        host = host[1:-1]
    try:
        port = int(endpoint[separator + 1 :])
    except ValueError:
        return None
    return host, port


@dataclass(frozen=True)
class WindowsTcpRow:
    protocol: str
    local: str
    remote: str
    state: str
    pid: int

    def as_artifact(self) -> dict[str, object]:
        return {
            "protocol": self.protocol,
            "local": self.local,
            "remote": self.remote,
            "state": self.state,
            "pid": self.pid,
        }

    def is_listener_on(self, port: int) -> bool:
        local = _endpoint_parts(self.local)
        remote = _endpoint_parts(self.remote)
        if local is None or remote is None:
            return False
        try:
            remote_is_unspecified = ipaddress.ip_address(remote[0]).is_unspecified
        except ValueError:
            return False
        return (
            self.protocol.casefold() == "tcp"
            and local[1] == port
            and remote[1] == 0
            and remote_is_unspecified
        )


def query_windows_tcp_table(*, timeout: float) -> tuple[WindowsTcpRow, ...]:
    """Return the current five-column Windows TCP table within ``timeout`` seconds."""

    if timeout <= 0:
        raise TimeoutError("Windows TCP-table query deadline is exhausted")
    try:
        completed = subprocess.run(
            ["netstat", "-ano", "-p", "tcp"],
            check=False,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=timeout,
        )
    except subprocess.TimeoutExpired as exc:
        raise TimeoutError("Windows TCP-table query exceeded its deadline") from exc
    except (OSError, subprocess.SubprocessError) as exc:
        raise OSError("unable to query the Windows TCP table") from exc
    if completed.returncode != 0:
        raise OSError(f"Windows TCP-table query failed with exit code {completed.returncode}")

    rows: list[WindowsTcpRow] = []
    for raw_line in completed.stdout.splitlines():
        parts = raw_line.split()
        if len(parts) != 5 or parts[0].casefold() != "tcp":
            continue
        try:
            pid = int(parts[4])
        except ValueError:
            continue
        rows.append(
            WindowsTcpRow(
                protocol="TCP",
                local=parts[1],
                remote=parts[2],
                state=parts[3].upper(),
                pid=pid,
            )
        )
    return tuple(rows)
