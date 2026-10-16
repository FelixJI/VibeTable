from __future__ import annotations

import asyncio
import base64
import tempfile

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


async def test_spool_close_failure_stays_a_definite_precommit_abort(monkeypatch):
    """A residual spool-close failure must never follow the commit submission.

    The spool is closed before ``finishWrite(commit=True)`` is submitted, so a
    cleanup failure lands on the abort path; the task runtime may then report a
    positive not-committed receipt without downgrading a committed file. The
    seam is the real underlying file object: CPython's SpooledTemporaryFile
    ``__exit__`` closes ``_file`` directly, so the failure must surface through
    exactly that path while the synthetic broker genuinely accepts commits.
    """
    import backend.application.host_files as host_files_module

    calls = []

    class _CloseOnceBrokenFile:
        """Delegating file wrapper whose first close fails, matching a rolled
        spool whose cleanup fault only strikes at context exit."""

        def __init__(self, inner):
            self._inner = inner
            self.failed = False

        def write(self, content):
            return self._inner.write(content)

        def read(self, size=-1):
            return self._inner.read(size)

        def seek(self, offset, whence=0):
            return self._inner.seek(offset, whence)

        def tell(self):
            return self._inner.tell()

        @property
        def closed(self):
            return self._inner.closed

        def close(self):
            if not self.failed:
                self.failed = True
                raise OSError("native spool close failed")
            self._inner.close()

    class _BrokenCloseSpool(tempfile.SpooledTemporaryFile):
        def __init__(self, *args, **kwargs):
            super().__init__(*args, **kwargs)
            self._file = _CloseOnceBrokenFile(self._file)

    monkeypatch.setattr(host_files_module, "SpooledTemporaryFile", _BrokenCloseSpool)

    async def call(action, params):
        calls.append((action, params))
        if action == "openWrite":
            return {"transferId": "t", "displayName": "out.csv"}
        if action == "write":
            return {"offset": len(base64.b64decode(params["base64"]))}
        assert action == "finishWrite"
        return {"committed": params["commit"], "bytes": 3 if params["commit"] else 0}

    async def flaky_export():
        async with HostFiles(call).write("g") as target:
            target.stream.write(b"abc")

    with pytest.raises(OSError, match="native spool close"):
        await flaky_export()
    submitted = [params for _, params in calls if params.get("commit") is True]
    assert submitted == [], "the close failure must abort before the native commit submission"
    assert calls[-1] == ("finishWrite", {"transferId": "t", "commit": False})


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
