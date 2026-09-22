"""Worker task lifecycle; all native file grants belong to the owning Host."""

from __future__ import annotations

import asyncio

from backend.application.host_files import HostFiles
from backend.application.path_grant import PathGrantError
from backend.application.task_runtime import NotificationSink, TaskRuntime
from backend.contracts.task import (
    CreateTaskParams,
    ExportTargetSettled,
    ResolveGrantParams,
    TaskIdParams,
    TaskStatus,
)


class TaskService:
    def __init__(self, runtime: TaskRuntime, files: HostFiles) -> None:
        self._runtime = runtime
        self.files = files
        self._export_admission = asyncio.Lock()

    async def create_task(self, params: CreateTaskParams) -> TaskStatus:
        if params.kind != "data.export":
            return await self._runtime.create(params.kind, params.params)
        grant_id = params.params.get("grantId")
        if not isinstance(grant_id, str):
            raise ValueError("Export requires a grantId.")
        async with self._export_admission:
            descriptor = await self.files.describe(grant_id)
            if descriptor.purpose != "export_target" or descriptor.direction != "write":
                raise PathGrantError(
                    "Export requires a writable Host grant.", code="grant_direction_mismatch"
                )
            return await self._runtime.create(params.kind, params.params, export_grant_id=grant_id)

    async def settle_export(self, params: ResolveGrantParams) -> ExportTargetSettled:
        # The Host revokes native access before calling this worker-only join.
        async with self._export_admission:
            await self._runtime.settle_export_grant(params.grant_id)
        return ExportTargetSettled(grant_id=params.grant_id, settled=True)

    async def cancel_task(self, params: TaskIdParams) -> TaskStatus:
        return await self._runtime.cancel(params.task_id)

    async def status_task(self, params: TaskIdParams) -> TaskStatus:
        return self._runtime.status(params.task_id)

    @property
    def runtime(self) -> TaskRuntime:
        return self._runtime


def build_task_service(
    *, files: HostFiles, notification_sink: NotificationSink | None = None
) -> TaskService:
    return TaskService(TaskRuntime(notification_sink=notification_sink), files)
