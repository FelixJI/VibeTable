"""Local file peer for format/Go integration tests, never a production fallback.

Native authorization and epoch contracts are tested against HostSessionFileBroker
in the desktop tests. This peer lets the existing worker format corpus exercise
its byte-only HostFiles port without launching a WPF application for each row.
"""

from __future__ import annotations

import base64
import io
import uuid
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

from backend.application.host_files import FileBuffer, HostFiles
from backend.application.task_runtime import TaskRuntime
from backend.application.task_service import TaskService
from backend.contracts.task import SessionPathGrant
from tests.backend.path_grant_fixture import SessionPathGrantStore


class LocalFilePeer:
    def __init__(self, grants: SessionPathGrantStore | None = None) -> None:
        self.grants = grants or SessionPathGrantStore()
        self.files = HostFiles(self.call)
        self.transfers: dict[str, tuple[str, io.BytesIO]] = {}
        self.reservations: dict[str, Any] = {}
        self.runs: dict[str, str] = {}

    async def call(self, action: str, params: dict[str, Any]) -> dict[str, Any]:
        if action == "describe":
            return self.grants.descriptor(params["grantId"]).model_dump(mode="json", by_alias=True)
        if action in {"openRead", "openWrite"}:
            grant = params["grantId"]
            if params.get("runId") != self.runs.get(grant):
                raise ValueError("grant does not belong to this run")
            write = action == "openWrite"
            path = self.grants.resolve(
                grant,
                purpose="export_target" if write else "import_source",
                direction="write" if write else "read",
            )
            transfer = uuid.uuid4().hex
            stream = io.BytesIO() if write else io.BytesIO(Path(path).read_bytes())
            self.transfers[transfer] = (grant, stream)
            return {"transferId": transfer, "displayName": Path(path).name}
        if action == "reserveImport":
            reservation = self.grants.reserve(
                params["grantId"], purpose="import_source", direction="read"
            )
            commit = reservation.__enter__()
            key = uuid.uuid4().hex
            self.reservations[key] = (reservation, commit)
            return {"reservationId": key}
        if action == "settleImport":
            reservation, commit = self.reservations.pop(params["reservationId"])
            if params["outcome"] == "consumed":
                commit()
            reservation.__exit__(None, None, None)
            return {"outcome": params["outcome"]}
        grant, stream = self.transfers[params["transferId"]]
        if action == "read":
            block = stream.read(params["maxBytes"])
            return {"base64": base64.b64encode(block).decode(), "eof": not block}
        if action == "write":
            assert params["offset"] == stream.tell()
            stream.write(base64.b64decode(params["base64"]))
            return {"offset": stream.tell()}
        if action == "finishWrite":
            size = len(stream.getvalue())
            if params["commit"]:
                path = self.grants.resolve(grant, purpose="export_target", direction="write")
                Path(path).write_bytes(stream.getvalue())
                self.grants.consume(grant)
            stream.close()
            return {"committed": params["commit"], "bytes": size}
        assert action == "closeRead"
        if grant in self.runs:
            self.grants.consume(grant)
        stream.close()
        return {"closed": True}


class FileTaskFixture(TaskService):
    def __init__(
        self, runtime: TaskRuntime | None = None, grants: SessionPathGrantStore | None = None
    ):
        self.peer = LocalFilePeer(grants)
        super().__init__(runtime or TaskRuntime(), self.peer.files)

    @property
    def grants(self):
        return self.peer.grants

    def issue_import_source(self, path: str, *, size_bytes: int | None = None) -> SessionPathGrant:
        return self.grants.issue(
            path=path, purpose="import_source", direction="read", size_bytes=size_bytes
        )

    def issue_export_target(self, path: str) -> SessionPathGrant:
        return self.grants.issue(path=path, purpose="export_target", direction="write")

    async def register_host_import_source(self, params):
        return self.issue_import_source(params.path, size_bytes=params.size_bytes)

    async def register_host_export_target(self, params):
        return self.issue_export_target(params.path)

    async def resolve_grant(self, params):
        return self.grants.descriptor(params.grant_id)

    async def revoke_export_target(self, params):
        self.grants.revoke(params.grant_id)
        return await self.settle_export(params)


def file_task_fixture(*, notification_sink=None):
    return FileTaskFixture(TaskRuntime(notification_sink=notification_sink))


class FormatFiles(HostFiles):
    """Format-only port; does not model native authorization or epoch state."""

    def __init__(self, path: str | Path, consumed: list[str] | None = None):
        self.path = Path(path)
        self.consumed = consumed

    @asynccontextmanager
    async def read(self, grant_id, *, run_id=None):
        with io.BytesIO(self.path.read_bytes()) as stream:
            yield FileBuffer(stream, self.path.name)

    @asynccontextmanager
    async def write(self, grant_id, *, run_id=None):
        with io.BytesIO() as stream:
            yield FileBuffer(stream, self.path.name)
            self.path.write_bytes(stream.getvalue())

    @asynccontextmanager
    async def reserve_import(self, grant_id, plan_token):
        def commit():
            if self.consumed is not None:
                self.consumed.append(grant_id)

        yield commit
