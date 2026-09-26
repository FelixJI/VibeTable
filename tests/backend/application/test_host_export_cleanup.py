"""Host export grants close real streaming writers even when create replies are lost."""

from __future__ import annotations

import asyncio
from pathlib import Path

import pytest

from backend.application.export_service import ExportService
from backend.application.path_grant import PathGrantError
from backend.contracts.data_io import ExportParams
from backend.contracts.task import CreateTaskParams, ResolveGrantParams
from tests.backend.application.test_export_service import FakeQueryPort, _manifest
from tests.backend.host_files_fixture import FileTaskFixture as TaskService
from tests.backend.legacy_task_runtime import TaskRuntime
from tests.backend.path_grant_fixture import SessionPathGrantStore


@pytest.mark.parametrize("export_format", ["csv", "xlsx"])
@pytest.mark.parametrize("expired", [False, True])
async def test_revoke_joins_real_writer_without_a_create_reply(
    tmp_path: Path, export_format: str, expired: bool
):
    from openpyxl.worksheet._writer import ALL_TEMP_FILES

    clock = [0.0]
    grants = SessionPathGrantStore(clock=lambda: clock[0])
    runtime = TaskRuntime()
    service = TaskService(runtime, grants)
    target = tmp_path / f"export.{export_format}"
    target.write_bytes(b"previous complete file")
    grant = service.issue_export_target(str(target))
    entered = asyncio.Event()

    class BlockedQuery(FakeQueryPort):
        async def query_page(self, **kwargs):
            entered.set()
            await asyncio.Event().wait()
            raise AssertionError("unreachable")

    writer = ExportService(query_port=BlockedQuery([]), profiles=_manifest(), files=service.files)

    async def export(_task_id, _reporter, token, params):
        return await writer.export(
            ExportParams.model_validate(params), cancelled=lambda: token.cancelled
        )

    runtime.register("data.export", export)
    scratch_before = set(ALL_TEMP_FILES)
    # The host deliberately does not use the creation response's taskId for cleanup.
    created = await service.create_task(
        CreateTaskParams(
            kind="data.export",
            params={
                "collection": "vibetable_demo",
                "query": {},
                "format": export_format,
                "grantId": grant.grant_id,
            },
        )
    )
    await asyncio.wait_for(entered.wait(), 1)
    if expired:
        clock[0] = 100_000.0
        with pytest.raises(PathGrantError):
            grants.resolve(grant.grant_id, purpose="export_target", direction="write")
    result = await asyncio.wait_for(
        service.revoke_export_target(ResolveGrantParams(grant_id=grant.grant_id)), 1
    )
    assert result.settled is True
    assert runtime.status(created.task_id).state == "cancelled"
    assert target.read_bytes() == b"previous complete file"
    assert list(tmp_path.iterdir()) == [target]
    assert set(ALL_TEMP_FILES) == scratch_before
    assert (await service.revoke_export_target(ResolveGrantParams(grant_id=grant.grant_id))).settled
    with pytest.raises(PathGrantError):
        await service.create_task(
            CreateTaskParams(kind="data.export", params={"grantId": grant.grant_id})
        )


async def test_revoke_before_start_or_before_admission_never_runs_export(tmp_path: Path):
    service = TaskService(TaskRuntime(), SessionPathGrantStore())
    calls = []

    async def handler(*args):
        calls.append(args)
        return {}

    service.runtime.register("data.export", handler)
    first = service.issue_export_target(str(tmp_path / "first.csv"))
    await service.revoke_export_target(ResolveGrantParams(grant_id=first.grant_id))
    with pytest.raises(PathGrantError):
        await service.create_task(
            CreateTaskParams(kind="data.export", params={"grantId": first.grant_id})
        )
    second = service.issue_export_target(str(tmp_path / "second.csv"))
    created = await service.create_task(
        CreateTaskParams(kind="data.export", params={"grantId": second.grant_id})
    )
    await service.revoke_export_target(ResolveGrantParams(grant_id=second.grant_id))
    assert service.runtime.status(created.task_id).state == "cancelled"
    assert calls == []


async def test_revoke_after_commit_keeps_truthful_success_and_target(tmp_path: Path):
    service = TaskService(TaskRuntime(), SessionPathGrantStore())
    target = tmp_path / "complete.csv"
    grant = service.issue_export_target(str(target))

    async def handler(*_args):
        target.write_text("complete", encoding="utf-8")
        return {"rowsWritten": 1}

    service.runtime.register("data.export", handler)
    created = await service.create_task(
        CreateTaskParams(kind="data.export", params={"grantId": grant.grant_id})
    )
    await service.runtime.wait(created.task_id)
    await service.revoke_export_target(ResolveGrantParams(grant_id=grant.grant_id))
    assert service.runtime.status(created.task_id).state == "succeeded"
    assert target.read_text(encoding="utf-8") == "complete"


@pytest.mark.parametrize("export_format", ["csv", "xlsx"])
@pytest.mark.parametrize("cancel_at", ["write", "finishWrite"])
async def test_cooperative_cancel_respects_native_export_commit_boundary(export_format, cancel_at):
    from backend.application.host_files import HostFiles

    entered = asyncio.Event()
    release = asyncio.Event()
    previous = b"previous complete file"
    output = previous
    received = bytearray()
    finishes = []
    write_calls = 0

    async def call(action, params):
        nonlocal output, write_calls
        if action == "openWrite":
            return {"transferId": "transfer", "displayName": f"out.{export_format}"}
        if action == "write":
            import base64

            write_calls += 1
            assert params["offset"] == len(received)
            received.extend(base64.b64decode(params["base64"]))
        else:
            assert action == "finishWrite"
            finishes.append(params["commit"])
        if action == cancel_at:
            entered.set()
            await release.wait()
        if action == "write":
            return {"offset": len(received)}
        if params["commit"]:
            output = bytes(received)
        return {"committed": params["commit"], "bytes": len(received)}

    # CSV crosses the byte transport's block boundary; XLSX also exercises
    # cancellation after the final data block and before native commit.
    writer = ExportService(
        query_port=FakeQueryPort([[{"title": "x" * 300_000}]]),
        profiles=_manifest(),
        files=HostFiles(call),
    )
    runtime = TaskRuntime()

    async def export(_task_id, _reporter, token, params):
        return await writer.export(
            ExportParams.model_validate(params), cancelled=lambda: token.cancelled
        )

    runtime.register("data.export", export)
    created = await runtime.create(
        "data.export",
        {"collection": "vibetable_demo", "query": {}, "format": export_format, "grantId": "g"},
    )
    try:
        await asyncio.wait_for(entered.wait(), 2)
        await runtime.cancel(created.task_id)
        assert runtime.status(created.task_id).state == "running"
        release.set()
        result = await asyncio.wait_for(runtime.wait(created.task_id), 2)
        if cancel_at == "write":
            assert result.state == "cancelled"
            assert finishes == [False]
            assert output == previous
            assert write_calls == 1
        else:
            assert result.state == "succeeded"
            assert finishes == [True]
            assert output == bytes(received) != previous
    finally:
        release.set()
        await asyncio.wait_for(runtime.wait(created.task_id), 2)
