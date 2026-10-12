"""Cancellation latency and actual resource drain for the production file worker."""

from __future__ import annotations

import asyncio
import io
import time
from contextlib import asynccontextmanager
from pathlib import Path
from threading import Event

import pytest

from backend.application.export_service import ExportService
from backend.application.file_work import CheckedStream, run_file_work
from backend.application.host_files import FileBuffer
from backend.application.task_runtime import CancellationToken
from backend.contracts.data_io import ExportParams, PreviewImportParams
from tests.backend.application.test_export_service import FakeQueryPort, _manifest
from tests.backend.application.test_import_service import _service
from tests.backend.host_files_fixture import LocalFilePeer


async def test_slow_preview_accepts_cancel_before_source_finishes_and_closes(tmp_path: Path):
    path = tmp_path / "slow.csv"
    path.write_text("title\n" + ("a" * 4095 + "\n") * 200, encoding="utf-8")
    token = CancellationToken()
    source = io.BytesIO(path.read_bytes())
    bytes_read = 0

    class SlowSource(io.BytesIO):
        def read(self, size=-1):
            nonlocal bytes_read
            time.sleep(0.01)
            block = super().read(min(size, 4096) if size >= 0 else 4096)
            bytes_read += len(block)
            return block

    source = SlowSource(source.getvalue())

    class Files:
        @asynccontextmanager
        async def read(self, *_args, **_kwargs):
            with source:
                yield FileBuffer(source, "slow.csv")

    service, _ = _service(path)
    service._files = Files()
    reports = []

    async def progress(done, total, message):
        reports.append((done, total, message))

    started = time.monotonic()
    task = asyncio.create_task(
        service.preview(
            PreviewImportParams(
                collection="vibetable_demo",
                grant_id="g",
                schema_revision=service._profile("vibetable_demo").capability_hash,
            ),
            cancelled=lambda: token.cancelled,
            progress=progress,
        )
    )
    await asyncio.sleep(0.035)
    accepted = time.monotonic()
    token.cancel()
    with pytest.raises(asyncio.CancelledError):
        await asyncio.wait_for(task, 1)
    drained = time.monotonic()
    print(f"preview cancel accepted={accepted - started:.3f}s drain={drained - accepted:.3f}s")
    assert accepted - started < 0.5
    assert drained - accepted < 1
    assert source.closed
    assert 0 < bytes_read < path.stat().st_size
    assert reports
    assert all(total == 0 for _, total, _ in reports)


@pytest.mark.parametrize("fmt", ["csv", "xlsx"])
async def test_slow_encoding_cancel_joins_before_releasing_target(tmp_path, monkeypatch, fmt):
    from openpyxl.worksheet._writer import ALL_TEMP_FILES

    import backend.application.export_service as module

    path = tmp_path / f"out.{fmt}"
    path.write_bytes(b"previous")
    peer = LocalFilePeer()
    grant = peer.grants.issue(path=str(path), purpose="export_target", direction="write")
    token = CancellationToken()
    render = module._render_cell
    rendered = 0

    def slow_render(row, column):
        nonlocal rendered
        rendered += 1
        time.sleep(0.008)
        return render(row, column)

    monkeypatch.setattr(module, "_render_cell", slow_render)
    service = ExportService(
        query_port=FakeQueryPort([[{"title": "test"}] * 100, []]),
        profiles=_manifest(),
        files=peer.files,
    )
    temporary = set(ALL_TEMP_FILES)
    started = time.monotonic()
    task = asyncio.create_task(
        service.export(
            ExportParams(
                collection="vibetable_demo", query={}, format=fmt, grant_id=grant.grant_id
            ),
            cancelled=lambda: token.cancelled,
        )
    )
    await asyncio.sleep(0.035)
    accepted = time.monotonic()
    token.cancel()
    with pytest.raises(asyncio.CancelledError):
        await asyncio.wait_for(task, 1)
    drained = time.monotonic()
    print(f"{fmt} cancel accepted={accepted - started:.3f}s drain={drained - accepted:.3f}s")
    assert accepted - started < 0.5
    assert drained - accepted < 1
    assert rendered < 100
    assert path.read_bytes() == b"previous"
    assert all(stream.closed for _, stream in peer.transfers.values())
    assert set(ALL_TEMP_FILES) == temporary


async def test_hard_cancel_waits_for_thread_exit_before_stream_owner_can_close():
    entered, release, exited = Event(), Event(), Event()
    source = io.BytesIO(b"safe")

    def work(check, _report):
        entered.set()
        try:
            assert release.wait(1)
            assert not source.closed
            check()
        finally:
            exited.set()

    task = asyncio.create_task(run_file_work(work))
    assert await asyncio.to_thread(entered.wait, 1)
    task.cancel()
    await asyncio.sleep(0)
    task.cancel()
    assert not task.done()
    assert not exited.is_set()
    release.set()
    with pytest.raises(asyncio.CancelledError):
        await task
    assert exited.is_set()
    source.close()


async def test_xlsx_save_cancel_discards_staging_and_joins(tmp_path, monkeypatch):
    from openpyxl.worksheet._writer import ALL_TEMP_FILES

    path = tmp_path / "out.xlsx"
    path.write_bytes(b"previous")
    peer = LocalFilePeer()
    grant = peer.grants.issue(path=str(path), purpose="export_target", direction="write")
    token = CancellationToken()
    entered = Event()
    write = CheckedStream.write

    def slow_write(self, data):
        entered.set()
        time.sleep(0.01)
        return write(self, data)

    monkeypatch.setattr(CheckedStream, "write", slow_write)
    service = ExportService(
        query_port=FakeQueryPort([[{"title": "test"}] * 20]),
        profiles=_manifest(),
        files=peer.files,
    )
    temporary = set(ALL_TEMP_FILES)
    task = asyncio.create_task(
        service.export(
            ExportParams(
                collection="vibetable_demo", query={}, format="xlsx", grant_id=grant.grant_id
            ),
            cancelled=lambda: token.cancelled,
        )
    )
    assert await asyncio.to_thread(entered.wait, 1)
    accepted = time.monotonic()
    token.cancel()
    with pytest.raises(asyncio.CancelledError):
        await asyncio.wait_for(task, 1)
    print(f"xlsx save cancel drain={time.monotonic() - accepted:.3f}s")
    assert path.read_bytes() == b"previous"
    assert all(stream.closed for _, stream in peer.transfers.values())
    assert set(ALL_TEMP_FILES) == temporary


async def test_cancel_during_unconfirmed_export_commit_reports_unknown_not_cancelled():
    from backend.application.host_files import HostFiles
    from backend.application.task_runtime import HostExecutionRuntime

    terminal = asyncio.get_running_loop().create_future()
    runtime = None

    async def call(action, _params):
        if action == "openWrite":
            return {"transferId": "t", "displayName": "out.csv"}
        assert action == "finishWrite"
        assert _params["commit"]
        await runtime.cancel("t")
        raise ConnectionError("commit reply lost")

    async def report(status):
        if status.state not in {"queued", "running"}:
            terminal.set_result(status)

    runtime = HostExecutionRuntime(notification_sink=report)

    async def export(_id, _reporter, token, _params):
        async with HostFiles(call).write("g", cancelled=lambda: token.cancelled):
            pass

    runtime.register("data.export", export)
    await runtime.start("t", "data.export", {})
    result = await asyncio.wait_for(terminal, 1)
    assert result.state == "failed"
    assert "unknown" in result.error
    assert not runtime._handles
