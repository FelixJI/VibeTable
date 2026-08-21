from __future__ import annotations

import json
import subprocess
import sys
import time

import pytest

from tests.e2e.windows_tcp_listener_owner import WindowsTcpListenerOwnerLease

pytestmark = pytest.mark.skipif(sys.platform != "win32", reason="Windows process-handle contract")


def test_real_listener_owner_handle_converges_after_the_process_exits() -> None:
    code = (
        "import json, socket, sys; "
        "listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM); "
        "listener.bind(('127.0.0.1', 0)); listener.listen(); "
        "print(json.dumps({'port': listener.getsockname()[1]}), flush=True); "
        "sys.stdin.buffer.read(1); listener.close()"
    )
    with subprocess.Popen(
        [sys.executable, "-c", code],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=False,
    ) as process:
        assert process.stdout is not None
        assert process.stdin is not None
        port = int(json.loads(process.stdout.readline())["port"])
        lease = WindowsTcpListenerOwnerLease.capture(port)
        try:
            process.stdin.write(b"x")
            process.stdin.flush()
            report = lease.observe_release(timeout=15.0)
        finally:
            owner_cleanup = lease.close()
            process.stdin.close()

    assert process.returncode == 0
    assert report.released is True
    assert report.owner_pid == report.capture_rows[0].pid
    assert report.owner_pid > 0
    assert report.decision == "captured-owner-exited"
    assert report.errors == ()
    assert owner_cleanup.stable_handle_closed is True


def test_real_listener_release_does_not_wait_for_the_still_live_owner_process() -> None:
    code = (
        "import json, socket, sys; "
        "listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM); "
        "listener.bind(('127.0.0.1', 0)); listener.listen(); "
        "print(json.dumps({'port': listener.getsockname()[1]}), flush=True); "
        "sys.stdin.buffer.read(1); listener.close(); print('closed', flush=True); "
        "sys.stdin.buffer.read(1)"
    )
    with subprocess.Popen(
        [sys.executable, "-c", code],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=False,
    ) as process:
        assert process.stdout is not None
        assert process.stdin is not None
        port = int(json.loads(process.stdout.readline())["port"])
        lease = WindowsTcpListenerOwnerLease.capture(port)
        try:
            process.stdin.write(b"x")
            process.stdin.flush()
            assert process.stdout.readline().strip() == b"closed"
            assert process.poll() is None
            started = time.monotonic()
            report = lease.observe_release(timeout=15.0)
            observation_seconds = time.monotonic() - started
        finally:
            owner_cleanup = lease.close()
            if process.poll() is None:
                process.stdin.write(b"x")
                process.stdin.flush()
            process.stdin.close()

    assert process.returncode == 0
    assert report.released is True
    assert report.owner_exited is False
    assert report.decision == "listener-released"
    assert report.errors == ()
    assert observation_seconds < 5.0
    assert owner_cleanup.stable_handle_closed is True
