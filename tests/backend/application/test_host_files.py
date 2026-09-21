from __future__ import annotations

import asyncio
import base64

import pytest

from backend.application.host_files import HostFiles


async def test_read_returns_seekable_spool_and_never_requests_original_path():
    calls = []
    blocks = [b"a,b\n1,2\n", b""]

    async def call(action, params):
        calls.append((action, params))
        if action == "openRead":
            return {"transferId": "t", "displayName": "输入.csv"}
        if action == "read":
            value = blocks.pop(0)
            return {"base64": base64.b64encode(value).decode(), "eof": not value}
        assert action == "closeRead"
        return {"closed": True}

    async with HostFiles(call).read("g") as source:
        assert source.display_name == "输入.csv"
        assert source.stream.read() == b"a,b\n1,2\n"
        source.stream.seek(0)
        assert source.stream.read(3) == b"a,b"
    assert calls[0] == ("openRead", {"grantId": "g"})
    assert calls[-1] == ("closeRead", {"transferId": "t"})
    assert source.stream.closed


async def test_cancel_after_commit_submission_observes_success_before_returning():
    submitted = asyncio.Event()
    release = asyncio.Event()
    calls = []

    async def call(action, params):
        calls.append((action, params))
        if action == "openWrite":
            return {"transferId": "t", "displayName": "out.csv"}
        if action == "write":
            return {"offset": 3}
        assert action == "finishWrite"
        assert params["commit"]
        submitted.set()
        await release.wait()
        return {"committed": True, "bytes": 3}

    async def export():
        async with HostFiles(call).write("g") as target:
            target.stream.write(b"abc")
        return "succeeded"

    task = asyncio.create_task(export())
    try:
        await asyncio.wait_for(submitted.wait(), 2)
        task.cancel()
        await asyncio.sleep(0)
        assert not task.done()
        release.set()
        assert await asyncio.wait_for(task, 2) == "succeeded"
        assert len([action for action, _ in calls if action == "finishWrite"]) == 1
    finally:
        release.set()
        await asyncio.gather(task, return_exceptions=True)


async def test_failed_writer_aborts_without_submitting_partial_output():
    calls = []

    async def call(action, params):
        calls.append((action, params))
        return {"transferId": "t", "displayName": "out.csv"}

    async def failing_export():
        async with HostFiles(call).write("g") as target:
            target.stream.write(b"partial")
            raise ValueError("format failure")

    with pytest.raises(ValueError, match="format"):
        await failing_export()
    assert calls == [
        ("openWrite", {"grantId": "g"}),
        ("finishWrite", {"transferId": "t", "commit": False}),
    ]


async def test_lost_import_settlement_reply_never_releases_committed_reservation():
    calls = []

    async def call(action, params):
        calls.append((action, params))
        if action == "reserveImport":
            return {"reservationId": "r"}
        raise RuntimeError("lost reply")

    async with HostFiles(call).reserve_import("g", "plan") as committed:
        committed()
    assert calls[-1] == ("settleImport", {"reservationId": "r", "outcome": "consumed"})
    assert len(calls) == 2


async def test_uncommitted_import_settlement_error_still_propagates():
    async def call(action, params):
        if action == "reserveImport":
            return {"reservationId": "r"}
        assert params["outcome"] == "released"
        raise RuntimeError("release failed")

    async def release():
        async with HostFiles(call).reserve_import("g", "plan"):
            pass

    with pytest.raises(RuntimeError, match="release failed"):
        await release()
